package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/health"
	"github.com/open-source-cloud/devstack/internal/hooks"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/profile"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/internal/trust"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// This file wires the concrete `up` phases over the engine (C5b). It assembles
// the phase list that BuildUp returns; the Saga (orchestrate.go) drives it.
//
// Wired here: preflight → network → generate → shared(health-gated) →
// compose-up(per project) → hooks(postUp, per project), with compensation for
// the shared ref rows and each project's compose-up.
//
// Deliberately deferred (flagged): clone (gitx), provision (needs the
// shared-Postgres host-port coupling — a flagged design item), secrets (M4/S6),
// trust (N5), and firstRun hooks (need the provisioned-volume scope_key). Those
// slot in as additional phases without changing the engine.

// UpDeps is everything the up-saga phases need. Daemon I/O flows through the
// injected docker.Client + docker.Runner so the wiring is unit-testable with a
// mock client + a fake runner.
type UpDeps struct {
	Model    *config.Model
	DB       *state.DB
	Docker   docker.Client
	Manager  *workspace.Manager
	Source   template.TemplateSource
	LockPath string

	Runner   docker.Runner     // compose CLI runner (nil → docker.ExecRunner)
	Env      map[string]string // generate env (nil → process env, via generate default)
	Profile  string            // env-overlay profile for ${profile} (generate); "" → workspace default
	Profiles []string          // spec-12 SERVICE SLICES (--profile, repeatable); empty → defaultProfile/all
	Projects []string          // explicit subset; empty → every project in the workspace
	// Secrets resolves secret:// refs; nil → built from workspace.secrets.providers
	// with the built-in factories (SOPS+age). Injected for tests.
	Secrets *secrets.Registry
	// CredPusher, when non-nil, receives a resource's `generated` credential so the
	// random value is written to a secrets backend (aws-sm/ssm/infisical) instead of
	// only living in-process for the provisioner call. The plaintext never lands in a
	// generated file (spec 04 valueless-env coupling); nil → no push (the value is
	// still generated and used to provision, but not persisted anywhere).
	CredPusher secrets.Pusher
	// Trust installs the local CA when network.proxy.httpsLocal; nil → trust.New().
	// Injected for tests (the trust phase is fenced — failure never aborts up).
	Trust *trust.Trust
	// PgConnect opens an admin Postgres connection for the provision phase; nil →
	// the pgx-backed default. Injected for tests (so provisioning runs daemon-free).
	PgConnect PgConnector
	// S3Factory builds the admin S3 client for the minio/localstack provisioners;
	// nil → the real pure-Go aws-sdk-go-v2 path-style client. Injected for tests so
	// bucket ops run without a live endpoint.
	S3Factory resource.S3Factory
	// NatsFactory / KafkaFactory / SQSFactory / SNSFactory build the admin clients
	// for the messaging provisioners (spec 29); nil → the real pure-Go clients
	// (nats.go / franz-go / aws-sdk-go-v2). Injected for tests so queue/topic/stream
	// ops run without a live broker/endpoint.
	NatsFactory  resource.NatsFactory
	KafkaFactory resource.KafkaFactory
	SQSFactory   resource.SQSFactory
	SNSFactory   resource.SNSFactory

	Build         bool          // compose up --build (honors the generate ledger's selective-rebuild hash)
	Rebuild       bool          // force `compose build --no-cache` before up (spec 26 --rebuild)
	SkipClone     bool          // skip the clone/sync phase for repos already on disk (spec 26 --skip-clone)
	NoHooks       bool          // skip the hooks phase
	NoPreflight   bool          // skip the preflight phase (fast inner loops)
	NoProvision   bool          // skip the per-project Postgres provision phase
	HealthTimeout time.Duration // per-shared-service gate cap (0 → health.Compile default)
}

// intersect returns the elements of a that are also in b, preserving a's order.
func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if set[x] {
			out = append(out, x)
		}
	}
	return out
}

