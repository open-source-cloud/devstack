package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
)

func exposeFixture() []orchestrate.ExposedPort {
	return []orchestrate.ExposedPort{
		{Instance: "postgres", Engine: "postgres", Alias: "shared-postgres", Label: "postgres", Host: "127.0.0.1", Port: 55432, Container: 5432, Primary: true, URL: "postgres://devstack:devstack@127.0.0.1:55432/postgres?sslmode=disable"},
		{Instance: "minio", Engine: "minio", Alias: "shared-minio", Label: "console", Host: "127.0.0.1", Port: 59001, Container: 9001, Primary: false, URL: "http://127.0.0.1:59001"},
	}
}

func TestRenderExposed_Table(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := renderExposed(cmd, &GlobalOpts{}, exposeFixture()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"shared-postgres", "55432", "shared-minio (console)", "59001", "per-project database:"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestRenderExposed_JSON(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := renderExposed(cmd, &GlobalOpts{JSON: true}, exposeFixture()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "\"exposed\"") || !strings.Contains(out, "\"port\": 55432") {
		t.Errorf("json missing fields:\n%s", out)
	}
}

func TestRenderExposed_Quiet(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	if err := renderExposed(cmd, &GlobalOpts{Quiet: true}, exposeFixture()); err != nil {
		t.Fatal(err)
	}
	out := strings.TrimSpace(buf.String())
	// Quiet emits only the connection URLs, one per line.
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "postgres://") {
		t.Errorf("quiet should print only URLs, got:\n%s", out)
	}
}

// TestSharedExposeCommandsRegistered guards that `shared expose` and
// `shared ports` are wired into the shared command tree.
func TestSharedExposeCommandsRegistered(t *testing.T) {
	sh := newSharedCmd(&GlobalOpts{})
	have := map[string]bool{}
	for _, c := range sh.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"expose", "ports", "status", "gc", "doctor"} {
		if !have[want] {
			t.Errorf("shared subcommand %q not registered", want)
		}
	}
}
