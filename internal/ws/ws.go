// Package ws is a small RFC6455 server and client used by terminal sessions.
package ws

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

const (
	// OpText is a text frame.
	OpText byte = 1
	// OpBinary is a binary frame.
	OpBinary byte = 2
	// OpClose is a close frame.
	OpClose byte = 8
	// OpPing is a ping frame.
	OpPing byte = 9
	// OpPong is a pong frame.
	OpPong byte = 10

	magic      = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxMessage = 1 << 20
)

// Conn is one WebSocket connection.
type Conn struct {
	c      net.Conn
	r      *bufio.Reader
	client bool
	mu     sync.Mutex
}

// Accept upgrades an HTTP request to a WebSocket.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("websocket upgrade required")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("missing websocket key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("websocket upgrade is not supported")
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + magic))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	if _, err := fmt.Fprintf(bufrw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		conn.Close()
		return nil, err
	}
	if err := bufrw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{c: conn, r: bufrw.Reader}, nil
}

// Dial opens a client WebSocket. Tests and same-process callers use it.
func Dial(urlStr string) (*Conn, error) {
	return DialHeader(urlStr, nil)
}

// DialHeader opens a client WebSocket and sends extra request headers, such
// as Authorization. ws and http URLs use TCP; wss and https use TLS.
func DialHeader(urlStr string, header http.Header) (*Conn, error) {
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	secure := req.URL.Scheme == "wss" || req.URL.Scheme == "https"
	addr := req.URL.Host
	if req.URL.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		addr = net.JoinHostPort(req.URL.Hostname(), port)
	}
	var conn net.Conn
	if secure {
		conn, err = tls.Dial("tcp", addr, &tls.Config{ServerName: req.URL.Hostname()})
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	var extra strings.Builder
	for name, values := range header {
		for _, v := range values {
			fmt.Fprintf(&extra, "%s: %s\r\n", name, v)
		}
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n%s\r\n", req.URL.RequestURI(), req.Host, encoded, extra.String())
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket upgrade status %s", resp.Status)
	}
	sum := sha1.Sum([]byte(encoded + magic))
	want := base64.StdEncoding.EncodeToString(sum[:])
	if resp.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, errors.New("bad websocket accept")
	}
	return &Conn{c: conn, r: br, client: true}, nil
}

// Read returns the next data frame. Ping frames are answered automatically.
func (c *Conn) Read() (byte, []byte, error) {
	for {
		op, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case OpPing:
			if err := c.Write(OpPong, data); err != nil {
				return 0, nil, err
			}
		case OpPong:
			continue
		case OpClose:
			return OpClose, data, io.EOF
		default:
			return op, data, nil
		}
	}
}

// Write sends one frame.
func (c *Conn) Write(op byte, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.write(op, data)
}

// Close closes the connection.
func (c *Conn) Close() error {
	_ = c.Write(OpClose, nil)
	return c.c.Close()
}

func (c *Conn) write(op byte, data []byte) error {
	if data == nil {
		data = []byte{}
	}
	if len(data) > maxMessage {
		return errors.New("websocket message is too large")
	}
	header := []byte{0x80 | op, 0}
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	n := len(data)
	switch {
	case n < 126:
		header[1] = maskBit | byte(n)
	case n < 65536:
		header[1] = maskBit | 126
		var extra [2]byte
		binary.BigEndian.PutUint16(extra[:], uint16(n))
		header = append(header, extra[:]...)
	default:
		header[1] = maskBit | 127
		var extra [8]byte
		binary.BigEndian.PutUint64(extra[:], uint64(n))
		header = append(header, extra[:]...)
	}
	payload := data
	if c.client {
		var mask [4]byte
		if _, err := rand.Read(mask[:]); err != nil {
			return err
		}
		header = append(header, mask[:]...)
		payload = make([]byte, len(data))
		for i := range data {
			payload[i] = data[i] ^ mask[i%4]
		}
	}
	if _, err := c.c.Write(header); err != nil {
		return err
	}
	_, err := c.c.Write(payload)
	return err
}

func (c *Conn) readFrame() (byte, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c.r, hdr); err != nil {
		return 0, nil, err
	}
	op := hdr[0] & 0x0f
	if hdr[0]&0x80 == 0 {
		return 0, nil, errors.New("fragmented websocket frames are not supported")
	}
	masked := hdr[1]&0x80 != 0
	size := uint64(hdr[1] & 0x7f)
	switch size {
	case 126:
		extra := make([]byte, 2)
		if _, err := io.ReadFull(c.r, extra); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(extra))
	case 127:
		extra := make([]byte, 8)
		if _, err := io.ReadFull(c.r, extra); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(extra)
	}
	if size > maxMessage {
		return 0, nil, errors.New("websocket message is too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(c.r, data); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range data {
			data[i] ^= mask[i%4]
		}
	}
	return op, data, nil
}

func headerHas(h http.Header, name, part string) bool {
	for _, value := range strings.Split(h.Get(name), ",") {
		if strings.EqualFold(strings.TrimSpace(value), part) {
			return true
		}
	}
	return false
}
