package orchestrate

import (
	"context"
	"fmt"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/profile"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/secrets"
)

// This file is the spec-27 `resources` saga phase: the declarative complement to
// the implicit Postgres `provision` phase. It provisions every resource a project
// declares in its `devstack.yaml` `resources:` block (databases, buckets,
// lifecycle policies, …) through the matching engine Provisioner — idempotent,
// existence-guarded, under the flock — records each in the `provisioned`
// ownership ledger + the event log, and REPORTS drift (a ledger resource no
// longer declared) WITHOUT auto-dropping it (Q-RESOURCE-DRIFT: teardown is always
// explicit + confirmed).
//
// It runs alongside the `provision` phase (which still handles the implicit
// postgres role+db from `uses`, byte-identically). A declared postgres database
// that overlaps the implicit one is a harmless no-op (existence-guarded + the
// ledger INSERT OR IGNORE). Non-postgres engines without a live provisioner in
// this milestone are skipped with a note (their provisioners land in Full scope).

// perEngineOverlay is the per-engine host-reachability registry (spec 27
// §"host-port overlay"): each engine publishes 127.0.0.1:<ledgerPort>:<container>
// under its own (purpose, portBase). Postgres reuses the provision phase's values
// so one overlay/port covers both phases.
type perEngineOverlay struct {
	purpose       string
	portBase      int
	containerPort int
}

var engineOverlays = map[string]perEngineOverlay{
	"postgres": {provisionPurpose, provisionPortBase, 5432},
	"redis":    {"redis-provision", 46379, 6379},
	"minio":    {"minio-provision", 49000, 9000},
	"nats":     {"nats-provision", 44222, 4222},
	// Kafka (Redpanda) advertises its EXTERNAL listener at a fixed 127.0.0.1:49092
	// (template), so host clients must reach the broker on exactly that port — the
	// overlay publishes the in-container external listener (19092) there. The port
	// base is 49092 to match the advertised address (a mismatch breaks the Kafka
	// bootstrap→redirect handshake, the #1 local-Kafka footgun).
	"kafka": {"kafka-provision", 49092, 19092},
	// LocalStack's edge port (4566) serves every AWS service (SQS/SNS/S3/…); engine
	// key "aws" matches the template's `provides: aws`.
	"aws": {"localstack-provision", 44566, 4566},
}

// declaredKind reports whether a ledger kind is one the declarative resources
// phase manages (so drift detection ignores the implicitly-provisioned
// role/database/redis_index kinds and never false-flags them).
func declaredKind(kind string) bool {
	switch kind {
	case "role", "database", "redis_index":
		return false
	default:
		return true
	}
}

// resDecl is one resolved declarative resource to provision.
type resDecl struct {
	project  string
	instance string
	engine   string
	kind     string
	name     string
	cred     resource.CredentialPolicy
	params   map[string]any
}

// collectResourceDecls gathers every active project's `resources:` entries whose
// target shared instance is in the up set. Active projects are those with active
// services (spec 12 slicing); the instance must be one being brought up.
func collectResourceDecls(m *config.Model, active profile.Active) []resDecl {
	upInstances := map[string]bool{}
	for _, n := range active.Shared {
		upInstances[n] = true
	}
	var out []resDecl
	for _, project := range sortedStringSlice(keysOf(active.Services)) {
		if len(active.Services[project]) == 0 {
			continue
		}
		p, ok := m.Projects[project]
		if !ok {
			continue
		}
		for _, d := range p.Resources {
			ref, ok := config.ParseRef(d.Uses)
			if !ok || ref.Kind != config.RefShared {
				continue
			}
			if !upInstances[ref.Name] {
				continue
			}
			engine := d.Engine
			if engine == "" {
				engine = m.Workspace.Shared[ref.Name].Template
			}
			name := d.Name
			if name == "" {
				name = project
			}
			cred := resource.CredentialPolicy(d.Credentials)
			if cred == "" {
				cred = resource.CredPredictable
			}
			out = append(out, resDecl{
				project: project, instance: ref.Name, engine: engine,
				kind: d.Kind, name: name, cred: cred, params: d.Params,
			})
		}
	}
	return out
}

// toResourceConnector adapts an orchestrate PgConnector to the resource one.
func toResourceConnector(connect PgConnector) resource.PgConnector {
	if connect == nil {
		return nil
	}
	return resource.PgConnector(connect)
}

// buildRegistry builds the engine→Provisioner registry the phase/commands use,
// wired with the injected Postgres connector + S3 factory so provisioning is
// daemon/endpoint-free in tests. Postgres + MinIO are live in this milestone.
func buildRegistry(d UpDeps) *resource.Registry {
	return resource.NewRegistry(
		resource.Postgres{Connect: toResourceConnector(d.PgConnect)},
		resource.MinIO{Factory: d.S3Factory},
		resource.NATS{Factory: d.NatsFactory},
		resource.Kafka{Factory: d.KafkaFactory},
		resource.LocalStack{SQSFactory: d.SQSFactory, SNSFactory: d.SNSFactory},
		resource.Redis{},
	)
}

