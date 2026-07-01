package resource

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresEnsureUserWithGrant(t *testing.T) {
	c := &mockConn{}
	p := Postgres{Connect: mockConnector(c)}
	attrs, err := p.Ensure(context.Background(), target(), Resource{
		Engine: "postgres", Kind: "role", Name: "api_reports", Owner: "api",
		Params: map[string]any{"db": "api_orders", "level": "read"},
	})
	if err != nil {
		t.Fatalf("Ensure user: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if !strings.Contains(joined, "CREATE ROLE") {
		t.Errorf("user create must run CREATE ROLE: %s", joined)
	}
	if !strings.Contains(joined, "GRANT SELECT ON ALL TABLES") {
		t.Errorf("read grant missing: %s", joined)
	}
	if strings.Contains(joined, "CREATE DATABASE") {
		t.Errorf("user create must not create a database: %s", joined)
	}
	if attrs["role"] != "api_reports" || attrs["database"] != "api_orders" {
		t.Errorf("attrs = %v, want role api_reports on database api_orders", attrs)
	}
	// Predictable dev-cred for a role == the role name.
	if attrs["password"] != "api_reports" {
		t.Errorf("role password should default to the role name, got %q", attrs["password"])
	}
}

func TestPostgresGrantOnly(t *testing.T) {
	// grant_only: the role already exists; only the GRANT runs, no ALTER/CREATE.
	c := &mockConn{}
	p := Postgres{Connect: mockConnector(c)}
	_, err := p.Ensure(context.Background(), target(), Resource{
		Engine: "postgres", Kind: "role", Name: "api_reports", Owner: "api",
		Params: map[string]any{"db": "api_orders", "level": "write", "grant_only": "1"},
	})
	if err != nil {
		t.Fatalf("Ensure grant_only: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if strings.Contains(joined, "CREATE ROLE") || strings.Contains(joined, "ALTER ROLE") {
		t.Errorf("grant_only must not create/alter the role: %s", joined)
	}
	if !strings.Contains(joined, "INSERT, UPDATE, DELETE") {
		t.Errorf("write grant missing: %s", joined)
	}
}

func TestPostgresEnsureDatabaseWithOwner(t *testing.T) {
	c := &mockConn{}
	p := Postgres{Connect: mockConnector(c)}
	attrs, err := p.Ensure(context.Background(), target(), Resource{
		Engine: "postgres", Kind: "database", Name: "api_orders", Owner: "api",
		Params: map[string]any{"owner": "api"},
	})
	if err != nil {
		t.Fatalf("Ensure database with owner: %v", err)
	}
	joined := strings.Join(c.execs, " | ")
	if !strings.Contains(joined, `CREATE DATABASE "api_orders" OWNER "api"`) {
		t.Errorf("owner-based db create missing: %s", joined)
	}
	if strings.Contains(joined, "CREATE ROLE") {
		t.Errorf("owner-based db create must not create a role: %s", joined)
	}
	if _, hasRole := attrs["role"]; hasRole {
		t.Errorf("owner-based create must not surface a new role attr: %v", attrs)
	}
	if attrs["database"] != "api_orders" || attrs["owner"] != "api" {
		t.Errorf("attrs = %v, want database api_orders owner api", attrs)
	}
}
