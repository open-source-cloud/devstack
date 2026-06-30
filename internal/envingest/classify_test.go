package envingest

import "testing"

func TestClassifyLadder(t *testing.T) {
	vars := map[string]string{
		"DB_PASSWORD":       "s3cr3t-p@ss",
		"STRIPE_SECRET_KEY": "sk_live_51Hxxxxxxxxxxxxabcdef",
		"APP_ENV":           "local",
		"PORT":              "8080",
		"REDIS_URL":         "redis://shared-redis:6379/0",
		"DATABASE_URL":      "postgres://user:p4ss@db:5432/app",
		"FEATURE_FLAG":      "true",
	}
	decisions, err := Classify(vars, nil, nil, nil, "api")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Decision{}
	prev := ""
	for _, d := range decisions {
		if prev != "" && d.Key < prev {
			t.Fatalf("decisions not sorted: %s after %s", d.Key, prev)
		}
		prev = d.Key
		byKey[d.Key] = d
	}
	want := map[string]string{
		"DB_PASSWORD":       "secret",
		"STRIPE_SECRET_KEY": "secret",
		"APP_ENV":           "config",
		"PORT":              "config",
		"REDIS_URL":         "config",
		"DATABASE_URL":      "secret", // credentialed URL
		"FEATURE_FLAG":      "config",
	}
	for k, w := range want {
		if byKey[k].Class != w {
			t.Errorf("%s: class=%s reason=%q, want %s", k, byKey[k].Class, byKey[k].Reason, w)
		}
	}
}

func TestClassifyGlobOverrides(t *testing.T) {
	vars := map[string]string{
		"DB_PASSWORD": "x",     // name → secret, but --public forces config
		"PLAIN_NAME":  "local", // benign → config, but --secret forces secret
		"PORT":        "8080",  // config; --from-host marks it host-sourced
	}
	decisions, err := Classify(vars, []string{"PLAIN_*"}, []string{"DB_PASSWORD"}, []string{"PORT"}, "api")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Decision{}
	for _, d := range decisions {
		m[d.Key] = d
	}
	if m["DB_PASSWORD"].Class != "config" {
		t.Errorf("--public override failed: %+v", m["DB_PASSWORD"])
	}
	if m["PLAIN_NAME"].Class != "secret" {
		t.Errorf("--secret override failed: %+v", m["PLAIN_NAME"])
	}
	if m["PORT"].Class != "config" || !m["PORT"].HostFrom {
		t.Errorf("--from-host failed: %+v", m["PORT"])
	}
}

func TestClassifyDefaultDeny(t *testing.T) {
	// An unrecognized, opaque, mid-length value with no benign signal → secret.
	vars := map[string]string{"WEIRD": "Zk29fjA0qLmZxQwErTyU"}
	decisions, _ := Classify(vars, nil, nil, nil, "api")
	if decisions[0].Class != "secret" {
		t.Fatalf("default-deny failed: %+v", decisions[0])
	}
}
