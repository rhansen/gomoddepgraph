package gomoddepgraph

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"slices"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph/internal/itertools"
	"github.com/rhansen/gomoddepgraph/internal/syncmap"
	"golang.org/x/mod/modfile"
	"golang.org/x/sync/errgroup"
)

// RequirementsComplete returns a [RequirementGraph] of the complete transitive closure of
// requirements in each module's go.mod.  Unlike [RequirementsGo], no requirements are [pruned].
// Any go.mod directives that might affect the requirement graph are ignored (specifically,
// [replace] and [exclude]).
//
// For complex modules this produces a much bigger graph than [RequirementsGo] because it does not
// prune any requirements.  Operations on the returned graph can take a considerable amount of time
// because they must download and process metadata for many more modules.  The size of the graph can
// be reduced by [UnifyRequirements] (although reproducibility is impacted).
//
// Canceling the provided [context.Context] or calling the returned cancel function frees some
// resources.  Once canceled, in-progress and future calls to [RequirementGraph.Load] might fail.
//
// [pruned]: https://go.dev/ref/mod#graph-pruning
// [replace]: https://go.dev/ref/mod#go-mod-file-replace
// [exclude]: https://go.dev/ref/mod#go-mod-file-exclude
func RequirementsComplete(ctx context.Context, rootId ModuleId) (RequirementGraph, context.CancelFunc, error) {
	if err := rootId.Check(); err != nil {
		return nil, func() {}, err
	}
	gr, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	rg := &requirementGraphComplete{
		root: requirement{rootId},
		ctx:  ctx,
		gr:   gr,
		qCh:  make(chan *loadQ),
	}
	gr.Go(func() error { return rg.batchify(ctx) })
	return rg, cancel, nil
}

type requirementGraphComplete struct {
	root    Requirement
	immReqs syncmap.Map[Requirement, func() (*requirementGraphReqs, error)]
	ctx     context.Context
	gr      *errgroup.Group
	qCh     chan *loadQ
}

var _ RequirementGraph = (*requirementGraphComplete)(nil)

func (rg *requirementGraphComplete) Root() Requirement {
	return rg.root
}

func (rg *requirementGraphComplete) Req(mId ModuleId) Requirement {
	if err := mId.Check(); err != nil {
		panic(err)
	}
	return requirement{mId}
}

func (rg *requirementGraphComplete) Load(ctx context.Context, m Requirement) error {
	for {
		fn, loaded := rg.immReqs.LoadOrStore(m,
			sync.OnceValues(func() (*requirementGraphReqs, error) { return rg.load(ctx, m.Id()) }))
		if _, err := fn(); err == nil {
			return nil
		} else if !loaded {
			// Allow a future (or concurrent) call to retry.
			rg.immReqs.Delete(m)
			return err
		}
		// The other call to Load that stored the [sync.Once] will delete the failed entry, allowing
		// this invocation to retry.  Yield to the scheduler to give the other goroutine an opportunity
		// to run before retrying.
		runtime.Gosched()
	}
}

func (rg *requirementGraphComplete) DirectReqs(m Requirement) iter.Seq[Requirement] {
	return mapset.Elements(rg.reqs(m).d)
}

func (rg *requirementGraphComplete) ImmediateIndirectReqs(m Requirement) iter.Seq[Requirement] {
	return mapset.Elements(rg.reqs(m).i)
}

func (rg *requirementGraphComplete) reqs(m Requirement) *requirementGraphReqs {
	fn, _ := rg.immReqs.Load(m)
	if fn == nil {
		panic(fmt.Errorf("module %v not yet loaded", m))
	}
	reqs, err := fn()
	if err != nil {
		panic(fmt.Errorf("previous load of module %v failed; got error %w", m, err))
	}
	return reqs
}

