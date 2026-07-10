package orchestrate

import (
	"context"
	"fmt"
	"sort"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/provision"
)

// This file is the provision saga phase (M2 capstone, DECISIONS D8): per-project
// Postgres role+database isolation on the shared engine. Because provisioning runs
// pgx FROM THE HOST, the shared Postgres needs a reachable port — published as a
// 127.0.0.1-only mapping via an UP-TIME compose overlay (so the deterministic,
// golden-asserted `generate` output is untouched and the "no host ports by
// default" posture holds for every other service). The per-project password is the
// project name: a predictable dev credential for a loopback-only, network-isolated
// dev database (THREAT-MODEL: container isolation is a non-goal), so nothing secret
// is generated or stored, and an app opts in via the documented DSN
// `postgres://<project>:<project>@shared-postgres:5432/<project>`.

const pgTemplate = "postgres" // shared engine template that this phase provisions

// PgConnector opens an admin connection to a Postgres DSN. Injectable so the
// provision phase is unit-testable without a live server (the default wraps
// provision.Connect / pgx).
type PgConnector func(ctx context.Context, dsn string) (provision.Conn, func() error, error)

func defaultPgConnect(ctx context.Context, dsn string) (provision.Conn, func() error, error) {
	c, closeFn, err := provision.Connect(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return c, closeFn, nil
}

// provTarget is one (project, shared-Postgres-instance) pair to provision.
type provTarget struct {
	project  string
	instance string
}

// pgInstances returns the set of shared services that are Postgres engines.
func pgInstances(m *config.Model) map[string]bool {
	out := map[string]bool{}
	for name, s := range m.Workspace.Shared {
		if s.Template == pgTemplate {
			out[name] = true
		}
	}
	return out
}

// provTargets returns the (project, instance) pairs to provision: every active
// project that `uses` a shared Postgres instance. Sorted and de-duplicated.
func provTargets(m *config.Model, activeServices map[string][]string, pg map[string]bool) []provTarget {
	seen := map[string]bool{}
	var out []provTarget
	for _, project := range sortedStringSlice(keysOf(activeServices)) {
		p, ok := m.Projects[project]
		if !ok {
			continue
		}
		for _, sname := range activeServices[project] {
			for _, u := range p.Services[sname].Uses {
				ref, ok := config.ParseRef(u)
				if !ok || ref.Kind != config.RefShared || !pg[ref.Name] {
					continue
				}
				key := project + "\x00" + ref.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, provTarget{project: project, instance: ref.Name})
			}
		}
	}
	return out
}

// provInstanceList returns the sorted distinct instances across targets.
func provInstanceList(targets []provTarget) []string {
	set := map[string]bool{}
	for _, t := range targets {
		set[t.instance] = true
	}
	return sortedStringSlice(keysOf(set))
}

// provisionPhase creates each project's role+database on its shared Postgres,
// idempotently, holding the flock for the SQL mutations (DECISIONS D7/D8). It
// re-derives the host port from the ledger (the same one sharedPhase published),
// so it is safe across re-runs and crashes. Compensation is intentionally empty:
// provisioned roles/dbs are data and survive a failed `up`.
func provisionPhase(d UpDeps, targets []provTarget) Phase {
	return Phase{
		Name:     "provision",
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			keys := make([]string, 0, len(targets))
			for _, t := range targets {
				keys = append(keys, t.project+"@"+t.instance)
			}
			return Fingerprint(append([]string{"provision"}, keys...)...), nil
		},
		Run: func(ctx context.Context) (any, error) {
			connect := d.PgConnect
			if connect == nil {
				connect = defaultPgConnect
			}
			// Retry a transient connect: the host-port overlay may have just
			// recreated the engine, so the proxy RSTs the handshake until Postgres
			// relistens (the "connection reset by peer" race).
			connect = retryingPgConnect(connect)
			byInst := map[string][]string{}
			for _, t := range targets {
				byInst[t.instance] = append(byInst[t.instance], t.project)
			}

			// Publish each shared Postgres on its stable STANDARD 127.0.0.1 port and
			// resolve the port host-side pgx dials. This is the SAME unified overlay as
			// `expose`/auto-expose (idempotent — no container recreate), and it also
			// guarantees the port exists even under --no-expose, since host-side
			// provisioning fundamentally needs one host port to reach.
			ports, err := ensureExposed(ctx, d, sortedStringSlice(keysOf(byInst)))
			if err != nil {
				return nil, fmt.Errorf("publish shared postgres host port: %w", err)
			}

			provisioned := []map[string]any{}
			// Hold the flock for the role/db mutations (provision pkg contract).
			err = lock.WithLock(ctx, d.LockPath, func() error {
				for _, inst := range sortedStringSlice(keysOf(byInst)) {
					params := d.Model.Workspace.Shared[inst].Params
					user := paramString(params, "rootUser", "devstack")
					pass := paramString(params, "rootPassword", "devstack")
					dsn := provision.DSN("127.0.0.1", ports[inst], user, pass, user)
					conn, closeConn, err := connect(ctx, dsn)
					if err != nil {
						return fmt.Errorf("connect to shared %s on 127.0.0.1:%d: %w", inst, ports[inst], err)
					}
					for _, project := range byInst[inst] {
						creds, err := provision.Postgres{}.EnsureProject(ctx, conn, project, project)
						if err != nil {
							_ = closeConn()
							return fmt.Errorf("provision %s on %s: %w", project, inst, err)
						}
						if err := d.DB.RecordProvisioned(project, "role", creds.Role); err != nil {
							_ = closeConn()
							return err
						}
						if err := d.DB.RecordProvisioned(project, "database", creds.Database); err != nil {
							_ = closeConn()
							return err
						}
						d.DB.LogEvent("provision", project, "role+db on "+generate.SharedAlias(inst))
						provisioned = append(provisioned, map[string]any{
							"project": project, "instance": inst, "role": creds.Role, "database": creds.Database,
						})
					}
					if err := closeConn(); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"provisioned": provisioned}, nil
		},
	}
}

// paramString reads a string param with a default.
func paramString(params map[string]any, key, def string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedStringSlice(s []string) []string {
	sort.Strings(s)
	return s
}
