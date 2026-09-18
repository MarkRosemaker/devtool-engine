package maintain

import (
	"io"

	"github.com/MarkRosemaker/devtool-engine/event"
)

// The reporting contract moved to [event], where a consumer can import it
// without linking a runner it has no use for. These names are what this
// package used to call the same things, kept so a caller that has not moved
// yet goes on compiling.
//
// Deprecated: use the event package.
type (
	EventKind   = event.Kind
	Event       = event.Event
	Emitter     = event.Emitter
	EmitterFunc = event.EmitterFunc
	Result      = event.Result
	Board       = event.Board
)

// Deprecated: use the event package.
const (
	RunStart  = event.RunStart
	RunDone   = event.RunDone
	RepoStart = event.RepoStart
	RepoDone  = event.RepoDone
	TaskStart = event.TaskStart
	TaskDone  = event.TaskDone

	RepoPushed  = event.RepoPushed
	RunProgress = event.RunProgress
)

// Deprecated: use the event package.
var (
	ErrRemote   = event.ErrRemote
	Emit        = event.Emit
	Key         = event.Key
	RenderTable = event.RenderTable
	NewBoard    = event.NewBoard
	NewBoardFor = event.NewBoardFor
)

// NewJSONLEmitter returns an [Emitter] writing one JSON object per line to w.
//
// Deprecated: use [event.Write].
func NewJSONLEmitter(w io.Writer) event.Emitter { return event.Write(w) }

// ReadEvents reads a JSON Lines stream, calling fn for each event.
//
// Deprecated: use [event.Read].
func ReadEvents(r io.Reader, fn func(event.Event)) error { return event.Read(r, fn) }
