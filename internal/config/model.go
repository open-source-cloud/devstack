// Package config defines the clean-slate, two-file declarative model
// (workspace.yaml + per-repo devstack.yaml), discovers the workspace by walking
// up from the CWD, parses with goccy/go-yaml (retaining source positions for
// file:line:col errors), and validates structure + cross-references against the
// WORKSPACE service graph (not a single project). It is the source of truth
// every other module reads. See docs/specs/01-config-schema.md.
//
// The loaded Model is immutable after Load: configuration is loaded once and
// treated read-only (the goccy-decoded model is not safe for concurrent
// mutation).
package config

// Schema constants for the apiVersion/kind header on every file (spec 01).
const (
	APIVersion    = "devstack/v1"
	KindWorkspace = "Workspace"
	KindProject   = "Project"
)

// Workspace is workspace.yaml — the shared layer at the workspace root: shared
// services, aliases, secret providers, network/proxy/tunnel, and the project
// list. Service slices (groups/defaultProfile) come from spec 12.
type Workspace struct {
	APIVersion     string               `yaml:"apiVersion" validate:"required,eq=devstack/v1"`
	Kind           string               `yaml:"kind" validate:"required,eq=Workspace"`
	Name           string               `yaml:"name" validate:"required,dsname"`
	Aliases        []string             `yaml:"aliases" validate:"dive,dsname"`
	Profiles       Profiles             `yaml:"profiles"`
	DefaultProfile string               `yaml:"defaultProfile"` // spec 12 — slice activated by `up` with no --profile
	Groups         map[string]Group     `yaml:"groups"`         // spec 12 — workspace-level service slices
	MemoryBudgetMB int                  `yaml:"memoryBudgetMB"` // spec 12/18 — warn when active services' memoryMB sum exceeds this
	Secrets        Secrets              `yaml:"secrets"`
	Network        Network              `yaml:"network"`
	Backend        *BackendConfig       `yaml:"backend"` // spec 21 — where the shared stack runs (nil = local)
	Hooks          Hooks                `yaml:"hooks"`   // spec 11 — workspace-scope lifecycle hooks
	Shared         map[string]SharedSvc `yaml:"shared" validate:"dive"`
	Projects       []ProjectRef         `yaml:"projects" validate:"dive"`
}

// BackendConfig selects WHERE the shared stack runs (spec 21). nil / the zero
// value means the LOCAL Docker daemon (the active context / DOCKER_HOST) — the
// default and fully backward-compatible. Exactly one of Context or Host may be
// set to target a remote host:
//   - Context names a `docker context` (typically an ssh:// one, which inherits
//     the user's SSH config/agent/ProxyJump for free — DECISIONS D9).
//   - Host is a raw DOCKER_HOST endpoint (ssh://, tcp://, unix://).
//
// The ledger is already keyed by Docker context (spec 08), so a remote endpoint
// simply keys its own rows — no cross-context count bleed with the local daemon.
// The mutual-exclusion of Context vs Host is enforced by the cross-field
// validator (validateBackend); the host scheme by the `dockerhost` field rule.
type BackendConfig struct {
	Context string `yaml:"context"`
	Host    string `yaml:"host" validate:"omitempty,dockerhost"`
}

// IsRemote reports whether this backend targets a non-local endpoint.
func (b *BackendConfig) IsRemote() bool {
	return b != nil && (b.Context != "" || b.Host != "")
}

// Profiles selects the env OVERLAY (config layering), distinct from the service
// slices in spec 12. Default overlay is `dev`.
type Profiles struct {
	Default string `yaml:"default"`
}

// Group is a named, workspace-authored service slice (spec 12).
type Group struct {
	Services     []string `yaml:"services"`
	MemoryHintMB int      `yaml:"memoryHintMB"`
}

// Secrets declares the providers that resolve secret:// refs (spec 04).
type Secrets struct {
	Providers []SecretProvider `yaml:"providers" validate:"dive"`
}

// SecretProvider is one named, typed secrets backend. Per-kind options are kept
// as explicit fields for the kinds v1 ships; unknown kinds are caught by the
// secrets factory (spec 04), not here.
type SecretProvider struct {
	Name      string `yaml:"name" validate:"required,dsname"`
	Kind      string `yaml:"kind" validate:"required"`
	Env       string `yaml:"env"`
	ProjectID string `yaml:"projectId"`
	Region    string `yaml:"region"`
}

