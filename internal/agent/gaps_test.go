package agent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestErrorsAndHandshakeFrame(t *testing.T) {
	err := errUnsupported("pty is unavailable")
	if err.Error() != "pty is unavailable" || !IsUnsupported(err) || IsUnsupported(errors.New("other")) {
		t.Fatalf("unsupported %v", err)
	}

	client, server := net.Pipe()
	go func() {
		frame, readErr := ReadFrame(server)
		if readErr != nil || frame.Type != MsgHello {
			return
		}
		_ = WriteFrame(server, 0, MsgData, []byte("nope"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Handshake(ctx, client, "tok"); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatal(err)
	}

	var empty Client
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessEdges(t *testing.T) {
	a := New("tok")
	a.maxProcs = 1
	c, done := pair(t, a)
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := c.Start(ctx, ExecSpec{Cmd: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(ctx, ExecSpec{Cmd: []string{"sleep", "30"}}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatal(err)
	}
	if err := c.Signal(ctx, first.ID, "NOPE"); err == nil {
		t.Fatal("expected unknown signal")
	}
	if err := c.Signal(ctx, "missing", "KILL"); err == nil {
		t.Fatal("expected missing process")
	}
	if err := c.Resize(ctx, first.ID, 40, 12); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatal(err)
	}
	if err := c.Resize(ctx, "missing", 40, 12); err == nil {
		t.Fatal("expected missing resize")
	}
	payload := bytes.Repeat([]byte("x"), ChunkSize+3)
	if err := c.WriteStdin(ctx, first.ID, payload); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteStdin(ctx, "missing", []byte("z")); err == nil {
		t.Fatal("expected missing stdin")
	}
	if err := c.Signal(ctx, first.ID, "15"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err := c.List(ctx)
		if err == nil {
			for _, item := range list {
				if item.ID == first.ID && item.Status == "exited" {
					goto exited
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("not exited %+v %v", list, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
exited:
	second, err := c.Start(ctx, ExecSpec{Cmd: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Signal(ctx, second.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dial(ctx, 0); err == nil {
		t.Fatal("expected invalid port")
	}
	if _, err := c.Dial(ctx, 1); err == nil {
		t.Fatal("expected refused dial")
	}

	s, err := c.mux.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(99, []byte("nope")); err != nil {
		t.Fatal(err)
	}
	frame, err := s.Recv(ctx)
	if err != nil || frame.Type != MsgError || !bytes.Contains(frame.Data, []byte("unknown")) {
		t.Fatalf("%+v %v", frame, err)
	}
	s.Close()

	bad, err := c.mux.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Send(MsgExec, []byte("{")); err != nil {
		t.Fatal(err)
	}
	frame, err = bad.Recv(ctx)
	if err != nil || frame.Type != MsgError {
		t.Fatalf("bad exec %+v %v", frame, err)
	}
	bad.Close()

	if !c.Alive() {
		t.Fatal("expected alive")
	}
	c.Close()
	select {
	case <-c.mux.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not stop")
	}
	if c.Alive() {
		t.Fatal("expected closed session")
	}
}

func TestExecEmitErrorAndFileEdges(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "target"), link); err != nil {
		t.Fatal(err)
	}
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.Exec(ctx, ExecSpec{Cmd: []string{"echo", "hi"}}, nil, func(uint8, []byte) error {
		return errors.New("stop")
	})
	if err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, "relative"); err == nil {
		t.Fatal("expected relative path to fail")
	}
	if _, err := c.Stat(ctx, link); err != nil {
		t.Fatal(err)
	}
	info, err := c.Stat(ctx, link)
	if err != nil || info.Type != "symlink" {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := c.ListFiles(ctx, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected missing dir")
	}
	if err := c.Mkdir(ctx, filepath.Join(dir, "d"), 0); err != nil {
		t.Fatal(err)
	}
	conn, err := c.Dial(ctx, 1)
	if err == nil {
		conn.Close()
		t.Fatal("expected dial error")
	}
}

func TestStreamConnMetadata(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8)
		n, _ := conn.Read(buf)
		_, _ = conn.Write(buf[:n])
	}()
	c, cleanup := startAgent(t, "tok")
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := c.Dial(ctx, ln.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	if conn.LocalAddr().Network() != "sbx" || conn.LocalAddr().String() != "local" {
		t.Fatalf("local %s %s", conn.LocalAddr().Network(), conn.LocalAddr())
	}
	if conn.RemoteAddr().String() != "sandbox" {
		t.Fatal(conn.RemoteAddr())
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("%q %v", buf[:n], err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}
