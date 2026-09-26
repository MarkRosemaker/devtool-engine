package maintain

import (
	"context"
	"strings"
	"testing"

	"github.com/MarkRosemaker/devtool-engine/event"
	"github.com/spf13/afero"
)

const goModBefore = `module example.com/thing

go 1.27.0

require (
	github.com/kept/same v1.0.0
	github.com/moved/up v1.2.0
	github.com/moved/down v1.10.0
	github.com/gone/away v0.3.0 // indirect
)
`

const goModAfter = `module example.com/thing

go 1.27.0

require (
	github.com/kept/same v1.0.0
	github.com/moved/up v1.3.0
	github.com/moved/down v1.9.0
	github.com/new/one v0.1.0 // indirect
)
`

// unreadable is a go.mod ParseLax rejects. Lax means an unknown directive is
// skipped, so it takes a malformed known one — a requirement with no version.
const unreadable = "module m\n\nrequire github.com/x/y\n"

// render is how the cases below read, in go get's own order of words.
func render(changes []event.ModuleChange) string {
	parts := make([]string, 0, len(changes))

	for _, c := range changes {
		parts = append(parts, c.Path+" "+c.From+"=>"+c.To)
	}

	return strings.Join(parts, ", ")
}

func TestModuleChanges(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after string
		want          string
	}{
		{
			"moved, added and removed, indirect included, sorted by path",
			goModBefore, goModAfter,
			"github.com/gone/away v0.3.0=>, github.com/moved/down v1.10.0=>v1.9.0, " +
				"github.com/moved/up v1.2.0=>v1.3.0, github.com/new/one =>v0.1.0",
		},
		{"nothing moved", goModBefore, goModBefore, ""},
		// A go.mod that appeared reports its requirements as added, which
		// is what go mod init followed by go get would say.
		{"a new go.mod", "", "module m\n\nrequire a.b/c v1.0.0\n", "a.b/c =>v1.0.0"},
		// Not knowing is no reason to report everything as having moved —
		// on either side, and whichever the other side is.
		{"an unreadable go.mod after", goModBefore, unreadable, ""},
		{"an unreadable go.mod before", unreadable, goModAfter, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after []byte
			if tc.before != "" {
				before = []byte(tc.before)
			}

			if tc.after != "" {
				after = []byte(tc.after)
			}

			if got := render(moduleChanges(before, after)); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestCollapseVendor(t *testing.T) {
	got := collapseVendor([]string{
		"vendor/modules.txt", "go.sum", "vendor/a/b/c.go", "go.mod", "vendor/x/y.go",
	})

	if want := "go.mod,go.sum,vendor/"; strings.Join(got, ",") != want {
		t.Errorf("got %q, want %q", strings.Join(got, ","), want)
	}

	if got := collapseVendor([]string{"README.md"}); strings.Join(got, ",") != "README.md" {
		t.Errorf("nothing vendored, and vendor/ appeared anyway: %v", got)
	}
}

// TestADependencyUpdateReportsVersions is the case this is for, through the
// runner: a task that bumps a dependency and re-vendors it says which module
// moved and from what to what, and names vendor/ once.
func TestADependencyUpdateReportsVersions(t *testing.T) {
	repo := &fakeRepo{changed: []string{
		"go.mod", "go.sum", "vendor/modules.txt",
		"vendor/github.com/moved/up/a.go", "vendor/github.com/moved/up/b.go",
	}}

	if err := afero.WriteFile(repo.Fs(), goModPath, []byte(goModBefore), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}

	seq := func(*Runner, Repo, Spec) []Task {
		return []Task{{Name: "update dependencies", Short: "deps", Run: func(context.Context) error {
			repo.dirty = true

			return afero.WriteFile(repo.Fs(), goModPath, []byte(goModAfter), 0o644)
		}}}
	}

	(&Runner{}).Update(t.Context(), repo, Spec{}, seq, rec)

	for _, ev := range rec.events {
		if ev.Kind != TaskDone || ev.Task != "deps" {
			continue
		}

		if got := strings.Join(ev.Files, ","); got != "go.mod,go.sum,vendor/" {
			t.Errorf("Files = %q", got)
		}

		if got, want := render(ev.Modules), "github.com/gone/away v0.3.0=>, "+
			"github.com/moved/down v1.10.0=>v1.9.0, github.com/moved/up v1.2.0=>v1.3.0, "+
			"github.com/new/one =>v0.1.0"; got != want {
			t.Errorf("Modules = %q\nwant      %q", got, want)
		}

		return
	}

	t.Fatal("no task_done for deps")
}
