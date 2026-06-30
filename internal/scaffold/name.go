package scaffold

import "strings"

// SanitizeName coerces an arbitrary string into a valid devstack name
// (^[a-z][a-z0-9_-]{0,62}$, the config dsname rule): lowercase, any other rune
// becomes '-', repeated '-' collapse, leading non-letters are trimmed, and the
// result is capped at 63 chars. Falls back to "workspace" when nothing valid
// remains. Used for the CWD-basename default and the wizard's live name field
// (so a repo dir like "My.App" pre-fills as "my-app", never an invalid value).
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.TrimLeft(out, "0123456789-_") // first char must be a letter
	if len(out) > 63 {
		out = out[:63]
	}
	out = strings.TrimRight(out, "-_")
	if out == "" {
		return "workspace"
	}
	return out
}
