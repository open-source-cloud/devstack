package mcpserve

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeRun records every command line a tool asked for and returns canned output,
// so the whole surface is exercised without Docker, a ledger or a subprocess.
type fakeRun struct {
	calls []string
	err   error
	out   string
}

func (f *fakeRun) run(_ context.Context, argv []string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(argv, " "))
	if f.err != nil {
		return []byte(f.out), f.err
	}
	if f.out == "" {
		return []byte(`{"ok":true}`), nil
	}
	return []byte(f.out), nil
}

type fakeDocs struct{}

func (fakeDocs) List() ([]DocMeta, error) {
	return []DocMeta{
		{Slug: "guide/templates", Path: "docs/guide/templates.md", Title: "Templates", Summary: "Authoring", Section: "guide", Lines: 249},
		{Slug: "architecture", Path: "docs/ARCHITECTURE.md", Title: "Architecture", Summary: "Design", Section: "root", Lines: 207},
	}, nil
}

func (fakeDocs) Read(slug string) (DocMeta, []byte, error) {
	list, _ := fakeDocs{}.List()
	for _, d := range list {
		if d.Slug == slug {
			return d, []byte("# " + d.Title + "\n\nbody\n"), nil
		}
	}
	return DocMeta{}, nil, errNotFound(slug)
}

type errNotFound string

func (e errNotFound) Error() string { return "no such doc " + string(e) }

func testDeps(run *fakeRun) Deps {
	return Deps{
		Version: "1.2.3",
		Binary:  "devstack",
		Run:     run.run,
		Docs:    fakeDocs{},
		Templates: func() fs.FS {
			return fstest.MapFS{
				"postgres/template.yaml": &fstest.MapFile{Data: []byte("provides: postgres\n")},
			}
		},
		Schema:      func(kind string) ([]byte, error) { return []byte(`{"title":"` + kind + `"}`), nil },
		SchemaKinds: func() []string { return []string{"project", "workspace"} },
	}
}

