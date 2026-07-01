package config

import (
	"strings"
	"testing"
)

const resWorkspace = `apiVersion: devstack/v1
kind: Workspace
name: acme
shared:
  postgres: { template: postgres }
  minio: { template: minio }
projects:
  - { name: api, path: api }
`

func projectWithResources(body string) map[string]string {
	return map[string]string{
		"workspace.yaml":    resWorkspace,
		"api/devstack.yaml": "apiVersion: devstack/v1\nkind: Project\nname: api\nservices:\n  api: { template: t }\n" + body,
	}
}

func TestResourcesValid(t *testing.T) {
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: database }
  - { uses: workspace.shared.minio, kind: bucket, name: api-uploads, credentials: generated }
  - { uses: workspace.shared.minio, kind: lifecycle, name: api-uploads, params: { expireDays: 7 } }
`))
	m, err := LoadAt(root)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	rs := m.Projects["api"].Resources
	if len(rs) != 3 {
		t.Fatalf("resources = %d, want 3", len(rs))
	}
	if rs[0].Kind != "database" || rs[0].Uses != "workspace.shared.postgres" {
		t.Errorf("resource[0] = %+v", rs[0])
	}
	if rs[1].Credentials != "generated" || rs[1].Name != "api-uploads" {
		t.Errorf("resource[1] = %+v", rs[1])
	}
}

func TestResourcesUnknownShared(t *testing.T) {
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.ghost, kind: database }
`))
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want an unknown-shared error naming ghost, got %v", err)
	}
}

func TestResourcesBadUsesForm(t *testing.T) {
	root := writeTree(t, projectWithResources(`resources:
  - { uses: postgres, kind: database }
`))
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "workspace.shared.<name>") {
		t.Fatalf("want a uses-form error, got %v", err)
	}
}

func TestResourcesKindNotSupportedByEngine(t *testing.T) {
	// A bucket on postgres is rejected (postgres provisioner lists no bucket kind).
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: bucket }
`))
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "not supported by engine") {
		t.Fatalf("want a kind/engine error, got %v", err)
	}
}

func TestResourcesDuplicateCollision(t *testing.T) {
	// Two databases defaulting to the project name collide on (engine, name).
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: database }
  - { uses: workspace.shared.postgres, kind: database }
`))
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "duplicate database resource") {
		t.Fatalf("want a duplicate-resource error, got %v", err)
	}
}

func TestResourcesBadCredentials(t *testing.T) {
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: database, credentials: bogus }
`))
	_, err := LoadAt(root)
	if err == nil || !strings.Contains(err.Error(), "predictable, generated") {
		t.Fatalf("want a credentials oneof error, got %v", err)
	}
}

func TestResourcesForwardTolerantUnknownKeys(t *testing.T) {
	// Unknown keys inside a resource entry are ignored, never fatal (additive block).
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: database, futureField: whatever }
`))
	if _, err := LoadAt(root); err != nil {
		t.Fatalf("unknown resource keys must be tolerated, got %v", err)
	}
}

func TestResourcesExplicitEngineOverride(t *testing.T) {
	// An explicit engine that supports the kind is accepted even if it differs from
	// the inferred template (forward-tolerance for custom shared templates).
	root := writeTree(t, projectWithResources(`resources:
  - { uses: workspace.shared.postgres, kind: bucket, engine: minio, name: api-uploads }
`))
	if _, err := LoadAt(root); err != nil {
		t.Fatalf("explicit valid engine override should pass, got %v", err)
	}
}