// Network carries proxy + tunnel config (spec 05).
type Network struct {
	Proxy  Proxy  `yaml:"proxy"`
	Tunnel Tunnel `yaml:"tunnel"`
}

// Proxy is the reverse-proxy engine + local-HTTPS toggle (spec 05).
type Proxy struct {
	Engine     string `yaml:"engine"`     // caddy|traefik|nginx
	HTTPSLocal bool   `yaml:"httpsLocal"` // opt-in local HTTPS
}

// Tunnel is the optional public-tunnel config (spec 05).
type Tunnel struct {
	Provider string `yaml:"provider"`
	Hostname string `yaml:"hostname"`
}

// SharedSvc is one shared infrastructure service (postgres/redis/minio/...),
// rendered from a template (spec 03). Reached by alias DNS, ref-counted.
//
// Resources/Platform (spec 18) apply the same CPU/memory limits + arch selector
// to the shared stack that project services get; changing a shared limit is a
// stateful-service restart, gated behind the same explicit confirm as any shared
// recreate (spec 03) — never silent.
type SharedSvc struct {
	Template  string         `yaml:"template" validate:"required"`
	Params    map[string]any `yaml:"params"`
	Resources *Resources     `yaml:"resources"`                              // spec 18 — CPU/memory limits
	Platform  string         `yaml:"platform" validate:"omitempty,platform"` // spec 18 — e.g. linux/amd64
}

// EffectiveMemoryMB is the shared service's hard memory limit in MB (0 = unset).
func (s SharedSvc) EffectiveMemoryMB() int {
	if s.Resources != nil {
		return s.Resources.MemoryMB
	}
	return 0
}

// ProjectRef points workspace.yaml at a repo containing a devstack.yaml.
type ProjectRef struct {
	Name string `yaml:"name" validate:"required,dsname"`
	Path string `yaml:"path" validate:"required"`
	Git  string `yaml:"git"`
}

// Project is a per-repo devstack.yaml — the portable project layer: services →
// template + params, env, and which shared services they `uses`.
type Project struct {
	APIVersion string             `yaml:"apiVersion" validate:"required,eq=devstack/v1"`
	Kind       string             `yaml:"kind" validate:"required,eq=Project"`
	Name       string             `yaml:"name" validate:"required,dsname"`
	Services   map[string]Service `yaml:"services" validate:"required,dive"`
	Hooks      Hooks              `yaml:"hooks"`                     // spec 11 — project-scope lifecycle hooks
	Resources  []ResourceDecl     `yaml:"resources" validate:"dive"` // spec 27 — declarative data-plane resources
	Tasks      map[string]Task    `yaml:"tasks" validate:"dive"`     // spec 31 — non-container task graph (`devstack run`)
}

// Task is one node in a project's task graph (spec 31): a short-lived command run
// on demand by `devstack run`, NOT a container. `run: host` executes on the host
// (inheriting your toolchain); `run: exec` runs inside a service container via
// `compose exec`. `deps` are other task names that must complete first; the graph
// is executed in dependency order (cycles are rejected at run time). `watch` marks
// long-running dev-server tasks that `--watch` keeps alive.
type Task struct {
	Command []string          `yaml:"command" validate:"required,min=1"`
	Run     string            `yaml:"run" validate:"omitempty,oneof=host exec"` // default host
	Service string            `yaml:"service"`                                  // target for run:exec
	Deps    []string          `yaml:"deps"`
	Workdir string            `yaml:"workdir"`
	Env     map[string]string `yaml:"env"`
	Watch   bool              `yaml:"watch"`
}

// ResourceDecl is one declarative data-plane resource a project needs INSIDE a
// shared engine (spec 27): a database, bucket, lifecycle policy, queue, … `up`
// provisions each idempotently. The block is additive and forward-tolerant
// (unknown keys are ignored, never fatal); the cross-ref resolver checks `uses`
// targets a declared shared instance, `kind` is one the engine supports, and no
// two resources collide on (engine, name). Removing an entry NEVER auto-drops the
// resource (Q-RESOURCE-DRIFT) — teardown is always explicit + confirmed.
type ResourceDecl struct {
	Uses        string         `yaml:"uses" validate:"required"` // workspace.shared.<name>
	Kind        string         `yaml:"kind" validate:"required"` // database|user|bucket|lifecycle|queue|stream|topic
	Name        string         `yaml:"name"`                     // engine-level identifier (default: project name)
	Engine      string         `yaml:"engine"`                   // optional; inferred from the uses target's template
	Params      map[string]any `yaml:"params"`                   // kind-specific knobs
	Credentials string         `yaml:"credentials" validate:"omitempty,oneof=predictable generated"`
}

