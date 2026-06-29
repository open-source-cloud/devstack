package generate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/template"
	"github.com/open-source-cloud/devstack/templates"
)

// newGen loads the shared config fixture and returns a hermetic Generator (fixed
// empty host env, default profile) over the embedded built-in templates.
func newGen(t *testing.T) (*Generator, *config.Model) {
	t.Helper()
	m, err := config.LoadAt(filepath.Join("..", "config", "testdata", "valid"))
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	g, err := New(m, template.NewFSSource(templates.FS), WithEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, m
}

func mustProject(t *testing.T, g *Generator, name string) *Stack {
	t.Helper()
	st, err := g.GenerateProject(name)
	if err != nil {
		t.Fatalf("GenerateProject(%s): %v", name, err)
	}
	return st
}

// TestAcceptance1_ExtendsMergedComposeAndDockerfiles — php.laravel.nginx extends
// php.nginx renders correctly merged compose + Dockerfiles.
func TestAcceptance1_ExtendsMergedComposeAndDockerfiles(t *testing.T) {
	g, _ := newGen(t)
	api := mustProject(t, g, "api")
	compose := string(api.Compose)

	// Merged build args: PHP_VERSION (base) + WITH_COMPOSER (leaf).
	for _, want := range []string{"PHP_VERSION:", "WITH_COMPOSER:"} {
		if !strings.Contains(compose, want) {
			t.Errorf("merged compose missing %q:\n%s", want, compose)
		}
	}
	// Inherited Dockerfile + child entrypoint.sh both present as build files.
	if _, ok := api.BuildFiles["api/build/Dockerfile"]; !ok {
		t.Error("inherited Dockerfile not staged")
	}
	if _, ok := api.BuildFiles["api/build/entrypoint.sh"]; !ok {
		t.Error("child entrypoint.sh not staged")
	}
}

// TestAcceptance2_DockerfileLiteralsSurvive — the rendered Dockerfile keeps its
// literal ${XDEBUG_HOST}, $TAG and ${VAR:-""}.
func TestAcceptance2_DockerfileLiteralsSurvive(t *testing.T) {
	g, _ := newGen(t)
	api := mustProject(t, g, "api")
	df := string(api.BuildFiles["api/build/Dockerfile"])
	for _, lit := range []string{"${XDEBUG_HOST}", "$TAG", `${VAR:-""}`} {
		if !strings.Contains(df, lit) {
			t.Errorf("Dockerfile mangled literal %q:\n%s", lit, df)
		}
	}
	if !strings.Contains(df, "ARG PHP_VERSION=8.3") {
		t.Errorf("Dockerfile action not rendered:\n%s", df)
	}
}

// TestAcceptance3_Deterministic — byte-identical inputs produce byte-identical
// generated files.
func TestAcceptance3_Deterministic(t *testing.T) {
	g1, _ := newGen(t)
	g2, _ := newGen(t)
	for _, name := range []string{"api", "web"} {
		a := mustProject(t, g1, name)
		b := mustProject(t, g2, name)
		if string(a.Compose) != string(b.Compose) {
			t.Errorf("%s compose is non-deterministic", name)
		}
		for f, data := range a.BuildFiles {
			if string(b.BuildFiles[f]) != string(data) {
				t.Errorf("%s build file %s is non-deterministic", name, f)
			}
		}
	}
	// Shared stack too.
	s1, _ := g1.GenerateShared()
	s2, _ := g2.GenerateShared()
	if string(s1.Compose) != string(s2.Compose) {
		t.Error("shared compose is non-deterministic")
	}
}

// TestAcceptance4_SelectiveRebuild — changing one service's build input marks
// only that build context for rebuild; unrelated services are untouched.
func TestAcceptance4_SelectiveRebuild(t *testing.T) {
	g, m := newGen(t)
	dir := t.TempDir()

	// First generation establishes the baseline state.json for the api stack.
	api1 := mustProject(t, g, "api")
	api1.OutDir = filepath.Join(dir, "api")
	if _, err := api1.Write(); err != nil {
		t.Fatal(err)
	}

	// Change the api service's php version (alters its Dockerfile + build args)
	// and regenerate into the same dir.
	p := m.Projects["api"]
	svc := p.Services["api"]
	svc.Params = map[string]any{"phpVersion": "8.4"}
	p.Services["api"] = svc

	api2 := mustProject(t, g, "api")
	api2.OutDir = filepath.Join(dir, "api")
	res, err := api2.Write()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RebuildNeeded) != 1 || res.RebuildNeeded[0] != "api" {
		t.Errorf("RebuildNeeded = %v, want [api]", res.RebuildNeeded)
	}

	// Web stack, generated fresh, must not be implicated by api's change.
	web := mustProject(t, g, "web")
	web.OutDir = filepath.Join(dir, "web")
	if _, err := web.Write(); err != nil {
		t.Fatal(err)
	}
	// A second identical web generation reports no rebuild.
	web2 := mustProject(t, g, "web")
	web2.OutDir = filepath.Join(dir, "web")
	r2, _ := web2.Write()
	if len(r2.RebuildNeeded) != 0 {
		t.Errorf("web RebuildNeeded = %v, want []", r2.RebuildNeeded)
	}
}

