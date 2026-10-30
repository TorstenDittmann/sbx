package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
)

// hostileAgent answers the handshake, then ignores every limit the client
// asks for: it streams far more output and file data than requested.
func hostileAgent(t *testing.T, conn net.Conn) {
	t.Helper()
	go func() {
		if _, err := ReadFrame(conn); err != nil {
			return
		}
		if err := WriteFrame(conn, 0, MsgHelloOk, nil); err != nil {
			return
		}
		chunk := bytes.Repeat([]byte("x"), ChunkSize)
		for {
			f, err := ReadFrame(conn)
			if err != nil {
				return
			}
			switch f.Type {
			case MsgExec:
				for i := 0; i < 64; i++ {
					_ = WriteFrame(conn, f.Stream, MsgStdout, chunk)
				}
				res, _ := json.Marshal(ExitResult{})
				_ = WriteFrame(conn, f.Stream, MsgExit, res)
			case MsgFileRead:
				for i := 0; i < 64; i++ {
					_ = WriteFrame(conn, f.Stream, MsgData, chunk)
				}
				_ = WriteFrame(conn, f.Stream, MsgOK, nil)
			}
		}
	}()
}

func TestClientCapsUntrustedAgent(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	hostileAgent(t, server)
	c, err := Handshake(context.Background(), client, "tok")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const limit = 100 << 10
	got := 0
	out, err := c.Exec(ctx, ExecSpec{Cmd: []string{"yes"}, MaxOutput: limit}, nil, func(_ uint8, data []byte) error {
		got += len(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != limit || !out.Truncated {
		t.Fatalf("exec passed %d bytes (truncated=%v), want %d", got, out.Truncated, limit)
	}
	if _, err := c.ReadFile(ctx, "/big", limit); err == nil {
		t.Fatal("ReadFile accepted more than max from the agent")
	}
}
