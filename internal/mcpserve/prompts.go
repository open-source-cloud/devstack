package mcpserve

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptSpec is one guided workflow. Prompts are the cross-client equivalent of
// slash commands — in Claude Code they surface as /mcp__devstack__<name> — which
// makes them the "commands" surface for every MCP client, not just Claude Code.
type promptSpec struct {
	Name        string
	Title       string
	Description string
	Args        []*mcp.PromptArgument
	Text        func(d Deps, args map[string]string) string
}

func arg(name, desc string, required bool) *mcp.PromptArgument {
	return &mcp.PromptArgument{Name: name, Description: desc, Required: required}
}

// prompts is the registered set. Each one encodes a workflow whose correct order
// is not guessable — the failure mode they prevent is a model doing the right
// steps in the wrong order, or reaching for docker compose.
var prompts = []promptSpec{
	{
		Name:        "onboard-repo",
		Title:       "Onboard a repository into devstack",
		Description: "Add an existing repository to a devstack workspace and bring it up for the first time.",
		Args:        []*mcp.PromptArgument{arg("path", "path to the repository, relative to the workspace root", false)},
		Text: func(d Deps, a map[string]string) string {
			b := d.binary()
			path := a["path"]
			if path == "" {
				path = "<path>"
			}
			return join(
				"Onboard the repository at `"+path+"` into this devstack workspace.",
				"",
				"Follow this order and stop if a step fails:",
				"",
				"1. Read the current state: run `"+b+" context` and `"+b+" config show --json`.",
				"   If there is no workspace yet, run `"+b+" init` first.",
				"2. Inspect the repository to determine its stack (package.json, composer.json,",
				"   go.mod, requirements.txt) and which backing services it actually needs.",
				"3. List the available templates with `"+b+" template list --json` and pick the",
				"   closest match. Do not invent a template name.",
				"4. Register the project: `"+b+" project new <name> --path "+path+"`.",
				"5. Edit its `devstack.yaml` to declare the services, their `uses:` entries for",
				"   the shared engines, and any `env.import` blocks it needs. Never write a",
				"   docker-compose.yaml by hand — devstack generates those.",
				"6. Validate with `"+b+" config validate`, then `"+b+" generate --check`.",
				"7. Bring it up with `"+b+" up` and confirm with `"+b+" status`.",
				"",
				"If you need to know how a field behaves, read the docs rather than guessing:",
				"`"+b+" ai docs guide/projects` and `"+b+" ai docs guide/config-reference`.",
			)
		},
	},
	{
		Name:        "add-service",
		Title:       "Add a service to a project",
		Description: "Add a new container service to an existing devstack project, wired to the shared infrastructure.",
		Args: []*mcp.PromptArgument{
			arg("service", "what the service should be, e.g. a Redis-backed worker", true),
			arg("project", "which project to add it to; defaults to the active one", false),
		},
		Text: func(d Deps, a map[string]string) string {
			b := d.binary()
			return join(
				"Add this service to the devstack project: "+a["service"],
				projectLine(a["project"]),
				"",
				"1. Run `"+b+" config show --json` to see the project's existing services and",
				"   the shared engines the workspace provides.",
				"2. Run `"+b+" template list --json` and choose an existing template. Only author",
				"   a new one if nothing fits — and if so, read `"+b+" ai docs guide/templates` first.",
				"3. Add the service under `services:` in the project's `devstack.yaml`. Declare",
				"   `uses:` for each shared engine it needs, and `env.import` to pull connection",
				"   attributes rather than hard-coding a host or password.",
				"4. Remember: shared engines are reached by their DNS alias (`shared-postgres`),",
				"   never `localhost` and never the bare service name.",
				"5. `"+b+" config validate`, then `"+b+" generate`, then `"+b+" up`.",
				"6. Confirm with `"+b+" status`; if the service is unhealthy, `"+b+" logs <service>`.",
				"",
				"Do not edit anything under `.devstack/` — it is generated output.",
			)
		},
	},
	{
		Name:        "write-template",
		Title:       "Author a devstack service template",
		Description: "Write a new devstack service template — the template.yaml, its build/ tree and a golden fixture.",
		Args: []*mcp.PromptArgument{
			arg("name", "the template name, e.g. python.fastapi", true),
			arg("kind", "engine (shared infrastructure) or app (a project service)", false),
		},
		Text: func(d Deps, a map[string]string) string {
			b := d.binary()
			kind := a["kind"]
			if kind == "" {
				kind = "<engine|app>"
			}
			return join(
				"Author a devstack template named `"+a["name"]+"` of kind `"+kind+"`.",
				"",
				"Read these first — the rules are not guessable:",
				"- `"+b+" ai docs guide/templates` — the authoring guide.",
				"- `"+b+" ai docs specs/23` — the authoring spec and its lints.",
				"- The MCP resource `devstack://template/postgres` (an engine) or",
				"  `devstack://template/node.next` (an app) for a real, working example.",
				"",
				"The rules that will bite you:",
				"- An ENGINE uses `image:`, declares `provides:`/`exports:`/`defaultPort:`, and",
				"  must never have `build:`. An APP uses `build:` and must never declare",
				"  `provides:`.",
				"- Delimiters are `[[ ]]`, not `{{ }}`, so shell and Dockerfile `${VAR}` pass",
				"  through untouched. The only data in scope is `.params`.",
				"- Metadata keys are parsed UNRENDERED. A `[[ ]]` action in `description`,",
				"  `provides`, `exports` or `params` is a hard lint error. Only `service:` and",
				"  `volumes:` are rendered.",
				"- The FuncMap is deterministic: no now, no uuid, no randomness. Argument order",
				"  is pipeline-style, with the data last.",
				"- Deep-merge REPLACES lists; use `$merge: append` to add to a parent's list.",
				"",
				"Then follow the loop:",
				"1. `"+b+" template new "+a["name"]+" --kind "+kind+" --print-spec` and show the spec.",
				"2. `"+b+" template new "+a["name"]+" --kind "+kind+" --from <spec>` to materialize it.",
				"3. `"+b+" template lint <dir> --show` until clean.",
				"4. `"+b+" template test <dir>` against the golden fixture.",
			)
		},
	},
	{
		Name:        "debug-up-failure",
		Title:       "Diagnose a failing devstack up",
		Description: "Work through a failing or unhealthy devstack workspace methodically.",
		Args:        []*mcp.PromptArgument{arg("symptom", "what went wrong, in the user's words", false)},
		Text: func(d Deps, a map[string]string) string {
			b := d.binary()
			out := []string{"Diagnose this devstack workspace."}
			if s := a["symptom"]; s != "" {
				out = append(out, "", "Reported symptom: "+s)
			}
			return join(append(out,
				"",
				"Work in this order and report what each step showed:",
				"",
				"1. `"+b+" doctor --json` — is the host itself sound (Docker, compose, git, ports)?",
				"2. `"+b+" status --json` — which service is unhealthy, and what do the reference",
				"   counts look like?",
				"3. `"+b+" logs <service> --tail 200` — the actual error is almost always here.",
				"4. `"+b+" config validate` — configuration errors report file:line:col.",
				"5. `"+b+" generate --check` — are the generated artifacts stale?",
				"6. `"+b+" shared status` — are the shared engines up and correctly ref-counted?",
				"",
				"Escalate only as far as needed: `"+b+" doctor --fix`, then `"+b+" shared doctor`,",
				"then `"+b+" shared gc`. Do NOT run `"+b+" workspace destroy` or any other",
				"destructive verb without asking the user first.",
				"",
				"Never work around a problem with `docker compose` or by editing `.devstack/`:",
				"that forks a parallel stack and the edit is overwritten on the next generate.",
				"",
				"`"+b+" ai docs guide/recovery` has the full triage table.",
			)...)
		},
	},
	{
		Name:        "migrate-from-compose",
		Title:       "Migrate a docker-compose project to devstack",
		Description: "Convert an existing docker-compose.yaml into devstack's two-file model, moving backing services to the shared stack.",
		Args:        []*mcp.PromptArgument{arg("path", "path to the existing docker-compose.yaml", false)},
		Text: func(d Deps, a map[string]string) string {
			b := d.binary()
			path := a["path"]
			if path == "" {
				path = "the existing docker-compose.yaml"
			}
			return join(
				"Migrate "+path+" onto devstack.",
				"",
				"1. Read the compose file and classify every service into two buckets:",
				"   - BACKING SERVICES (postgres, mysql, redis, minio, kafka, nats, rabbitmq).",
				"     These become SHARED — one instance for the whole workspace, declared once",
				"     under `shared:` in workspace.yaml. Do not give each project its own.",
				"   - APPLICATION SERVICES. These stay per-project, under `services:` in the",
				"     repo's devstack.yaml.",
				"2. `"+b+" template list --json` — map each service to a template. Backing",
				"   services almost always have one already.",
				"3. Try `"+b+" import <path>` first; it does the mechanical conversion.",
				"4. Translate connection settings: a service reaches the shared Postgres at the",
				"   DNS alias `shared-postgres`, not `localhost` and not `db`. Prefer",
				"   `env.import` from the shared service over hard-coded values.",
				"5. Any per-project database, user or bucket becomes a `resources:` entry, so",
				"   `up` provisions it idempotently.",
				"6. `"+b+" config validate`, `"+b+" generate`, `"+b+" up`, `"+b+" status`.",
				"7. Once it works, the old docker-compose.yaml can go. Explain to the user that",
				"   `.devstack/` is now generated and must not be edited by hand.",
				"",
				"`"+b+" ai docs migration` covers the whole path in detail.",
			)
		},
	},
}