// connect wires a client to the server over the SDK's in-memory transports, so
// the protocol layer is genuinely exercised with no subprocess and no stdio.
func connect(t *testing.T, d Deps, opts Options) *mcp.ClientSession {
	t.Helper()
	built, err := Build(d, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx := context.Background()
	clientTr, serverTr := mcp.NewInMemoryTransports()

	go func() {
		ss, err := built.Server.Connect(ctx, serverTr, nil)
		if err != nil {
			return
		}
		_ = ss.Wait()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestDepsValidation(t *testing.T) {
	if _, err := New(Deps{}, Options{}); err == nil {
		t.Fatal("expected New to reject empty Deps")
	}
	run := &fakeRun{}
	d := testDeps(run)
	d.Docs = nil
	if _, err := New(d, Options{}); err == nil {
		t.Error("expected New to require Docs")
	}
}

// TestToolSetIsPinned makes changing what a model can do to someone's machine a
// deliberate, test-breaking act.
func TestToolSetIsPinned(t *testing.T) {
	run := &fakeRun{}

	readOnly, err := ToolNames(testDeps(run), Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	wantRead := []string{
		"devstack_commands", "devstack_config_schema", "devstack_config_show",
		"devstack_config_validate", "devstack_context", "devstack_docs",
		"devstack_doctor", "devstack_env_list", "devstack_generate_check",
		"devstack_logs", "devstack_ports", "devstack_project_list",
		"devstack_shared_status", "devstack_status", "devstack_template_lint",
		"devstack_template_list",
	}
	assertSameSet(t, "--read-only", readOnly, wantRead)

	def, err := ToolNames(testDeps(run), Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := append(append([]string{}, wantRead...),
		"devstack_ai_install", "devstack_db_create", "devstack_down",
		"devstack_expose", "devstack_generate", "devstack_project_new",
		"devstack_run", "devstack_s3_mb", "devstack_up",
	)
	assertSameSet(t, "default", def, wantDefault)

	all, err := ToolNames(testDeps(run), Options{AllowDestructive: true})
	if err != nil {
		t.Fatal(err)
	}
	wantAll := append(append([]string{}, wantDefault...),
		"devstack_db_drop", "devstack_db_reset", "devstack_workspace_destroy",
	)
	assertSameSet(t, "--allow-destructive", all, wantAll)
}

// TestDestructiveToolsAreAbsentByDefault is the safety property that matters
// most: absent, not merely annotated. MCP has no TTY, so an exposed destructive
// tool would run with --yes and no confirmation anywhere in the chain.
func TestDestructiveToolsAreAbsentByDefault(t *testing.T) {
	run := &fakeRun{}
	for _, opts := range []Options{{}, {ReadOnly: true}} {
		names, err := ToolNames(testDeps(run), opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			switch n {
			case "devstack_db_drop", "devstack_db_reset", "devstack_workspace_destroy":
				t.Errorf("%+v exposes the destructive tool %s", opts, n)
			}
		}
	}
}

// TestSecretsAreNeverExposed: no tool may reach the secrets group, or provider
// material lands in a model's context.
func TestSecretsAreNeverExposed(t *testing.T) {
	run := &fakeRun{}
	for _, opts := range []Options{{}, {ReadOnly: true}, {AllowDestructive: true}} {
		names, err := ToolNames(testDeps(run), opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			for _, banned := range NeverRegistered() {
				if strings.Contains(n, banned) {
					t.Errorf("%+v exposes %q, which touches the never-registered group %q", opts, n, banned)
				}
			}
		}
	}
}

func TestToolsListOverTheWire(t *testing.T) {
	run := &fakeRun{}
	cs := connect(t, testDeps(run), Options{})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) < 20 {
		t.Fatalf("expected the full tool set, got %d", len(res.Tools))
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
		if tool.Description == "" {
			t.Errorf("%s has no description; the model routes on it", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations; the host cannot gate it", tool.Name)
		}
	}
	if a := byName["devstack_status"].Annotations; a == nil || !a.ReadOnlyHint {
		t.Error("devstack_status must be annotated readOnlyHint")
	}
	if a := byName["devstack_up"].Annotations; a == nil || a.ReadOnlyHint {
		t.Error("devstack_up must not be annotated readOnlyHint")
	}
}

// TestToolCallRunsTheRightCommand is the heart of "the tools are the CLI": the
// handler must build the exact command line a human would type, with --json.
func TestToolCallRunsTheRightCommand(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"devstack_status", nil, "--json status"},
		{"devstack_context", nil, "--json context"},
		{"devstack_config_validate", nil, "--json config validate"},
		{"devstack_config_schema", map[string]any{"kind": "workspace"}, "--json config schema --kind workspace"},
		{"devstack_config_schema", nil, "--json config schema --kind project"},
		{"devstack_generate_check", map[string]any{"project": "api"}, "--json generate --check --project api"},
		{"devstack_logs", map[string]any{"service": "api", "tail": 50}, "--json logs api --tail 50"},
		{"devstack_logs", nil, "--json logs --tail 200"},
		{"devstack_docs", map[string]any{"slug": "guide/templates"}, "--json ai docs guide/templates"},
		{"devstack_docs", map[string]any{"search": "port"}, "--json ai docs --search port"},
		{"devstack_up", map[string]any{"project": "api"}, "--json up api"},
		{"devstack_db_create", map[string]any{"name": "shop", "project": "api"}, "--json db create shop --project api"},
	} {
		run := &fakeRun{}
		cs := connect(t, testDeps(run), Options{})
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: tc.tool, Arguments: tc.args,
		})
		if err != nil {
			t.Errorf("CallTool(%s): %v", tc.tool, err)
			continue
		}
		if len(run.calls) != 1 {
			t.Errorf("%s ran %d commands, want 1: %v", tc.tool, len(run.calls), run.calls)
			continue
		}
		if run.calls[0] != tc.want {
			t.Errorf("%s ran %q, want %q", tc.tool, run.calls[0], tc.want)
		}
	}
}

// TestDestructiveToolsInjectYes: MCP has no TTY, so an allowed destructive tool
// must pass --yes or it would hang forever waiting for a confirmation.
func TestDestructiveToolsInjectYes(t *testing.T) {
	run := &fakeRun{}
	cs := connect(t, testDeps(run), Options{AllowDestructive: true})
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "devstack_db_drop", Arguments: map[string]any{"name": "shop"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(run.calls) != 1 || !strings.Contains(run.calls[0], "--yes") {
		t.Errorf("a destructive tool must inject --yes, got %v", run.calls)
	}
}

// TestToolFailureIsReportedAsContent: a failing command should reach the model as
// readable tool output, not as a protocol error it cannot interpret.
func TestToolFailureIsReportedAsContent(t *testing.T) {
	run := &fakeRun{err: errNotFound("boom"), out: "partial"}
	cs := connect(t, testDeps(run), Options{})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "devstack_status"})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Error("expected IsError to be set")
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "boom") {
		t.Errorf("the failure message should reach the model, got: %q", text)
	}
}

