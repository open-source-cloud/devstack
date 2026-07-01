package provision

import (
	"context"
	"strings"
	"testing"
)

func TestEnsureRoleCreatePath(t *testing.T) {
	m := &mockConn{existing: map[string]bool{}}
	id, err := Postgres{}.EnsureRole(context.Background(), m, "api-reports", "pw123")
	if err != nil {
		t.Fatal(err)
	}
	if id != "api_reports" {
		t.Fatalf("role ident = %q, want api_reports (hyphen sanitized)", id)
	}
	sql := m.joined()
	if !strings.Contains(sql, `CREATE ROLE "api_reports" WITH LOGIN PASSWORD 'pw123'`) {
		t.Fatalf("missing guarded CREATE ROLE: %s", sql)
	}
	if strings.Contains(sql, "ALTER ROLE") {
		t.Fatalf("fresh role must not ALTER: %s", sql)
	}
}

func TestEnsureRoleIdempotentAlter(t *testing.T) {
	m := &mockConn{existing: map[string]bool{"api_reports": true}}
	if _, err := (Postgres{}).EnsureRole(context.Background(), m, "api_reports", "newpw"); err != nil {
		t.Fatal(err)
	}
	sql := m.joined()
	if strings.Contains(sql, "CREATE ROLE") {
		t.Fatalf("existing role must not be re-created: %s", sql)
	}
	if !strings.Contains(sql, `ALTER ROLE "api_reports" WITH LOGIN PASSWORD 'newpw'`) {
		t.Fatalf("existing role must have password synced: %s", sql)
	}
}

func TestEnsureDatabaseGuarded(t *testing.T) {
	// Fresh database → guarded CREATE with the owner; no new role.
	m := &mockConn{existing: map[string]bool{}}
	id, err := Postgres{}.EnsureDatabase(context.Background(), m, "api-orders", "api")
	if err != nil {
		t.Fatal(err)
	}
	if id != "api_orders" {
		t.Fatalf("db ident = %q, want api_orders", id)
	}
	sql := m.joined()
	if !strings.Contains(sql, `CREATE DATABASE "api_orders" OWNER "api"`) {
		t.Fatalf("missing CREATE DATABASE ... OWNER: %s", sql)
	}
	if strings.Contains(sql, "CREATE ROLE") {
		t.Fatalf("db create must not create a role: %s", sql)
	}

	// Existing database → no CREATE (idempotent).
	m2 := &mockConn{existing: map[string]bool{"api_orders": true}}
	if _, err := (Postgres{}).EnsureDatabase(context.Background(), m2, "api_orders", "api"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m2.joined(), "CREATE DATABASE") {
		t.Fatalf("existing db must not be re-created: %s", m2.joined())
	}
}

func TestGrantLevels(t *testing.T) {
	tests := []struct {
		level    GrantLevel
		wantAny  []string
		wantNone []string
	}{
		{GrantRead, []string{"GRANT CONNECT ON DATABASE", "GRANT SELECT ON ALL TABLES"}, []string{"INSERT", "GRANT ALL"}},
		{GrantWrite, []string{"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES"}, []string{"GRANT ALL PRIVILEGES ON DATABASE"}},
		{GrantAdmin, []string{"GRANT ALL PRIVILEGES ON DATABASE", "GRANT ALL ON SCHEMA public"}, []string{"GRANT SELECT ON ALL TABLES"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.level), func(t *testing.T) {
			m := &mockConn{}
			if err := (Postgres{}).Grant(context.Background(), m, "api-reports", "api-orders", tc.level); err != nil {
				t.Fatal(err)
			}
			sql := m.joined()
			if !strings.Contains(sql, `TO "api_reports"`) || !strings.Contains(sql, `DATABASE "api_orders"`) {
				t.Fatalf("grant idents not sanitized/quoted: %s", sql)
			}
			for _, w := range tc.wantAny {
				if !strings.Contains(sql, w) {
					t.Errorf("%s grant missing %q: %s", tc.level, w, sql)
				}
			}
			for _, n := range tc.wantNone {
				if strings.Contains(sql, n) {
					t.Errorf("%s grant should not contain %q: %s", tc.level, n, sql)
				}
			}
		})
	}
}

func TestGrantUnknownLevel(t *testing.T) {
	if err := (Postgres{}).Grant(context.Background(), &mockConn{}, "r", "d", GrantLevel("bogus")); err == nil {
		t.Fatal("unknown grant level must error")
	}
}