// registerPrompts installs every prompt.
func registerPrompts(s *mcp.Server, d Deps) {
	for _, p := range prompts {
		s.AddPrompt(&mcp.Prompt{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Arguments:   p.Args,
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			spec, ok := promptByName(req.Params.Name)
			if !ok {
				return nil, fmt.Errorf("unknown prompt %q", req.Params.Name)
			}
			for _, a := range spec.Args {
				if a.Required && req.Params.Arguments[a.Name] == "" {
					return nil, fmt.Errorf("prompt %q requires the %q argument", spec.Name, a.Name)
				}
			}
			return &mcp.GetPromptResult{
				Description: spec.Description,
				Messages: []*mcp.PromptMessage{{
					Role:    "user",
					Content: &mcp.TextContent{Text: spec.Text(d, req.Params.Arguments)},
				}},
			}, nil
		})
	}
}

func promptByName(name string) (promptSpec, bool) {
	for _, p := range prompts {
		if p.Name == name {
			return p, true
		}
	}
	return promptSpec{}, false
}

// PromptNames returns the registered prompt names, for tests and for the CLI's
// startup summary.
func PromptNames() []string {
	out := make([]string, 0, len(prompts))
	for _, p := range prompts {
		out = append(out, p.Name)
	}
	return out
}

func join(lines ...string) string { return strings.Join(lines, "\n") }

func projectLine(project string) string {
	if project == "" {
		return "Target the active project."
	}
	return "Target the project `" + project + "`."
}
