package gomoddepgraph

import (
	"context"
	"log/slog"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"golang.org/x/mod/semver"
)

// UnifyRequirements walks the input graph and returns a [RequirementGraph] that has the same rough
// shape as the input graph, except every requirement version is adjusted to equal the newest
// version of each required module encountered during the input graph walk.  The resulting graph is
// similar to, but not the same as, the MVS selection on the input graph.  Paths through older
// module versions are ignored, so this does not need to traverse the complete input graph.  Passing
// [RequirementsComplete] to [UnifyRequirements] to [ResolveMvs] can significantly reduce the number
// of [Requirement.Load] calls compared to passing [RequirementsComplete] directly to [ResolveMvs],
// avoiding lots of slow go.mod downloads and processing for a complex module.
//
// For example, this requirement graph (rooted at `A`):
//
//   - A requires B and C
//   - B requires Dv1.0
//   - C requires Dv1.1
//   - Dv1.0 requires E
//   - Dv1.1 requires F
//
// becomes:
//
//   - A requires B and C
//   - B requires Dv1.1
//   - C requires Dv1.1
//   - Dv1.1 requires F
//
// Warning: This can turn an acyclic graph into a cyclic graph (e.g., Xv1.1 -> Y -> Xv1.0 becomes
// Xv1.1 -> Y -> Xv1.1).
//
// Warning: Because this algorithm prunes some edges in the input [RequirementGraph], newer versions
// of some other modules required elsewhere may become unreachable and thus not selected when
// resolving the output graph to a [DependencyGraph].  The resulting selection is still correct (any
// set of modules that collectively satisfy their combined requirements as specified in the output
// [RequirementGraph] will also satisfy their combined requirements as specified in the input
// [RequirementGraph]), but the returned [RequirementGraph]—and thus any [DependencyGraph] computed
// from it—may change depending on which requirements in the input graph are traversed first by this
// function.  This implementation performs a non-deterministic graph walk, so different runs on the
// same input requirement graph might produce different returned graphs.  If reproducibility is
// important, do not use this function.
//
// [module proxy]: https://go.dev/ref/mod#module-proxy
func UnifyRequirements(ctx context.Context, rg RequirementGraph) (RequirementGraph, error) {
	max := map[string]string{}
	for {
		unified, restart, err := unifyRequirementsInner(ctx, rg, max)
		if err != nil {
			return nil, err
		}
		if restart {
			slog.DebugContext(ctx, "UnifyRequirements: restart")
			rg = unified
			continue
		}
		return unified, nil
	}
}

func unifyRequirementsInner(ctx context.Context, rg RequirementGraph, max map[string]string) (_ RequirementGraph, restart bool, _ error) {
	var mu sync.Mutex // Protects max and the returned graph.
	ret := &requirementGraph{reqs: map[Requirement]*requirementGraphReqs{}}
	err := WalkRequirementGraph(ctx, rg, rg.Root(),
		func(ctx context.Context, m Requirement) (bool, error) {
			mId := m.Id()
			mu.Lock()
			defer mu.Unlock()
			mv, ok := max[mId.Path]
			if ok {
				if cmp := semver.Compare(mId.Version, mv); cmp < 0 {
					return false, nil
				} else if cmp > 0 {
					slog.DebugContext(ctx, "unifyRequirementsInner: restart", "old", mv, "new", mId)
					// A newer version of an already encountered module was encountered.  Signal a
					// reunificiation of the requirement versions to ensure that versions are truly unified.
					// We could cancel all in-flight visits and restart the walk immediately, but if there are
					// many version bumps like this then we'll end up re-walking the same parts of the graph
					// over and over again (making a bit more progress each time) at great cost for no real
					// benefit.  Instead, we finish this walk, then re-walk the partially unified graph
					// exactly once more.
					restart = true
				}
				// Continue even if cmp == 0 because this call to unifyRequirementsInner might be a restart
				// on a partially unified requirement graph.  This callback should never be called twice for
				// the same version in the same walk so there's no harm in continuing if cmp == 0.
			}
			max[mId.Path] = mId.Version
			m2 := requirement{mId}
			if mId == rg.Root().Id() {
				ret.root = m2
			}
			ret.reqs[m2] = &requirementGraphReqs{
				d: mapset.NewThreadUnsafeSet[Requirement](),
				i: mapset.NewThreadUnsafeSet[Requirement](),
			}
			return true, nil
		},
		func(ctx context.Context, p, m Requirement, ind bool) error {
			pId := p.Id()
			p2 := requirement{pId}
			mId := m.Id()
			mu.Lock()
			defer mu.Unlock()
			if mv := max[pId.Path]; pId.Version != mv {
				// This can happen if the parent node was the newest observed version at the time this
				// edge was queued, but a newer version of the parent node was discovered immediately
				// after.  Just ignore this edge; the graph walk will eventually visit the edges from the
				// newest version of the parent node.
				slog.DebugContext(ctx, "unify: ignoring edge from older parent node",
					"p", p, "m", m, "max", mv)
				return nil
			}
			mId.Version = max[mId.Path]
			m2 := requirement{mId}
			if ind {
				ret.reqs[p2].i.Add(m2)
			} else {
				ret.reqs[p2].d.Add(m2)
			}
			return nil
		})
	if err != nil {
		return nil, false, err
	}
	return ret, restart, nil
}
