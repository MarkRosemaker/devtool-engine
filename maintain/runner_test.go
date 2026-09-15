package maintain

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRunnerUpdate drives [Runner.Update] end to end against a fake
// repository: no disk, no git, no GitHub.
//
// This is the test the roadmap said the interface change would unblock. The
// runner's decision — run the task, ask whether anything changed, test, then
// commit — had no test at all, because building a repository to hand it meant
// a live one.
func TestRunnerUpdate(t *testing.T) {
	t.Run("a task that changes something is tested, committed and pushed", func(t *testing.T) {
		repo := &fakeRepo{coverage: 96.4}

		// Only the first task changes anything, so only it should produce a
		// commit and the second should pass silently.
		seq := func(*Runner, Repo, Spec) []Task {
			return []Task{
				{Name: "first", Run: func(context.Context) error { repo.dirty = true; return nil }},
				{Name: "second", Run: func(context.Context) error { return nil }},
			}
		}

		res := (&Runner{}).Update(t.Context(), repo, Spec{Coverage: 90}, seq, nil)

		if res.Err != nil {
			t.Fatal(res.Err)
		}

		if want := []string{"first"}; !equal(res.Commits, want) {
			t.Errorf("Commits = %v, want %v", res.Commits, want)
		}

		if !res.Pushed {
			t.Error("want a push, since something was committed")
		}

		if res.Coverage != 96.4 || res.PrevCoverage != 90 {
			t.Errorf("Coverage = %v, PrevCoverage = %v", res.Coverage, res.PrevCoverage)
		}

		// The order is the load-bearing part: metadata and a clean worktree
		// first, a test before anything changes, then a test before each
		// commit, and the push only once.
		want := "description,topics,pull,test,test,commit,push,test"
		if got := strings.Join(repo.calls, ","); got != want {
			t.Errorf("calls =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("a run that changes nothing commits nothing and does not push", func(t *testing.T) {
		repo := &fakeRepo{}

		res := (&Runner{}).Update(t.Context(), repo, Spec{}, noopTasks("first", "second"), nil)

		if res.Err != nil {
			t.Fatal(res.Err)
		}

		if len(res.Commits) != 0 {
			t.Errorf("Commits = %v, want none", res.Commits)
		}

		if res.Pushed {
			t.Error("nothing was committed, so nothing should have been pushed")
		}

		for _, unwanted := range []string{"commit", "push"} {
			if strings.Contains(strings.Join(repo.calls, ","), unwanted) {
				t.Errorf("%q should not have happened: %v", unwanted, repo.calls)
			}
		}
	})

	// A failure is an outcome to report, not something that stops a run
	// covering many repositories — so it comes back inside the Result.
	t.Run("a failing task is reported in the result, not returned", func(t *testing.T) {
		repo := &fakeRepo{}
		boom := errors.New("boom")

		res := (&Runner{}).Update(t.Context(), repo, Spec{}, func(*Runner, Repo, Spec) []Task {
			return []Task{{
				Name: "explode",
				Run:  func(context.Context) error { return boom },
			}}
		}, nil)

		if !errors.Is(res.Err, boom) {
			t.Errorf("Err = %v, want it to wrap %v", res.Err, boom)
		}

		if res.Pushed {
			t.Error("a failed run should not push")
		}
	})

	// The worktree an interrupted earlier run left behind is cleaned before
	// anything else, rather than being committed as though this run did it.
	//
	// It also fixes the quiet case: nothing was committed, so the one test on
	// the way in is the only one there is.
	t.Run("changes left over from before are discarded first", func(t *testing.T) {
		repo := &fakeRepo{dirty: true}

		if res := (&Runner{}).Update(t.Context(), repo, Spec{},
			func(*Runner, Repo, Spec) []Task { return nil }, nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		want := "description,topics,reset,clean,pull,test"
		if got := strings.Join(repo.calls, ","); got != want {
			t.Errorf("calls =\n  %s\nwant\n  %s", got, want)
		}
	})
}

// TestRunnerTestsOnceWhenNothingChanges is the difference between one test run
// per repository and two. A repository where no task committed is the same
// code the run tested on its way in, so measuring coverage again would run the
// whole suite to arrive at the figure already in hand — and most repositories
// are quiet most runs.
func TestRunnerTestsOnceWhenNothingChanges(t *testing.T) {
	t.Run("nothing changed", func(t *testing.T) {
		repo := &fakeRepo{coverage: 71.7}

		res := (&Runner{}).Update(t.Context(), repo, Spec{},
			noopTasks("readme", "makefile"), nil)
		if res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 1 {
			t.Errorf("tested %d times, want 1: %v", n, repo.calls)
		}

		// The figure still has to be reported, from the run on the way in.
		if res.Coverage != 71.7 {
			t.Errorf("Coverage = %v, want the figure measured on the way in", res.Coverage)
		}
	})

	// Something was committed, so the code at the end is not the code tested
	// on the way in and the figure has to be measured again.
	t.Run("something changed", func(t *testing.T) {
		repo := &fakeRepo{coverage: 88.1}

		res := (&Runner{}).Update(t.Context(), repo, Spec{},
			func(*Runner, Repo, Spec) []Task {
				return []Task{{Name: "readme", Run: func(context.Context) error {
					repo.dirty = true

					return nil
				}}}
			}, nil)
		if res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 3 {
			t.Errorf("tested %d times, want 3 (in, after the task, at the end): %v",
				n, repo.calls)
		}
	})
}

// noopTasks is a sequence of tasks that succeed without touching anything,
// so the runner should find the worktree clean after every one.
func noopTasks(names ...string) Sequence {
	return func(*Runner, Repo, Spec) []Task {
		tasks := make([]Task, 0, len(names))
		for _, name := range names {
			tasks = append(tasks, Task{
				Name: name,
				Run:  func(context.Context) error { return nil },
			})
		}

		return tasks
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}

// TestRunnerSkipsTestsForInertChanges: most runs change only a README or a
// Makefile, and those cannot alter what the tests do. A caller that says so
// gets no test run for them, before the commit or after it.
func TestRunnerSkipsTestsForInertChanges(t *testing.T) {
	// inert is what a generator of documentation would supply.
	inert := func(path string) bool {
		return path == "README.md" || path == "Makefile" ||
			strings.HasPrefix(path, "README/")
	}

	// touching makes a task that dirties the worktree, reporting the given
	// files as changed.
	touching := func(repo *fakeRepo, files ...string) Sequence {
		return func(*Runner, Repo, Spec) []Task {
			return []Task{{Name: "write", Run: func(context.Context) error {
				repo.dirty = true
				repo.changed = files

				return nil
			}}}
		}
	}

	t.Run("only inert files: no test at all", func(t *testing.T) {
		repo := &fakeRepo{coverage: 71.7}
		r := &Runner{Inert: inert}

		res := r.Update(t.Context(), repo, Spec{},
			touching(repo, "README.md", "README/description.md"), nil)
		if res.Err != nil {
			t.Fatal(res.Err)
		}

		// One on the way in, and no more: nothing committed could have moved
		// the figure.
		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 1 {
			t.Errorf("tested %d times, want 1: %v", n, repo.calls)
		}

		if !res.Pushed {
			t.Error("the inert change should still be committed and pushed")
		}

		if res.Coverage != 71.7 {
			t.Errorf("Coverage = %v, want the figure from the way in", res.Coverage)
		}
	})

	t.Run("a Go file among them: tested", func(t *testing.T) {
		repo := &fakeRepo{coverage: 88.1}
		r := &Runner{Inert: inert}

		if res := r.Update(t.Context(), repo, Spec{},
			touching(repo, "README.md", "thing.go"), nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 3 {
			t.Errorf("tested %d times, want 3: %v", n, repo.calls)
		}
	})

	t.Run("a file nobody claims: tested", func(t *testing.T) {
		repo := &fakeRepo{coverage: 50}
		r := &Runner{Inert: inert}

		if res := r.Update(t.Context(), repo, Spec{},
			touching(repo, "assets/banner.png"), nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 3 {
			t.Errorf("tested %d times, want 3 — an unclaimed file could be embedded: %v",
				n, repo.calls)
		}
	})

	// A repository whose own tests run the linter can be moved by anything the
	// linter reads, so nothing there is inert.
	t.Run("LintInTests: nothing is inert", func(t *testing.T) {
		repo := &fakeRepo{coverage: 50}
		r := &Runner{Inert: inert}

		if res := r.Update(t.Context(), repo, Spec{LintInTests: true},
			touching(repo, "README.md"), nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 3 {
			t.Errorf("tested %d times, want 3: %v", n, repo.calls)
		}
	})

	// Without a predicate the runner cannot know, so it tests as it always did.
	t.Run("no predicate: tested", func(t *testing.T) {
		repo := &fakeRepo{coverage: 50}

		if res := (&Runner{}).Update(t.Context(), repo, Spec{},
			touching(repo, "README.md"), nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		if n := strings.Count(strings.Join(repo.calls, ","), "test"); n != 3 {
			t.Errorf("tested %d times, want 3: %v", n, repo.calls)
		}
	})
}
