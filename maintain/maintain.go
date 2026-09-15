// Package maintain brings Go repositories up to a standard and keeps them there.
//
// A [Task] is one named unit of work on a repository — format the code, update
// the dependencies, write a licence. A [Runner] applies a sequence of them to a
// repository, committing after each task that changed something and pushing once
// at the end, and reports what happened as a [Result].
//
// The package supplies the machinery and a few tasks general enough to be worth
// sharing; which tasks to run, and in what order, is the caller's decision.
//
// A Runner is safe to share between goroutines maintaining different
// repositories, and serialises the steps that cannot run concurrently.
package maintain

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Spec is the state a repository is expected to be in.
type Spec struct {
	// Description and Topics are pushed to the repository's GitHub metadata.
	Description string
	Topics      []string

	// Coverage is the test coverage recorded by the previous run, used to
	// report the change this run produced.
	Coverage float64

	// LintInTests marks a repository whose own test suite invokes the linter.
	// Its tests then have to be serialised along with everything else that
	// lints, or two linters end up running at once.
	LintInTests bool
}

// Task is one named unit of work on a repository.
type Task struct {
	// Name identifies the task in logs and is used as its commit message.
	Name string

	// Short is the abbreviated name shown in a progress table, where space is
	// scarce. It falls back to Name when empty.
	Short string

	// Run performs the work. A task that finds nothing to do should make no
	// changes and return nil: an unchanged worktree is what tells the runner
	// there is nothing to commit.
	Run func(ctx context.Context) error
}

// label returns the name to show for the task in a progress table.
func (t Task) label() string {
	if t.Short != "" {
		return t.Short
	}

	return t.Name
}

// Sequence produces the tasks to run against a repository.
//
// It is called once per repository so that tasks can close over the repository
// they act on, given the runner so that tasks needing serialisation can ask for
// it, and given the spec so that a task can render what the run already knows
// about the repository — the coverage figure a README badge shows, say.
type Sequence func(r *Runner, repo Repo, spec Spec) []Task

// Runner applies task sequences to repositories.
//
// The zero value is ready to use. A Runner must be shared, not copied, between
// the goroutines maintaining different repositories: the serialisation it
// provides is only meaningful across a single instance.
type Runner struct {
	// Inert reports that a change to this path cannot alter what the tests
	// do, so a task that only touched such files need not be tested before it
	// is committed and need not send the run back to measure coverage again.
	//
	// The caller supplies it because only the caller knows what it writes:
	// a README, a Makefile and a set of documentation fragments are inert in
	// most repositories and embedded in some. Left nil, nothing is inert and
	// every change is tested, which is the safe reading and was the only one
	// before this existed.
	Inert func(path string) bool

	// lint serialises everything that shells out to golangci-lint. The linter
	// is memory-hungry and does its own parallelism, so a second concurrent
	// invocation slows both down rather than finishing sooner.
	lint sync.Mutex
}

// Serialise wraps run so that it holds the lint lock while it executes.
//
// Use it for anything that invokes golangci-lint, directly or otherwise.
func (r *Runner) Serialise(run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		r.lint.Lock()
		defer r.lint.Unlock()

		return run(ctx)
	}
}

// TestCover measures the repository's test coverage.
//
// Repositories whose tests invoke the linter are serialised along with the
// linting tasks, since running their tests means running the linter.
func (r *Runner) TestCover(ctx context.Context, repo Repo, spec Spec) (float64, error) {
	if spec.LintInTests {
		r.lint.Lock()
		defer r.lint.Unlock()
	}

	return repo.GoTestCover(ctx)
}

// Update brings one repository up to standard and reports what happened.
//
// Failures are returned inside the [Result] rather than as an error: one
// repository failing is an outcome to report, not something that should stop a
// run covering many.
func (r *Runner) Update(
	ctx context.Context, repo Repo, spec Spec, seq Sequence, events Emitter,
) Result {
	res := Result{
		Owner:        repo.Owner(),
		Name:         repo.Name(),
		PrevCoverage: spec.Coverage,
	}

	Emit(events, Event{Kind: RepoStart, Repo: repo.String()})

	if err := r.update(ctx, repo, spec, seq, events, &res); err != nil {
		res.Err = err

		slog.ErrorContext(ctx, "maintaining repository failed",
			"repo", repo.String(), "error", err)
	}

	done := Event{
		Kind:         RepoDone,
		Repo:         repo.String(),
		Coverage:     res.Coverage,
		PrevCoverage: res.PrevCoverage,
		Commits:      res.Commits,
		Pushed:       res.Pushed,
	}

	if res.Err != nil {
		done.Err = res.Err.Error()
	}

	Emit(events, done)

	return res
}

