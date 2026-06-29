// Package workspace is the differentiator (spec 03): it runs shared infrastructure
// ONCE and lets many project stacks attach to it, with per-project data isolation
// and reference counting derived from live reality. Every mutation of the ledger
// or the shared stack goes through the machine-global flock (the concurrency
// spine, DECISIONS D7).
//
// This file owns the ledger orchestration — resolving which shared instances a
// project consumes, registering/unregistering ref rows, self-healing reconcile
// against live containers, and the `shared status` projection. The daemon I/O
// (network ensure, compose up, provisioning) is driven through the injected
// interfaces so the logic is unit/race-testable without a real daemon.
package workspace

import (
	"context"
	"fmt"
	"sort"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
)

// Manager orchestrates the shared stack and ref-counting for one workspace.
type Manager struct {
	Model    *config.Model
	DB       *state.DB
	Docker   docker.Client
	Source   template.TemplateSource
	LockPath string
}

// SharedInstance is a resolved shared service: its config name, the DNS alias /
// ledger instance name it is reached by, and its (engine, majorVersion) identity.
type SharedInstance struct {
	Name   string // config shared name, e.g. "postgres"
	Alias  string // ledger/DNS instance name, e.g. "shared-postgres"
	Engine string // capability/engine, e.g. "postgres" (template `provides` or name)
	Major  string // major version from params.version, e.g. "16"
	Port   int    // in-network port
}

// SharedInstances resolves every declared shared service to its instance
// identity by reading its template (provides/defaultPort) and params (version).
func (m *Manager) SharedInstances() (map[string]SharedInstance, error) {
	out := map[string]SharedInstance{}
	for _, name := range sortedKeys(m.Model.Workspace.Shared) {
		ss := m.Model.Workspace.Shared[name]
		res, err := template.Resolve(m.Source, ss.Template, ss.Params)
		if err != nil {
			return nil, fmt.Errorf("shared %q: %w", name, err)
		}
		engine := res.Provides
		if engine == "" {
			engine = ss.Template
		}
		out[name] = SharedInstance{
			Name:   name,
			Alias:  generate.SharedAlias(name),
			Engine: engine,
			Major:  majorOf(ss.Params),
			Port:   res.DefaultPort,
		}
	}
	return out, nil
}

// consumer is one (project, service) that uses a shared instance.
type consumer struct{ project, service string }

// consumersOf returns the (project, service) pairs that declare `uses` of the
// given shared instance, across the whole workspace graph.
func (m *Manager) consumersOf(sharedName string) []consumer {
	var out []consumer
	for _, pname := range sortedKeys(m.Model.Projects) {
		p := m.Model.Projects[pname]
		for _, sname := range sortedKeys(p.Services) {
			for _, u := range p.Services[sname].Uses {
				if ref, ok := config.ParseRef(u); ok && ref.Kind == config.RefShared && ref.Name == sharedName {
					out = append(out, consumer{pname, sname})
				}
			}
		}
	}
	return out
}

// instancesUsedBy returns the shared instances a single project consumes.
func (m *Manager) instancesUsedBy(project string, instances map[string]SharedInstance) []SharedInstance {
	seen := map[string]bool{}
	var out []SharedInstance
	p := m.Model.Projects[project]
	for _, sname := range sortedKeys(p.Services) {
		for _, u := range p.Services[sname].Uses {
			ref, ok := config.ParseRef(u)
			if !ok || ref.Kind != config.RefShared {
				continue
			}
			if inst, ok := instances[ref.Name]; ok && !seen[inst.Alias] {
				seen[inst.Alias] = true
				out = append(out, inst)
			}
		}
	}
	return out
}

