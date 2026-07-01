package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/dns"
	"github.com/open-source-cloud/devstack/internal/docker"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/proxy"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/trust"
	"github.com/open-source-cloud/devstack/internal/workspace"
	"github.com/open-source-cloud/devstack/internal/xdg"
)

// Probe categories (spec 13): a `fail` in critical → non-zero exit; warn/info do
// not gate. Category is orthogonal to Status.
const (
	catCritical = "critical"
	catWarn     = "warn"
	catInfo     = "info"
)

func newDoctorCmd(g *GlobalOpts) *cobra.Command {
	var (
		fix          bool
		rebuildState bool
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the environment and report capabilities with remediations",
		Long: "doctor runs the REAL branch logic (not docs) for the tools and paths devstack\n" +
			"depends on, and prints a one-line remediation for anything that isn't OK.\n\n" +
			"With --fix it applies the STRICTLY non-destructive, idempotent remediations\n" +
			"(create the shared external network, prune stale ledger ref rows, create/tighten\n" +
			"XDG dirs) under the machine-global lock, re-probes, and reports fixed/still-failing.\n" +
			"It never removes a volume, container, network, database, or CA — those live in\n" +
			"the teardown verbs (`workspace destroy`, `uninstall`).\n\n" +
			"With --rebuild-state, the shared_service + ref ledger is reconstructed from\n" +
			"on-disk config intersected with live container labels (recovery when state.db\n" +
			"is lost or corrupt — the ledger is a cache of reality).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if rebuildState {
				return rebuildLedger(cmd, g)
			}

			sess, cleanup := openDoctorSession(cmd)
			defer cleanup()

			probes := sess.probes(cmd.Context())

			var fixes []fixResult
			if fix {
				// applyFixes runs only fixable, non-OK probes under the flock (inside
				// each fix closure), re-probes, and updates probes in place so the
				// report and exit code below reflect the POST-fix state.
				fixes = applyFixes(cmd.Context(), probes)
			}

			checks := checksOf(probes)
			if g.JSON {
				payload := map[string]any{"checks": checks}
				if fix {
					payload["fixes"] = fixes
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(payload)
			}
			renderChecks(cmd, checks, g.Quiet)
			if fix {
				renderFixes(cmd, fixes)
			}
			if n := countFails(checks); n > 0 {
				return fmt.Errorf("doctor found %d failing check(s)", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe, idempotent, non-destructive remediations under the lock")
	cmd.Flags().BoolVar(&rebuildState, "rebuild-state", false, "reconstruct the ledger from config + live container labels")
	return cmd
}

// probe is one doctor capability check plus its optional safe remediation. The
// embedded Check carries the JSON-serialized result (id/category/status/detail/
// remediation/fixable). fix, when set (and only when check.Fixable), performs a
// reconstructible, non-destructive repair; recheck re-runs just this probe after
// a fix so `--fix` can report the post-fix state. A probe with a nil fix is
// diagnose-only and is left with its remediation for the human to run.
type probe struct {
	check   docker.Check
	fix     func(context.Context) error
	recheck func(context.Context) docker.Check
}

// fixResult records the outcome of one attempted `--fix` remediation.
type fixResult struct {
	ID          string `json:"id"`
	Fixed       bool   `json:"fixed"`
	Detail      string `json:"detail,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

// applyFixes runs the remediation of every FIXABLE, non-OK probe, then re-probes
// it and records whether it is now green. Invariants (spec 13 safe subset):
//   - a passing (StatusOK) probe's fix is NEVER invoked;
//   - a probe with no fix (not remediable) is left untouched with its remediation;
//   - each probe's post-fix Check replaces the pre-fix one in `probes`, so the
//     final report and exit code reflect the POST-fix state.
//
// Shared-state mutations are locked INSIDE the individual fix closures (network
// ensure and ref reconcile each take the flock); filesystem-only fixes (XDG dir
// perms) need no lock. This keeps the runner itself pure and unit-testable.
func applyFixes(ctx context.Context, probes []probe) []fixResult {
	var results []fixResult
	for i := range probes {
		p := &probes[i]
		// Never touch a passing check, and skip diagnose-only probes.
		if p.check.Status == docker.StatusOK || !p.check.Fixable || p.fix == nil {
			continue
		}
		res := fixResult{ID: p.check.ID}
		if err := p.fix(ctx); err != nil {
			res.Detail = "fix failed: " + err.Error()
			res.Remediation = p.check.Remediation
			results = append(results, res)
			continue
		}
		if p.recheck != nil {
			p.check = p.recheck(ctx)
		}
		p.check.Fixed = p.check.Status == docker.StatusOK
		res.Fixed = p.check.Fixed
		res.Detail = p.check.Detail
		if !res.Fixed {
			res.Remediation = p.check.Remediation
		}
		results = append(results, res)
	}
	return results
}

// checksOf projects the probe slice down to the serialized checks.
func checksOf(probes []probe) []docker.Check {
	out := make([]docker.Check, 0, len(probes))
	for _, p := range probes {
		out = append(out, p.check)
	}
	return out
}

// doctorSession holds the best-effort live resources the probes read from and
// that `--fix` mutates: the read-only docker client, the state ledger, the loaded
// workspace, and the machine-global lock path. Any handle may be nil/absent when
// unavailable (no daemon, corrupt ledger, cwd not a workspace) — probes degrade
// to a diagnose-only result rather than crashing, and their fixes become no-ops.
type doctorSession struct {
	cwd       string
	client    docker.Client // nil when the Engine SDK client could not be built
	clientErr error
	db        *state.DB // nil when the ledger could not be opened
	dbErr     error
	model     *config.Model // nil when cwd is not a workspace
	ctxName   string
	lockPath  string
}

// openDoctorSession opens the docker client and state ledger once (best-effort)
// so probes and their fixes share a single set of handles for the whole run. The
// returned cleanup releases them.
func openDoctorSession(cmd *cobra.Command) (*doctorSession, func()) {
	ctx := cmd.Context()
	cwd, _ := os.Getwd()
	s := &doctorSession{
		cwd:      cwd,
		ctxName:  state.DefaultContext,
		lockPath: filepath.Join(xdg.RuntimeDir(), "devstack.lock"),
	}
	if m, err := config.Load(cwd); err == nil {
		s.model = m
	}
	if c, err := docker.NewClient(ctx); err == nil {
		s.client = c
		s.ctxName = c.ContextName()
	} else {
		s.clientErr = err
	}
	if db, err := state.Open(ctx, xdg.DataHome(), s.ctxName); err == nil {
		s.db = db
	} else {
		s.dbErr = err
	}
	return s, func() {
		if s.db != nil {
			s.db.Close()
		}
		if s.client != nil {
			_ = s.client.Close()
		}
	}
}

// manager builds a workspace.Manager over the session's shared handles. Reconcile
// (the state.refs fix) uses only DB/Docker/LockPath, so a nil Model is fine.
func (s *doctorSession) manager() *workspace.Manager {
	return &workspace.Manager{
		Model:    s.model,
		DB:       s.db,
		Docker:   s.client,
		LockPath: s.lockPath,
	}
}

// probes assembles the full capability matrix. Each probe is independent so a
// single failure never hides the others; the fixable ones carry a safe fix.
func (s *doctorSession) probes(ctx context.Context) []probe {
	var probes []probe

	// fs.workdir — WSL2 /mnt refusal (critical).
	if err := xdg.RefuseWindowsMount(s.cwd); err != nil {
		probes = append(probes, plain(docker.Check{
			Name: "working dir", ID: "fs.workdir", Category: catCritical,
			Status: docker.StatusFail, Detail: err.Error(),
			Remediation: "move the workspace onto the Linux filesystem",
		}))
	} else {
		probes = append(probes, plain(docker.Check{
			Name: "working dir", ID: "fs.workdir", Category: catCritical,
			Status: docker.StatusOK, Detail: s.cwd,
		}))
	}

	// fs.statedir / fs.lockdir — SQLite + flock reliability (warn on 9p/networked).
	stateDir := xdg.DataHome()
	probes = append(probes, plain(fsCheck("state dir (SQLite)", "fs.statedir", stateDir,
		"set XDG_DATA_HOME to a local (ext4/apfs) path on the Linux filesystem")))
	lockDir := xdg.RuntimeDir()
	if lockDir != stateDir {
		probes = append(probes, plain(fsCheck("lock dir (flock)", "fs.lockdir", lockDir,
			"set XDG_RUNTIME_DIR to a local tmpfs/ext4 path; the advisory lock lives here")))
	}

	// fs.xdg — the private XDG dirs exist with 0700-ish perms (fixable: create/tighten).
	probes = append(probes, s.xdgDirsProbe())

	// platform — WSL2 awareness (info).
	if xdg.IsWSL2() {
		probes = append(probes, plain(docker.Check{
			Name: "platform", ID: "platform", Category: catInfo,
			Status: docker.StatusOK, Detail: "WSL2 detected",
		}))
	}

	// Docker / compose / git preflight (critical). Categorized post-hoc.
	if s.client == nil {
		for _, c := range docker.Preflight(ctx, nil) {
			probes = append(probes, plain(withCategory(c, catCritical)))
		}
		probes = append(probes, plain(docker.Check{
			Name: "docker client", ID: "docker.client", Category: catCritical,
			Status: docker.StatusWarn, Detail: errText(s.clientErr),
			Remediation: "ensure DOCKER_HOST / the active docker context is valid",
		}))
	} else {
		for _, c := range docker.Preflight(ctx, s.client) {
			probes = append(probes, plain(withCategory(c, catCritical)))
		}
	}

	// state.ledger — the ledger opens/migrates cleanly (critical).
	if s.dbErr != nil {
		probes = append(probes, plain(docker.Check{
			Name: "state ledger", ID: "state.ledger", Category: catCritical,
			Status: docker.StatusFail, Detail: s.dbErr.Error(),
			Remediation: "remove a corrupt state.db (a backup is kept) or run `devstack doctor --rebuild-state`",
		}))
	} else {
		v, _ := s.db.SchemaVersion()
		probes = append(probes, plain(docker.Check{
			Name: "state ledger", ID: "state.ledger", Category: catCritical,
			Status: docker.StatusOK, Detail: fmt.Sprintf("schema v%d @ %s", v, s.ctxName),
		}))
		// state.shared — informational instance/ref summary (kept for compatibility).
		shared, _ := s.db.ListSharedServices()
		totalRefs := 0
		for _, sv := range shared {
			n, _ := s.db.RefCount(sv.Name)
			totalRefs += n
		}
		probes = append(probes, plain(docker.Check{
			Name: "shared services", ID: "state.shared", Category: catInfo,
			Status: docker.StatusOK,
			Detail: fmt.Sprintf("%d instance(s), %d ref(s)", len(shared), totalRefs),
		}))
	}

	// net.shared — the tool-owned external bridge exists (fixable: EnsureNetwork).
	probes = append(probes, s.netSharedProbe(ctx))

	// state.refs — stale ref rows vs live containers (fixable: Reconcile).
	probes = append(probes, s.stateRefsProbe(ctx))

	// dns.resolver — *.localhost /etc/hosts fence, when the cwd is a proxied
	// workspace. Diagnose-only: the write needs sudo, so it is out of `--fix`.
	if p, ok := s.dnsProbe(); ok {
		probes = append(probes, p)
	}

	// trust.host — local-CA readiness (diagnose-only: driving mkcert/NSS is out of
	// `--fix`; it mutates OS/browser trust stores which is a `trust install` job).
	probes = append(probes, s.trustProbe(ctx))

	return probes
}

// netSharedProbe checks the pinned external bridge network exists; when it does
// not (and docker is reachable) the fix idempotently creates it under the lock.
// Classified `warn` (not critical): a fresh machine has no network until the
// first `up`, so a missing network must not gate the exit code — `--fix` (or the
// next `up`) creates it.
func (s *doctorSession) netSharedProbe(ctx context.Context) probe {
	c := docker.Check{Name: "shared network", ID: "net.shared", Category: catWarn}
	if s.client == nil {
		c.Status = docker.StatusWarn
		c.Detail = "docker unreachable; cannot inspect " + generate.SharedNetwork
		c.Remediation = "start Docker, then re-run `devstack doctor`"
		return plain(c)
	}
	exists, err := s.client.NetworkExists(ctx, generate.SharedNetwork)
	switch {
	case err != nil:
		c.Status = docker.StatusWarn
		c.Detail = err.Error()
		c.Remediation = "verify the active docker context, then `devstack doctor --fix`"
		return plain(c)
	case exists:
		c.Status = docker.StatusOK
		c.Detail = generate.SharedNetwork + " present"
		return plain(c)
	default:
		c.Status = docker.StatusWarn
		c.Fixable = true
		c.Detail = "network " + generate.SharedNetwork + " not found"
		c.Remediation = "run `devstack doctor --fix` to create the external network"
	}
	return probe{
		check: c,
		fix: func(ctx context.Context) error {
			return lock.WithLock(ctx, s.lockPath, func() error {
				return s.client.EnsureNetwork(ctx, generate.SharedNetwork, map[string]string{
					generate.LabelManaged: "true",
				})
			})
		},
		recheck: func(ctx context.Context) docker.Check { return s.netSharedProbe(ctx).check },
	}
}

// stateRefsProbe detects ledger ref rows whose project has no live container and,
// when any exist, fixes them via the self-healing reconcile (which takes the
// flock internally and prunes only derived rows — never data).
func (s *doctorSession) stateRefsProbe(ctx context.Context) probe {
	c := docker.Check{Name: "ledger refs", ID: "state.refs", Category: catWarn}
	switch {
	case s.db == nil:
		c.Status = docker.StatusWarn
		c.Detail = "state ledger unavailable; cannot reconcile ref rows"
		c.Remediation = "resolve the state-ledger probe first"
		return plain(c)
	case s.client == nil:
		c.Status = docker.StatusWarn
		c.Detail = "docker unreachable; cannot compare ref rows against live containers"
		c.Remediation = "start Docker, then `devstack doctor --fix`"
		return plain(c)
	}
	stale, err := s.staleRefs(ctx)
	if err != nil {
		c.Status = docker.StatusWarn
		c.Detail = err.Error()
		c.Remediation = "verify the docker context and ledger"
		return plain(c)
	}
	if len(stale) == 0 {
		c.Status = docker.StatusOK
		c.Detail = "no stale ref rows"
		return plain(c)
	}
	c.Status = docker.StatusWarn
	c.Fixable = true
	c.Detail = fmt.Sprintf("%d stale ref row(s) for project(s) with no live container", len(stale))
	c.Remediation = "run `devstack doctor --fix` to prune stale ref rows"
	mgr := s.manager()
	return probe{
		check: c,
		fix: func(ctx context.Context) error {
			_, err := mgr.Reconcile(ctx) // takes the flock internally
			return err
		},
		recheck: func(ctx context.Context) docker.Check { return s.stateRefsProbe(ctx).check },
	}
}

// staleRefs returns the ledger ref rows whose project has no running container
// (the label-filtered live set, All=true / one-offs excluded per ListManaged).
func (s *doctorSession) staleRefs(ctx context.Context) ([]state.Ref, error) {
	containers, err := s.client.ListManaged(ctx, map[string]string{generate.LabelManaged: "true"})
	if err != nil {
		return nil, fmt.Errorf("list managed containers: %w", err)
	}
	live := map[string]bool{}
	for _, ct := range containers {
		if p := ct.Labels[generate.LabelProject]; p != "" && ct.Running() {
			live[p] = true
		}
	}
	refs, err := s.db.AllRefs()
	if err != nil {
		return nil, err
	}
	var stale []state.Ref
	for _, r := range refs {
		if !live[r.Project] {
			stale = append(stale, r)
		}
	}
	return stale, nil
}

// xdgDirsProbe verifies the private XDG dirs exist and are not group/other
// writable (SQLite ledger + flock integrity). The fix creates missing dirs and
// tightens perms to 0700 — reconstructible and non-destructive.
func (s *doctorSession) xdgDirsProbe() probe {
	c := docker.Check{Name: "xdg dirs", ID: "fs.xdg", Category: catWarn}
	dirs := []string{xdg.DataHome(), xdg.StateHome(), xdg.ConfigHome()}
	var bad []string
	for _, d := range dirs {
		if !dirSecure(d) {
			bad = append(bad, d)
		}
	}
	if len(bad) == 0 {
		c.Status = docker.StatusOK
		c.Detail = "data/state/config dirs present (0700)"
		return plain(c)
	}
	c.Status = docker.StatusWarn
	c.Fixable = true
	c.Detail = fmt.Sprintf("%d XDG dir(s) missing or group/world-writable", len(bad))
	c.Remediation = "run `devstack doctor --fix` to create them with 0700 permissions"
	return probe{
		check: c,
		fix: func(context.Context) error {
			for _, d := range bad {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return fmt.Errorf("create %s: %w", d, err)
				}
				if err := os.Chmod(d, 0o700); err != nil {
					return fmt.Errorf("chmod %s: %w", d, err)
				}
			}
			return nil
		},
		recheck: func(context.Context) docker.Check { return s.xdgDirsProbe().check },
	}
}

// dnsProbe reports whether the *.localhost /etc/hosts fence covers a proxied
// workspace's routes. Diagnose-only (the write needs sudo) — the second return
// is false when the cwd is not a proxied workspace.
func (s *doctorSession) dnsProbe() (probe, bool) {
	if s.model == nil || !proxy.Enabled(s.model) {
		return probe{}, false
	}
	var hosts []string
	for _, r := range proxy.BuildRoutes(s.model) {
		hosts = append(hosts, r.Host)
	}
	c := docker.Check{Name: "dns (/etc/hosts)", ID: "dns.resolver", Category: catWarn}
	missing, err := dns.Missing(dns.DefaultHostsPath, hosts)
	if err != nil {
		c.Status = docker.StatusWarn
		c.Detail = err.Error()
		c.Remediation = fmt.Sprintf("run `%s`", sudoSelfCmd("dns setup"))
		return plain(c), true
	}
	if len(missing) == 0 {
		c.Status = docker.StatusOK
		c.Detail = fmt.Sprintf("%d *.localhost host(s) resolved", len(hosts))
		return plain(c), true
	}
	c.Status = docker.StatusWarn
	c.Detail = fmt.Sprintf("%d of %d *.localhost host(s) missing", len(missing), len(hosts))
	c.Remediation = fmt.Sprintf("run `%s` (a marker-fenced /etc/hosts write needs root)", sudoSelfCmd("dns setup"))
	return plain(c), true
}

// sudoSelfCmd renders a copy-pasteable "sudo <abs-binary> <sub>" remediation.
// The ABSOLUTE path is load-bearing: sudo resets PATH to its secure_path (which
// on most systems excludes ~/.local/bin), so a bare `sudo devstack …` fails with
// "command not found" for a user-local install. os.Executable() resolves the
// real running binary; it falls back to the bare name only if that lookup fails.
func sudoSelfCmd(sub string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "devstack"
	}
	return fmt.Sprintf("sudo %s %s", exe, sub)
}

// trustProbe reports local-CA readiness. Diagnose-only: driving mkcert/NSS
// mutates OS/browser trust stores, which is `trust install`, not `--fix`.
func (s *doctorSession) trustProbe(ctx context.Context) probe {
	ts := trust.New().Status(ctx)
	if ts.OK() {
		return plain(docker.Check{
			Name: "trust (mkcert)", ID: "trust.host", Category: catWarn,
			Status: docker.StatusOK, Detail: "local CA installed (" + ts.CARoot + ")",
		})
	}
	return plain(docker.Check{
		Name: "trust (mkcert)", ID: "trust.host", Category: catWarn,
		Status:      docker.StatusWarn,
		Detail:      fmt.Sprintf("mkcert=%v CA=%v firefox=%v", ts.MkcertFound, ts.CAInstalled, ts.FirefoxTrust),
		Remediation: ts.Remediation,
	})
}

// rebuildLedger reconstructs the shared_service + ref ledger from on-disk config
// intersected with live container labels (spec 09 §crash-recovery).
func rebuildLedger(cmd *cobra.Command, g *GlobalOpts) error {
	mgr, closeFn, err := buildManager(cmd)
	if err != nil {
		return err
	}
	defer closeFn()
	sum, err := mgr.RebuildState(cmd.Context())
	if err != nil {
		return err
	}
	if g.JSON {
		return writeJSON(cmd, sum)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"rebuilt ledger from live labels: %d shared service(s), %d ref row(s)\n",
		len(sum.Shared), sum.Refs)
	return nil
}

// plain wraps a diagnose-only check (no fix) as a probe.
func plain(c docker.Check) probe { return probe{check: c} }

// withCategory stamps a category onto a check that lacks one (e.g. from Preflight).
func withCategory(c docker.Check, cat string) docker.Check {
	if c.Category == "" {
		c.Category = cat
	}
	return c
}

// errText renders err, or a fallback when nil.
func errText(err error) string {
	if err == nil {
		return "unavailable"
	}
	return err.Error()
}

// dirSecure reports whether dir exists, is a directory, and is not group/other
// writable — the integrity bar for the SQLite ledger and flock file.
func dirSecure(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o022 == 0
}

func renderChecks(cmd *cobra.Command, checks []docker.Check, quiet bool) {
	w := cmd.OutOrStdout()
	for _, c := range checks {
		if quiet && c.Status == docker.StatusOK {
			continue
		}
		var icon string
		switch c.Status {
		case docker.StatusOK:
			icon = "✓"
		case docker.StatusWarn:
			icon = "!"
		default:
			icon = "✗"
		}
		fixed := ""
		if c.Fixed {
			fixed = " (fixed)"
		}
		fmt.Fprintf(w, "%s %-32s %s%s\n", icon, c.Name, c.Detail, fixed)
		if c.Status != docker.StatusOK && c.Remediation != "" {
			fmt.Fprintf(w, "    → %s\n", c.Remediation)
		}
	}
}

func renderFixes(cmd *cobra.Command, fixes []fixResult) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "\n--fix: applying safe remediations…")
	if len(fixes) == 0 {
		fmt.Fprintln(w, "  nothing to fix (no fixable check was failing)")
		return
	}
	for _, f := range fixes {
		if f.Fixed {
			fmt.Fprintf(w, "  ✓ %s: fixed — %s\n", f.ID, f.Detail)
			continue
		}
		fmt.Fprintf(w, "  ✗ %s: still failing — %s\n", f.ID, f.Detail)
		if f.Remediation != "" {
			fmt.Fprintf(w, "      → %s\n", f.Remediation)
		}
	}
}

// fsCheck warns when dir is backed by a 9p/networked filesystem where SQLite and
// flock locking are unreliable (spec 08).
func fsCheck(name, id, dir, remediation string) docker.Check {
	fsType := xdg.FilesystemType(dir)
	switch {
	case fsType == "":
		return docker.Check{Name: name, ID: id, Category: catWarn, Status: docker.StatusOK, Detail: dir}
	case xdg.IsUnreliableLockFS(fsType):
		return docker.Check{
			Name: name, ID: id, Category: catWarn, Status: docker.StatusWarn,
			Detail:      fmt.Sprintf("%s is on %q where locking is unreliable", dir, fsType),
			Remediation: remediation,
		}
	default:
		return docker.Check{Name: name, ID: id, Category: catWarn, Status: docker.StatusOK, Detail: fmt.Sprintf("%s (%s)", dir, fsType)}
	}
}

// countFails counts only FAIL-status checks (warns never gate the exit code, per
// the spec 13 exit-code contract).
func countFails(checks []docker.Check) int {
	n := 0
	for _, c := range checks {
		if c.Status == docker.StatusFail {
			n++
		}
	}
	return n
}
