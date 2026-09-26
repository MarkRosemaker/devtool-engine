package maintain

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// manifest is vendor/modules.txt as "go mod vendor" writes it, trimmed to what
// the parser reads: module lines, and the lines that are not modules.
const manifest = `# github.com/a/b v1.0.0
## explicit; go 1.23
github.com/a/b
# github.com/a/b/v2 v2.1.0
## explicit
github.com/a/b/v2/pkg
# golang.org/x/mod v0.41.0
golang.org/x/mod/semver
`

func TestSplitVendored(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, vendorManifest, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	plain, modules := splitVendored(fs, []string{
		"go.sum", "go.mod",
		vendorManifest,
		"vendor/github.com/a/b/b.go",
		"vendor/github.com/a/b/internal/c.go",
		// Nested inside github.com/a/b's path, and a module of its own.
		"vendor/github.com/a/b/v2/pkg/d.go",
		"vendor/golang.org/x/mod/semver/semver.go",
		// Removed by the update, so no longer in the manifest.
		"vendor/github.com/gone/away/sub/e.go",
	})

	if got, want := strings.Join(plain, ","), "go.mod,go.sum"; got != want {
		t.Errorf("plain = %q, want %q: nothing under vendor/, the manifest included", got, want)
	}

	want := "github.com/a/b,github.com/a/b/v2,github.com/gone/away,golang.org/x/mod"
	if got := strings.Join(modules, ","); got != want {
		t.Errorf("modules = %q, want %q", got, want)
	}
}

// TestSplitVendoredWithoutAManifest: a repository that does not vendor has
// nothing under vendor/, and one that has lost its manifest still gets its
// modules named by the fallback rather than its files listed.
func TestSplitVendoredWithoutAManifest(t *testing.T) {
	plain, modules := splitVendored(afero.NewMemMapFs(), []string{
		"README.md", "vendor/github.com/x/y/z.go",
	})

	if strings.Join(plain, ",") != "README.md" || strings.Join(modules, ",") != "github.com/x/y" {
		t.Errorf("plain = %v, modules = %v", plain, modules)
	}
}

// TestADependencyUpdateNamesModules is the case the summary is for, through
// the runner: a task that re-vendors reports which modules moved, not the
// hundreds of files that came with them.
func TestADependencyUpdateNamesModules(t *testing.T) {
	repo := &fakeRepo{changed: []string{
		"go.mod", "go.sum", vendorManifest,
		"vendor/golang.org/x/mod/semver/semver.go",
		"vendor/golang.org/x/mod/module/module.go",
	}}

	if err := afero.WriteFile(repo.Fs(), vendorManifest, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}

	seq := func(*Runner, Repo, Spec) []Task {
		return []Task{{Name: "update dependencies", Short: "deps", Run: func(context.Context) error {
			repo.dirty = true

			return nil
		}}}
	}

	(&Runner{}).Update(t.Context(), repo, Spec{}, seq, rec)

	for _, ev := range rec.events {
		if ev.Kind != TaskDone || ev.Task != "deps" {
			continue
		}

		if !ev.Committed {
			t.Error("the update committed and the event does not say so")
		}

		if got := strings.Join(ev.Files, ","); got != "go.mod,go.sum" {
			t.Errorf("Files = %q, want the vendored copies left out", got)
		}

		if got := strings.Join(ev.Vendored, ","); got != "golang.org/x/mod" {
			t.Errorf("Vendored = %q, want golang.org/x/mod", got)
		}

		return
	}

	t.Fatal("no task_done for deps")
}