// BuildUp assembles the ordered up-saga phases for the requested projects.
func BuildUp(d UpDeps) ([]Phase, error) {
	if d.Runner == nil {
		d.Runner = docker.ExecRunner{}
	}
	projects := d.Projects
	if len(projects) == 0 {
		projects = sortedProjects(d.Model)
	}
	for _, p := range projects {
		if _, ok := d.Model.Projects[p]; !ok {
			return nil, fmt.Errorf("project %q is not in this workspace", p)
		}
	}

	// Selective-up (spec 12): --profile slices the workspace to a set of active
	// services + the shared instances they transitively use. Empty d.Profiles →
	// defaultProfile, or the reserved `all` (every service). A project with no
	// active services drops out of the up entirely.
	active := profile.Resolve(d.Model, d.Profiles)
	activeProjects := projects[:0:0]
	for _, p := range projects {
		if len(active.Services[p]) > 0 {
			activeProjects = append(activeProjects, p)
		}
	}
	projects = activeProjects

	gen, err := generate.New(d.Model, d.Source, generate.WithEnv(d.Env), generate.WithProfile(d.Profile))
	if err != nil {
		return nil, err
	}

	// Resolved secret env per project, shared between the secrets phase (writes)
	// and each compose-up phase (reads). Values live only here + in the child
	// process env — never on disk.
	secretEnv := map[string][]string{}

	var phases []Phase
	if !d.NoPreflight {
		phases = append(phases, preflightPhase(d))
	}
	// Provision targets: active projects that `uses` a shared Postgres get a
	// per-project role+db (DECISIONS D8). The instances they need are published on
	// 127.0.0.1 by the shared phase so host-side pgx can reach them.
	var targets []provTarget
	var provInstances []string
	var resDecls []resDecl
	if !d.NoProvision {
		targets = provTargets(d.Model, active.Services, pgInstances(d.Model))
		provInstances = provInstanceList(targets)
		// Declarative spec-27 resources for active projects on up instances.
		resDecls = collectResourceDecls(d.Model, active)
	}

	phases = append(phases,
		networkPhase(d),
		generatePhase(d, gen),
		secretsPhase(d, projects, secretEnv),
		trustPhase(d),
		sharedPhase(d, projects, active.Shared, provInstances),
	)
	if len(targets) > 0 {
		phases = append(phases, provisionPhase(d, targets))
	}
	// Declarative resources (spec 27): provisioned after the implicit postgres
	// provision phase (which publishes the shared instances they reach), before
	// the per-project compose-up/hooks.
	if len(resDecls) > 0 {
		phases = append(phases, resourcesPhase(d, resDecls))
	}
	// Hook ordering (spec 11): workspace preUp → per-project (preUp → compose-up →
	// postUp) → workspace postUp.
	if !d.NoHooks {
		phases = append(phases, hookPhase(d, "", "preUp", d.Model.Workspace.Hooks.PreUp, hooks.OnAbort))
	}
	for _, p := range projects {
		if !d.NoHooks {
			phases = append(phases, hookPhase(d, p, "preUp", d.Model.Projects[p].Hooks.PreUp, hooks.OnAbort))
		}
		phases = append(phases, composeUpPhase(d, p, secretEnv, active.Services[p]))
		if !d.NoHooks {
			phases = append(phases, firstRunPhase(d, p, d.Model.Projects[p].Hooks.FirstRun))
			phases = append(phases, hookPhase(d, p, "postUp", d.Model.Projects[p].Hooks.PostUp, hooks.OnAbort))
		}
	}
	if !d.NoHooks {
		phases = append(phases, hookPhase(d, "", "postUp", d.Model.Workspace.Hooks.PostUp, hooks.OnAbort))
	}
	return phases, nil
}

// trustPhase installs the local CA when network.proxy.httpsLocal is set (spec 05
// §trust, spec 09 phase 7). It is FENCED: a missing mkcert / no sudo degrades to
// a warning and never aborts `up`. A no-op when httpsLocal is off.
func trustPhase(d UpDeps) Phase {
	return Phase{
		Name:      "trust",
		AlwaysRun: true,
		Run: func(ctx context.Context) (any, error) {
			if !d.Model.Workspace.Network.Proxy.HTTPSLocal {
				return map[string]any{"status": "skipped (httpsLocal off)"}, nil
			}
			t := d.Trust
			if t == nil {
				t = trust.New()
			}
			if err := t.Install(ctx); err != nil {
				// Fenced: never fail the saga on a trust problem.
				return map[string]any{"status": "warning", "error": err.Error()}, nil
			}
			return map[string]any{"status": "installed"}, nil
		},
	}
}

