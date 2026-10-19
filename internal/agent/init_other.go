//go:build !linux

package agent

func preparePID1() error { return nil }

func reapZombies() {}
