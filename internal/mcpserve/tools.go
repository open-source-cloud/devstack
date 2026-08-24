package mcpserve

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolClass decides whether a tool is registered, and with what annotations.
type toolClass int

const (
	// classRead cannot change anything. Always registered.
	classRead toolClass = iota
	// classWrite mutates the workspace but is recoverable — bring a stack up,
	// regenerate artifacts, create a database. Registered by default.
	classWrite
	// classDestructive is irreversible: it deletes data or tears a workspace
	// down. ABSENT unless --allow-destructive, because MCP has no TTY and a
	// mutating tool must inject --yes, which would leave nothing between a model
	// and permanent data loss.
	classDestructive
)

// A tool that is never registered at any setting. The secrets group would put
// provider material into a model's context; `aws --` and `shell` are arbitrary
// command execution wearing a devstack hat, which the agent's own shell already
// provides with a per-command permission prompt.
var neverRegistered = []string{"secrets", "aws", "shell"}

// Argument shapes. A small set covers the whole surface, and the SDK infers each
// tool's input schema from the struct, so there is no hand-written JSON Schema to
// drift.

type noArgs struct{}

type projectArgs struct {
	Project string `json:"project,omitempty" jsonschema:"limit the operation to one project by name"`
}

type nameProjectArgs struct {
	Name    string `json:"name" jsonschema:"the resource name to create"`
	Project string `json:"project,omitempty" jsonschema:"the owning project; defaults to the active one"`
}

type serviceArgs struct {
	Service string `json:"service,omitempty" jsonschema:"a single service name; omit for all services"`
	Project string `json:"project,omitempty" jsonschema:"the owning project; defaults to the active one"`
	Tail    int    `json:"tail,omitempty" jsonschema:"how many trailing log lines to return (default 200)"`
}

type pathArgs struct {
	Path string `json:"path" jsonschema:"a filesystem path to a template directory"`
}

type taskArgs struct {
	Task    string `json:"task" jsonschema:"the task name from the project's tasks: block"`
	Project string `json:"project,omitempty" jsonschema:"the owning project; defaults to the active one"`
}

type docsArgs struct {
	Slug   string `json:"slug,omitempty" jsonschema:"a document slug such as guide/templates, or a spec number such as 23"`
	Search string `json:"search,omitempty" jsonschema:"search the corpus instead of reading one document"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum search results (default 10)"`
}

type kindArgs struct {
	Kind string `json:"kind,omitempty" jsonschema:"which config file to describe: project or workspace"`
}

// result is the uniform output shape: the command's stdout, plus the argv so a
// model can see (and tell the user) exactly what ran.
type result struct {
	Command string `json:"command" jsonschema:"the devstack command line that produced this"`
	Output  string `json:"output" jsonschema:"the command's JSON output"`
}

// allows reports whether a class is registered under the given options.
func allows(c toolClass, opts Options) bool {
	switch c {
	case classRead:
		return true
	case classWrite:
		return !opts.ReadOnly
	case classDestructive:
		return !opts.ReadOnly && opts.AllowDestructive
	}
	return false
}

// registrar decides what gets registered and remembers what did, so the tool set
// can be reported without asking the SDK to enumerate it.
type registrar struct {
	opts       Options
	registered []string
}

func (r *registrar) admit(name string, c toolClass) bool {
	if !allows(c, r.opts) {
		return false
	}
	r.registered = append(r.registered, name)
	return true
}

