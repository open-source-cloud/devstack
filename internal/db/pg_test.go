package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recRunner records the argv + env of every shelled tool and returns a canned
// Output for psql.
type recRunner struct {
	cmds   [][]string
	envs   [][]string
	output []byte
}

func (r *recRunner) Run(_ context.Context, env []string, _, name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	return nil
}
func (r *recRunner) Output(_ context.Context, env []string, _, name string, args ...string) ([]byte, error) {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	return r.output, nil
}

func conn() ConnInfo {
	return ConnInfo{Host: "127.0.0.1", Port: 45432, User: "devstack", Password: "s3cr3t", Database: "app"}
}

func TestPgDumperSnapshotArgv(t *testing.T) {
	r := &recRunner{}
	p := PgDumper{Runner: r}
	if err := p.Snapshot(context.Background(), conn(), "/tmp/app.dump"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	argv := strings.Join(r.cmds[0], " ")
	for _, want := range []string{"pg_dump", "-h 127.0.0.1", "-p 45432", "-U devstack", "-d app", "--format=custom", "--no-owner", "--file /tmp/app.dump"} {
		if !strings.Contains(argv, want) {
			t.Errorf("Snapshot argv missing %q: %s", want, argv)
		}
	}
	// Password only in the env, never on argv.
	if strings.Contains(argv, "s3cr3t") {
		t.Errorf("password leaked into argv: %s", argv)
	}
	if got := strings.Join(r.envs[0], " "); got != "PGPASSWORD=s3cr3t" {
		t.Errorf("env = %q, want PGPASSWORD=s3cr3t", got)
	}
}

func TestPgDumperRestoreArgv(t *testing.T) {
	r := &recRunner{}
	p := PgDumper{Runner: r}
	if err := p.Restore(context.Background(), conn(), "/tmp/app.dump"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	argv := strings.Join(r.cmds[0], " ")
	for _, want := range []string{"pg_restore", "-d app", "--clean", "--if-exists", "--no-owner", "/tmp/app.dump"} {
		if !strings.Contains(argv, want) {
			t.Errorf("Restore argv missing %q: %s", want, argv)
		}
	}
}

func TestPgDumperIsEmpty(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"0\n", true},
		{"7\n", false},
		{" 0 ", true},
	} {
		r := &recRunner{output: []byte(tc.out)}
		p := PgDumper{Runner: r}
		got, err := p.IsEmpty(context.Background(), conn())
		if err != nil {
			t.Fatalf("IsEmpty(%q): %v", tc.out, err)
		}
		if got != tc.want {
			t.Errorf("IsEmpty(%q) = %v, want %v", tc.out, got, tc.want)
		}
		if r.cmds[0][0] != "psql" {
			t.Errorf("IsEmpty shelled %q, want psql", r.cmds[0][0])
		}
	}
}

func TestPreflightMissingTool(t *testing.T) {
	p := PgDumper{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	err := p.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight should fail when the tool is absent")
	}
	if !IsToolMissing(err) {
		t.Errorf("want ErrToolMissing, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "postgresql-client") {
		t.Errorf("remediation missing: %v", err)
	}
}

func TestPreflightPresent(t *testing.T) {
	p := PgDumper{LookPath: func(string) (string, error) { return "/usr/bin/x", nil }}
	if err := p.Preflight(context.Background()); err != nil {
		t.Errorf("Preflight should pass when tools are present: %v", err)
	}
}
