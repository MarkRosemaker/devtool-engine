package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalBuildIsLeftAlone is the case that runs on every developer machine:
// a binary built from a working tree must not be replaced by a published one.
func TestLocalBuildIsLeftAlone(t *testing.T) {
	for _, current := range []string{devel, ""} {
		u := &Updater{Module: "example.com/tool", Current: current}

		out, err := u.Update(t.Context())
		if err != nil {
			t.Fatalf("current %q: %v", current, err)
		}

		if out.Updated {
			t.Errorf("current %q: a local build was replaced", current)
		}

		if out.Reason == "" {
			t.Errorf("current %q: nothing happened and no reason was given", current)
		}

		// It must not have gone to the network to decide this.
		if out.Latest != "" {
			t.Errorf("current %q: resolved a version it did not need", current)
		}
	}
}

func TestUpdateNeedsAModule(t *testing.T) {
	if _, err := (&Updater{Current: "v1.0.0"}).Update(t.Context()); err == nil {
		t.Error("an updater with no module reported success")
	}
}

func TestOutcomeString(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  Outcome
		want string
	}{
		{
			name: "updated",
			out:  Outcome{Current: "v1", Latest: "v2", Updated: true},
			want: "updated v1 to v2",
		},
		{
			name: "updated but shadowed",
			out: Outcome{
				Current: "v1", Latest: "v2", Updated: true,
				Shadowed: "/usr/local/bin/tool",
			},
			want: "/usr/local/bin/tool comes first on PATH",
		},
		{
			name: "not updated",
			out:  Outcome{Current: "v1", Reason: "already on the latest version"},
			want: "not updated: already on the latest version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.out.String(); !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestVersionOfThisTest: a test binary is built rather than installed, so the
// toolchain stamps it as a local build. That is what Version has to report,
// and it is why the local-build guard above is the common path.
func TestVersionOfThisTest(t *testing.T) {
	if got := Version(); got != devel && got != "" {
		t.Errorf("a test binary reported %q, want %q", got, devel)
	}
}

func TestEnvGoesDirectOnlyWhenAsked(t *testing.T) {
	if got := strings.Join((&Updater{}).env(), " "); strings.Contains(got, "GONOPROXY=") {
		t.Error("bypassed the proxy without being asked")
	}

	got := strings.Join((&Updater{Module: "example.com/tool", Direct: true}).env(), " ")

	for _, want := range []string{
		"GONOPROXY=example.com/tool",
		"GONOSUMDB=example.com/tool",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Direct did not set %s", want)
		}
	}

	// GOPROXY=direct would take the toolchain download direct with it, and
	// that is served by the proxy: a machine that cannot reach go.dev then
	// fails the install outright. Measured, not supposed.
	if strings.Contains(got, "GOPROXY=direct") {
		t.Error("Direct set GOPROXY, which also diverts the toolchain download")
	}
}

func TestPrepend(t *testing.T) {
	for _, tc := range []struct{ list, want string }{
		{"", "example.com/tool"},
		{"other.example/*", "example.com/tool,other.example/*"},
	} {
		if got := prepend(tc.list, "example.com/tool"); got != tc.want {
			t.Errorf("prepend(%q) = %q, want %q", tc.list, got, tc.want)
		}
	}
}

// TestLatestNeedsAModule guards the exported check, which a caller may use on
// its own to report drift without installing anything.
func TestLatestNeedsAModule(t *testing.T) {
	if _, err := (&Updater{}).Latest(t.Context()); err == nil {
		t.Error("resolving with no module reported success")
	}
}

// TestStderrOfCarriesTheMessage: an install that fails reports "exit status 1"
// and nothing else unless the command's stderr is captured and attached.
func TestStderrOfCarriesTheMessage(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "echo something went wrong >&2; exit 1")

	_, err := cmd.Output()
	if err == nil {
		t.Fatal("the command was expected to fail")
	}

	if got := stderrOf(err).Error(); !strings.Contains(got, "something went wrong") {
		t.Errorf("the message was lost: %q", got)
	}
}

