package gomoddepgraph

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph/internal/command"
	"github.com/rhansen/gomoddepgraph/internal/logging"
	"golang.org/x/sync/errgroup"
)

// A requirementGraphGo is a [RequirementGraph] that builds the graph by parsing the output of `go
// mod graph`.
type requirementGraphGo struct {
	requirementGraph
}

var _ RequirementGraph = (*requirementGraphGo)(nil)

// RequirementsGo returns a [RequirementGraph] computed by Go.  The return value is equivalent to
// the processed output of the `go mod graph` command run in a directory containing the extracted
// contents of the root module, except any go.mod directives that might affect the requirement graph
// are ignored (specifically, [replace] and [exclude]).  Go 1.25 produces a [pruned] transitive
// closure.
//
// [replace]: https://go.dev/ref/mod#go-mod-file-replace
// [exclude]: https://go.dev/ref/mod#go-mod-file-exclude
// [pruned]: https://go.dev/ref/mod#graph-pruning
func RequirementsGo(ctx context.Context, rootId ModuleId) (_ RequirementGraph, retErr error) {
	if err := rootId.Check(); err != nil {
		return nil, err
	}

	// "go mod graph" does not report whether the requirement has an "// indirect" comment or not, so
	// we have to parse the node's go.mod to get that information.  Reuse [RequirementsComplete] for
	// this purpose.
	//
	// TODO: Maybe take an optional *requirementGraphComplete as an argument so that the requirement
	// lookup results can be reused?  That uglies the API though.
	crg, cancel, err := RequirementsComplete(ctx, rootId)
	if err != nil {
		return nil, err
	}
	defer cancel()
	isIndirect := func(pId, mId ModuleId) (bool, error) {
		p := crg.Req(pId)
		m := crg.Req(mId)
		if err := crg.Load(ctx, p); err != nil {
			return false, err
		}
		reqs := crg.(*requirementGraphComplete).reqs(p)
		ind := reqs.i.Contains(m)
		if !ind && !reqs.d.Contains(m) {
			return false, fmt.Errorf(
				"\"go mod graph\" returned a requirement not listed in go.mod: %v -> %v", pId, mId)
		}
		return ind, nil
	}

	var (
		mu sync.Mutex
		rg = &requirementGraphGo{requirementGraph{reqs: map[Requirement]*requirementGraphReqs{}}}
	)
	tmp, done, err := tempFilteredModClone(ctx, rootId)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := done(); retErr == nil {
			retErr = err
		}
	}()
	args := []string{"go", "mod", "graph"}
	if slog.Default().Enabled(ctx, logging.LevelVerbose) {
		args = append(args, "-x")
	}
	cmd, out, err := command.Pipe(ctx, tmp, args...)
	if err != nil {
		return nil, err
	}
	gr, ctx := errgroup.WithContext(ctx)
	scn := bufio.NewScanner(out)
	for scn.Scan() {
		line := scn.Text()
		slog.DebugContext(ctx, "go mod graph output", "line", line)
		if strings.HasPrefix(line, "go@") {
			continue
		}
		gr.Go(func() error {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) != 2 {
				return fmt.Errorf("command %q unexpected output: %q", strings.Join(args, " "), line)
			}
			pId := ParseModuleId(parts[0])
			if pId.Path == rootId.Path && pId.Version == "" {
				pId = rootId
			}
			if err := pId.Check(); err != nil {
				return err
			}
			p := requirement{pId}
			var m Requirement
			var ind bool
			if !strings.HasPrefix(parts[1], "go@") {
				mId := ParseModuleId(parts[1])
				if err := mId.Check(); err != nil {
					return err
				}
				m = requirement{mId}
				var err error
				if ind, err = isIndirect(pId, mId); err != nil {
					return err
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if pId == rootId {
				rg.root = p
			}
			for _, n := range []Requirement{p, m} {
				if n != nil && rg.reqs[n] == nil {
					rg.reqs[n] = &requirementGraphReqs{
						d: mapset.NewThreadUnsafeSet[Requirement](),
						i: mapset.NewThreadUnsafeSet[Requirement](),
					}
				}
			}
			if m != nil {
				if ind {
					rg.reqs[p].i.Add(m)
				} else {
					rg.reqs[p].d.Add(m)
				}
			}
			return nil
		})
	}
	if err := scn.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("command %q failed: %w", strings.Join(args, " "), err)
	}
	if err := gr.Wait(); err != nil {
		return nil, err
	}
	if rg.root == nil {
		return nil, fmt.Errorf("`go mod graph` did not output the root node %v", rootId)
	}
	return rg, nil
}
