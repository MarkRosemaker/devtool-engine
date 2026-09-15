package maintain_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/MarkRosemaker/devtool-engine/maintain"
)

func TestResultNotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  maintain.Result
		want string
	}{
		{
			name: "pending",
			res:  maintain.Result{Pending: true},
			want: "⏳",
		},
		{
			name: "nothing to do",
			res:  maintain.Result{Coverage: 80},
			want: "—",
		},
		{
			name: "commits without a coverage change",
			res:  maintain.Result{Commits: []string{"fmt", "vet"}, PrevCoverage: 80, Coverage: 80},
			want: "fmt, vet → pushed",
		},
		{
			name: "commits with a coverage gain",
			res:  maintain.Result{Commits: []string{"fmt"}, PrevCoverage: 80, Coverage: 85.5},
			want: "fmt → pushed (+5.5%)",
		},
		{
			name: "commits with a coverage loss",
			res:  maintain.Result{Commits: []string{"fmt"}, PrevCoverage: 80, Coverage: 75},
			want: "fmt → pushed (-5.0%)",
		},
		{
			name: "no delta without a previous figure to compare against",
			res:  maintain.Result{Commits: []string{"fmt"}, PrevCoverage: 0, Coverage: 42},
			want: "fmt → pushed",
		},
		{
			name: "error",
			res:  maintain.Result{Err: errors.New("boom")},
			want: "ERR: boom",
		},
		{
			name: "a long error is truncated to keep the table readable",
			res:  maintain.Result{Err: errors.New(strings.Repeat("x", 100))},
			want: "ERR: " + strings.Repeat("x", 60) + "…",
		},
		{
			name: "an error outranks the commits that preceded it",
			res:  maintain.Result{Commits: []string{"fmt"}, Err: errors.New("boom")},
			want: "ERR: boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// notes is unexported, so go through the table it feeds.
			table := maintain.RenderTable([]maintain.Result{tc.res})
			if !strings.Contains(table, tc.want) {
				t.Errorf("table\n%s\ndoes not contain %q", table, tc.want)
			}
		})
	}
}

func TestResultErrorMessage(t *testing.T) {
	t.Run("no error yields no message", func(t *testing.T) {
		if got := (maintain.Result{}).ErrorMessage(); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("markup in an error is escaped rather than sent as HTML", func(t *testing.T) {
		res := maintain.Result{
			Owner: "o", Name: "n",
			Err: errors.New("got <b>value</b> & more"),
		}

		got := res.ErrorMessage()

		if strings.Contains(got, "<b>value</b>") {
			t.Errorf("error markup reached the message unescaped: %q", got)
		}

		for _, want := range []string{"&lt;b&gt;value&lt;/b&gt;", "&amp; more", "<b>o/n</b>"} {
			if !strings.Contains(got, want) {
				t.Errorf("got %q, want it to contain %q", got, want)
			}
		}
	})
}

func TestBoard(t *testing.T) {
	rows := []maintain.Result{
		{Owner: "o", Name: "first"},
		{Owner: "o", Name: "second"},
		{Owner: "o", Name: "third"},
	}

	t.Run("every row starts pending", func(t *testing.T) {
		for i, res := range maintain.NewBoard(rows).Results() {
			if !res.Pending {
				t.Errorf("row %d is not pending", i)
			}
		}
	})

	t.Run("results keep board order, not arrival order", func(t *testing.T) {
		b := maintain.NewBoard(rows)

		b.Set(maintain.Result{Owner: "o", Name: "third", Coverage: 30})
		b.Set(maintain.Result{Owner: "o", Name: "first", Coverage: 10})

		got := b.Results()

		for i, want := range []string{"first", "second", "third"} {
			if got[i].Name != want {
				t.Errorf("row %d is %q, want %q", i, got[i].Name, want)
			}
		}

		if got[1].Pending != true {
			t.Error("the row nothing was set for should still be pending")
		}
	})

	t.Run("a result for an unknown repository is ignored", func(t *testing.T) {
		b := maintain.NewBoard(rows)
		b.Set(maintain.Result{Owner: "o", Name: "stranger"})

		if got := len(b.Results()); got != len(rows) {
			t.Errorf("board grew to %d rows, want %d", got, len(rows))
		}
	})

	t.Run("Results returns a copy the caller cannot corrupt the board with", func(t *testing.T) {
		b := maintain.NewBoard(rows)

		b.Results()[0].Name = "mutated"

		if got := b.Results()[0].Name; got != "first" {
			t.Errorf("board row was mutated through the returned slice: got %q", got)
		}
	})

	t.Run("concurrent updates are safe", func(t *testing.T) {
		b := maintain.NewBoard(rows)

		var wg sync.WaitGroup
		for _, row := range rows {
			wg.Go(func() {
				b.Set(maintain.Result{Owner: row.Owner, Name: row.Name, Coverage: 50})
				b.Render()
			})
		}

		wg.Wait()

		for _, res := range b.Results() {
			if res.Pending {
				t.Errorf("%s is still pending after being set", res.Name)
			}
		}
	})
}

func TestKey(t *testing.T) {
	if got, want := maintain.Key("owner", "name"), "owner/name"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if got, want := (maintain.Result{Owner: "o", Name: "n"}).Key(), "o/n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResultNotable(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  maintain.Result
		want bool
	}{
		{"quiet run", maintain.Result{Coverage: 80}, false},
		{"pushed", maintain.Result{Pushed: true}, true},
		{"failed", maintain.Result{Err: errors.New("boom")}, true},
		// Commits that were made but never reached the remote are not worth
		// reporting on their own: the push is what makes them visible.
		{"committed but not pushed", maintain.Result{Commits: []string{"fmt"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Notable(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
