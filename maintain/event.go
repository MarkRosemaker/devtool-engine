package maintain

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// EventKind says what happened.
type EventKind string

const (
	// RunStart and RunDone bracket a whole run. A [Runner] maintains one
	// repository and so emits neither: whatever drives the run emits them.
	RunStart EventKind = "run_start"
	RunDone  EventKind = "run_done"

	// RepoStart and RepoDone bracket one repository. RepoDone carries
	// everything a finished row needs, so a consumer that cares only about
	// the outcome can read that kind and ignore the rest.
	RepoStart EventKind = "repo_start"
	RepoDone  EventKind = "repo_done"

	// TaskStart and TaskDone bracket one task. TaskDone says whether it
	// committed, which is the only thing a task's outcome amounts to: a task
	// that changed nothing leaves the worktree clean and is not a failure.
	TaskStart EventKind = "task_start"
	TaskDone  EventKind = "task_done"
)

// Event is one thing that happened during a run.
//
// This is the contract between whatever does the work and whatever reports
// it, and it is deliberately a flat struct of plain fields: it is written one
// JSON object per line, so a person can read a run off a terminal and a
// consumer in any language can follow one without a schema.
//
// Everything optional is omitted rather than written empty, which keeps a
// line short enough to read and means adding a field later does not disturb
// the lines that do not use it.
type Event struct {
	Kind EventKind `json:"kind"`
	Time time.Time `json:"ts"`

	// Repo is "owner/name", empty on the events that bracket a whole run.
	Repo string `json:"repo,omitempty"`

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

// jsonlEmitter writes events as JSON Lines.
type jsonlEmitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewJSONLEmitter returns an [Emitter] writing one JSON object per line to w.
//
// A write that fails is dropped rather than reported. Emitting is reporting,
// not the work: a run whose progress could not be written down has still
// maintained the repositories, and failing it for that would be the wrong
// trade.
func NewJSONLEmitter(w io.Writer) Emitter {
	return &jsonlEmitter{enc: json.NewEncoder(w)}
}

func (e *jsonlEmitter) Emit(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	_ = e.enc.Encode(ev)
}

// ReadEvents reads a JSON Lines stream, calling fn for each event.
//
// The counterpart of [NewJSONLEmitter], and the half a reporter uses: it is
// how the progress table is built from a run it did not perform itself.
func ReadEvents(r io.Reader, fn func(Event)) error {
	scan := bufio.NewScanner(r)

	// A line carries a whole event, and an error message can be long, so the
	// default 64KiB limit is worth raising.
	scan.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("reading event %q: %w", line, err)
		}

		fn(ev)
	}

	if err := scan.Err(); err != nil {
		return fmt.Errorf("reading events: %w", err)
	}

	return nil
}

// Emit sends an event, stamping it with the time, and does nothing where
// there is no emitter — so a caller that does not want reporting passes nil.
//
// Use this rather than calling [Emitter.Emit] directly: it is the one place
// the timestamp is filled in, and an event without one is not much use to
// whoever reads the stream later. [Runner] emits the repository and task
// events through it; the run-level pair is the caller's to send, since a
// Runner only ever sees one repository.
//
// The emitter is a parameter rather than a field on Runner because one Runner
// serves every repository in a run and, through the lint lock, every
// concurrent run in the process. A field would be shared mutable state, and
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
