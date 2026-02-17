package gomoddepgraph

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/go-cmp/cmp"
)

type tNode string
type tColor string
type tEdges map[tNode]tColor
type tGraph map[tNode]tEdges

func TestWalkGraph(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		desc string
		g    tGraph
	}{
		{
			desc: "single node",
			g: tGraph{
				"a": tEdges{},
			},
		},
		{
			desc: "simple dep",
			g: tGraph{
				"a": tEdges{
					"b": "red",
				},
				"b": tEdges{},
			},
		},
		{
			desc: "cycle",
			g: tGraph{
				"a": tEdges{
					"b": "red",
				},
				"b": tEdges{
					"a": "blue",
				},
			},
		},
		{
			desc: "high fan-out and fan-in",
			g:    newHighFanOutFanInGraph(t),
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			// Each run has some random sleeps to try to exercise the parallelism.
			for i := range 10 {
				t.Run(strconv.Itoa(i), func(t *testing.T) {
					t.Parallel()
					var mu sync.Mutex
					got := tGraph{}
					nodeVisit := func(ctx context.Context, n tNode) (bool, error) {
						time.Sleep(rand.N(20 * time.Millisecond))
						if err := context.Cause(ctx); err != nil {
							t.Fatal(err)
						}
						if n == "" {
							t.Fatal("nil node visited")
						}
						mu.Lock()
						defer mu.Unlock()
						if _, ok := got[n]; ok {
							t.Fatalf("node %v already visited", n)
						}
						got[n] = tEdges{}
						return true, nil
					}
					load := func(ctx context.Context, n tNode) error {
						time.Sleep(rand.N(20 * time.Millisecond))
						if err := context.Cause(ctx); err != nil {
							t.Fatal(err)
						}
						if n == "" {
							t.Fatal("nil node loaded")
						}
						mu.Lock()
						defer mu.Unlock()
						if got[n] == nil {
							t.Fatalf("loaded node %v before visit", n)
						}
						return nil
					}
					edges := func(n tNode) iter.Seq2[tNode, tColor] {
						time.Sleep(rand.N(20 * time.Millisecond))
						return maps.All(tc.g[n])
					}
					edgeVisit := func(ctx context.Context, p, n tNode, color tColor) error {
						time.Sleep(rand.N(20 * time.Millisecond))
						mu.Lock()
						defer mu.Unlock()
						if p == "" {
							if got[p] != nil {
								t.Fatalf("multiple edges to start node")
							}
							got[p] = tEdges{}
						} else if got[p] == nil {
							t.Fatalf("parent node %v not yet visited", p)
						}
						if got[n] == nil {
							t.Fatalf("child node %v not yet visited", n)
						}
						if c, ok := got[p][n]; ok {
							t.Fatalf("edge %v -> %v already seen (with color %v)", p, n, c)
						}
						got[p][n] = color
						return nil
					}
					if err := walkGraph(t.Context(), "a", nodeVisit, load, edges, edgeVisit); err != nil {
						t.Fatal(err)
					}
					if diff := cmp.Diff(tc.g, got); diff != "" {
						t.Errorf("reconstructed graph differs (-want +got):\n%s", diff)
					}
				})
			}
		})
	}
}