func TestResources(t *testing.T) {
	run := &fakeRun{}
	cs := connect(t, testDeps(run), Options{})
	ctx := context.Background()

	res, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	got := map[string]bool{}
	for _, r := range res.Resources {
		got[r.URI] = true
	}
	for _, want := range []string{
		"devstack://docs/index",
		"devstack://docs/guide/templates",
		"devstack://schema/project.json",
		"devstack://schema/workspace.json",
		"devstack://commands.json",
	} {
		if !got[want] {
			t.Errorf("resource %s is not published", want)
		}
	}

	doc, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "devstack://docs/guide/templates"})
	if err != nil {
		t.Fatalf("ReadResource(docs): %v", err)
	}
	if len(doc.Contents) == 0 || !strings.Contains(doc.Contents[0].Text, "Templates") {
		t.Errorf("unexpected document content: %+v", doc.Contents)
	}

	// The resource TEMPLATE: a real, currently-shipping template source.
	tmpl, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "devstack://template/postgres"})
	if err != nil {
		t.Fatalf("ReadResource(template): %v", err)
	}
	if len(tmpl.Contents) == 0 || !strings.Contains(tmpl.Contents[0].Text, "provides: postgres") {
		t.Errorf("unexpected template content: %+v", tmpl.Contents)
	}

	// A traversal attempt must not escape the template source.
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "devstack://template/../../etc/passwd"}); err == nil {
		t.Error("expected a path-traversal template name to be rejected")
	}
}

func TestPrompts(t *testing.T) {
	run := &fakeRun{}
	cs := connect(t, testDeps(run), Options{})
	ctx := context.Background()

	list, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	got := map[string]bool{}
	for _, p := range list.Prompts {
		got[p.Name] = true
		if p.Description == "" {
			t.Errorf("prompt %s has no description", p.Name)
		}
	}
	for _, want := range PromptNames() {
		if !got[want] {
			t.Errorf("prompt %s is not published", want)
		}
	}

	res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "write-template",
		Arguments: map[string]string{"name": "python.fastapi", "kind": "app"},
	})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("expected a prompt message")
	}
	text := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"python.fastapi", "[[ ]]", "template lint", "UNRENDERED"} {
		if !strings.Contains(text, want) {
			t.Errorf("the write-template prompt should mention %q", want)
		}
	}

	// A required argument must be enforced.
	if _, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: "write-template"}); err == nil {
		t.Error("expected a missing required argument to be rejected")
	}
}

// TestPromptsWarnAgainstTheClassicMistakes: the prompts exist to stop a model
// reaching for docker compose or editing generated files.
func TestPromptsWarnAgainstTheClassicMistakes(t *testing.T) {
	run := &fakeRun{}
	d := testDeps(run)
	var all strings.Builder
	for _, p := range prompts {
		args := map[string]string{}
		for _, a := range p.Args {
			args[a.Name] = "x"
		}
		all.WriteString(p.Text(d, args))
	}
	body := all.String()
	for _, want := range []string{"docker-compose", ".devstack/", "shared-postgres"} {
		if !strings.Contains(body, want) {
			t.Errorf("no prompt warns about %q", want)
		}
	}
}

// TestPromptsFollowTheAliasedBinary keeps generated instructions correct for an
// installation invoked as rq or uranus.
func TestPromptsFollowTheAliasedBinary(t *testing.T) {
	run := &fakeRun{}
	d := testDeps(run)
	d.Binary = "rq"
	for _, p := range prompts {
		args := map[string]string{}
		for _, a := range p.Args {
			args[a.Name] = "x"
		}
		text := p.Text(d, args)
		if !strings.Contains(text, "`rq ") {
			t.Errorf("prompt %s issues no command as the invoked binary", p.Name)
		}
		// The product NAME may still appear as prose ("this devstack workspace");
		// what must not appear is a runnable command naming the wrong binary.
		if strings.Contains(text, "`devstack ") {
			t.Errorf("prompt %s hard-codes a `devstack ...` command while aliased", p.Name)
		}
	}
}

func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	for w := range wantSet {
		if !gotSet[w] {
			t.Errorf("%s: tool %q is missing", label, w)
		}
	}
	for g := range gotSet {
		if !wantSet[g] {
			t.Errorf("%s: unexpected tool %q — if this is intentional, update the pinned set", label, g)
		}
	}
}
