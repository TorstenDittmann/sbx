package docker

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

type cpuStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

// containerStats is the subset of GET /containers/{id}/stats the driver reads.
type containerStats struct {
	CPU    cpuStats `json:"cpu_stats"`
	PreCPU cpuStats `json:"precpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	PIDs struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// ContainerStats takes one stats sample. oneShot skips the second CPU
// sample the daemon otherwise waits about a second for.
func (c *Client) ContainerStats(ctx context.Context, id string, oneShot bool) (containerStats, error) {
	q := "?stream=false"
	if oneShot {
		q += "&one-shot=true"
	}
	var out containerStats
	err := c.DoJSON(ctx, http.MethodGet, "/containers/"+id+"/stats"+q, nil, &out)
	return out, err
}

// Stats reports CPU, memory, network, and process usage from the engine.
// With full it also samples CPU over an interval and runs df for /workspace.
func (d *Driver) Stats(ctx context.Context, id string, full bool) (driver.Stats, error) {
	raw, err := d.client.ContainerStats(ctx, id, !full)
	if err != nil {
		return driver.Stats{}, err
	}
	st := statsFrom(raw)
	if full {
		st.DiskUsedBytes, st.DiskTotalBytes = d.workspaceUsage(ctx, id)
	}
	return st, nil
}

func statsFrom(raw containerStats) driver.Stats {
	st := driver.Stats{
		CPUSeconds:       float64(raw.CPU.CPUUsage.TotalUsage) / 1e9,
		MemoryBytes:      int64(memoryUsed(raw.Memory.Usage, raw.Memory.Stats)),
		MemoryLimitBytes: int64(raw.Memory.Limit),
		PIDs:             int64(raw.PIDs.Current),
		DiskUsedBytes:    -1,
		DiskTotalBytes:   -1,
	}
	for _, n := range raw.Networks {
		st.NetRxBytes += int64(n.RxBytes)
		st.NetTxBytes += int64(n.TxBytes)
	}
	cpuDelta := float64(raw.CPU.CPUUsage.TotalUsage) - float64(raw.PreCPU.CPUUsage.TotalUsage)
	sysDelta := float64(raw.CPU.SystemUsage) - float64(raw.PreCPU.SystemUsage)
	if raw.PreCPU.SystemUsage > 0 && sysDelta > 0 && cpuDelta >= 0 {
		cpus := float64(raw.CPU.OnlineCPUs)
		if cpus == 0 {
			cpus = float64(len(raw.CPU.CPUUsage.PercpuUsage))
		}
		if cpus > 0 {
			pct := cpuDelta / sysDelta * cpus * 100
			st.CPUPercent = &pct
		}
	}
	return st
}

// memoryUsed matches docker stats: page cache that can be reclaimed is not
// counted. cgroup v2 reports inactive_file, v1 total_inactive_file.
func memoryUsed(usage uint64, stats map[string]uint64) uint64 {
	inactive, ok := stats["inactive_file"]
	if !ok {
		inactive = stats["total_inactive_file"]
	}
	if inactive < usage {
		return usage - inactive
	}
	return usage
}

// workspaceUsage runs df inside the sandbox. /workspace is a tmpfs or a
// volume, neither of which the engine's container size accounts for.
func (d *Driver) workspaceUsage(ctx context.Context, id string) (used, total int64) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := d.Exec(ctx, id, driver.ExecRequest{Cmd: []string{"df", "-Pk", "/workspace"}})
	if err != nil || res.ExitCode != 0 {
		return -1, -1
	}
	return parseDF(res.Stdout)
}

// parseDF reads the last line of POSIX df -Pk output:
// Filesystem 1024-blocks Used Available Capacity Mounted-on
func parseDF(out []byte) (used, total int64) {
	lines := strings.Split(strings.TrimSpace(string(bytes.TrimSpace(out))), "\n")
	if len(lines) < 2 {
		return -1, -1
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return -1, -1
	}
	t, err1 := strconv.ParseInt(f[1], 10, 64)
	u, err2 := strconv.ParseInt(f[2], 10, 64)
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return u * 1024, t * 1024
}
