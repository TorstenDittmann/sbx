package sbx

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

const (
	opText   byte = 1
	opBinary byte = 2
	opClose  byte = 8
	opPing   byte = 9
	opPong   byte = 10

	wsMagic      = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsMaxMessage = 1 << 20
)

// TerminalSession is a WebSocket attached to a PTY. It is an
// io.ReadWriteCloser: Write sends keystrokes, and Read returns output until
// the process exits, then io.EOF. ExitCode reports how it exited.
//
// Read is not safe to call from several goroutines at once; Write, Resize,
// and Close are.
type TerminalSession struct {
	ID string

	conn    net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	pending []byte

	mu       sync.Mutex
	exitCode int
	exited   bool
	closed   bool
}

// ConnectTerminal attaches to a PTY started with StartTerminal. ctx bounds
// the dial and handshake only; close the session to end it. The API key is
// sent in the Authorization header. HTTP proxies are not used.
func (c *Client) ConnectTerminal(ctx context.Context, id, termID string) (*TerminalSession, error) {
	u, err := url.Parse(c.baseURL + sandboxPath(id, "terminals", termID, "ws"))
	if err != nil {
		return nil, err
	}
	var tlsConfig *tls.Config
	if t, ok := c.http.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
		tlsConfig = t.TLSClientConfig.Clone()
	}
	conn, br, err := dialWebSocket(ctx, u, http.Header{"Authorization": {"Bearer " + c.apiKey}}, tlsConfig)
	if err != nil {
		return nil, err
	}
	return &TerminalSession{ID: termID, conn: conn, r: br}, nil
}

// Write sends input to the PTY.
func (t *TerminalSession) Write(p []byte) (int, error) {
	for off := 0; off < len(p); off += wsMaxMessage {
		end := min(off+wsMaxMessage, len(p))
		if err := t.write(opBinary, p[off:end]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

// Resize changes the PTY size.
func (t *TerminalSession) Resize(cols, rows int) error {
	msg, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	return t.write(opText, msg)
}

// Read reads PTY output. It returns io.EOF once the process has exited or
// the server closed the connection.
func (t *TerminalSession) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		if t.Exited() {
			return 0, io.EOF
		}
		op, data, err := t.readMessage()
		if err != nil {
			return 0, err
		}
		switch op {
		case opBinary:
			t.pending = data
		case opText:
			var msg struct {
				Type     string `json:"type"`
				ExitCode int    `json:"exit_code"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "exit" {
				t.mu.Lock()
				t.exited, t.exitCode = true, msg.ExitCode
				t.mu.Unlock()
			}
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

// ExitCode returns the process's exit code once Read has seen it exit.
func (t *TerminalSession) ExitCode() (code int, exited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.exited
}

// Exited reports whether the process has exited.
func (t *TerminalSession) Exited() bool {
	_, exited := t.ExitCode()
	return exited
}

// Close closes the connection. The PTY keeps running until it exits or is
// killed with KillTerminal.
func (t *TerminalSession) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.write(opClose, nil)
	return t.conn.Close()
}

func (t *TerminalSession) readMessage() (byte, []byte, error) {
	for {
		op, data, err := readFrame(t.r)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0, nil, io.EOF
			}
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := t.write(opPong, data); err != nil {
				return 0, nil, err
			}
		case opPong:
		case opClose:
			return 0, nil, io.EOF
		default:
			return op, data, nil
		}
	}
}

func (t *TerminalSession) write(op byte, data []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	return writeFrame(t.conn, op, data)
}

func dialWebSocket(ctx context.Context, u *url.URL, header http.Header, tlsConfig *tls.Config) (net.Conn, *bufio.Reader, error) {
	secure := false
	switch u.Scheme {
	case "https", "wss":
		secure = true
	case "http", "ws":
	default:
		return nil, nil, fmt.Errorf("sbx: unsupported URL scheme %q", u.Scheme)
	}
	addr := u.Host
	if u.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		addr = net.JoinHostPort(u.Hostname(), port)
	}
	var conn net.Conn
	var err error
	if secure {
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = u.Hostname()
		}
		conn, err = (&tls.Dialer{Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, nil, err
	}
	// Cancelling ctx during the handshake closes the connection.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		conn.Close()
		return nil, nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	var req strings.Builder
	fmt.Fprintf(&req, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", u.RequestURI(), u.Host, encoded)
	for name, values := range header {
		for _, v := range values {
			fmt.Fprintf(&req, "%s: %s\r\n", name, v)
		}
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		return nil, nil, errorFrom(resp)
	}
	sum := sha1.Sum([]byte(encoded + wsMagic))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, nil, errors.New("sbx: bad websocket accept header")
	}
	if !stop() {
		conn.Close()
		return nil, nil, ctx.Err()
	}
	return conn, br, nil
}

// writeFrame writes one masked client frame.
func writeFrame(w io.Writer, op byte, data []byte) error {
	if len(data) > wsMaxMessage {
		return errors.New("sbx: websocket message is too large")
	}
	header := []byte{0x80 | op, 0}
	n := len(data)
	switch {
	case n < 126:
		header[1] = 0x80 | byte(n)
	case n < 65536:
		header[1] = 0x80 | 126
		header = binary.BigEndian.AppendUint16(header, uint16(n))
	default:
		header[1] = 0x80 | 127
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	frame := make([]byte, 0, len(header)+4+n)
	frame = append(frame, header...)
	frame = append(frame, mask[:]...)
	for i, b := range data {
		frame = append(frame, b^mask[i%4])
	}
	_, err := w.Write(frame)
	return err
}

// readFrame reads one frame. Fragmented messages are not supported; the
// server never sends them.
func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0]&0x80 == 0 {
		return 0, nil, errors.New("sbx: fragmented websocket frames are not supported")
	}
	op := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	size := uint64(hdr[1] & 0x7f)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	if size > wsMaxMessage {
		return 0, nil, errors.New("sbx: websocket message is too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return op, data, nil
}