// TestAcceptance6_AtomicWriteNoTempLeftBehind — writeIfChanged leaves no temp
// file behind and rewrites only on change.
func TestAcceptance6_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "compose.yaml")

	changed, err := writeIfChanged(path, []byte("a"))
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("a"))
	if err != nil || changed {
		t.Fatalf("identical rewrite should be a no-op: changed=%v err=%v", changed, err)
	}
	changed, err = writeIfChanged(path, []byte("b"))
	if err != nil || !changed {
		t.Fatalf("changed write: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "b" {
		t.Errorf("content = %q, want b", b)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".devstack-tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestAcceptance7_InvalidComposeRejected — a template whose fragment is not a
// valid compose service is rejected by compose-go before any write.
func TestAcceptance7_InvalidComposeRejected(t *testing.T) {
	// A service with neither image nor build fails compose-go's consistency check.
	doc := composeDoc{
		"name":     "devstack-bad",
		"services": map[string]any{"x": map[string]any{"restart": "always"}},
	}
	if _, err := validateAndMarshal(doc, t.TempDir()); err == nil {
		t.Fatal("want a compose validation error for a service with no image/build")
	}
}

// TestRefResolution — ${ref}/import resolves shared Postgres to its DNS alias,
// default port, and the consumer project's role/db.
func TestRefResolution(t *testing.T) {
	g, _ := newGen(t)
	api := mustProject(t, g, "api")
	compose := string(api.Compose)
	wants := []string{
		"POSTGRES_HOST: shared-postgres",
		"POSTGRES_PORT: \"5432\"",
		"POSTGRES_USER: api",
		"POSTGRES_DATABASE: api",
		"API_URL: https://api", // ${self.host}
		"APP_ENV: dev",         // ${profile}
	}
	for _, w := range wants {
		if !strings.Contains(compose, w) {
			t.Errorf("compose missing %q:\n%s", w, compose)
		}
	}
}

// TestSecretCoupling_NoValueInOutput is the §7.5 guarantee: an imported secret
// attribute becomes a VALUELESS env key, and no secret value is ever written.
func TestSecretCoupling_NoValueInOutput(t *testing.T) {
	g, _ := newGen(t)
	// Inject a fake "secret" into the host env under the name the valueless key
	// would resolve from at runtime; it must NOT leak into the generated file.
	g.env = map[string]string{"POSTGRES_PASSWORD": "super-secret-value"}
	api := mustProject(t, g, "api")
	compose := string(api.Compose)

	if strings.Contains(compose, "super-secret-value") {
		t.Error("secret value leaked into generated compose")
	}
	if !strings.Contains(compose, "POSTGRES_PASSWORD: null") {
		t.Errorf("POSTGRES_PASSWORD should be a valueless (null) key:\n%s", compose)
	}
}

// TestWriteAndUpToDate exercises the full Write → UpToDate round-trip.
func TestWriteAndUpToDate(t *testing.T) {
	g, _ := newGen(t)
	dir := t.TempDir()
	api := mustProject(t, g, "api")
	api.OutDir = dir
	if _, err := api.Write(); err != nil {
		t.Fatal(err)
	}
	// Regenerate a fresh stack pointed at the same dir; it should report up to date.
	api2 := mustProject(t, g, "api")
	api2.OutDir = dir
	if !api2.UpToDate() {
		t.Error("freshly written stack should be UpToDate")
	}
	if _, err := os.Stat(filepath.Join(dir, ComposeFile)); err != nil {
		t.Errorf("compose not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFile)); err != nil {
		t.Errorf("state.json not written: %v", err)
	}
}

// TestGenerateAllOrder — shared stack first, then projects in sorted order.
func TestGenerateAllOrder(t *testing.T) {
	g, _ := newGen(t)
	stacks, err := g.GenerateAll()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(stacks))
	for i, s := range stacks {
		got[i] = s.Name
	}
	want := []string{"devstack-shared", "devstack-api", "devstack-web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stack order = %v, want %v", got, want)
	}
}