// Service is one container in a project stack.
type Service struct {
	Template    string         `yaml:"template" validate:"required"`
	Params      map[string]any `yaml:"params"`
	Uses        []string       `yaml:"uses"` // consume SHARED services: workspace.shared.<name>
	Env         Env            `yaml:"env"`
	Ports       map[string]int `yaml:"ports"`
	Profiles    []string       `yaml:"profiles"`                               // spec 12 — Compose profile membership tags
	MemoryMB    int            `yaml:"memoryMB"`                               // spec 12/18 — budget hint == shorthand for resources.memoryMB
	Resources   *Resources     `yaml:"resources"`                              // spec 18 — CPU/memory/pids limits (nil = none)
	Platform    string         `yaml:"platform" validate:"omitempty,platform"` // spec 18 — arch selector, e.g. linux/amd64
	Healthcheck *Healthcheck   `yaml:"healthcheck"`                            // spec 10 — readiness probe (nil = none)
	DependsOn   []DependsOn    `yaml:"dependsOn" validate:"dive"`              // spec 10 — ordering edges
}

// Resources is the spec-18 per-service resource-limit block. It lowers to a
// deterministic dual-write in the generated compose (deploy.resources.limits.*
// AND the legacy top-level cpus/mem_limit/pids_limit), so both the deploy-aware
// and non-deploy compose readers honor the same canonical values. All fields are
// optional; an omitted field emits nothing. See docs/specs/18.
type Resources struct {
	CPUs            string `yaml:"cpus" validate:"omitempty,cpus"`             // fractional cores, e.g. "1.5"
	MemoryMB        int    `yaml:"memoryMB" validate:"omitempty,gte=0"`        // hard memory limit
	MemoryReserveMB int    `yaml:"memoryReserveMB" validate:"omitempty,gte=0"` // soft reservation (scheduling hint)
	PidsLimit       int    `yaml:"pidsLimit" validate:"omitempty,gte=0"`       // max PIDs
}

// EffectiveMemoryMB is the service's hard memory limit in MB: resources.memoryMB
// when set, else the top-level memoryMB shorthand (spec 18). 0 means unset — used
// for both the emitted mem_limit and the budget summation so the two never drift.
func (s Service) EffectiveMemoryMB() int {
	if s.Resources != nil && s.Resources.MemoryMB > 0 {
		return s.Resources.MemoryMB
	}
	return s.MemoryMB
}

// Healthcheck declares a service's readiness probe (spec 10). It compiles to
// BOTH a Compose-native healthcheck: block and a tool-side prober; this struct
// is the declarative source — `internal/health` (C3b) normalizes durations to
// time.Duration and owns the per-kind semantics. A nil *Healthcheck means the
// service declares no check (it may still inherit one from its template).
//
// Duration fields (interval/timeout/startPeriod) are Compose-style strings
// (e.g. "5s", "1m30s") validated here as parseable Go durations; they stay
// strings because the compose lowering emits strings verbatim.
type Healthcheck struct {
	Kind         string   `yaml:"kind" validate:"required,oneof=tcp http https exec pg_isready redis"`
	Port         int      `yaml:"port"`
	Path         string   `yaml:"path"`         // http/https
	ExpectStatus string   `yaml:"expectStatus"` // http/https; "200" or a "200-399" range
	Host         string   `yaml:"host"`         // http/https Host header
	Command      []string `yaml:"command"`      // exec kind: argv (exit 0 = healthy)
	User         string   `yaml:"user"`         // pg_isready
	DB           string   `yaml:"db"`           // pg_isready
	Auth         string   `yaml:"auth"`         // redis (may be a secret:// ref)
	Interval     string   `yaml:"interval" validate:"omitempty,duration"`
	Timeout      string   `yaml:"timeout" validate:"omitempty,duration"`
	Retries      int      `yaml:"retries"`
	StartPeriod  string   `yaml:"startPeriod" validate:"omitempty,duration"`
}

