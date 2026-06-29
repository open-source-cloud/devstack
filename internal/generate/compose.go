package generate

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/goccy/go-yaml"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/proxy"
	"github.com/open-source-cloud/devstack/internal/secrets"
	"github.com/open-source-cloud/devstack/internal/template"
)

// composeDoc is a generic compose document under assembly, before it is handed to
// compose-go for validation + normalization + deterministic marshaling.
type composeDoc map[string]any

// validateAndMarshal feeds the assembled doc through compose-go/v2 (the typed
// model: schema validation + consistency check + normalization) and returns the
// canonical, stable-key YAML. Interpolation and host-env resolution are skipped —
// devstack already resolved its own ${...} grammar, and valueless secret env keys
// must be preserved as-is (not resolved from the host). Path resolution is off so
// build contexts stay relative and the output is CWD-independent (deterministic).
func validateAndMarshal(doc composeDoc, workingDir string) ([]byte, error) {
	// Hand compose-go the raw bytes (not the map) so its project-name detection
	// never falls back to reading the not-yet-written file from disk. goccy's map
	// ordering is irrelevant — compose-go re-parses and canonicalizes the output.
	content, err := yaml.Marshal(map[string]any(doc))
	if err != nil {
		return nil, fmt.Errorf("pre-marshal compose doc: %w", err)
	}
	details := types.ConfigDetails{
		WorkingDir:  workingDir,
		ConfigFiles: []types.ConfigFile{{Filename: ComposeFile, Content: content}},
		Environment: types.Mapping{},
	}
	proj, err := loader.LoadWithContext(context.Background(), details, func(o *loader.Options) {
		o.SkipInterpolation = true
		o.SkipResolveEnvironment = true
		o.ResolvePaths = false
		o.SkipConsistencyCheck = false
	})
	if err != nil {
		return nil, fmt.Errorf("compose validation failed: %w", err)
	}
	out, err := proj.MarshalYAML()
	if err != nil {
		return nil, fmt.Errorf("compose marshal failed: %w", err)
	}
	return out, nil
}

// serviceBuilder assembles one service's compose map from its resolved template
// fragment plus the project config overlay (env, ports, networks, labels).
type serviceBuilder struct {
	res        *graphResolver
	wsName     string
	stack      string // compose project name (devstack-<project> or devstack-shared)
	project    string // config project name ("" for shared)
	service    string // service / shared name
	contextDir string // per-service build context, relative to the stack's .devstack dir
}

// buildProjectService renders one project service into a compose service map and
// returns it together with any build-context files that must be written.
func buildProjectService(res *graphResolver, m *config.Model, project, service string, svc config.Service, resolved *template.Resolved) (map[string]any, error) {
	res.curProject = project
	res.curService = service

	out, ok := mapClone(resolved.Service)
	if !ok {
		return nil, fmt.Errorf("project %q service %q: template %q produced no service fragment", project, service, svc.Template)
	}

	b := &serviceBuilder{
		res: res, wsName: m.Workspace.Name, stack: projectStackName(project),
		project: project, service: service, contextDir: service + "/build",
	}
	rewriteBuild(out, b.contextDir)

	env, err := b.projectEnv(svc, out)
	if err != nil {
		return nil, err
	}
	if len(env) > 0 {
		out["environment"] = env
	} else {
		delete(out, "environment")
	}

	out["networks"] = map[string]any{"default": nil, SharedNetwork: nil}
	svcLabels := map[string]string{LabelProject: project, LabelService: service}
	// spec 05 — when a reverse proxy is configured, emit the caddy-docker-proxy
	// route labels onto the service so adding/removing it reloads Caddy with no
	// central-config edit. No-op (nil) when the proxy is disabled.
	maps.Copy(svcLabels, proxy.LabelsForService(m, project, service))
	out["labels"] = b.labels(svcLabels)

	if exp := exposeList(svc.Ports); len(exp) > 0 {
		out["expose"] = exp
	}

	// spec 10 — a service-declared healthcheck overrides any template default and
	// is lowered to a Compose-native healthcheck: block.
	if svc.Healthcheck != nil {
		hc, err := healthcheckBlock(svc.Healthcheck)
		if err != nil {
			return nil, fmt.Errorf("project %q service %q healthcheck: %w", project, service, err)
		}
		out["healthcheck"] = hc
	}
	// spec 10 — intra-project dependsOn → compose depends_on (cross-project edges
	// are gated tool-side by the up saga, not expressible in compose).
	dep, err := dependsOnBlock(m, project, svc.DependsOn)
	if err != nil {
		return nil, err
	}
	if len(dep) > 0 {
		out["depends_on"] = dep
	}

	// NOTE: service-level compose `profiles:` are deliberately NOT emitted in M1.
	// Compose disables a profiled service unless its profile is active, which would
	// drop it from the generated document and from a plain `up`. Profile membership
	// stays declared in devstack.yaml; the selective `up --profile` graph walk that
	// emits/activates them is owned by spec 12 / M6.
	return out, nil
}

