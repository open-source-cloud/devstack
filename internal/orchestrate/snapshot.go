package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-source-cloud/devstack/internal/db"
	"github.com/open-source-cloud/devstack/internal/generate"
	"github.com/open-source-cloud/devstack/internal/lock"
	"github.com/open-source-cloud/devstack/internal/store"
)

// This file is the imperative side of spec 15 (thin v2 scope): Postgres-only
// `db snapshot` / `db restore` / `db snapshot ls` against a project's per-project
// tenant database on the SHARED Postgres. It reuses the provision phase's exact
// host-reachability pattern (engineTarget → FreeHostPort + writeProvisionOverlay
// + `compose up -d <inst>` on the shared stack, DECISIONS D8) so the dump/restore
// client tooling reaches the warm server over a ledger-allocated 127.0.0.1 host
// port WITHOUT publishing a permanent one.
//
// Lock discipline (spec 15): the streaming dump/restore PROCESS runs OUTSIDE the
// flock (it is long — holding the lock for a multi-GB pg_restore would serialize
// every other invocation). Only the ledger row writes + the port allocation run
// inside the flock (FreeHostPort self-locks; the snapshot row write is wrapped).

// snapshotKind is the free-text provisioned-ledger kind for a captured dump. The
// kind column is free-text so no migration is needed (spec 15 / task note).
const snapshotKind = "snapshot"

// SnapshotOptions selects the tenant to capture.
type SnapshotOptions struct {
	Project  string // owner project (default: the workspace's single/first project)
	Database string // physical tenant db (default: the project's own <project> db)
	Instance string // shared Postgres instance (default: the first postgres instance)
	Name     string // human label (default: a timestamp label)
}

// RestoreOptions selects the tenant + snapshot to replay.
type RestoreOptions struct {
	Project  string
	Database string
	Instance string
	Name     string // required: the snapshot label to restore
	Force    bool   // replay over a non-empty tenant (destructive)
}

// SnapshotMeta is the on-disk + ledger record of one captured dump. It is written
// as a sidecar JSON next to the dump and surfaced verbatim by `db snapshot ls`.
type SnapshotMeta struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	Kind      string `json:"kind"`     // pg (this milestone)
	Instance  string `json:"instance"` // shared instance captured from
	Database  string `json:"database"` // physical tenant db
	Digest    string `json:"digest"`   // sha256 of the dump bytes
	Size      int64  `json:"size"`     // dump bytes
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"` // absolute dump path
}

// pgTenantDB maps a project name to its default tenant database identifier
// (hyphens → underscores), matching provision.EnsureProject's naming.
func pgTenantDB(project string) string { return strings.ReplaceAll(project, "-", "_") }

// resolveTenant fills in the (project, database, instance) defaults and validates
// that a shared Postgres instance exists.
func resolveTenant(d UpDeps, project, database, instance string) (proj, dbName, inst string, err error) {
	proj = project
	if proj == "" {
		if names := sortedProjects(d.Model); len(names) > 0 {
			proj = names[0]
		}
	}
	if proj == "" {
		return "", "", "", fmt.Errorf("no project in this workspace to snapshot")
	}
	if _, ok := d.Model.Projects[proj]; !ok {
		return "", "", "", fmt.Errorf("project %q is not in this workspace", proj)
	}
	inst = instance
	if inst == "" {
		var ok bool
		inst, ok = ResolveInstance(d.Model, "postgres")
		if !ok {
			return "", "", "", fmt.Errorf("no shared postgres instance in this workspace (declare one under workspace.shared)")
		}
	} else if d.Model.Workspace.Shared[inst].Template != "postgres" {
		return "", "", "", fmt.Errorf("shared instance %q is not a postgres engine", inst)
	}
	dbName = database
	if dbName == "" {
		dbName = pgTenantDB(proj)
	}
	return proj, dbName, inst, nil
}

// tenantConn resolves the host-reachable admin endpoint for the tenant, reusing
// the provision overlay (allocates/looks up the ledger port, applies the loopback
// overlay via compose up). Returns the ConnInfo the dumper connects with.
func tenantConn(ctx context.Context, d UpDeps, inst, dbName string) (db.ConnInfo, error) {
	target, err := engineTarget(ctx, d, "postgres", inst)
	if err != nil {
		return db.ConnInfo{}, err
	}
	return db.ConnInfo{
		Host:     target.Host,
		Port:     target.Port,
		User:     target.AdminEnv["user"],
		Password: target.AdminEnv["password"],
		Database: dbName,
	}, nil
}

// Snapshot captures the project's tenant database to the workspace snapshot store
// and records a ledger row. The dump streams OUTSIDE the flock; only the ledger
// write is locked (spec 15).
func Snapshot(ctx context.Context, d UpDeps, dumper db.Dumper, opt SnapshotOptions) (SnapshotMeta, error) {
	proj, dbName, inst, err := resolveTenant(d, opt.Project, opt.Database, opt.Instance)
	if err != nil {
		return SnapshotMeta{}, err
	}
	name := opt.Name
	if name == "" {
		name = time.Now().UTC().Format("20060102-150405")
	}
	if err := validSnapshotName(name); err != nil {
		return SnapshotMeta{}, err
	}

	conn, err := tenantConn(ctx, d, inst, dbName)
	if err != nil {
		return SnapshotMeta{}, err
	}

	dir := store.SnapshotsPath(d.Model.Workspace.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SnapshotMeta{}, fmt.Errorf("create snapshot store: %w", err)
	}
	dumpPath := filepath.Join(dir, name+".dump")

	// The dump PROCESS runs outside the flock (spec 15 — long-running).
	if err := dumper.Snapshot(ctx, conn, dumpPath); err != nil {
		return SnapshotMeta{}, err
	}

	digest, size, err := hashFile(dumpPath)
	if err != nil {
		return SnapshotMeta{}, err
	}
	meta := SnapshotMeta{
		Name: name, Project: proj, Kind: "pg", Instance: inst, Database: dbName,
		Digest: digest, Size: size, CreatedAt: time.Now().UTC().Format(time.RFC3339), Path: dumpPath,
	}
	if err := writeSidecar(dir, meta); err != nil {
		return SnapshotMeta{}, err
	}

	// Ledger row + event, inside the flock (fast).
	if err := lock.WithLock(ctx, d.LockPath, func() error {
		if err := d.DB.RecordProvisioned(proj, snapshotKind, name); err != nil {
			return err
		}
		d.DB.LogEvent("db.snapshot", proj, fmt.Sprintf("%s of %s (%s, %d bytes)", name, dbName, generate.SharedAlias(inst), size))
		return nil
	}); err != nil {
		return SnapshotMeta{}, err
	}
	return meta, nil
}

