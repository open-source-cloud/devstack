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
		return strings.TrimSpace(rest)
	}
	// Already a full URL or scp-like remote or a path.
	if looksLikeURL(spec) {
		return spec
	}
	if scheme, rest, ok := strings.Cut(spec, ":"); ok {
		if host, known := knownHosts[scheme]; known {
			path := strings.TrimSuffix(strings.TrimSpace(rest), ".git")
			return "git@" + host + ":" + path + ".git"
		}
	}
	return spec
}

// SameRemote reports whether two git remote specs point at the same repository,
// tolerating ssh/https/scp/shorthand differences (e.g. git@github.com:a/b.git
// and https://github.com/a/b are the same). Used by `ws clone` so an existing
// clone reached via a different transport is not flagged as a URL mismatch.
func SameRemote(a, b string) bool {
	ah, ap := canonicalRemote(a)
	bh, bp := canonicalRemote(b)
	return ah == bh && ap == bp
}

// canonicalRemote reduces a remote spec to a (host, path) identity. A purely
// local path has an empty host and its cleaned path.
func canonicalRemote(s string) (host, path string) {
	s = ExpandURL(strings.TrimSpace(s))
	switch {
	case strings.Contains(s, "://"):
		s = s[strings.Index(s, "://")+3:]
		if at := strings.IndexByte(s, '@'); at >= 0 {
			if slash := strings.IndexByte(s, '/'); slash < 0 || at < slash {
				s = s[at+1:]
			}
		}
		host, path, _ = strings.Cut(s, "/")
		host, _, _ = strings.Cut(host, ":") // drop :port
	case strings.HasPrefix(s, "/"), strings.HasPrefix(s, "."), strings.HasPrefix(s, "~"):
		return "", strings.TrimSuffix(s, ".git") // local path
	default:
		// scp-style git@host:path
		if at := strings.IndexByte(s, '@'); at >= 0 {
			s = s[at+1:]
		}
		if h, p, ok := strings.Cut(s, ":"); ok {
			host, path = h, p
		} else {
			return "", strings.TrimSuffix(s, ".git")
		}
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git")
	return host, path
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
