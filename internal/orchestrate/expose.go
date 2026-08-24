package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
)

// This file implements `shared expose` / `shared ports`: publishing the shared
// engines on stable 127.0.0.1 host ports so a developer's GUI clients (DataGrip,
// a Redis or S3 browser, the RabbitMQ management UI) can reach them. The default
// posture is still "no host ports" (DNS over devstack_shared, spec 03); exposure
// is an explicit opt-in that, like provisioning, is an UP-TIME compose overlay —
// it never touches the deterministic, golden-asserted generated compose.
//
// Exposure publishes each engine on its OWN WELL-KNOWN host port — the same port
// the template advertises in-network (postgres→5432, mysql→3306, redis→6379, …) —
// so a GUI client's default connection settings just work and there is no gap
// between what the template's `defaultPort` says and what the host sees. That
// deliberately differs from the provisioning range (45xxx): the two overlays map
// different host ports onto the same container port, so both can be applied
// without a duplicate binding. Ports remain ledger-allocated (FreeHostPort) with
// the standard port as the search base, so the same engine keeps the same host
// port across runs, and if a host-native server already holds the standard port
// the allocator transparently falls back to the next free one in the band.

const exposeFile = "compose.expose.yaml"

// exposePort is one host-published port for a shared engine.
type exposePort struct {
	container int    // the in-container port to publish
	label     string // human label (postgres / console / management / …)
	purpose   string // ledger port_alloc purpose (stable per engine)
	base      int    // host-port search base
	primary   bool   // the port a client uses for the engine's main protocol
}

// exposeEngines maps a shared engine (template name) to the ports `shared expose`
// publishes on 127.0.0.1. The search base is the engine's WELL-KNOWN port (equal
// to the in-container port), so clients connect on the port they already expect
// and the allocator only drifts off it when a host-native server already holds it.
// Kafka is the exception: host clients MUST reach the broker on 127.0.0.1:49092
// (the fixed advertised external listener from the template), so it keeps that
// base and reuses the kafka provision port rather than the broker's 19092.
var exposeEngines = map[string][]exposePort{
	"postgres":   {{5432, "postgres", "pg-expose", 5432, true}},
	"mysql":      {{3306, "mysql", "mysql-expose", 3306, true}},
	"mariadb":    {{3306, "mariadb", "mariadb-expose", 3306, true}},
	"mongodb":    {{27017, "mongodb", "mongodb-expose", 27017, true}},
	"cassandra":  {{9042, "cassandra", "cassandra-expose", 9042, true}},
	"arangodb":   {{8529, "arangodb", "arangodb-expose", 8529, true}},
	"redis":      {{6379, "redis", "redis-expose", 6379, true}},
	"minio":      {{9000, "s3", "minio-expose", 9000, true}, {9001, "console", "minio-console-expose", 9001, false}},
	"localstack": {{4566, "aws", "localstack-expose", 4566, true}},
	"ministack":  {{4566, "aws", "ministack-expose", 4566, true}},
	"nats":       {{4222, "nats", "nats-expose", 4222, true}, {8222, "monitor", "nats-monitor-expose", 8222, false}},
	"kafka":      {{19092, "kafka", "kafka-provision", 49092, true}},
	"rabbitmq":   {{5672, "amqp", "rmq-expose", 5672, true}, {15672, "management", "rmq-mgmt-expose", 15672, false}},

	// Stores and caches.
	"valkey":      {{6379, "valkey", "valkey-expose", 6379, true}},
	"timescaledb": {{5432, "postgres", "timescale-expose", 5432, true}},
	"clickhouse":  {{8123, "http", "clickhouse-expose", 8123, true}, {9000, "native", "clickhouse-native-expose", 9000, false}},
	"neo4j":       {{7687, "bolt", "neo4j-expose", 7687, true}, {7474, "browser", "neo4j-browser-expose", 7474, false}},
	"etcd":        {{2379, "etcd", "etcd-expose", 2379, true}},
	"consul":      {{8500, "http", "consul-expose", 8500, true}},

	// Object storage. rustfs speaks the S3 API on the same ports MinIO uses, so
	// the two only collide on the host when both are declared — the allocator
	// then drifts the second one off its base.
	"rustfs": {{9000, "s3", "rustfs-expose", 9000, true}, {9001, "console", "rustfs-console-expose", 9001, false}},

	// Search.
	"meilisearch": {{7700, "http", "meili-expose", 7700, true}},
	"opensearch":  {{9200, "http", "opensearch-expose", 9200, true}},

	// Developer-facing services. These are the ones a HUMAN opens in a browser,
	// so their secondary UI ports matter more than usual.
	"mailpit":   {{1025, "smtp", "mailpit-expose", 1025, true}, {8025, "web", "mailpit-web-expose", 8025, false}},
	"keycloak":  {{8080, "http", "keycloak-expose", 8080, true}},
	"jaeger":    {{4317, "otlp-grpc", "jaeger-expose", 4317, true}, {16686, "ui", "jaeger-ui-expose", 16686, false}},
	"mosquitto": {{1883, "mqtt", "mosquitto-expose", 1883, true}},
}

