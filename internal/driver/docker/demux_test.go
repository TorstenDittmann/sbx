package docker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func frame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

func TestCopyDemux(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(frame(1, "hello"))
	raw.Write(frame(2, "err"))
	raw.Write(frame(0, "ignore"))
	var out, errb bytes.Buffer
	n, trunc, err := CopyDemux(&raw, 0, func(stream byte, data []byte) error {
		if stream == 1 {
			out.Write(data)
		} else {
			errb.Write(data)
		}
		return nil
	})
	if err != nil || trunc || n != 8 || out.String() != "hello" || errb.String() != "err" {
		t.Fatalf("n=%d trunc=%v err=%v out=%q errb=%q", n, trunc, err, out.String(), errb.String())
	}
}

func TestCopyDemuxTruncates(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(frame(1, "abcdef"))
	raw.Write(frame(1, "ghij"))
	var got bytes.Buffer
	n, trunc, err := CopyDemux(&raw, 8, func(stream byte, data []byte) error {
		got.Write(data)
		return nil
	})
	if err != nil || !trunc || n != 8 || got.String() != "abcdefgh" {
		t.Fatalf("n=%d trunc=%v err=%v got=%q", n, trunc, err, got.String())
	}
}

func TestCopyDemuxCallbackError(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(frame(1, "ab"))
	sentinel := errors.New("stop")
	_, _, err := CopyDemux(&raw, 0, func(byte, []byte) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestCopyDemuxEmpty(t *testing.T) {
	n, trunc, err := CopyDemux(bytes.NewReader(nil), 0, func(byte, []byte) error { return nil })
	if err != nil || trunc || n != 0 {
		t.Fatal(err, trunc, n)
	}
	_, _, err = CopyDemux(bytes.NewReader([]byte{1, 0, 0}), 0, func(byte, []byte) error { return nil })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}
