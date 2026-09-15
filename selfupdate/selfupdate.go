// Package selfupdate keeps a go-installed binary current without publishing
// releases.
//
// The usual mechanism downloads a built asset from a GitHub release and swaps
// it over the running executable. This does neither. It asks the Go toolchain
// what the module's latest version is and, if that is not the version this
// binary was built from, runs "go install module@version" — which is how the
// binary was installed in the first place, so there is one way a build gets
// onto a machine rather than two.
//
// What that buys, beyond not having to cut a release to ship a fix: it works
// from the first commit of a repository that has never been tagged, because
// the toolchain resolves an untagged module to a pseudo-version naming the
// commit. It works for a private module too, on a machine whose git can
// already read it.
//
// What it costs: a Go toolchain on the machine, and the new binary takes
// effect at the next invocation rather than the current one. For a process
// that runs the tool as a child that is not a cost at all — the next run picks
// it up, which is the point.
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// devel is what the toolchain stamps into a binary built from a working tree
// rather than installed from a module.
const devel = "(devel)"

// Updater checks one module and installs it.
type Updater struct {
	// Module is the path to install, as "go install" would take it, without a
	// version: "github.com/MarkRosemaker/devtool".
	Module string

	// Current is the version this binary was built from, as [Version] reads
	// it. An empty or "(devel)" value stops the update rather than replacing
	// somebody's local build with a published one.
	Current string

	// Direct asks the toolchain to skip the module proxy for this module when
	// resolving its latest version.
	//
	// Worth setting wherever a change is expected to take effect promptly:
	// proxy.golang.org caches its answer to "what is the latest version", so
	// for some minutes after a push it goes on reporting the version before
	// it. Going direct asks the repository itself and costs a git operation.
	//
	// It is scoped to this module rather than set as GOPROXY=direct, which
	// would send everything else direct too — including the toolchain
	// download that "go install" starts when the module needs a newer Go than
	// the one running. That comes from the proxy, and a machine that cannot
	// reach go.dev fails the install outright.
	Direct bool
}

// Outcome says what an update did, so a caller can report it without
// inspecting an error to find out whether anything happened.
type Outcome struct {
	// Current and Latest are the versions compared. Latest is empty when the
	// check did not get as far as asking.
	Current, Latest string

	// Updated says a new version was installed.
	Updated bool

	// Reason says why nothing was installed, and is empty when something was.
	Reason string

	// Shadowed names a binary earlier on PATH than the one just installed, so
	// a caller can say why an update appeared to do nothing. Empty otherwise.
	Shadowed string
}

// String describes the outcome in one line.
func (o Outcome) String() string {
	switch {
	case o.Updated && o.Shadowed != "":
		return fmt.Sprintf("updated %s to %s, but %s comes first on PATH and will be the one that runs",
			o.Current, o.Latest, o.Shadowed)
	case o.Updated:
		return fmt.Sprintf("updated %s to %s", o.Current, o.Latest)
	default:
		return fmt.Sprintf("not updated: %s", o.Reason)
	}
}

// ErrNoToolchain reports that the Go toolchain is missing, which is the one
// requirement this mechanism has beyond a network.
var ErrNoToolchain = errors.New("no go toolchain on PATH")

// Update brings the binary up to date, returning what it did.
//
// A failure to reach the network is an error; being already current, or built
// locally, is an [Outcome] with a reason and no error. The distinction matters
// to the caller: one is worth retrying and the other is the normal case.
func (u *Updater) Update(ctx context.Context) (Outcome, error) {
	out := Outcome{Current: u.Current}

	if u.Module == "" {
		return out, errors.New("no module to update from")
	}

	switch u.Current {
	case "":
		out.Reason = "this binary carries no version, so there is nothing to compare"

		return out, nil
	case devel:
		out.Reason = "this is a local build, which a published one should not replace"

		return out, nil
	}

	if _, err := exec.LookPath("go"); err != nil {
		return out, ErrNoToolchain
	}

	latest, err := u.Latest(ctx)
	if err != nil {
		return out, err
	}

	out.Latest = latest

	if latest == u.Current {
		out.Reason = "already on the latest version"

		return out, nil
	}

	if err := u.install(ctx, latest); err != nil {
		return out, err
	}

	out.Updated = true
	out.Shadowed = u.shadowing(ctx)

	return out, nil
}

