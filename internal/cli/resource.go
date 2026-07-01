package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
	"github.com/open-source-cloud/devstack/internal/state"
	"github.com/open-source-cloud/devstack/internal/workspace"
)

// newResourceCmd wires `devstack resource list|show|create|rm|gc` — the
// engine-agnostic data-plane resource verbs (spec 27). list/show are lock-free
// ledger reads; create/rm/gc mirror the up-saga provision flow (lock → overlay →
// provisioner → ledger → event) via internal/orchestrate. Secrets are masked
// unless --show-secrets.
func newResourceCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resource",
		Short: "Manage per-project resources inside shared engines (databases, buckets, …)",
	}
	cmd.AddCommand(
		newResourceListCmd(g),
		newResourceShowCmd(g),
		newResourceCreateCmd(g),
		newResourceRmCmd(g),
		newResourceGcCmd(g),
	)
	return cmd
}

// secretAttrKeys are attribute names whose value is a credential — masked in
// output unless --show-secrets (spec 27 §Credential surfacing).
var secretAttrKeys = map[string]bool{"password": true, "secret": true, "secretkey": true, "token": true}

func maskAttrs(a resource.Attrs, show bool) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		if secretAttrKeys[k] && !show {
			out[k] = "***"
			continue
		}
		out[k] = v
	}
	return out
}

