package maintain

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the testdata golden files")

// recorder is an [Emitter] keeping what it was sent.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, ev)
}

// kinds returns the sequence of kinds, which is what most of these tests are
// actually about.
func (r *recorder) kinds() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	parts := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		parts = append(parts, string(ev.Kind))
	}

	return strings.Join(parts, ",")
}

func TestRunnerEmitsEvents(t *testing.T) {
	t.Run("a repository and each of its tasks are bracketed", func(t *testing.T) {
		repo := &fakeRepo{coverage: 96.4}
		rec := &recorder{}

		seq := func(*Runner, Repo, Spec) []Task {
			return []Task{
				{Name: "readme", Short: "readme", Run: func(context.Context) error {
					repo.dirty = true

					return nil
				}},
				{Name: "go vet", Short: "vet", Run: func(context.Context) error { return nil }},
			}
		}

		(&Runner{}).Update(t.Context(), repo, Spec{Coverage: 90}, seq, rec)

		want := "repo_start,task_start,task_done,task_start,task_done,repo_done"
		if got := rec.kinds(); got != want {
			t.Errorf("kinds =\n  %s\nwant\n  %s", got, want)
		}

		// The first task changed something and the second did not, which is
		// the distinction a reporter needs and the only thing a task's
		// outcome amounts to.
		var committed []string

		for _, ev := range rec.events {
			if ev.Kind == TaskDone && ev.Committed {
				committed = append(committed, ev.Task)
			}
		}

		if len(committed) != 1 || committed[0] != "readme" {
			t.Errorf("committed tasks = %v, want just [readme]", committed)
		}

		// RepoDone carries a whole row, so a consumer reading only that kind
		// still has everything the table shows.
		done := rec.events[len(rec.events)-1]
		if done.Coverage != 96.4 || done.PrevCoverage != 90 ||
			!done.Pushed || len(done.Commits) != 1 {
			t.Errorf("RepoDone = %+v", done)
		}

		// Every event says which repository it is about, since a run
		// interleaves several.
		for _, ev := range rec.events {
			if ev.Repo != "user/gorepo" {
				t.Errorf("event %s has repo %q", ev.Kind, ev.Repo)
			}

			if ev.Time.IsZero() {
				t.Errorf("event %s has no timestamp", ev.Kind)
			}
		}
	})

	t.Run("a failure is reported on RepoDone as text", func(t *testing.T) {
		rec := &recorder{}
		boom := errors.New("boom")

		(&Runner{}).Update(t.Context(), &fakeRepo{}, Spec{},
			func(*Runner, Repo, Spec) []Task {
				return []Task{{
					Name: "explode",
					Run:  func(context.Context) error { return boom },
				}}
			}, rec)

		done := rec.events[len(rec.events)-1]
		if done.Kind != RepoDone {
			t.Fatalf("last event is %s, want repo_done", done.Kind)
		}

		if !strings.Contains(done.Err, "boom") {
			t.Errorf("Err = %q, want it to mention the failure", done.Err)
		}
	})

	// Nil is the documented way to say "do not report", and it has to be safe
	// rather than merely tolerated: a Runner with no reporter is how the
	// engine's own tests drive it.
	t.Run("no emitter is not a crash", func(t *testing.T) {
		if res := (&Runner{}).Update(t.Context(), &fakeRepo{}, Spec{},
			noopTasks("only"), nil); res.Err != nil {
			t.Fatal(res.Err)
		}
	})
}

// TestEventsRoundTrip is the contract check that matters once the two halves
// are in different processes: what the emitter writes is what the reader
// reads.
func TestEventsRoundTrip(t *testing.T) {
	repo := &fakeRepo{coverage: 88.1}

	buf := &bytes.Buffer{}

	seq := func(*Runner, Repo, Spec) []Task {
		return []Task{{Name: "readme", Short: "readme", Run: func(context.Context) error {
			repo.dirty = true

			return nil
		}}}
	}

	(&Runner{}).Update(t.Context(), repo, Spec{Coverage: 80}, seq,
		NewJSONLEmitter(buf))

	// One object per line, so a person can read a run off a terminal.
	lines := strings.Count(strings.TrimSuffix(buf.String(), "\n"), "\n") + 1

	var got []Event
	if err := ReadEvents(buf, func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatal(err)
	}

	if len(got) != lines {
		t.Errorf("read %d events from %d lines", len(got), lines)
	}

	done := got[len(got)-1]
	if done.Kind != RepoDone || done.Coverage != 88.1 || done.PrevCoverage != 80 {
		t.Errorf("RepoDone did not survive the trip: %+v", done)
	}

	if !done.Pushed || len(done.Commits) != 1 || done.Commits[0] != "readme" {
		t.Errorf("commits did not survive the trip: %+v", done)
	}
}

