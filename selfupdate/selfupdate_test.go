package selfupdate

import (
	"os/exec"
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
