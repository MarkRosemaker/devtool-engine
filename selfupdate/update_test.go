package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The test binary stands in for the go command when it is run under this
// variable, so Update can be driven end to end without a network: asked the
// latest version, it answers from the environment, and asked to install, it
// writes a file where go install would.
const fakeGoEnv = "SELFUPDATE_FAKE_GO"

func TestMain(m *testing.M) {
	if os.Getenv(fakeGoEnv) == "1" {
		os.Exit(fakeGo(os.Args[1:]))
	}

	os.Exit(m.Run())
}

// fakeGo answers the three questions Update asks the go command, logging each
// with the bypass it was given, and fails where the environment says to.
func fakeGo(args []string) int {
	if log := os.Getenv("FAKE_GO_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%s\tGONOPROXY=%s\n", strings.Join(args, " "), os.Getenv("GONOPROXY"))
			f.Close() //nolint:errcheck
		}
	}

	switch {
	case len(args) == 2 && args[0] == "env":
		fmt.Println(os.Getenv("FAKE_GO_" + args[1]))
	case len(args) > 0 && args[0] == "list":
		if msg := os.Getenv("FAKE_GO_LIST_FAILS"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)

			return 1
		}

		fmt.Println(os.Getenv("FAKE_GO_LATEST"))
	case len(args) == 2 && args[0] == "install":
		if msg := os.Getenv("FAKE_GO_INSTALL_FAILS"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)

			return 1
		}

		dir := os.Getenv("FAKE_GO_GOBIN")
		if dir == "" {
			dir = filepath.Join(os.Getenv("FAKE_GO_GOPATH"), "bin")
		}

		module, _, _ := strings.Cut(args[1], "@")
		name := module[strings.LastIndex(module, "/")+1:]

		if err := os.WriteFile(filepath.Join(dir, name), []byte(args[1]), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)

			return 1
		}
	default:
		fmt.Fprintf(os.Stderr, "fake go: unexpected %q\n", args)

		return 2
	}

	return 0
}

// toolchain is a fake go command on PATH, with a GOBIN of its own.
type toolchain struct {
	dir, gobin, log string
}

