package generate

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
)

// Generator owns the config→compose pipeline for one loaded workspace. It is
// created once (resolving the shared engines' ports up front) and then asked to
// generate the shared stack and each project stack.
type Generator struct {
	model      *config.Model
	src        template.TemplateSource
	env        map[string]string
	profile    string
	sharedPort map[string]int
}

// Option configures a Generator.
type Option func(*Generator)

// WithEnv injects the host environment used to resolve ${env.NAME}. Defaults to
// the process environment; tests inject a fixed map for determinism.
func WithEnv(env map[string]string) Option {
	return func(g *Generator) { g.env = env }
}

// WithProfile sets the active env-overlay profile used for ${profile}. Defaults
// to the workspace's profiles.default (or "dev").
func WithProfile(profile string) Option {
	return func(g *Generator) {
		if profile != "" {
			g.profile = profile
		}
	}
}

// New builds a Generator for the loaded model, resolving each shared engine's
// template so its in-network port is known to the ${ref} resolver.
func New(m *config.Model, src template.TemplateSource, opts ...Option) (*Generator, error) {
	g := &Generator{
		model:      m,
		src:        src,
		env:        osEnvMap(),
		profile:    defaultProfile(m),
		sharedPort: map[string]int{},
	}
	for _, o := range opts {
		o(g)
	}
	// Generate-time collision guardrail (ARCHITECTURE §4) — fail fast before any
	// rendering if the workspace would produce ambiguous DNS on the shared network.
	if err := lintWorkspace(m); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(m.Workspace.Shared) {
		ss := m.Workspace.Shared[name]
		res, err := template.Resolve(src, ss.Template, ss.Params)
		if err != nil {
			return nil, fmt.Errorf("shared service %q: %w", name, err)
		}
		g.sharedPort[name] = res.DefaultPort
	}
	return g, nil
}

// resolver builds a fresh graphResolver bound to this generator's state.
func (g *Generator) resolver() *graphResolver {
	return &graphResolver{
		model:      g.model,
		sharedPort: g.sharedPort,
		env:        g.env,
		profile:    g.profile,
	}
}

// Stack is one fully-generated compose stack held in memory before it is written.
type Stack struct {
	// Name is the compose project name (devstack-<project> or devstack-shared).
	Name string
	// Kind is "shared" or "project".
	Kind string
	// OutDir is the absolute directory the artifacts are written under.
	OutDir string
	// Compose is the canonical, validated compose document.
	Compose []byte
	// BuildFiles maps a path RELATIVE to OutDir to its rendered bytes
	// (e.g. "api/build/Dockerfile").
	BuildFiles map[string][]byte
	// BuildHashes maps each build context (service name) to its rebuild SHA-256.
	BuildHashes map[string]string
}

// GenerateShared builds the shared-services stack from workspace.yaml's shared:
// block. Returns nil (no stack) when no shared services are declared.
func (g *Generator) GenerateShared() (*Stack, error) {
	names := sortedKeys(g.model.Workspace.Shared)
	if len(names) == 0 {
		return nil, nil
	}
	services := map[string]any{}
	volumes := map[string]any{}
	for _, name := range names {
		ss := g.model.Workspace.Shared[name]
		res, err := template.Resolve(g.src, ss.Template, ss.Params)
		if err != nil {
			return nil, fmt.Errorf("shared service %q: %w", name, err)
		}
		svc, err := buildSharedService(g.model, name, res)
		if err != nil {
			return nil, err
		}
		services[name] = svc
		maps.Copy(volumes, res.Volumes)
	}
	outDir := filepath.Join(g.model.Root, GenDir, "shared")
	doc := composeDoc{
		"name":     SharedStackName,
		"services": services,
		"networks": externalNetworkOnly(),
	}
	if len(volumes) > 0 {
		doc["volumes"] = volumes
	}
	compose, err := validateAndMarshal(doc, outDir)
	if err != nil {
		return nil, fmt.Errorf("shared stack: %w", err)
	}
	return &Stack{
		Name: SharedStackName, Kind: "shared", OutDir: outDir,
		Compose: compose, BuildFiles: map[string][]byte{}, BuildHashes: map[string]string{},
	}, nil
}

