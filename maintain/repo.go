package maintain

import (
	"context"
	"fmt"

	"github.com/spf13/afero"
)

// Repo is a repository this package can maintain.
//
// An interface rather than a concrete type, and a deliberately small one: it
// names what the machinery here actually uses and nothing else. The previous
// version embedded a repository library's concrete type and so inherited that
// library's whole surface, which made this package impossible to test without
// a live repository and impossible to lift out without taking the library
// along.
//
// The working tree is a method rather than an embedded afero.Fs, and that is
// the one part worth explaining. Embedding an interface promotes its own
// method set and nothing else, so a Repo would have satisfied afero.Fs while
// silently lacking whatever the concrete filesystem underneath could do — a
// task needing the real path behind a name once failed on every repository it
// was given while every test passed. Asking for the filesystem makes it a
// value with its real type, and there is nothing left to lose on the way.
type Repo interface {
	// Owner and Name identify the repository; String reads "owner/name".
	Owner() string
	Name() string
	String() string

	// Private reports whether the repository is visible only to its owner.
	Private() bool

	// Fs is the working tree, rooted at the repository.
	Fs() afero.Fs

	// ExecCommand runs a command in the repository's directory and returns
	// its combined output.
	ExecCommand(ctx context.Context, name string, args ...string) ([]byte, error)

	// Pull, Push, CommitAll and GetChangedFiles are the git operations a run
	// needs to do its work and record it.
	Pull(ctx context.Context) error
	Push(ctx context.Context) error
	CommitAll(msg string) error
	GetChangedFiles() ([]string, error)

	// HardReset and Clean return the worktree to a clean state. Named without
	// a reset mode, so this package needs no git library of its own for the
	// sake of one constant.
	HardReset() error
	Clean() error

	// IsGoRepo, GoModInit and GoTestCover are what a Go repository is asked
	// about itself.
	IsGoRepo() (bool, error)
	GoModInit(ctx context.Context) error
	GoTestCover(ctx context.Context) (float64, error)

	// SetDescription and SetTopics push metadata to wherever the repository
	// is hosted.
	SetDescription(ctx context.Context, descr string) error
	SetTopics(ctx context.Context, topics []string) error
}

// discardLocalChanges returns the worktree to a clean state, undoing anything
// an interrupted earlier run left behind.
func discardLocalChanges(repo Repo) error {
	files, err := repo.GetChangedFiles()
	if err != nil {
		return fmt.Errorf("getting changed files: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	if err := repo.HardReset(); err != nil {
		return fmt.Errorf("resetting git: %w", err)
	}

	if err := repo.Clean(); err != nil {
		return fmt.Errorf("cleaning git: %w", err)
	}

	if files, err = repo.GetChangedFiles(); err != nil {
		return fmt.Errorf("getting changed files after reset: %w", err)
	} else if len(files) > 0 {
		return fmt.Errorf("repository still has uncommitted changes after reset: %q", files)
	}

	return nil
}

// initGoModule turns an empty repository into a Go module and pushes the
// result.
func initGoModule(ctx context.Context, repo Repo) error {
	if err := repo.GoModInit(ctx); err != nil {
		return fmt.Errorf("initialising Go module: %w", err)
	}

	if err := repo.CommitAll("initial commit"); err != nil {
		return fmt.Errorf("committing initial commit: %w", err)
	}

	if err := repo.Push(ctx); err != nil {
		return fmt.Errorf("pushing initial commit: %w", err)
	}

	return nil
}
