package maintain

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/MarkRosemaker/devtool-engine/event"
)

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

		// Asserted as properties rather than as one expected string: the
		// sequence is the caller's and the steps around it are the runner's,
		// so a literal would have to be rewritten every time either changed
		// and would say nothing about what a reader depends on.
		//
		// Every step is bracketed. A start with no done is a step a reader
		// would show as running for the rest of the run.
		open := map[string]int{}

		for _, ev := range rec.events {
			switch ev.Kind {
			case TaskStart:
				open[ev.Task]++
			case TaskDone:
				open[ev.Task]--
			default:
			}
		}

		for task, n := range open {
			if n != 0 {
				t.Errorf("step %q has %d starts left unfinished", task, n)
			}
		}

		// The work the runner does around the sequence is reported too: these
		// are most of a repository's time, and a reader watching only the
		// caller's tasks watches the fast part.
		for _, want := range []string{"prepare", "test", "push"} {
			if !started(rec.events, want) {
				t.Errorf("no %q step was reported", want)
			}
		}

		// Positions are 1-based and within the count, including the test a
		// task triggers, which keeps its parent's position.
		for _, ev := range rec.events {
			if ev.Kind != TaskStart && ev.Kind != TaskDone {
				continue
			}

			if ev.TaskIndex < 1 || ev.TaskIndex > ev.TaskCount {
				t.Errorf("step %q is %d of %d", ev.Task, ev.TaskIndex, ev.TaskCount)
			}
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

		// The push is announced when it happens, not only inside RepoDone,
		// which can be a whole test suite later.
		var pushed *Event

		for i, ev := range rec.events {
			if ev.Kind == RepoPushed {
				pushed = &rec.events[i]
			}
		}

		if pushed == nil {
			t.Fatal("no repo_pushed event")
		}

		if len(pushed.Commits) != 1 || pushed.Commits[0] != "readme" {
			t.Errorf("repo_pushed carried %v, want the commits", pushed.Commits)
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

		// The step that failed is named on the wire. Without this a reader
		// has to find it in the error's prose, which is a sentence the task
		// wrote and nobody promised to keep parseable.
		var failed *Event

		for i, ev := range rec.events {
			if ev.Kind == TaskDone && ev.Err != "" {
				failed = &rec.events[i]
			}
		}

		if failed == nil {
			t.Fatal("no task_done named the step that failed")
		}

		if failed.Task != "explode" {
			t.Errorf("the failing step is named %q, want explode", failed.Task)
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
		event.Write(buf))

	// One object per line, so a person can read a run off a terminal.
	lines := strings.Count(strings.TrimSuffix(buf.String(), "\n"), "\n") + 1

	var got []Event
	if err := event.Read(buf, func(ev Event) { got = append(got, ev) }); err != nil {
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

// started reports whether any step with this label began.
func started(events []Event, task string) bool {
	for _, ev := range events {
		if ev.Kind == TaskStart && ev.Task == task {
			return true
		}
	}

	return false
}
