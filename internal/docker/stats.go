package docker

import (
	"context"
	"encoding/json"
	"fmt"

	ctypes "github.com/moby/moby/api/types/container"
	moby "github.com/moby/moby/client"
)

// Stats is the read-only, decoded resource-usage projection for one container
// (spec 16 CPU/mem columns). CPUPercent is the *computed* engine-side percentage
// (deltas over system usage × online CPUs) — NOT the daemon's pre-baked value,
// and on the Docker Desktop / WSL2 VM it reflects the VM's CPU allocation, not
// the host's (spec 16 §gotchas: label the column "CPU% (engine)"). MemUsage is
// cache-adjusted (inactive file pages subtracted) to match `docker stats`.
type Stats struct {
	CPUPercent float64 // 0..(100×onlineCPUs)
	MemUsage   uint64  // bytes, cache-adjusted
	MemLimit   uint64  // bytes; 0 when the Engine reports no limit
	MemPercent float64 // MemUsage / MemLimit × 100 (0 when MemLimit==0)
}

// ContainerStats retrieves a single resource-usage sample for one container and
// decodes it into a small Stats projection. It asks the daemon to include a
// previous sample (IncludePreviousSample) so CPU% can be computed from the
// cpu/precpu delta in one call, without holding a streaming reader open — the
// dashboard fetches this per visible container on each poll. Strictly read-only.
func (m *mobyClient) ContainerStats(ctx context.Context, id string) (Stats, error) {
	res, err := m.cli.ContainerStats(ctx, id, moby.ContainerStatsOptions{
		Stream:                false,
		IncludePreviousSample: true,
	})
	if err != nil {
		return Stats{}, fmt.Errorf("stats for container %q: %w", id, err)
	}
	defer func() { _ = res.Body.Close() }()

	var sr ctypes.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		return Stats{}, fmt.Errorf("decode stats for container %q: %w", id, err)
	}
	return statsFromResponse(sr), nil
}

// statsFromResponse projects a raw Engine StatsResponse into the Stats struct,
// applying the Docker CPU% formula and the cache-adjusted memory accounting.
func statsFromResponse(sr ctypes.StatsResponse) Stats {
	onlineCPUs := sr.CPUStats.OnlineCPUs
	if onlineCPUs == 0 {
		onlineCPUs = uint32(len(sr.CPUStats.CPUUsage.PercpuUsage))
	}
	st := Stats{
		CPUPercent: cpuPercent(
			sr.CPUStats.CPUUsage.TotalUsage,
			sr.PreCPUStats.CPUUsage.TotalUsage,
			sr.CPUStats.SystemUsage,
			sr.PreCPUStats.SystemUsage,
			onlineCPUs,
		),
		MemUsage: memUsageNoCache(sr.MemoryStats),
		MemLimit: sr.MemoryStats.Limit,
	}
	if st.MemLimit > 0 {
		st.MemPercent = float64(st.MemUsage) / float64(st.MemLimit) * 100
	}
	return st
}

// cpuPercent computes a container's CPU utilisation the way the Docker CLI does:
// the container's CPU-time delta as a fraction of the system CPU-time delta,
// scaled by the number of online CPUs. Returns 0 when either delta is
// non-positive (the first sample, or an idle container) so callers never divide
// by zero. Pure and table-driven testable.
func cpuPercent(cpuTotal, preCPUTotal, systemUsage, preSystemUsage uint64, onlineCPUs uint32) float64 {
	// uint64 subtraction guarded against counter resets / a missing previous
	// sample (which would wrap negative).
	if cpuTotal < preCPUTotal || systemUsage < preSystemUsage {
		return 0
	}
	cpuDelta := float64(cpuTotal - preCPUTotal)
	systemDelta := float64(systemUsage - preSystemUsage)
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}
	n := float64(onlineCPUs)
	if n <= 0 {
		n = 1
	}
	return (cpuDelta / systemDelta) * n * 100
}

// memUsageNoCache returns memory usage with the page cache subtracted, matching
// `docker stats`: on cgroup v1 subtract total_inactive_file, on v2 subtract
// inactive_file (whichever is present and below Usage). Falls back to raw Usage.
func memUsageNoCache(mem ctypes.MemoryStats) uint64 {
	if v, ok := mem.Stats["total_inactive_file"]; ok && v <= mem.Usage {
		return mem.Usage - v
	}
	if v, ok := mem.Stats["inactive_file"]; ok && v <= mem.Usage {
		return mem.Usage - v
	}
	return mem.Usage
}
