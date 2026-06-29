package state

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/open-source-cloud/devstack/internal/lock"
)

// G5 — the two-terminal concurrency guarantee, exercised across REAL OS processes
// (not just goroutines). The ledger is pre-created once; then N copies of the test
// binary each allocate K host ports under the machine-global flock. Port
// allocation is the mutation that CANNOT be made idempotent — two processes
// handing out the same host port is a hard bug — so distinct ports across every
// process is a direct proof the cross-process flock serialized the critical
// section. If the flock were a no-op, two processes would read the same "lowest
// free" port and collide (spec 08, ARCHITECTURE §"#1 rule: lock-first").

const (
	helperEnv    = "DEVSTACK_STATE_ALLOC_HELPER"
	portsPerProc = 6
	helperProcs  = 5
	allocBase    = 41000
	allocCeiling = 42000
)

// TestHelperAllocPorts is the subprocess body (gated by the env var); a no-op
// during a normal `go test` run.
func TestHelperAllocPorts(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	dir := os.Getenv("HELPER_DATA")
	lockPath := os.Getenv("HELPER_LOCK")
	id := os.Getenv("HELPER_ID")

	db, err := Open(context.Background(), dir, "ctx")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(3)
	}
	defer db.Close()

	ports := make([]string, 0, portsPerProc)
	for i := range portsPerProc {
		var p int
		err := lock.WithLock(context.Background(), lockPath, func() error {
			var e error
			// Unique (owner,purpose) per allocation so PortFor never short-circuits;
			// the flock is what must keep the chosen ports distinct across processes.
			p, e = db.AllocatePort("proc"+id, "p"+strconv.Itoa(i), allocBase, allocCeiling, func(int) bool { return true })
			return e
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "alloc:", err)
			os.Exit(4)
		}
		ports = append(ports, strconv.Itoa(p))
	}
	// Only the port line goes to stdout; os.Exit skips the test framework's trailer.
	fmt.Fprintln(os.Stdout, strings.Join(ports, ","))
	os.Exit(0)
}

func TestCrossProcessPortAllocationNeverCollides(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(t.TempDir(), "devstack.lock")

	// Pre-create + migrate the ledger once so the concurrent children only race the
	// ALLOCATION critical section (not schema creation). This isolates the flock
	// guarantee we care about here.
	db, err := Open(context.Background(), dir, "ctx")
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	db.Close()

	var (
		mu      sync.Mutex
		outputs []string
		failed  int
		wg      sync.WaitGroup
	)
	for i := range helperProcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperAllocPorts$")
			cmd.Env = append(os.Environ(),
				helperEnv+"=1",
				"HELPER_DATA="+dir,
				"HELPER_LOCK="+lockPath,
				"HELPER_ID="+strconv.Itoa(i),
			)
			out, err := cmd.Output()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				t.Logf("helper %d failed: %v", i, err)
				return
			}
			outputs = append(outputs, strings.TrimSpace(string(out)))
		}(i)
	}
	wg.Wait()

	if failed > 0 {
		t.Fatalf("%d/%d helper processes failed", failed, helperProcs)
	}

	seen := map[string]bool{}
	total := 0
	for _, line := range outputs {
		for p := range strings.SplitSeq(lastNonEmptyLine(line), ",") {
			if p == "" {
				continue
			}
			if seen[p] {
				t.Errorf("port %s allocated by two processes — the cross-process flock did not serialize allocation", p)
			}
			seen[p] = true
			total++
		}
	}
	if want := helperProcs * portsPerProc; total != want {
		t.Errorf("allocated %d distinct ports, want %d", total, want)
	}
}

// lastNonEmptyLine guards against any stray output before the ports line.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
