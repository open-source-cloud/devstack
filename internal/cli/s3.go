package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
)

// newS3Cmd wires the `devstack s3` group (spec 29 §object storage): tenant-scoped
// bucket lifecycle/versioning/policy/cors on the shared MinIO (LocalStack S3 is an
// endpoint swap later). mb/rb/mutations go through the flock via internal/
// orchestrate; ls/get are lock-free reads. Bucket names are project-PREFIXED for
// global uniqueness unless --no-prefix.
func newS3Cmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "s3",
		Short: "Tenant-scoped object-storage buckets on the shared MinIO",
	}
	cmd.AddCommand(
		newS3MbCmd(g),
		newS3RbCmd(g),
		newS3LsCmd(g),
		newS3LifecycleCmd(g),
		newS3VersioningCmd(g),
		newS3PolicyCmd(g),
		newS3CorsCmd(g),
	)
	return cmd
}

// bucketPrefixed computes the DNS-safe tenant-scoped bucket name <project>-<name>
// (spec 29 §tenant naming), unless --no-prefix keeps the literal name.
func bucketPrefixed(project, name string, noPrefix bool) string {
	if noPrefix {
		return name
	}
	return project + "-" + name
}

func newS3MbCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var versioning, noPrefix bool
	cmd := &cobra.Command{
		Use:   "mb <bucket>",
		Short: "Make a tenant bucket (idempotent)",
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
			bucket := bucketPrefixed(proj, args[0], noPrefix)
			params := map[string]any{}
			if versioning {
				params["versioning"] = true
			}
			r := resource.Resource{
				Engine: "minio", Kind: "bucket", Name: bucket, Owner: proj,
				Params: params, CredKind: resource.CredPredictable,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{
					"kind": "bucket", "name": bucket, "project": proj,
					"endpoint": attrs["endpoint"], "versioning": versioning,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "made bucket %q (%s)\n", bucket, attrs["endpoint"])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&versioning, "versioning", false, "enable object versioning")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal bucket name (skip the <project>- prefix)")
	return cmd
}

func newS3RbCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var force, yes, noPrefix bool
	cmd := &cobra.Command{
		Use:   "rb <bucket>",
		Short: "Remove a tenant bucket (destructive; confirm required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to remove a bucket without --yes for --json/non-interactive use")
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
			bucket := bucketPrefixed(proj, args[0], noPrefix)
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This REMOVES bucket %q (objects destroyed). Type 'yes' to continue: ", bucket)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: "minio", Kind: "bucket", Name: bucket, Owner: proj}
			if force {
				// Recursively purge every object before removing the bucket, so a
				// non-empty bucket can be deleted (the provisioner empties it first).
				r.Params = map[string]any{"force": true}
			}
			if err := orchestrate.DropResource(cmd.Context(), d, r, true); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"removed": map[string]string{"kind": "bucket", "name": bucket, "project": proj}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed bucket %q\n", bucket)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&force, "force", false, "remove even if the bucket is non-empty")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal bucket name (skip the <project>- prefix)")
	return cmd
}

func newS3LsCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var all bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the project's buckets (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			proj := project
			if proj == "" {
				proj = defaultProject(d)
			}
			prefix := proj + "-"
			if all {
				prefix = ""
			}
			names, err := orchestrate.ListBuckets(cmd.Context(), d, prefix)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"buckets": names})
			}
			w := cmd.OutOrStdout()
			if len(names) == 0 {
				fmt.Fprintln(w, "no buckets")
				return nil
			}
			for _, n := range names {
				fmt.Fprintln(w, n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&all, "all", false, "list every bucket, not just this project's")
	return cmd
}

func newS3LifecycleCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "lifecycle", Short: "Manage bucket object-lifecycle rules"}
	cmd.AddCommand(newS3LifecycleSetCmd(g), newS3LifecycleGetCmd(g), newS3LifecycleRmCmd(g))
	return cmd
}

func newS3LifecycleSetCmd(g *GlobalOpts) *cobra.Command {
	var project, transition, prefix string
	var expireDays int
	cmd := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Set an expiry (+optional transition) rule on a bucket",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rule := resource.LifecycleRule{ExpireDays: expireDays, Prefix: prefix}
			if transition != "" {
				days, tier, err := parseTransition(transition)
				if err != nil {
					return err
				}
				rule.TransitionDays, rule.TransitionTier = days, tier
			}
			if rule.ExpireDays <= 0 && rule.TransitionDays <= 0 {
				return fmt.Errorf("specify --expire-days N (and/or --transition days=N,tier=T)")
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
			if err := orchestrate.SetBucketLifecycle(cmd.Context(), d, proj, args[0], rule); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "expire_days": rule.ExpireDays, "transition_days": rule.TransitionDays, "transition_tier": rule.TransitionTier})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "lifecycle set on %q (expire=%dd)\n", args[0], rule.ExpireDays)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().IntVar(&expireDays, "expire-days", 0, "expire objects after N days")
	cmd.Flags().StringVar(&transition, "transition", "", "transition rule days=N,tier=STANDARD_IA")
	cmd.Flags().StringVar(&prefix, "prefix", "", "apply only to keys under this prefix")
	return cmd
}

func newS3LifecycleGetCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <bucket>",
		Short: "Show a bucket's lifecycle rules (lock-free)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			rules, err := orchestrate.GetBucketLifecycle(cmd.Context(), d, args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"rules": rules})
			}
			w := cmd.OutOrStdout()
			if len(rules) == 0 {
				fmt.Fprintln(w, "no lifecycle rules")
				return nil
			}
			for _, r := range rules {
				fmt.Fprintf(w, "%v\n", r)
			}
			return nil
		},
	}
	return cmd
}

