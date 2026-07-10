package workspace

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-source-cloud/devstack/internal/xdg"
)

// netshTimeout bounds the netsh.exe call so a wedged Windows side never hangs
// the CLI; on timeout we fall back to "no exclusions" (the bind-test still runs).
const netshTimeout = 3 * time.Second

// portRange is an inclusive [start,end] host-port range that cannot be published.
type portRange struct{ start, end int }

// netshRunner returns the raw `netsh int ipv4 show excludedportrange` output.
// Overridable in tests; nil in production means "shell out to netsh.exe".
var netshRunner func() string

// wsl2Detect gates the exclusion query to WSL2. A package var so tests can force
// the WSL2 path deterministically on any platform.
var wsl2Detect = xdg.IsWSL2

var (
	excludedMu       sync.Mutex
	excludedComputed bool
	excludedCache    []portRange
)

// excludedPortRanges returns the host-port ranges that cannot be bound as a
// published Docker port on this host. On WSL2 with Docker Desktop, Windows and
// Hyper-V DYNAMICALLY reserve TCP port ranges (`netsh int ipv4 show
// excludedportrange protocol=tcp`); the ranges change on every Windows reboot.
// A bind-test inside the Linux distro does NOT see them, so Docker Desktop's
// Windows-side port forward fails with:
//
//	ports are not available: exposing port TCP 127.0.0.1:X -> 127.0.0.1:0:
//	/forwards/expose returned unexpected status: 500
//
// Treating these ranges as unavailable during allocation is what keeps `expose`
// (and any other host-published port) working on WSL2. Non-WSL2 hosts have no
// such exclusions and return nil. Result is cached for this process' lifetime
// (the CLI is short-lived; ranges are stable within a boot).
func excludedPortRanges() []portRange {
	excludedMu.Lock()
	defer excludedMu.Unlock()
	if !excludedComputed {
		excludedComputed = true
		if wsl2Detect() {
			run := netshRunner
			if run == nil {
				run = runNetshExcluded
			}
			excludedCache = parseExcludedPortRanges(run())
		}
	}
	return excludedCache
}

// resetExcludedCache clears the memoized ranges so a later call recomputes.
// Used by tests that inject a fake netsh output; a no-op cost in production.
func resetExcludedCache() {
	excludedMu.Lock()
	excludedComputed = false
	excludedCache = nil
	excludedMu.Unlock()
}

// runNetshExcluded shells out to the Windows netsh.exe (reachable from WSL2) for
// the TCP excluded-port-range table. A failure (netsh missing, non-Desktop WSL2)
// yields no exclusions rather than an error — the bind-test remains the backstop.
func runNetshExcluded() string {
	ctx, cancel := context.WithTimeout(context.Background(), netshTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "netsh.exe", "int", "ipv4",
		"show", "excludedportrange", "protocol=tcp").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// parseExcludedPortRanges extracts inclusive [start,end] pairs from netsh's
// excluded-port-range table. Each data row is two integers (start, end) with an
// optional trailing "*" note; the title, header, and separator lines have no
// leading integer pair. Matching on the "two integers begin the line" shape (not
// on column headings) keeps it locale-independent — netsh localizes its headers.
func parseExcludedPortRanges(text string) []portRange {
	var ranges []portRange
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		start, err1 := strconv.Atoi(fields[0])
		end, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || start <= 0 || end < start {
			continue
		}
		ranges = append(ranges, portRange{start, end})
	}
	return ranges
}

// portExcluded reports whether p falls inside any host-reserved excluded range.
func portExcluded(p int) bool {
	for _, r := range excludedPortRanges() {
		if p >= r.start && p <= r.end {
			return true
		}
	}
	return false
}
