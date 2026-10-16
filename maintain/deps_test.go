package maintain_test

import (
	"slices"
	"testing"

	"github.com/MarkRosemaker/devtool-engine/maintain"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

func requires(paths ...string) []*modfile.Require {
	reqs := make([]*modfile.Require, len(paths))
	for i, p := range paths {
		reqs[i] = &modfile.Require{Mod: module.Version{Path: p, Version: "v1.0.0"}}
	}

	return reqs
}

func TestRepoDeps(t *testing.T) {
	keys := []string{"MarkRosemaker/jsonutil", "go-api-libs/types"}

	for _, tc := range []struct {
		name                     string
		requires                 []*modfile.Require
		maintained, unmaintained []string
	}{
		{
			name:       "a repository in the set is maintained",
			requires:   requires("github.com/MarkRosemaker/jsonutil"),
			maintained: []string{"MarkRosemaker/jsonutil"},
		},
		{
			name:       "the owner's case does not matter, and the set's spelling wins",
			requires:   requires("github.com/markrosemaker/jsonutil"),
			maintained: []string{"MarkRosemaker/jsonutil"},
		},
		{
			name:       "a major version or a submodule is the same repository, named once",
			requires:   requires("github.com/go-api-libs/types/v2", "github.com/go-api-libs/types/sub"),
			maintained: []string{"go-api-libs/types"},
		},
		{
			name:         "another repository of an owner in the set is unmaintained",
			requires:     requires("github.com/MarkRosemaker/errpath", "github.com/go-api-libs/api/v3"),
			unmaintained: []string{"MarkRosemaker/errpath", "go-api-libs/api"},
		},
		{
			name:     "another owner's repository is neither",
			requires: requires("github.com/spf13/afero", "golang.org/x/mod", "github.com/short"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maintained, unmaintained := maintain.RepoDeps(tc.requires, keys)

			if !slices.Equal(maintained, tc.maintained) {
				t.Errorf("maintained = %v, want %v", maintained, tc.maintained)
			}

			if !slices.Equal(unmaintained, tc.unmaintained) {
				t.Errorf("unmaintained = %v, want %v", unmaintained, tc.unmaintained)
			}
		})
	}
}