// buildSharedService renders one shared-stack service (postgres/redis/minio).
func buildSharedService(m *config.Model, name string, resolved *template.Resolved) (map[string]any, error) {
	out, ok := mapClone(resolved.Service)
	if !ok {
		return nil, fmt.Errorf("shared service %q: template produced no service fragment", name)
	}
	// Shared services are image-based engines (spec 03). A build context would not
	// be staged into the shared stack, so reject it explicitly rather than emit a
	// compose file whose build path points nowhere.
	if _, hasBuild := out["build"]; hasBuild {
		return nil, fmt.Errorf("shared service %q (template %q): shared services must be image-based; build contexts are not supported", name, resolved.Name)
	}
	b := &serviceBuilder{wsName: m.Workspace.Name, stack: SharedStackName, service: name}
	// Shared services join only the shared network, reached by their stable alias.
	out["networks"] = map[string]any{
		SharedNetwork: map[string]any{"aliases": []any{sharedAlias(name)}},
	}
	out["labels"] = b.labels(map[string]string{LabelShared: name})
	return out, nil
}

// projectEnv computes the final environment map for a project service: the
// template's own env, then env.raw, then env.prefixed, then env.import. Secret
// import attrs become valueless keys (nil) — the §7.5 coupling. All values are
// stringified so the output is uniform and deterministic.
func (b *serviceBuilder) projectEnv(svc config.Service, fragment map[string]any) (map[string]any, error) {
	// 1. Template-provided environment (already rendered), in either map or list
	// (["KEY=VALUE"]) form — both are valid compose and must not be dropped.
	env := envFromFragment(fragment["environment"])
	// 2. env.raw — interpolated, emitted verbatim. A secret:// value becomes a
	// VALUELESS key (the §7.5 coupling): its resolved value is injected at runtime
	// via the compose-up process env, never written to the generated file.
	for _, k := range sortedKeys(svc.Env.Raw) {
		if secrets.IsRef(svc.Env.Raw[k]) {
			env[k] = nil
			continue
		}
		val, err := config.Interpolate(svc.Env.Raw[k], b.res)
		if err != nil {
			return nil, fmt.Errorf("%s.%s env.raw[%s]: %w", b.project, b.service, k, err)
		}
		env[k] = val
	}
	// 3. env.prefixed — same, but namespaced under the service name.
	for _, k := range sortedKeys(svc.Env.Prefixed) {
		key := envPrefix(b.service) + "_" + k
		if secrets.IsRef(svc.Env.Prefixed[k]) {
			env[key] = nil
			continue
		}
		val, err := config.Interpolate(svc.Env.Prefixed[k], b.res)
		if err != nil {
			return nil, fmt.Errorf("%s.%s env.prefixed[%s]: %w", b.project, b.service, k, err)
		}
		env[key] = val
	}
	// 4. env.import — connection vars pulled from a shared service / another service.
	for i, imp := range svc.Env.Import {
		ref, ok := config.ParseRef(imp.From)
		if !ok {
			return nil, fmt.Errorf("%s.%s env.import[%d]: invalid reference %q", b.project, b.service, i, imp.From)
		}
		prefix := envPrefix(ref.Name)
		for _, v := range imp.Vars {
			key := prefix + "_" + envPrefix(v)
			if secretAttrs[lower(v)] {
				env[key] = nil // valueless — value supplied at runtime via process env (§7.5)
				continue
			}
			val, err := b.res.Ref(imp.From + "." + v)
			if err != nil {
				return nil, fmt.Errorf("%s.%s env.import[%d] %s: %w", b.project, b.service, i, v, err)
			}
			env[key] = val
		}
	}
	return env, nil
}

// envFromFragment normalizes a compose `environment:` fragment (map form or
// ["KEY=VALUE"]/["KEY"] list form) into a map of string-or-nil values. A bare
// "KEY" with no '=' becomes a valueless (nil) key, matching compose semantics.
func envFromFragment(v any) map[string]any {
	out := map[string]any{}
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			out[k] = scalarString(val)
		}
	case []any:
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				continue
			}
			if k, val, found := strings.Cut(s, "="); found {
				out[k] = val
			} else {
				out[s] = nil
			}
		}
	}
	return out
}

// labels merges the tool-owned label set with any extra per-service labels.
func (b *serviceBuilder) labels(extra map[string]string) map[string]any {
	out := map[string]any{
		LabelManaged:   "true",
		LabelWorkspace: b.wsName,
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// rewriteBuild points a service's build context at its per-service directory
// inside .devstack/, normalizing the short string form to the map form.
func rewriteBuild(svc map[string]any, contextDir string) {
	b, ok := svc["build"]
	if !ok {
		return
	}
	switch t := b.(type) {
	case string:
		svc["build"] = map[string]any{"context": contextDir, "dockerfile": "Dockerfile"}
	case map[string]any:
		t["context"] = contextDir
		if _, ok := t["dockerfile"]; !ok {
			t["dockerfile"] = "Dockerfile"
		}
	}
}

// exposeList turns a config ports map into a sorted compose `expose` list. v1
// publishes no host ports by default (DNS over the shared network); host-port
// allocation from the ledger lands in M2.
func exposeList(ports map[string]int) []any {
	if len(ports) == 0 {
		return nil
	}
	seen := map[int]bool{}
	nums := make([]int, 0, len(ports))
	for _, p := range ports {
		if !seen[p] {
			seen[p] = true
			nums = append(nums, p)
		}
	}
	sort.Ints(nums)
	out := make([]any, len(nums))
	for i, n := range nums {
		out[i] = n
	}
	return out
}
