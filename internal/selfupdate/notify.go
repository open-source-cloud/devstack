package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/mod/semver"
)

// This file is the background update notifier (spec 14): a throttled, fail-silent
// check that prints a one-line notice when a newer release exists. It hits the
// network at most once per TTL (cached under the XDG cache dir), never blocks a
// command for long, never errors out, and stays silent for dev builds, --json/
// --quiet output, or when DEVSTACK_NO_UPDATE_NOTIFIER is set.

const (
	notifierTTL        = 24 * time.Hour
	notifierTimeout    = 2 * time.Second
	notifierDisableEnv = "DEVSTACK_NO_UPDATE_NOTIFIER"
	notifierCacheFile  = "update-check.json"
)

type notifyCache struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
}

// Notifier performs a throttled, fail-silent "is there a newer release?" check.
type Notifier struct {
	Current  string                                    // the running version
	CacheDir string                                    // the devstack cache dir (cache file written directly inside)
	Now      func() time.Time                          // injectable clock (default time.Now)
	CheckFn  func(ctx context.Context) (string, error) // returns the latest tag (default LatestTag)
}

// Notify writes an update notice to w if a newer release is available. It is
// best-effort: any error (no network, bad cache, rate limit) silently yields no
// notice. Returns true iff a notice was written (for tests).
func (n Notifier) Notify(ctx context.Context, w io.Writer) bool {
	if os.Getenv(notifierDisableEnv) != "" {
		return false
	}
	if n.Current == "" || IsDevBuild(n.Current) {
		return false // dev/dirty builds have no clean version to compare
	}
	latest := n.latest(ctx)
	if latest == "" {
		return false
	}
	if semver.IsValid(n.Current) && semver.IsValid(latest) && semver.Compare(latest, n.Current) > 0 {
		fmt.Fprintf(w, "\nA new devstack release is available: %s → %s  (run `devstack self update`)\n", n.Current, latest)
		return true
	}
	return false
}

// latest returns the latest tag from a fresh cache, or refreshes via CheckFn
// (bounded + fail-silent) when the cache is stale/absent.
func (n Notifier) latest(ctx context.Context) string {
	now := n.now()
	if c, ok := n.readCache(); ok && !c.CheckedAt.IsZero() && now.Sub(c.CheckedAt) < notifierTTL {
		return c.Latest // fresh cache → no network
	}
	cctx, cancel := context.WithTimeout(ctx, notifierTimeout)
	defer cancel()
	latest, err := n.check(cctx)
	if err != nil || latest == "" {
		return ""
	}
	n.writeCache(notifyCache{CheckedAt: now, Latest: latest})
	return latest
}

func (n Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n Notifier) check(ctx context.Context) (string, error) {
	if n.CheckFn != nil {
		return n.CheckFn(ctx)
	}
	return LatestTag(ctx)
}

func (n Notifier) cachePath() string {
	if n.CacheDir == "" {
		return ""
	}
	return filepath.Join(n.CacheDir, notifierCacheFile)
}

func (n Notifier) readCache() (notifyCache, bool) {
	p := n.cachePath()
	if p == "" {
		return notifyCache{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return notifyCache{}, false
	}
	var c notifyCache
	if err := json.Unmarshal(b, &c); err != nil {
		return notifyCache{}, false
	}
	return c, true
}

func (n Notifier) writeCache(c notifyCache) {
	p := n.cachePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return // best-effort
	}
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}
