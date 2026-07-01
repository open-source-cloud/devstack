package tenant

import (
	"errors"
	"testing"
)

func fixedEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve_LocalHasNoTenant(t *testing.T) {
	id := Resolve(false, "ignored", Deps{
		Getenv: fixedEnv(map[string]string{"DEVSTACK_TENANT": "bob"}),
		OSUser: func() (string, error) { return "bob", nil },
	})
	if id.IsTenant() || id.Name != "" {
		t.Fatalf("local backend must have no tenant, got %q", id.Name)
	}
	// Qualify is a no-op locally → backward-compatible names.
	if got := id.Qualify("app", "_"); got != "app" {
		t.Errorf("local Qualify = %q, want app", got)
	}
}

func TestResolve_RemotePrecedence(t *testing.T) {
	cases := []struct {
		name       string
		env        map[string]string
		configured string
		osUser     string
		osErr      error
		want       string
	}{
		{"env wins", map[string]string{"DEVSTACK_TENANT": "Alice.Dev"}, "cfg", "root", nil, "alice-dev"},
		{"configured next", nil, "Team-One", "root", nil, "team-one"},
		{"os user fallback", nil, "", "Gustavo.Bertoi", nil, "gustavo-bertoi"},
		{"nameless fallback", nil, "", "", errors.New("no user"), "user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := Resolve(true, tc.configured, Deps{
				Getenv: fixedEnv(tc.env),
				OSUser: func() (string, error) { return tc.osUser, tc.osErr },
			})
			if id.Name != tc.want {
				t.Errorf("Resolve = %q, want %q", id.Name, tc.want)
			}
		})
	}
}

func TestResolve_JunkConfiguredFallsToUser(t *testing.T) {
	// A configured value that sanitizes to empty must not yield an empty tenant.
	id := Resolve(true, "***", Deps{
		Getenv: fixedEnv(nil),
		OSUser: func() (string, error) { return "", errors.New("none") },
	})
	if id.Name != "user" {
		t.Errorf("junk configured → %q, want user", id.Name)
	}
}

func TestQualify_RemoteNamespaces(t *testing.T) {
	id := Identity{Name: "alice"}
	if got := id.Qualify("app", "_"); got != "alice_app" {
		t.Errorf("sql Qualify = %q, want alice_app", got)
	}
	if got := id.Qualify("uploads", "-"); got != "alice-uploads" {
		t.Errorf("bucket Qualify = %q, want alice-uploads", got)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Gustavo.Bertoi": "gustavo-bertoi",
		"UPPER_snake":    "upper-snake",
		"  trim.me  ":    "trim-me",
		"a@@@b---c":      "a-b-c",
		"---leading":     "leading",
		"trailing---":    "trailing",
		"only***symbols": "only-symbols",
		"***":            "",
		"good123":        "good123",
		"日本語user":        "user", // non-ascii dropped, leaving "user"
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitize_LengthCapped(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghij" // 45 chars
	got := Sanitize(long)
	if len(got) > maxLen {
		t.Errorf("Sanitize length = %d, want <= %d", len(got), maxLen)
	}
}