func (rg *requirementGraphComplete) load(ctx context.Context, mId ModuleId) (*requirementGraphReqs, error) {
	if err := mId.Check(); err != nil {
		return nil, err
	}
	ch := make(chan *loadR)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-rg.ctx.Done():
		return nil, fmt.Errorf("RequirementsComplete context: %w", context.Cause(rg.ctx))
	case rg.qCh <- &loadQ{ctx: ctx, mId: mId, ch: ch}:
	}
	var r *loadR
	// batchify will send a result even if its context is canceled so there's no need to include
	// rg.ctx.Done() in this select.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r = <-ch:
	}
	if r.err != nil {
		return nil, r.err
	}
	md := r.md
	if md.Path != mId.Path {
		return nil, fmt.Errorf("module path mismatch; got %v, want %v", md.Path, mId.Path)
	}
	if md.Version != mId.Version {
		return nil, fmt.Errorf("module %v version mismatch; got %v, want %v",
			mId.Path, md.Version, mId.Version)
	}
	// md.GoMod might have been synthesized by $GOPROXY.
	goModData, err := os.ReadFile(md.GoMod)
	if err != nil {
		return nil, err
	}
	goMod, err := modfile.ParseLax(md.GoMod, goModData, nil)
	if err != nil {
		return nil, err
	}
	reqs := &requirementGraphReqs{
		d: mapset.NewThreadUnsafeSet[Requirement](),
		i: mapset.NewThreadUnsafeSet[Requirement](),
	}
	for _, r := range goMod.Require {
		rs := reqs.d
		if r.Indirect {
			rs = reqs.i
		}
		rs.Add(requirement{ModuleId{r.Mod}})
	}
	return reqs, nil
}

func (rg *requirementGraphComplete) batchify(ctx context.Context) error {
	var qCh <-chan *loadQ = rg.qCh
	batChOrig := make(chan map[ModuleId]*loadQ)
	var batCh chan<- map[ModuleId]*loadQ
	bat := map[ModuleId]*loadQ{}
	defer func() {
		for mId := range bat {
			err := fmt.Errorf("RequirementsComplete context: %w", ctx.Err())
			rg.sendResult(mId, bat, &loadR{err: err})
		}
	}()
	const concurrency = 1
	concurrencyLimiter := make(chan struct{}, concurrency)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case q := <-qCh:
			bat[q.mId] = q
			batCh = batChOrig
			// Avoid hitting ARG_MAX.
			const maxBatchSize = 500
			if len(bat) >= maxBatchSize {
				qCh = nil
			}
		case batCh <- bat:
			bat = map[ModuleId]*loadQ{}
			batCh = nil
			qCh = rg.qCh
		case concurrencyLimiter <- struct{}{}:
			rg.gr.Go(func() error {
				defer func() { <-concurrencyLimiter }()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case bat := <-batChOrig:
					rg.loadBatch(ctx, bat)
				}
				return nil
			})
		}
	}
}

func (rg *requirementGraphComplete) loadBatch(ctx context.Context, bat map[ModuleId]*loadQ) {
	defer func() {
		for mId := range bat {
			err := fmt.Errorf("batch metadata lookup missing results for %v", mId)
			rg.sendResult(mId, bat, &loadR{err: err})
		}
	}()
	for mId, q := range bat {
		if q.ctx.Err() != nil {
			// No point in sending an error result; it won't receive it (except if a select race is lost).
			delete(bat, mId)
		}
	}
	if len(bat) == 0 {
		return
	}
	lsIter, done := goListM(ctx, "/", slices.Collect(itertools.Stringify(maps.Keys(bat)))...)
	defer func() {
		if err := done(); err != nil {
			slog.ErrorContext(ctx, "`go list -m` failed", "err", err)
		}
	}()
	for md := range lsIter {
		slog.DebugContext(ctx, "read module metadata from Go", "metadata", md)
		mId := NewModuleId(md.Path, md.Version)
		rg.sendResult(mId, bat, &loadR{md: md})
	}
}

func (rg *requirementGraphComplete) sendResult(mId ModuleId, bat map[ModuleId]*loadQ, r *loadR) {
	q := bat[mId]
	delete(bat, mId)
	if q == nil {
		slog.Error("unexpected metadata lookup result", "module", mId)
		return
	}
	go func() {
		select {
		case <-q.ctx.Done():
			// Don't close q.ch because the receiver might select the closed q.ch instead of the closed
			// ctx.Done() channel.
		case q.ch <- r:
		}
	}()
}

type loadQ struct {
	ctx context.Context
	mId ModuleId
	ch  chan<- *loadR
}

type loadR struct {
	md  *jsonMetadata
	err error
}