// secretsPhase resolves every secret:// ref the requested projects reference and
// stashes the resolved KEY=VALUE env per project (spec 04 §6). It ALWAYS runs
// (never cached — values stay in memory) and mutates nothing global, so it has no
// compensation. A nil Secrets registry with no secret refs is a no-op; refs with
// no registry is a clear error.
func secretsPhase(d UpDeps, projects []string, out map[string][]string) Phase {
	return Phase{
		Name:      "secrets",
		AlwaysRun: true,
		Run: func(ctx context.Context) (any, error) {
			total := 0
			for _, p := range projects {
				keyRefs := generate.SecretRefs(d.Model, p)
				if len(keyRefs) == 0 {
					continue
				}
				reg, err := d.secretRegistry()
				if err != nil {
					return nil, err
				}
				raws := make([]string, 0, len(keyRefs))
				for _, raw := range keyRefs {
					raws = append(raws, raw)
				}
				refs, err := secrets.Collect(raws...)
				if err != nil {
					return nil, err
				}
				resolved, err := secrets.Resolve(ctx, reg, refs)
				if err != nil {
					return nil, fmt.Errorf("resolve secrets for %s: %w", p, err)
				}
				env := make([]string, 0, len(keyRefs))
				for _, key := range sortedStringKeys(keyRefs) {
					env = append(env, key+"="+resolved[keyRefs[key]])
				}
				out[p] = env
				total += len(env)
			}
			return map[string]any{"resolved": total}, nil
		},
	}
}

