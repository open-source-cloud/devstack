package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
)

// newStreamCmd wires the `devstack stream` group (spec 29 §messaging — durable
// streams): tenant-scoped streams on the NATIVE default NATS (JetStream) or a
// Kafka (Redpanda) partitioned topic. create/rm go through the flock via internal/
// orchestrate; list is a lock-free ledger read. Names are project-PREFIXED unless
// --no-prefix. --partitions/--replicas are KAFKA-ONLY (rejected for NATS at parse
// time); --retention maps to NATS --max-age or Kafka retention (spec 29 §NATS vs
// Kafka retention models). The ledger kind is `stream` (free-text; no migration).
func newStreamCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Tenant-scoped durable streams on the shared NATS / Kafka engines",
	}
	cmd.AddCommand(newStreamCreateCmd(g), newStreamListCmd(g), newStreamRmCmd(g))
	return cmd
}

// streamEngines are the accepted --engine values; streamOrder is the native-default
// inference order (prefer NATS JetStream, then Kafka).
var (
	streamEngines = []string{"nats", "kafka"}
	streamOrder   = []string{"nats", "kafka"}
)

func newStreamCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, engine, retention string
	var partitions, replicas int
	var noPrefix bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant stream (idempotent). Engine inferred from active engines unless --engine.",
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
			eng, err := resolveMsgEngine(d, engine, streamEngines, streamOrder, "stream")
			if err != nil {
				return err
			}
			// --partitions/--replicas are a Kafka partition-model concept; a NATS
			// JetStream stream has neither. Reject them for NATS at parse time
			// (spec 29 §NATS work-queue vs Kafka partitions).
			if eng == "nats" {
				if cmd.Flags().Changed("partitions") {
					return fmt.Errorf("--partitions is only valid for --engine kafka (a NATS stream has no partitions)")
				}
				if cmd.Flags().Changed("replicas") {
					return fmt.Errorf("--replicas is only valid for --engine kafka")
				}
			}
			if retention != "" {
				if _, perr := time.ParseDuration(retention); perr != nil {
					return fmt.Errorf("invalid --retention %q (want a Go duration like 168h): %w", retention, perr)
				}
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			params := map[string]any{}
			if retention != "" {
				params["retention"] = retention
			}
			if partitions > 0 {
				params["partitions"] = partitions
			}
			if replicas > 0 {
				params["replicas"] = replicas
			}
			r := resource.Resource{
				Engine: eng, Kind: "stream", Name: name, Owner: proj,
				Params: params, CredKind: resource.CredPredictable,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				out := map[string]any{"kind": "stream", "name": name, "project": proj, "engine": eng, "endpoint": attrs["endpoint"]}
				if retention != "" {
					out["retention"] = retention
				}
				if attrs["subjects"] != "" {
					out["subjects"] = attrs["subjects"]
				}
				if attrs["partitions"] != "" {
					out["partitions"] = attrs["partitions"]
				}
				return writeJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "created stream %q on %s\n", name, eng)
			if attrs["subjects"] != "" {
				fmt.Fprintf(w, "subjects: %s\n", attrs["subjects"])
			}
			if attrs["partitions"] != "" {
				fmt.Fprintf(w, "partitions: %s\n", attrs["partitions"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "stream backend: nats|kafka (default: inferred from active engines)")
	cmd.Flags().IntVar(&partitions, "partitions", 0, "partition count (KAFKA only)")
	cmd.Flags().IntVar(&replicas, "replicas", 0, "replication factor (KAFKA only)")
	cmd.Flags().StringVar(&retention, "retention", "", "retention window as a Go duration (e.g. 168h)")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}

func newStreamListCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's provisioned streams (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, _, err := listMessagingRows(cmd, "stream", project, all)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"streams": rows})
			}
			w := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(w, "no streams")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(w, "%-10s %-24s %s\n", r.Project, r.Name, r.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&all, "all", false, "list every project's streams")
	return cmd
}

func newStreamRmCmd(g *GlobalOpts) *cobra.Command {
	var project, engine string
	var yes, noPrefix bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a tenant stream (destructive; confirm required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to remove a stream without --yes for --json/non-interactive use")
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
			eng, err := resolveMsgEngine(d, engine, streamEngines, streamOrder, "stream")
			if err != nil {
				return err
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This REMOVES stream %q on %s. Type 'yes' to continue: ", name, eng)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: eng, Kind: "stream", Name: name, Owner: proj}
			if err := orchestrate.DropResource(cmd.Context(), d, r, true); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"removed": map[string]string{"kind": "stream", "name": name, "project": proj, "engine": eng}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed stream %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "stream backend: nats|kafka (default: inferred)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}
