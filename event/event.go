// Package event is the contract between whatever maintains repositories and
// whatever reports on it.
//
// One process does the work and writes events; another reads them and draws a
// table. Both import this package, so neither can drift from the other without
// the compiler saying so.
//
// # Events are not logs
//
// The stream carries events and nothing else. Logs are a separate stream, for
// a reason learned the hard way: while a run's logs went to the same place as
// its events, every log line was a line the reader had to make sense of, and
// the ones whose keys happened to match an event's field — repo, coverage,
// commits — parsed into a valid-looking event that said nothing. [Read] keeps
// them apart by skipping a line that is not an event and saying so, so a
// stream nobody meant to share is visible rather than either silent or fatal.
package event

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Kind says what happened.
type Kind string

const (
	// RunStart and RunDone bracket a whole run. Whatever maintains one
	// repository emits neither: only the caller knows where a run begins.
	RunStart Kind = "run_start"
	RunDone  Kind = "run_done"

	// RepoStart and RepoDone bracket one repository. RepoDone carries
	// everything a finished row needs, so a consumer that cares only about
	// the outcome can read that kind and ignore the rest.
	RepoStart Kind = "repo_start"
	RepoDone  Kind = "repo_done"

	// TaskStart and TaskDone bracket one task. TaskDone says whether it
	// committed, which is the only thing a task's outcome amounts to: a task
	// that changed nothing leaves the worktree clean and is not a failure.
	TaskStart Kind = "task_start"
	TaskDone  Kind = "task_done"
)

// kinds is every kind this package knows, which is what makes a line an event
// rather than some other JSON object that happened to arrive.
var kinds = []Kind{RunStart, RunDone, RepoStart, RepoDone, TaskStart, TaskDone}

// Valid reports whether k is a kind this package knows.
func (k Kind) Valid() bool { return slices.Contains(kinds, k) }

// Event is one thing that happened during a run.
//
// A flat struct of plain fields, written one JSON object per line, so a
// consumer in any language can follow a run without a schema.
//
// Everything optional is omitted rather than written empty, which keeps a
// line short enough to read and means adding a field later does not disturb
// the lines that do not use it. That is the extension mechanism: a new datum
// gets a new field, which both ends then see at compile time, and a reader
// that predates it ignores what it does not know. There is deliberately no
// free-form map — a corner of the contract neither end can check is not a
// contract.
type Event struct {
	Kind Kind      `json:"kind"`
	Time time.Time `json:"ts"`

	// Repo is "owner/name", empty on the events that bracket a whole run.
	Repo string `json:"repo,omitempty"`

	// Repos is every repository the run covers, as "owner/name", in the order
	// they should be reported. Set on RunStart and nowhere else.
	//
	// It is what lets a reporter in another process draw the table: a board
	// shows a fixed set of rows and ignores a repository it was not told
	// about, so without this a reader of the stream has nothing to build one
	// from until the first repository has already finished.
	Repos []string `json:"repos,omitempty"`

	// Task is the task's short label, set on TaskStart and TaskDone.
	Task string `json:"task,omitempty"`

	// Committed is set on TaskDone: the task changed something and the change
	// was committed.
	Committed bool `json:"committed,omitempty"`

	// Coverage and PrevCoverage are set on RepoDone. Both, because reporting
	// the change is the point of keeping either.
	Coverage     float64 `json:"coverage,omitempty"`
	PrevCoverage float64 `json:"prevCoverage,omitempty"`

	// Commits and Pushed are set on RepoDone.
	Commits []string `json:"commits,omitempty"`
	Pushed  bool     `json:"pushed,omitempty"`

	// Err is the failure that stopped this repository, as text. Text rather
	// than an error, because this crosses a process boundary: what survives
	// the trip is the message.
	Err string `json:"err,omitempty"`
}

// Emitter receives the events of a run.
//
// An implementation has to be safe for concurrent use: a run maintains
// several repositories at once, and each emits as it goes.
type Emitter interface {
	Emit(Event)
}

// EmitterFunc adapts a function to an [Emitter].
type EmitterFunc func(Event)

func (f EmitterFunc) Emit(ev Event) { f(ev) }

// writer writes events as JSON Lines.
type writer struct {
	mu sync.Mutex
	w  io.Writer
}

