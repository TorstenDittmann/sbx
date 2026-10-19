# Kata

Kata sandboxes are Docker containers started with the Kata runtime class. The image, exec stream, files, terminals, previews, network policy, and TTL are the same calls as `backend: docker`. The guest kernel is the one Kata is configured with, not the host kernel.

## What sbx checks

`sbx doctor` and `POST /v1/sandboxes` with `"backend": "kata"` both require:

- a reachable Docker daemon
- `/dev/kvm` writable by the server
- a Docker runtime whose `runtimeType` is `io.containerd.kata.v2`, or whose path contains `kata`

The runtime name defaults to `kata`. Override it with `kata.runtime` or `SBX_KATA_RUNTIME`. The expected type is `kata.runtime_type` / `SBX_KATA_RUNTIME_TYPE`.

A missing runtime is HTTP 400 `unsupported`. sbx does not fall back to runc.

## Register the runtime

Install Kata's containerd shim (`containerd-shim-kata-v2`), then:

```bash
sbx setup kata
sbx setup kata --apply --docker-config /etc/docker/daemon.json
```

`--apply` merges this into `daemon.json` and does not restart Docker:

```json
{
  "runtimes": {
    "kata": {
      "runtimeType": "io.containerd.kata.v2"
    }
  }
}
```

Restart the daemon yourself, then `sbx doctor`. Create a sandbox with `"backend": "kata"`, or set `SBX_BACKEND=kata`.

## What is not different

Pause uses the engine pause API. Snapshots are not a Kata feature in sbx. `network: full` and `network: allow` use the same bridge, allow and deny lists, and metadata filter as Docker.
