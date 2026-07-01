package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/config"
	"github.com/open-source-cloud/devstack/internal/orchestrate"
	"github.com/open-source-cloud/devstack/internal/state"
)

// This file holds the shared plumbing for the messaging resource groups (spec 29
// §messaging): queue / topic / stream. Each verb group mirrors the db/s3 flow —
// create/rm go through the up-saga's lock → overlay → provisioner → ledger → event
// path via internal/orchestrate; list is a lock-free ledger read. The DECISION is
// NATIVE-by-default: NATS backs queues+streams, Redpanda/Kafka backs topics+kafka
// streams, and LocalStack (SQS/SNS) is opt-in via --engine sqs|sns. A bare verb
// with no --engine infers from the ACTIVE shared engines (never auto-starting one)
// and errors clearly when unsatisfiable.

// msgPrefixed computes the DNS-safe tenant-scoped resource name <project>-<name>
// (spec 29 §tenant naming) — hyphen-separated, valid for NATS streams, Kafka
// topics, SQS/SNS names and Redis keys alike — unless --no-prefix keeps the literal.
func msgPrefixed(project, name string, noPrefix bool) string {
	if noPrefix {
		return name
	}
	return project + "-" + name
}

// engineForFlag maps a user-facing --engine value to the registry engine key. The
// LocalStack provisioner is registered under "aws" (the localstack template's
// `provides: aws`), so both sqs and sns resolve there.
func engineForFlag(flag string) string {
	switch flag {
	case "sqs", "sns", "aws":
		return "aws"
	case "nats":
		return "nats"
	case "kafka":
		return "kafka"
	case "redis":
		return "redis"
	default:
		return flag
	}
}

// inferEngine picks the first engine in the preference order that has a live shared
// instance in the workspace (never auto-starting one). Returns "" if none match.
func inferEngine(d orchestrate.UpDeps, order []string) string {
	for _, e := range order {
		if _, ok := orchestrate.ResolveInstance(d.Model, e); ok {
			return e
		}
	}
	return ""
}

// resolveMsgEngine turns the --engine flag (validated against allowed) plus the
// native-default inference order into a concrete registry engine key, or a clear
// error when the flag is unknown or nothing is inferable.
func resolveMsgEngine(d orchestrate.UpDeps, flag string, allowed []string, order []string, domain string) (string, error) {
	if flag != "" {
		if !contains(allowed, flag) {
			return "", fmt.Errorf("invalid --engine %q for %s (want one of %v)", flag, domain, allowed)
		}
		engine := engineForFlag(flag)
		if _, ok := orchestrate.ResolveInstance(d.Model, engine); !ok {
			return "", fmt.Errorf("no shared %s engine (for --engine %s) in this workspace; declare one under workspace.shared and run `devstack up` (devstack never auto-starts an engine)", engine, flag)
		}
		return engine, nil
	}
	engine := inferEngine(d, order)
	if engine == "" {
		return "", fmt.Errorf("no active engine to back a %s in this workspace (tried %v); add one under workspace.shared and run `devstack up`, or pass --engine", domain, order)
	}
	return engine, nil
}

// listMessagingRows returns the project's provisioned rows of the given kind
// (queue|topic|stream) — a lock-free ledger read, prefix-scoped to the project
// unless --all.
func listMessagingRows(cmd *cobra.Command, kind, project string, all bool) ([]state.Provisioned, string, error) {
	mgr, closeFn, err := buildManager(cmd)
	if err != nil {
		return nil, "", err
	}
	defer closeFn()
	proj := project
	if proj == "" {
		proj = defaultProjectFromModel(mgr.Model)
	}
	var rows []state.Provisioned
	if proj != "" && !all {
		rows, err = mgr.DB.ProvisionedFor(proj)
	} else {
		rows, err = mgr.DB.AllProvisioned()
	}
	if err != nil {
		return nil, proj, err
	}
	var out []state.Provisioned
	for _, r := range rows {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, proj, nil
}

// defaultProjectFromModel returns the workspace's single/first project by name.
func defaultProjectFromModel(m *config.Model) string {
	names := sortedProjectNames(m)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
