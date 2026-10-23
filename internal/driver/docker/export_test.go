package docker

import (
	"context"
	"io"
	"testing"
)

func TestExportFilesystem(t *testing.T) {
	_, sock := startFakeDaemon(t)
	cli, err := NewClient(sock)
	if err != nil {
		t.Fatal(err)
	}
	body, err := cli.ExportFilesystem(context.Background(), "alpine:3.20", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "rootfs-tar" {
		t.Fatalf("%q", raw)
	}
}
