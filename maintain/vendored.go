package maintain

import (
	"bufio"
	"bytes"
	"path"
	"slices"
	"strings"

	"github.com/spf13/afero"
)

const (
	// vendorDir is where "go mod vendor" puts its copies, and vendorManifest
	// the list of the modules they came from.
	vendorDir      = "vendor/"
	vendorManifest = vendorDir + "modules.txt"
)

// splitVendored separates the paths under vendor/ from the rest, naming the
// module each belongs to in place of the file.
//
// The module is the longest one vendor/modules.txt lists that the file falls
// under, so a module nested inside another's path is told apart from it. A
// module the change removed is no longer listed, and its first three path
// elements stand in for it — right for every host that names a module
// host/owner/repo. The manifest itself changes with every re-vendoring and is
// not a module, so it names nothing.
func splitVendored(fs afero.Fs, files []string) (plain, modules []string) {
	listed := vendoredModules(fs)

	for _, f := range files {
		rel, ok := strings.CutPrefix(f, vendorDir)
		if !ok {
			plain = append(plain, f)

			continue
		}

		if f == vendorManifest {
			continue
		}

		if m := owningModule(listed, rel); !slices.Contains(modules, m) {
			modules = append(modules, m)
		}
	}

	slices.Sort(plain)
	slices.Sort(modules)

	return plain, modules
}

// vendoredModules reads the module paths vendor/modules.txt lists, longest
// first so the first match is the most specific. No manifest is not an error:
// a repository that does not vendor has nothing under vendor/ to name.
func vendoredModules(fs afero.Fs) []string {
	b, err := afero.ReadFile(fs, vendorManifest)
	if err != nil {
		return nil
	}

	var mods []string

	for sc := bufio.NewScanner(bytes.NewReader(b)); sc.Scan(); {
		// A module line reads "# path version"; "## explicit" and package
		// lines are not modules.
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "#" {
			mods = append(mods, fields[1])
		}
	}

	slices.SortFunc(mods, func(a, b string) int { return len(b) - len(a) })

	return mods
}

// owningModule names the module a vendored path belongs to.
func owningModule(listed []string, rel string) string {
	dir := path.Dir(rel)

	for _, m := range listed {
		if dir == m || strings.HasPrefix(dir, m+"/") {
			return m
		}
	}

	elems := strings.Split(dir, "/")

	return strings.Join(elems[:min(3, len(elems))], "/")
}
