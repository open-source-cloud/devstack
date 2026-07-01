package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/state"
)

// mkProbe builds a probe whose fix flips a live flag so the recheck can report a
// transition to OK. fixCalls counts fix invocations so we can assert a passing
// probe's fix is never run.
func mkProbe(id string, status docker.CheckStatus, fixable bool, fixCalls *int, fixErr error, fixed *bool) probe {
	c := docker.Check{ID: id, Name: id, Status: status, Fixable: fixable}
	if !fixable {
		c.Remediation = "do it yourself"
		return probe{check: c}
	}
	c.Remediation = "run --fix"
	return probe{
		check: c,
		fix: func(context.Context) error {
			*fixCalls++
			if fixErr != nil {
				return fixErr
			}
			*fixed = true
			return nil
		},
		recheck: func(context.Context) docker.Check {
			if *fixed {
				return docker.Check{ID: id, Name: id, Status: docker.StatusOK, Fixable: false, Detail: "repaired"}
			}
			return c
		},
	}
}

func TestApplyFixes(t *testing.T) {
	t.Run("failing fixable check is fixed and re-probed green", func(t *testing.T) {
		var calls int
		var fixed bool
		probes := []probe{mkProbe("net.shared", docker.StatusWarn, true, &calls, nil, &fixed)}

		res := applyFixes(context.Background(), probes)

		if calls != 1 {
			t.Fatalf("fix should run exactly once, ran %d times", calls)
		}
		if len(res) != 1 || !res[0].Fixed {
			t.Fatalf("expected one fixed result, got %+v", res)
		}
		// The probe is updated in place so the report reflects the post-fix state.
		if probes[0].check.Status != docker.StatusOK || !probes[0].check.Fixed {
			t.Fatalf("probe not updated post-fix: %+v", probes[0].check)
		}
	})

	t.Run("non-fixable failing check is left with its remediation", func(t *testing.T) {
		var calls int
		var fixed bool
		probes := []probe{mkProbe("trust.host", docker.StatusWarn, false, &calls, nil, &fixed)}

		res := applyFixes(context.Background(), probes)

		if calls != 0 {
			t.Fatalf("a non-fixable probe must never be fixed, ran %d times", calls)
		}
		if len(res) != 0 {
			t.Fatalf("non-fixable probe must not appear in fix results, got %+v", res)
		}
		if probes[0].check.Status != docker.StatusWarn || probes[0].check.Remediation == "" {
			t.Fatalf("non-fixable probe should keep its warn + remediation: %+v", probes[0].check)
		}
	})

	t.Run("passing check's fix is never invoked", func(t *testing.T) {
		var calls int
		var fixed bool
		// Fixable but already OK — the fix must NOT run.
		probes := []probe{mkProbe("fs.xdg", docker.StatusOK, true, &calls, nil, &fixed)}

		res := applyFixes(context.Background(), probes)

		if calls != 0 {
			t.Fatalf("a passing check's fix must never run, ran %d times", calls)
		}
		if len(res) != 0 {
			t.Fatalf("a passing check must not appear in fix results, got %+v", res)
		}
	})

	t.Run("a failing fix reports still-failing with remediation", func(t *testing.T) {
		var calls int
		var fixed bool
		probes := []probe{mkProbe("state.refs", docker.StatusFail, true, &calls, os.ErrPermission, &fixed)}

		res := applyFixes(context.Background(), probes)

		if calls != 1 {
			t.Fatalf("fix should be attempted once, ran %d times", calls)
		}
		if len(res) != 1 || res[0].Fixed {
			t.Fatalf("a failing fix must report not-fixed: %+v", res)
		}
		if res[0].Remediation == "" {
			t.Fatalf("a failing fix must keep the remediation: %+v", res[0])
		}
		// Status unchanged (recheck not applied on fix error).
		if probes[0].check.Status != docker.StatusFail {
			t.Fatalf("probe status should be unchanged after a failed fix: %+v", probes[0].check)
		}
	})

	t.Run("mixed matrix: only the fixable non-OK probe is remediated", func(t *testing.T) {
		var okCalls, warnCalls, plainCalls int
		var f1, f2, f3 bool
		probes := []probe{
			mkProbe("ok.fixable", docker.StatusOK, true, &okCalls, nil, &f1),       // passing → skip
			mkProbe("warn.fixable", docker.StatusWarn, true, &warnCalls, nil, &f2), // fixed
			mkProbe("warn.plain", docker.StatusWarn, false, &plainCalls, nil, &f3), // non-fixable → skip
		}

		res := applyFixes(context.Background(), probes)

		if okCalls != 0 || plainCalls != 0 {
			t.Fatalf("only the fixable non-OK probe should run its fix: ok=%d plain=%d", okCalls, plainCalls)
		}
		if warnCalls != 1 {
			t.Fatalf("the fixable warn probe should run once, ran %d", warnCalls)
		}
		if len(res) != 1 || res[0].ID != "warn.fixable" || !res[0].Fixed {
			t.Fatalf("expected exactly the warn.fixable probe fixed, got %+v", res)
		}
	})
}

