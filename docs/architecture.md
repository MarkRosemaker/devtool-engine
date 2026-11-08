# Architecture

The machinery behind `devtool`: what a run does to one repository, how it
reports, and how the binary keeps itself current. Which tasks run, and what
standard they hold a repository to, is the caller's.

## Packages

| | |
| --- | --- |
| `maintain` | the [`Repo`](../maintain/repo.go) a run works on, the `Task`, and the `Runner` that applies a sequence of them |
| `event` | the event stream between a run and whatever reports on it, and the table built from it |
| `depgraph` | orders repositories by their dependencies and runs them as parallel as that allows |
| `selfupdate` | keeps a `go install`ed binary on its module's latest version |

## A run over one repository

`Runner.Update` takes a repository from wherever it is to the standard the
caller's sequence describes, in steps it reports as it goes:

1. **Prepare.** Push the description and topics the caller gives (an empty
   one leaves the host's as it is), discard whatever an interrupted run left
   in the worktree, and pull. Changes a reset cannot clear stop the run rather
   than being committed as its work.
2. **Bootstrap.** A repository with no Go module is given one, committed and
   pushed, and the run carries on: the same run lays down the rest of the
   scaffolding, so development can begin straight away.
3. **Test.** The suite runs once before anything changes, so a failure that
   was already there is not blamed on the first task.
4. **Tasks.** Each task that changes something is tested and committed on its
   own. A task that changed only files `Runner.Inert` names is committed
   without testing. A task that changed nothing leaves no commit.
5. **Push,** once, if anything was committed.

A failure is an outcome, not an error: it comes back in the `Result` and in
the run's `repo_done`, so one repository failing does not stop a run over
many. Every step names itself in the error it fails with.

The coverage figure is the last one measured, which ran on exactly the code
that was pushed. Two optional checks act on it after a successful run:
`Spec.CoverageFloor` fails a run that takes coverage from at or above it to
below, and `Spec.PublicCoverage` fails every run of a public repository below
it. Either failure is the report; the work is already pushed.

Everything that runs golangci-lint, including the tests of a repository whose
tests lint (`Spec.LintInTests`), holds one lock on the `Runner`, so two
linters never run at once.

## Self-update

`selfupdate` asks the toolchain for the module's latest version and, where it
differs from the running build, runs `go install module@version`: the command
that installed the binary in the first place. The version installed is the one
compared against, pinned, never a later `@latest`. A new build takes effect
at the next invocation.

What it rests on:

- **No tags needed.** An untagged module resolves to a pseudo-version naming
  the commit.
- **Private modules work.** It shells out to `go list -m` rather than writing a
  proxy request by hand, so whatever `GOPROXY`, `GOPRIVATE` and git credentials
  the machine has are the ones that apply.
- **The question is asked from outside any module.** Inside one that vendors
  its dependencies the toolchain refuses it ("cannot query module due to
  `-mod=vendor`"), and that is where self-update gets run: from a repository
  somebody is working in, which vendors. `go install pkg@version` ignores the
  current module by design.
- **The proxy caches.** `proxy.golang.org` can name the commit before the
  latest for the best part of an hour after a push. `Updater.Direct` bypasses
  the proxy for this one module, leaving the toolchain download, which only
  the proxy serves, on it. It also carries `GOPRIVATE` across, since setting
  `GONOPROXY` or `GONOSUMDB` overrides it.
- **A local build is left alone,** in either shape the toolchain stamps:
  `(devel)` with no version information, or a commit with `+dirty`.
- **A shadowing copy is named.** `go install` writes to `GOBIN`, or
  `GOPATH/bin`, which need not be what `PATH` finds first; an older copy ahead
  of it is reported, since it is the one that will run.