// Write returns an [Emitter] writing one JSON object per line to w.
//
// Give it a stream of its own. [Read] survives anything else written there,
// but only by throwing it away, so a producer sharing this stream is losing
// whatever it shared it with.
//
// A write that fails is dropped rather than reported. Emitting is reporting,
// not the work: a run whose progress could not be written down has still
// maintained the repositories, and failing it for that would be the wrong
// trade.
func Write(w io.Writer) Emitter { return &writer{w: w} }

func (e *writer) Emit(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	b, err := json.Marshal(ev)
	if err != nil {
		return
	}

	_, _ = e.w.Write(append(b, '\n'))
}

// maxStrayReports bounds what one contaminated stream can put in the log. A
// producer writing something other than events is worth seeing, not worth
// drowning in: something piping a whole log file in should cost a handful of
// lines and a count, not a line per line.
const maxStrayReports = 5

// Read reads a JSON Lines stream, calling fn for each event.
//
// The counterpart of [Write], and the half a reporter uses: it is how a
// progress table gets built from a run this process did not perform.
//
// A line that is not an event is skipped and reported, not fatal. Both halves
// matter, and each is there because the other alone was wrong:
//
// Skipped, because a stream is shared whether or not it should be. A clone's
// progress, a warning from a tool the producer shells out to — one stray line
// used to end the read, and with it the whole run's report, for a run that had
// gone perfectly well. Reporting is not the work, and it should not be able to
// throw the work away.
//
// Reported, because the bug this package exists to prevent was contamination
// nobody could see: a logger shared the stream for months, its lines parsed
// into events with no kind, and a consumer dropped them without a word. Silence
// is what let that run. A skipped line is written to the log, which is a
// different stream on purpose.
//
// What remains fatal is a stream that cannot be read at all — a line past the
// buffer, a broken pipe — since that is not a stray line but a lost one, and a
// table built from what arrived before it would be quietly wrong.
//
// Unknown *fields* are accepted, which is what lets a newer producer add one
// without breaking an older reader.
func Read(r io.Reader, fn func(Event)) error {
	scan := bufio.NewScanner(r)

	// A line carries a whole event, and an error message can be long, so the
	// default 64KiB limit is worth raising.
	scan.Buffer(make([]byte, 0, 64<<10), 1<<20)

	strays := 0

	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}

		ev, err := decode(line)
		if err != nil {
			strays++

			if strays <= maxStrayReports {
				slog.Warn("not an event, skipped",
					"line", string(line), "why", err)
			}

			continue
		}

		fn(ev)
	}

	if strays > maxStrayReports {
		slog.Warn("more of the event stream was not events",
			"skipped", strays, "reported", maxStrayReports)
	}

	if err := scan.Err(); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}

	return nil
}

// decode turns one line into an event, saying why it is not one where it is
// not: whether it failed to parse at all, or parsed into something carrying no
// kind this package knows.
func decode(line []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, err
	}

	if !ev.Kind.Valid() {
		return Event{}, fmt.Errorf("kind %q is not one this package knows", ev.Kind)
	}

	return ev, nil
}

// Emit sends an event, stamping it with the time, and does nothing where
// there is no emitter — so a caller that does not want reporting passes nil.
//
// Use this rather than calling [Emitter.Emit] directly: it is the one place
// the timestamp is filled in, and an event without one is not much use to
// whoever reads the stream later. The runner emits the repository and task
// events through it; the run-level pair is its caller's to send, since a
// runner only ever sees one repository.
//
// An emitter is passed to a run rather than held by it, because one runner
// serves every repository in a run and, through the lint lock, every
// concurrent run in the process. Held, it would be shared mutable state, and
// two runs with two progress tables would race for it.
func Emit(events Emitter, ev Event) {
	if events == nil {
		return
	}

	ev.Time = time.Now().UTC()

	events.Emit(ev)
}

// Result reconstructs the outcome of one repository from its RepoDone event,
// so a consumer across a process boundary gets what the run itself had.
//
// A failure comes back as an error whose message is the original verbatim and
// which matches [ErrRemote], so a table renders what the run said while a
// consumer can still tell the failure was reported rather than raised here.
func (ev Event) Result() Result {
	owner, name, _ := splitKey(ev.Repo)

	res := Result{
		Owner:        owner,
		Name:         name,
		Coverage:     ev.Coverage,
		PrevCoverage: ev.PrevCoverage,
		Commits:      ev.Commits,
		Pushed:       ev.Pushed,
	}

	if ev.Err != "" {
		res.Err = remoteError{msg: ev.Err}
	}

	return res
}
