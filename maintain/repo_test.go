package maintain

import (
	"context"

	"github.com/spf13/afero"
)

// fakeRepo is a [Repo] for tests: a filesystem, some facts about itself, and
// a record of what was asked of it.
//
// This type is the point of Repo being an interface. Before it, a test had to
// build a real repository from the library the engine embedded, which is why
// nothing here tested [Runner] at all and why the task tests ran against a
// filesystem the run never actually hands them.
type fakeRepo struct {
	fs      afero.Fs
	private bool

	// dirty says the worktree has uncommitted changes. A test's task sets
	// it, a commit or a reset clears it — the same shape as a real worktree,
	// which is what the runner's decisions are actually reading.
	dirty bool

	coverage float64

	// changed is what GetChangedFiles reports while the worktree is dirty,
	// so a test can say which files a task touched and not only that it did.
	changed []string

	// calls records every method with an effect, in the order it was called.
	calls []string
}

func (r *fakeRepo) Owner() string  { return "user" }
func (r *fakeRepo) Name() string   { return "gorepo" }
func (r *fakeRepo) String() string { return "user/gorepo" }
func (r *fakeRepo) Private() bool  { return r.private }

func (r *fakeRepo) Fs() afero.Fs {
	if r.fs == nil {
		r.fs = afero.NewMemMapFs()
	}

	return r.fs
}

func (r *fakeRepo) record(name string) { r.calls = append(r.calls, name) }

func (r *fakeRepo) ExecCommand(_ context.Context, name string, _ ...string) ([]byte, error) {
	r.record("exec " + name)

	return nil, nil
}

func (r *fakeRepo) Pull(context.Context) error { r.record("pull"); return nil }
func (r *fakeRepo) Push(context.Context) error { r.record("push"); return nil }
func (r *fakeRepo) CommitAll(string) error {
	r.record("commit")
	r.dirty = false

	return nil
}

func (r *fakeRepo) HardReset() error {
	r.record("reset")
	r.dirty = false

	return nil
}

func (r *fakeRepo) Clean() error { r.record("clean"); return nil }
func (r *fakeRepo) GoModInit(context.Context) error {
	r.record("mod init")

	return nil
}

func (r *fakeRepo) IsGoRepo() (bool, error) { return true, nil }

func (r *fakeRepo) GoTestCover(context.Context) (float64, error) {
	r.record("test")

	return r.coverage, nil
}

func (r *fakeRepo) SetDescription(context.Context, string) error {
	r.record("description")

	return nil
}

func (r *fakeRepo) SetTopics(context.Context, []string) error {
	r.record("topics")

	return nil
}

// GetChangedFiles is what the runner reads to decide whether a task did
// anything, so it reports the worktree rather than a fixed answer.
func (r *fakeRepo) GetChangedFiles() ([]string, error) {
	if !r.dirty {
		return nil, nil
	}

	if r.changed != nil {
		return r.changed, nil
	}

	return []string{"changed.go"}, nil
}
