# Backends

A backend is where a sandbox runs. All four share the same API: exec, files, terminals, watches, previews, network policy, pause, and TTL. They differ in how strong the isolation is and what the host needs.

| Backend | Isolation | Host needs |
|---|---|---|
| `docker` | Linux namespaces and cgroups (runc). | Docker. |
| `gvisor` | The Docker driver with `runsc`: a user-space kernel that intercepts syscalls. No KVM. | Docker with the `runsc` runtime. |
| `kata` | The Docker driver with the Kata runtime class: each container runs in a lightweight VM with its own kernel. | Kata, Docker, and a writable `/dev/kvm`. |
| `firecracker` | A Firecracker microVM per sandbox. The OCI image becomes an ext4 root disk, and `sbx-agent` is init. | Linux, `/dev/kvm`, the Firecracker binary and kernel, `sbx-agent`, and Docker to export images. |

There is no Kubernetes driver.

## Choosing a backend

The server default is `backend` in the [configuration](/docs/configuration/) (`docker` unless set). A template's `backend` overrides it, and a create request's `backend` overrides both:

```json
{ "template": "python", "backend": "gvisor" }
```

A backend that is not installed is an error (`400 unsupported`). sbx never falls back to plain runc silently.

As a rule of thumb: `docker` for trusted or semi-trusted code and local development, `gvisor` for untrusted code on hosts without KVM, and `kata` or `firecracker` when you want a separate kernel per sandbox.

## What this host can run

```bash
sbx doctor
```

`GET /v1/backends` returns the same picture to API clients, one row per backend:

```json
{
  "name": "gvisor",
  "available": true,
  "isolation": "gvisor",
  "pause": true,
  "snapshot": false,
  "exec": true,
  "pty": true,
  "files": true,
  "watch": true,
  "preview": true,
  "networks": ["none", "full"],
  "requirements": ["docker", "runsc"]
}
```

`detail` explains why a backend is not available. `pty`, `watch`, and `preview` are true only when `sbx-agent` is configured. `allow` mode is listed as `full`, since it is `full` with an allow list.

## gVisor

```bash
sbx setup gvisor            # print the steps
sbx setup gvisor --apply    # download pinned runsc and register it with Docker
```

`--apply` merges the runtime into Docker's `daemon.json` but does not restart Docker. Restart it yourself, then run `sbx doctor`. The runtime name defaults to `runsc` (`docker.gvisor_runtime`).

For `network: full` and `allow`, install runsc with `runsc install -- --network=host`. That makes gVisor use the Linux network stack inside the namespace Docker created, so sbx's bridge, DNS, and firewall rules apply. With gVisor's default netstack, DNS from the sandbox fails.

## Kata

Install Kata's containerd shim (`containerd-shim-kata-v2`), then:

```bash
sbx setup kata
sbx setup kata --apply --docker-config /etc/docker/daemon.json
```

`--apply` adds this to `daemon.json` and does not restart Docker:

```json
{
  "runtimes": {
    "kata": { "runtimeType": "io.containerd.kata.v2" }
  }
}
```

sbx requires a reachable Docker daemon, a writable `/dev/kvm`, and a Docker runtime whose `runtimeType` is `io.containerd.kata.v2` or whose path contains `kata`. Override the names with `kata.runtime` and `kata.runtime_type`. Images, network policy, and pause behave exactly as on Docker.

## Firecracker

```bash
sbx setup firecracker
sbx setup firecracker --apply --data-dir ./data
```

That downloads `firecracker`, `jailer`, and the `vmlinux` guest kernel into `data/firecracker/`. Then:

```yaml
backend: firecracker
firecracker:
  bin: data/firecracker/firecracker
  kernel: data/firecracker/vmlinux
  # jailer: data/firecracker/jailer
  uid: 123
  gid: 123
```

The host needs a writable `/dev/kvm`, `sbx-agent` next to `sbx` (or `SBX_DOCKER_AGENT_BIN`), Docker to export the OCI image the first time it is used, `mkfs.ext4` and permission to mount a loop device, and, for `full` and `allow` networking, root or enough capability to create a tap device and install iptables rules. The jailer is only used when `firecracker.jailer` is set, and needs a uid and gid of at least 1.

| | Firecracker sandbox |
|---|---|
| CPU | `ceil(cpu)` vCPUs, at most 32 |
| Memory | `memory_mb` MiB |
| Disk | An ext4 root filesystem of `disk_mb`, cached per image, agent, and size. Each sandbox gets its own copy. |
| Network `none` | No NIC. |
| Network `full` / `allow` | One tap with NAT and the same IPv4 allow, deny, and block rules as Docker. A positive `egress_kbps` is rejected. No IPv6. |
| Volumes | Not supported. |
| Pause | Firecracker pause and resume. |

Each guest gets a `/30` in `172.31.0.0/16`, which sits inside the blocked private range, so sandboxes cannot reach each other or the host's private addresses. Snapshot and restore are not part of this build; every create is a cold boot.

## Differences that matter

- **Disk.** On Docker, gVisor, and Kata, `disk_mb` sizes the `/workspace` tmpfs and the root filesystem is read-only. Firecracker sizes the whole ext4 root disk to `disk_mb`.
- **Bandwidth.** Docker, gVisor, and Kata enforce `egress_kbps`. Firecracker does not and rejects it.
- **PID limit.** Firecracker does not apply a guest PID rlimit; the vCPU and memory caps are the hard limit.
- **Volumes.** Docker volumes attach on Docker, gVisor, and Kata, not on Firecracker.
- **The agent.** Optional on Docker, gVisor, and Kata. Required on Firecracker, where it is the init process.