// fakeToolchain puts the fake go command first on PATH, followed by extra.
// Latest is what it reports as the module's newest version.
func fakeToolchain(t *testing.T, latest string, extra ...string) toolchain {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the fake go command is a symlink to the test binary")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	tc := toolchain{dir: t.TempDir(), gobin: t.TempDir()}
	tc.log = filepath.Join(tc.dir, "calls")

	if err := os.Symlink(self, filepath.Join(tc.dir, "go")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", strings.Join(append([]string{tc.dir}, extra...), string(os.PathListSeparator)))
	t.Setenv(fakeGoEnv, "1")
	t.Setenv("FAKE_GO_LOG", tc.log)
	t.Setenv("FAKE_GO_LATEST", latest)
	t.Setenv("FAKE_GO_GOBIN", tc.gobin)
	t.Setenv("FAKE_GO_GOPATH", "")

	return tc
}

// calls is every command the fake was asked, in order.
func (tc toolchain) calls(t *testing.T) []string {
	t.Helper()

	b, err := os.ReadFile(tc.log)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// installs is the install commands among them.
func (tc toolchain) installs(t *testing.T) []string {
	t.Helper()

	var installs []string

	for _, c := range tc.calls(t) {
		if strings.HasPrefix(c, "install ") {
			cmd, _, _ := strings.Cut(c, "\t")
			installs = append(installs, cmd)
		}
	}

	return installs
}

// An update installs the version it compared against, pinned, and says where
// it put it.
func TestUpdateInstallsTheLatestVersion(t *testing.T) {
	tc := fakeToolchain(t, "v1.1.0")

	out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := Outcome{
		Current:   "v1.0.0",
		Latest:    "v1.1.0",
		Updated:   true,
		Installed: filepath.Join(tc.gobin, "tool"),
	}
	if out != want {
		t.Errorf("Outcome = %+v, want %+v", out, want)
	}

	if got := tc.installs(t); len(got) != 1 || got[0] != "install example.com/x/tool@v1.1.0" {
		t.Errorf("installs = %q, want the one version it resolved", got)
	}

	if b, err := os.ReadFile(out.Installed); err != nil || string(b) != "example.com/x/tool@v1.1.0" {
		t.Errorf("nothing at %s: %v", out.Installed, err)
	}
}

// A binary already on the latest version installs nothing.
func TestUpdateLeavesTheLatestAlone(t *testing.T) {
	tc := fakeToolchain(t, "v1.1.0")

	out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.1.0"}).Update(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if out.Updated || out.Latest != "v1.1.0" || !strings.Contains(out.Reason, "latest") {
		t.Errorf("Outcome = %+v, want nothing installed and the reason why", out)
	}

	if got := tc.installs(t); len(got) != 0 {
		t.Errorf("installed %q on a binary already current", got)
	}
}

// An older copy earlier on PATH than the one installed is named, since it is
// the one that will run; one later on PATH, or the installed copy itself, is
// not.
func TestUpdateNamesACopyThatComesFirst(t *testing.T) {
	older := t.TempDir()
	if err := os.WriteFile(filepath.Join(older, "tool"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("an older copy first on PATH", func(t *testing.T) {
		tc := fakeToolchain(t, "v1.1.0", older)
		t.Setenv("PATH", older+string(os.PathListSeparator)+os.Getenv("PATH")+
			string(os.PathListSeparator)+tc.gobin)

		out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if want := filepath.Join(older, "tool"); out.Shadowed != want {
			t.Errorf("Shadowed = %q, want %q", out.Shadowed, want)
		}

		if !strings.Contains(out.String(), "comes first on PATH") {
			t.Errorf("String() = %q, want it to say which copy runs", out)
		}
	})

	t.Run("the installed copy first on PATH", func(t *testing.T) {
		tc := fakeToolchain(t, "v1.1.0")
		t.Setenv("PATH", tc.gobin+string(os.PathListSeparator)+os.Getenv("PATH")+
			string(os.PathListSeparator)+older)

		out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if out.Shadowed != "" {
			t.Errorf("Shadowed = %q, want none where the new copy is the one that runs", out.Shadowed)
		}
	})
}

// A failure says what the toolchain said, and a failure to resolve installs
// nothing.
func TestUpdateCarriesTheToolchainsReason(t *testing.T) {
	t.Run("resolving", func(t *testing.T) {
		tc := fakeToolchain(t, "")
		t.Setenv("FAKE_GO_LIST_FAILS", "module lookup disabled by GOPROXY=off")

		_, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
		if err == nil || !strings.Contains(err.Error(), "module lookup disabled") {
			t.Errorf("err = %v, want the toolchain's reason", err)
		}

		if got := tc.installs(t); len(got) != 0 {
			t.Errorf("installed %q without a version to install", got)
		}
	})

	t.Run("resolving to nothing", func(t *testing.T) {
		fakeToolchain(t, "")

		_, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
		if err == nil || !strings.Contains(err.Error(), "named no version") {
			t.Errorf("err = %v, want it to say no version came back", err)
		}
	})

	t.Run("installing", func(t *testing.T) {
		fakeToolchain(t, "v1.1.0")
		t.Setenv("FAKE_GO_INSTALL_FAILS", "go: example.com/x/tool@v1.1.0 requires go >= 1.99")

		out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
		if err == nil || !strings.Contains(err.Error(), "requires go >= 1.99") {
			t.Errorf("err = %v, want the toolchain's reason", err)
		}

		if out.Updated {
			t.Error("a failed install reported an update")
		}
	})
}

// Without a go command there is nothing to update with, and that is said as
// such rather than as a failure to resolve.
func TestUpdateWithoutAToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
	if !errors.Is(err, ErrNoToolchain) {
		t.Errorf("err = %v, want %v", err, ErrNoToolchain)
	}
}

// Where GOBIN is unset, go install writes to GOPATH/bin, and that is where
// the new copy is reported.
func TestUpdateFallsBackToGOPATH(t *testing.T) {
	fakeToolchain(t, "v1.1.0")

	gopath := t.TempDir()
	if err := os.Mkdir(filepath.Join(gopath, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FAKE_GO_GOBIN", "")
	t.Setenv("FAKE_GO_GOPATH", gopath)

	out, err := (&Updater{Module: "example.com/x/tool", Current: "v1.0.0"}).Update(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if want := filepath.Join(gopath, "bin", "tool"); out.Installed != want {
		t.Errorf("Installed = %q, want %q", out.Installed, want)
	}
}

// Direct reaches both commands as a bypass for this module alone; without it,
// neither carries one.
func TestUpdateGoesDirectForBothCommands(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(fmt.Sprint("direct=", direct), func(t *testing.T) {
			tc := fakeToolchain(t, "v1.1.0")
			t.Setenv("GOPRIVATE", "")
			t.Setenv("GONOPROXY", "")

			u := &Updater{Module: "example.com/x/tool", Current: "v1.0.0", Direct: direct}
			if _, err := u.Update(t.Context()); err != nil {
				t.Fatal(err)
			}

			for _, call := range tc.calls(t) {
				if strings.HasPrefix(call, "env ") {
					continue
				}

				bypassed := strings.HasSuffix(call, "GONOPROXY=example.com/x/tool")
				if bypassed != direct {
					t.Errorf("%q: bypassed = %v, want %v", call, bypassed, direct)
				}
			}
		})
	}
}
