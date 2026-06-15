// Package docker is the boundary to the container runtime (DECISIONS D4, D5).
//
// Split of responsibility (avoids ownership drift):
//   - Lifecycle verbs shell out to the `docker compose` CLI (v2.20+) with an
//     explicit `-p` and tool-owned labels. Compose owns container lifecycle.
//   - The Engine SDK (`github.com/moby/moby/client`, NOT the deprecated
//     `docker/docker/client`) is used READ-ONLY for network ensure, label-filtered
//     enumeration, health polling, and ref-count reconciliation.
//
// The read-only surface sits behind the Client interface so it is mockable for
// concurrency/race tests without a real daemon.
package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Minimum external tool versions (DECISIONS D5, D9).
var (
	MinCompose = Version{2, 20}
	MinGit     = Version{2, 30}
)

// Client is the read-only Engine SDK surface devstack depends on. It never runs
// containers — Compose owns lifecycle. Kept tiny so it is trivially mockable.
type Client interface {
	// Ping verifies daemon reachability.
	Ping(ctx context.Context) error
	// ServerVersion returns the Engine version string (e.g. "27.1.1").
	ServerVersion(ctx context.Context) (string, error)
	// ContextName returns the active Docker context the client is bound to;
	// this keys the state ledger.
	ContextName() string
	// Close releases the underlying connection.
	Close() error
}

// Version is a simple major.minor for tool-version gating.
type Version struct{ Major, Minor int }

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// AtLeast reports whether v >= min.
func (v Version) AtLeast(min Version) bool {
	if v.Major != min.Major {
		return v.Major > min.Major
	}
	return v.Minor >= min.Minor
}

// ComposeVersion runs `docker compose version --short` and parses major.minor.
func ComposeVersion(ctx context.Context) (Version, error) {
	out, err := exec.CommandContext(ctx, "docker", "compose", "version", "--short").Output()
	if err != nil {
		return Version{}, fmt.Errorf("`docker compose version` failed (is Docker Compose v2 installed?): %w", err)
	}
	return parseVersion(string(out))
}

// GitVersion runs `git --version` and parses major.minor.
func GitVersion(ctx context.Context) (Version, error) {
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return Version{}, fmt.Errorf("`git --version` failed (is git installed?): %w", err)
	}
	return parseVersion(string(out))
}

// parseVersion extracts the first MAJOR.MINOR it finds in s, tolerating
// surrounding text like "git version 2.43.0" or "Docker Compose version v2.29.1".
func parseVersion(s string) (Version, error) {
	for field := range strings.FieldsSeq(s) {
		field = strings.TrimPrefix(field, "v")
		parts := strings.SplitN(field, ".", 3)
		if len(parts) < 2 {
			continue
		}
		major, err1 := strconv.Atoi(parts[0])
		minor, err2 := strconv.Atoi(strings.TrimRight(parts[1], ","))
		if err1 == nil && err2 == nil {
			return Version{Major: major, Minor: minor}, nil
		}
	}
	return Version{}, fmt.Errorf("could not parse a version from %q", strings.TrimSpace(s))
}