// update does the work of [Runner.Update], writing what it learns into res and
// returning the first error that stops it.
//
// Keeping the error path here, as ordinary returns, is what lets Update record
// the failure in exactly one place.
func (r *Runner) update(
	ctx context.Context, repo Repo, spec Spec, seq Sequence, events Emitter, res *Result,
) error {
	if err := r.prepare(ctx, repo, spec); err != nil {
		return err
	}

	// Establish that the repository is healthy before changing anything, so a
	// pre-existing failure is not reported against the first task that runs.
	coverage, err := r.TestCover(ctx, repo, spec)
	if err != nil {
		return fmt.Errorf("testing before any changes: %w", err)
	}

	// Whether anything committed could have moved the coverage figure. A run
	// that only rewrote a README has not.
	var moved bool

	for _, task := range seq(r, repo, spec) {
		committed, relevant, err := r.apply(ctx, repo, spec, task, events)
		if err != nil {
			return err
		}

		if committed {
			res.Commits = append(res.Commits, task.label())
		}

		moved = moved || relevant
	}

	if len(res.Commits) > 0 {
		if err := repo.Push(ctx); err != nil {
			return fmt.Errorf("pushing: %w", err)
		}

		res.Pushed = true

		slog.InfoContext(ctx, "pushed",
			"repo", repo.String(), "commits", res.Commits)
	}

	// The figure measured on the way in still stands unless something
	// committed could have moved it. A run that changed nothing, or changed
	// only files the caller calls inert, has the same code it tested going in
	// — and measuring again would run the whole suite to arrive at a number
	// already in hand.
	if moved {
		if coverage, err = r.TestCover(ctx, repo, spec); err != nil {
			return fmt.Errorf("measuring coverage: %w", err)
		}
	}

	res.Coverage = coverage

	slog.InfoContext(ctx, "repository is up to date",
		"repo", repo.String(), "coverage", coverage)

	return nil
}

// prepare gets the repository into a state where tasks can run: metadata in
// sync, worktree clean, and up to date with the remote.
func (r *Runner) prepare(ctx context.Context, repo Repo, spec Spec) error {
	if err := repo.SetDescription(ctx, spec.Description); err != nil {
		return fmt.Errorf("setting description: %w", err)
	}

	if err := repo.SetTopics(ctx, spec.Topics); err != nil {
		return fmt.Errorf("setting topics: %w", err)
	}

	if err := discardLocalChanges(repo); err != nil {
		return err
	}

	if err := repo.Pull(ctx); err != nil {
		return fmt.Errorf("pulling latest changes: %w", err)
	}

	// A repository with no Go module yet cannot be linted or tested, so give it
	// one and leave the rest to the next run.
	if ok, err := repo.IsGoRepo(); err != nil {
		return fmt.Errorf("checking for a Go module: %w", err)
	} else if !ok {
		slog.InfoContext(ctx, "not a Go repository yet, initialising it",
			"repo", repo.String())

		return initGoModule(ctx, repo)
	}

	return nil
}

// apply runs one task and commits what it changed, reporting whether it
// produced a commit.
//
// A task that leaves the worktree clean had nothing to do, which is the normal
// case once a repository is in good shape.
func (r *Runner) apply(
	ctx context.Context, repo Repo, spec Spec, task Task, events Emitter,
) (committed, relevant bool, err error) {
	Emit(events, Event{Kind: TaskStart, Repo: repo.String(), Task: task.label()})

	if err := task.Run(ctx); err != nil {
		return false, false, fmt.Errorf("%s: %w", task.Name, err)
	}

	files, err := repo.GetChangedFiles()
	if err != nil {
		return false, false, fmt.Errorf("%s: getting changed files: %w", task.Name, err)
	}

	if len(files) == 0 {
		Emit(events, Event{Kind: TaskDone, Repo: repo.String(), Task: task.label()})

		return false, false, nil
	}

	relevant = r.relevant(spec, files)

	// Test before committing, so a task that breaks the build is reported
	// against that task and its damage is never pushed. A task that rewrote
	// only inert files has nothing to break.
	if relevant {
		if _, err := r.TestCover(ctx, repo, spec); err != nil {
			return false, false, fmt.Errorf("%s: testing after changes: %w", task.Name, err)
		}
	}

	if err := repo.CommitAll(task.Name); err != nil {
		return false, false, fmt.Errorf("%s: committing: %w", task.Name, err)
	}

	slog.InfoContext(ctx, "committed",
		"repo", repo.String(), "task", task.Name, "files", len(files))

	Emit(events, Event{
		Kind: TaskDone, Repo: repo.String(), Task: task.label(), Committed: true,
	})

	return true, relevant, nil
}

// relevant reports whether a set of changed files is worth testing over.
//
// A repository whose own test suite runs the linter is the exception with no
// exceptions: the linter reads configuration, documentation and whatever else
// it is pointed at, so nothing there can be called inert.
func (r *Runner) relevant(spec Spec, files []string) bool {
	if r.Inert == nil || spec.LintInTests {
		return true
	}

	for _, path := range files {
		if !r.Inert(path) {
			return true
		}
	}

	return false
}
