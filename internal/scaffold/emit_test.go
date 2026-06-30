package scaffold_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/scaffold"
)

func TestEmitWorkspaceYAML_Golden(t *testing.T) {
	cases := []struct {
		name string
		ws   config.Workspace
		want string
	}{
		{
			name: "name only (apiVersion/kind defaulted)",
			ws:   config.Workspace{Name: "demo"},
			want: "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n",
		},
		{
			name: "aliases and profile",
			ws: config.Workspace{
				Name: "demo", Aliases: []string{"rq", "uranus"},
				Profiles: config.Profiles{Default: "dev"},
			},
			want: "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n" +
				"aliases:\n- rq\n- uranus\n" +
				"profiles:\n  default: dev\n",
		},
		{
			// keys sorted (minio < postgres < redis); params kept as strings.
			name: "shared sorted with params",
			ws: config.Workspace{
				Name: "demo",
				Shared: map[string]config.SharedSvc{
					"redis":    {Template: "redis"},
					"postgres": {Template: "postgres", Params: map[string]any{"version": "16"}},
					"minio":    {Template: "minio"},
				},
			},
			want: "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n" +
				"shared:\n  minio:\n    template: minio\n" +
				"  postgres:\n    template: postgres\n    params:\n      version: \"16\"\n" +
				"  redis:\n    template: redis\n",
		},
		{
			// projects sorted by Name (api < web); git omitted when empty.
			name: "projects sorted, git optional",
			ws: config.Workspace{
				Name: "demo",
				Projects: []config.ProjectRef{
					{Name: "web", Path: "services/web"},
					{Name: "api", Path: "services/api", Git: "git@github.com:acme/api.git"},
				},
			},
			want: "apiVersion: devstack/v1\nkind: Workspace\nname: demo\n" +
				"projects:\n- name: api\n  path: services/api\n  git: git@github.com:acme/api.git\n" +
				"- name: web\n  path: services/web\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scaffold.EmitWorkspaceYAML(tc.ws)
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("emit mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, tc.want)
			}
			// Every golden must be structurally valid (the spec-22 pre-write check).
			if err := config.ValidateWorkspaceBytes(got); err != nil {
				t.Errorf("emitted golden does not validate: %v", err)
			}
			// Determinism: a second emit is byte-identical.
			if got2, _ := scaffold.EmitWorkspaceYAML(tc.ws); !bytes.Equal(got, got2) {
				t.Error("emit is not deterministic")
			}
		})
	}
}

// TestEmitWorkspaceYAML_IntNotFloat pins the goccy MapSlice hazard: a Go int
// param must render as `16`, never `16.0` (which would break determinism and
// re-parsing). Also doubles as the byte-shape guard for a future internal/migrate
// consolidation onto this emitter.
func TestEmitWorkspaceYAML_IntNotFloat(t *testing.T) {
	ws := config.Workspace{Name: "demo", Shared: map[string]config.SharedSvc{
		"pg": {Template: "postgres", Params: map[string]any{"n": 16}},
	}}
	got, err := scaffold.EmitWorkspaceYAML(ws)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "16.0") {
		t.Errorf("int param rendered as float:\n%s", got)
	}
	if !strings.Contains(string(got), "16") {
		t.Errorf("int param missing:\n%s", got)
	}
}
