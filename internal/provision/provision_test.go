package provision

import (
	"context"
	"strings"
	"testing"
)

// mockConn records executed statements and answers existence guards from a set.
type mockConn struct {
	execs    []string
	existing map[string]bool
}

func (m *mockConn) Exec(_ context.Context, sql string, _ ...any) error {
	m.execs = append(m.execs, sql)
	return nil
}

func (m *mockConn) Exists(_ context.Context, _ string, args ...any) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	name, _ := args[0].(string)
	return m.existing[name], nil
}

func (m *mockConn) joined() string { return strings.Join(m.execs, "\n") }

func TestEnsureProjectFresh(t *testing.T) {
	m := &mockConn{existing: map[string]bool{}}
	creds, err := Postgres{}.EnsureProject(context.Background(), m, "api", "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Role != "api" || creds.Database != "api" || creds.Password != "s3cr3t" {
		t.Fatalf("creds = %+v", creds)
	}
	sql := m.joined()
	for _, want := range []string{
		`CREATE ROLE "api" WITH LOGIN PASSWORD 's3cr3t'`,
		`CREATE DATABASE "api" OWNER "api"`,
		`REVOKE ALL ON DATABASE "api" FROM PUBLIC`,
		`GRANT ALL ON DATABASE "api" TO "api"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing statement %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "ALTER ROLE") {
		t.Error("fresh provision should CREATE, not ALTER")
	}
}

func TestEnsureProjectIdempotent(t *testing.T) {
	// Role + db already exist → ALTER (keep password in sync), no CREATE DATABASE.
	m := &mockConn{existing: map[string]bool{"api": true}}
	if _, err := (Postgres{}).EnsureProject(context.Background(), m, "api", "newpw"); err != nil {
		t.Fatal(err)
	}
	sql := m.joined()
	if !strings.Contains(sql, `ALTER ROLE "api" WITH LOGIN PASSWORD 'newpw'`) {
		t.Errorf("expected ALTER ROLE, got:\n%s", sql)
	}
	if strings.Contains(sql, "CREATE ROLE") || strings.Contains(sql, "CREATE DATABASE") {
		t.Errorf("should not CREATE when role/db exist:\n%s", sql)
	}
}

func TestIdentifierAndLiteralQuoting(t *testing.T) {
	// Hyphenated project → underscore identifier; password with a quote escaped.
	m := &mockConn{existing: map[string]bool{}}
	creds, err := Postgres{}.EnsureProject(context.Background(), m, "my-app", "pa'ss")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Role != "my_app" {
		t.Errorf("role = %q, want my_app", creds.Role)
	}
	sql := m.joined()
	if !strings.Contains(sql, `CREATE ROLE "my_app" WITH LOGIN PASSWORD 'pa''ss'`) {
		t.Errorf("quoting wrong:\n%s", sql)
	}
}

func TestDSN(t *testing.T) {
	got := DSN("shared-postgres", 5432, "devstack", "p@ss word", "postgres")
	if !strings.HasPrefix(got, "postgres://devstack:") || !strings.Contains(got, "@shared-postgres:5432/postgres") {
		t.Errorf("DSN = %q", got)
	}
	if !strings.Contains(got, "sslmode=disable") {
		t.Errorf("DSN missing sslmode: %q", got)
	}
}
