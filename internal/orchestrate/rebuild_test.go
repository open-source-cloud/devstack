package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// projectBuild reports whether a `compose build` (optionally --no-cache) ran for
// the project stack. It matches on exact argv tokens (not substrings) so a temp
// path containing "build" (e.g. the test name) can't produce a false positive.
func projectBuild(fr *fakeRunner, project string) (built, noCache bool) {
	for _, cmd := range fr.cmds {
		joined := strings.Join(cmd, " ")
		if !strings.Contains(joined, "-p "+project) {
			continue
		}
		if slices.Contains(cmd, "build") {
			built = true
			if slices.Contains(cmd, "--no-cache") {
				noCache = true
			}
		}
	}
	return built, noCache
}

// TestRebuildForcesNoCache asserts the spec-26 --rebuild threading: UpDeps.Rebuild
// makes the compose-up phase run `compose build --no-cache`, whereas plain --build
// builds without --no-cache and no build flag builds not at all.
func TestRebuildForcesNoCache(t *testing.T) {
	cases := []struct {
		name        string
		build       bool
		rebuild     bool
		wantBuild   bool
		wantNoCache bool
	}{
		{"neither", false, false, false, false},
		{"build only", true, false, true, false},
		{"rebuild forces no-cache", false, true, true, true},
		{"both", true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, fr, db := upFixture(t)
			d.Build = c.build
			d.Rebuild = c.rebuild
			phases, err := BuildUp(d)
			if err != nil {
				t.Fatalf("BuildUp: %v", err)
			}
			saga := &Saga{Workspace: d.Model.Workspace.Name, DB: db, LockPath: d.LockPath}
			if _, err := saga.Run(context.Background(), phases); err != nil {
				t.Fatalf("saga: %v", err)
			}
			built, noCache := projectBuild(fr, "devstack-app")
			if built != c.wantBuild {
				t.Errorf("built = %v, want %v (cmds: %v)", built, c.wantBuild, fr.cmds)
			}
			if noCache != c.wantNoCache {
				t.Errorf("noCache = %v, want %v (cmds: %v)", noCache, c.wantNoCache, fr.cmds)
			}
		})
	}
}
