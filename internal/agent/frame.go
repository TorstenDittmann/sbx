// Package agent is the control protocol between sbx and the sbx-agent process
// that runs inside a sandbox.
//
// Frames are a 9-byte header plus a payload, multiplexed by stream id on one
// bidirectional byte stream. That stream is stdin/stdout of `sbx-agent dial`
// today (reached through docker exec) and can be a TCP or vsock connection
// later without a protocol change. Stream 0 is the handshake. The client
// allocates every other stream id.
package agent

import (
	"encoding/binary"
	"errors"
	"io"
)

// ProtocolVersion is the handshake version the agent reports.
const ProtocolVersion = 1

// MaxPayload is the largest single frame body.
const MaxPayload = 1 << 20

// ChunkSize is the payload size used for streamed bytes.
const ChunkSize = 32 << 10

const headerSize = 9

// Message types. Control frames use the same numbers on every stream.
const (
	MsgHello uint8 = 1
	MsgHelloOk uint8 = 2
	MsgError uint8 = 3
	MsgOK uint8 = 4
	MsgData uint8 = 5
	MsgEnd uint8 = 6

	MsgExec uint8 = 10
	MsgStdin uint8 = 11
	MsgStdout uint8 = 12
	MsgStderr uint8 = 13
	MsgExit uint8 = 14
	MsgKill uint8 = 15
	MsgResize uint8 = 16
	MsgStdinEOF uint8 = 17

	MsgProcList uint8 = 20
	MsgProcSignal uint8 = 21
	MsgProcAttach uint8 = 22
	MsgProcStdin uint8 = 23

	MsgFileList uint8 = 30
	MsgFileStat uint8 = 31
	MsgFileRead uint8 = 32
	MsgFileWrite uint8 = 33
	MsgFileRemove uint8 = 34
	MsgFileMkdir uint8 = 35
	MsgFileWatch uint8 = 36
	MsgFileEvent uint8 = 37

	MsgDial uint8 = 40
)

var (
	// ErrFrameTooLarge means a peer advertised a payload over MaxPayload.
	ErrFrameTooLarge = errors.New("frame exceeds the size limit")
	// ErrUnauthorized means the handshake token did not match.
	ErrUnauthorized = errors.New("unauthorized")
	// ErrClosed means the multiplexed connection is gone.
	ErrClosed = errors.New("connection closed")
)

// Frame is one protocol message.
type Frame struct {
	Stream uint32
	Type   uint8
	Data   []byte
}

// ReadFrame reads one frame. io.EOF and io.ErrUnexpectedEOF are returned as-is.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(hdr[0:4])
	if n > MaxPayload {
		return Frame{}, ErrFrameTooLarge
	}
	f := Frame{Stream: binary.BigEndian.Uint32(hdr[4:8]), Type: hdr[8]}
	if n == 0 {
		return f, nil
	}
	f.Data = make([]byte, n)
	if _, err := io.ReadFull(r, f.Data); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// WriteFrame writes one frame. It handles short writes.
func WriteFrame(w io.Writer, stream uint32, typ uint8, data []byte) error {
	if len(data) > MaxPayload {
		return ErrFrameTooLarge
	}
	var hdr [headerSize]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(data)))
	binary.BigEndian.PutUint32(hdr[4:8], stream)
	hdr[8] = typ
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, data)
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		b = b[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