// registerTools installs the tool set selected by opts and returns the names it
// registered, in registration order.
func registerTools(s *mcp.Server, d Deps, opts Options) []string {
	reg := &registrar{opts: opts}

	// --- read ---------------------------------------------------------------

	addCmd(s, d, reg, classRead, false, "devstack_status",
		"Service health, the last saga outcome and the shared-service reference graph for the current workspace. Start here when asked what is running.",
		func(noArgs) []string { return []string{"status"} })

	addCmd(s, d, reg, classRead, false, "devstack_context",
		"The active workspace, project, role, Docker context and version. The cheapest way to find out where you are.",
		func(noArgs) []string { return []string{"context"} })

	addCmd(s, d, reg, classRead, false, "devstack_config_show",
		"The resolved workspace configuration: every project, its services and the shared engines they consume.",
		func(noArgs) []string { return []string{"config", "show"} })

	addCmd(s, d, reg, classRead, false, "devstack_config_validate",
		"Validate workspace.yaml and every devstack.yaml, including cross-references and cycles. Errors report the exact file:line:col.",
		func(noArgs) []string { return []string{"config", "validate"} })

	addCmd(s, d, reg, classRead, false, "devstack_config_schema",
		"The published JSON Schema for devstack.yaml or workspace.yaml — the exact field contract for authoring either file.",
		func(a kindArgs) []string {
			kind := a.Kind
			if kind == "" {
				kind = "project"
			}
			return []string{"config", "schema", "--kind", kind}
		})

	addCmd(s, d, reg, classRead, false, "devstack_doctor",
		"The host preflight matrix: Docker, compose, git, ports and paths. Run this first when a command fails for an unclear reason.",
		func(noArgs) []string { return []string{"doctor"} })

	addCmd(s, d, reg, classRead, false, "devstack_generate_check",
		"Report whether the generated compose and build artifacts are stale, without writing anything.",
		func(a projectArgs) []string { return withProject([]string{"generate", "--check"}, a.Project) })

	addCmd(s, d, reg, classRead, false, "devstack_template_list",
		"Every available service template with its metadata: what it provides, what it exports, its default port and its parameters. Read this before writing a template.",
		func(noArgs) []string { return []string{"template", "list"} })

	addCmd(s, d, reg, classRead, false, "devstack_template_lint",
		"Lint a template directory: the authoring lints plus compose-go validation of the rendered service.",
		func(a pathArgs) []string { return []string{"template", "lint", a.Path} })

	addCmd(s, d, reg, classRead, false, "devstack_shared_status",
		"The shared engines: which are running, their reference counts and which projects hold them.",
		func(noArgs) []string { return []string{"shared", "status"} })

	addCmd(s, d, reg, classRead, false, "devstack_ports",
		"Host ports currently published for shared services, with connection strings.",
		func(noArgs) []string { return []string{"ports"} })

	addCmd(s, d, reg, classRead, false, "devstack_project_list",
		"Every project registered in this workspace.",
		func(noArgs) []string { return []string{"project", "list"} })

	addCmd(s, d, reg, classRead, false, "devstack_env_list",
		"The local environment variables declared for a service.",
		func(a serviceArgs) []string {
			argv := []string{"env", "list"}
			argv = withProject(argv, a.Project)
			if a.Service != "" {
				argv = append(argv, "--service", a.Service)
			}
			return argv
		})

	addCmd(s, d, reg, classRead, false, "devstack_logs",
		"Recent logs across the project and shared stacks. Use this to find out WHY a service is unhealthy.",
		func(a serviceArgs) []string {
			argv := []string{"logs"}
			if a.Service != "" {
				argv = append(argv, a.Service)
			}
			tail := a.Tail
			if tail <= 0 {
				tail = 200
			}
			return append(withProject(argv, a.Project), "--tail", fmt.Sprint(tail))
		})

	addCmd(s, d, reg, classRead, false, "devstack_docs",
		"Read or search devstack's documentation, which is compiled into the binary. Prefer this over guessing how a feature behaves.",
		func(a docsArgs) []string {
			argv := []string{"ai", "docs"}
			switch {
			case a.Search != "":
				argv = append(argv, "--search", a.Search)
				if a.Limit > 0 {
					argv = append(argv, "--limit", fmt.Sprint(a.Limit))
				}
			case a.Slug != "":
				argv = append(argv, a.Slug)
			}
			return argv
		})

	addCmd(s, d, reg, classRead, false, "devstack_commands",
		"The whole devstack command surface as data: every path, its summary, argument spec and flags.",
		func(noArgs) []string { return []string{"ai", "commands"} })

	// --- write --------------------------------------------------------------

	addCmd(s, d, reg, classWrite, true, "devstack_up",
		"Bring the workspace up: ensure the shared network, start the shared engines, provision each project's isolated data, then compose up. Idempotent.",
		func(a projectArgs) []string { return withProjectArg([]string{"up"}, a.Project) })

	addCmd(s, d, reg, classWrite, true, "devstack_down",
		"Stop this workspace's project stacks and release their references. Data is preserved.",
		func(a projectArgs) []string { return withProjectArg([]string{"down"}, a.Project) })

	addCmd(s, d, reg, classWrite, true, "devstack_generate",
		"Re-render the compose and build artifacts from the current configuration and templates.",
		func(a projectArgs) []string { return withProject([]string{"generate"}, a.Project) })

	addCmd(s, d, reg, classWrite, false, "devstack_run",
		"Run a task from the project's tasks: graph, in dependency order.",
		func(a taskArgs) []string { return withProject([]string{"run", a.Task}, a.Project) })

	addCmd(s, d, reg, classWrite, true, "devstack_db_create",
		"Create a tenant database on the shared Postgres. Idempotent.",
		func(a nameProjectArgs) []string {
			return withProject([]string{"db", "create", a.Name}, a.Project)
		})

	addCmd(s, d, reg, classWrite, true, "devstack_s3_mb",
		"Create a tenant bucket on the shared object store. Idempotent.",
		func(a nameProjectArgs) []string {
			return withProject([]string{"s3", "mb", a.Name}, a.Project)
		})

	addCmd(s, d, reg, classWrite, false, "devstack_project_new",
		"Scaffold a devstack.yaml for a new project and register it in workspace.yaml.",
		func(a nameProjectArgs) []string { return []string{"project", "new", a.Name} })

	addCmd(s, d, reg, classWrite, true, "devstack_expose",
		"Publish the shared services on stable localhost ports so host tools can reach them.",
		func(noArgs) []string { return []string{"expose"} })

	addCmd(s, d, reg, classWrite, true, "devstack_ai_install",
		"Write or refresh the agent-integration files (skills, the AGENTS.md block, the MCP registration) in this repository.",
		func(noArgs) []string { return []string{"ai", "install"} })

	// --- destructive (absent unless explicitly allowed) ----------------------

	addCmd(s, d, reg, classDestructive, false, "devstack_db_drop",
		"Permanently drop a tenant database. This deletes data and cannot be undone.",
		func(a nameProjectArgs) []string {
			return withProject([]string{"db", "drop", a.Name, "--yes"}, a.Project)
		})

	addCmd(s, d, reg, classDestructive, false, "devstack_db_reset",
		"Drop and recreate a tenant database. This deletes data and cannot be undone.",
		func(a projectArgs) []string {
			return withProject([]string{"db", "reset", "--yes"}, a.Project)
		})

	addCmd(s, d, reg, classDestructive, false, "devstack_workspace_destroy",
		"Tear down this workspace's stacks and release its references and ports. Irreversible.",
		func(noArgs) []string { return []string{"workspace", "destroy", "--yes"} })

	return reg.registered
}

