package firecracker

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
)

// DialVsock connects to a Firecracker vsock Unix socket and then to guest port.
// The handshake is "CONNECT <port>\n" followed by "OK <port>\n".
func DialVsock(sock string, port uint32) (net.Conn, error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.HasPrefix(line, "OK ") {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake %q", strings.TrimSpace(line))
	}
	return &prefixConn{Conn: conn, r: r}, nil
}

// prefixConn returns bytes bufio already pulled past the handshake line.
type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if c.r != nil && c.r.Buffered() > 0 {
		return c.r.Read(p)
	}
	return c.Conn.Read(p)
}

// ReadFrom keeps io.Copy from ignoring the buffered handshake tail.
func (c *prefixConn) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{c.Conn}, r)
}
