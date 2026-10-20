//go:build !linux

package agent

import (
	"context"
	"errors"
	"net"
)

func listenVsock(uint32) (net.Listener, error) {
	return nil, errors.New("vsock is only available on linux")
}

func dialHostVsock(uint32) (net.Conn, error) {
	return nil, errors.New("vsock is only available on linux")
}

func dialHostUntil(context.Context, uint32, *Agent) {}