func TestWalkGraph_ParallelVisits(t *testing.T) {
	// Strategy for this test:
	//   1. Build a high fan-out graph to ensure lots of parallel nodeVisit and edgeVisit calls.
	//   2. Assign the nodes and edges in the fan-out level into two groups.
	//   3. Wait for all of the 1st half to be in nodeVisit.
	//   4. Let the 1st half finish nodeVisit
	//   5. Wait for 1st half to enter edgeVisit.
	//   5. Let the 1st half finish edgeVisit.
	//   6. Let the 2nd half progress.
	t.Parallel()
	g := newHighFanOutFanInGraph(t)
	nodes := [2]mapset.Set[tNode]{
		mapset.NewThreadUnsafeSet[tNode](),
		mapset.NewThreadUnsafeSet[tNode](),
	}
	i := 0
	for n := range g["a"] {
		nodes[i%len(nodes)].Add(n)
		i++
	}
	for _, ns := range nodes {
		if ns.Cardinality() <= 0 {
			t.Fatalf("test setup failed")
		}
	}
	resumeNodeVisit := [2]chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
	}
	var grNodeVisit [2]sync.WaitGroup
	for i := range 2 {
		grNodeVisit[i].Add(nodes[i].Cardinality())
	}
	nodeVisit := func(ctx context.Context, n tNode) (bool, error) {
		for i := range 2 {
			if nodes[i].Contains(n) {
				grNodeVisit[i].Done()
				select {
				case <-ctx.Done():
					return false, context.Cause(ctx)
				case <-resumeNodeVisit[i]:
				}
				break
			}
		}
		return true, nil
	}
	edges := func(n tNode) iter.Seq2[tNode, tColor] { return maps.All(g[n]) }
	resumeEdgeVisit := [2]chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
	}
	var grEdgeVisit [2]sync.WaitGroup
	for i := range 2 {
		grEdgeVisit[i].Add(nodes[i].Cardinality())
	}
	edgeVisit := func(ctx context.Context, p, n tNode, color tColor) error {
		if p == "a" {
			for i := range 2 {
				if nodes[i].Contains(n) {
					grEdgeVisit[i].Done()
					select {
					case <-ctx.Done():
						return context.Cause(ctx)
					case <-resumeEdgeVisit[i]:
					}
					break
				}
			}
		}
		return nil
	}
	go func() {
		for i := range 2 {
			// First ensure that nodeVisit is called concurrently by waiting for half of them to start.
			grNodeVisit[i].Wait()
			// Now let that half complete, which should allow the edges to those nodes to be visited even
			// though half of the nodeVisit calls are still running.
			close(resumeNodeVisit[i])
			// Wait for the first half of the edgeVisit calls to start running.
			grEdgeVisit[i].Wait()
			// Let the first half of the edgeVisit calls to finish.
			close(resumeEdgeVisit[i])
			// Now repeat with the 2nd half.
		}
	}()
	if err := walkGraph(t.Context(), "a", nodeVisit, nil, edges, edgeVisit); err != nil {
		t.Fatal(err)
	}
}

func TestWalkGraph_ErrorHandling(t *testing.T) {
	t.Parallel()
	g := newHighFanOutFanInGraph(t)
	for _, tc := range []struct {
		desc           string
		errNodeVisit   bool
		errLoad        bool
		errEdgeVisit   bool
		wantNodeVisits bool
		wantLoads      bool
		wantEdgeVisits bool
	}{
		{
			desc:           "nodeVisit",
			errNodeVisit:   true,
			wantNodeVisits: true,
		},
		{
			desc:           "load",
			errLoad:        true,
			wantNodeVisits: true,
			wantLoads:      true,
		},
		{
			desc:           "edgeVisit",
			errEdgeVisit:   true,
			wantNodeVisits: true,
			wantLoads:      true,
			wantEdgeVisits: true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			var gr sync.WaitGroup
			gr.Add(len(g["a"]))
			errCh := make(chan error)
			var gotNodeVisits, gotLoads, gotEdgeVisits atomic.Int32
			maybeErr := func(ctx context.Context, maybe bool, counter *atomic.Int32, n tNode) error {
				if strings.HasPrefix(string(n), "b_") {
					counter.Add(1)
					if maybe {
						gr.Done()
						select {
						case <-ctx.Done():
							return ctx.Err()
						case err := <-errCh:
							return err
						}
					}
				}
				return nil
			}
			nodeVisit := func(ctx context.Context, n tNode) (bool, error) {
				if err := maybeErr(ctx, tc.errNodeVisit, &gotNodeVisits, n); err != nil {
					return false, err
				}
				return true, nil
			}
			load := func(ctx context.Context, n tNode) error {
				return maybeErr(ctx, tc.errLoad, &gotLoads, n)
			}
			edges := func(n tNode) iter.Seq2[tNode, tColor] { return maps.All(g[n]) }
			edgeVisit := func(ctx context.Context, p, n tNode, color tColor) error {
				return maybeErr(ctx, tc.errEdgeVisit, &gotEdgeVisits, p)
			}
			go func() {
				gr.Wait()
				select {
				case <-ctx.Done():
				case errCh <- testErr:
				}
			}()
			gotErr := walkGraph(ctx, "a", nodeVisit, load, edges, edgeVisit)
			if !errors.Is(gotErr, testErr) {
				t.Errorf("got error %v, want %v", gotErr, testErr)
			}
			want := int32(len(g["a"]))
			wantNodeVisits := want
			if !tc.wantNodeVisits {
				wantNodeVisits = 0
			}
			if got := gotNodeVisits.Load(); got != wantNodeVisits {
				t.Errorf("got node visits %v, want %v", got, wantNodeVisits)
			}
			wantLoads := want
			if !tc.wantLoads {
				wantLoads = 0
			}
			if got := gotLoads.Load(); got != wantLoads {
				t.Errorf("got loads %v, want %v", got, wantLoads)
			}
			wantEdgeVisits := want
			if !tc.wantEdgeVisits {
				wantEdgeVisits = 0
			}
			if got := gotEdgeVisits.Load(); got != wantEdgeVisits {
				t.Errorf("got edge visits %v, want %v", got, wantEdgeVisits)
			}
		})
	}
}

