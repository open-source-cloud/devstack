package orchestrate

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/state"
)

// This file is the imperative side of spec 27: the `resource create|rm|gc`
// commands' engine-agnostic mechanics, mirroring the resources saga phase
// (lock → overlay → provisioner → ledger → event) so there is ONE code path. The
// CLI (internal/cli) is a thin wrapper over these; they are tested here with the
// same mock docker client + injected Postgres connector the saga tests use.

// ResourceRegistry exposes the engine→Provisioner registry (Postgres live; other
// engines land in Full scope), wired with the injected connector.
func ResourceRegistry(connect PgConnector) *resource.Registry {
	return resource.NewRegistry(resource.Postgres{Connect: toResourceConnector(connect)})
}

// ResolveInstance returns the shared instance name serving engine (first shared
// service whose template == engine, matching the pgInstances convention).
func ResolveInstance(m *config.Model, engine string) (string, bool) {
	for _, name := range sortedStringSlice(m.SharedNames()) {
		if m.Workspace.Shared[name].Template == engine {
			return name, true
		}
	}
	return "", false
}

// engineDefaultAdmin is the per-engine fallback root credential when the shared
// instance's params don't set rootUser/rootPassword (matching the engine
// templates: postgres → devstack, minio → devstackadmin).
func engineDefaultAdmin(engine string) string {
	switch engine {
	case "minio":
		return "devstackadmin"
	case "localstack":
		// LocalStack accepts any credentials; the community convention is "test".
		return "test"
	default:
		return "devstack"
	}
}

// engineTarget resolves the host-reachable admin endpoint for an instance: it
// allocates/looks up the ledger port, writes+applies the per-engine 127.0.0.1
// overlay via `compose up -d <inst>` (idempotent, no recreate), and returns the
// Target with the instance's admin creds. Postgres + MinIO overlays are wired.
func engineTarget(ctx context.Context, d UpDeps, engine, instance string) (resource.Target, error) {
	ov, ok := engineOverlays[engine]
	if !ok {
		return resource.Target{}, fmt.Errorf("engine %q has no host-reachability overlay (postgres/minio in this milestone)", engine)
	}
	port, err := d.Manager.FreeHostPort(ctx, generate.SharedAlias(instance), ov.purpose, ov.portBase)
	if err != nil {
		return resource.Target{}, fmt.Errorf("allocate host port for %s: %w", instance, err)
	}
	overlay, err := writeProvisionOverlay(d.Model.Root, map[string]int{instance: port}, ov.containerPort)
	if err != nil {
		return resource.Target{}, err
	}
	outDir := filepath.Join(d.Model.Root, generate.GenDir, "shared")
	runner := d.Runner
	if runner == nil {
		runner = docker.ExecRunner{}
	}
	cp := docker.Compose{
		Project: generate.SharedStackName,
		File:    filepath.Join(outDir, generate.ComposeFile),
		Dir:     outDir, Runner: runner, Overrides: []string{overlay},
	}
	if err := cp.Up(ctx, instance); err != nil {
		return resource.Target{}, fmt.Errorf("apply host overlay for %s: %w", instance, err)
	}
	params := d.Model.Workspace.Shared[instance].Params
	def := engineDefaultAdmin(engine)
	return resource.Target{
		Instance: instance, Host: "127.0.0.1", Port: port,
		AdminEnv: map[string]string{
			"user":     paramString(params, "rootUser", def),
			"password": paramString(params, "rootPassword", def),
		},
	}, nil
}

