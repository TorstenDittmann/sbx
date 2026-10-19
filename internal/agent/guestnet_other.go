//go:build !linux

package agent

import "errors"

func configureGuestNet(ipCIDR, _ string) error {
	if ipCIDR == "" {
		return nil
	}
	return errors.New("guest network setup is only available on linux")
}

func writeResolv(path string, servers []string) error {
	body := resolvBody(servers)
	if body == "" {
		return nil
	}
	return errors.New("resolv.conf is written on linux (" + path + ")")
}
