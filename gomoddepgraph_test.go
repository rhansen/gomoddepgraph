package gomoddepgraph_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/rhansen/gomoddepgraph"
	fm "github.com/rhansen/gomoddepgraph/internal/test/fakemodule"
)

// Convenience types to simplify test code.
type tNode = string
type tColor = bool
type tEdges = map[tNode]tColor
type tGraph = map[tNode]tEdges

func TestGoModDepGraph(t *testing.T) {
	t.Parallel()
	type testCase struct {
		desc                      string
		root                      string
		fakemods                  [][]fm.Option
		want_RequirementsGo       tGraph
		want_RequirementsComplete tGraph
		want_UnifyRequirements    tGraph
		want_ResolveGo            tGraph
		want_ResolveMvs           tGraph
		want_ResolveSat           tGraph
	}
	testCases := []*testCase{
		{
			desc: "single node",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/root@v1.0.0")},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {},
			},
			// want_RequirementsComplete: ditto,
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "simple dep",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/dep@v1.0.0")},
				{fm.Id("example.com/root@v1.0.0"),
					fm.Require("example.com/dep@v1.0.0", false)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep@v1.0.0": false,
				},
				"example.com/dep@v1.0.0": {},
			},
			// want_RequirementsComplete: ditto,
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep@v1.0.0": false,
				},
				"example.com/dep@v1.0.0": {},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "non-immediate indirect dep",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/dep2@v1.0.0")},
				{fm.Id("example.com/dep1@v1.0.0"),
					fm.Require("example.com/dep2@v1.0.0", false)},
				{fm.Id("example.com/root@v1.0.0"),
					fm.Require("example.com/dep1@v1.0.0", false)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {},
			},
			// want_RequirementsComplete: ditto,
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "immediate indirect non-surprise dep",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/dep2@v1.0.0")},
				{fm.Id("example.com/dep1@v1.0.0"),
					fm.Require("example.com/dep2@v1.0.0", false)},
				{fm.Id("example.com/root@v1.0.0"),
					fm.Require("example.com/dep1@v1.0.0", false),
					fm.Require("example.com/dep2@v1.0.0", true)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
					"example.com/dep2@v1.0.0": true,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {},
			},
			// want_RequirementsComplete: ditto,
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "simple surprise dep",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/dep@v1.0.0")},
				{fm.Id("example.com/root@v1.0.0"),
					fm.Require("example.com/dep@v1.0.0", true)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep@v1.0.0": true,
				},
				"example.com/dep@v1.0.0": {},
			},
			// want_RequirementsComplete: ditto,
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep@v1.0.0": true,
				},
				"example.com/dep@v1.0.0": {},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "cycle",
			root: "example.com/root@v1.1.0",
			// A true requirement cycle is not possible without disabling authentication (GOSUMDB=off)
			// because it is ~impossible to generate two cryptographic hashes that use each other's values
			// in their input.  Users are unlikely to disable authentication, and a true requirement cycle
			// is unlikely to appear in the wild, so there is no requirement cycle in these test cases.
			//
			// A *dependency* cycle *is* created from the non-cyclic requirement graph, however.
			fakemods: [][]fm.Option{
				{fm.Id("example.com/root@v1.0.0")},
				{fm.Id("example.com/dep@v1.0.0"),
					fm.Require("example.com/root@v1.0.0", false)},
				{fm.Id("example.com/root@v1.1.0"),
					fm.Require("example.com/dep@v1.0.0", false)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {},
				"example.com/root@v1.1.0": {
					"example.com/dep@v1.0.0": false,
				},
				"example.com/dep@v1.0.0": {
					"example.com/root@v1.0.0": false,
				},
			},
			// want_RequirementsComplete: ditto,
			want_UnifyRequirements: tGraph{
				"example.com/root@v1.1.0": {
					"example.com/dep@v1.0.0": false,
				},
				"example.com/dep@v1.0.0": {
					"example.com/root@v1.1.0": false,
				},
			},
			want_ResolveGo: tGraph{
				"example.com/root@v1.1.0": {
					"example.com/dep@v1.0.0": false,
				},
				"example.com/dep@v1.0.0": {
					"example.com/root@v1.1.0": false,
				},
			},
			// want_ResolveMvs: ditto,
			// want_ResolveSat: ditto,
		},
		{
			desc: "pruned",
			root: "example.com/root@v1.0.0",
			fakemods: [][]fm.Option{
				{fm.Id("example.com/dep3@v1.0.0")},
				{fm.Id("example.com/dep2@v1.0.0"),
					fm.Require("example.com/dep3@v1.0.0", false)},
				{fm.Id("example.com/dep1@v1.0.0"),
					fm.Require("example.com/dep2@v1.0.0", false)},
				{fm.Id("example.com/root@v1.0.0"),
					fm.Require("example.com/dep1@v1.0.0", false)},
			},
			want_RequirementsGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				// Note the lack of dep3.
				"example.com/dep2@v1.0.0": {},
			},
			want_RequirementsComplete: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {
					// RequirementsGo prunes this, but RequirementsComplete does not.
					"example.com/dep3@v1.0.0": false,
				},
				"example.com/dep3@v1.0.0": {},
			},
			// want_UnifyRequirements: ditto,
			want_ResolveGo: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				// Note the lack of dep3.
				"example.com/dep2@v1.0.0": {},
			},
			want_ResolveMvs: tGraph{
				"example.com/root@v1.0.0": {
					"example.com/dep1@v1.0.0": false,
				},
				"example.com/dep1@v1.0.0": {
					"example.com/dep2@v1.0.0": false,
				},
				"example.com/dep2@v1.0.0": {
					// The ResolveMvs and ResolveSat tests use RequirementsComplete, so the dep3 requirement
					// isn't pruned here.
					"example.com/dep3@v1.0.0": false,
				},
				"example.com/dep3@v1.0.0": {},
			},
			// want_ResolveSat: ditto,
		},
	}
	// Fill in the "ditto" wants.
	for _, tc := range testCases {
		if tc.want_RequirementsComplete == nil {
			tc.want_RequirementsComplete = tc.want_RequirementsGo
		}
		if tc.want_UnifyRequirements == nil {
			tc.want_UnifyRequirements = tc.want_RequirementsComplete
		}
		if tc.want_ResolveMvs == nil {
			tc.want_ResolveMvs = tc.want_ResolveGo
		}
		if tc.want_ResolveSat == nil {
			tc.want_ResolveSat = tc.want_ResolveMvs
		}
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			ctx := fm.NewTestFakeGoProxy(t).AddAll(tc.fakemods...).Context()
			cancel := func() {}
			defer func() { cancel() }()
			rootId := ParseModuleId(tc.root)
			rgGo := sync.OnceValues(func() (RequirementGraph, error) {
				return RequirementsGo(ctx, rootId)
			})
			rgComplete := sync.OnceValues(func() (RequirementGraph, error) {
				var rg RequirementGraph
				var err error
				rg, cancel, err = RequirementsComplete(ctx, rootId)
				return rg, err
			})
			t.Run("RequirementsGo", func(t *testing.T) {
				t.Parallel()
				rg, err := rgGo()
				if err != nil {
					t.Fatal(err)
				}
				checkReqGraph(ctx, t, rg, tc.want_RequirementsGo)
			})
			t.Run("RequirementsComplete", func(t *testing.T) {
				t.Parallel()
				rg, err := rgComplete()
				if err != nil {
					t.Fatal(err)
				}
				checkReqGraph(ctx, t, rg, tc.want_RequirementsComplete)
			})
			t.Run("UnifyRequirements", func(t *testing.T) {
				t.Parallel()
				rg, err := rgComplete()
				if err != nil {
					t.Fatal(err)
				}
				if rg, err = UnifyRequirements(ctx, rg); err != nil {
					t.Fatal(err)
				}
				checkReqGraph(ctx, t, rg, tc.want_UnifyRequirements)
			})
			t.Run("ResolveGo", func(t *testing.T) {
				t.Parallel()
				rg, err := rgGo()
				if err != nil {
					t.Fatal(err)
				}
				dg, err := ResolveGo(ctx, rg)
				if err != nil {
					t.Fatal(err)
				}
				checkDepGraph(t, dg, tc.want_ResolveGo)
			})
			t.Run("ResolveMvs", func(t *testing.T) {
				t.Parallel()
				rg, err := rgComplete()
				if err != nil {
					t.Fatal(err)
				}
				dg, err := ResolveMvs(ctx, rg)
				if err != nil {
					t.Fatal(err)
				}
				checkDepGraph(t, dg, tc.want_ResolveMvs)
			})
			t.Run("ResolveSat", func(t *testing.T) {
				t.Parallel()
				rg, err := rgComplete()
				if err != nil {
					t.Fatal(err)
				}
				dg, err := ResolveSat(ctx, rg)
				if err != nil {
					t.Fatal(err)
				}
				checkDepGraph(t, dg, tc.want_ResolveSat)
			})
		})
	}
}

func checkReqGraph(ctx context.Context, t *testing.T, rg RequirementGraph, want tGraph) {
	t.Helper()
	var mu sync.Mutex
	got := tGraph{}
	if err := WalkRequirementGraph(ctx, rg, rg.Root(),
		func(ctx context.Context, m Requirement) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			got[m.String()] = tEdges{}
			return true, nil
		},
		func(ctx context.Context, p, m Requirement, ind bool) error {
			mu.Lock()
			defer mu.Unlock()
			got[p.String()][m.String()] = ind
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("graph differs from expected (-want, +got):\n%s", diff)
	}
}

func checkDepGraph(t *testing.T, dg DependencyGraph, want tGraph) {
	t.Helper()
	var mu sync.Mutex
	got := tGraph{}
	if err := WalkDependencyGraph(dg, dg.Root(),
		func(m Dependency) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			got[m.String()] = tEdges{}
			return true, nil
		},
		func(p, m Dependency, surprise bool) error {
			mu.Lock()
			defer mu.Unlock()
			got[p.String()][m.String()] = surprise
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("graph differs from expected (-want, +got):\n%s", diff)
	}
}
