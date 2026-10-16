package maintain

import (
	"slices"
	"strings"

	"github.com/MarkRosemaker/devtool-engine/event"
	"golang.org/x/mod/modfile"
)

// RepoDeps sorts the repositories requires names by whether keys maintains
// them, keys being every repository in the set as "owner/name".
//
// maintained is the keys among them, spelt as keys spells them: the edges a
// dependency graph orders the set by. unmaintained is every other repository
// of an owner keys names — one that could be maintained first and is not.
// Each repository is named once, however many of its modules are required,
// and a requirement outside GitHub or of another owner is in neither.
func RepoDeps(requires []*modfile.Require, keys []string) (maintained, unmaintained []string) {
	// GitHub's names are case-insensitive, so a module path may spell one
	// differently from the set and still name the same repository.
	known := make(map[string]string, len(keys))
	owners := make(map[string]bool, len(keys))

	for _, key := range keys {
		known[strings.ToLower(key)] = key

		owner, _, _ := strings.Cut(key, "/")
		owners[strings.ToLower(owner)] = true
	}

	for _, req := range requires {
		key, ok := moduleRepo(req.Mod.Path)
		if !ok {
			continue
		}

		if k, ok := known[strings.ToLower(key)]; ok {
			if !slices.Contains(maintained, k) {
				maintained = append(maintained, k)
			}

			continue
		}

		owner, _, _ := strings.Cut(key, "/")
		if owners[strings.ToLower(owner)] && !slices.Contains(unmaintained, key) {
			unmaintained = append(unmaintained, key)
		}
	}

	return maintained, unmaintained
}

// moduleRepo is the repository a module on GitHub lives in, as "owner/name":
// the first two elements after the host, whatever follows them — a major
// version, or a module in a subdirectory.
func moduleRepo(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "github.com/")
	if !ok {
		return "", false
	}

	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}

	return event.Key(parts[0], parts[1]), true
}
