package gomoddepgraph

import (
	"context"
	"fmt"
	"iter"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph/internal/itertools"
)

// A RequirementGraph is a directed graph (possibly cyclic) representing the transitive closure of
// module requirements starting with a particular root module.
type RequirementGraph interface {
	// Root returns the root node in the [RequirementGraph].
	Root() Requirement

	// Req returns the [Requirement] in this [RequirementGraph] that has the given [ModuleId].
	// Returns nil if no such [Requirement] exists.  May return a non-nil [Requirement] that is not
	// reachable from [RequirementGraph.Root].  May panic or return nil if the given [ModuleId] is
	// invalid or does not have a fully-specified semantic version (see [ModuleId.Check]).
	Req(m ModuleId) Requirement

	// Load loads the given module's requirements into memory (from disk, network, another process,
	// etc.).  This method must be return successfully before calling [RequirementGraph.DirectReqs] or
	// [RequirementGraph.ImmediateIndirectReqs] for the given module.
	//
	// May panic or return an error if the given module is not reachable from the root module, or if
	// there does not exist a path from the root module to this module where every module on the path
	// (except the given module) has been sucessfully loaded.  Idempotent except failed loads are
	// retried, and a canceled context may cause this to return an error even if a previous load
	// succeeded.  Thread-safe.
	Load(ctx context.Context, m Requirement) error

	// DirectReqs returns the given [Requirement]'s own direct requirements.  These requirements,
	// along with the requirements returned from [RequirementGraph.ImmediateIndirectReqs], are the
	// edges in this graph from the given module.
	//
	// Requirement cycles are possible, especially with [UnifyRequirements].
	//
	// The requirements returned here and from [RequirementGraph.ImmediateIndirectReqs] can differ
	// from the requirements listed in the module's go.mod, perhaps because requirements were [pruned]
	// by Go or adjusted by [UnifyRequirements].  To identify such differences, compare this graph's
	// returned requirements with those returned from a [RequirementsComplete] graph.
	//
	// [RequirementGraph.Load] must have returned successfully for the given module before calling
	// this.
	//
	// [pruned]: https://go.dev/ref/mod#graph-pruning
	DirectReqs(m Requirement) iter.Seq[Requirement]

	// ImmediateIndirectReqs returns the given [Requirement]'s own immediate indirect requirements.
	// See [RequirementGraph.DirectReqs] for more details.
	//
	// [RequirementGraph.Load] must have returned successfully for the given module before calling
	// this.
	ImmediateIndirectReqs(m Requirement) iter.Seq[Requirement]
}

// Reqs is a convenience function that returns both [RequirementGraph.DirectReqs] and
// [RequirementGraph.ImmediateIndirectReqs], with the mapped value set to true for any immediate
// indirect requirements.
func Reqs(rg RequirementGraph, r Requirement) iter.Seq2[Requirement, bool] {
	return itertools.Cat2(
		itertools.Attach(rg.DirectReqs(r), false),
		itertools.Attach(rg.ImmediateIndirectReqs(r), true))
}

type requirementGraphReqs struct {
	d, i mapset.Set[Requirement]
}

type requirementGraph struct {
	root Requirement
	reqs map[Requirement]*requirementGraphReqs
}

var _ RequirementGraph = (*requirementGraph)(nil)

func (rg *requirementGraph) Root() Requirement {
	return rg.root
}

func (rg *requirementGraph) Req(mId ModuleId) Requirement {
	if err := mId.Check(); err != nil {
		panic(err)
	}
	m := requirement{mId}
	if rg.reqs[m] == nil {
		return nil
	}
	return m
}

func (rg *requirementGraph) Load(ctx context.Context, m Requirement) error {
	if rg.reqs[m] == nil {
		panic(fmt.Errorf("module %v is not in this requirement graph", m))
	}
	return nil
}

func (rg *requirementGraph) DirectReqs(m Requirement) iter.Seq[Requirement] {
	return mapset.Elements(rg.reqs[m].d)
}

func (rg *requirementGraph) ImmediateIndirectReqs(m Requirement) iter.Seq[Requirement] {
	return mapset.Elements(rg.reqs[m].i)
}

// WalkRequirementGraph visits each node ([Requirement]) and edge in the [RequirementGraph] in
// topological order and calls the optional visit callbacks.  The callbacks are called at most once
// per node or edge.  Either callback (or both) may be nil.
//
// The nodeVisit callback's return value should be true if the walk should visit outgoing edges from
// the node, false if the edges should not be visited, defaulting to true if nodeVisit is nil.  This
// function does not load (see [Requirement.Load]) the [Requirement] before passing it to the
// nodeVisit callback.
//
// The parent [Requirement] is loaded (see [Requirement.Load]) before the edgeVisit callback is
// called, but the child [Requirement] is not.
//
// The nodes and edges are visited in parallel, and the callbacks are called concurrently, except no
// edgeVisit callback will be called for a pair of nodes before the nodeVisit callbacks for the two
// nodes have both returned.  This results in a topological ordering of callback calls.
//
// If there is an error, including if any callback returns non-nil, the [context.Context] passed to
// the callbacks is canceled and the walk stops.  (It may take some time to conclude any in-progress
// node or edge processing.)  The first error encountered is returned.
func WalkRequirementGraph(ctx context.Context, rg RequirementGraph, start Requirement,
	nodeVisit func(ctx context.Context, m Requirement) (bool, error),
	edgeVisit func(ctx context.Context, p, m Requirement, ind bool) error) error {

	edges := func(m Requirement) iter.Seq2[Requirement, bool] { return Reqs(rg, m) }
	return walkGraph(ctx, start, nodeVisit, rg.Load, edges, edgeVisit)
}

// AllRequirements walks the given [RequirementGraph] and yields every [Requirement] it encounters.
// The [Requirement] objects are yielded in topological order.  Every yielded [Requirement] is
// loaded (see [RequirementGraph.Load]).  The returned done callback must be called when done
// iterating; it returns the first error encountered during the walk.
func AllRequirements(ctx context.Context, rg RequirementGraph) (iter.Seq[Requirement], func() error) {
	return allNodes(ctx, rg, rg.Root(), WalkRequirementGraph)
}