// TestDirectKeepsTheMachinesPrivatePatterns is the bug that broke a real
// install: GONOPROXY and GONOSUMDB override GOPRIVATE rather than adding to
// it, so naming only this module turned the bypass off for the private
// dependency this module has, and the install failed verifying it against the
// public checksum database.
func TestDirectKeepsTheMachinesPrivatePatterns(t *testing.T) {
	t.Setenv("GOPRIVATE", "github.com/acme/*")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GONOSUMDB", "")

	got := strings.Join((&Updater{Module: "github.com/acme/tool", Direct: true}).env(), " ")

	for _, want := range []string{
		"GONOPROXY=github.com/acme/tool,github.com/acme/*",
		"GONOSUMDB=github.com/acme/tool,github.com/acme/*",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %s in:\n%s", want, got)
		}
	}
}

// TestDirectPrefersAnExplicitBypass: where the machine set GONOPROXY itself,
// that is what is widened, not GOPRIVATE.
func TestDirectPrefersAnExplicitBypass(t *testing.T) {
	t.Setenv("GOPRIVATE", "github.com/acme/*")
	t.Setenv("GONOPROXY", "github.com/other/*")

	got := strings.Join((&Updater{Module: "github.com/acme/tool", Direct: true}).env(), " ")
	if !strings.Contains(got, "GONOPROXY=github.com/acme/tool,github.com/other/*") {
		t.Errorf("an explicit GONOPROXY was not carried across:\n%s", got)
	}
}

func TestPrependDoesNotRepeat(t *testing.T) {
	if got := prepend("a,example.com/tool,b", "example.com/tool"); got != "a,example.com/tool,b" {
		t.Errorf("the module was added twice: %q", got)
	}
}

// TestLatestIsAskedOutsideTheCurrentModule is the bug this guards against:
// "go list -m …@latest" refuses inside a module that vendors its
// dependencies, with "cannot query module due to -mod=vendor". That is
// precisely where self-update gets run — from the repository somebody is
// working in — so the question has to be asked from outside any module.
//
// The module below does not exist, so resolution fails either way; what is
// asserted is the shape of the failure. A vendored module is built around the
// test rather than assumed, so this fails without the fix.
func TestLatestIsAskedOutsideTheCurrentModule(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to the go command")
	}

	dir := t.TempDir()

	for name, content := range map[string]string{
		"go.mod":             "module example.invalid/vendored\n\ngo 1.27.0\n",
		"vendor/modules.txt": "",
		"doc.go":             "// Package vendored exists.\npackage vendored\n",
	} {
		path := filepath.Join(dir, name)

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(dir)

	_, err := (&Updater{Module: "example.invalid/nothing-here"}).Latest(t.Context())
	if err == nil {
		t.Fatal("a module that does not exist resolved")
	}

	if strings.Contains(err.Error(), "-mod=vendor") {
		t.Errorf("the question was asked inside the current module: %v", err)
	}
}

// TestADirtyBuildIsLocalToo: the toolchain no longer stamps every local build
// "(devel)". One built from a worktree carries the commit it came from and
// "+dirty", and semver ignores build metadata when comparing — so a check that
// only knew "(devel)" would let a published build overwrite somebody's own.
func TestADirtyBuildIsLocalToo(t *testing.T) {
	const built = "v0.0.0-20260917220415-af1bbeee66e1"

	for _, tc := range []struct {
		version string
		want    bool
	}{
		{devel, true},
		{built + "+dirty", true},
		{built, false},
		{"v1.2.3", false},
		{"", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if got := local(tc.version); got != tc.want {
				t.Errorf("local(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}

	// Through Update, which is where it decides anything: a dirty build is
	// left alone without the toolchain being asked a thing.
	out, err := (&Updater{
		Module:  "example.invalid/nothing-here",
		Current: built + "+dirty",
	}).Update(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if out.Updated {
		t.Error("a dirty local build was replaced")
	}

	if !strings.Contains(out.Reason, "local build") {
		t.Errorf("Reason = %q, want it to say why", out.Reason)
	}
}
