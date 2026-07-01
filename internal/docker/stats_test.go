package docker

import (
	"context"
	"math"
	"testing"

	ctypes "github.com/moby/moby/api/types/container"
)

// TestCPUPercent covers the Docker CPU% formula from a stats delta: the
// container CPU-time delta over the system CPU-time delta, scaled by online CPUs.
func TestCPUPercent(t *testing.T) {
	tests := []struct {
		name                               string
		cpuTotal, preCPU, sysUsage, preSys uint64
		onlineCPUs                         uint32
		want                               float64
	}{
		// 1 of 1 CPU fully busy: cpuDelta == systemDelta, 1 CPU → 100%.
		{"one-cpu-full", 2000, 1000, 2000, 1000, 1, 100},
		// Half of one CPU: cpuDelta is half the system delta → 50%.
		{"half-cpu", 1500, 1000, 2000, 1000, 1, 50},
		// 4 CPUs, container uses one full core's worth of the aggregate → 100%.
		{"one-of-four", 2000, 1000, 4000, 0, 4, 100},
		// onlineCPUs derived elsewhere as 0 defaults to 1 (never divide by zero).
		{"zero-online-defaults-one", 1500, 1000, 2000, 1000, 0, 50},
		// First sample: no previous → both deltas zero → 0, not NaN.
		{"first-sample", 1000, 1000, 2000, 2000, 2, 0},
		// Counter reset (cur < prev): guarded to 0, never negative/huge.
		{"cpu-reset", 500, 1000, 2000, 1000, 1, 0},
		{"system-reset", 2000, 1000, 500, 1000, 1, 0},
		// System delta zero but cpu delta positive → 0 (avoid inf).
		{"no-system-delta", 2000, 1000, 1000, 1000, 1, 0},
		// Idle container: no cpu delta → 0.
		{"idle", 1000, 1000, 5000, 1000, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cpuPercent(tt.cpuTotal, tt.preCPU, tt.sysUsage, tt.preSys, tt.onlineCPUs)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("cpuPercent = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStatsFromResponse verifies the full projection: CPU%, cache-adjusted
// memory, limit, and the derived memory percentage.
func TestStatsFromResponse(t *testing.T) {
	sr := ctypes.StatsResponse{
		CPUStats: ctypes.CPUStats{
			CPUUsage:    ctypes.CPUUsage{TotalUsage: 2000},
			SystemUsage: 2000,
			OnlineCPUs:  2,
		},
		PreCPUStats: ctypes.CPUStats{
			CPUUsage:    ctypes.CPUUsage{TotalUsage: 1000},
			SystemUsage: 1000,
		},
		MemoryStats: ctypes.MemoryStats{
			Usage: 500,
			Limit: 1000,
			Stats: map[string]uint64{"inactive_file": 100},
		},
	}
	st := statsFromResponse(sr)
	// cpuDelta=1000, systemDelta=1000, 2 CPUs → 200%.
	if math.Abs(st.CPUPercent-200) > 1e-9 {
		t.Errorf("CPUPercent = %v, want 200", st.CPUPercent)
	}
	// 500 - 100 (inactive_file) = 400 bytes.
	if st.MemUsage != 400 {
		t.Errorf("MemUsage = %d, want 400", st.MemUsage)
	}
	if st.MemLimit != 1000 {
		t.Errorf("MemLimit = %d, want 1000", st.MemLimit)
	}
	// 400 / 1000 = 40%.
	if math.Abs(st.MemPercent-40) > 1e-9 {
		t.Errorf("MemPercent = %v, want 40", st.MemPercent)
	}
}

// TestStatsFromResponseOnlineCPUsFromPercpu covers the fallback where the Engine
// omits online_cpus and the count comes from the per-CPU usage slice length.
func TestStatsFromResponseOnlineCPUsFromPercpu(t *testing.T) {
	sr := ctypes.StatsResponse{
		CPUStats: ctypes.CPUStats{
			CPUUsage:    ctypes.CPUUsage{TotalUsage: 2000, PercpuUsage: []uint64{1, 1, 1, 1}},
			SystemUsage: 4000,
		},
		PreCPUStats: ctypes.CPUStats{CPUUsage: ctypes.CPUUsage{TotalUsage: 1000}},
	}
	st := statsFromResponse(sr)
	// cpuDelta=1000, systemDelta=4000, 4 CPUs → 100%.
	if math.Abs(st.CPUPercent-100) > 1e-9 {
		t.Fatalf("CPUPercent = %v, want 100", st.CPUPercent)
	}
}

// TestMemUsageNoCacheV1 covers the cgroup v1 branch (total_inactive_file).
func TestMemUsageNoCacheV1(t *testing.T) {
	got := memUsageNoCache(ctypes.MemoryStats{Usage: 1000, Stats: map[string]uint64{"total_inactive_file": 250}})
	if got != 750 {
		t.Fatalf("MemUsage = %d, want 750", got)
	}
}

// TestMemUsageNoCacheRaw falls back to raw Usage when no cache stat is present.
func TestMemUsageNoCacheRaw(t *testing.T) {
	got := memUsageNoCache(ctypes.MemoryStats{Usage: 1000})
	if got != 1000 {
		t.Fatalf("MemUsage = %d, want 1000", got)
	}
}

// TestMockContainerStats verifies the mock returns seeded samples and counts calls.
func TestMockContainerStats(t *testing.T) {
	m := &MockClient{Stats: map[string]Stats{"c1": {CPUPercent: 12.5, MemUsage: 400, MemLimit: 1000}}}
	st, err := m.ContainerStats(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if st.CPUPercent != 12.5 || st.MemUsage != 400 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	if _, err := m.ContainerStats(context.Background(), "missing"); err != nil {
		t.Fatalf("unseeded id should not error: %v", err)
	}
	if m.StatsCalls != 2 {
		t.Fatalf("StatsCalls = %d, want 2", m.StatsCalls)
	}
}