func newResourceListCmd(g *GlobalOpts) *cobra.Command {
	var project, engine, kind string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List provisioned resources from the ownership ledger",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			var rows []state.Provisioned
			if project != "" {
				rows, err = mgr.DB.ProvisionedFor(project)
			} else {
				rows, err = mgr.DB.AllProvisioned()
			}
			if err != nil {
				return err
			}
			// kind filter (engine is derived, not stored; filter best-effort by kind).
			var filtered []state.Provisioned
			for _, r := range rows {
				if kind != "" && r.Kind != kind {
					continue
				}
				if engine != "" && !resource.SupportsKind(engine, r.Kind) {
					continue
				}
				filtered = append(filtered, r)
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"resources": filtered})
			}
			w := cmd.OutOrStdout()
			if len(filtered) == 0 {
				fmt.Fprintln(w, "no provisioned resources")
				return nil
			}
			for _, r := range filtered {
				fmt.Fprintf(w, "%-12s %-12s %-24s %s\n", r.Project, r.Kind, r.Name, r.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project's resources")
	cmd.Flags().StringVar(&engine, "engine", "", "filter by engine (postgres/redis/minio/…)")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (database/bucket/…)")
	return cmd
}

func newResourceShowCmd(g *GlobalOpts) *cobra.Command {
	var project, engine string
	var showSecrets bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show a resource's connection attributes (secrets masked unless --show-secrets)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			mgr, closeFn, err := buildManager(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			rows, err := mgr.DB.AllProvisioned()
			if err != nil {
				return err
			}
			var match *state.Provisioned
			for i := range rows {
				if rows[i].Name != name {
					continue
				}
				if project != "" && rows[i].Project != project {
					continue
				}
				match = &rows[i]
				break
			}
			if match == nil {
				return fmt.Errorf("no provisioned resource named %q", name)
			}
			if engine == "" {
				engine = mgr.Model.Workspace.Shared[firstSharedForKind(mgr, match.Kind)].Template
			}
			// Derive non-secret attrs from the ledger row + workspace model (no daemon).
			attrs := resource.Attrs{
				"project":  match.Project,
				"kind":     match.Kind,
				"name":     match.Name,
				"host":     "shared-" + engineInstanceName(mgr, engine),
				"password": match.Project, // predictable dev-cred (masked unless --show-secrets)
			}
			out := maskAttrs(attrs, showSecrets)
			if g.JSON {
				return writeJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			for _, k := range sortedKeysOf(out) {
				fmt.Fprintf(w, "%-10s %s\n", k, out[k])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "disambiguate by owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "engine hint for attribute derivation")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "print credential values (diagnostics only)")
	return cmd
}

func newResourceCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, credentials string
	var params []string
	cmd := &cobra.Command{
		Use:   "create <engine> <kind> <name>",
		Short: "Create a resource on a running shared engine (idempotent)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, kind, name := args[0], args[1], args[2]
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			owner := project
			if owner == "" {
				owner = defaultProject(d)
			}
			cred := resource.CredPredictable
			if credentials != "" {
				cred = resource.CredentialPolicy(credentials)
			}
			p, err := parseParams(params)
			if err != nil {
				return err
			}
			r := resource.Resource{Engine: engine, Kind: kind, Name: name, Owner: owner, Params: p, CredKind: cred}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"created": map[string]string{"engine": engine, "kind": kind, "name": name, "project": owner}, "attrs": maskAttrs(attrs, false)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s %s %q for project %q\n", engine, kind, name, owner)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project (default: the workspace's single/first project)")
	cmd.Flags().StringSliceVar(&params, "param", nil, "kind-specific parameter k=v (repeatable)")
	cmd.Flags().StringVar(&credentials, "credentials", "", "predictable|generated (default: predictable)")
	return cmd
}

func newResourceRmCmd(g *GlobalOpts) *cobra.Command {
	var project, engine string
	var purge, yes bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Un-track a resource (or --purge-data to also drop it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if purge && g.JSON && !yes {
				return fmt.Errorf("refusing to --purge-data without --yes for --json/non-interactive use")
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()

			owner := project
			if owner == "" {
				owner = defaultProject(d)
			}
			// Resolve the kind from the ledger (a resource is identified by name+owner).
			rows, err := d.DB.ProvisionedFor(owner)
			if err != nil {
				return err
			}
			var kind string
			for _, r := range rows {
				if r.Name == name && r.Kind != "role" {
					kind = r.Kind
					break
				}
			}
			if kind == "" {
				return fmt.Errorf("no provisioned resource named %q for project %q", name, owner)
			}
			if engine == "" {
				engine = engineForKindGuess(kind)
			}
			if purge && !yes {
				if !confirm(cmd, fmt.Sprintf("This DROPS %s %q (data is destroyed). Type 'yes' to continue: ", kind, name)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: engine, Kind: kind, Name: name, Owner: owner}
			if err := orchestrate.DropResource(cmd.Context(), d, r, purge); err != nil {
				return err
			}
			verb := "un-tracked"
			if purge {
				verb = "dropped"
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"removed": map[string]any{"name": name, "kind": kind, "project": owner, "purged": purge}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %q (project %q)\n", verb, kind, name, owner)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "engine (default: inferred from kind)")
	cmd.Flags().BoolVar(&purge, "purge-data", false, "also DROP the resource (destructive)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newResourceGcCmd(g *GlobalOpts) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim resources whose owner project left the workspace (destructive)",
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
			// Preview the orphans so the confirmation is informed.
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

// --- helpers ---------------------------------------------------------------

// defaultProject returns the workspace's single project, or the first by name.
func defaultProject(d orchestrate.UpDeps) string {
	names := sortedProjectNames(d.Model)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// engineForKindGuess maps a ledger kind back to its engine for the live set
// (postgres only this milestone). Ambiguous/unknown kinds default to postgres.
func engineForKindGuess(kind string) string {
	switch kind {
	case "bucket", "lifecycle", "access_key":
		return "minio"
	case "redis_index", "acl_user":
		return "redis"
	default:
		return "postgres"
	}
}

func engineInstanceName(mgr *workspace.Manager, engine string) string {
	if inst, ok := orchestrate.ResolveInstance(mgr.Model, engine); ok {
		return inst
	}
	return engine
}

func firstSharedForKind(mgr *workspace.Manager, kind string) string {
	engine := engineForKindGuess(kind)
	if inst, ok := orchestrate.ResolveInstance(mgr.Model, engine); ok {
		return inst
	}
	// Fall back to any shared name so the map lookup does not panic.
	for _, n := range mgr.Model.SharedNames() {
		return n
	}
	return ""
}

func parseParams(kv []string) (map[string]any, error) {
	if len(kv) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for _, pair := range kv {
		i := strings.IndexByte(pair, '=')
		if i <= 0 {
			return nil, fmt.Errorf("invalid --param %q (want k=v)", pair)
		}
		out[pair[:i]] = pair[i+1:]
	}
	return out, nil
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
