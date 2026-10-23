package docker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/driver"
)

func TestStatsAgainstFakeDaemon(t *testing.T) {
	fd, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	d := NewWithClient(Options{Name: "docker", UID: 1000, GID: 1000, MaxOutputBytes: 1 << 20}, cli)
	ctx := context.Background()
	h, err := d.Create(ctx, driver.SandboxSpec{ID: "sbx_stats000000001", Image: "alpine:3.20", CPU: 1, MemoryMB: 64, DiskMB: 64, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}

	st, err := d.Stats(ctx, h.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	q, cmd := fd.statsQuery, strings.Join(fd.lastCmd, " ")
	fd.mu.Unlock()
	if strings.Contains(q, "one-shot") || !strings.Contains(cmd, "df -Pk /workspace") {
		t.Fatalf("query %q cmd %q", q, cmd)
	}
	if st.CPUSeconds != 1.5 || st.CPUPercent == nil || *st.CPUPercent != 50 {
		t.Fatalf("cpu %+v", st)
	}
	if st.MemoryBytes != 40<<20 || st.MemoryLimitBytes != 512<<20 || st.PIDs != 4 {
		t.Fatalf("mem %+v", st)
	}
	if st.NetRxBytes != 101 || st.NetTxBytes != 202 {
		t.Fatalf("net %+v", st)
	}
	if st.DiskUsedBytes != 2048*1024 || st.DiskTotalBytes != 65536*1024 {
		t.Fatalf("disk %+v", st)
	}

	st, err = d.Stats(ctx, h.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	q = fd.statsQuery
	fd.mu.Unlock()
	if !strings.Contains(q, "one-shot=true") || st.CPUPercent != nil || st.DiskUsedBytes != -1 || st.CPUSeconds != 1.5 {
		t.Fatalf("cheap sample %q %+v", q, st)
	}

	if _, err := d.Stats(ctx, "missing", false); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestParseDF(t *testing.T) {
	for _, tc := range []struct {
		in          string
		used, total int64
	}{
		{"Filesystem 1024-blocks Used Available Capacity Mounted on\ntmpfs 100 25 75 25% /workspace\n", 25 * 1024, 100 * 1024},
		{"", -1, -1},
		{"df: /workspace: No such file or directory\n", -1, -1},
		{"Filesystem 1024-blocks Used\nx y z\n", -1, -1},
	} {
		used, total := parseDF([]byte(tc.in))
		if used != tc.used || total != tc.total {
			t.Fatalf("%q: got %d/%d", tc.in, used, total)
		}
	}
}

func TestMemoryUsed(t *testing.T) {
	if got := memoryUsed(100, map[string]uint64{"total_inactive_file": 30}); got != 70 {
		t.Fatal(got)
	}
	if got := memoryUsed(10, map[string]uint64{"inactive_file": 30}); got != 10 {
		t.Fatal(got)
	}
}