// DependsOn is one readiness-ordering edge (spec 10). Service targets an
// intra-project service name or a shared service ("workspace.shared.<name>").
// Condition is "healthy" (default) or "started"; a "healthy" edge requires the
// target to declare a healthcheck — that semantic check lives in `internal/health`
// (generate-time, C3b/X2), not in these structural rules.
type DependsOn struct {
	Service   string `yaml:"service" validate:"required"`
	Condition string `yaml:"condition" validate:"omitempty,oneof=healthy started"`
}

// Env declares the container environment. `raw`/`prefixed` are literal (with
// ${...} interpolation); `import` pulls exported vars from another service.
type Env struct {
	Raw      map[string]string `yaml:"raw"`
	Prefixed map[string]string `yaml:"prefixed"`
	Import   []Import          `yaml:"import" validate:"dive"`
}

// Import pulls a set of exported vars from a referenced service.
type Import struct {
	From string   `yaml:"from" validate:"required"`
	Vars []string `yaml:"vars"`
}

// Hooks groups the lifecycle-hook lists by saga phase (spec 11). It attaches to
// a Project (per-repo) and to the Workspace (whole-bootstrap); a hook targets a
// service via Hook.Service, not by nesting under a service. Lists REPLACE on
// overlay merge unless the YAML opts into `$merge: append` (spec 02). The
// idempotency/ordering semantics (firstRun ledger, run:exec gating) belong to
// `internal/hooks` (C4); these are the declarative shape only.
type Hooks struct {
	PreUp    []Hook `yaml:"preUp" validate:"dive"`
	FirstRun []Hook `yaml:"firstRun" validate:"dive"`
	PostUp   []Hook `yaml:"postUp" validate:"dive"`
	PostPull []Hook `yaml:"postPull" validate:"dive"`
	PreDown  []Hook `yaml:"preDown" validate:"dive"`
}

// IsZero reports whether no hooks are declared (all phase lists empty), so
// callers can cheaply skip the hook machinery.
func (h Hooks) IsZero() bool {
	return len(h.PreUp) == 0 && len(h.FirstRun) == 0 && len(h.PostUp) == 0 &&
		len(h.PostPull) == 0 && len(h.PreDown) == 0
}

// Hook is one declarative command run at a saga phase (spec 11). `run: host`
// executes via os/exec from a documented working dir; `run: exec` shells into a
// running service via `compose exec -T`. Command is an argv array (never
// shell-split by us). Timeout is a Go duration string (default applied by C4).
// `service` is required when run==exec — enforced semantically in `internal/hooks`,
// not here, because that rule is transport-specific.
type Hook struct {
	Name      string            `yaml:"name" validate:"required,dsname"`
	Run       string            `yaml:"run" validate:"required,oneof=host exec"`
	Service   string            `yaml:"service"` // target for run:exec
	Command   []string          `yaml:"command" validate:"required,min=1"`
	Workdir   string            `yaml:"workdir"`
	Env       map[string]string `yaml:"env"`
	Timeout   string            `yaml:"timeout" validate:"omitempty,duration"`
	Retries   int               `yaml:"retries"`
	OnFailure string            `yaml:"onFailure" validate:"omitempty,oneof=abort warn continue"`
	Once      bool              `yaml:"once"`
}

// Model is the assembled, validated workspace: workspace.yaml plus every
// project's devstack.yaml keyed by project name. Immutable after Load.
type Model struct {
	// Root is the absolute workspace root (the dir containing workspace.yaml).
	Root string
	// Workspace is the parsed workspace.yaml.
	Workspace Workspace
	// Projects is each repo's parsed devstack.yaml, keyed by project name.
	Projects map[string]Project
	// projectDir maps a project name to its absolute directory (Root/<path>).
	projectDir map[string]string
}

// ProjectDir returns the absolute directory for a project, or "" if unknown.
func (m *Model) ProjectDir(name string) string { return m.projectDir[name] }

// SharedNames returns the declared shared-service names (unordered).
func (m *Model) SharedNames() []string {
	out := make([]string, 0, len(m.Workspace.Shared))
	for name := range m.Workspace.Shared {
		out = append(out, name)
	}
	return out
}