// ExposableEngine reports whether an engine has a defined host-expose port set.
func ExposableEngine(engine string) bool {
	_, ok := exposeEngines[engine]
	return ok
}

// primaryExposePort returns an engine's PRIMARY host-published port — the one a
// client (and devstack's own host-side provisioning) connects the engine's main
// protocol on. This is the single source of truth for "the host port of engine
// X": provisioning, reset, snapshot and resource ops all resolve their admin
// endpoint from it, so there is exactly ONE host port per engine (the standard
// one), never a separate provisioning band.
func primaryExposePort(engine string) (exposePort, bool) {
	for _, ep := range exposeEngines[engine] {
		if ep.primary {
			return ep, true
		}
	}
	return exposePort{}, false
}

// exposableUnion returns the shared instances to publish: the requested set
// unioned with any already-exposed instance (so writing the overlay never drops
// another instance's ports), filtered to engines that support exposure. Sorted
// for a byte-stable overlay.
func exposableUnion(d UpDeps, want []string) []string {
	set := map[string]bool{}
	for _, i := range want {
		set[i] = true
	}
	for _, i := range exposedInstances(d.Model.Root) {
		set[i] = true
	}
	var insts []string
	for i := range set {
		if s, ok := d.Model.Workspace.Shared[i]; ok && ExposableEngine(s.Template) {
			insts = append(insts, i)
		}
	}
	sort.Strings(insts)
	return insts
}

// exposeOverlayFor allocates the standard host ports for the exposable instances
// among want (unioned with the currently-exposed set) and WRITES the single
// expose overlay, returning its path ("" when there is nothing to expose). It does
// NOT run compose — the caller (the shared phase) folds the returned path into its
// own `compose up` so ports are published as the services come up.
func exposeOverlayFor(ctx context.Context, d UpDeps, want []string) (string, error) {
	insts := exposableUnion(d, want)
	if len(insts) == 0 {
		return "", nil
	}
	_, pub, err := allocateExposePorts(ctx, d, insts)
	if err != nil {
		return "", err
	}
	return writeExposeOverlay(d.Model.Root, pub)
}

// ensureExposed is the unified host-reachability primitive for callers that need
// the ports published NOW (provisioning, reset, snapshot, resource ops): it writes
// the single expose overlay for the exposable instances among want (unioned with
// the already-exposed set, so it never drops another instance's ports) and applies
// it via `compose up`. It is idempotent — the ledger returns the same standard
// ports and the overlay bytes are unchanged, so compose does not recreate the
// container on repeat calls. Returns instance→primary host port. Because both
// auto-expose and every host-side admin op go through this one overlay, they can
// never fight over a container's `ports:`.
func ensureExposed(ctx context.Context, d UpDeps, want []string) (map[string]int, error) {
	insts := exposableUnion(d, want)
	if len(insts) == 0 {
		return map[string]int{}, nil
	}
	out, pub, err := allocateExposePorts(ctx, d, insts)
	if err != nil {
		return nil, err
	}
	overlay, err := writeExposeOverlay(d.Model.Root, pub)
	if err != nil {
		return nil, err
	}
	outDir := filepath.Join(d.Model.Root, generate.GenDir, "shared")
	if err := composeUpShared(ctx, d, outDir, []string{overlay}, insts); err != nil {
		return nil, fmt.Errorf("apply host-port overlay: %w", err)
	}
	ports := map[string]int{}
	for _, ep := range out {
		if ep.Primary {
			ports[ep.Instance] = ep.Port
		}
	}
	return ports, nil
}

