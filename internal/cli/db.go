package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/state"
)

// newDbCmd wires the `devstack db` group (spec 29 §databases): tenant-scoped
// Postgres database + role/grant verbs on the shared engine. create/user/grant/
// drop/gc mirror the up-saga provision flow (lock → overlay → provisioner →
// ledger → event) via internal/orchestrate; list is a lock-free ledger read.
// snapshot/restore (+ snapshot ls) plus reset/pull graduate the full spec-15
// data-lifecycle surface (reset = drop + re-provision an empty tenant; pull =
// fetch-by-name from the LOCAL snapshot store + restore — the remote/team shared
// store is deferred to spec 21).
func newDbCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Tenant-scoped databases, roles and grants on the shared Postgres",
	}
	cmd.AddCommand(
		newDbCreateCmd(g),
		newDbUserCmd(g),
		newDbGrantCmd(g),
		newDbListCmd(g),
		newDbDropCmd(g),
		newDbGcCmd(g),
		// spec-15 data-lifecycle verbs.
		newDbSnapshotCmd(g),
		newDbRestoreCmd(g),
		newDbResetCmd(g),
		newDbPullCmd(g),
	)
	return cmd
}

// newDbSnapshotCmd wires `db snapshot [name]` (capture) with the `ls` subcommand
// (list). A snapshot dumps ONLY the project's tenant namespace on the shared engine
// selected by --kind (pg|redis|minio) to ~/.devstack/snapshots/<workspace>/ and
// records a ledger row (spec 15).
func newDbSnapshotCmd(g *GlobalOpts) *cobra.Command {
	var project, database, instance, kind string
	cmd := &cobra.Command{
		Use:   "snapshot [name]",
		Short: "Capture a project's tenant namespace (pg|redis|minio) to the snapshot store",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			dumper, err := orchestrate.SelectDumper(d, kind)
			if err != nil {
				return err
			}
			if err := dumper.Preflight(cmd.Context()); err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			meta, err := orchestrate.Snapshot(cmd.Context(), d, dumper, orchestrate.SnapshotOptions{
				Kind: kind, Project: project, Database: database, Instance: instance, Name: name,
			})
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, meta)
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "captured %s snapshot %q of %s (%d bytes)\n%s\n", meta.Kind, meta.Name, meta.Database, meta.Size, meta.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "pg", "engine: pg|redis|minio")
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringVar(&database, "db", "", "tenant namespace: pg db / redis index / minio bucket (default: derived)")
	cmd.Flags().StringVar(&instance, "instance", "", "shared instance (default: the first instance of the engine)")
	cmd.AddCommand(newDbSnapshotLsCmd(g))
	return cmd
}

func newDbSnapshotLsCmd(g *GlobalOpts) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List captured snapshots (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			snaps, err := orchestrate.ListSnapshots(d, project)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"snapshots": snaps})
			}
			w := cmd.OutOrStdout()
			if len(snaps) == 0 {
				fmt.Fprintln(w, "no snapshots")
				return nil
			}
			for _, s := range snaps {
				fmt.Fprintf(w, "%-24s %-12s %10d  %s  %s\n", s.Name, s.Database, s.Size, shortDigest(s.Digest), s.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project's snapshots")
	return cmd
}

func newDbRestoreCmd(g *GlobalOpts) *cobra.Command {
	var project, database, instance, kind string
	var force, yes bool
	cmd := &cobra.Command{
		Use:   "restore <name>",
		Short: "Restore a project's tenant namespace (pg|redis|minio) from a snapshot (destructive)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to restore without --yes for --json/non-interactive use")
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			dumper, err := orchestrate.SelectDumper(d, kind)
			if err != nil {
				return err
			}
			if err := dumper.Preflight(cmd.Context()); err != nil {
				return err
			}
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This REPLACES the tenant namespace from snapshot %q (current data destroyed). Type 'yes' to continue: ", args[0])) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			meta, err := orchestrate.Restore(cmd.Context(), d, dumper, orchestrate.RestoreOptions{
				Kind: kind, Project: project, Database: database, Instance: instance, Name: args[0], Force: force,
			})
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, meta)
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "restored %s from snapshot %q (digest %s)\n", meta.Database, meta.Name, shortDigest(meta.Digest))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "pg", "engine: pg|redis|minio")
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringVar(&database, "db", "", "tenant namespace: pg db / redis index / minio bucket (default: derived)")
	cmd.Flags().StringVar(&instance, "instance", "", "shared instance (default: the first instance of the engine)")
	cmd.Flags().BoolVar(&force, "force", false, "replay over a non-empty namespace (overwrite existing data)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (required for --json)")
	return cmd
}

