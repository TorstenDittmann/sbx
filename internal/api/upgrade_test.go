package api

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
)

type hijackConn struct {
	net.Conn
	header http.Header
}

func (h *hijackConn) Header() http.Header {
	if h.header == nil {
		h.header = make(http.Header)
	}
	return h.header
}

func (h *hijackConn) Write(p []byte) (int, error) { return len(p), nil }

func (h *hijackConn) WriteHeader(int) {}

func (h *hijackConn) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.Conn, bufio.NewReadWriter(bufio.NewReader(h.Conn), bufio.NewWriter(h.Conn)), nil
}

func TestSpliceUpgrade(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn := <-accepted
	defer serverConn.Close()

	proxyConn, sandbox := net.Pipe()
	defer sandbox.Close()
	resp := &http.Response{
		Status:        "101 Switching Protocols",
		StatusCode:    http.StatusSwitchingProtocols,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bufio.NewReader(proxyConn)),
		ContentLength: -1,
	}
	resp.Header.Set("Upgrade", "websocket")
	resp.Header.Set("Connection", "Upgrade")

	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Server{}).spliceUpgrade(&hijackConn{Conn: serverConn}, proxyConn, resp)
	}()

	reader := bufio.NewReader(client)
	got, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %s", got.Status)
	}
	echo := make(chan error, 1)
	go func() {
		buf := make([]byte, 4)
		_, err := io.ReadFull(sandbox, buf)
		if err != nil {
			echo <- err
			return
		}
		_, err = sandbox.Write(buf)
		echo <- err
	}()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := <-echo; err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo %q %v", buf, err)
	}
	client.Close()
	sandbox.Close()
	select {
	case <-done:
	default:
		<-done
	}
}

func TestSpliceUpgradeWithoutHijack(t *testing.T) {
	rec := newRecorder()
	proxy, sandbox := net.Pipe()
	defer proxy.Close()
	defer sandbox.Close()
	resp := &http.Response{Status: "101 Switching Protocols", StatusCode: 101, Header: make(http.Header), Body: io.NopCloser(sandbox)}
	(&Server{}).spliceUpgrade(rec, proxy, resp)
	if rec.code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.code)
	}
}

type memResponse struct {
	header http.Header
	code   int
	body   []byte
}

func newRecorder() *memResponse { return &memResponse{header: make(http.Header), code: 200} }

func (m *memResponse) Header() http.Header { return m.header }

func (m *memResponse) Write(p []byte) (int, error) {
	m.body = append(m.body, p...)
	return len(p), nil
}

func (m *memResponse) WriteHeader(code int) { m.code = code }
