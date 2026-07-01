package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
)

// newTopicCmd wires the `devstack topic` group (spec 29 §messaging — pub/sub
// topics): tenant-scoped topics on the NATIVE default Kafka (Redpanda), a NATS
// subject convention, or opt-in LocalStack SNS. create/rm go through the flock via
// internal/orchestrate; list is a lock-free ledger read. Names are project-PREFIXED
// unless --no-prefix. The ledger kind is `topic` (free-text; no migration).
func newTopicCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topic",
		Short: "Tenant-scoped pub/sub topics on the shared Kafka / NATS / SNS engines",
	}
	cmd.AddCommand(newTopicCreateCmd(g), newTopicListCmd(g), newTopicRmCmd(g))
	return cmd
}

// topicEngines are the accepted --engine values; topicOrder is the native-default
// inference order (prefer Kafka, then NATS subjects, then LocalStack SNS).
var (
	topicEngines = []string{"sns", "kafka", "nats"}
	topicOrder   = []string{"kafka", "nats", "localstack"}
)

func newTopicCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, engine, subscribe string
	var noPrefix bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant topic (idempotent). Engine inferred from active engines unless --engine.",
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
			eng, err := resolveMsgEngine(d, engine, topicEngines, topicOrder, "topic")
			if err != nil {
				return err
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			params := map[string]any{}
			if subscribe != "" {
				if eng != "localstack" {
					return fmt.Errorf("--subscribe (SNS→SQS fan-out) is only supported for --engine sns")
				}
				params["subscribe"] = msgPrefixed(proj, subscribe, noPrefix)
			}
			r := resource.Resource{
				Engine: eng, Kind: "topic", Name: name, Owner: proj,
				Params: params, CredKind: resource.CredPredictable,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				out := map[string]any{"kind": "topic", "name": name, "project": proj, "engine": eng, "endpoint": attrs["endpoint"]}
				if attrs["topicArn"] != "" {
					out["topicArn"] = attrs["topicArn"]
				}
				if attrs["subscribed"] != "" {
					out["subscribed"] = attrs["subscribed"]
				}
				return writeJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "created topic %q on %s\n", name, eng)
			if attrs["topicArn"] != "" {
				fmt.Fprintf(w, "arn: %s\n", attrs["topicArn"])
			}
			if attrs["subscribed"] != "" {
				fmt.Fprintf(w, "subscribed: %s\n", attrs["subscribed"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "topic backend: sns|kafka|nats (default: inferred from active engines)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "SQS queue to subscribe (SNS only; the classic SNS→SQS fan-out)")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}

func newTopicListCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's provisioned topics (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, _, err := listMessagingRows(cmd, "topic", project, all)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"topics": rows})
			}
			w := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(w, "no topics")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(w, "%-10s %-24s %s\n", r.Project, r.Name, r.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&all, "all", false, "list every project's topics")
	return cmd
}

func newTopicRmCmd(g *GlobalOpts) *cobra.Command {
	var project, engine string
	var yes, noPrefix bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a tenant topic (destructive; confirm required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to remove a topic without --yes for --json/non-interactive use")
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
			eng, err := resolveMsgEngine(d, engine, topicEngines, topicOrder, "topic")
			if err != nil {
				return err
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This REMOVES topic %q on %s. Type 'yes' to continue: ", name, eng)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: eng, Kind: "topic", Name: name, Owner: proj}
			if err := orchestrate.DropResource(cmd.Context(), d, r, true); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"removed": map[string]string{"kind": "topic", "name": name, "project": proj, "engine": eng}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed topic %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "topic backend: sns|kafka|nats (default: inferred)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}
