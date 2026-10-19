//go:build linux

package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func configureGuestNet(ipCIDR, gateway string) error {
	if ipCIDR == "" {
		return nil
	}
	ip, network, err := net.ParseCIDR(ipCIDR)
	if err != nil {
		return fmt.Errorf("guest ip: %w", err)
	}
	iface, err := net.InterfaceByName("eth0")
	if err != nil {
		return fmt.Errorf("eth0: %w", err)
	}
	ones, _ := network.Mask.Size()
	if err := linkUp(iface.Name); err != nil {
		return err
	}
	msg, err := addrMessage(iface.Index, ip, ones)
	if err != nil {
		return err
	}
	if err := sendNetlink(msg); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	if gateway == "" {
		return nil
	}
	route, err := routeMessage(iface.Index, net.ParseIP(gateway))
	if err != nil {
		return err
	}
	if err := sendNetlink(route); err != nil {
		return fmt.Errorf("route: %w", err)
	}
	return nil
}

func linkUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

func sendNetlink(msg []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Bind(fd, sa); err != nil {
		return err
	}
	tv := unix.Timeval{Sec: 2}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	if err := unix.Sendto(fd, msg, 0, sa); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return err
	}
	if n < 16 {
		return fmt.Errorf("short netlink reply")
	}
	// NLMSG_ERROR is 2. The payload starts with a 4-byte errno.
	if binary.LittleEndian.Uint16(buf[4:]) == 2 && n >= 20 {
		errno := int32(binary.LittleEndian.Uint32(buf[16:]))
		if errno != 0 {
			return fmt.Errorf("netlink: %w", unix.Errno(-errno))
		}
	}
	return nil
}

func writeResolv(path string, servers []string) error {
	body := resolvBody(servers)
	if body == "" {
		return nil
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
