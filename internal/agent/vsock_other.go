//go:build !linux

package agent

import (
	"errors"
	"net"
)

func listenVsock(uint32) (net.Listener, error) {
	return nil, errors.New("vsock is only available on linux")
}
