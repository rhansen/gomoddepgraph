package gomoddepgraph

import (
	"context"
	"fmt"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"golang.org/x/sync/errgroup"
)

// ResolveGo returns a [DependencyGraph] that represents the dependencies reported by running `go
// list -m all` in the root module.  As of Go 1.25, this is the result of running the [Minimal
// Version Selection (MVS) algorithm] on a [pruned] requirement graph.
//
// The [RequirementGraph] argument must be a graph returned from [RequirementsGo].
//
// [Minimal Version Selection (MVS) algorithm]: https://go.dev/ref/mod#minimal-version-selection
// [pruned]: https://go.dev/ref/mod#graph-pruning
func ResolveGo(ctx context.Context, rg RequirementGraph) (_ DependencyGraph, retErr error) {
	// Approach:
	//
	//   1. Create a temporary dummy module.
	//   2. Copy the root module's go.mod, dropping non-requirement directives.
	//   3. Run "go list -m all" in the dummy module.
	//   4. Filter out the dummy module from the results.
	//   5. Add this root module to the results.
	//
	// In general it is incorrect to just run "go list -m all" in the extracted contents of this
	// module's downloaded zip file ($GOMODCACHE/$module@$version).  This is because there are some
	// go.mod directives that can affect the requirement graph (specifically, "replace" and "exclude")
	// but only when the module is the "main" module (i.e., the root of the graph).  By copying this
	// root module's requirements list to a new dummy module, those directives are filtered out.
	//
	// Another approach that is incorrect: Create a new dummy module that depends on all of this root
	// module's packages, and run "go list -m all" in it.  The extra hop in the requirement graph
	// would affect the pruning that is done by Go's graph pruning algorithm, resulting in a different
	// subgraph for the MVS selection.

	if _, ok := rg.(*requirementGraphGo); !ok {
		// The returned [DependencyGraph] does not use anything other than the [RequirementGraph]
		// interface (it does not reach into implementation details of the *goRequirementGraph type),
		// but the requirements must be consistent with what Go selects as reported by `go list -m all`.
		// Specifically, if the requirement graph contains a requirement that isn't satisfied by the
		// modules selected by Go then [dependency.DirectDeps] will panic.  To prevent such panics, this
		// function requires a graph returned from [RequirementsGo] because it is safe to assume that
		// the output of `go mod graph` is consistent with the output of `go list -m all`.
		return nil, fmt.Errorf("RequirementGraph passed to ResolveGo is not from RequirementsGo")
	}
	rootId := rg.Root().Id()
	tmp, tmpDone, err := tempFilteredModClone(ctx, rootId)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tmpDone(); retErr == nil {
			retErr = err
		}
	}()
	lsJson, lsmDone := goListM(ctx, tmp, "all")
	defer func() {
		if err := lsmDone(); err != nil {
			retErr = err
		}
	}()
	dg := &dependencyGraph{
		rg:       rg,
		sel:      map[string]Dependency{},
		surprise: map[Dependency]mapset.Set[Dependency]{},
	}
	for md := range lsJson {
		dId := rootId
		if md.Path != rootId.Path || md.Version != "" {
			dId = NewModuleId(md.Path, md.Version)
		}
		if err := dId.Check(); err != nil {
			return nil, err
		}
		r := rg.Req(dId)
		if r == nil {
			return nil, fmt.Errorf("selected dependency %v missing from requirement graph", dId)
		}
		d := dependency{dId}
		dg.sel[dId.Path] = d
	}
	// Compute the set of surprise dependencies for each dependency in the selection set.
	//
	// TODO: This implementation is O(|V|*(|V|+|E|)), which can be improved.  However, a more
	// efficient implementation might be tricky due to possible dependency cycles.
	var mu sync.Mutex
	gr, ctx := errgroup.WithContext(ctx)
	for _, d := range dg.sel {
		gr.Go(func() error {
			surprise, err := computeSurpriseDeps(ctx, rg, dg, d)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			dg.surprise[d] = surprise
			return nil
		})
	}
	if err := gr.Wait(); err != nil {
		return nil, err
	}
	return dg, nil
}
