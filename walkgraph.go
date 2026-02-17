package gomoddepgraph

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// The zero value for N must not be a valid node value because it is used to indicate the parent of
// the start node.
func walkGraph[N comparable, E any](ctx context.Context, start N,
	nodeVisit func(ctx context.Context, m N) (bool, error),
	load func(ctx context.Context, m N) error,
	edges func(m N) iter.Seq2[N, E],
	edgeVisit func(ctx context.Context, p, m N, color E) error) (retErr error) {

	zeroN := *new(N)
	zeroE := *new(E)
	slog.DebugContext(ctx, "walkGraph start")
	nNodes := 0
	nEdges := 0
	var nDescends atomic.Int32
	defer func() {
		slog.DebugContext(ctx, "walkGraph done",
			"nodes", nNodes, "edges", nEdges, "descends", nDescends.Load(), "err", retErr)
	}()
	seen := map[N]<-chan struct{}{}
	type qEnt struct {
		p     N // Parent node.
		m     N // Child node.
		color E // Edge color/flavor/type/weight/whatever.
	}
	q := make(chan qEnt)
	var inflight atomic.Int32
	inflightDone := func() {
		if n := inflight.Add(-1); n == 0 {
			close(q)
		}
	}
	gr, ctx := errgroup.WithContext(ctx)
	enqueue := func(qe qEnt) {
		inflight.Add(1)
		gr.Go(func() error {
			select {
			case <-ctx.Done():
				inflightDone()
				return context.Cause(ctx)
			case q <- qe:
				return nil
			}
		})
	}
	// process processes an edge in the graph.  It always runs synchronously in the main select loop
	// so no synchronization primitives are needed to protect `seen`.
	process := func(qe qEnt) error {
		defer inflightDone()
		nEdges++
		readyCh := seen[qe.m]
		if seen[qe.m] == nil {
			nNodes++
			bidiReadyCh := make(chan struct{})
			readyCh = bidiReadyCh
			seen[qe.m] = readyCh
			inflight.Add(1)
			gr.Go(func() error {
				defer inflightDone()
				descend := true
				if nodeVisit != nil {
					var err error
					slog.DebugContext(ctx, "walkGraph: visiting node", "node", qe.m)
					descend, err = nodeVisit(ctx, qe.m)
					slog.DebugContext(ctx, "walkGraph: done visiting node",
						"node", qe.m, "descend", descend, "err", err)
					if err != nil {
						return err
					}
				}
				close(bidiReadyCh)
				if descend {
					nDescends.Add(1)
					if load != nil {
						if err := load(ctx, qe.m); err != nil {
							return err
						}
					}
					for child, color := range edges(qe.m) {
						enqueue(qEnt{p: qe.m, m: child, color: color})
					}
				}
				return nil
			})
		}
		if edgeVisit != nil && qe.p != zeroN {
			inflight.Add(1)
			parentReadyCh := seen[qe.p]
			gr.Go(func() error {
				defer inflightDone()
				select {
				case <-ctx.Done():
					return context.Cause(ctx)
				case <-readyCh:
					select {
					case <-parentReadyCh:
					default:
						panic(fmt.Errorf("parent %v not visited before visiting edge to %v", qe.p, qe.m))
					}
					slog.DebugContext(ctx, "walkGraph: visiting edge",
						"parent", qe.p, "child", qe.m, "color", qe.color)
					err := edgeVisit(ctx, qe.p, qe.m, qe.color)
					slog.DebugContext(ctx, "walkGraph: done visiting edge",
						"parent", qe.p, "child", qe.m, "color", qe.color, "err", err)
					return err
				}
			})
		}
		return nil
	}
	enqueue(qEnt{p: zeroN, m: start, color: zeroE})
	gr.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return context.Cause(ctx)
			case qe, ok := <-q:
				if !ok {
					return nil
				}
				if err := process(qe); err != nil {
					return err
				}
			}
		}
	})
	return gr.Wait()
}

type walkGraphFn[N comparable, G, E any] = func(ctx context.Context, g G, start N,
	nodeVisit func(ctx context.Context, m N) (bool, error),
	edgeVisit func(ctx context.Context, p, m N, color E) error) error

func allNodes[N comparable, G, E any](ctx context.Context, g G, start N, walk walkGraphFn[N, G, E]) (iter.Seq[N], func() error) {
	stop := false
	var retErr error
	var mu sync.Mutex
	return func(yield func(N) bool) {
		retErr = walk(ctx, g, start,
			func(ctx context.Context, m N) (bool, error) {
				mu.Lock()
				defer mu.Unlock()
				if stop || !yield(m) {
					stop = true
					return false, walkStopErr
				}
				return true, nil
			},
			nil)
		if errors.Is(retErr, walkStopErr) {
			retErr = nil
		}
	}, func() error { return retErr }
}

type walkStopError struct{}

func (_ walkStopError) Error() string { return "stop" }

var walkStopErr error = walkStopError{}
