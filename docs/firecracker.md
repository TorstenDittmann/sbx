# Firecracker

`sbx` boots a Firecracker microVM per sandbox. The guest kernel is the Firecracker CI `vmlinux-6.1.155` image, the root disk is an ext4 filesystem built from the OCI image, and `sbx-agent` is init. The host talks to that agent through Firecracker's vsock socket, so exec, files, terminals, watches, and preview ports are the same calls as Docker.

Snapshot and restore are not in this build. A cold boot is what `POST /v1/sandboxes` does. `TestIntegrationBoot` (`SBX_FC_INTEGRATION=1`) logs `startup_ms` from process start until the agent answers. This tree does not publish a number from a host where `/dev/kvm` was not writable.

## Requirements

- Linux with a writable `/dev/kvm`
- the `firecracker` binary and `vmlinux` from `sbx setup firecracker --apply` (or `firecracker.bin` and `firecracker.kernel`)
- `sbx-agent` next to `sbx`, or `SBX_AGENT_BIN`
- Docker, so the server can export the OCI image the first time it is used
- `mkfs.ext4` and permission to mount a loop device while the rootfs is built
- for `network: full` and `network: allow`: root, or enough capability to create a tap and install iptables

`sbx doctor` and `GET /v1/backends` report whether those pieces are present. A missing one is an error on create, not a fallback to Docker.

## Setup

```bash
sbx setup firecracker
sbx setup firecracker --apply --data-dir ./data
```

That writes `data/firecracker/firecracker`, `jailer`, `vmlinux`, and `manifest.json`. The jailer is downloaded but not used unless `firecracker.jailer` (or `SBX_FIRECRACKER_JAILER`) points at it. The jailer needs a uid and gid of at least 1; the default is 123.

```yaml
backend: firecracker
firecracker:
  bin: data/firecracker/firecracker
  kernel: data/firecracker/vmlinux
  # jailer: data/firecracker/jailer
  uid: 123
  gid: 123
```

## What a sandbox gets

| | |
|---|---|
| CPU | `ceil(cpu)` vCPUs, at most 32 |
| Memory | `memory_mb` MiB |
| Disk | an ext4 rootfs of `disk_mb` mebibytes, cached per image, agent, and size |
| Processes | the agent tracks them. The vCPU and memory caps are the hard limit; a guest PID rlimit is not applied |
| Network `none` | no NIC |
| Network `full` and `allow` | one tap, NAT, and the same IPv4 allow, deny, and block rules as Docker. A positive `egress_kbps` is rejected. The guest NIC is IPv4; IPv6 egress is not configured. An allow list also opens `1.1.1.1`, `8.8.8.8`, and `9.9.9.9` unless `dns` is set |
| Pause | Firecracker `Paused` / `Resumed` |
| Reconcile | VM records live under `data/firecracker/vms/<id>/vm.json`. On startup, a sandbox whose process is gone is marked stopped |

The guest address is a `/30` in `172.31.0.0/16`. Those addresses are inside the blocked private range, so a sandbox cannot route to another sandbox or to the host's other private addresses. The tap gateway is on-link, not a forwarded destination.

`ext4:/path/to/root.ext4` skips the OCI export and uses that file as the base image. sbx still copies it and writes `/opt/sbx/guest.json` before boot.

## Guest

The kernel command line is `init=/opt/sbx/sbx-agent`. With no arguments, and with `/opt/sbx/guest.json` on disk, the agent mounts `devtmpfs`, `proc`, and `sysfs` when it is PID 1, configures `eth0` from the file, writes `/etc/resolv.conf`, and listens on vsock port 52. The token in that file is the same handshake secret Docker puts in `SBX_AGENT_TOKEN`.

Boot args:

```text
console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/opt/sbx/sbx-agent
```

The rootfs cache is `data/firecracker/cache/<sha256>.ext4`. Each sandbox gets its own copy so the cache stays read-only.

## CI

`.github/workflows/ci.yml` runs `TestIntegrationBoot` when `/dev/kvm` is writable. The test downloads the pinned binary and kernel, builds an alpine rootfs, and logs `startup_ms`. Without KVM the job skips. Unit tests use a fake Firecracker API socket and a real `sbx-agent` session over the vsock handshake, so they do not need KVM.