func newS3LifecycleRmCmd(g *GlobalOpts) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "rm <bucket>",
		Short: "Remove a bucket's lifecycle configuration",
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
			if err := orchestrate.RemoveBucketLifecycle(cmd.Context(), d, proj, args[0]); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"lifecycle_removed": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "lifecycle removed from %q\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	return cmd
}

func newS3VersioningCmd(g *GlobalOpts) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "versioning <bucket> on|off",
		Short: "Enable or suspend bucket versioning",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var enabled bool
			switch strings.ToLower(args[1]) {
			case "on", "enable", "enabled":
				enabled = true
			case "off", "suspend", "suspended":
				enabled = false
			default:
				return fmt.Errorf("want on|off, got %q", args[1])
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
			if err := orchestrate.SetBucketVersioning(cmd.Context(), d, proj, args[0], enabled); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "versioning": enabled})
			}
			state := "suspended"
			if enabled {
				state = "enabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "versioning %s on %q\n", state, args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	return cmd
}

func newS3PolicyCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Manage a bucket policy"}
	cmd.AddCommand(newS3PolicySetCmd(g), newS3PolicyGetCmd(g))
	return cmd
}

// publicReadPolicy is the canned anonymous-read policy for --public-read.
func publicReadPolicy(bucket string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucket)
}

func newS3PolicySetCmd(g *GlobalOpts) *cobra.Command {
	var project, file string
	var publicRead bool
	cmd := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Set a bucket policy (--public-read or --file policy.json)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var policy string
			switch {
			case publicRead:
				policy = publicReadPolicy(args[0])
			case file != "":
				b, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				policy = string(b)
			default:
				return fmt.Errorf("specify --public-read or --file policy.json")
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
			if err := orchestrate.SetBucketPolicy(cmd.Context(), d, proj, args[0], policy); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "policy_set": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "policy set on %q\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&file, "file", "", "policy JSON file")
	cmd.Flags().BoolVar(&publicRead, "public-read", false, "apply a canned anonymous read policy")
	return cmd
}

func newS3PolicyGetCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <bucket>",
		Short: "Print a bucket's policy JSON (lock-free)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			policy, err := orchestrate.GetBucketPolicy(cmd.Context(), d, args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "policy": policy})
			}
			if policy == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "no policy set")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), policy)
			return nil
		},
	}
	return cmd
}

func newS3CorsCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "cors", Short: "Manage bucket CORS rules"}
	cmd.AddCommand(newS3CorsSetCmd(g), newS3CorsGetCmd(g))
	return cmd
}

func newS3CorsSetCmd(g *GlobalOpts) *cobra.Command {
	var project, file string
	cmd := &cobra.Command{
		Use:   "set <bucket>",
		Short: "Set bucket CORS rules from a JSON file (an array of CORS rules)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("--file cors.json is required")
			}
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			rules, err := parseCORS(b)
			if err != nil {
				return err
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
			if err := orchestrate.SetBucketCORS(cmd.Context(), d, proj, args[0], rules); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "cors_rules": len(rules)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cors set on %q (%d rule(s))\n", args[0], len(rules))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&file, "file", "", "CORS JSON file")
	return cmd
}

func newS3CorsGetCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <bucket>",
		Short: "Print a bucket's CORS rules (lock-free)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			rules, err := orchestrate.GetBucketCORS(cmd.Context(), d, args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"bucket": args[0], "rules": rules})
			}
			w := cmd.OutOrStdout()
			if len(rules) == 0 {
				fmt.Fprintln(w, "no cors rules")
				return nil
			}
			for _, r := range rules {
				fmt.Fprintf(w, "methods=%v origins=%v\n", r.AllowedMethods, r.AllowedOrigins)
			}
			return nil
		},
	}
	return cmd
}

// --- parsing helpers ----------------------------------------------------------

// parseTransition parses a `days=N,tier=T` transition spec.
func parseTransition(s string) (int, string, error) {
	var days int
	var tier string
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return 0, "", fmt.Errorf("invalid --transition %q (want days=N,tier=T)", s)
		}
		switch strings.TrimSpace(kv[0]) {
		case "days":
			n, err := strconv.Atoi(strings.TrimSpace(kv[1]))
			if err != nil {
				return 0, "", fmt.Errorf("invalid transition days %q: %w", kv[1], err)
			}
			days = n
		case "tier", "class":
			tier = strings.TrimSpace(kv[1])
		default:
			return 0, "", fmt.Errorf("unknown --transition key %q (want days|tier)", kv[0])
		}
	}
	if days <= 0 || tier == "" {
		return 0, "", fmt.Errorf("--transition needs both days=N and tier=T")
	}
	return days, tier, nil
}

// parseCORS unmarshals a JSON array of CORS rules into the SDK type.
func parseCORS(b []byte) ([]s3types.CORSRule, error) {
	var raw []struct {
		AllowedMethods []string `json:"AllowedMethods"`
		AllowedOrigins []string `json:"AllowedOrigins"`
		AllowedHeaders []string `json:"AllowedHeaders"`
		ExposeHeaders  []string `json:"ExposeHeaders"`
		MaxAgeSeconds  int32    `json:"MaxAgeSeconds"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse cors json (want an array of rules): %w", err)
	}
	var rules []s3types.CORSRule
	for _, r := range raw {
		rule := s3types.CORSRule{
			AllowedMethods: r.AllowedMethods,
			AllowedOrigins: r.AllowedOrigins,
			AllowedHeaders: r.AllowedHeaders,
			ExposeHeaders:  r.ExposeHeaders,
		}
		if r.MaxAgeSeconds > 0 {
			rule.MaxAgeSeconds = &r.MaxAgeSeconds
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
