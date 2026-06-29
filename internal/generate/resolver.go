package generate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/open-source-cloud/devstack/internal/config"
)

// secretAttrs are reference attributes whose value is a secret. They are emitted
// as VALUELESS per-service env keys — the value is supplied at runtime via the
// process env (secrets land in M4) and never written into a generated file
// (ARCHITECTURE §7.5). Inline ${ref:...secret} is rejected; secrets must flow
// through env.import so the coupling stays explicit.
var secretAttrs = map[string]bool{
	"password":  true,
	"secretkey": true,
	"secret":    true,
	"token":     true,
}

// graphResolver implements config.Resolver against the workspace service graph.
// The per-service context (curProject/curService) is set immediately before each
// service's env values are interpolated.
type graphResolver struct {
	model      *config.Model
	sharedPort map[string]int    // shared name → in-network port (from its template's defaultPort)
	env        map[string]string // host env (injectable for deterministic tests)
	profile    string

	curProject string
	curService string
}

func (r *graphResolver) Env(name string) (string, bool) {
	v, ok := r.env[name]
	return v, ok
}

func (r *graphResolver) Self(attr string) (string, bool) {
	switch strings.ToLower(attr) {
	case "host", "name", "service":
		// A service's in-project-network hostname is its compose service name.
		return r.curService, true
	case "project":
		return r.curProject, true
	}
	return "", false
}

func (r *graphResolver) Profile() string       { return r.profile }
func (r *graphResolver) WorkspaceName() string { return r.model.Workspace.Name }

func (r *graphResolver) Ref(path string) (string, error) {
	ref, ok := config.ParseRef(path)
	if !ok {
		return "", fmt.Errorf("invalid reference %q", path)
	}
	return r.refAttr(ref)
}

// refAttr resolves a parsed reference's attribute to a concrete value.
func (r *graphResolver) refAttr(ref config.Reference) (string, error) {
	attr := strings.ToLower(ref.Attr)
	if secretAttrs[attr] {
		return "", fmt.Errorf("reference attribute %q is a secret; consume it via env.import, not inline ${ref}", ref.Attr)
	}
	switch ref.Kind {
	case config.RefShared:
		return r.sharedAttr(ref.Name, attr)
	case config.RefService:
		return r.serviceAttr(ref.Project, ref.Name, attr)
	default:
		return "", fmt.Errorf("unresolvable reference")
	}
}

// sharedAttr resolves an attribute of a shared service. host/port are stable
// (DNS alias + the engine's default port); user/database default to the CONSUMER
// project name — the per-project role/db provisioned on the shared engine in M2.
func (r *graphResolver) sharedAttr(name, attr string) (string, error) {
	if _, ok := r.model.Workspace.Shared[name]; !ok {
		return "", fmt.Errorf("shared service %q does not exist%s", name, suggestShared(r.model))
	}
	switch attr {
	case "", "host":
		return sharedAlias(name), nil
	case "port":
		if p := r.sharedPort[name]; p != 0 {
			return strconv.Itoa(p), nil
		}
		return "", fmt.Errorf("shared service %q exposes no default port", name)
	case "user", "accesskey":
		return r.curProject, nil
	case "database", "db":
		return r.curProject, nil
	default:
		return "", fmt.Errorf("unknown attribute %q on shared service %q", attr, name)
	}
}

// serviceAttr resolves an attribute of another project's service.
func (r *graphResolver) serviceAttr(project, service, attr string) (string, error) {
	p, ok := r.model.Projects[project]
	if !ok {
		return "", fmt.Errorf("project %q does not exist", project)
	}
	if _, ok := p.Services[service]; !ok {
		return "", fmt.Errorf("service %q does not exist in project %q", service, project)
	}
	switch attr {
	case "", "host", "name":
		return service, nil
	default:
		return "", fmt.Errorf("unknown attribute %q on service %q (cross-project import resolves host only in v1)", attr, service)
	}
}

func suggestShared(m *config.Model) string {
	names := m.SharedNames()
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(" (available shared: %s)", strings.Join(names, ", "))
}