// resourcesPhase provisions declared resources idempotently and reports drift.
// Compensation is intentionally empty: provisioned resources are data and survive
// a failed `up`, exactly like the Postgres provision phase.
func resourcesPhase(d UpDeps, decls []resDecl) Phase {
	return Phase{
		Name:     "resources",
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			keys := make([]string, 0, len(decls))
			for _, r := range decls {
				keys = append(keys, r.project+"@"+r.instance+":"+r.engine+"/"+r.kind+"/"+r.name+"/"+string(r.cred))
			}
			return Fingerprint(append([]string{"resources"}, keys...)...), nil
		},
		Run: func(ctx context.Context) (any, error) {
			reg := buildRegistry(d)

			// Resolve each instance's published host port (idempotent — returns the
			// port the shared/provision phase already allocated).
			ports := map[string]int{}
			for _, r := range decls {
				if _, done := ports[r.instance]; done {
					continue
				}
				ov, ok := engineOverlays[r.engine]
				if !ok {
					continue // no host-reachability overlay for this engine yet
				}
				p, err := d.Manager.FreeHostPort(ctx, generate.SharedAlias(r.instance), ov.purpose, ov.portBase)
				if err != nil {
					return nil, fmt.Errorf("resolve resource port for %s: %w", r.instance, err)
				}
				ports[r.instance] = p
			}

			provisioned := []map[string]any{}
			skipped := []map[string]any{}
			err := lock.WithLock(ctx, d.LockPath, func() error {
				for _, r := range decls {
					prov, ok := reg.For(r.engine)
					if !ok {
						skipped = append(skipped, map[string]any{
							"project": r.project, "engine": r.engine, "kind": r.kind, "name": r.name,
							"reason": "no provisioner for engine (lands in spec 27 Full scope)",
						})
						continue
					}
					port, ok := ports[r.instance]
					if !ok {
						skipped = append(skipped, map[string]any{
							"project": r.project, "engine": r.engine, "kind": r.kind, "name": r.name,
							"reason": "no host-reachability overlay for engine",
						})
						continue
					}
					res := resource.Resource{
						Engine: r.engine, Kind: r.kind, Name: r.name, Owner: r.project,
						Params: r.params, CredKind: r.cred,
					}
					// A `generated` credential gets a random value here so a consumer
					// never sees a predictable password; Pusher delivery to a provider
					// lands in Full scope, so the value is currently held only for the
					// provisioner call (never written to a generated file).
					if r.cred == resource.CredGenerated {
						pw, err := secrets.RandomPassword(24)
						if err != nil {
							return err
						}
						if res.Params == nil {
							res.Params = map[string]any{}
						}
						if _, set := res.Params["password"]; !set {
							res.Params["password"] = pw
						}
					}
					params := d.Model.Workspace.Shared[r.instance].Params
					target := resource.Target{
						Instance: r.instance, Host: "127.0.0.1", Port: port,
						AdminEnv: map[string]string{
							"user":     paramString(params, "rootUser", "devstack"),
							"password": paramString(params, "rootPassword", "devstack"),
						},
					}
					attrs, err := prov.Ensure(ctx, target, res)
					if err != nil {
						return fmt.Errorf("provision %s %s/%s on %s: %w", r.engine, r.kind, r.name, r.instance, err)
					}
					// Record the resource's own row, plus the postgres role that
					// EnsureProject creates (preserving the implicit phase's ledger shape).
					if err := d.DB.RecordProvisioned(r.project, r.kind, r.name); err != nil {
						return err
					}
					if r.engine == "postgres" && r.kind == "database" {
						if role := attrs["role"]; role != "" {
							if err := d.DB.RecordProvisioned(r.project, "role", role); err != nil {
								return err
							}
						}
					}
					d.DB.LogEvent("provision", r.project, r.kind+" on "+generate.SharedAlias(r.instance))
					provisioned = append(provisioned, map[string]any{
						"project": r.project, "engine": r.engine, "kind": r.kind, "name": r.name,
					})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}

			drift := detectResourceDrift(d, decls)
			return map[string]any{
				"provisioned": provisioned,
				"skipped":     skipped,
				"drift":       drift,
			}, nil
		},
	}
}

// detectResourceDrift reports ledger resources for active projects whose
// declarative kind is no longer declared (Q-RESOURCE-DRIFT). It is REPORT-ONLY:
// nothing is dropped. Implicit kinds (role/database/redis_index) are excluded so
// the Postgres provision path is never misread as drift.
func detectResourceDrift(d UpDeps, decls []resDecl) []map[string]any {
	declared := map[string]bool{} // project\x00kind\x00name
	projects := map[string]bool{}
	for _, r := range decls {
		declared[r.project+"\x00"+r.kind+"\x00"+r.name] = true
		projects[r.project] = true
	}
	var drift []map[string]any
	for _, project := range sortedStringSlice(keysOf(projects)) {
		rows, err := d.DB.ProvisionedFor(project)
		if err != nil {
			continue
		}
		for _, row := range rows {
			if !declaredKind(row.Kind) {
				continue
			}
			if declared[project+"\x00"+row.Kind+"\x00"+row.Name] {
				continue
			}
			drift = append(drift, map[string]any{
				"project": project, "kind": row.Kind, "name": row.Name,
				"note": "declared removed; not dropped — reclaim with `resource rm --purge-data` or `resource gc`",
			})
		}
	}
	return drift
}
