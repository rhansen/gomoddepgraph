package gomoddepgraph

import (
	"context"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"
)

// ResolveMvs performs the [Minimal Version Selection (MVS) algorithm] on the given
// [RequirementGraph].  This is expected to behave the same as [ResolveGo], except it works with any
// [RequirementGraph], not just one returned from [RequirementsGo], and its behavior will not change
// if Go's dependency resolution algorithm changes.
//
// [Minimal Version Selection (MVS) algorithm]: https://go.dev/ref/mod#minimal-version-selection
func ResolveMvs(ctx context.Context, rg RequirementGraph) (DependencyGraph, error) {
	var mu sync.Mutex
	dg := &dependencyGraph{
		rg:       rg,
		sel:      map[string]Dependency{},
		surprise: map[Dependency]mapset.Set[Dependency]{},
	}
	if err := WalkRequirementGraph(ctx, rg, rg.Root(),
		func(ctx context.Context, m Requirement) (bool, error) {
			mId := m.Id()
			mu.Lock()
			defer mu.Unlock()
			if d := dg.sel[mId.Path]; d == nil || semver.Compare(mId.Version, d.Id().Version) > 0 {
				d = dependency{mId}
				dg.sel[mId.Path] = d
			}
			return true, nil
		},
		nil); err != nil {
		return nil, err
	}
	// Compute the set of surprise dependencies for each dependency in the selection set.
	//
	// TODO: This implementation is O(|V|*(|V|+|E|)), which can be improved.  However, a more
	// efficient implementation might be tricky due to possible dependency cycles.
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