// newDbResetCmd wires `db reset` (spec 15): DROP and re-provision a project's
// tenant database on the shared Postgres to a clean, empty tenant. Destructive —
// it refuses without --yes; --force overrides a still-connected database
// (terminating its live backends). The drop + re-provision DDL runs under the
// flock; the never-recreate-a-stateful-shared-service guard holds (only the
// tenant DATABASE is dropped, never the shared container).
func newDbResetCmd(g *GlobalOpts) *cobra.Command {
	var project, instance string
	var yes, force bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Drop and re-provision a project's tenant database to empty (destructive)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to reset without --yes for --json/non-interactive use")
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This DROPS and recreates %q's tenant database EMPTY (all data destroyed). Type 'yes' to continue: ", proj)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			res, err := orchestrate.Reset(cmd.Context(), d, orchestrate.ResetOptions{
				Project: project, Instance: instance, Force: force,
			})
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, res)
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "reset %s: dropped and recreated empty database %q owned by %q\n", res.Project, res.Database, res.Role)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringVar(&instance, "instance", "", "shared postgres instance (default: the first postgres instance)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (required for --json)")
	cmd.Flags().BoolVar(&force, "force", false, "terminate live connections and reset a still-connected database")
	return cmd
}

// newDbPullCmd wires `db pull <name>` (spec 15): fetch a named snapshot from the
// LOCAL snapshot store and apply it into the project's tenant, seeding it from a
// real dataset. In this scope the store is local ($DEVSTACK_HOME/snapshots), so a
// pull is a fetch-by-name + restore that refuses to clobber a non-empty tenant
// (run `db reset` first to re-seed). The REMOTE/team "shared store" (S3/HTTP fetch
// + the mandatory sanitize transform) is DEFERRED to spec 21.
func newDbPullCmd(g *GlobalOpts) *cobra.Command {
	var project, database, instance string
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Seed a project's tenant from a named snapshot in the local store",
		Long: "Fetch a named snapshot from the local snapshot store ($DEVSTACK_HOME/snapshots) " +
			"and apply it into the project's tenant database, seeding a fresh tenant. " +
			"Refuses to overwrite a non-empty tenant (run `db reset` first to re-seed). " +
			"The remote/team shared store (fetch-by-URL + sanitize) is deferred to spec 21.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			dumper := defaultPgDumper()
			if err := dumper.Preflight(cmd.Context()); err != nil {
				return err
			}
			meta, err := orchestrate.Pull(cmd.Context(), d, dumper, orchestrate.PullOptions{
				Project: project, Database: database, Instance: instance, Name: args[0],
			})
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, meta)
			}
			if !g.Quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "pulled snapshot %q into %s (digest %s)\n", meta.Name, meta.Database, shortDigest(meta.Digest))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringVar(&database, "db", "", "physical tenant database (default: the project's own database)")
	cmd.Flags().StringVar(&instance, "instance", "", "shared postgres instance (default: the first postgres instance)")
	return cmd
}

// shortDigest is a display helper: the first 12 hex chars of a sha256, or "-".
func shortDigest(d string) string {
	if len(d) >= 12 {
		return d[:12]
	}
	if d == "" {
		return "-"
	}
	return d
}

