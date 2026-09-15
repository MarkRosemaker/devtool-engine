package depgraph_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MarkRosemaker/devtool-engine/depgraph"
)

func TestNew(t *testing.T) {
	t.Run("no deps leaves every key ready", func(t *testing.T) {
		g, err := depgraph.New([]string{"a/x", "a/y"}, nil)
		if err != nil {
			t.Fatal(err)
		}

		if got := len(started(t, g)); got != 2 {
			t.Errorf("expected both keys to start immediately, got %d", got)
		}
	})

	t.Run("cycle is rejected", func(t *testing.T) {
		_, err := depgraph.New([]string{"a/x", "a/y"}, map[string][]string{
			"a/x": {"a/y"},
			"a/y": {"a/x"},
		})
		if err == nil {
			t.Fatal("expected a cycle error, got nil")
		}
	})

	t.Run("self-dependency is ignored rather than read as a cycle", func(t *testing.T) {
		g, err := depgraph.New([]string{"a/x"}, map[string][]string{"a/x": {"a/x"}})
		if err != nil {
			t.Fatalf("a self-edge should not deadlock the graph: %v", err)
		}

		if got := order(t, g); !slices.Equal(got, []string{"a/x"}) {
			t.Errorf("got %v, want [a/x]", got)
		}
	})

	t.Run("dependency outside the key set is ignored", func(t *testing.T) {
		// Nothing can wait on a key that will never be processed, so an
		// unknown dependency must not hold its dependent back forever.
		g, err := depgraph.New([]string{"a/x"}, map[string][]string{"a/x": {"other/lib"}})
		if err != nil {
			t.Fatal(err)
		}

		if got := order(t, g); !slices.Equal(got, []string{"a/x"}) {
			t.Errorf("got %v, want [a/x]", got)
		}
	})
}

func TestRun(t *testing.T) {
	t.Run("linear chain runs in dependency order", func(t *testing.T) {
		g, err := depgraph.New(
			[]string{"a/base", "a/mid", "a/top"},
			map[string][]string{
				"a/mid": {"a/base"},
				"a/top": {"a/mid"},
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		want := []string{"a/base", "a/mid", "a/top"}
		if got := order(t, g); !slices.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("diamond runs the apex last", func(t *testing.T) {
		//   a/lib
		//   /   \
		// a/left a/right
		//   \   /
		//   a/top
		g, err := depgraph.New(
			[]string{"a/lib", "a/left", "a/right", "a/top"},
			map[string][]string{
				"a/left":  {"a/lib"},
				"a/right": {"a/lib"},
				"a/top":   {"a/left", "a/right"},
			},
		)
		if err != nil {
			t.Fatal(err)
		}

		got := order(t, g)
		if len(got) != 4 {
			t.Fatalf("got %d results, want 4", len(got))
		}

		if got[0] != "a/lib" {
			t.Errorf("a/lib should run first, got %q", got[0])
		}

		if got[3] != "a/top" {
			t.Errorf("a/top should run last, got %q", got[3])
		}
	})

	t.Run("every key is processed exactly once", func(t *testing.T) {
		keys := []string{"a/1", "a/2", "a/3", "a/4"}

		g, err := depgraph.New(keys, map[string][]string{"a/4": {"a/1", "a/2", "a/3"}})
		if err != nil {
			t.Fatal(err)
		}

		got := order(t, g)
		slices.Sort(got)

		if !slices.Equal(got, keys) {
			t.Errorf("got %v, want %v", got, keys)
		}
	})

	// The keys that start immediately are read from the same bookkeeping the
	// running goroutines update as they finish. A wide graph, where the first
	// keys finish while later ones are still being started, is what exposes an
	// unguarded overlap between the two. Run under -race.
	t.Run("starting and finishing do not race", func(t *testing.T) {
		const width = 200

		keys := make([]string, 0, width+1)
		for i := range width {
			keys = append(keys, fmt.Sprintf("a/%d", i))
		}

		// One key waits on every other, so decrements land throughout.
		keys = append(keys, "a/last")

		g, err := depgraph.New(keys, map[string][]string{"a/last": keys[:width]})
		if err != nil {
			t.Fatal(err)
		}

		var count atomic.Int64

		depgraph.Run(t.Context(), g,
			func(_ context.Context, key string) string {
				count.Add(1)

				return key
			}, nil)

		if got := count.Load(); got != int64(len(keys)) {
			t.Errorf("processed %d keys, want %d", got, len(keys))
		}
	})

	t.Run("onDone sees every result", func(t *testing.T) {
		g, err := depgraph.New([]string{"a/x", "a/y"}, map[string][]string{"a/y": {"a/x"}})
		if err != nil {
			t.Fatal(err)
		}

		var (
			mu   sync.Mutex
			seen []string
		)

		depgraph.Run(t.Context(), g,
			func(_ context.Context, key string) string { return key },
			func(key string) {
				mu.Lock()
				defer mu.Unlock()

				seen = append(seen, key)
			})

		if want := []string{"a/x", "a/y"}; !slices.Equal(seen, want) {
			t.Errorf("got %v, want %v", seen, want)
		}
	})
}

// order runs g, recording keys as they complete. Every node is serialised
// behind one mutex, so the recorded sequence is a valid topological order.
func order(t *testing.T, g *depgraph.Graph) []string {
	t.Helper()

	var (
		mu   sync.Mutex
		done []string
	)

	depgraph.Run(t.Context(), g, func(_ context.Context, key string) string {
		mu.Lock()
		defer mu.Unlock()

		done = append(done, key)

		return key
	}, nil)

	return done
}

// started reports the keys the graph is willing to begin with.
func started(t *testing.T, g *depgraph.Graph) []string {
	t.Helper()

	return order(t, g)
}
