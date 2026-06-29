package generate

import (
	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/secrets"
)

// SecretRefs returns the compose env-key → secret:// reference for every
// secret-valued env entry across a project's services. The KEY here is byte-for-
// byte the valueless key projectEnv emits (env.raw verbatim; env.prefixed as
// <SERVICE>_<k>), so the up saga can inject `KEY=<resolved>` through the
// compose-up process env and have Compose substitute the valueless key (§7.5) —
// no secret value ever in a generated file.
func SecretRefs(m *config.Model, project string) map[string]string {
	out := map[string]string{}
	p, ok := m.Projects[project]
	if !ok {
		return out
	}
	for _, sname := range sortedKeys(p.Services) {
		svc := p.Services[sname]
		for k, v := range svc.Env.Raw {
			if secrets.IsRef(v) {
				out[k] = v
			}
		}
		for k, v := range svc.Env.Prefixed {
			if secrets.IsRef(v) {
				out[envPrefix(sname)+"_"+k] = v
			}
		}
	}
	return out
}