// sanitizePg maps a name to a safe Postgres identifier (hyphens → underscores),
// matching provision.pgIdent so ledger + physical names line up.
func sanitizePg(s string) string { return strings.ReplaceAll(s, "-", "_") }

// pgPrefixed computes the tenant-scoped physical identifier: <project>_<name>
// (spec 29 §tenant naming), unless --no-prefix keeps the literal name.
func pgPrefixed(project, name string, noPrefix bool) string {
	if noPrefix {
		return sanitizePg(name)
	}
	return sanitizePg(project + "_" + name)
}

func newDbCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, owner string
	var noPrefix bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant database owned by the project role (idempotent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			ownerRole := owner
			if ownerRole == "" {
				ownerRole = sanitizePg(proj)
			}
			physical := pgPrefixed(proj, args[0], noPrefix)
			r := resource.Resource{
				Engine: "postgres", Kind: "database", Name: physical, Owner: proj,
				Params: map[string]any{"owner": ownerRole}, CredKind: resource.CredPredictable,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s", attrs["user"], attrs["password"], attrs["host"], attrs["port"], attrs["database"])
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"kind": "database", "name": physical, "project": proj, "owner": ownerRole, "dsn": dsn,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created database %q owned by %q\n%s\n", physical, ownerRole, dsn)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringVar(&owner, "owner", "", "owning role (default: the project role)")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>_ prefix)")
	return cmd
}

// newDbUserCmd hosts `db user create`.
func newDbUserCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage tenant login roles"}
	cmd.AddCommand(newDbUserCreateCmd(g))
	return cmd
}

func newDbUserCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, db, role, password string
	var generate, noPrefix bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant login role, optionally granted on a database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if role == "" {
				role = "read"
			}
			if !validGrantLevel(role) {
				return fmt.Errorf("invalid --role %q (want read|write|admin)", role)
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			physical := pgPrefixed(proj, args[0], noPrefix)
			params := map[string]any{"level": role}
			if db != "" {
				params["db"] = db
			}
			cred := resource.CredPredictable
			if generate {
				cred = resource.CredGenerated
			}
			if password != "" {
				params["password"] = password
			}
			r := resource.Resource{
				Engine: "postgres", Kind: "role", Name: physical, Owner: proj,
				Params: params, CredKind: cred,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				out := map[string]any{"kind": "role", "name": physical, "project": proj, "role": role}
				if db != "" {
					out["database"] = attrs["database"]
				}
				if generate {
					out["password"] = attrs["password"]
				}
				return writeJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "created role %q (%s)\n", physical, role)
			if generate {
				fmt.Fprintf(w, "password: %s\n", attrs["password"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&db, "db", "", "database to grant the role on (physical name)")
	cmd.Flags().StringVar(&role, "role", "read", "privilege tier: read|write|admin")
	cmd.Flags().StringVar(&password, "password", "", "explicit password (default: predictable dev cred == role name)")
	cmd.Flags().BoolVar(&generate, "generate", false, "mint a random password via crypto/rand")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal role name (skip the <project>_ prefix)")
	return cmd
}

func newDbGrantCmd(g *GlobalOpts) *cobra.Command {
	var project, on, as string
	cmd := &cobra.Command{
		Use:   "grant <role>",
		Short: "Grant a privilege tier to an existing role on a database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if on == "" {
				return fmt.Errorf("--on <db> is required")
			}
			if as == "" {
				as = "read"
			}
			if !validGrantLevel(as) {
				return fmt.Errorf("invalid --as %q (want read|write|admin)", as)
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			// The role argument is the literal (already-tenant-scoped) role name.
			r := resource.Resource{
				Engine: "postgres", Kind: "role", Name: sanitizePg(args[0]), Owner: proj,
				Params:   map[string]any{"db": on, "level": as, "grant_only": "1"},
				CredKind: resource.CredPredictable,
			}
			if _, err := orchestrate.CreateResource(cmd.Context(), d, r); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"granted": as, "role": sanitizePg(args[0]), "on": sanitizePg(on)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "granted %s on %q to %q\n", as, sanitizePg(on), sanitizePg(args[0]))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&on, "on", "", "database to grant on (physical name)")
	cmd.Flags().StringVar(&as, "as", "read", "privilege tier: read|write|admin")
	return cmd
}

func newDbListCmd(g *GlobalOpts) *cobra.Command {
	var project, kind string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List provisioned databases and roles (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			wantKind := dbKindAlias(kind)
			var rows []state.Provisioned
			if project != "" {
				rows, err = mgr.DB.ProvisionedFor(project)
			} else {
				rows, err = mgr.DB.AllProvisioned()
			}
			if err != nil {
				return err
			}
			var filtered []state.Provisioned
			for _, r := range rows {
				if r.Kind != "database" && r.Kind != "role" {
					continue
				}
				if wantKind != "" && r.Kind != wantKind {
					continue
				}
				filtered = append(filtered, r)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"databases": filtered})
			}
			w := cmd.OutOrStdout()
			if len(filtered) == 0 {
				fmt.Fprintln(w, "no databases or roles provisioned")
				return nil
			}
			for _, r := range filtered {
				fmt.Fprintf(w, "%-10s %-10s %-24s %s\n", r.Project, r.Kind, r.Name, r.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project's databases/roles")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind: db|role")
	return cmd
}

func newDbDropCmd(g *GlobalOpts) *cobra.Command {
	var project, kind string
	var yes, noPrefix bool
	cmd := &cobra.Command{
		Use:   "drop <name>",
		Short: "Drop a tenant database or role (destructive; confirm required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to drop without --yes for --json/non-interactive use")
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			realKind := dbKindAlias(kind)
			if realKind == "" {
				realKind = "database"
			}
			physical := pgPrefixed(proj, args[0], noPrefix)
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This DROPS %s %q (data destroyed). Type 'yes' to continue: ", realKind, physical)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: "postgres", Kind: realKind, Name: physical, Owner: proj}
			if err := orchestrate.DropResource(cmd.Context(), d, r, true); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"dropped": map[string]string{"kind": realKind, "name": physical, "project": proj}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dropped %s %q (project %q)\n", realKind, physical, proj)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&kind, "kind", "", "db|role (default: db)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>_ prefix)")
	return cmd
}

// newDbGcCmd graduates the spec-13 `db gc` verb across ALL kinds (database, role,
// bucket, lifecycle, …): it reaps ledger rows whose owner project left the
// workspace, dropping the underlying engine object via the provisioner.
func newDbGcCmd(g *GlobalOpts) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim resources whose owner project left the workspace (all kinds)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to gc without --yes for --json/non-interactive use")
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			active := map[string]bool{}
			for _, p := range sortedProjectNames(d.Model) {
				active[p] = true
			}
			orphans, err := d.DB.OrphanedProvisioned(active)
			if err != nil {
				return err
			}
			if len(orphans) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no orphaned resources to reclaim")
				return nil
			}
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This DROPS %d orphaned resource(s) (data destroyed). Type 'yes' to continue: ", len(orphans))) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			res, err := orchestrate.GCResources(cmd.Context(), d, active)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, res)
			}
			w := cmd.OutOrStdout()
			for _, r := range res.Reaped {
				fmt.Fprintf(w, "reaped %s %s (project %s)\n", r["kind"], r["name"], r["project"])
			}
			for _, s := range res.Skipped {
				fmt.Fprintf(w, "skipped %s %s: %s\n", s["kind"], s["name"], s["reason"])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (required for --json)")
	return cmd
}

// dbKindAlias maps the user-facing `--kind db|role` to the stored ledger kind.
func dbKindAlias(k string) string {
	switch k {
	case "db", "database":
		return "database"
	case "role", "user":
		return "role"
	default:
		return ""
	}
}

func validGrantLevel(s string) bool {
	switch s {
	case "read", "write", "admin":
		return true
	}
	return false
}
