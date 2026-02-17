package gomoddepgraph

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sync"

	"github.com/crillab/gophersat/solver"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph/internal/itertools"
	"golang.org/x/sync/errgroup"
)

// ResolveSat constructs a Boolean satisfiability (SAT) problem from the given [RequirementGraph]
// and uses a SAT solver to select the dependencies.
func ResolveSat(ctx context.Context, rg RequirementGraph) (DependencyGraph, error) {
	prob, nodes, _, err := buildSatProblem(ctx, rg)
	if err != nil {
		return nil, err
	}
	s := solver.New(prob)
	if status := s.Solve(); status != solver.Sat {
		return nil, fmt.Errorf("no selection satisfies the requirements (SAT status: %v)", status)
	}
	model := s.Model()
	trueVars := satModelTrueVars(model)
	dg := &dependencyGraph{
		rg: rg,
		sel: maps.Collect(
			itertools.Map12(
				trueVars,
				func(v solver.Var) (string, Dependency) {
					m := nodes[v]
					mId := m.Id()
					return mId.Path, dependency{mId}
				})),
		surprise: map[Dependency]mapset.Set[Dependency]{},
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

func buildSatProblem(ctx context.Context, rg RequirementGraph) (*solver.Problem, []Requirement, map[Requirement]solver.Var, error) {
	nodesSeq, done := AllRequirements(ctx, rg)
	nodes := slices.SortedFunc(nodesSeq, RequirementCompare)
	if err := done(); err != nil {
		return nil, nil, nil, err
	}
	vars := maps.Collect(
		itertools.Map2(slices.All(nodes), func(v int, m Requirement) (Requirement, solver.Var) {
			return m, solver.Var(v)
		}))
	delete(vars, nil)
	constrs := []solver.PBConstr{
		// First of all, the root module must be selected.
		solver.PropClause(int(vars[rg.Root()].Int())),
	}
	for v, pathLits := solver.Var(0), []int(nil); v < solver.Var(len(nodes)); v++ {
		m := nodes[v]
		var nextm Requirement
		if v+1 < solver.Var(len(nodes)) {
			nextm = nodes[v+1]
		}
		// pathLits is the list of literals corresponding to every [Module] with the same [Module.Path]
		// as m that has been seen so far.  This list is incrementally constructed by assuming that nodes
		// is ordered by path.
		pathLits = append(pathLits, int(v.Int()))
		if nextm == nil || nextm.Id().Path != m.Id().Path {
			// pathLits is now complete for this path.  Only one of these versions can be selected.
			if len(pathLits) > 1 {
				constrs = append(constrs, solver.AtMost(pathLits, 1))
			}
			pathLits = nil
		}
		// Add dependency constraints for m.
		for req := range Reqs(rg, m) {
			reqClause := []int{
				// Either this module is NOT selected (hence negative)...
				-int(v.Int()),
			}
			// Assumption: nodes is ordered by path then by increasing versions.
			for v := vars[req]; v < solver.Var(len(nodes)) && nodes[v].Id().Path == req.Id().Path; v++ {
				// ...or a version that satisfies the requirement IS selected.
				reqClause = append(reqClause, int(v.Int()))
			}
			constrs = append(constrs, solver.PropClause(reqClause...))
		}
	}
	prob := solver.ParsePBConstrs(constrs)
	prob.SetCostFunc(
		slices.Collect(func(yield func(solver.Lit) bool) {
			for v := solver.Var(0); v < solver.Var(len(nodes)); v++ {
				if !yield(v.Lit()) {
					return
				}
			}
		}),
		slices.Repeat([]int{1}, len(nodes)))
	return prob, nodes, vars, nil
}

func satModelTrueVars(model []bool) iter.Seq[solver.Var] {
	return itertools.Map21(
		itertools.Filter2(
			slices.All(model),
			func(_ int, isSel bool) bool { return isSel }),
		func(v int, _ bool) solver.Var { return solver.Var(v) })
}
