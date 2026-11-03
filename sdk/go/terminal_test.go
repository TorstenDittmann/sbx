package sbx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// serverFrame writes an unmasked server frame.
func serverFrame(w io.Writer, op byte, data []byte) {
	hdr := []byte{0x80 | op}
	switch n := len(data); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 65536:
		hdr = append(hdr, 126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	w.Write(append(hdr, data...))
}

func TestFrameRoundTrip(t *testing.T) {
	for _, n := range []int{0, 5, 125, 126, 70000} {
		var buf bytes.Buffer
		data := bytes.Repeat([]byte{'x'}, n)
		if err := writeFrame(&buf, opBinary, data); err != nil {
			t.Fatal(err)
		}
		op, got, err := readFrame(&buf)
		if err != nil || op != opBinary || !bytes.Equal(got, data) {
			t.Fatalf("n=%d op=%d len=%d err=%v", n, op, len(got), err)
		}
	}
	if err := writeFrame(io.Discard, opBinary, make([]byte, wsMaxMessage+1)); err == nil {
		t.Fatal("want size error")
	}
}

func TestTerminalSession(t *testing.T) {
	got := make(chan string, 4)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sbx_test" || r.URL.Path != "/v1/sandboxes/sbx_1/terminals/term_1/ws" {
			http.Error(w, `{"error":{"code":"unauthorized","message":"nope"}}`, http.StatusUnauthorized)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsMagic))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		rw.Flush()
		serverFrame(conn, opPing, []byte("p"))
		serverFrame(conn, opBinary, []byte("hello "))
		serverFrame(conn, opBinary, []byte("world"))
		br := bufio.NewReader(rw)
		for i := 0; i < 3; i++ {
			op, data, err := readFrame(br)
			if err != nil {
				t.Error(err)
				return
			}
			got <- fmt.Sprintf("%d:%s", op, data)
		}
		serverFrame(conn, opText, []byte(`{"type":"exit","exit_code":7}`))
		readFrame(br) // close frame
	})
	ctx := context.Background()
	if _, err := c.ConnectTerminal(ctx, "sbx_1", "wrong"); StatusCode(err) != http.StatusUnauthorized {
		t.Fatalf("want 401, got %v", err)
	}
	s, err := c.ConnectTerminal(ctx, "sbx_1", "term_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(s)
	if err != nil || string(out) != "hello world" {
		t.Fatalf("%q %v", out, err)
	}
	if code, exited := s.ExitCode(); !exited || code != 7 {
		t.Fatalf("exit %d %v", code, exited)
	}
	frames := []string{<-got, <-got, <-got}
	// The pong goes out once Read sees the ping.
	want := []string{"2:ls\n", `1:{"cols":100,"rows":30,"type":"resize"}`, "10:p"}
	if strings.Join(frames, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", frames)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("second close", err)
	}
}