// CreateResource provisions one resource imperatively (idempotent) and records
// it. Returns the connection attributes (secrets included so the caller can mask).
func CreateResource(ctx context.Context, d UpDeps, r resource.Resource) (resource.Attrs, error) {
	instance, ok := ResolveInstance(d.Model, r.Engine)
	if !ok {
		return nil, fmt.Errorf("no shared %q instance in this workspace (declare one under workspace.shared and run `devstack up`)", r.Engine)
	}
	reg := buildRegistry(d)
	prov, ok := reg.For(r.Engine)
	if !ok {
		return nil, fmt.Errorf("no provisioner for engine %q (lands in spec 27 Full scope)", r.Engine)
	}
	if !resource.SupportsKind(r.Engine, r.Kind) {
		return nil, fmt.Errorf("engine %q does not support kind %q", r.Engine, r.Kind)
	}
	if r.CredKind == resource.CredGenerated {
		if _, set := r.Params["password"]; !set {
			pw, err := secrets.RandomPassword(24)
			if err != nil {
				return nil, err
			}
			if r.Params == nil {
				r.Params = map[string]any{}
			}
			r.Params["password"] = pw
		}
	}
	target, err := engineTarget(ctx, d, r.Engine, instance)
	if err != nil {
		return nil, err
	}
	var attrs resource.Attrs
	err = lock.WithLock(ctx, d.LockPath, func() error {
		a, err := prov.Ensure(ctx, target, r)
		if err != nil {
			return err
		}
		attrs = a
		if err := d.DB.RecordProvisioned(r.Owner, r.Kind, r.Name); err != nil {
			return err
		}
		if r.Engine == "postgres" && r.Kind == "database" {
			if role := a["role"]; role != "" {
				if err := d.DB.RecordProvisioned(r.Owner, "role", role); err != nil {
					return err
				}
			}
		}
		d.DB.LogEvent("provision", r.Owner, r.Kind+" on "+generate.SharedAlias(instance))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return attrs, nil
}

// DropResource removes a resource's ledger row, and — when purge is set — drops
// the underlying object first (destructive; the caller confirm-gates it). Without
// purge it only un-tracks the resource, leaving its bytes (the data survives).
func DropResource(ctx context.Context, d UpDeps, r resource.Resource, purge bool) error {
	if purge {
		instance, ok := ResolveInstance(d.Model, r.Engine)
		if !ok {
			return fmt.Errorf("no shared %q instance to drop %s/%s from", r.Engine, r.Kind, r.Name)
		}
		reg := buildRegistry(d)
		prov, ok := reg.For(r.Engine)
		if !ok {
			return fmt.Errorf("no provisioner for engine %q (cannot --purge-data)", r.Engine)
		}
		target, err := engineTarget(ctx, d, r.Engine, instance)
		if err != nil {
			return err
		}
		if err := lock.WithLock(ctx, d.LockPath, func() error {
			if err := prov.Drop(ctx, target, r); err != nil {
				return err
			}
			return removeResourceRows(d.DB, r)
		}); err != nil {
			return err
		}
		d.DB.LogEvent("gc.drop", r.Owner, r.Kind+" "+r.Name+" purged from "+generate.SharedAlias(instance))
		return nil
	}
	// Un-track only (bytes preserved).
	return lock.WithLock(ctx, d.LockPath, func() error { return removeResourceRows(d.DB, r) })
}

// removeResourceRows drops the resource's own ledger row plus, for a postgres
// database, the paired role row (the two EnsureProject records).
func removeResourceRows(db *state.DB, r resource.Resource) error {
	if err := db.RemoveProvisioned(r.Owner, r.Kind, r.Name); err != nil {
		return err
	}
	if r.Engine == "postgres" && r.Kind == "database" {
		role := r.Name
		if role == "" {
			role = r.Owner
		}
		if err := db.RemoveProvisioned(r.Owner, "role", role); err != nil {
			return err
		}
	}
	return nil
}

// GCResult reports the outcome of a resource gc pass.
type GCResult struct {
	Reaped  []map[string]string `json:"reaped"`  // rows dropped + un-tracked
	Skipped []map[string]string `json:"skipped"` // rows whose engine has no live provisioner/instance
}

// GCResources reclaims orphaned resources (owner project no longer active). It
// resolves each row's engine from the provisioner catalog, drops it via the
// engine Provisioner (destructive — the caller confirm-gates), and un-tracks it.
// Rows whose engine has no live provisioner/instance are reported as skipped,
// never silently dropped from the ledger. Never recreates or bounces a container.
func GCResources(ctx context.Context, d UpDeps, active map[string]bool) (GCResult, error) {
	var res GCResult
	orphans, err := d.DB.OrphanedProvisioned(active)
	if err != nil {
		return res, err
	}
	reg := buildRegistry(d)
	for _, o := range orphans {
		engine, instance, prov, ok := engineForRow(d, reg, o.Kind)
		if !ok {
			res.Skipped = append(res.Skipped, map[string]string{
				"project": o.Project, "kind": o.Kind, "name": o.Name,
				"reason": "no live provisioner/instance for this kind's engine",
			})
			continue
		}
		r := resource.Resource{Engine: engine, Kind: o.Kind, Name: o.Name, Owner: o.Project}
		target, err := engineTarget(ctx, d, engine, instance)
		if err != nil {
			return res, err
		}
		if err := lock.WithLock(ctx, d.LockPath, func() error {
			if err := prov.Drop(ctx, target, r); err != nil {
				return err
			}
			return d.DB.RemoveProvisioned(o.Project, o.Kind, o.Name)
		}); err != nil {
			return res, err
		}
		d.DB.LogEvent("gc.drop", o.Project, o.Kind+" "+o.Name+" reaped from "+generate.SharedAlias(instance))
		res.Reaped = append(res.Reaped, map[string]string{
			"project": o.Project, "engine": engine, "kind": o.Kind, "name": o.Name,
		})
	}
	return res, nil
}

// engineForRow picks the engine whose registered provisioner lists kind AND has a
// live instance in the workspace. Postgres is the only live engine this milestone.
func engineForRow(d UpDeps, reg *resource.Registry, kind string) (engine, instance string, prov resource.Provisioner, ok bool) {
	for _, e := range sortedStringSlice(reg.Engines()) {
		p, has := reg.For(e)
		if !has {
			continue
		}
		for _, k := range p.Kinds() {
			if k != kind {
				continue
			}
			if inst, found := ResolveInstance(d.Model, e); found {
				return e, inst, p, true
			}
		}
	}
	return "", "", nil, false
}
