package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// WorkspaceFile is the marker filename that identifies a workspace root.
const WorkspaceFile = "workspace.yaml"

// ProjectFile is the per-repo project config filename.
const ProjectFile = "devstack.yaml"

// envWorkspace overrides discovery with an explicit workspace root.
const envWorkspace = "DEVSTACK_WORKSPACE"

// Discover finds the workspace root by walking up from start (git/go.mod style)
// until it finds a workspace.yaml, stopping at the filesystem root or $HOME
// (whichever comes first). The DEVSTACK_WORKSPACE env var, when set to a
// directory containing workspace.yaml, short-circuits the walk. Returns the
// absolute directory containing workspace.yaml.
func Discover(start string) (string, error) {
	if env := os.Getenv(envWorkspace); env != "" {
		dir, err := filepath.Abs(env)
		if err != nil {
			return "", fmt.Errorf("%s=%q: %w", envWorkspace, env, err)
		}
		if !fileExists(filepath.Join(dir, WorkspaceFile)) {
			return "", fmt.Errorf("%s=%q does not contain %s", envWorkspace, dir, WorkspaceFile)
		}
		return dir, nil
	}

	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve start dir %q: %w", start, err)
	}
	home, _ := os.UserHomeDir()

	for {
		if fileExists(filepath.Join(dir, WorkspaceFile)) {
			return dir, nil
		}
		if home != "" && dir == home {
			break // do not walk above the user's home
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root
		}
		dir = parent
	}
	return "", fmt.Errorf("no %s found in %q or any parent directory (is this a devstack workspace?)", WorkspaceFile, start)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
