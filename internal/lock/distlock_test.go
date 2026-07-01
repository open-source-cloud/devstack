package lock

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFileLocker_RunsFn(t *testing.T) {
	l := NewFileLocker(filepath.Join(t.TempDir(), "x.lock"))
	ran := false
	if err := l.WithLock(context.Background(), func() error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn did not run under the file lock")
	}
}

func TestFileLocker_EmptyPathRunsUnlocked(t *testing.T) {
	ran := false
	if err := (FileLocker{}).WithLock(context.Background(), func() error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("empty-path FileLocker should run fn unlocked")
	}
}

func TestLockerFor_Selects(t *testing.T) {
	connect := func(context.Context) (AdvisoryConn, error) { return nil, nil }

	// Remote + a connector → distributed pg lock.
	if _, ok := LockerFor(true, "subj", "/tmp/x.lock", connect).(*PGLocker); !ok {
		t.Error("remote + connector should select *PGLocker")
	}
	// Remote but no connector (reachability not wired) → safe local fallback.
	if _, ok := LockerFor(true, "subj", "/tmp/x.lock", nil).(FileLocker); !ok {
		t.Error("remote + nil connector should fall back to FileLocker")
	}
	// Local → file lock.
	if _, ok := LockerFor(false, "subj", "/tmp/x.lock", connect).(FileLocker); !ok {
		t.Error("local should select FileLocker")
	}
}
