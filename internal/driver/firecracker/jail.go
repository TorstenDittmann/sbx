package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
)

// jailRoot is the chroot Firecracker's jailer builds for id.
func jailRoot(base, id string) string {
	return filepath.Join(base, "firecracker", id, "root")
}

// stageJail copies the kernel and rootfs into the jail chroot.
// API paths inside the jail are /kernel and /rootfs.ext4.
func stageJail(root, kernel, disk string, uid, gid int) error {
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		return err
	}
	if err := copyFile(kernel, filepath.Join(root, "kernel")); err != nil {
		return fmt.Errorf("jail kernel: %w", err)
	}
	if err := copyFile(disk, filepath.Join(root, "rootfs.ext4")); err != nil {
		return fmt.Errorf("jail rootfs: %w", err)
	}
	for _, name := range []string{"kernel", "rootfs.ext4"} {
		if err := os.Chown(filepath.Join(root, name), uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", name, err)
		}
	}
	return nil
}
