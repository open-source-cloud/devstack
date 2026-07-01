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
// Exposure uses its OWN host-port range (55xxx/58xxx…), distinct from the
// provisioning range (45xxx), so the expose overlay and the provision overlay
// never publish the same host port and can both be applied without a duplicate
// binding. Ports are ledger-allocated (FreeHostPort), so the same engine keeps
// the same host port across runs and two terminals never collide.

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
// publishes on 127.0.0.1. Bases sit in the 5xxxx range so they never collide with
// the 4xxxx provisioning overlay. Kafka is the exception: host clients MUST reach
// the broker on 127.0.0.1:49092 (the fixed advertised external listener from the
// template), so it reuses the kafka provision port rather than a 5xxxx one.
var exposeEngines = map[string][]exposePort{
	"postgres":   {{5432, "postgres", "pg-expose", 55432, true}},
	"redis":      {{6379, "redis", "redis-expose", 56379, true}},
	"minio":      {{9000, "s3", "minio-expose", 59000, true}, {9001, "console", "minio-console-expose", 59001, false}},
	"localstack": {{4566, "aws", "localstack-expose", 54566, true}},
	"ministack":  {{4566, "aws", "ministack-expose", 54567, true}},
	"nats":       {{4222, "nats", "nats-expose", 54222, true}, {8222, "monitor", "nats-monitor-expose", 58222, false}},
	"kafka":      {{19092, "kafka", "kafka-provision", 49092, true}},
	"rabbitmq":   {{5672, "amqp", "rmq-expose", 55672, true}, {15672, "management", "rmq-mgmt-expose", 55673, false}},
}

// ExposableEngine reports whether an engine has a defined host-expose port set.
func ExposableEngine(engine string) bool {
	_, ok := exposeEngines[engine]
	return ok
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
	}
	return host
}
