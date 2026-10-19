package agent

import (
	"bytes"
	"testing"
)

func FuzzReadFrame(f *testing.F) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, 1, MsgOK, []byte("ok")); err != nil {
		f.Fatal(err)
	}
	f.Add(buf.Bytes())
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1, MsgData})
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := ReadFrame(bytes.NewReader(data))
		if err != nil {
			return
		}
		if len(frame.Data) > MaxPayload {
			t.Fatalf("payload %d", len(frame.Data))
		}
	})
}