// A failure off the wire renders as the run stated it, and is still
// identifiable as having come from there. Both halves matter: the first keeps
// the table's limited room for the message, the second lets a consumer tell a
// reported failure from one raised locally.
func TestRemoteErrorKeepsItsMessage(t *testing.T) {
	res := Event{
		Kind: RepoDone, Repo: "user/gorepo",
		Err: "pulling latest changes: timeout",
	}.Result()

	if got := res.Err.Error(); got != "pulling latest changes: timeout" {
		t.Errorf("Error() = %q, want the message verbatim", got)
	}

	if !errors.Is(res.Err, ErrRemote) {
		t.Error("want errors.Is(err, ErrRemote) to hold")
	}

	if got := res.notes(); !strings.HasPrefix(got, "ERR: pulling") {
		t.Errorf("notes() = %q, want no provenance in the table", got)
	}
}

func TestReadEventsRejectsGarbage(t *testing.T) {
	err := ReadEvents(strings.NewReader("{\"kind\":\"repo_start\"}\nnot json\n"),
		func(Event) {})
	if err == nil {
		t.Fatal("expected an error for a line that is not an event")
	}
}

// TestBoardFromEventsMatchesResults is the acceptance check for making the
// table event-driven: for the same run, a board fed events renders exactly
// what a board fed results renders.
//
// Byte-identical, because the point of this change was to move where the
// table's inputs come from without changing the table.
func TestBoardFromEventsMatchesResults(t *testing.T) {
	rows := []Result{
		{Owner: "user", Name: "alpha"},
		{Owner: "user", Name: "beta"},
		{Owner: "user", Name: "gamma"},
	}

	results := []Result{
		{
			Owner: "user", Name: "alpha", Coverage: 96.4, PrevCoverage: 90,
			Commits: []string{"readme", "vet"}, Pushed: true,
		},
		{Owner: "user", Name: "beta", Coverage: 71.7, PrevCoverage: 71.7},
		{Owner: "user", Name: "gamma", Err: errors.New("pulling latest changes: timeout")},
	}

	fromResults := NewBoard(rows)
	for _, res := range results {
		fromResults.Set(res)
	}

	fromEvents := NewBoard(rows)

	for _, res := range results {
		ev := Event{
			Kind: RepoDone, Repo: res.Key(),
			Coverage: res.Coverage, PrevCoverage: res.PrevCoverage,
			Commits: res.Commits, Pushed: res.Pushed,
		}

		if res.Err != nil {
			ev.Err = res.Err.Error()
		}

		fromEvents.Apply(ev)
	}

	got, want := fromEvents.Render(), fromResults.Render()
	if got != want {
		t.Errorf("the event-driven table differs:\n--- events ---\n%s\n--- results ---\n%s",
			got, want)
	}
}

// TestBoardFromEventStreamGolden renders a recorded stream, so a change to
// either the event contract or the table is a reviewable diff. Run
// "go test ./maintain -update" to rewrite it.
func TestBoardFromEventStreamGolden(t *testing.T) {
	const stream = "testdata/run.jsonl"

	f, err := os.Open(stream)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	board := NewBoard([]Result{
		{Owner: "user", Name: "alpha"},
		{Owner: "user", Name: "beta"},
		{Owner: "user", Name: "gamma"},
	})

	if err := ReadEvents(f, board.Apply); err != nil {
		t.Fatal(err)
	}

	got := []byte(board.Render())
	golden := filepath.Join("testdata", "run.table.golden")

	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}

		return
	}

	wantBytes, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./maintain -update)", err)
	}

	if string(got) != string(wantBytes) {
		t.Errorf("%s does not match:\n--- got ---\n%s\n--- want ---\n%s",
			golden, got, wantBytes)
	}
}
