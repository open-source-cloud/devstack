package selfupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNotifyAvailable(t *testing.T) {
	calls := 0
	n := Notifier{
		Current:  "v0.1.0",
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return time.Unix(1000, 0) },
		CheckFn:  func(context.Context) (string, error) { calls++; return "v0.2.0", nil },
	}
	var w strings.Builder
	if !n.Notify(context.Background(), &w) {
		t.Fatal("expected an update notice")
	}
	if !strings.Contains(w.String(), "v0.1.0 → v0.2.0") {
		t.Errorf("notice = %q", w.String())
	}
	if calls != 1 {
		t.Errorf("checked %d times, want 1", calls)
	}
}

func TestNotifyUpToDate(t *testing.T) {
	n := Notifier{
		Current:  "v0.2.0",
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return time.Unix(1000, 0) },
		CheckFn:  func(context.Context) (string, error) { return "v0.2.0", nil },
	}
	var w strings.Builder
	if n.Notify(context.Background(), &w) || w.Len() != 0 {
		t.Errorf("no notice when current == latest, got %q", w.String())
	}
}

func TestNotifyThrottledByCache(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	mk := func(now time.Time) Notifier {
		return Notifier{
			Current: "v0.1.0", CacheDir: dir,
			Now:     func() time.Time { return now },
			CheckFn: func(context.Context) (string, error) { calls++; return "v0.2.0", nil },
		}
	}
	// First call hits the network and caches.
	mk(time.Unix(0, 0)).Notify(context.Background(), &strings.Builder{})
	// Second call within the TTL uses the cache (no network).
	mk(time.Unix(int64((notifierTTL-time.Minute).Seconds()), 0)).Notify(context.Background(), &strings.Builder{})
	if calls != 1 {
		t.Errorf("checked %d times within TTL, want 1 (cached)", calls)
	}
	// After the TTL it refreshes.
	mk(time.Unix(int64((notifierTTL+time.Hour).Seconds()), 0)).Notify(context.Background(), &strings.Builder{})
	if calls != 2 {
		t.Errorf("checked %d times after TTL, want 2 (refreshed)", calls)
	}
}

func TestNotifyFailSilent(t *testing.T) {
	n := Notifier{
		Current: "v0.1.0", CacheDir: t.TempDir(),
		Now:     func() time.Time { return time.Unix(1000, 0) },
		CheckFn: func(context.Context) (string, error) { return "", errors.New("no network") },
	}
	var w strings.Builder
	if n.Notify(context.Background(), &w) || w.Len() != 0 {
		t.Errorf("network failure must be silent, got %q", w.String())
	}
}

func TestNotifyDevBuildSilent(t *testing.T) {
	n := Notifier{
		Current: "v0.1.0-11-gabc1234", CacheDir: t.TempDir(),
		CheckFn: func(context.Context) (string, error) { t.Fatal("dev build must not check"); return "", nil },
	}
	if n.Notify(context.Background(), &strings.Builder{}) {
		t.Error("dev build should never notify")
	}
}

func TestNotifyDisabledByEnv(t *testing.T) {
	t.Setenv(notifierDisableEnv, "1")
	n := Notifier{
		Current: "v0.1.0", CacheDir: t.TempDir(),
		CheckFn: func(context.Context) (string, error) { t.Fatal("must not check when disabled"); return "", nil },
	}
	if n.Notify(context.Background(), &strings.Builder{}) {
		t.Error("notifier disabled by env should do nothing")
	}
}