// RegisterUp records ref rows for every shared instance `project` consumes and
// upserts the corresponding shared_service rows. Idempotent. Acquires the lock.
func (m *Manager) RegisterUp(ctx context.Context, project string) error {
	instances, err := m.SharedInstances()
	if err != nil {
		return err
	}
	return lock.WithLock(ctx, m.LockPath, func() error {
		for _, inst := range m.instancesUsedBy(project, instances) {
			if err := m.DB.UpsertSharedService(state.SharedService{
				Name: inst.Alias, Engine: inst.Engine, MajorVersion: inst.Major, Status: "unknown",
			}); err != nil {
				return err
			}
			// One ref row per consuming service of this project.
			for _, c := range m.consumersOf(inst.Name) {
				if c.project != project {
					continue
				}
				if err := m.DB.AddRef(c.project, c.service, inst.Alias); err != nil {
					return err
				}
			}
			m.DB.LogEvent("ref-add", inst.Alias, "up "+project)
		}
		return nil
	})
}

// RegisterDown removes all of a project's ref rows. Acquires the lock. Returns
// the shared instances that dropped to zero refs (candidates for autostop/gc).
func (m *Manager) RegisterDown(ctx context.Context, project string) ([]string, error) {
	var zeroed []string
	err := lock.WithLock(ctx, m.LockPath, func() error {
		if _, err := m.DB.RemoveProjectRefs(project); err != nil {
			return err
		}
		m.DB.LogEvent("ref-del", project, "down "+project)
		shared, err := m.DB.ListSharedServices()
		if err != nil {
			return err
		}
		for _, s := range shared {
			n, err := m.DB.RefCount(s.Name)
			if err != nil {
				return err
			}
			if n == 0 {
				zeroed = append(zeroed, s.Name)
			}
		}
		sort.Strings(zeroed)
		return nil
	})
	return zeroed, err
}

// Reconcile is the self-healing pass run on every command: it derives the set of
// live projects from tool-labelled containers and prunes ref rows for projects
// that are no longer up (the count is derived from reality, not a trusted
// counter). Acquires the lock. Returns the pruned rows.
func (m *Manager) Reconcile(ctx context.Context) ([]state.Ref, error) {
	containers, err := m.Docker.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		return nil, fmt.Errorf("reconcile: list managed containers: %w", err)
	}
	live := map[string]bool{}
	for _, c := range containers {
		if p := c.Labels[generate.LabelProject]; p != "" && c.Running() {
			live[p] = true
		}
	}
	var pruned []state.Ref
	err = lock.WithLock(ctx, m.LockPath, func() error {
		var e error
		pruned, e = m.DB.PruneRefsForProjectsNotIn(live)
		return e
	})
	for _, r := range pruned {
		m.DB.LogEvent("ref-prune", r.SharedService, "reconcile: "+r.Project+" not live")
	}
	return pruned, err
}

// SharedStatus is the projection behind `shared status`.
type SharedStatus struct {
	Alias    string   `json:"alias"`
	Engine   string   `json:"engine"`
	Major    string   `json:"majorVersion"`
	Status   string   `json:"status"`
	RefCount int      `json:"refCount"`
	Projects []string `json:"projects"`
}

// Status returns the shared services with their live ref counts and consuming
// projects. Read-only (lock-free snapshot).
func (m *Manager) Status() ([]SharedStatus, error) {
	shared, err := m.DB.ListSharedServices()
	if err != nil {
		return nil, err
	}
	out := make([]SharedStatus, 0, len(shared))
	for _, s := range shared {
		n, err := m.DB.RefCount(s.Name)
		if err != nil {
			return nil, err
		}
		projs, err := m.DB.ProjectsUsing(s.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, SharedStatus{
			Alias: s.Name, Engine: s.Engine, Major: s.MajorVersion,
			Status: s.Status, RefCount: n, Projects: projs,
		})
	}
	return out, nil
}

// --- helpers ---------------------------------------------------------------

// majorOf extracts the major version from a shared service's params, defaulting
// to "default" when no version is pinned (keeps the (engine,major) key total).
func majorOf(params map[string]any) string {
	if params == nil {
		return "default"
	}
	if v, ok := params["version"]; ok {
		return fmt.Sprintf("%v", v)
	}
	return "default"
}

func sortedKeys[V any](mp map[string]V) []string {
	out := make([]string, 0, len(mp))
	for k := range mp {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