// Restore replays a stored snapshot into the project's tenant database. It refuses
// a non-empty tenant unless opt.Force (data-loss guard, spec 15). The pg_restore
// PROCESS runs outside the flock; the event row write is locked.
func Restore(ctx context.Context, d UpDeps, dumper db.Dumper, opt RestoreOptions) (SnapshotMeta, error) {
	if opt.Name == "" {
		return SnapshotMeta{}, fmt.Errorf("a snapshot name is required")
	}
	proj, dbName, inst, err := resolveTenant(d, opt.Project, opt.Database, opt.Instance)
	if err != nil {
		return SnapshotMeta{}, err
	}
	dir := store.SnapshotsPath(d.Model.Workspace.Name)
	meta, err := readSidecar(dir, opt.Name)
	if err != nil {
		return SnapshotMeta{}, err
	}
	if _, statErr := os.Stat(meta.Path); statErr != nil {
		return SnapshotMeta{}, fmt.Errorf("snapshot dump %q is missing: %w", meta.Path, statErr)
	}
	// Integrity: re-hash the dump and hard-fail on mismatch (spec 15).
	digest, _, err := hashFile(meta.Path)
	if err != nil {
		return SnapshotMeta{}, err
	}
	if meta.Digest != "" && digest != meta.Digest {
		return SnapshotMeta{}, fmt.Errorf("snapshot %q is corrupted: digest %s does not match recorded %s", opt.Name, digest, meta.Digest)
	}

	conn, err := tenantConn(ctx, d, inst, dbName)
	if err != nil {
		return SnapshotMeta{}, err
	}

	if !opt.Force {
		empty, err := dumper.IsEmpty(ctx, conn)
		if err != nil {
			return SnapshotMeta{}, err
		}
		if !empty {
			return SnapshotMeta{}, fmt.Errorf("refusing to restore over non-empty database %q (data would be lost); pass --force to overwrite", dbName)
		}
	}

	// The restore PROCESS runs outside the flock (spec 15 — long-running).
	if err := dumper.Restore(ctx, conn, meta.Path); err != nil {
		return SnapshotMeta{}, err
	}

	if err := lock.WithLock(ctx, d.LockPath, func() error {
		d.DB.LogEvent("db.restore", proj, fmt.Sprintf("%s into %s (%s, digest %s)", opt.Name, dbName, generate.SharedAlias(inst), digest))
		return nil
	}); err != nil {
		return SnapshotMeta{}, err
	}
	return meta, nil
}

// ListSnapshots returns the project's captured snapshots (lock-free): the ledger
// rows of kind=snapshot enriched with each dump's on-disk sidecar metadata. A
// missing sidecar still yields a row (name only) so a partially-removed store is
// visible rather than hidden.
func ListSnapshots(d UpDeps, project string) ([]SnapshotMeta, error) {
	proj := project
	if proj == "" {
		if names := sortedProjects(d.Model); len(names) > 0 {
			proj = names[0]
		}
	}
	rows, err := d.DB.ProvisionedFor(proj)
	if err != nil {
		return nil, err
	}
	dir := store.SnapshotsPath(d.Model.Workspace.Name)
	var out []SnapshotMeta
	for _, r := range rows {
		if r.Kind != snapshotKind {
			continue
		}
		meta, err := readSidecar(dir, r.Name)
		if err != nil {
			// Sidecar gone — surface the bare ledger row.
			meta = SnapshotMeta{Name: r.Name, Project: proj, Kind: "pg", CreatedAt: r.CreatedAt}
		}
		out = append(out, meta)
	}
	return out, nil
}

// --- helpers ---------------------------------------------------------------

func sidecarPath(dir, name string) string { return filepath.Join(dir, name+".json") }

func writeSidecar(dir string, meta SnapshotMeta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sidecarPath(dir, meta.Name), b, 0o644)
}

func readSidecar(dir, name string) (SnapshotMeta, error) {
	b, err := os.ReadFile(sidecarPath(dir, name))
	if err != nil {
		return SnapshotMeta{}, fmt.Errorf("no snapshot %q in the store: %w", name, err)
	}
	var meta SnapshotMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return SnapshotMeta{}, fmt.Errorf("read snapshot metadata %q: %w", name, err)
	}
	return meta, nil
}

// hashFile returns the sha256 hex digest + byte size of a file.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open dump %q: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash dump %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// validSnapshotName rejects path-traversal and separators in the label so it maps
// cleanly to a single file in the store.
func validSnapshotName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid snapshot name %q (no slashes or '..')", name)
	}
	return nil
}
