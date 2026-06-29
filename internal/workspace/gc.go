package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/state"
)

// This file implements `shared gc` (reclaim zero-ref shared services) and
// `doctor --rebuild-state` (reconstruct the ledger from on-disk config + live
// container labels). The ledger is a cache of reality, never the trusted source
// (spec 09 §crash-recovery, spec 13).

// RebuildSummary reports what RebuildState reconstructed.
type RebuildSummary struct {
	Shared []string `json:"shared"` // shared_service rows re-derived (aliases)
	Refs   int      `json:"refs"`   // service_ref rows re-derived
}

// RebuildState reconstructs the shared_service + service_ref ledger rows from the
// on-disk workspace config intersected with live tool-labelled containers — the
// recovery path when state.db is lost or corrupt. Acquires the lock.
func (m *Manager) RebuildState(ctx context.Context) (RebuildSummary, error) {
	cs, err := m.Docker.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		return RebuildSummary{}, fmt.Errorf("rebuild-state: list containers: %w", err)
	}
	liveProjects := map[string]bool{}
	liveShared := map[string]bool{}
	for _, c := range cs {
		if !c.Running() {
			continue
		}
		if p := c.Labels[generate.LabelProject]; p != "" {
			liveProjects[p] = true
		}
		if s := c.Labels[generate.LabelShared]; s != "" {
			liveShared[s] = true
		}
	}

	instances, err := m.SharedInstances()
	if err != nil {
		return RebuildSummary{}, err
	}

	var sum RebuildSummary
	err = lock.WithLock(ctx, m.LockPath, func() error {
		for _, name := range sortedKeys(instances) {
			if !liveShared[name] {
				continue
			}
			inst := instances[name]
			if err := m.DB.UpsertSharedService(state.SharedService{
				Name: inst.Alias, Engine: inst.Engine, MajorVersion: inst.Major, Status: "running",
			}); err != nil {
				return err
			}
			sum.Shared = append(sum.Shared, inst.Alias)
		}
		for _, project := range sortedKeys(liveProjects) {
			for _, inst := range m.instancesUsedBy(project, instances) {
				if !liveShared[inst.Name] {
					continue
				}
				for _, c := range m.consumersOf(inst.Name) {
					if c.project != project {
						continue
					}
					if err := m.DB.AddRef(c.project, c.service, inst.Alias); err != nil {
						return err
					}
					sum.Refs++
				}
			}
		}
		return nil
	})
	if err == nil {
		m.DB.LogEvent("rebuild-state", m.Model.Workspace.Name,
			fmt.Sprintf("re-derived %d shared + %d refs from live labels", len(sum.Shared), sum.Refs))
	}
	return sum, err
}

// GCResult reports the zero-ref shared services and which were stopped.
type GCResult struct {
	Candidates []string `json:"candidates"` // zero-ref shared aliases
	Stopped    []string `json:"stopped"`    // actually stopped (when stop=true)
}

// GC reconciles the ledger, then finds shared services at zero refs. With
// stop=false it only reports candidates (the safe default — warm DBs are cheap).
// With stop=true it `compose stop`s each candidate on the shared stack and marks
// it stopped. The external network and volumes are never touched (spec 03/09).
func (m *Manager) GC(ctx context.Context, stop bool) (GCResult, error) {
	if _, err := m.Reconcile(ctx); err != nil {
		// Reconcile is best-effort; a down daemon shouldn't block a dry-run report.
		if stop {
			return GCResult{}, err
		}
	}
	shared, err := m.DB.ListSharedServices()
	if err != nil {
		return GCResult{}, err
	}
	instances, err := m.SharedInstances()
	if err != nil {
		return GCResult{}, err
	}
	aliasToName := map[string]string{}
	for name, inst := range instances {
		aliasToName[inst.Alias] = name
	}

	var res GCResult
	var zero []state.SharedService
	for _, s := range shared {
		n, err := m.DB.RefCount(s.Name)
		if err != nil {
			return GCResult{}, err
		}
		if n == 0 {
			res.Candidates = append(res.Candidates, s.Name)
			zero = append(zero, s)
		}
	}
	if !stop || len(zero) == 0 {
		return res, nil
	}

	cp := m.sharedCompose()
	for _, s := range zero {
		name := aliasToName[s.Name]
		if name == "" {
			continue // ledger alias has no current config service; skip (orphan)
		}
		if err := cp.Stop(ctx, name); err != nil {
			return res, fmt.Errorf("stop shared %s: %w", s.Name, err)
		}
		if err := lock.WithLock(ctx, m.LockPath, func() error {
			return m.DB.SetSharedStatus(s.Engine, s.MajorVersion, "stopped")
		}); err != nil {
			return res, err
		}
		m.DB.LogEvent("shared-gc", s.Name, "stopped (0 refs)")
		res.Stopped = append(res.Stopped, s.Name)
	}
	return res, nil
}

// sharedCompose builds the compose driver for the shared stack. The compose file
// is used when present; otherwise the label-driven `-p devstack-shared` form
// drives stop/down (so gc works even if generated artifacts were cleaned).
func (m *Manager) sharedCompose() docker.Compose {
	outDir := filepath.Join(m.Model.Root, generate.GenDir, "shared")
	file := filepath.Join(outDir, generate.ComposeFile)
	if _, err := os.Stat(file); err != nil {
		file = ""
	}
	runner := m.Runner
	if runner == nil {
		runner = docker.ExecRunner{}
	}
	return docker.Compose{Project: generate.SharedStackName, File: file, Dir: outDir, Runner: runner}
}
