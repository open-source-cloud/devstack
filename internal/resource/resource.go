// Package resource is the data-plane resource layer (spec 27): the
// generalization of today's per-project Postgres provisioning into a first-class
// Resource model + a Provisioner interface family. A Resource is a per-project
// object that lives INSIDE a shared engine container (a database, role, bucket,
// queue, …), owned by exactly one project (tenant-scoping is the load-bearing
// invariant) and tracked in the `provisioned` ownership ledger.
//
// The Provisioner is the engine-agnostic seam the orchestrator + the `resource`
// cobra commands depend on — one implementation per shared engine. It mirrors the
// existing internal/provision.Conn pattern: small, existence-guarded, idempotent,
// and mockable so race/unit tests run without a live engine. Postgres is the only
// live provisioner in this milestone (it wraps provision.EnsureProject verbatim);
// other engines land in the Full scope (spec 27 §Dependencies).
package resource

import "context"

// CredentialPolicy selects how a resource's credential is produced (spec 27
// §Credential surfacing). `predictable` is the Postgres dev-cred model (password
// == project name, nothing secret stored, attrs reach containers via env.import);
// `generated` is a random value pushed to a secrets provider (opt-in).
type CredentialPolicy string

const (
	// CredPredictable is the loopback dev-cred default (password == owner project).
	CredPredictable CredentialPolicy = "predictable"
	// CredGenerated is a crypto/rand value surfaced via the secrets Pusher.
	CredGenerated CredentialPolicy = "generated"
)

// Resource is the tuple (engine, kind, name, owner, attributes) — one per-project
// object inside a shared engine. Name is tenant-namespaced (defaults to the owner
// project); Owner is the ONLY project that may read or mutate it.
type Resource struct {
	Engine   string           // == template provides: ; ledger 'engine' column
	Kind     string           // database|role|user|bucket|lifecycle|queue|stream|topic
	Name     string           // engine-level identifier (tenant-namespaced)
	Owner    string           // owning project (provisioned.project)
	Params   map[string]any   // kind-specific knobs (e.g. lifecycle: expireDays)
	CredKind CredentialPolicy // predictable | generated
}

// Attrs are the connection-surfacing facts a consumer needs (host/port/user/
// database/bucket/subject …) plus a credential reference. Derived, never stored.
type Attrs map[string]string

// Target is the resolved, host-reachable admin endpoint for ONE shared instance,
// produced by the overlay + ledger-port resolution (mirrors provision.DSN today).
type Target struct {
	Instance string            // shared service name (e.g. "postgres")
	Host     string            // "127.0.0.1"
	Port     int               // ledger-allocated published host port
	AdminEnv map[string]string // root creds from the instance's params (user/password/database)
}

// Provisioner is the engine-agnostic contract the orchestrator + cobra commands
// depend on. One implementation per shared engine. Every method is safe to call
// while the caller holds the machine-global flock; Ensure/Drop MUST be
// existence-guarded and idempotent (CREATE DATABASE / CREATE ROLE are not, D8).
type Provisioner interface {
	Engine() string                                                  // matches template provides:
	Kinds() []string                                                 // the kinds it can create
	Ensure(ctx context.Context, t Target, r Resource) (Attrs, error) // idempotent, existence-guarded
	Drop(ctx context.Context, t Target, r Resource) error            // teardown for gc / --purge-data
	Preflight(ctx context.Context, t Target) error                   // tool present + version-compatible
}

// supportedKinds is the static engine→kinds catalog (spec 27 §engine table). It
// is the single source of truth the config resolver consults to reject a kind the
// target engine's provisioner does not list (no ledger row nothing can drop). It
// is a plain map — no provisioner needs constructing — so config can validate
// without a live connection. Engines absent here are forward-tolerant (unknown →
// no kind check; the config resolver only enforces membership for known engines).
var supportedKinds = map[string][]string{
	"postgres": {"database", "role", "user"},
	"redis":    {"redis_index", "acl_user", "queue", "topic"},
	"minio":    {"bucket", "lifecycle", "access_key"},
	// The localstack template (`provides: aws`) backs SQS queues + SNS topics; the
	// provisioner is keyed by the template name "localstack".
	"localstack": {"bucket", "queue", "topic", "stream", "table"},
	"nats":       {"stream", "consumer", "kv", "queue", "topic"},
	"kafka":      {"topic", "acl", "stream"},
}

// Kinds returns the kinds a given engine's provisioner can create, or nil if the
// engine is not in the catalog (an unknown/custom engine, treated leniently).
func Kinds(engine string) []string {
	ks := supportedKinds[engine]
	if ks == nil {
		return nil
	}
	out := make([]string, len(ks))
	copy(out, ks)
	return out
}

// SupportsKind reports whether engine's provisioner lists kind. Unknown engines
// return true (forward-tolerant: we cannot know, so we do not reject).
func SupportsKind(engine, kind string) bool {
	ks := supportedKinds[engine]
	if ks == nil {
		return true
	}
	for _, k := range ks {
		if k == kind {
			return true
		}
	}
	return false
}

// Registry maps an engine to its Provisioner. The orchestrator and the `resource`
// commands share one, built with the injected Postgres connector so provisioning
// is daemon-free in tests.
type Registry struct {
	byEngine map[string]Provisioner
}

// NewRegistry builds a Registry from the given provisioners (last wins per engine).
func NewRegistry(ps ...Provisioner) *Registry {
	r := &Registry{byEngine: map[string]Provisioner{}}
	for _, p := range ps {
		r.byEngine[p.Engine()] = p
	}
	return r
}

// For returns the provisioner for engine, ok=false if none is registered.
func (r *Registry) For(engine string) (Provisioner, bool) {
	p, ok := r.byEngine[engine]
	return p, ok
}

// Engines returns the registered engine names (unordered).
func (r *Registry) Engines() []string {
	out := make([]string, 0, len(r.byEngine))
	for e := range r.byEngine {
		out = append(out, e)
	}
	return out
}
