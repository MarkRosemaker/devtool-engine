package maintain

import (
	"context"

	"github.com/spf13/afero"
	"golang.org/x/mod/modfile"
)

// GenLintfile returns a task that makes settings the repository's golangci-lint
// configuration, removing any other the repository carries so there is one
// answer to what it is linted against.
//
// The settings are a parameter rather than something this package holds: which
// linters to enable is the caller's opinion, and every caller has their own.
func GenLintfile(repo Repo, settings []byte) Task {
	return Task{
		Name:  "generate .golangci.yaml",
		Short: "lintgen",
		Run: func(context.Context) error {
			// Remove all other configuration
			for _, name := range []string{".golangci.yml", ".golangci.toml", ".golangci.json"} {
				if ok, err := afero.Exists(repo.Fs(), name); err != nil {
					return err
				} else if ok {
					if err := repo.Fs().Remove(name); err != nil {
						return err
					}
				}
			}

			// Write the authoritative, possibly updated lint config
			if err := afero.WriteFile(repo.Fs(), ".golangci.yaml", settings, 0o666); err != nil {
				return err
			}

			return nil
		},
	}
}

// ModuleDeps returns the keys of the modules in byPath that requires depends on.
//
// It is how a repository's go.mod is turned into edges for a dependency graph:
// byPath maps a module path to the key naming it, and anything not in there is
// outside the set being maintained and cannot constrain the order.
func ModuleDeps(requires []*modfile.Require, byPath map[string]string) []string {
	var deps []string

	for _, req := range requires {
		if key, ok := byPath[req.Mod.Path]; ok {
			deps = append(deps, key)
		}
	}

	return deps
}
