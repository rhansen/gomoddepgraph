package gomoddepgraph

import (
	"context"
	"fmt"
	"iter"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph/internal/itertools"
	"golang.org/x/mod/semver"
)

// A DependencyGraph is a directed graph (often cyclic) representing the modules selected to satisfy
// every [Requirement] in a [RequirementGraph], and organized with a similar topology as the
// [RequirementGraph].
type DependencyGraph interface {
	// Root returns the [Dependency] selected to satisfy [RequirementGraph.Root].
	Root() Dependency

	// Selected returns the [Dependency] in this [DependencyGraph] that satisfies the requirement
	// indicated by the given [ModuleId].  Returns nil if no [Dependency] satisfies the requirement.
	// May panic or return nil if the given [ModuleId] is invalid or does not have a fully-specified
	// semantic version (see [ModuleId.Check]).
	Selected(req ModuleId) Dependency

	// DirectDeps returns the given [Dependency]'s own direct dependencies.  These are the modules
	// that were selected (see [AllDependencies] and [DependencyGraph.Selected]) to satisfy the
	// module's direct requirements (see [RequirementGraph.DirectReqs]).
	//
	// This method does not return any surprise dependencies; see [DependencyGraph.SurpriseDeps].
	DirectDeps(m Dependency) iter.Seq[Dependency]

	// SurpriseDeps returns the given [Dependency]'s own surprise dependencies.  See the "Surprise
	// Dependencies" section of the package-level documentation for details.
	SurpriseDeps(m Dependency) iter.Seq[Dependency]
}

// Deps is a convenience function that returns both [DependencyGraph.DirectDeps] and
// [DependencyGraph.SurpriseDeps], with the mapped value set to true for any surprise dependencies.
func Deps(dg DependencyGraph, d Dependency) iter.Seq2[Dependency, bool] {
	return itertools.Cat2(
		itertools.Attach(dg.DirectDeps(d), false),
		itertools.Attach(dg.SurpriseDeps(d), true))
}

type dependencyGraph struct {
	rg       RequirementGraph
	sel      map[string]Dependency
	surprise map[Dependency]mapset.Set[Dependency]
}

var _ DependencyGraph = (*dependencyGraph)(nil)

func (dg *dependencyGraph) Root() Dependency {
	return dg.Selected(dg.rg.Root().Id())
}

func (dg *dependencyGraph) Selected(req ModuleId) Dependency {
	d, ok := dg.sel[req.Path]
	if !ok || semver.Compare(d.Id().Version, req.Version) < 0 {
		return nil
	}
	return d
}

func (dg *dependencyGraph) DirectDeps(m Dependency) iter.Seq[Dependency] {
	return func(yield func(Dependency) bool) {
		r := dg.rg.Req(m.Id())
		if r == nil {
			panic(fmt.Errorf("no corresponding requirement for dependency %v", m))
		}
		for rr := range dg.rg.DirectReqs(r) {
			d := dg.Selected(rr.Id())
			if d == nil {
				panic(fmt.Errorf("requirement %v not satisfied by selection of dependencies", rr))
			}
			if !yield(d) {
				return
			}
		}
	}
}

func (dg *dependencyGraph) SurpriseDeps(m Dependency) iter.Seq[Dependency] {
	return mapset.Elements(dg.surprise[m])
}

// computeSurpriseDeps discovers any surprise dependencies without calling
// [DependencyGraph.SurpriseDeps].  This can be used to implement [DependencyGraph.SurpriseDeps],
// but note that [DependencyGraph.DirectDeps] must return the correct direct dependencies for every
// [Dependency] in the [DependencyGraph] before this is called.
func computeSurpriseDeps(ctx context.Context, rg RequirementGraph, dg DependencyGraph, d Dependency) (mapset.Set[Dependency], error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	needles := mapset.NewThreadUnsafeSet[Dependency]()
	haystack := []Dependency(nil)
	if err := d.Id().Check(); err != nil {
		return nil, err
	}
	r := rg.Req(d.Id())
	if err := rg.Load(ctx, r); err != nil {
		return nil, err
	}
	seen := mapset.NewThreadUnsafeSet[Dependency]()
	for dr, ind := range Reqs(rg, r) {
		dd := dg.Selected(dr.Id())
		if dd == nil {
			return nil, fmt.Errorf("requirement %v not satisfied by the selection of dependencies", dr)
		}
		if ind {
			needles.Add(dd)
		} else {
			haystack = append(haystack, dd)
			seen.Add(dd)
		}
	}
	// d is added to the seen set because there might be a circular dependency, and a d->needle
	// edge should not be traversed.
	seen.Add(d)
	// BFS is likely to find the needles faster than DFS because they are likely to appear as
	// immediate dependencies due to the way Go adds "// indirect" requirements to go.mod.
	for len(haystack) > 0 {
		m := haystack[0]
		haystack = haystack[1:]
		needles.Remove(m)
		if needles.IsEmpty() {
			break
		}
		for md := range dg.DirectDeps(m) {
			if seen.Add(md) {
				haystack = append(haystack, md)
			}
		}
	}
	return needles, nil
}

func walkDependencyGraph(ctx context.Context, dg DependencyGraph, start Dependency,
	nodeVisit func(ctx context.Context, m Dependency) (bool, error),
	edgeVisit func(ctx context.Context, p, m Dependency, surprise bool) error) error {

	edges := func(m Dependency) iter.Seq2[Dependency, bool] { return Deps(dg, m) }
	return walkGraph(ctx, start, nodeVisit, nil, edges, edgeVisit)
}

// WalkDependencyGraph visits each node ([Dependency]) and edge in the [DependencyGraph] in
// topological order and calls the optional visit callbacks.  The callbacks are called at most once
// per node or edge.  Either callback (or both) may be nil.
//
// The nodeVisit callback's return value should be true if the walk should visit outgoing edges from
// the node, false if the edges should not be visited, defaulting to true if nodeVisit is nil.
//
// The nodes and edges are visited in parallel, and the callbacks are called concurrently, except no
// edgeVisit callback will be called for a pair of nodes before the nodeVisit callbacks for the two
// nodes have both returned.  This results in a topological ordering of callback calls.
//
// If there is an error, including if any callback returns non-nil, the walk stops.  (It may take
// some time to conclude any in-progress node or edge processing.)  The first error encountered is
// returned.
func WalkDependencyGraph(dg DependencyGraph, start Dependency,
	nodeVisit func(m Dependency) (bool, error),
	edgeVisit func(p, m Dependency, surprise bool) error) error {

	return walkDependencyGraph(context.Background(), dg, start,
		func(ctx context.Context, m Dependency) (bool, error) { return nodeVisit(m) },
		func(ctx context.Context, p, m Dependency, s bool) error { return edgeVisit(p, m, s) })
}

// AllDependencies walks the given [DependencyGraph] and yields every [Dependency] it encounters.
// The [Dependency] objects are yielded in topological order.  Together, these [Dependency] objects
// form the selection set, which are the modules selected to satisfy the requirements of
// [DependencyGraph.Root] and the selected dependencies' own requirements.
func AllDependencies(dg DependencyGraph) iter.Seq[Dependency] {
	deps, done := allNodes(context.Background(), dg, dg.Root(), walkDependencyGraph)
	return func(yield func(Dependency) bool) {
		defer func() {
			if err := done(); err != nil {
				panic("bug: DependencyGraph walk should never return an error")
			}
		}()
		for d := range deps {
			if !yield(d) {
				return
			}
		}
	}
}
