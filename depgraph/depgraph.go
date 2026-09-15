// Package depgraph orders work by its dependencies and runs it in parallel.
//
// A Graph is built once from a set of node keys and the edges between them, and
// rejects cycles at that point, so callers never have to defend against one
// later. [Run] then walks the graph, starting every node whose dependencies have
// finished and keeping as many in flight as the shape of the graph allows.
//
// The package knows nothing about what a node is. Keys are opaque strings, and
// the work done for each one is supplied by the caller.
package depgraph

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

// Graph is an acyclic dependency graph over string keys.
//
// The zero value is not usable; build one with [New].
type Graph struct {
	keys       []string
	dependents map[string][]string // key → keys that wait on it
	inDegree   map[string]int      // key → number of keys it waits on
}

// New builds a graph over keys, where deps[k] lists the keys k depends on.
//
// Keys appearing only in deps are ignored: a dependency on something outside the
// set cannot be waited for, so it cannot constrain the order. An edge that would
// close a cycle is not resolvable in any order, so New reports it as an error
// rather than picking one arbitrarily.
func New(keys []string, deps map[string][]string) (*Graph, error) {
	known := make(map[string]bool, len(keys))
	for _, key := range keys {
		known[key] = true
	}

	g := &Graph{
		keys:       keys,
		dependents: make(map[string][]string, len(keys)),
		inDegree:   make(map[string]int, len(keys)),
	}

	for _, key := range keys {
		g.inDegree[key] += 0 // ensure every key is present, even with no edges

		for _, dep := range deps[key] {
			if !known[dep] || dep == key {
				continue
			}

			g.dependents[dep] = append(g.dependents[dep], key)
			g.inDegree[key]++
		}
	}

	if cycle := g.findCycle(); len(cycle) > 0 {
		return nil, fmt.Errorf("dependency cycle: %v", cycle)
	}

	return g, nil
}

// findCycle returns the keys that cannot be reached in any valid order, or nil
// when the graph is acyclic.
//
// Kahn's algorithm drains every node whose remaining dependencies are satisfied.
// Whatever it cannot drain is exactly the set of nodes waiting, directly or
// transitively, on a cycle.
func (g *Graph) findCycle() []string {
	remaining := make(map[string]int, len(g.inDegree))
	maps.Copy(remaining, g.inDegree)

	queue := make([]string, 0, len(g.keys))
	for _, key := range g.keys {
		if remaining[key] == 0 {
			queue = append(queue, key)
		}
	}

	drained := make(map[string]bool, len(g.keys))
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		drained[key] = true

		for _, dependent := range g.dependents[key] {
			if remaining[dependent]--; remaining[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	var stuck []string
	for _, key := range g.keys {
		if !drained[key] {
			stuck = append(stuck, key)
		}
	}

	return stuck
}

// Keys returns the graph's nodes in the order they were given to [New].
func (g *Graph) Keys() []string { return g.keys }

// Run calls process once for every key, in an order that respects the graph:
// a key starts only once everything it depends on has finished. Keys whose
// dependencies are all satisfied run concurrently.
//
// onDone, when non-nil, is called with each result as it arrives, before the
// keys it unblocks are started. It runs on the worker's goroutine and may be
// called concurrently, so it must be safe for concurrent use.
//
// Run blocks until every key has been processed. Results come back in
// completion order; sort them if a stable order matters.
func Run[T any](
	ctx context.Context,
	g *Graph,
	process func(ctx context.Context, key string) T,
	onDone func(T),
) []T {
	remaining := make(map[string]int, len(g.inDegree))
	maps.Copy(remaining, g.inDegree)

	var (
		mu      sync.Mutex // guards remaining
		wg      sync.WaitGroup
		results = make(chan T, len(g.keys))
	)

	var start func(key string)

	start = func(key string) {
		wg.Go(func() {
			result := process(ctx, key)

			if onDone != nil {
				onDone(result)
			}

			results <- result

			// Unblocking dependents inside the lock, before this goroutine
			// returns, is what keeps wg.Wait honest: every successor is
			// registered with the group before its predecessor is done.
			mu.Lock()
			defer mu.Unlock()

			for _, dependent := range g.dependents[key] {
				if remaining[dependent]--; remaining[dependent] == 0 {
					start(dependent)
				}
			}
		})
	}

	// Collect the ready keys before starting any of them. Reading the map here,
	// while nothing is running yet, is what keeps it single-threaded: once the
	// first goroutine exists, every access to remaining is under the lock.
	ready := make([]string, 0, len(g.keys))

	for _, key := range g.keys {
		if remaining[key] == 0 {
			ready = append(ready, key)
		}
	}

	for _, key := range ready {
		start(key)
	}

	wg.Wait()
	close(results)

	collected := make([]T, 0, len(g.keys))
	for result := range results {
		collected = append(collected, result)
	}

	return collected
}
