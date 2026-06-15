package docker

import (
	"context"
	"fmt"
)

// CheckStatus is the outcome of a single preflight probe.
type CheckStatus string

const (
	StatusOK   CheckStatus = "ok"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
)

// Check is one capability probe with a one-line remediation when not OK
// (ARCHITECTURE §7.6: actionable errors are what close GitHub issues).
type Check struct {
	Name        string      `json:"name"`
	Status      CheckStatus `json:"status"`
	Detail      string      `json:"detail"`
	Remediation string      `json:"remediation,omitempty"`
}

// Preflight probes the external tools devstack drives. The daemon probe is
// skipped when client is nil (e.g. the SDK could not be constructed).
func Preflight(ctx context.Context, client Client) []Check {
	var checks []Check

	if v, err := ComposeVersion(ctx); err != nil {
		checks = append(checks, Check{
			Name:        "docker compose",
			Status:      StatusFail,
			Detail:      err.Error(),
			Remediation: "install Docker Compose v2 (the `docker compose` plugin), version " + MinCompose.String() + " or newer",
		})
	} else if !v.AtLeast(MinCompose) {
		checks = append(checks, Check{
			Name:        "docker compose",
			Status:      StatusFail,
			Detail:      fmt.Sprintf("found v%s, need v%s+", v, MinCompose),
			Remediation: "upgrade Docker Desktop / the compose plugin to v" + MinCompose.String() + "+",
		})
	} else {
		checks = append(checks, Check{Name: "docker compose", Status: StatusOK, Detail: "v" + v.String()})
	}

	if v, err := GitVersion(ctx); err != nil {
		checks = append(checks, Check{
			Name:        "git",
			Status:      StatusFail,
			Detail:      err.Error(),
			Remediation: "install git " + MinGit.String() + " or newer",
		})
	} else if !v.AtLeast(MinGit) {
		checks = append(checks, Check{
			Name:        "git",
			Status:      StatusFail,
			Detail:      fmt.Sprintf("found v%s, need v%s+", v, MinGit),
			Remediation: "upgrade git to v" + MinGit.String() + "+",
		})
	} else {
		checks = append(checks, Check{Name: "git", Status: StatusOK, Detail: "v" + v.String()})
	}

	if client == nil {
		checks = append(checks, Check{
			Name:        "docker daemon",
			Status:      StatusWarn,
			Detail:      "Engine SDK client unavailable",
			Remediation: "ensure DOCKER_HOST / the active docker context points at a running daemon",
		})
	} else if err := client.Ping(ctx); err != nil {
		checks = append(checks, Check{
			Name:        "docker daemon",
			Status:      StatusFail,
			Detail:      err.Error(),
			Remediation: "start Docker (Docker Desktop or `dockerd`) and verify `docker info` works in this context",
		})
	} else {
		detail := "reachable"
		if sv, err := client.ServerVersion(ctx); err == nil {
			detail = "Engine v" + sv
		}
		checks = append(checks, Check{
			Name:   fmt.Sprintf("docker daemon (context %q)", client.ContextName()),
			Status: StatusOK,
			Detail: detail,
		})
	}

	return checks
}
