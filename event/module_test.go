package event

import "testing"

func TestModuleChangeString(t *testing.T) {
	const (
		morning = "v0.0.0-20260927120855-e8f3c270f455"
		evening = "v0.0.0-20260927160336-4edd48539178"
		nextDay = "v0.0.0-20260928090000-aaaaaaaaaaaa"
	)

	for _, tc := range []struct {
		name string
		m    ModuleChange
		want string
	}{
		{"tagged", ModuleChange{Path: "golang.org/x/mod", From: "v0.40.0", To: "v0.41.0"}, "golang.org/x/mod v0.40.0 => v0.41.0"},
		{"pseudo, same day", ModuleChange{Path: "example.com/m", From: morning, To: evening}, "example.com/m 2026-09-27 12:08 => 16:03"},
		{"pseudo, next day", ModuleChange{Path: "example.com/m", From: evening, To: nextDay}, "example.com/m 2026-09-27 16:03 => 2026-09-28 09:00"},
		{"tag to pseudo", ModuleChange{Path: "gitlab.com/o/m", From: "v1.2.3", To: nextDay}, "gitlab.com/o/m v1.2.3 => 2026-09-28 09:00"},
		// GitHub is where everything lives, so it goes.
		{"on GitHub", ModuleChange{Path: "github.com/MarkRosemaker/openapi-codegen", From: morning, To: evening}, "MarkRosemaker/openapi-codegen 2026-09-27 12:08 => 16:03"},
		{"added", ModuleChange{Path: "example.com/m", To: "v1.0.0"}, "example.com/m v1.0.0 (added)"},
		{"removed", ModuleChange{Path: "example.com/m", From: morning}, "example.com/m 2026-09-27 12:08 (removed)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTagged(t *testing.T) {
	for v, want := range map[string]bool{"v1.2.3": true, "v0.0.0-20260927120855-e8f3c270f455": false, "": false} {
		if got := Tagged(v); got != want {
			t.Errorf("Tagged(%q) = %v, want %v", v, got, want)
		}
	}
}
