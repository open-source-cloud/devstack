package telemetry

import "strings"

// CategorizeError maps a wrapped error to one of the closed Category* enum values.
// It inspects known signatures (ARCHITECTURE §7.6) but returns ONLY the enum — the
// raw err.Error() text is never returned or transmitted, so an IP, path, username,
// or repo name embedded in the message cannot leak. An unmatched error becomes
// CategoryOther.
func CategorizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "cannot connect to the docker daemon", "is the docker daemon running", "dockerd", "/var/run/docker.sock"):
		return CategoryDockerDaemonUnreachable
	case containsAny(msg, "compose", "docker compose") && containsAny(msg, "too old", "version", "requires", "unsupported"):
		return CategoryComposeTooOld
	case containsAny(msg, "port is already allocated", "address already in use", "port in use", "bind: address already"):
		return CategoryPortInUse
	case containsAny(msg, "authentication failed", "could not read username", "terminal prompts disabled", "askpass", "permission denied (publickey"):
		return CategoryGitAuthPrompt
	case containsAny(msg, "database is locked", "resource temporarily unavailable", "could not acquire lock", "state locked", "busy"):
		return CategoryStateLocked
	default:
		return CategoryOther
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
