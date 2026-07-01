package cli

import (
	"strings"
	"testing"
)

func TestDbGroupRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	// db graduated from a stub: create/user/grant/list/drop/gc are real commands.
	for _, path := range [][]string{
		{"db", "create"}, {"db", "user", "create"}, {"db", "grant"},
		{"db", "list"}, {"db", "drop"}, {"db", "gc"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Fatalf("db %v not registered as a real command: %v", path, err)
		}
	}
	// The stub `db` must be gone (no more "planned for v2" placeholder parent).
	c, _, _ := root.Find([]string{"db"})
	if strings.Contains(c.Short, "snapshot/restore") {
		t.Errorf("db still looks like the old stub: %q", c.Short)
	}
}

func TestDbCreateFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"db", "create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"project", "owner", "no-prefix"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("db create missing --%s", f)
		}
	}
}

func TestDbUserCreateFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"db", "user", "create"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"db", "role", "password", "generate", "no-prefix"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("db user create missing --%s", f)
		}
	}
}

func TestS3GroupRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	for _, path := range [][]string{
		{"s3", "mb"}, {"s3", "rb"}, {"s3", "ls"},
		{"s3", "lifecycle", "set"}, {"s3", "lifecycle", "get"}, {"s3", "lifecycle", "rm"},
		{"s3", "versioning"}, {"s3", "policy", "set"}, {"s3", "policy", "get"},
		{"s3", "cors", "set"}, {"s3", "cors", "get"},
	} {
		c, _, err := root.Find(path)
		if err != nil || c.RunE == nil {
			t.Fatalf("s3 %v not registered as a real command: %v", path, err)
		}
	}
}

func TestS3MbFlags(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"s3", "mb"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"versioning", "no-prefix", "project"} {
		if c.Flags().Lookup(f) == nil {
			t.Errorf("s3 mb missing --%s", f)
		}
	}
}

func TestAwsShimRegistered(t *testing.T) {
	root := NewRootCmd(Options{})
	c, _, err := root.Find([]string{"aws"})
	if err != nil || c.RunE == nil {
		t.Fatalf("aws shim not registered as a real command: %v", err)
	}
	if !c.DisableFlagParsing {
		t.Error("aws shim must disable flag parsing to pass args through verbatim")
	}
}

func TestAwsArgsPassthrough(t *testing.T) {
	got := awsArgs("http://127.0.0.1:49000", "us-east-1", []string{"s3", "ls", "--recursive"})
	want := []string{"--endpoint-url=http://127.0.0.1:49000", "--region=us-east-1", "s3", "ls", "--recursive"}
	if len(got) != len(want) {
		t.Fatalf("awsArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("awsArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAwsEnvInjectsCredsNotArgv(t *testing.T) {
	env := awsEnv([]string{"PATH=/usr/bin"}, "test-key", "test-secret", "us-east-1")
	var sawKey, sawSecret bool
	for _, e := range env {
		if e == "AWS_ACCESS_KEY_ID=test-key" {
			sawKey = true
		}
		if e == "AWS_SECRET_ACCESS_KEY=test-secret" {
			sawSecret = true
		}
	}
	if !sawKey || !sawSecret {
		t.Errorf("aws creds not injected into env: %v", env)
	}
	// Creds must never leak into argv.
	for _, a := range awsArgs("http://x", "us-east-1", []string{"s3", "ls"}) {
		if strings.Contains(a, "test-secret") {
			t.Errorf("secret leaked into argv: %q", a)
		}
	}
}

func TestAwsHelpShortCircuits(t *testing.T) {
	// In an empty dir there is no workspace, so buildUpDeps + any daemon access
	// would fail. `aws --help` (and bare `aws`) must still exit 0 by printing the
	// shim's own help BEFORE constructing any docker/S3 client.
	for _, args := range [][]string{{"aws", "--help"}, {"aws", "-h"}, {"aws"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out strings.Builder
			root := NewRootCmd(Options{})
			root.SetArgs(args)
			root.SetOut(&out)
			root.SetErr(&out)
			if err := root.Execute(); err != nil {
				t.Fatalf("`devstack %s` should exit 0 via help, got: %v", strings.Join(args, " "), err)
			}
			if !strings.Contains(out.String(), "aws") || !strings.Contains(out.String(), "Usage") {
				t.Errorf("help output missing usage text: %q", out.String())
			}
		})
	}
}

func TestAwsAbsentBinaryError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty dir → no `aws` on PATH
	if _, err := lookupAws(); err == nil {
		t.Fatal("lookupAws must error when the aws binary is absent")
	} else if !strings.Contains(err.Error(), "aws") {
		t.Errorf("error should mention the aws CLI: %v", err)
	}
}

func TestDbKindAliasAndGrantLevel(t *testing.T) {
	if dbKindAlias("db") != "database" || dbKindAlias("role") != "role" || dbKindAlias("bogus") != "" {
		t.Error("dbKindAlias mapping wrong")
	}
	if !validGrantLevel("read") || !validGrantLevel("write") || !validGrantLevel("admin") || validGrantLevel("owner") {
		t.Error("validGrantLevel wrong")
	}
}

func TestPrefixHelpers(t *testing.T) {
	if got := pgPrefixed("api", "orders", false); got != "api_orders" {
		t.Errorf("pgPrefixed = %q, want api_orders", got)
	}
	if got := pgPrefixed("my-app", "orders", false); got != "my_app_orders" {
		t.Errorf("pgPrefixed hyphen sanitize = %q, want my_app_orders", got)
	}
	if got := pgPrefixed("api", "shared_orders", true); got != "shared_orders" {
		t.Errorf("pgPrefixed --no-prefix = %q, want shared_orders", got)
	}
	if got := bucketPrefixed("web", "uploads", false); got != "web-uploads" {
		t.Errorf("bucketPrefixed = %q, want web-uploads", got)
	}
	if got := bucketPrefixed("web", "external-contract", true); got != "external-contract" {
		t.Errorf("bucketPrefixed --no-prefix = %q, want external-contract", got)
	}
}

func TestParseTransition(t *testing.T) {
	days, tier, err := parseTransition("days=90,tier=GLACIER")
	if err != nil || days != 90 || tier != "GLACIER" {
		t.Fatalf("parseTransition = %d,%q,%v", days, tier, err)
	}
	if _, _, err := parseTransition("days=90"); err == nil {
		t.Error("transition without tier must error")
	}
	if _, _, err := parseTransition("garbage"); err == nil {
		t.Error("malformed transition must error")
	}
}

func TestParseCORS(t *testing.T) {
	rules, err := parseCORS([]byte(`[{"AllowedMethods":["GET","PUT"],"AllowedOrigins":["*"],"MaxAgeSeconds":3000}]`))
	if err != nil || len(rules) != 1 {
		t.Fatalf("parseCORS = %+v err=%v", rules, err)
	}
	if len(rules[0].AllowedMethods) != 2 || rules[0].MaxAgeSeconds == nil || *rules[0].MaxAgeSeconds != 3000 {
		t.Errorf("cors rule not parsed: %+v", rules[0])
	}
	if _, err := parseCORS([]byte(`{bad`)); err == nil {
		t.Error("malformed cors json must error")
	}
}