// Latest asks the toolchain to resolve the module's latest version.
//
// "go list -m" rather than a request to the proxy by hand: it honours whatever
// GOPROXY, GOPRIVATE and credentials the machine is already configured with,
// which is what makes this work for a private module without this package
// knowing anything about authentication.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	if u.Module == "" {
		return "", errors.New("no module to resolve")
	}

	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Version}}",
		u.Module+"@latest")
	cmd.Env = u.env()

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving the latest %s: %w", u.Module, stderrOf(err))
	}

	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("resolving the latest %s: the toolchain named no version", u.Module)
	}

	return version, nil
}

// install runs the same command that put this binary here in the first place.
//
// The resolved version is pinned rather than "@latest" repeated, so the
// version installed is the one that was compared against and not a newer one
// that landed in between.
func (u *Updater) install(ctx context.Context, version string) error {
	cmd := exec.CommandContext(ctx, "go", "install", u.Module+"@"+version)
	cmd.Env = u.env()

	// Output rather than Run: an ExitError only carries stderr when the
	// command was run this way, and "exit status 1" on its own says nothing
	// about why an install failed.
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("installing %s@%s: %w", u.Module, version, stderrOf(err))
	}

	return nil
}

// env is the environment for the toolchain, bypassing the proxy for this one
// module when a stale answer is not acceptable.
//
// GONOPROXY and GONOSUMDB name the module rather than GOPROXY naming nothing:
// the bypass has to be narrow, or the toolchain download goes direct with it,
// and that is served by the proxy.
func (u *Updater) env() []string {
	env := append(os.Environ(), "GO111MODULE=on")

	if u.Direct {
		env = append(env,
			"GONOPROXY="+prepend(patterns("GONOPROXY"), u.Module),
			"GONOSUMDB="+prepend(patterns("GONOSUMDB"), u.Module),
		)
	}

	return env
}

// patterns is the effective value of one of the bypass variables.
//
// Setting GONOPROXY or GONOSUMDB *overrides* GOPRIVATE rather than adding to
// it, so writing either without carrying GOPRIVATE across silently turns the
// bypass off for everything else the machine calls private. That is not
// hypothetical: it left this module exempt while its own private dependency
// was not, and the install failed trying to verify that dependency against the
// public checksum database.
func patterns(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return os.Getenv("GOPRIVATE")
}

// prepend puts module at the front of a comma-separated pattern list, leaving
// an empty list as just the module and not repeating a module already in it.
func prepend(list, module string) string {
	if list == "" {
		return module
	}

	for _, p := range strings.Split(list, ",") {
		if p == module {
			return list
		}
	}

	return module + "," + list
}

// shadowing returns a binary that would run instead of the one just installed.
//
// "go install" writes to GOBIN, which is not necessarily the copy PATH finds
// first. An older build earlier on PATH makes a successful update look like it
// did nothing at all, and that is a confusing afternoon unless somebody says
// so out loud.
func (u *Updater) shadowing(ctx context.Context) string {
	name := u.Module[strings.LastIndex(u.Module, "/")+1:]

	found, err := exec.LookPath(name)
	if err != nil {
		return ""
	}

	installed := filepath.Join(gobin(ctx), name)
	if sameFile(found, installed) {
		return ""
	}

	return found
}

// sameFile compares two paths by identity where it can and by name otherwise,
// so a symlink or a "./" does not read as a different binary.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}

	fa, err := os.Stat(a)
	if err != nil {
		return false
	}

	fb, err := os.Stat(b)
	if err != nil {
		return false
	}

	return os.SameFile(fa, fb)
}

// gobin is where "go install" writes: GOBIN when it is set, and GOPATH/bin
// otherwise.
func gobin(ctx context.Context) string {
	if dir := goEnv(ctx, "GOBIN"); dir != "" {
		return dir
	}

	if dir := goEnv(ctx, "GOPATH"); dir != "" {
		return filepath.Join(dir, "bin")
	}

	return ""
}

// goEnv reads one value out of "go env".
func goEnv(ctx context.Context, key string) string {
	out, err := exec.CommandContext(ctx, "go", "env", key).Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// stderrOf puts what the toolchain said on stderr into the error, which is
// otherwise only an exit status.
func stderrOf(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}

	return err
}

// Version reports the version this binary was built from, as "go install"
// would name it: a tag, a pseudo-version, or "(devel)" for a local build.
//
// It reads what the toolchain stamped in rather than a constant somebody
// maintains, because the failure this exists to prevent is a binary that is
// not what its source says.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	return info.Main.Version
}