// secretRegistry returns the injected registry, or builds one from the
// workspace's declared providers with the built-in factories (SOPS+age).
func (d UpDeps) secretRegistry() (*secrets.Registry, error) {
	if d.Secrets != nil {
		return d.Secrets, nil
	}
	reg := secrets.NewRegistry()
	secrets.RegisterBuiltins(reg)
	for _, pr := range d.Model.Workspace.Secrets.Providers {
		reg.Configure(secrets.ProviderConfig{
			Name: pr.Name, Kind: pr.Kind, Env: pr.Env,
			ProjectID: pr.ProjectID, Region: pr.Region,
		})
	}
	return reg, nil
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// preflight — daemon reachable (critical). The full doctor matrix is X6.
func preflightPhase(d UpDeps) Phase {
	return Phase{
		Name:      "preflight",
		AlwaysRun: true,
		Run: func(ctx context.Context) (any, error) {
			if err := d.Docker.Ping(ctx); err != nil {
				return nil, fmt.Errorf("docker daemon not reachable: %w", err)
			}
			return map[string]any{"context": d.Docker.ContextName()}, nil
		},
	}
}

// network — idempotent ensure of the pinned external bridge (must precede any
// compose up). Mutating but never auto-removed (shared by other workspaces).
func networkPhase(d UpDeps) Phase {
	return Phase{
		Name:     "network",
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			return Fingerprint(generate.SharedNetwork), nil
		},
		Run: func(ctx context.Context) (any, error) {
			err := lock.WithLock(ctx, d.LockPath, func() error {
				return d.Docker.EnsureNetwork(ctx, generate.SharedNetwork, map[string]string{
					generate.LabelManaged: "true", generate.LabelWorkspace: d.Model.Workspace.Name,
				})
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"network": generate.SharedNetwork}, nil
		},
	}
}

// generate — the deterministic pipeline, writeIfChanged. Runs before the compose
// phases (the compose files must exist to `up`); re-armed by a config edit.
func generatePhase(d UpDeps, gen *generate.Generator) Phase {
	return Phase{
		Name: "generate",
		Fingerprint: func(context.Context) (string, error) {
			return configFingerprint(d.Model)
		},
		Run: func(context.Context) (any, error) {
			stacks, err := gen.GenerateAll()
			if err != nil {
				return nil, err
			}
			var written []string
			for _, st := range stacks {
				res, err := st.Write()
				if err != nil {
					return nil, fmt.Errorf("write %s: %w", st.Name, err)
				}
				if res.ComposeChanged {
					written = append(written, st.Name)
				}
			}
			return map[string]any{"changed": written}, nil
		},
	}
}

// shared — register ref rows, bring up only the shared services the requested
// projects use, then health-gate them. Compensation drops the ref rows.
func sharedPhase(d UpDeps, projects, names, provInstances []string) Phase {
	// Only provision instances that are actually being brought up this run.
	prov := intersect(provInstances, names)
	return Phase{
		Name:     "shared",
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			// Fold the provisioned set in: newly publishing a port (a consumer was
			// added) must re-run shared-up rather than skip on a stale fingerprint.
			return Fingerprint(append(append([]string{"shared"}, names...), append([]string{"prov"}, prov...)...)...), nil
		},
		Run: func(ctx context.Context) (any, error) {
			if len(names) == 0 {
				return map[string]any{"services": []any{}}, nil
			}
			for _, p := range projects {
				if err := d.Manager.RegisterUp(ctx, p); err != nil {
					return nil, fmt.Errorf("register refs for %s: %w", p, err)
				}
			}
			outDir := filepath.Join(d.Model.Root, generate.GenDir, "shared")
			cp := docker.Compose{
				Project: generate.SharedStackName,
				File:    filepath.Join(outDir, generate.ComposeFile),
				Dir:     outDir, Runner: d.Runner,
			}
			// Publish each provisioned Postgres on 127.0.0.1:<ledger port> via an
			// up-time overlay so host-side pgx (the provision phase) can reach it,
			// without touching the deterministic generated compose.
			if len(prov) > 0 {
				ports := map[string]int{}
				for _, inst := range prov {
					port, err := d.Manager.FreeHostPort(ctx, generate.SharedAlias(inst), provisionPurpose, provisionPortBase)
					if err != nil {
						return nil, fmt.Errorf("allocate provision port for %s: %w", inst, err)
					}
					ports[inst] = port
				}
				overlay, err := writeProvisionOverlay(d.Model.Root, ports, 5432)
				if err != nil {
					return nil, err
				}
				cp.Overrides = []string{overlay}
			}
			if err := cp.Up(ctx, names...); err != nil {
				return nil, fmt.Errorf("compose up shared: %w", err)
			}
			gated, err := gateShared(ctx, d, names)
			if err != nil {
				return nil, err
			}
			return map[string]any{"services": gated}, nil
		},
		Compensate: func(ctx context.Context) error {
			for _, p := range projects {
				if _, err := d.Manager.RegisterDown(ctx, p); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// gateShared resolves each shared service's container and polls it ready. A
// service with a healthcheck is gated on Healthy; one without, on Started.
func gateShared(ctx context.Context, d UpDeps, names []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		cs, err := d.Docker.ListManaged(ctx, map[string]string{
			generate.LabelManaged: "true", generate.LabelShared: name,
		})
		if err != nil {
			return nil, fmt.Errorf("locate shared %s: %w", name, err)
		}
		id := firstRunningID(cs)
		if id == "" {
			return nil, fmt.Errorf("shared service %s has no running container after compose up", name)
		}
		cond := health.Started
		if det, err := d.Docker.ContainerInspect(ctx, id); err == nil && det.HasHealthcheck() {
			cond = health.Healthy
		}
		tm := health.Compile(nil)
		pollCtx := ctx
		if d.HealthTimeout > 0 {
			var cancel context.CancelFunc
			pollCtx, cancel = context.WithTimeout(ctx, d.HealthTimeout)
			defer cancel()
		}
		rec, err := health.Poll(pollCtx, d.Docker, health.Target{
			ContainerID: id, Service: generate.SharedAlias(name),
			Project: generate.SharedStackName, Condition: cond,
		}, tm)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"name": generate.SharedAlias(name), "health": rec.Status})
	}
	return out, nil
}

// composeUpPhase brings one project stack up. Compensation tears it back down
// (idempotent) — refs are owned by the shared phase, not unwound here.
func composeUpPhase(d UpDeps, project string, secretEnv map[string][]string, services []string) Phase {
	outDir := filepath.Join(d.Model.ProjectDir(project), generate.GenDir)
	cp := func() docker.Compose {
		return docker.Compose{
			Project: "devstack-" + project,
			File:    filepath.Join(outDir, generate.ComposeFile),
			Dir:     outDir, Runner: d.Runner,
			// Resolved secret values reach the containers ONLY here, via the
			// compose-up process env (Compose substitutes the valueless keys); they
			// are never written to a file (§7.5).
			Env: secretEnv[project],
		}
	}
	return Phase{
		Name:     "compose-up",
		Scope:    project,
		Mutating: true,
		Fingerprint: func(context.Context) (string, error) {
			fp, err := projectFingerprint(d.Model, project)
			if err != nil {
				return "", err
			}
			// Key on the active slice too: narrowing/widening --profile must
			// re-run compose-up rather than skip on a stale fingerprint.
			return Fingerprint(append([]string{fp}, services...)...), nil
		},
		Run: func(ctx context.Context) (any, error) {
			c := cp()
			// --rebuild forces a no-cache build (ignoring the selective-rebuild hash);
			// --build does a normal build. --rebuild implies a build even without --build.
			if d.Build || d.Rebuild {
				if err := c.Build(ctx, d.Rebuild, services...); err != nil {
					return nil, err
				}
			}
			if err := c.Up(ctx, services...); err != nil {
				return nil, fmt.Errorf("compose up %s: %w", project, err)
			}
			return map[string]any{"project": project, "services": services}, nil
		},
		Compensate: func(ctx context.Context) error {
			c := cp()
			return c.Down(ctx, false) // never -v: a failed up must not drop volumes
		},
	}
}

// hooksPhase runs a project's postUp hooks (unconditional). firstRun/postPull
// (idempotent, ledger-keyed) arrive with provision/git wiring.
// hookPhase runs one lifecycle-hook list at a saga phase boundary (spec 11).
// scope is a project name, or "" for workspace-scope hooks (run from the
// workspace root with no compose project). Empty lists are a no-op.
func hookPhase(d UpDeps, scope, phaseName string, hookList []config.Hook, onFail string) Phase {
	return Phase{
		Name:      phaseName, // preUp | postUp
		Scope:     scope,
		AlwaysRun: true,
		Run: func(ctx context.Context) (any, error) {
			if len(hookList) == 0 {
				return map[string]any{"ran": 0}, nil
			}
			var ex hooks.Execer
			ledgerProject := scope
			if scope == "" {
				ex = hooks.OSExecer{BaseDir: d.Model.Root}
				ledgerProject = "workspace"
			} else {
				outDir := filepath.Join(d.Model.ProjectDir(scope), generate.GenDir)
				ex = hooks.OSExecer{
					BaseDir: d.Model.ProjectDir(scope),
					Project: "devstack-" + scope,
					File:    filepath.Join(outDir, generate.ComposeFile),
				}
			}
			runner := &hooks.Runner{
				Execer: ex, Ledger: d.DB,
				Lock: func(ctx context.Context, fn func() error) error { return lock.WithLock(ctx, d.LockPath, fn) },
			}
			results, err := runner.RunPhase(ctx, hookList, hooks.PhaseOpts{
				Project: ledgerProject, Phase: phaseName, DefaultOnFailure: onFail,
			})
			if err != nil {
				return map[string]any{"results": results}, err
			}
			return map[string]any{"ran": len(results)}, nil
		},
	}
}

// firstRunPhase runs a project's `firstRun` hooks exactly once (spec 11): the
// first time the project comes up (e.g. seed/migrate against the freshly
// provisioned database). Idempotency is ledger-backed and keyed per hook NAME, so
// each firstRun hook runs once and is recorded under the flock; a re-`up` skips
// them. `workspace destroy`/`uninstall` clear the hook_run rows, so a fresh
// provision re-runs firstRun. It runs AFTER compose-up (the container — and its
// provisioned DB — exists) and before postUp. Default onFailure is abort: a failed
// first-time migration should stop the up rather than leave a half-seeded stack.
func firstRunPhase(d UpDeps, project string, hookList []config.Hook) Phase {
	return Phase{
		Name:  "firstRun",
		Scope: project,
		Run: func(ctx context.Context) (any, error) {
			if len(hookList) == 0 {
				return map[string]any{"ran": 0}, nil
			}
			outDir := filepath.Join(d.Model.ProjectDir(project), generate.GenDir)
			runner := &hooks.Runner{
				Execer: hooks.OSExecer{
					BaseDir: d.Model.ProjectDir(project),
					Project: "devstack-" + project,
					File:    filepath.Join(outDir, generate.ComposeFile),
				},
				Ledger: d.DB,
				Lock:   func(ctx context.Context, fn func() error) error { return lock.WithLock(ctx, d.LockPath, fn) },
			}
			results, err := runner.RunPhase(ctx, hookList, hooks.PhaseOpts{
				Project:          project,
				Phase:            "firstRun",
				Idempotent:       true,
				ScopeKey:         func(h config.Hook) string { return h.Name },
				DefaultOnFailure: hooks.OnAbort,
			})
			if err != nil {
				return map[string]any{"results": results}, err
			}
			return map[string]any{"ran": len(results)}, nil
		},
	}
}

// --- helpers ---------------------------------------------------------------

func firstRunningID(cs []docker.Container) string {
	for _, c := range cs {
		if c.Running() {
			return c.ID
		}
	}
	if len(cs) > 0 {
		return cs[0].ID
	}
	return ""
}

func sortedProjects(m *config.Model) []string {
	out := make([]string, 0, len(m.Projects))
	for k := range m.Projects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// configFingerprint hashes the workspace.yaml + every project's devstack.yaml so
// any config edit re-arms generate.
func configFingerprint(m *config.Model) (string, error) {
	parts := []string{readFileOrEmpty(filepath.Join(m.Root, "workspace.yaml"))}
	for _, p := range sortedProjects(m) {
		parts = append(parts, p, readFileOrEmpty(filepath.Join(m.ProjectDir(p), "devstack.yaml")))
	}
	return Fingerprint(parts...), nil
}

// projectFingerprint re-arms a project's compose-up on an edit to its devstack.yaml.
func projectFingerprint(m *config.Model, project string) (string, error) {
	return Fingerprint(project, readFileOrEmpty(filepath.Join(m.ProjectDir(project), "devstack.yaml"))), nil
}

func readFileOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
