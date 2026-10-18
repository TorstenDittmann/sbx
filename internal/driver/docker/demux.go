package docker

import (
	"encoding/binary"
	"fmt"
	"io"
)

// CopyDemux reads a Docker multiplexed stream (8-byte header + payload).
// stream 1 is stdout and stream 2 is stderr. maxBytes limits the sum of
// delivered payload; further bytes are discarded and truncated is set.
func CopyDemux(r io.Reader, maxBytes int, fn func(stream byte, data []byte) error) (written int, truncated bool, err error) {
	hdr := make([]byte, 8)
	for {
		_, err = io.ReadFull(r, hdr)
		if err == io.EOF {
			return written, truncated, nil
		}
		if err != nil {
			return written, truncated, err
		}
		stream := hdr[0]
		n := binary.BigEndian.Uint32(hdr[4:])
		if n > 16<<20 {
			return written, truncated, fmt.Errorf("docker stream frame is %d bytes", n)
		}
		buf := make([]byte, n)
		if _, err = io.ReadFull(r, buf); err != nil {
			return written, truncated, err
		}
		if stream != 1 && stream != 2 {
			continue
		}
		if maxBytes > 0 && written >= maxBytes {
			truncated = true
			continue
		}
		if maxBytes > 0 && written+int(n) > maxBytes {
			keep := maxBytes - written
			if keep > 0 {
				if err = fn(stream, buf[:keep]); err != nil {
					return written, true, err
				}
				written += keep
			}
			truncated = true
			continue
		}
		if err = fn(stream, buf); err != nil {
			return written, truncated, err
		}
		written += int(n)
	}
}
