package ws

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		op, data, err := conn.Read()
		if err != nil || op != OpBinary || string(data) != "hi" {
			t.Errorf("read %d %q %v", op, data, err)
			return
		}
		if err := conn.Write(OpText, []byte(`{"type":"exit","exit_code":0}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()
	url := "ws" + srv.URL[len("http"):] + "/"
	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Write(OpBinary, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	op, data, err := client.Read()
	if err != nil || op != OpText || string(data) == "" {
		t.Fatalf("%d %q %v", op, data, err)
	}
}

func TestAcceptRejects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := Accept(rec, req); err == nil {
		t.Fatal("expected upgrade")
	}
	req.Header.Set("Connection", "keep-alive, Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if _, err := Accept(rec, req); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatal(err)
	}
	req.Header.Set("Sec-WebSocket-Version", "13")
	if _, err := Accept(rec, req); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatal(err)
	}
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if _, err := Accept(rec, req); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatal(err)
	}
	if _, err := Dial("://bad"); err == nil {
		t.Fatal("expected bad url")
	}
	if _, err := Dial("ws://127.0.0.1:1/"); err == nil {
		t.Fatal("expected refused dial")
	}
}

func TestPingLargeAndFragment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Accept(w, r)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.Close()
		op, data, err := conn.Read()
		if err != nil || op != OpBinary || len(data) != 200 {
			t.Errorf("medium %d %d %v", op, len(data), err)
			return
		}
		if err := conn.Write(OpBinary, data); err != nil {
			t.Errorf("echo: %v", err)
			return
		}
		op, data, err = conn.Read()
		if err != nil || op != OpBinary || len(data) != 70000 {
			t.Errorf("large %d %d %v", op, len(data), err)
		}
	}))
	defer srv.Close()
	client, err := Dial("ws" + strings.TrimPrefix(srv.URL, "http") + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Write(OpPing, []byte("xyz")); err != nil {
		t.Fatal(err)
	}
	medium := bytes.Repeat([]byte("m"), 200)
	if err := client.Write(OpBinary, medium); err != nil {
		t.Fatal(err)
	}
	op, data, err := client.Read()
	if err != nil || op != OpBinary || !bytes.Equal(data, medium) {
		t.Fatalf("echo %d %d %v", op, len(data), err)
	}
	large := bytes.Repeat([]byte("L"), 70000)
	if err := client.Write(OpBinary, large); err != nil {
		t.Fatal(err)
	}
	if err := client.Write(OpBinary, bytes.Repeat([]byte("x"), maxMessage+1)); err == nil {
		t.Fatal("expected oversized write")
	}

	left, right := net.Pipe()
	raw := &Conn{c: left, r: bufio.NewReader(left)}
	go func() {
		_, _ = right.Write([]byte{0x01, 0x00})
		_, _ = right.Write([]byte{0x82, 126, 0x00, 0x02, 'a', 'b'})
	}()
	if _, _, err := raw.Read(); err == nil || !strings.Contains(err.Error(), "fragmented") {
		t.Fatal(err)
	}
	op, data, err = raw.readFrame()
	if err != nil || op != OpBinary || string(data) != "ab" {
		t.Fatalf("len126 %d %q %v", op, data, err)
	}

	over := &Conn{c: left, r: bufio.NewReader(bytes.NewReader([]byte{0x82, 127, 0, 0, 0, 0, 0, 0x20, 0, 0}))}
	if _, _, err := over.readFrame(); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatal(err)
	}
	short := &Conn{c: left, r: bufio.NewReader(bytes.NewReader([]byte{0x81}))}
	if _, _, err := short.readFrame(); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}
