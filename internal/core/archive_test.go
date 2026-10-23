package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/TorstenDittmann/sbx/internal/config"
	"github.com/TorstenDittmann/sbx/internal/driver"
	"github.com/TorstenDittmann/sbx/internal/driver/fake"
)

type recordingArchive struct {
	*fake.Driver
	dest string
	body []byte
	max  int64
	dir  string
	fail error
}

func (r *recordingArchive) NativeArchive() bool { return true }

func (r *recordingArchive) ExtractArchive(_ context.Context, _, dest string, rd io.Reader, max int64) error {
	b, err := io.ReadAll(rd)
	r.dest = dest
	r.body = b
	r.max = max
	if err != nil {
		return err
	}
	return r.fail
}

func (r *recordingArchive) PackArchive(_ context.Context, _, dir string, _ int) ([]byte, error) {
	r.dir = dir
	if r.fail != nil {
		return nil, r.fail
	}
	return []byte{0x1f, 0x8b, 0x08}, nil
}

func TestNativeArchiveSkipsTar(t *testing.T) {
	st := openStore(t)
	rec := &recordingArchive{Driver: fake.New("docker")}
	cfg := config.Default()
	mgr := New(cfg, st, map[string]driver.Driver{"docker": rec}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("ustar-body")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteArchive(ctx, actor, sb.ID, "/workspace/in", bytes.NewReader(gz.Bytes())); err != nil {
		t.Fatal(err)
	}
	if rec.dest != "/workspace/in" || string(rec.body) != string(raw) || rec.max != int64(cfg.Limits.MaxArchiveBytes) {
		t.Fatalf("extract dest %s max %d body %q", rec.dest, rec.max, rec.body)
	}
	packed, err := mgr.ReadArchive(ctx, actor, sb.ID, "/workspace/in")
	if err != nil || len(packed) != 3 || rec.dir != "/workspace/in" {
		t.Fatalf("%q %s %v", packed, rec.dir, err)
	}
	rec.fail = io.ErrClosedPipe
	err = mgr.WriteArchive(ctx, actor, sb.ID, "/workspace/in", bytes.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "closed pipe") {
		t.Fatal(err)
	}
	if _, err := mgr.ReadArchive(ctx, actor, sb.ID, "/workspace/in"); err == nil {
		t.Fatal("expected pack error")
	}
}

func TestNativeArchiveStreamLimits(t *testing.T) {
	st := openStore(t)
	rec := &recordingArchive{Driver: fake.New("docker")}
	cfg := config.Default()
	cfg.Limits.MaxArchiveBytes = 4096
	mgr := New(cfg, st, map[string]driver.Driver{"docker": rec}, discard())
	ctx := context.Background()
	actor := coreActor(t, st)
	sb, err := mgr.Create(ctx, actor, CreateInput{Image: "alpine:3.20"})
	if err != nil {
		t.Fatal(err)
	}
	gzipped := func(raw []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	status := func(err error) int {
		var op *OpError
		if !errors.As(err, &op) {
			t.Fatalf("not an OpError: %v", err)
		}
		return op.Status
	}

	err = mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", bytes.NewReader(bytes.Repeat([]byte("x"), 5000)))
	if status(err) != 413 || strings.Contains(err.Error(), "gunzip") || !strings.Contains(err.Error(), "4 KiB") {
		t.Fatal(err)
	}

	// Zeros compress well, so the body fits and the tar does not.
	err = mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", bytes.NewReader(gzipped(make([]byte, 5000))))
	if status(err) != 413 || !strings.Contains(err.Error(), "after gunzip") {
		t.Fatal(err)
	}

	// A broken gzip stream is the caller's fault, not the driver's.
	bad := gzipped([]byte("ustar-body"))
	bad[len(bad)-1] ^= 0xff
	err = mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", bytes.NewReader(bad))
	if status(err) != 400 || !strings.Contains(err.Error(), "invalid gzip") {
		t.Fatal(err)
	}

	// Exactly at the limit still goes through.
	if err := mgr.WriteArchive(ctx, actor, sb.ID, "/workspace", bytes.NewReader(gzipped(make([]byte, 4096)))); err != nil {
		t.Fatal(err)
	}
	if len(rec.body) != 4096 {
		t.Fatalf("extracted %d bytes", len(rec.body))
	}
}