func TestDirSecure(t *testing.T) {
	base := t.TempDir()

	tests := []struct {
		name  string
		setup func() string
		want  bool
	}{
		{
			name:  "missing dir is not secure",
			setup: func() string { return filepath.Join(base, "nope") },
			want:  false,
		},
		{
			name: "0700 dir is secure",
			setup: func() string {
				d := filepath.Join(base, "priv")
				if err := os.Mkdir(d, 0o700); err != nil {
					t.Fatal(err)
				}
				return d
			},
			want: true,
		},
		{
			name: "group/world-writable dir is not secure",
			setup: func() string {
				d := filepath.Join(base, "loose")
				if err := os.Mkdir(d, 0o777); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(d, 0o777); err != nil {
					t.Fatal(err)
				}
				return d
			},
			want: false,
		},
		{
			name: "a file (not a dir) is not secure",
			setup: func() string {
				f := filepath.Join(base, "afile")
				if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return f
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dirSecure(tt.setup()); got != tt.want {
				t.Fatalf("dirSecure = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNetSharedProbeFixCreatesNetwork wires the real net.shared remediation: a
// missing external network warns fixable, the fix (EnsureNetwork under the lock)
// creates it, and the re-probe is green.
func TestNetSharedProbeFixCreatesNetwork(t *testing.T) {
	mock := &docker.MockClient{Context: "ctx"} // no networks seeded
	s := &doctorSession{
		client:   mock,
		lockPath: filepath.Join(t.TempDir(), "devstack.lock"),
	}
	ctx := context.Background()

	p := s.netSharedProbe(ctx)
	if p.check.Status != docker.StatusWarn || !p.check.Fixable {
		t.Fatalf("a missing network should be a fixable warn, got %+v", p.check)
	}

	probes := []probe{p}
	res := applyFixes(ctx, probes)
	if len(res) != 1 || !res[0].Fixed {
		t.Fatalf("network fix should report fixed, got %+v", res)
	}
	if !mock.Networks[generate.SharedNetwork] {
		t.Fatalf("EnsureNetwork was not called: %+v", mock.Networks)
	}
	if probes[0].check.Status != docker.StatusOK {
		t.Fatalf("re-probe should be OK after creating the network, got %+v", probes[0].check)
	}
}

// TestStateRefsProbeFixPrunesStaleRows wires the real state.refs remediation: a
// ledger ref row for a project with no live container is stale, and the fix
// (Reconcile under the flock) prunes it — never touching data.
func TestStateRefsProbeFixPrunesStaleRows(t *testing.T) {
	db, err := state.Open(context.Background(), t.TempDir(), "ctx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// A ref row for a project that has NO live container → stale.
	if err := db.AddRef("ghost", "web", "shared-postgres"); err != nil {
		t.Fatal(err)
	}

	mock := &docker.MockClient{Context: "ctx"} // no containers → ghost is stale
	s := &doctorSession{
		client:   mock,
		db:       db,
		lockPath: filepath.Join(t.TempDir(), "devstack.lock"),
	}
	ctx := context.Background()

	p := s.stateRefsProbe(ctx)
	if p.check.Status != docker.StatusWarn || !p.check.Fixable {
		t.Fatalf("a stale ref row should be a fixable warn, got %+v", p.check)
	}

	probes := []probe{p}
	res := applyFixes(ctx, probes)
	if len(res) != 1 || !res[0].Fixed {
		t.Fatalf("ref prune should report fixed, got %+v", res)
	}
	refs, err := db.AllRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("stale ref rows should be pruned, remaining: %+v", refs)
	}
}

// TestStateRefsProbeKeepsLiveRefs proves the reconcile never prunes a ref whose
// project IS live (no false-positive teardown of an in-use shared service).
func TestStateRefsProbeKeepsLiveRefs(t *testing.T) {
	db, err := state.Open(context.Background(), t.TempDir(), "ctx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.AddRef("api", "db", "shared-postgres"); err != nil {
		t.Fatal(err)
	}

	mock := &docker.MockClient{
		Context: "ctx",
		Containers: []docker.Container{{
			Name:   "devstack-api-db-1",
			State:  "running",
			Labels: map[string]string{generate.LabelManaged: "true", generate.LabelProject: "api"},
		}},
	}
	s := &doctorSession{client: mock, db: db, lockPath: filepath.Join(t.TempDir(), "devstack.lock")}

	p := s.stateRefsProbe(context.Background())
	if p.check.Status != docker.StatusOK || p.check.Fixable {
		t.Fatalf("a live ref should be OK and not fixable, got %+v", p.check)
	}
}

// TestXDGDirsProbeFixCreatesDirs exercises the real fs.xdg fix end to end: a
// missing XDG dir warns fixable, the fix creates it 0700, and the re-probe is
// green — all without touching any shared state.
func TestXDGDirsProbeFixCreatesDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))

	s := &doctorSession{}
	p := s.xdgDirsProbe()
	if p.check.Status != docker.StatusWarn || !p.check.Fixable {
		t.Fatalf("expected a fixable warn for missing XDG dirs, got %+v", p.check)
	}

	probes := []probe{p}
	res := applyFixes(context.Background(), probes)
	if len(res) != 1 || !res[0].Fixed {
		t.Fatalf("xdg dir fix should report fixed, got %+v", res)
	}
	if probes[0].check.Status != docker.StatusOK {
		t.Fatalf("re-probe should be OK after creating dirs, got %+v", probes[0].check)
	}
	// The private data dir now exists with 0700.
	fi, err := os.Stat(filepath.Join(base, "data", "devstack"))
	if err != nil {
		t.Fatalf("data dir not created: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("data dir perm = %o, want 0700", fi.Mode().Perm())
	}
}
