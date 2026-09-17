package event

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"sync"
	"text/tabwriter"
)

// Key is the canonical identifier for a repository, "owner/name".
func Key(owner, name string) string { return owner + "/" + name }

// splitKey is Key's inverse, for reading a repository back off an [Event].
//
// It rejects what Key could never have produced — a missing half, or a second
// slash — because an event can arrive from another process, where the key is
// whatever that process wrote rather than something this package built.
func splitKey(key string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(key, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}

	return owner, name, true
}

// ErrRemote marks a failure that reached us as text rather than as an error:
// one read off an [Event], having crossed a process boundary. The message
// survived the trip and the original type did not, so errors.Is is how a
// consumer tells, rather than a type assertion that can no longer work.
var ErrRemote = errors.New("reported by the run")

// remoteError is such a failure.
//
// Error returns the original message verbatim and nothing else, so anything
// rendering a run shows what the run said. Putting the provenance in the
// message instead would spend the table's limited room on a phrase that is
// the same on every failing row.
type remoteError struct{ msg string }

func (e remoteError) Error() string { return e.msg }
func (e remoteError) Unwrap() error { return ErrRemote }

// Result is the outcome of maintaining one repository.
type Result struct {
	Owner, Name string

	// PrevCoverage is the coverage recorded before this run, and Coverage the
	// coverage after it. Reporting the change is the point of keeping both.
	PrevCoverage float64
	Coverage     float64

	// Commits lists the short names of the tasks that produced a commit.
	Commits []string

	// Pushed records whether those commits reached the remote.
	Pushed bool

	// Err is the failure that stopped the run for this repository.
	Err error

	// Pending marks a repository whose result is not in yet, so a progress
	// table can show it as still running.
	Pending bool
}

// Key returns the repository's canonical identifier.
func (r Result) Key() string { return Key(r.Owner, r.Name) }

// Notable reports whether anything happened worth telling a human about.
func (r Result) Notable() bool { return r.Pushed || r.Err != nil }

// ErrorMessage renders the failure in full, as HTML, for sending on its own.
//
// The table only has room for a truncated error; this is where the whole thing
// goes, so a failure can actually be diagnosed from the notification.
func (r Result) ErrorMessage() string {
	if r.Err == nil {
		return ""
	}

	return fmt.Sprintf("<b>%s</b><pre>%s</pre>",
		html.EscapeString(r.Key()),
		html.EscapeString(r.Err.Error()))
}

// maxNoteLen is how much of an error fits in the table before it starts
// crowding out the other rows.
const maxNoteLen = 60

// notes summarises the result for the table's last column.
func (r Result) notes() string {
	switch {
	case r.Pending:
		return "⏳"
	case r.Err != nil:
		msg := r.Err.Error()
		if len(msg) > maxNoteLen {
			msg = msg[:maxNoteLen] + "…"
		}

		return "ERR: " + msg
	case len(r.Commits) == 0:
		return "—"
	}

	notes := strings.Join(r.Commits, ", ") + " → pushed"

	// A delta is only meaningful against a coverage figure we actually had.
	if r.PrevCoverage > 0 && r.Coverage != r.PrevCoverage {
		notes += fmt.Sprintf(" (%+.1f%%)", r.Coverage-r.PrevCoverage)
	}

	return notes
}

// coverage renders the coverage column, which stays blank until there is a real
// figure to put in it.
func (r Result) coverage() string {
	if r.Pending || (r.Err != nil && r.Coverage == 0) {
		return "—"
	}

	return fmt.Sprintf("%.1f%%", r.Coverage)
}

// RenderTable lays the results out as a monospaced HTML table.
func RenderTable(results []Result) string {
	var buf strings.Builder

	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintln(w, "Repo\tCoverage\tNotes")

	for _, res := range results {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", res.Name, res.coverage(), res.notes())
	}

	_ = w.Flush()

	return "<pre>" + buf.String() + "</pre>"
}

// Board collects the results of a run as they arrive, holding them in a fixed
// order so the table a reader is watching never reshuffles itself.
//
// A Board is safe for concurrent use.
type Board struct {
	mu      sync.Mutex
	index   map[string]int
	results []Result
}

// NewBoard creates a board with a row per repository, each marked pending.
func NewBoard(repos []Result) *Board {
	b := &Board{
		index:   make(map[string]int, len(repos)),
		results: make([]Result, len(repos)),
	}

	for i, res := range repos {
		res.Pending = true
		b.results[i] = res
		b.index[res.Key()] = i
	}

	return b
}

// NewBoardFor creates a board from the keys a [RunStart] event carries, which
// is how a process that did not perform the run gets its rows.
//
// A key that is not "owner/name" is skipped rather than rendered as a broken
// row: the stream is another process's output, and a reporter that dies on a
// malformed line reports nothing at all.
func NewBoardFor(keys []string) *Board {
	repos := make([]Result, 0, len(keys))

	for _, key := range keys {
		owner, name, ok := splitKey(key)
		if !ok {
			continue
		}

		repos = append(repos, Result{Owner: owner, Name: name})
	}

	return NewBoard(repos)
}

// Set records a repository's result, replacing its pending row.
//
// A result for a repository the board does not know about is ignored: the board
// exists to show a fixed set of rows, and growing it mid-run would move the ones
// the reader is already looking at.
func (b *Board) Set(res Result) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if i, ok := b.index[res.Key()]; ok {
		b.results[i] = res
	}
}

// Render lays the board out as a table.
func (b *Board) Render() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return RenderTable(b.results)
}

// Results returns the results so far, in board order.
func (b *Board) Results() []Result {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]Result(nil), b.results...)
}

// Apply updates the board from an event, which is how a table gets built by
// something that did not perform the run.
//
// Only [RepoDone] changes a row. Every row starts pending, so the events that
// say work has begun have nothing to add, and the task events are for a
// reporter that wants to show more than each repository's outcome. An event
// for a repository the board does not know about is ignored, for the reason
// [Board.Set] ignores one.
func (b *Board) Apply(ev Event) {
	if ev.Kind == RepoDone {
		b.Set(ev.Result())
	}
}
