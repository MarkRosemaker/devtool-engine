package maintain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// noTasks is a sequence with nothing in it, for tests about what happens
// around the tasks.
func noTasks(*Runner, Repo, Spec) []Task { return nil }

// A repository with no Go module is given one, committed and pushed before
// it is tested, so it exists on the remote as a module from the first run.
func TestARepositoryWithoutAModuleIsGivenOne(t *testing.T) {
	repo := &fakeRepo{notGo: true}

	res := (&Runner{}).Update(t.Context(), repo, Spec{}, noTasks, nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}

	if got, want := strings.Join(repo.calls, ","), "pull,mod init,commit,push"; !strings.HasPrefix(got, want) {
		t.Errorf("calls = %s, want them to start %s", got, want)
	}
}

// A step of preparing that fails stops the run there, says which step it was,
// and changes nothing on the remote.
func TestAFailedPreparationSaysWhatFailed(t *testing.T) {
	boom := errors.New("boom")

	for _, tc := range []struct {
		call, want string
		notGo      bool
	}{
		{call: "description", want: "setting description"},
		{call: "topics", want: "setting topics"},
		{call: "pull", want: "pulling latest changes"},
		{call: "reset", want: "resetting git"},
		{call: "clean", want: "cleaning git"},
		{call: "is go", want: "checking for a Go module"},
		{call: "mod init", want: "initialising Go module", notGo: true},
		{call: "commit", want: "committing initial commit", notGo: true},
		{call: "push", want: "pushing initial commit", notGo: true},
	} {
		t.Run(tc.call, func(t *testing.T) {
			repo := &fakeRepo{
				dirty: true, notGo: tc.notGo,
				fail: map[string]error{tc.call: boom},
			}
			spec := Spec{Description: "A thing.", Topics: []string{"go"}}

			ran := false
			seq := func(*Runner, Repo, Spec) []Task {
				return []Task{{Name: "task", Run: func(context.Context) error { ran = true; return nil }}}
			}

			res := (&Runner{}).Update(t.Context(), repo, spec, seq, nil)

			if !errors.Is(res.Err, boom) || !strings.Contains(res.Err.Error(), tc.want) {
				t.Errorf("Err = %v, want it to say %q and wrap the cause", res.Err, tc.want)
			}

			if ran || res.Pushed {
				t.Errorf("ran a task (%v) or pushed (%v) after preparing failed", ran, res.Pushed)
			}

			for _, c := range repo.calls {
				if c == "test" {
					t.Errorf("tested after preparing failed: %v", repo.calls)
				}
			}
		})
	}
}

// Changes a reset cannot clear are not committed as this run's work: the run
// stops, naming them.
func TestChangesThatSurviveAResetStopTheRun(t *testing.T) {
	repo := &fakeRepo{dirty: true, stuck: true, changed: []string{"left.go"}}

	res := (&Runner{}).Update(t.Context(), repo, Spec{}, noTasks, nil)

	if res.Err == nil || !strings.Contains(res.Err.Error(), `still has uncommitted changes after reset: ["left.go"]`) {
		t.Errorf("Err = %v, want the files that survived named", res.Err)
	}
}

// Everything that lints holds one lock, so two never run at once: a
// serialised task, and the tests of a repository whose tests lint.
func TestLintingIsSerialised(t *testing.T) {
	r := &Runner{}

	var running, most atomic.Int32

	lint := func(context.Context) error {
		n := running.Add(1)
		defer running.Add(-1)

		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}

		time.Sleep(5 * time.Millisecond)

		return nil
	}

	repo := &lintingRepo{lint: lint}

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() { _ = r.Serialise(lint)(t.Context()) })
		wg.Go(func() { _, _ = r.TestCover(t.Context(), repo, Spec{LintInTests: true}) })
	}

	wg.Wait()

	if m := most.Load(); m != 1 {
		t.Errorf("%d ran at once, want 1", m)
	}
}

// lintingRepo is a repository whose tests run the linter.
type lintingRepo struct {
	fakeRepo

	lint func(context.Context) error
}

func (r *lintingRepo) GoTestCover(ctx context.Context) (float64, error) {
	return 0, r.lint(ctx)
}

// The lint config the caller gives is the only one the repository keeps.
func TestGenLintfile(t *testing.T) {
	repo := &fakeRepo{}
	fs := repo.Fs()

	for _, name := range []string{".golangci.yml", ".golangci.toml", ".golangci.json"} {
		if err := afero.WriteFile(fs, name, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	settings := []byte("version: \"2\"\n")
	if err := GenLintfile(repo, settings).Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, err := afero.ReadFile(fs, ".golangci.yaml")
	if err != nil || string(got) != string(settings) {
		t.Errorf(".golangci.yaml = %q (%v), want the settings given", got, err)
	}

	for _, name := range []string{".golangci.yml", ".golangci.toml", ".golangci.json"} {
		if ok, _ := afero.Exists(fs, name); ok {
			t.Errorf("%s is still there beside the generated config", name)
		}
	}

	// A second run with nothing else to remove changes nothing.
	if err := GenLintfile(repo, settings).Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}
