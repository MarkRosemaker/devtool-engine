package maintain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/MarkRosemaker/devtool-engine/event"
	"github.com/spf13/afero"
	"golang.org/x/mod/modfile"
)

const (
	// goModPath is where a commit's module changes are read from.
	goModPath = "go.mod"

	// vendorDir is where "go mod vendor" puts its copies.
	vendorDir = "vendor/"
)

// readGoMod returns the go.mod as it stands, or nil where there is none or it
// cannot be read: not knowing what moved is no reason to fail a task that
// has already succeeded.
func readGoMod(fs afero.Fs) []byte {
	b, err := afero.ReadFile(fs, goModPath)
	if err != nil {
		return nil
	}

	return b
}

// moduleChanges compares the requirements of go.mod before and after a task,
// the way "go get -u" reports them: each module whose version moved, and each
// one added or removed, sorted by path. Indirect requirements count, as they
// do there.
func moduleChanges(before, after []byte) []event.ModuleChange {
	from, ok := requirements(before)
	if !ok {
		return nil
	}

	to, ok := requirements(after)
	if !ok {
		return nil
	}

	var changes []event.ModuleChange

	for path, v := range to {
		if from[path] != v {
			changes = append(changes, event.ModuleChange{Path: path, From: from[path], To: v})
		}
	}

	for path, v := range from {
		if _, kept := to[path]; !kept {
			changes = append(changes, event.ModuleChange{Path: path, From: v})
		}
	}

	slices.SortFunc(changes, func(a, b event.ModuleChange) int {
		return strings.Compare(a.Path, b.Path)
	})

	return changes
}

// requirements maps each required module to its version. No go.mod has none,
// which is how a new one reports its requirements as added. A go.mod that does
// not parse is not the same as none: ok is false, and the caller reports
// nothing rather than every module on the other side as having moved.
func requirements(data []byte) (reqs map[string]string, ok bool) {
	if data == nil {
		return nil, true
	}

	f, err := modfile.ParseLax(goModPath, data, nil)
	if err != nil {
		return nil, false
	}

	reqs = make(map[string]string, len(f.Require))
	for _, r := range f.Require {
		reqs[r.Mod.Path] = r.Mod.Version
	}

	return reqs, true
}

// collapseVendor lists vendor/ once in place of every file under it.
// Re-vendoring touches hundreds of files; the modules that moved are what a
// reader wants, and they come from go.mod. vendor/ stays in the list so a
// change under it that go.mod does not explain is still visible.
func collapseVendor(files []string) []string {
	var out []string

	vendored := false

	for _, f := range files {
		if strings.HasPrefix(f, vendorDir) {
			vendored = true

			continue
		}

		out = append(out, f)
	}

	if vendored {
		out = append(out, vendorDir)
	}

	slices.Sort(out)

	return out
}

// maxDescribedFiles is how many changed files a failure names before it counts
// the rest: a reader needs to see where the change was, not every file in it.
const maxDescribedFiles = 10

// describeChanges says what a step changed, for a failure it caused: the files,
// with vendor/ named once, and each requirement it moved, the way "go get -u"
// reports them.
func describeChanges(files []string, modules []event.ModuleChange) string {
	files = collapseVendor(files)

	var b strings.Builder

	b.WriteString("changed: ")

	if len(files) > maxDescribedFiles {
		b.WriteString(strings.Join(files[:maxDescribedFiles], ", "))
		fmt.Fprintf(&b, " and %d more", len(files)-maxDescribedFiles)
	} else {
		b.WriteString(strings.Join(files, ", "))
	}

	for _, m := range modules {
		b.WriteString("\n")

		switch {
		case m.From == "":
			b.WriteString(m.Path + " " + m.To + " (added)")
		case m.To == "":
			b.WriteString(m.Path + " " + m.From + " (removed)")
		default:
			b.WriteString(m.Path + " " + m.From + " => " + m.To)
		}
	}

	return b.String()
}
