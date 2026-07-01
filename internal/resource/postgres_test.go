package resource

import (
	"context"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/provision"
)

// mockConn records the SQL a provisioner runs and answers Exists from a set. It
// mirrors the fakePgConn model in orchestrate/provision tests.
type mockConn struct {
	execs  []string
	args   [][]any
	exists map[string]bool // guard-query substring → present
}

func (c *mockConn) Exec(_ context.Context, sql string, args ...any) error {
	c.execs = append(c.execs, sql)
	c.args = append(c.args, args)
	return nil
}

func (c *mockConn) Exists(_ context.Context, sql string, _ ...any) (bool, error) {
	for sub, ok := range c.exists {
		if strings.Contains(sql, sub) {
			return ok, nil
		}
	}
	return false, nil
}

func mockConnector(c *mockConn) PgConnector {
	return func(context.Context, string) (provision.Conn, func() error, error) {
		return c, func() error { return nil }, nil
	}
}

func target() Target {
	return Target{
		Instance: "postgres", Host: "127.0.0.1", Port: 45432,
		AdminEnv: map[string]string{"user": "devstack", "password": "devstack"},
	}
}

func TestPostgresEngineAndKinds(t *testing.T) {
	p := Postgres{}
	if p.Engine() != "postgres" {
		t.Errorf("Engine() = %q, want postgres", p.Engine())
	}
	if got := p.Kinds(); len(got) == 0 || got[0] != "database" {
		t.Errorf("Kinds() = %v, want database first", got)
	}
}

func TestPostgresEnsureCreatePath(t *testing.T) {
	tests := []struct {
		name     string
		resource Resource
		wantPass string // password baked into ALTER/CREATE ROLE literal
		wantDB   string
	}{
		{
			name:     "predictable default name",
			resource: Resource{Engine: "postgres", Kind: "database", Owner: "acme", CredKind: CredPredictable},
			wantPass: "acme",
			wantDB:   "acme",
		},
		{
			name:     "hyphenated project sanitized",
			resource: Resource{Engine: "postgres", Kind: "database", Name: "my-app", Owner: "my-app", CredKind: CredPredictable},
			wantPass: "my-app",
			wantDB:   "my_app",
		},
		{
			name: "generated credential via params",
			resource: Resource{Engine: "postgres", Kind: "database", Owner: "acme", CredKind: CredGenerated,
				Params: map[string]any{"password": "s3cr3t-random"}},
			wantPass: "s3cr3t-random",
			wantDB:   "acme",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &mockConn{} // Exists → false: the create path runs
			p := Postgres{Connect: mockConnector(c)}
			attrs, err := p.Ensure(context.Background(), target(), tc.resource)
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			joined := strings.Join(c.execs, " | ")
			if !strings.Contains(joined, "CREATE ROLE") || !strings.Contains(joined, "CREATE DATABASE") {
				t.Errorf("missing role/db creation: %s", joined)
			}
			if !strings.Contains(joined, "'"+tc.wantPass+"'") {
				t.Errorf("password literal %q not in SQL: %s", tc.wantPass, joined)
			}
			if attrs["database"] != tc.wantDB {
				t.Errorf("database attr = %q, want %q", attrs["database"], tc.wantDB)
			}
			if attrs["host"] != "shared-postgres" {
				t.Errorf("host attr = %q, want shared-postgres", attrs["host"])
			}
			if attrs["password"] != tc.wantPass {
				t.Errorf("password attr = %q, want %q", attrs["password"], tc.wantPass)
			}
		})
	}
}

func TestPostgresEnsureIdempotentAlterPath(t *testing.T) {
	// Role + database already exist → EnsureProject takes the ALTER (no CREATE)
	// path, proving existence-guarded idempotency (D8).
	c := &mockConn{exists: map[string]bool{"pg_roles": true, "pg_database": true}}
	p := Postgres{Connect: mockConnector(c)}
	if _, err := p.Ensure(context.Background(), target(),
		Resource{Engine: "postgres", Kind: "database", Owner: "acme"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if strings.Contains(joined, "CREATE ROLE") || strings.Contains(joined, "CREATE DATABASE") {
		t.Errorf("existing role/db must not be re-created: %s", joined)
	}
	if !strings.Contains(joined, "ALTER ROLE") {
		t.Errorf("expected ALTER ROLE to keep password in sync: %s", joined)
	}
}

func TestPostgresDropDatabase(t *testing.T) {
	c := &mockConn{}
	p := Postgres{Connect: mockConnector(c)}
	if err := p.Drop(context.Background(), target(),
		Resource{Engine: "postgres", Kind: "database", Name: "my-app", Owner: "my-app"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if !strings.Contains(joined, "pg_terminate_backend") {
		t.Errorf("Drop must terminate the tenant's sessions first: %s", joined)
	}
	if !strings.Contains(joined, `DROP DATABASE IF EXISTS "my_app"`) {
		t.Errorf("Drop must drop the sanitized database: %s", joined)
	}
	if !strings.Contains(joined, `DROP ROLE IF EXISTS "my_app"`) {
		t.Errorf("Drop must drop the owning role: %s", joined)
	}
}

func TestPostgresDropRoleOnly(t *testing.T) {
	c := &mockConn{}
	p := Postgres{Connect: mockConnector(c)}
	if err := p.Drop(context.Background(), target(),
		Resource{Engine: "postgres", Kind: "role", Name: "acme", Owner: "acme"}); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if strings.Contains(joined, "DROP DATABASE") {
		t.Errorf("role-kind Drop must not drop a database: %s", joined)
	}
	if !strings.Contains(joined, `DROP ROLE IF EXISTS "acme"`) {
		t.Errorf("role-kind Drop must drop the role: %s", joined)
	}
}

func TestRegistryAndKinds(t *testing.T) {
	reg := NewRegistry(Postgres{})
	if _, ok := reg.For("postgres"); !ok {
		t.Error("postgres provisioner not registered")
	}
	if _, ok := reg.For("minio"); ok {
		t.Error("minio should not be registered in the default set")
	}
	if !SupportsKind("postgres", "database") {
		t.Error("postgres should support database")
	}
	if SupportsKind("postgres", "bucket") {
		t.Error("postgres must not support bucket")
	}
	if !SupportsKind("customengine", "anything") {
		t.Error("unknown engine must be forward-tolerant (kind check skipped)")
	}
}
