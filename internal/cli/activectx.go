package cli

import (
	"os"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/state"
)

// resolveActiveProject picks the default project for a command when none was given
// on the --project flag (spec 30 active-context resolution). Precedence:
//
//	DEVSTACK_PROJECT env → the persisted active context → the single/first project.
//
// The persisted active project is only honored when its workspace_root matches the
// workspace discovered at command time, so a stale pointer never leaks a project
// into a different workspace. Any candidate must name a real project in the model.
// db may be nil (callers without a ledger handle); persisted resolution is skipped.
func resolveActiveProject(m *config.Model, db *state.DB) string {
	names := sortedProjectNames(m)
	inModel := func(p string) bool { return p != "" && contains(names, p) }

	if env := os.Getenv("DEVSTACK_PROJECT"); inModel(env) {
		return env
	}
	if db != nil {
		if a, ok, err := db.ActiveContext(); err == nil && ok {
			if a.WorkspaceRoot == m.Root && inModel(a.Project) {
				return a.Project
			}
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}
