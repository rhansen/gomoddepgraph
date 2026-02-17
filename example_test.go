package gomoddepgraph_test

import (
	"context"
	"fmt"
	"slices"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/rhansen/gomoddepgraph"
	"github.com/rhansen/gomoddepgraph/internal/test/fakemodule"
)

func Example() {
	// Create some fake modules so that this example does not require network access.
	ctx, done := withFakeModules(context.Background(), [][]fakemodule.Option{
		{fakemodule.Id("example.com/dependency@v1.0.0")},
		{fakemodule.Id("example.com/root@v1.0.0"),
			fakemodule.Require("example.com/dependency@v1.0.0", false)},
	}...)
	defer done()

	// Construct a [gomoddepgraph.ModuleId] for the root module, for example:
	rootId := gomoddepgraph.ParseModuleId("example.com/root@latest")

	// Use [gomoddepgraph.ResolveVersion] to query the [Go module proxy] to resolve a [version query]
	// to the actual version.
	//
	// [Go module proxy]: https://go.dev/ref/mod#module-proxy
	// [version query]: https://go.dev/ref/mod#version-queries
	rootId, err := gomoddepgraph.ResolveVersion(ctx, rootId)
	if err != nil {
		panic(err)
	}

	// Build a [gomoddepgraph.RequirementGraph] rooted at the desired module.
	rg, err := gomoddepgraph.RequirementsGo(ctx, rootId)
	if err != nil {
		panic(err)
	}

	// Resolve the [gomoddepgraph.RequirementGraph] to a [gomoddepgraph.DependencyGraph].
	dg, err := gomoddepgraph.ResolveGo(ctx, rg)
	if err != nil {
		panic(err)
	}

	// Use [gomoddepgraph.AllDependencies] to get the complete selection set.
	fmt.Printf("selection set: %v\n", slices.Collect(gomoddepgraph.AllDependencies(dg)))

	// Use [gomoddepgraph.WalkDependencyGraph] to visit the nodes and edges of the [DependencyGraph]:
	if err := gomoddepgraph.WalkDependencyGraph(dg, dg.Root(),
		func(m gomoddepgraph.Dependency) (bool, error) {
			fmt.Printf("visited node %v\n", m)
			return true, nil
		},
		func(p, m gomoddepgraph.Dependency, surprise bool) error {
			fmt.Printf("visited edge %v -> %v (surprise: %v)\n", p, m, surprise)
			return nil
		}); err != nil {
		panic(err)
	}

	// Or manually walk the graph:
	seen := mapset.NewThreadUnsafeSet(dg.Root())
	q := []gomoddepgraph.Dependency{dg.Root()}
	for len(q) > 0 {
		m := q[0]
		q = q[1:]
		fmt.Printf("manually visited node %v\n", m)
		for d := range gomoddepgraph.Deps(dg, m) {
			if seen.Add(d) {
				q = append(q, d)
			}
		}
	}

	// Output:
	// selection set: [example.com/root@v1.0.0 example.com/dependency@v1.0.0]
	// visited node example.com/root@v1.0.0
	// visited node example.com/dependency@v1.0.0
	// visited edge example.com/root@v1.0.0 -> example.com/dependency@v1.0.0 (surprise: false)
	// manually visited node example.com/root@v1.0.0
	// manually visited node example.com/dependency@v1.0.0
}

func withFakeModules(ctx context.Context, optss ...[]fakemodule.Option) (context.Context, func()) {
	gp, done, err := fakemodule.NewFakeGoProxy()
	if err != nil {
		panic(err)
	}
	cleanup := func() {
		if err := done(); err != nil {
			panic(err)
		}
	}
	defer func() { cleanup() }()
	ctx = gp.WithEnv(ctx)
	if err := gp.AddAll(ctx, optss...); err != nil {
		panic(err)
	}
	retCleanup := cleanup
	cleanup = func() {}
	return ctx, retCleanup
}
