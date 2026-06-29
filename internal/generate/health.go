package generate

import (
	"fmt"
	"strconv"

	"github.com/open-source-cloud/devstack/internal/config"
)

// This file lowers spec-10 readiness config into the generated compose document:
// a service's `healthcheck:` block (the Compose-native, in-container probe) and
// its intra-project `dependsOn` → compose `depends_on: { dep: { condition } }`.
//
// CROSS-project edges (shared services, or another project's service) are NOT
// lowered here: compose `depends_on` cannot reference a service in a different
// compose project, so those are gated tool-side by the up saga's health poll
// (internal/health, spec 10 §two-enforcement-layers). The richer generate-time
// validation ("condition: healthy ⇒ the target declares a healthcheck", with
// file:line:col) is X2; here compose-go's own consistency check is the backstop.

// healthcheckBlock lowers a config.Healthcheck into a Compose-native healthcheck
// map. Timing fields are emitted only when set (compose applies its own defaults
// otherwise); the result is deterministic for a given input.
func healthcheckBlock(hc *config.Healthcheck) (map[string]any, error) {
	test, err := healthcheckTest(hc)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"test": test}
	if hc.Interval != "" {
		out["interval"] = hc.Interval
	}
	if hc.Timeout != "" {
		out["timeout"] = hc.Timeout
	}
	if hc.Retries > 0 {
		out["retries"] = hc.Retries
	}
	if hc.StartPeriod != "" {
		out["start_period"] = hc.StartPeriod
	}
	return out, nil
}

// healthcheckTest lowers a healthcheck kind to a compose `test` directive (spec
// 10 §kinds). The probe runs IN-CONTAINER (Compose owns it), so it relies on the
// image carrying the relevant client (curl / pg_isready / redis-cli / nc).
func healthcheckTest(hc *config.Healthcheck) ([]any, error) {
	switch hc.Kind {
	case "tcp":
		if hc.Port == 0 {
			return nil, fmt.Errorf("kind tcp requires a port")
		}
		return cmdShell(fmt.Sprintf("nc -z localhost %d", hc.Port)), nil
	case "http", "https":
		host := hc.Host
		if host == "" {
			host = "localhost"
		}
		path := hc.Path
		if path == "" {
			path = "/"
		}
		url := hc.Kind + "://" + host
		if hc.Port != 0 {
			url = fmt.Sprintf("%s://%s:%d", hc.Kind, host, hc.Port)
		}
		url += path
		args := []any{"CMD", "curl", "-fsS"}
		if hc.Kind == "https" {
			args = append(args, "-k") // local CA: skip-verify (spec 10)
		}
		args = append(args, "-o", "/dev/null", url)
		return args, nil
	case "exec":
		if len(hc.Command) == 0 {
			return nil, fmt.Errorf("kind exec requires a command")
		}
		out := make([]any, 0, len(hc.Command)+1)
		out = append(out, "CMD")
		for _, c := range hc.Command {
			out = append(out, c)
		}
		return out, nil
	case "pg_isready":
		port := hc.Port
		if port == 0 {
			port = 5432
		}
		user := hc.User
		if user == "" {
			user = "postgres"
		}
		cmd := fmt.Sprintf("pg_isready -p %d -U %s", port, user)
		if hc.DB != "" {
			cmd += " -d " + hc.DB
		}
		return cmdShell(cmd), nil
	case "redis":
		port := hc.Port
		if port == 0 {
			port = 6379
		}
		cmd := "redis-cli -p " + strconv.Itoa(port)
		// A secret:// auth ref must never be written to the generated file (§7.5):
		// redis-cli reads $REDISCLI_AUTH from the container env, so we omit -a and
		// rely on that. Only a plain (already-committed) literal is embedded.
		if hc.Auth != "" && !isSecretRef(hc.Auth) {
			cmd += " -a " + hc.Auth
		}
		cmd += " PING"
		return cmdShell(cmd), nil
	default:
		// config validation restricts kind to the oneof set; defensive only.
		return nil, fmt.Errorf("unknown healthcheck kind %q", hc.Kind)
	}
}

func cmdShell(s string) []any { return []any{"CMD-SHELL", s} }

// isSecretRef reports whether a value is a secret:// reference (resolved later by
// internal/secrets, never embedded in a generated file).
func isSecretRef(s string) bool { return len(s) >= 9 && s[:9] == "secret://" }

// dependsOnBlock lowers a service's INTRA-project dependsOn edges to a compose
// `depends_on` map. Cross-project edges (shared, or another project) are skipped
// (the saga's tool-side poll handles them). An intra-project target that does not
// exist in the project is a generate error with context.
func dependsOnBlock(m *config.Model, project string, deps []config.DependsOn) (map[string]any, error) {
	if len(deps) == 0 {
		return nil, nil
	}
	proj := m.Projects[project]
	out := map[string]any{}
	for _, d := range deps {
		target, intra := intraProjectTarget(project, d.Service)
		if !intra {
			continue // cross-project: gated tool-side, not via compose
		}
		if _, ok := proj.Services[target]; !ok {
			return nil, fmt.Errorf("project %q: dependsOn target %q is not a service in this project", project, d.Service)
		}
		cond := "service_healthy"
		if d.Condition == "started" {
			cond = "service_started"
		}
		out[target] = map[string]any{"condition": cond}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// intraProjectTarget classifies a dependsOn target. It returns the bare service
// name and true when the target is in THIS compose project: a bare service name,
// or workspace.<project>.<service> with project == current. Shared refs and other
// projects' services return intra=false.
func intraProjectTarget(project, target string) (string, bool) {
	ref, ok := config.ParseRef(target)
	if !ok {
		// Not a dotted reference → a bare intra-project service name.
		return target, true
	}
	if ref.Kind == config.RefService && ref.Project == project {
		return ref.Name, true
	}
	return "", false
}
