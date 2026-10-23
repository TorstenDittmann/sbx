package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/store"
)

type statsResponse struct {
	SandboxID string         `json:"sandbox_id"`
	Status    string         `json:"status"`
	SampledAt time.Time      `json:"sampled_at"`
	CPU       cpuResponse    `json:"cpu"`
	Memory    memoryResponse `json:"memory"`
	Disk      diskResponse   `json:"disk"`
	Network   netResponse    `json:"network"`
	PIDs      pidsResponse   `json:"pids"`
}

type cpuResponse struct {
	UsageSeconds float64  `json:"usage_seconds"`
	Percent      *float64 `json:"percent"`
	LimitCores   float64  `json:"limit_cores"`
}

type memoryResponse struct {
	UsedBytes  int64 `json:"used_bytes"`
	LimitBytes int64 `json:"limit_bytes"`
}

type diskResponse struct {
	UsedBytes  *int64 `json:"used_bytes"`
	TotalBytes *int64 `json:"total_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
}

type netResponse struct {
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`
}

type pidsResponse struct {
	Current int64 `json:"current"`
	Limit   int   `json:"limit"`
}

func (s *Server) sandboxStats(w http.ResponseWriter, r *http.Request) {
	sb, st, err := s.mgr.Stats(r.Context(), actor(r), r.PathValue("id"))
	if err != nil {
		writeManager(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statsJSON(sb, st, time.Now().UTC()))
}

func statsJSON(sb store.Sandbox, st driver.Stats, at time.Time) statsResponse {
	memLimit := int64(sb.MemoryMB) << 20
	if memLimit == 0 {
		memLimit = st.MemoryLimitBytes
	}
	return statsResponse{
		SandboxID: sb.ID,
		Status:    sb.Status,
		SampledAt: at,
		CPU:       cpuResponse{UsageSeconds: st.CPUSeconds, Percent: st.CPUPercent, LimitCores: sb.CPU},
		Memory:    memoryResponse{UsedBytes: st.MemoryBytes, LimitBytes: memLimit},
		Disk: diskResponse{
			UsedBytes: known(st.DiskUsedBytes), TotalBytes: known(st.DiskTotalBytes),
			LimitBytes: int64(sb.DiskMB) << 20,
		},
		Network: netResponse{RxBytes: st.NetRxBytes, TxBytes: st.NetTxBytes},
		PIDs:    pidsResponse{Current: st.PIDs, Limit: sb.PIDs},
	}
}

func known(v int64) *int64 {
	if v < 0 {
		return nil
	}
	return &v
}

// writeSandboxMetrics appends per-sandbox series for every live sandbox the
// backend can sample. Disk is left out: it needs an exec per sandbox.
func (s *Server) writeSandboxMetrics(r *http.Request, b *strings.Builder) {
	samples, err := s.mgr.SampleStats(r.Context())
	if err != nil {
		s.log.Warn("sandbox stats", "err", err)
		return
	}
	type series struct {
		name, help, kind string
		value            func(st driver.Stats, sb store.Sandbox) float64
	}
	all := []series{
		{"sbx_sandbox_cpu_seconds_total", "CPU time used by the sandbox.", "counter",
			func(st driver.Stats, _ store.Sandbox) float64 { return st.CPUSeconds }},
		{"sbx_sandbox_cpu_limit_cores", "CPU limit of the sandbox in cores.", "gauge",
			func(_ driver.Stats, sb store.Sandbox) float64 { return sb.CPU }},
		{"sbx_sandbox_memory_bytes", "Memory used by the sandbox, without reclaimable page cache.", "gauge",
			func(st driver.Stats, _ store.Sandbox) float64 { return float64(st.MemoryBytes) }},
		{"sbx_sandbox_memory_limit_bytes", "Memory limit of the sandbox.", "gauge",
			func(st driver.Stats, sb store.Sandbox) float64 {
				if sb.MemoryMB > 0 {
					return float64(int64(sb.MemoryMB) << 20)
				}
				return float64(st.MemoryLimitBytes)
			}},
		{"sbx_sandbox_network_receive_bytes_total", "Bytes received by the sandbox.", "counter",
			func(st driver.Stats, _ store.Sandbox) float64 { return float64(st.NetRxBytes) }},
		{"sbx_sandbox_network_transmit_bytes_total", "Bytes sent by the sandbox.", "counter",
			func(st driver.Stats, _ store.Sandbox) float64 { return float64(st.NetTxBytes) }},
		{"sbx_sandbox_pids", "Processes running in the sandbox.", "gauge",
			func(st driver.Stats, _ store.Sandbox) float64 { return float64(st.PIDs) }},
	}
	for _, m := range all {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
		for _, smp := range samples {
			fmt.Fprintf(b, "%s{sandbox=%q,backend=%q} %g\n", m.name, smp.Sandbox.ID, smp.Sandbox.Backend, m.value(smp.Stats, smp.Sandbox))
		}
	}
}