// addCmd registers one CLI-backed tool, if its class is allowed.
//
// The handler builds argv, prepends --json, and runs the ordinary command tree.
// Mutating tools additionally get --yes injected by their argv builder, because
// MCP has no TTY to confirm on — which is exactly why the irreversible verbs are
// gated behind a separate flag rather than merely annotated.
func addCmd[In any](
	s *mcp.Server,
	d Deps,
	reg *registrar,
	class toolClass,
	idempotent bool,
	name, description string,
	argv func(In) []string,
) {
	if !reg.admit(name, class) {
		return
	}
	closedWorld := false
	destructive := class == classDestructive
	tool := &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    class == classRead,
			DestructiveHint: &destructive,
			IdempotentHint:  idempotent,
			OpenWorldHint:   &closedWorld,
		},
	}
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, result, error) {
		args := append([]string{"--json"}, argv(in)...)
		out, err := d.Run(ctx, args)
		line := d.binary() + " " + strings.Join(args, " ")
		if err != nil {
			// Surface the failure to the model as tool content rather than a
			// protocol error: the command's own message is the useful part, and
			// devstack's errors carry the command, exit code and remediation.
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{
					Text: fmt.Sprintf("%s\n\n%v\n%s", line, err, out),
				}},
			}, result{}, nil
		}
		return nil, result{Command: line, Output: string(out)}, nil
	})
}

// withProject appends --project when a project was named.
func withProject(argv []string, project string) []string {
	if project != "" {
		return append(argv, "--project", project)
	}
	return argv
}

// withProjectArg appends a positional project name, which up/down take instead of
// a flag.
func withProjectArg(argv []string, project string) []string {
	if project != "" {
		return append(argv, project)
	}
	return argv
}

// ToolNames returns the registered tool names for a given option set, sorted. It
// exists so a test can pin the surface: adding or removing a tool should be a
// deliberate, test-breaking act rather than a silent change in what a model can
// do to someone's machine.
func ToolNames(d Deps, opts Options) ([]string, error) {
	built, err := Build(d, opts)
	if err != nil {
		return nil, err
	}
	names := append([]string{}, built.Tools...)
	sort.Strings(names)
	return names, nil
}

// NeverRegistered lists the command groups that are not exposed as tools at any
// setting.
func NeverRegistered() []string {
	out := make([]string, len(neverRegistered))
	copy(out, neverRegistered)
	return out
}
