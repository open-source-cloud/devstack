package config

import (
	"fmt"
	"path/filepath"
)

// Load discovers the workspace by walking up from start, parses workspace.yaml
// and every referenced project's devstack.yaml, assembles the workspace Model,
// and validates it (structure + cross-references against the workspace graph).
// The returned Model is immutable.
func Load(start string) (*Model, error) {
	root, err := Discover(start)
	if err != nil {
		return nil, err
	}
	return LoadAt(root)
}

// LoadWorkspaceOnly discovers the workspace root and parses ONLY workspace.yaml
// (validated structurally), without loading each project's devstack.yaml — which
// may not exist yet (the `ws clone` case, before repos are on disk). Returns the
// root and the parsed Workspace.
func LoadWorkspaceOnly(start string) (string, *Workspace, error) {
	root, err := Discover(start)
	if err != nil {
		return "", nil, err
	}
	src, err := newSource(filepath.Join(root, WorkspaceFile))
	if err != nil {
		return "", nil, err
	}
	var ws Workspace
	if err := src.decode(&ws); err != nil {
		return "", nil, err
	}
	if err := structValidate(&ws); err != nil {
		return "", nil, formatStructErr(src.path, err)
	}
	return root, &ws, nil
}

// LoadAt loads the workspace rooted at an already-discovered directory.
func LoadAt(root string) (*Model, error) {
	wsSrc, err := newSource(filepath.Join(root, WorkspaceFile))
	if err != nil {
		return nil, err
	}
	var ws Workspace
	if err := wsSrc.decode(&ws); err != nil {
		return nil, err
	}

	m := &Model{
		Root:       root,
		Workspace:  ws,
		Projects:   make(map[string]Project, len(ws.Projects)),
		projectDir: make(map[string]string, len(ws.Projects)),
	}
	projSrc := make(map[string]*source, len(ws.Projects))

	seen := map[string]int{} // project name -> index, for duplicate detection
	for i, ref := range ws.Projects {
		if prev, dup := seen[ref.Name]; dup {
			return nil, wsSrc.errAt(fmt.Sprintf("$.projects[%d].name", i),
				"duplicate project name %q (also at projects[%d])", ref.Name, prev)
		}
		seen[ref.Name] = i

		dir := filepath.Join(root, ref.Path)
		pf := filepath.Join(dir, ProjectFile)
		src, err := newSource(pf)
		if err != nil {
			return nil, wsSrc.errAt(fmt.Sprintf("$.projects[%d].path", i),
				"project %q: cannot read %s: %v", ref.Name, filepath.Join(ref.Path, ProjectFile), err)
		}
		var p Project
		if err := src.decode(&p); err != nil {
			return nil, err
		}
		if p.Name != "" && p.Name != ref.Name {
			return nil, src.errAt("$.name",
				"project name %q does not match the workspace reference %q (workspace.yaml projects[%d])", p.Name, ref.Name, i)
		}
		m.Projects[ref.Name] = p
		projSrc[ref.Name] = src
		m.projectDir[ref.Name] = dir
	}

	if err := validateModel(m, wsSrc, projSrc); err != nil {
		return nil, err
	}
	return m, nil
}