func TestWalkGraph_ContextCancel(t *testing.T) {
	// Strategy:
	//   1. Split high fan-out nodes into 3 groups:
	//        - 1/3 block in nodeVisit
	//        - 1/3 block in load
	//        - 1/3 block in edgeVisit
	//   2. Inject an error into one of the groups (each test case picks a different third).
	//   3. Count the number of ctx.Done() events; should add up to N-1.
	t.Parallel()
	for _, tc := range []struct {
		desc         string
		nodeVisitErr bool
		loadErr      bool
		edgeVisitErr bool
	}{
		{
			desc:         "nodeVisit",
			nodeVisitErr: true,
		},
		{
			desc:    "load",
			loadErr: true,
		},
		{
			desc:         "edgeVisit",
			edgeVisitErr: true,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			g := newHighFanOutFanInGraph(t)
			nodes := [3]mapset.Set[tNode]{
				mapset.NewThreadUnsafeSet[tNode](),
				mapset.NewThreadUnsafeSet[tNode](),
				mapset.NewThreadUnsafeSet[tNode](),
			}
			i := 0
			for n := range g["a"] {
				nodes[i%len(nodes)].Add(n)
				i++
			}
			var readyGr, doneGr sync.WaitGroup
			num := len(g["a"])
			readyGr.Add(num)
			doneGr.Add(num)
			var gotCtxDone atomic.Int32
			errCh := make(chan error)
			maybePause := func(ctx context.Context, errCh <-chan error, pause bool) error {
				if pause {
					readyGr.Done()
					defer doneGr.Done()
					select {
					case <-ctx.Done():
						gotCtxDone.Add(1)
						return ctx.Err()
					case err := <-errCh:
						return err
					}
				}
				return nil
			}
			nodeVisit := func(ctx context.Context, n tNode) (bool, error) {
				var ch <-chan error
				if tc.nodeVisitErr {
					ch = errCh
				}
				if err := maybePause(ctx, ch, nodes[0].Contains(n)); err != nil {
					return false, err
				}
				return true, nil
			}
			load := func(ctx context.Context, n tNode) error {
				var ch <-chan error
				if tc.loadErr {
					ch = errCh
				}
				return maybePause(ctx, ch, nodes[1].Contains(n))
			}
			edges := func(n tNode) iter.Seq2[tNode, tColor] { return maps.All(g[n]) }
			edgeVisit := func(ctx context.Context, p, n tNode, color tColor) error {
				var ch <-chan error
				if tc.edgeVisitErr {
					ch = errCh
				}
				return maybePause(ctx, ch, nodes[2].Contains(p))
			}
			go func() {
				t.Log("waiting for callbacks to become ready")
				readyGr.Wait()
				t.Log("waiting to send testErr")
				select {
				case <-ctx.Done():
				case errCh <- testErr:
					t.Log("sent testErr")
				}
			}()
			gotErr := walkGraph(t.Context(), "a", nodeVisit, load, edges, edgeVisit)
			if !errors.Is(gotErr, testErr) {
				t.Errorf("got error %v, want %v", gotErr, testErr)
			}
			doneGr.Wait()
			if got, want := gotCtxDone.Load(), int32(num-1); got != want {
				t.Errorf("got %v context cancelations, want %v", got, want)
			}
		})
	}
}

func newHighFanOutFanInGraph(t *testing.T) tGraph {
	t.Helper()
	g := tGraph{
		"a": tEdges{},
		"c": tEdges{},
	}
	const fanOut = 1000
	for i := range fanOut {
		n := tNode(fmt.Sprintf("b_%v", i))
		g["a"][n] = ""
		g[n] = tEdges{"c": ""}
	}
	return g
}

type testError struct{}

func (_ testError) Error() string {
	return "testError"
}

var testErr error = testError{}
