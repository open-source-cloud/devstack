package workspace

import (
	"context"
	"testing"
)

// netshSample mirrors real `netsh int ipv4 show excludedportrange` output,
// including the header/separator lines (which the parser must skip regardless of
// locale) and the trailing "*" note on administered exclusions.
const netshSample = `
Protocol tcp Port Exclusion Ranges

Start Port    End Port
----------    --------
     50000       50059     *
     54235       54235
     58956       59055
     59056       59155

* - Administered port exclusions.
`

func TestParseExcludedPortRanges(t *testing.T) {
	got := parseExcludedPortRanges(netshSample)
	want := []portRange{{50000, 50059}, {54235, 54235}, {58956, 59055}, {59056, 59155}}
	if len(got) != len(want) {
		t.Fatalf("parsed %d ranges, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseExcludedPortRangesIgnoresJunk(t *testing.T) {
	if r := parseExcludedPortRanges(""); r != nil {
		t.Errorf("empty input yielded %+v", r)
	}
	// Header-only (no data rows) → no ranges. A reversed/invalid pair is dropped.
	if r := parseExcludedPortRanges("Start End\n---- ----\n900 100\n"); len(r) != 0 {
		t.Errorf("invalid rows yielded %+v", r)
	}
}

// withExcludedRanges forces the WSL2 exclusion path with a fake netsh output for
// one test (deterministic on any platform) and restores + clears the cache after.
func withExcludedRanges(t *testing.T, output string) {
	t.Helper()
	prevRunner, prevDetect := netshRunner, wsl2Detect
	netshRunner = func() string { return output }
	wsl2Detect = func() bool { return true }
	resetExcludedCache()
	t.Cleanup(func() {
		netshRunner, wsl2Detect = prevRunner, prevDetect
		resetExcludedCache()
	})
}

func TestFreeHostPortReallocatesExcludedPersistedPort(t *testing.T) {
	withExcludedRanges(t, netshSample)
	m := newManager(t, nil)
	ctx := context.Background()

	// Pre-seed the ledger with a port that lands inside an excluded range,
	// simulating an allocation made before Windows reserved that range.
	if p, err := m.DB.AllocatePort("minio", "minio-expose", 59000, 59000, nil); err != nil || p != 59000 {
		t.Fatalf("seed port: got %d, err %v", p, err)
	}
	port, err := m.FreeHostPort(ctx, "minio", "minio-expose", 59000)
	if err != nil {
		t.Fatal(err)
	}
	if portExcluded(port) {
		t.Fatalf("re-allocated into an excluded range: %d", port)
	}
	// Stable afterward.
	if again, _ := m.FreeHostPort(ctx, "minio", "minio-expose", 59000); again != port {
		t.Errorf("port not stable after reallocation: %d vs %d", again, port)
	}
}
