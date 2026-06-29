package git

import "strings"

// knownHosts maps a shorthand scheme to its SSH host (spec 06 shorthand
// expansion, a superset of devdock's !Repo model).
var knownHosts = map[string]string{
	"github":    "github.com",
	"gitlab":    "gitlab.com",
	"bitbucket": "bitbucket.org",
}

// ExpandURL expands a shorthand repo spec into a full clone URL:
//
//	github:org/repo      -> git@github.com:org/repo.git
//	gitlab:org/repo      -> git@gitlab.com:org/repo.git
//	bitbucket:org/repo   -> git@bitbucket.org:org/repo.git
//	repo:<anything>      -> <anything> (explicit passthrough)
//
// Anything that already looks like a URL (scheme://, git@host:, or a local path)
// is returned unchanged. SSH is the default transport for shorthand.
func ExpandURL(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return spec
	}
	// Explicit passthrough.
	if rest, ok := strings.CutPrefix(spec, "repo:"); ok {
		return rest
	}
	// Already a full URL or scp-like remote or a path.
	if looksLikeURL(spec) {
		return spec
	}
	if scheme, rest, ok := strings.Cut(spec, ":"); ok {
		if host, known := knownHosts[scheme]; known {
			path := strings.TrimSuffix(rest, ".git")
			return "git@" + host + ":" + path + ".git"
		}
	}
	return spec
}

// looksLikeURL reports whether spec is already a transport URL, an scp-style
// remote (git@host:path), or a filesystem path.
func looksLikeURL(spec string) bool {
	switch {
	case strings.Contains(spec, "://"):
		return true // ssh:// https:// git:// file://
	case strings.HasPrefix(spec, "/"), strings.HasPrefix(spec, "."), strings.HasPrefix(spec, "~"):
		return true // local path
	case strings.HasPrefix(spec, "git@"):
		return true // scp-style
	}
	// scp-style host:path with no recognized shorthand scheme (contains a ':'
	// and a '/' after it, and the part before ':' looks like a host with a dot).
	if host, rest, ok := strings.Cut(spec, ":"); ok {
		if strings.Contains(host, ".") && strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}
