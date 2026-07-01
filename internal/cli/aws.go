package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/open-source-cloud/devstack/internal/orchestrate"
)

// newAwsCmd wires the thin `devstack aws -- <args...>` shim (spec 29 §aws): a pure
// argv passthrough over the user's own `aws` binary with --endpoint-url +
// --region prepended and dev creds injected via the child ENV (never argv). It
// never reimplements any AWS call; it just removes the endpoint/port lookup. The
// aws binary is an external dependency — absence is a clear error, never a panic.
func newAwsCmd(g *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "aws -- <args...>",
		Short: "Run the host `aws` CLI against the local LocalStack/MinIO endpoint",
		Long: "A thin argv shim over your own `aws` binary: devstack resolves the\n" +
			"LocalStack/MinIO host port and prepends --endpoint-url/--region plus dev\n" +
			"credentials (via the child environment). It does not reimplement any AWS call.\n\n" +
			"Example: devstack aws -- s3 ls",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			args = stripLeadingDashDash(args)
			// Bare `aws` or a leading -h/--help is a request for THIS shim's help —
			// short-circuit before constructing any docker/S3 client or touching the
			// daemon. (DisableFlagParsing means cobra does not intercept --help itself,
			// so a help token would otherwise be forwarded to the real aws endpoint.)
			// A help flag AFTER a subcommand (e.g. `aws -- s3 --help`) passes through
			// to the user's aws unchanged.
			if len(args) == 0 || isHelpFlag(args[0]) {
				return cmd.Help()
			}
			awsPath, err := lookupAws()
			if err != nil {
				return err
			}
			d, closeFn, err := buildUpDeps(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			endpoint, region, access, secret, err := orchestrate.ResolveAwsEndpoint(cmd.Context(), d)
			if err != nil {
				return err
			}
			argv := awsArgs(endpoint, region, args)
			child := exec.CommandContext(cmd.Context(), awsPath, argv...)
			child.Env = awsEnv(os.Environ(), access, secret, region)
			child.Stdin = cmd.InOrStdin()
			child.Stdout = cmd.OutOrStdout()
			child.Stderr = cmd.ErrOrStderr()
			if err := child.Run(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					return fmt.Errorf("aws exited with code %d", ee.ExitCode())
				}
				return fmt.Errorf("run aws: %w", err)
			}
			return nil
		},
	}
}

// lookupAws resolves the host `aws` binary or returns an install remediation.
func lookupAws() (string, error) {
	p, err := exec.LookPath("aws")
	if err != nil {
		return "", fmt.Errorf("the `aws` CLI is not installed or not on PATH — install it (https://aws.amazon.com/cli/) to use `devstack aws --`")
	}
	return p, nil
}

// isHelpFlag reports whether tok is a help request (-h/--help/help) meant for the
// shim itself rather than the forwarded aws command.
func isHelpFlag(tok string) bool {
	switch tok {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// stripLeadingDashDash drops a leading "--" separator (cobra with
// DisableFlagParsing keeps it in args).
func stripLeadingDashDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

// awsArgs prepends the endpoint + region flags to the user's args (creds go in the
// env, never argv, per spec 29 / §7.5 secret hygiene).
func awsArgs(endpoint, region string, args []string) []string {
	out := []string{"--endpoint-url=" + endpoint, "--region=" + region}
	return append(out, args...)
}

// awsEnv layers the LocalStack/MinIO dev credentials over the parent environment.
func awsEnv(parent []string, access, secret, region string) []string {
	env := append([]string{}, parent...)
	env = append(env,
		"AWS_ACCESS_KEY_ID="+access,
		"AWS_SECRET_ACCESS_KEY="+secret,
		"AWS_DEFAULT_REGION="+region,
		"AWS_REGION="+region,
	)
	return env
}
