package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
)

// dsNameRE is the safe-identifier pattern for workspace/project/service/alias
// names (lowercase, Compose/DNS-safe). Kept deliberately strict.
var dsNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// validate is the shared validator instance. koanf/goccy models are loaded once
// and treated immutable, so a package-level validator is safe.
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// dsname: Compose/DNS-safe identifier.
	_ = v.RegisterValidation("dsname", func(fl validator.FieldLevel) bool {
		return dsNameRE.MatchString(fl.Field().String())
	})
	// duration: a Compose-style Go duration string ("5s", "1m30s"). Paired with
	// `omitempty` so an unset field is allowed; only non-empty values are parsed.
	_ = v.RegisterValidation("duration", func(fl validator.FieldLevel) bool {
		_, err := time.ParseDuration(fl.Field().String())
		return err == nil
	})
	return v
}

// structValidate runs validator/v10, recovering from the panic it raises on a
// malformed tag (DECISIONS D16) so a tag bug never crashes the CLI.
func structValidate(v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal: config validation panicked (bad validator tag?): %v", r)
		}
	}()
	return validate.Struct(v)
}

// validateModel runs the two validation layers (spec 01): structural field
// rules via validator/v10, then the custom resolver — cross-reference existence
// (uses/import) against the WORKSPACE graph and cycle detection. Cross-ref
// errors are positioned to file:line:col.
func validateModel(m *Model, ws *source, projSrc map[string]*source) error {
	if err := structValidate(&m.Workspace); err != nil {
		return formatStructErr(ws.path, err)
	}
	for name, p := range m.Projects {
		if err := structValidate(&p); err != nil {
			return formatStructErr(projSrc[name].path, err)
		}
	}
	if err := validateRefs(m, projSrc); err != nil {
		return err
	}
	return detectCycles(m)
}

// formatStructErr renders validator.ValidationErrors as a file-scoped, sorted,
// one-per-line message (positions for structural errors are a later refinement;
// cross-ref errors below carry line:col).
func formatStructErr(file string, err error) error {
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return fmt.Errorf("%s: %w", file, err)
	}
	lines := make([]string, 0, len(ve))
	for _, fe := range ve {
		lines = append(lines, "  "+describeFieldError(fe))
	}
	sort.Strings(lines)
	return fmt.Errorf("%s: invalid configuration:\n%s", file, strings.Join(lines, "\n"))
}

func describeFieldError(fe validator.FieldError) string {
	field := fe.Namespace()
	switch fe.Tag() {
	case "required":
		return field + " is required"
	case "dsname":
		return fmt.Sprintf("%s = %q is not a valid name (lowercase letters/digits/-/_, starting with a letter)", field, fe.Value())
	case "eq":
		return fmt.Sprintf("%s = %q must equal %q", field, fe.Value(), fe.Param())
	case "oneof":
		return fmt.Sprintf("%s = %q must be one of: %s", field, fe.Value(), strings.ReplaceAll(fe.Param(), " ", ", "))
	case "duration":
		return fmt.Sprintf("%s = %q is not a valid duration (e.g. \"5s\", \"1m30s\")", field, fe.Value())
	case "min":
		return fmt.Sprintf("%s must have at least %s element(s)", field, fe.Param())
	default:
		return fmt.Sprintf("%s failed %q", field, fe.Tag())
	}
}

// validateRefs checks every `uses` and `import.from` resolves against the
// workspace graph, reporting unknown targets with file:line:col and a hint.
func validateRefs(m *Model, projSrc map[string]*source) error {
	for _, pname := range sortedKeys(m.Projects) {
		p := m.Projects[pname]
		src := projSrc[pname]
		for _, sname := range sortedKeys(p.Services) {
			svc := p.Services[sname]
			for i, u := range svc.Uses {
				r := parseRef(u)
				if r.kind != refShared || r.attr != "" {
					return src.errAt(fmt.Sprintf("$.services.%s.uses[%d]", sname, i),
						"uses: %q must be a shared service reference of the form workspace.shared.<name>", u)
				}
				if _, ok := m.Workspace.Shared[r.name]; !ok {
					return src.errAt(fmt.Sprintf("$.services.%s.uses[%d]", sname, i),
						"uses: shared service %q does not exist%s", r.name, suggest(r.name, m.SharedNames()))
				}
			}
			for j, imp := range svc.Env.Import {
				path := fmt.Sprintf("$.services.%s.env.import[%d].from", sname, j)
				if err := m.checkResolvable(src, path, imp.From); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkResolvable verifies a reference target (shared or another project's
// service) exists.
func (m *Model) checkResolvable(src *source, path, target string) error {
	r := parseRef(target)
	switch r.kind {
	case refShared:
		if _, ok := m.Workspace.Shared[r.name]; !ok {
			return src.errAt(path, "import: shared service %q does not exist%s", r.name, suggest(r.name, m.SharedNames()))
		}
	case refService:
		tp, ok := m.Projects[r.project]
		if !ok {
			return src.errAt(path, "import: project %q does not exist", r.project)
		}
		if _, ok := tp.Services[r.name]; !ok {
			return src.errAt(path, "import: service %q does not exist in project %q", r.name, r.project)
		}
	default:
		return src.errAt(path, "import: %q is not a valid reference (expected workspace.shared.<name> or workspace.<project>.<service>)", target)
	}
	return nil
}

// detectCycles finds dependency cycles among project services via their
// `import.from` edges (uses→shared are leaves). Reports the full cycle path
// rather than hanging or overflowing the stack.
func detectCycles(m *Model) error {
	// Build adjacency over project-service nodes.
	adj := map[string][]string{}
	for _, pname := range sortedKeys(m.Projects) {
		p := m.Projects[pname]
		for _, sname := range sortedKeys(p.Services) {
			from := nodeID(pname, sname)
			for _, imp := range p.Services[sname].Env.Import {
				r := parseRef(imp.From)
				if r.kind == refService {
					adj[from] = append(adj[from], nodeID(r.project, r.name))
				}
			}
		}
	}

	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := map[string]int{}
	var stack []string

	var dfs func(n string) []string
	dfs = func(n string) []string {
		color[n] = gray
		stack = append(stack, n)
		for _, w := range adj[n] {
			switch color[w] {
			case gray:
				// Back-edge: extract the cycle from the stack.
				cyc := []string{}
				for i := len(stack) - 1; i >= 0; i-- {
					cyc = append([]string{stack[i]}, cyc...)
					if stack[i] == w {
						break
					}
				}
				return append(cyc, w)
			case white:
				if c := dfs(w); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}

	for _, n := range sortedKeys(adj) {
		if color[n] == white {
			if cyc := dfs(n); cyc != nil {
				return fmt.Errorf("reference cycle detected: %s", strings.Join(cyc, " → "))
			}
		}
	}
	return nil
}

// suggest appends a "did you mean" hint when a near-miss candidate exists.
func suggest(got string, candidates []string) string {
	for _, c := range candidates {
		if strings.HasPrefix(c, got) || strings.HasPrefix(got, c) {
			return fmt.Sprintf(" (did you mean %q?)", c)
		}
	}
	if len(candidates) > 0 {
		sort.Strings(candidates)
		return fmt.Sprintf(" (available: %s)", strings.Join(candidates, ", "))
	}
	return ""
}

// sortedKeys returns the map keys sorted, for deterministic iteration/errors.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