// GenerateProject builds one project's stack from its devstack.yaml.
func (g *Generator) GenerateProject(name string) (*Stack, error) {
	p, ok := g.model.Projects[name]
	if !ok {
		return nil, fmt.Errorf("project %q is not in this workspace", name)
	}
	projDir := g.model.ProjectDir(name)
	if projDir == "" {
		return nil, fmt.Errorf("project %q has no resolved directory", name)
	}
	outDir := filepath.Join(projDir, GenDir)

	res := g.resolver()
	services := map[string]any{}
	buildFiles := map[string][]byte{}
	buildHashes := map[string]string{}

	for _, sname := range sortedKeys(p.Services) {
		svc := p.Services[sname]
		resolved, err := template.Resolve(g.src, svc.Template, svc.Params)
		if err != nil {
			return nil, fmt.Errorf("project %q service %q: %w", name, sname, err)
		}
		svcMap, err := buildProjectService(res, g.model, name, sname, svc, resolved)
		if err != nil {
			return nil, err
		}
		services[sname] = svcMap

		// Stage this service's build context (files + rebuild hash) when it builds.
		if len(resolved.BuildFiles) > 0 {
			for _, rel := range resolved.BuildFileNames() {
				dst := filepath.ToSlash(filepath.Join(sname, "build", rel))
				buildFiles[dst] = resolved.BuildFiles[rel]
			}
			buildHashes[sname] = hashBuildContext(resolved.BuildFiles, svcMap["build"])
		}
	}

	doc := composeDoc{
		"name":     projectStackName(name),
		"services": services,
		"networks": projectNetworks(),
	}
	compose, err := validateAndMarshal(doc, outDir)
	if err != nil {
		return nil, fmt.Errorf("project %q: %w", name, err)
	}
	return &Stack{
		Name: projectStackName(name), Kind: "project", OutDir: outDir,
		Compose: compose, BuildFiles: buildFiles, BuildHashes: buildHashes,
	}, nil
}

// GenerateAll builds the shared stack (if any) followed by every project stack,
// in deterministic order.
func (g *Generator) GenerateAll() ([]*Stack, error) {
	var stacks []*Stack
	shared, err := g.GenerateShared()
	if err != nil {
		return nil, err
	}
	if shared != nil {
		stacks = append(stacks, shared)
	}
	for _, name := range sortedProjectNames(g.model) {
		st, err := g.GenerateProject(name)
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, st)
	}
	return stacks, nil
}

// UpToDate reports whether every artifact this stack would write already matches
// what is on disk — the basis for `generate --check` (CI drift gate). It does not
// consider state.json (a pure bookkeeping file).
func (s *Stack) UpToDate() bool {
	if !fileMatches(filepath.Join(s.OutDir, ComposeFile), s.Compose) {
		return false
	}
	for rel, data := range s.BuildFiles {
		if !fileMatches(filepath.Join(s.OutDir, filepath.FromSlash(rel)), data) {
			return false
		}
	}
	return true
}

func fileMatches(path string, data []byte) bool {
	existing, err := os.ReadFile(path)
	return err == nil && bytesEqual(existing, data)
}

// WriteResult reports what a Write changed for one stack.
type WriteResult struct {
	Stack          string   `json:"stack"`
	ComposePath    string   `json:"composePath"`
	ComposeChanged bool     `json:"composeChanged"`
	FilesChanged   []string `json:"filesChanged"`
	RebuildNeeded  []string `json:"rebuildNeeded"`
}

// Write materializes the stack: the compose document, every build file, and the
// state.json ledger — each via writeIfChanged (atomic). RebuildNeeded lists the
// build contexts whose content changed since the last generation.
func (s *Stack) Write() (WriteResult, error) {
	res := WriteResult{Stack: s.Name, ComposePath: filepath.Join(s.OutDir, ComposeFile)}

	// Clear any temp files a previously-killed run left behind before writing.
	if err := os.MkdirAll(s.OutDir, 0o755); err != nil {
		return res, err
	}
	sweepTemp(s.OutDir)

	statePath := filepath.Join(s.OutDir, StateFile)
	prev := loadState(statePath)

	changed, err := writeIfChanged(res.ComposePath, s.Compose)
	if err != nil {
		return res, err
	}
	res.ComposeChanged = changed

	for _, rel := range sortedKeys(s.BuildFiles) {
		p := filepath.Join(s.OutDir, filepath.FromSlash(rel))
		ch, err := writeIfChanged(p, s.BuildFiles[rel])
		if err != nil {
			return res, err
		}
		if ch {
			res.FilesChanged = append(res.FilesChanged, rel)
		}
	}

	cur := State{Version: StateVersion, Stack: s.Name, BuildHashes: s.BuildHashes}
	res.RebuildNeeded = changedContexts(prev, cur)
	stateBytes, err := cur.marshal()
	if err != nil {
		return res, err
	}
	if _, err := writeIfChanged(statePath, stateBytes); err != nil {
		return res, err
	}
	return res, nil
}

// --- helpers -------------------------------------------------------------

// projectNetworks declares the per-project default network plus the tool-owned
// external shared network.
func projectNetworks() map[string]any {
	return map[string]any{
		"default":     map[string]any{},
		SharedNetwork: externalNetwork(),
	}
}

// externalNetworkOnly declares just the shared external network (shared stack).
func externalNetworkOnly() map[string]any {
	return map[string]any{SharedNetwork: externalNetwork()}
}

func externalNetwork() map[string]any {
	return map[string]any{"external": true, "name": SharedNetwork}
}

func defaultProfile(m *config.Model) string {
	if p := m.Workspace.Profiles.Default; p != "" {
		return p
	}
	return "dev"
}

func osEnvMap() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

func sortedProjectNames(m *config.Model) []string {
	out := make([]string, 0, len(m.Projects))
	for k := range m.Projects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
