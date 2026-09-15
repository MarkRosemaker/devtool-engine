package selfupdate

import (
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
	if got := strings.Join((&Updater{}).env(), " "); strings.Contains(got, "GOPROXY=direct") {
		t.Error("went direct without being asked")
	}

	if got := strings.Join((&Updater{Direct: true}).env(), " "); !strings.Contains(got, "GOPROXY=direct") {
		t.Error("Direct did not disable the proxy")
	}
}

// TestLatestNeedsAModule guards the exported check, which a caller may use on
// its own to report drift without installing anything.
func TestLatestNeedsAModule(t *testing.T) {
	if _, err := (&Updater{}).Latest(t.Context()); err == nil {
		t.Error("resolving with no module reported success")
	}
}
