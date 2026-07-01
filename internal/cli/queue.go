package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/resource"
)

// newQueueCmd wires the `devstack queue` group (spec 29 §messaging — queues):
// tenant-scoped queues on the NATIVE default NATS (durable work-queue), a Redis
// list namespace, or opt-in LocalStack SQS. create/rm go through the flock via
// internal/orchestrate; list is a lock-free ledger read. Names are project-PREFIXED
// unless --no-prefix. The ledger kind is `queue` (free-text; no migration).
func newQueueCmd(g *GlobalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Tenant-scoped queues on the shared NATS / Redis / SQS engines",
	}
	cmd.AddCommand(newQueueCreateCmd(g), newQueueListCmd(g), newQueueRmCmd(g))
	return cmd
}

// queueEngines are the accepted --engine values; queueOrder is the native-default
// inference order (prefer NATS, then Redis, then LocalStack SQS).
var (
	queueEngines = []string{"nats", "sqs", "redis"}
	queueOrder   = []string{"nats", "redis", "localstack"}
)

func newQueueCreateCmd(g *GlobalOpts) *cobra.Command {
	var project, engine, dlq string
	var fifo, noPrefix bool
	var maxReceive int
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a tenant queue (idempotent). Engine inferred from active engines unless --engine.",
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
			eng, err := resolveMsgEngine(d, engine, queueEngines, queueOrder, "queue")
			if err != nil {
				return err
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			params := map[string]any{}
			if fifo {
				params["fifo"] = true
			}
			// A DLQ (SQS only) is created first as its own recorded resource, then the
			// main queue references it for a redrive policy (spec 29 §SQS FIFO/DLQ).
			var dlqPhysical string
			if dlq != "" {
				if eng != "localstack" {
					return fmt.Errorf("--dlq is only supported for --engine sqs")
				}
				dlqPhysical = msgPrefixed(proj, dlq, noPrefix)
				if _, err := orchestrate.CreateResource(cmd.Context(), d, resource.Resource{
					Engine: eng, Kind: "queue", Name: dlqPhysical, Owner: proj,
					Params: map[string]any{"fifo": fifo}, CredKind: resource.CredPredictable,
				}); err != nil {
					return fmt.Errorf("create DLQ %q: %w", dlqPhysical, err)
				}
				params["dlq"] = dlqPhysical
				if maxReceive > 0 {
					params["max_receive"] = maxReceive
				}
			}
			r := resource.Resource{
				Engine: eng, Kind: "queue", Name: name, Owner: proj,
				Params: params, CredKind: resource.CredPredictable,
			}
			attrs, err := orchestrate.CreateResource(cmd.Context(), d, r)
			if err != nil {
				return err
			}
			if g.JSON {
				out := map[string]any{"kind": "queue", "name": name, "project": proj, "engine": eng, "endpoint": attrs["endpoint"]}
				if dlqPhysical != "" {
					out["dlq"] = dlqPhysical
				}
				if attrs["queueUrl"] != "" {
					out["queueUrl"] = attrs["queueUrl"]
				}
				if attrs["hint"] != "" {
					out["hint"] = attrs["hint"]
				}
				return writeJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "created queue %q on %s\n", name, eng)
			if attrs["queueUrl"] != "" {
				fmt.Fprintf(w, "url: %s\n", attrs["queueUrl"])
			}
			if attrs["hint"] != "" {
				fmt.Fprintf(w, "hint: %s\n", attrs["hint"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "queue backend: nats|sqs|redis (default: inferred from active engines)")
	cmd.Flags().BoolVar(&fifo, "fifo", false, "FIFO queue (SQS: appends .fifo + sets FifoQueue)")
	cmd.Flags().StringVar(&dlq, "dlq", "", "dead-letter queue name (SQS only; created first)")
	cmd.Flags().IntVar(&maxReceive, "max-receive", 0, "redrive maxReceiveCount for the DLQ (SQS only)")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}

func newQueueListCmd(g *GlobalOpts) *cobra.Command {
	var project string
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's provisioned queues (lock-free)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, _, err := listMessagingRows(cmd, "queue", project, all)
			if err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"queues": rows})
			}
			w := cmd.OutOrStdout()
			if len(rows) == 0 {
				fmt.Fprintln(w, "no queues")
				return nil
			}
			for _, r := range rows {
				fmt.Fprintf(w, "%-10s %-24s %s\n", r.Project, r.Name, r.CreatedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().BoolVar(&all, "all", false, "list every project's queues")
	return cmd
}

func newQueueRmCmd(g *GlobalOpts) *cobra.Command {
	var project, engine string
	var yes, noPrefix bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a tenant queue (destructive; confirm required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON && !yes {
				return fmt.Errorf("refusing to remove a queue without --yes for --json/non-interactive use")
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
			eng, err := resolveMsgEngine(d, engine, queueEngines, queueOrder, "queue")
			if err != nil {
				return err
			}
			name := msgPrefixed(proj, args[0], noPrefix)
			if !yes {
				if !confirm(cmd, fmt.Sprintf("This REMOVES queue %q on %s. Type 'yes' to continue: ", name, eng)) {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted")
					return nil
				}
			}
			r := resource.Resource{Engine: eng, Kind: "queue", Name: name, Owner: proj}
			if err := orchestrate.DropResource(cmd.Context(), d, r, true); err != nil {
				return err
			}
			if g.JSON {
				return writeJSON(cmd, map[string]any{"removed": map[string]string{"kind": "queue", "name": name, "project": proj, "engine": eng}})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed queue %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "owner project")
	cmd.Flags().StringVar(&engine, "engine", "", "queue backend: nats|sqs|redis (default: inferred)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&noPrefix, "no-prefix", false, "use the literal name (skip the <project>- prefix)")
	return cmd
}
