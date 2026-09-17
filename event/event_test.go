package event

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the testdata golden files")

// A failure off the wire renders as the run stated it, and is still
// identifiable as having come from there. Both halves matter: the first keeps
// the table's limited room for the message, the second lets a consumer tell a
// reported failure from one raised locally.
func TestRemoteErrorKeepsItsMessage(t *testing.T) {
	res := Event{
		Kind: RepoDone, Repo: "user/gorepo",
		Err: "pulling latest changes: timeout",
	}.Result()

	if got := res.Err.Error(); got != "pulling latest changes: timeout" {
		t.Errorf("Error() = %q, want the message verbatim", got)
	}

	if !errors.Is(res.Err, ErrRemote) {
		t.Error("want errors.Is(err, ErrRemote) to hold")
	}

	if got := res.notes(); !strings.HasPrefix(got, "ERR: pulling") {
		t.Errorf("notes() = %q, want no provenance in the table", got)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	err := Read(strings.NewReader("{\"kind\":\"repo_start\"}\nnot json\n"),
		func(Event) {})
	if err == nil {
		t.Fatal("expected an error for a line that is not an event")
	}
}

// TestBoardFromEventsMatchesResults is the acceptance check for making the
// table event-driven: for the same run, a board fed events renders exactly
// what a board fed results renders.
//
// Byte-identical, because the point of this change was to move where the
// table's inputs come from without changing the table.
func TestBoardFromEventsMatchesResults(t *testing.T) {
	rows := []Result{
		{Owner: "user", Name: "alpha"},
		{Owner: "user", Name: "beta"},
		{Owner: "user", Name: "gamma"},
	}

	results := []Result{
		{
			Owner: "user", Name: "alpha", Coverage: 96.4, PrevCoverage: 90,
			Commits: []string{"readme", "vet"}, Pushed: true,
		},
		{Owner: "user", Name: "beta", Coverage: 71.7, PrevCoverage: 71.7},
		{Owner: "user", Name: "gamma", Err: errors.New("pulling latest changes: timeout")},
	}

	fromResults := NewBoard(rows)
	for _, res := range results {
		fromResults.Set(res)
	}

	fromEvents := NewBoard(rows)

	for _, res := range results {
		ev := Event{
			Kind: RepoDone, Repo: res.Key(),
			Coverage: res.Coverage, PrevCoverage: res.PrevCoverage,
			Commits: res.Commits, Pushed: res.Pushed,
		}

		if res.Err != nil {
			ev.Err = res.Err.Error()
		}

		fromEvents.Apply(ev)
	}

	got, want := fromEvents.Render(), fromResults.Render()
	if got != want {
		t.Errorf("the event-driven table differs:\n--- events ---\n%s\n--- results ---\n%s",
			got, want)
	}
}

// TestBoardFromEventStreamGolden renders a recorded stream, so a change to
// either the event contract or the table is a reviewable diff. Run
// "go test ./event -update" to rewrite it.
func TestBoardFromEventStreamGolden(t *testing.T) {
	const stream = "testdata/run.jsonl"

	f, err := os.Open(stream)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	// Built from the stream alone, the way a reporter in another process has
	// to: it is told the rows by RunStart and knows nothing else going in.
	var board *Board

	if err := Read(f, func(ev Event) {
		if ev.Kind == RunStart {
			board = NewBoardFor(ev.Repos)

			return
		}

		board.Apply(ev)
	}); err != nil {
		t.Fatal(err)
	}

	if board == nil {
		t.Fatal("the stream carried no run_start, so no table could be built")
	}

	got := []byte(board.Render())
	golden := filepath.Join("testdata", "run.table.golden")

	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}

		return
	}

	wantBytes, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./event -update)", err)
	}

	if string(got) != string(wantBytes) {
		t.Errorf("%s does not match:\n--- got ---\n%s\n--- want ---\n%s",
			golden, got, wantBytes)
	}
}

// TestNewBoardFor covers what a reporter gets handed: a list of keys off the
// wire, including whatever a malformed line puts in it.
func TestNewBoardFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want int
	}{
		{"none", nil, 0},
		{"two", []string{"user/alpha", "user/beta"}, 2},
		{"a key with no slash is skipped", []string{"user/alpha", "nope"}, 1},
		{"an empty owner is skipped", []string{"/beta", "user/alpha"}, 1},
		{"an empty name is skipped", []string{"user/", "user/alpha"}, 1},
		{"a second slash is skipped", []string{"user/a/b", "user/alpha"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(NewBoardFor(tc.keys).Results()); got != tc.want {
				t.Errorf("got %d rows, want %d", got, tc.want)
			}
		})
	}
}