// ExposedPort is one host-published shared-service port with a client-ready
// connection hint (the `--json` schema + the plain-table source).
type ExposedPort struct {
	Instance  string `json:"instance"`
	Engine    string `json:"engine"`
	Alias     string `json:"alias"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Container int    `json:"container"`
	Primary   bool   `json:"primary"`
	URL       string `json:"url,omitempty"`
}

// publishedPort is one host:container mapping for the overlay writer.
type publishedPort struct {
	host      int
	container int
}

// resolveExposeInstances returns the shared instances to expose: the requested
// subset (validated), or — when none are named — every shared instance whose
// engine supports exposure. Order is stable (sorted) for deterministic output.
func resolveExposeInstances(d UpDeps, requested []string) ([]string, error) {
	shared := d.Model.Workspace.Shared
	if len(requested) == 0 {
		var all []string
		for name, s := range shared {
			if ExposableEngine(s.Template) {
				all = append(all, name)
			}
		}
		sort.Strings(all)
		if len(all) == 0 {
			return nil, fmt.Errorf("no exposable shared services in this workspace (declare one under workspace.shared and run `devstack up`)")
		}
		return all, nil
	}
	var out []string
	for _, name := range requested {
		s, ok := shared[name]
		if !ok {
			return nil, fmt.Errorf("no shared service %q in this workspace", name)
		}
		if !ExposableEngine(s.Template) {
			return nil, fmt.Errorf("shared service %q (engine %q) has no host-expose ports defined", name, s.Template)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// allocateExposePorts resolves the stable host ports for each instance's expose
// port set (idempotent via the ledger) and builds the ExposedPort projection.
func allocateExposePorts(ctx context.Context, d UpDeps, insts []string) ([]ExposedPort, map[string][]publishedPort, error) {
	pub := map[string][]publishedPort{}
	var out []ExposedPort
	for _, inst := range insts {
		engine := d.Model.Workspace.Shared[inst].Template
		params := d.Model.Workspace.Shared[inst].Params
		for _, ep := range exposeEngines[engine] {
			port, err := d.Manager.FreeHostPort(ctx, generate.SharedAlias(inst), ep.purpose, ep.base)
			if err != nil {
				return nil, nil, fmt.Errorf("allocate %s host port for %s: %w", ep.label, inst, err)
			}
			pub[inst] = append(pub[inst], publishedPort{host: port, container: ep.container})
			out = append(out, ExposedPort{
				Instance: inst, Engine: engine, Alias: generate.SharedAlias(inst),
				Label: ep.label, Host: "127.0.0.1", Port: port, Container: ep.container,
				Primary: ep.primary, URL: connectionURL(engine, ep, params, port),
			})
		}
	}
	return out, pub, nil
}

// writeExposeOverlay writes the persistent up-time overlay that publishes each
// instance's expose ports on 127.0.0.1. Loopback-only (never 0.0.0.0) so nothing
// leaves the host. Instances/ports are sorted so the file is byte-stable.
func writeExposeOverlay(root string, pub map[string][]publishedPort) (string, error) {
	insts := make([]string, 0, len(pub))
	for inst := range pub {
		insts = append(insts, inst)
	}
	sort.Strings(insts)
	var b strings.Builder
	b.WriteString("services:\n")
	for _, inst := range insts {
		ports := pub[inst]
		sort.Slice(ports, func(i, j int) bool { return ports[i].container < ports[j].container })
		fmt.Fprintf(&b, "  %s:\n    ports:\n", inst)
		for _, p := range ports {
			fmt.Fprintf(&b, "      - \"127.0.0.1:%d:%d\"\n", p.host, p.container)
		}
	}
	dir := filepath.Join(root, generate.GenDir, "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, exposeFile)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// exposeOverlayPath returns the overlay path (whether or not it exists yet).
func exposeOverlayPath(root string) string {
	return filepath.Join(root, generate.GenDir, "shared", exposeFile)
}

// fileExists reports whether path is an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ExposeShared publishes the requested shared instances (or all exposable ones)
// on stable 127.0.0.1 host ports and applies the overlay by recreating those
// services. It returns the connection projection. Refuses a remote (ViaProxy)
// backend, whose bridge network is not host-routable (spec 21).
func ExposeShared(ctx context.Context, d UpDeps, requested []string) ([]ExposedPort, error) {
	if d.Backend.Reachability() == docker.ViaProxy {
		return nil, fmt.Errorf("cannot publish host ports on a %s: a remote bridge network is not host-routable (spec 21); reach it through a tunnel instead", d.Backend.String())
	}
	insts, err := resolveExposeInstances(d, requested)
	if err != nil {
		return nil, err
	}
	outDir := filepath.Join(d.Model.Root, generate.GenDir, "shared")
	if _, err := os.Stat(filepath.Join(outDir, generate.ComposeFile)); err != nil {
		return nil, fmt.Errorf("shared stack not generated yet — run `devstack up` first")
	}
	out, pub, err := allocateExposePorts(ctx, d, insts)
	if err != nil {
		return nil, err
	}
	overlay, err := writeExposeOverlay(d.Model.Root, pub)
	if err != nil {
		return nil, err
	}
	if err := composeUpShared(ctx, d, outDir, []string{overlay}, insts); err != nil {
		return nil, fmt.Errorf("apply expose overlay: %w", err)
	}
	return out, nil
}

// UnexposeShared removes the expose overlay and recreates the shared services
// without their host ports (DNS-only again). Ledger port rows are left in place
// (idempotent — a later `expose` reuses the same ports).
func UnexposeShared(ctx context.Context, d UpDeps) error {
	path := exposeOverlayPath(d.Model.Root)
	if _, err := os.Stat(path); err != nil {
		return nil // nothing exposed
	}
	insts := exposedInstances(d.Model.Root)
	if err := os.Remove(path); err != nil {
		return err
	}
	outDir := filepath.Join(d.Model.Root, generate.GenDir, "shared")
	if _, err := os.Stat(filepath.Join(outDir, generate.ComposeFile)); err != nil {
		return nil // stack not up; overlay removal is enough
	}
	return composeUpShared(ctx, d, outDir, nil, insts)
}

// ExposedStatus is the read-only projection for `shared ports`: it re-derives the
// currently-exposed ports from the persisted overlay + the ledger, without
// mutating anything (lock-free snapshot).
func ExposedStatus(ctx context.Context, d UpDeps) ([]ExposedPort, error) {
	insts := exposedInstances(d.Model.Root)
	if len(insts) == 0 {
		return nil, nil
	}
	out, _, err := allocateExposePorts(ctx, d, insts)
	return out, err
}

// exposedInstances reads which instances currently have an expose overlay by
// parsing the overlay's top-level service keys. Returns nil if not exposed.
func exposedInstances(root string) []string {
	data, err := os.ReadFile(exposeOverlayPath(root))
	if err != nil {
		return nil
	}
	var insts []string
	for line := range strings.SplitSeq(string(data), "\n") {
		// Top-level service keys are indented exactly two spaces: "  <name>:".
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			insts = append(insts, strings.TrimSuffix(strings.TrimSpace(line), ":"))
		}
	}
	sort.Strings(insts)
	return insts
}

// composeUpShared runs `compose up -d <insts>` for the shared stack with the given
// override files (nil = none), pinned to the active backend.
func composeUpShared(ctx context.Context, d UpDeps, outDir string, overrides, insts []string) error {
	runner := d.Runner
	if runner == nil {
		runner = docker.ExecRunner{}
	}
	cp := docker.Compose{
		Project:    generate.SharedStackName,
		File:       filepath.Join(outDir, generate.ComposeFile),
		Dir:        outDir,
		Runner:     runner,
		Overrides:  overrides,
		ContextEnv: d.Backend.ComposeEnv(),
	}
	return cp.Up(ctx, insts...)
}

// connectionURL builds a client-ready connection hint for one exposed port.
// Credentials shown are the shared engine's dev admin creds (loopback-only,
// container-isolation-is-a-non-goal threat model); per-project DB creds follow
// the documented postgres://<project>:<project>@… DSN.
func connectionURL(engine string, ep exposePort, params map[string]any, port int) string {
	host := fmt.Sprintf("127.0.0.1:%d", port)
	switch engine {
	case "postgres":
		user := paramString(params, "rootUser", "devstack")
		pass := paramString(params, "rootPassword", "devstack")
		return fmt.Sprintf("postgres://%s:%s@%s/postgres?sslmode=disable", user, pass, host)
	case "mysql", "mariadb":
		user := paramString(params, "rootUser", "devstack")
		pass := paramString(params, "rootPassword", "devstack")
		return fmt.Sprintf("mysql://%s:%s@%s/%s", user, pass, host, user)
	case "mongodb":
		user := paramString(params, "rootUser", "devstack")
		pass := paramString(params, "rootPassword", "devstack")
		return fmt.Sprintf("mongodb://%s:%s@%s/?authSource=admin", user, pass, host)
	case "cassandra":
		return host // contact point host:9042 (CQL native transport)
	case "arangodb":
		return "http://" + host // HTTP API + web UI (root / rootPassword)
	case "redis":
		return "redis://" + host
	case "minio":
		return "http://" + host // S3 endpoint / console URL
	case "localstack", "ministack":
		return "http://" + host // AWS endpoint-url
	case "nats":
		if ep.label == "monitor" {
			return "http://" + host
		}
		return "nats://" + host
	case "kafka":
		return host // bootstrap server
	case "rabbitmq":
		if ep.label == "management" {
			return "http://" + host
		}
		user := paramString(params, "user", "devstack")
		return fmt.Sprintf("amqp://%s@%s", user, host)

	case "valkey":
		return "redis://" + host // Valkey speaks the RESP protocol; redis:// clients work unchanged
	case "timescaledb":
		user := paramString(params, "rootUser", "devstack")
		pass := paramString(params, "rootPassword", "devstack")
		db := paramString(params, "database", "devstack")
		return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", user, pass, host, db)
	case "clickhouse":
		user := paramString(params, "rootUser", "devstack")
		pass := paramString(params, "rootPassword", "devstack")
		if ep.label == "native" {
			return fmt.Sprintf("clickhouse://%s:%s@%s", user, pass, host)
		}
		return fmt.Sprintf("http://%s:%s@%s", user, pass, host)
	case "neo4j":
		if ep.label == "browser" {
			return "http://" + host
		}
		user := paramString(params, "rootUser", "neo4j")
		pass := paramString(params, "rootPassword", "devstack1")
		return fmt.Sprintf("bolt://%s:%s@%s", user, pass, host)
	case "etcd":
		return "http://" + host
	case "consul":
		return "http://" + host // API and the web UI share this port (/ui)
	case "rustfs":
		return "http://" + host // S3 endpoint / console URL, same shape as minio
	case "meilisearch":
		return "http://" + host
	case "opensearch":
		return "http://" + host
	case "mailpit":
		if ep.label == "web" {
			return "http://" + host // the inbox a human opens
		}
		return "smtp://" + host
	case "keycloak":
		return "http://" + host
	case "jaeger":
		if ep.label == "ui" {
			return "http://" + host
		}
		return host // OTLP gRPC endpoint: host:port, no scheme
	case "mosquitto":
		return "mqtt://" + host
	}
	return host
}
