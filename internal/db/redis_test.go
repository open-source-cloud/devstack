package db

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func redisConn() ConnInfo {
	return ConnInfo{Host: "127.0.0.1", Port: 46379, Password: "s3cr3t", Database: "2"}
}

func TestRedisDumperSnapshotArgv(t *testing.T) {
	r := &recRunner{}
	d := RedisDumper{Runner: r}
	if err := d.Snapshot(context.Background(), redisConn(), "/tmp/app.rdb"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	argv := strings.Join(r.cmds[0], " ")
	for _, want := range []string{"redis-cli", "-h 127.0.0.1", "-p 46379", "-n 2", "--rdb /tmp/app.rdb"} {
		if !strings.Contains(argv, want) {
			t.Errorf("Snapshot argv missing %q: %s", want, argv)
		}
	}
	// Password only in the env, never on argv.
	if strings.Contains(argv, "s3cr3t") {
		t.Errorf("password leaked into argv: %s", argv)
	}
	if got := strings.Join(r.envs[0], " "); got != "REDISCLI_AUTH=s3cr3t" {
		t.Errorf("env = %q, want REDISCLI_AUTH=s3cr3t", got)
	}
}

func TestRedisDumperSnapshotNoAuthNoIndex(t *testing.T) {
	r := &recRunner{}
	d := RedisDumper{Runner: r}
	// Auth-less, whole-instance (no logical index): no -n flag, no REDISCLI_AUTH env.
	conn := ConnInfo{Host: "127.0.0.1", Port: 6379}
	if err := d.Snapshot(context.Background(), conn, "/tmp/all.rdb"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	argv := strings.Join(r.cmds[0], " ")
	if strings.Contains(argv, "-n ") {
		t.Errorf("unexpected -n flag on whole-instance snapshot: %s", argv)
	}
	if len(r.envs[0]) != 0 {
		t.Errorf("no REDISCLI_AUTH expected for an auth-less server, got %v", r.envs[0])
	}
}

func TestRedisDumperRestoreArgv(t *testing.T) {
	r := &recRunner{}
	d := RedisDumper{Runner: r}
	if err := d.Restore(context.Background(), redisConn(), "/tmp/app.rdb"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// Restore shells `sh -c "redis-cli ... --pipe < <file>"` (no stdin channel on
	// the Runner), so the redis-cli invocation is inside the -c payload.
	if r.cmds[0][0] != "sh" || r.cmds[0][1] != "-c" {
		t.Fatalf("restore did not shell via sh -c: %v", r.cmds[0])
	}
	payload := r.cmds[0][2]
	for _, want := range []string{"redis-cli", "-h 127.0.0.1", "-p 46379", "-n 2", "--pipe", "/tmp/app.rdb"} {
		if !strings.Contains(payload, want) {
			t.Errorf("restore payload missing %q: %s", want, payload)
		}
	}
	if strings.Contains(payload, "s3cr3t") {
		t.Errorf("password leaked into restore payload: %s", payload)
	}
	if got := strings.Join(r.envs[0], " "); got != "REDISCLI_AUTH=s3cr3t" {
		t.Errorf("env = %q, want REDISCLI_AUTH=s3cr3t", got)
	}
}

func TestRedisDumperIsEmpty(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"0\n", true},
		{"42\n", false},
		{" 0 ", true},
	} {
		r := &recRunner{output: []byte(tc.out)}
		d := RedisDumper{Runner: r}
		got, err := d.IsEmpty(context.Background(), redisConn())
		if err != nil {
			t.Fatalf("IsEmpty(%q): %v", tc.out, err)
		}
		if got != tc.want {
			t.Errorf("IsEmpty(%q) = %v, want %v", tc.out, got, tc.want)
		}
		if r.cmds[0][0] != "redis-cli" {
			t.Errorf("IsEmpty shelled %q, want redis-cli", r.cmds[0][0])
		}
		if last := r.cmds[0][len(r.cmds[0])-1]; last != "DBSIZE" {
			t.Errorf("IsEmpty command = %q, want DBSIZE", last)
		}
	}
}

func TestRedisPreflightMissingTool(t *testing.T) {
	d := RedisDumper{LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	err := d.Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight should fail when redis-cli is absent")
	}
	if !IsToolMissing(err) {
		t.Errorf("want ErrToolMissing, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "redis-cli") || !strings.Contains(err.Error(), "redis-tools") {
		t.Errorf("remediation missing: %v", err)
	}
}

func TestRedisPreflightPresent(t *testing.T) {
	d := RedisDumper{LookPath: func(string) (string, error) { return "/usr/bin/redis-cli", nil }}
	if err := d.Preflight(context.Background()); err != nil {
		t.Errorf("Preflight should pass when redis-cli is present: %v", err)
	}
}