// TestRunStartCarriesEveryRepo is the property the table depends on: a
// reporter that has only the stream can name every repository the run
// touched before any of them finishes.
func TestRunStartCarriesEveryRepo(t *testing.T) {
	f, err := os.Open("testdata/run.jsonl")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	var announced []string

	mentioned := map[string]bool{}

	if err := Read(f, func(ev Event) {
		if ev.Kind == RunStart {
			announced = ev.Repos
		}

		if ev.Repo != "" {
			mentioned[ev.Repo] = true
		}
	}); err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	for _, key := range announced {
		known[key] = true
	}

	for repo := range mentioned {
		if !known[repo] {
			t.Errorf("%q appears in the stream but run_start did not announce it", repo)
		}
	}
}

// TestRoundTrip is the contract across a process boundary: what [Write] wrote
// is what [Read] reads, field for field.
func TestRoundTrip(t *testing.T) {
	want := []Event{
		{Kind: RunStart, Repos: []string{"user/alpha", "user/beta"}},
		{Kind: RepoStart, Repo: "user/alpha"},
		{Kind: TaskDone, Repo: "user/alpha", Task: "readme", Committed: true},
		{
			Kind: RepoDone, Repo: "user/alpha",
			Coverage: 96.4, PrevCoverage: 90,
			Commits: []string{"readme"}, Pushed: true,
		},
		{Kind: RepoDone, Repo: "user/beta", Err: "pulling latest changes: timeout"},
		{Kind: RunDone},
	}

	buf := &bytes.Buffer{}

	w := Write(buf)
	for _, ev := range want {
		Emit(w, ev)
	}

	// One object per line, so a person can read a run off a terminal.
	if got := strings.Count(buf.String(), "\n"); got != len(want) {
		t.Errorf("wrote %d lines for %d events", got, len(want))
	}

	var got []Event
	if err := Read(buf, func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatal(err)
	}

	if len(got) != len(want) {
		t.Fatalf("read %d events, wrote %d", len(got), len(want))
	}

	for i, ev := range got {
		if ev.Time.IsZero() {
			t.Errorf("event %d arrived without a timestamp", i)
		}

		ev.Time = time.Time{} // Emit stamps it, so it is not part of what was sent.

		if !reflect.DeepEqual(ev, want[i]) {
			t.Errorf("event %d did not survive the trip:\n got %+v\nwant %+v",
				i, ev, want[i])
		}
	}
}

// TestReadRejectsLogLines is the regression guard for the bug this package
// exists to make impossible: a logger writing to the event stream. These are
// real lines from a run whose slog handler had been pointed at stdout.
func TestReadRejectsLogLines(t *testing.T) {
	for _, line := range []string{
		// The line that failed loudly: repos is a count, not a list.
		`{"time":"2026-09-16T11:41:02Z","level":"INFO","msg":"dependency graph built","repos":36}`,
		// The lines that failed silently: every field fits, and no kind.
		`{"time":"2026-09-16T11:41:09Z","level":"INFO","msg":"pushed","repo":"user/alpha","commits":["readme"]}`,
		`{"time":"2026-09-16T11:41:11Z","level":"INFO","msg":"repository is up to date","repo":"user/beta","coverage":71.7}`,
		// A kind this package does not know is no better than none.
		`{"kind":"repo_paused","repo":"user/alpha"}`,
	} {
		t.Run(line[:min(len(line), 60)], func(t *testing.T) {
			var seen int

			err := Read(strings.NewReader(line+"\n"), func(Event) { seen++ })
			if err == nil {
				t.Fatal("a log line was accepted as an event")
			}

			if seen != 0 {
				t.Errorf("%d events reached the consumer before the refusal", seen)
			}

			if !strings.Contains(err.Error(), strconv.Quote(line)) {
				t.Errorf("the error does not name the offending line: %v", err)
			}
		})
	}
}

// TestReadToleratesUnknownFields is the other half of that strictness: a
// newer producer may add a field, and a reader that predates it carries on.
func TestReadToleratesUnknownFields(t *testing.T) {
	const line = `{"kind":"repo_done","repo":"user/alpha","coverage":96.4,"duration":"1.2s"}`

	var got []Event
	if err := Read(strings.NewReader(line+"\n"), func(ev Event) {
		got = append(got, ev)
	}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Repo != "user/alpha" || got[0].Coverage != 96.4 {
		t.Errorf("got %+v, want the known fields read and the rest ignored", got)
	}
}
