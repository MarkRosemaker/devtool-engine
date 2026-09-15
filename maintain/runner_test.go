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
	t.Run("changes left over from before are discarded first", func(t *testing.T) {
		repo := &fakeRepo{dirty: true}

		if res := (&Runner{}).Update(t.Context(), repo, Spec{},
			func(*Runner, Repo, Spec) []Task { return nil }, nil); res.Err != nil {
			t.Fatal(res.Err)
		}

		want := "description,topics,reset,clean,pull,test,test"
		if got := strings.Join(repo.calls, ","); got != want {
			t.Errorf("calls =\n  %s\nwant\n  %s", got, want)
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
