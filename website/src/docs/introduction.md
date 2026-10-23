# Introduction

sbx is a sandbox server you run yourself. It is one Go binary with a SQLite file. Point it at Docker, create a sandbox over HTTP, and run commands, read and write files, open terminals, and publish ports inside it. When you need stronger isolation, the same API runs on gVisor, Kata Containers, or Firecracker microVMs.

It is built for running code you do not trust: output from an AI coding agent, a user's script, a build from a pull request. The defaults assume the code is hostile. A sandbox starts with no network, no Linux capabilities, a non-root user, a read-only root filesystem, and a lifetime the server enforces.

## How the pieces fit

| Piece | What it does |
|---|---|
| `sbx` | The server and CLI. It serves the HTTP API, stores state in `data/sbx.db`, talks to Docker (or Firecracker), and runs a reaper that stops expired sandboxes. |
| `sbx-agent` | A static binary the server copies into every sandbox. It runs commands, streams output, serves terminals and file watches, and proxies preview ports. Optional on Docker, required on Firecracker. |
| A backend | Where sandboxes actually run: `docker`, `gvisor`, `kata`, or `firecracker`. See [Backends](/docs/backends/). |
| A client | Anything that speaks HTTP. The [TypeScript SDK](/docs/sdk/) wraps the API for Node and Bun. |

A request flows like this: your client sends `POST /v1/sandboxes` with an API key. sbx checks the key's quota and network policy, picks a backend, starts a container or microVM from an image or [template](/docs/templates/), and returns the sandbox. Later calls such as `exec` go through `sbx-agent` inside the sandbox. When the TTL runs out, the reaper deletes it.

## What you get

- **Sandboxes** with CPU, memory, disk, and PID limits, a TTL, an optional idle timeout, labels, and secrets. See [Sandboxes](/docs/sandboxes/).
- **Exec** that waits for a result or streams it as server-sent events, plus background processes and interactive terminals.
- **Files**: list, read, write, stat, delete, mkdir, tar archives, and inotify watches.
- **Network modes**: `none` by default, `full` for public internet, or `allow` for a fixed allow list. Metadata and private ranges stay blocked. See [Network](/docs/network/).
- **Templates** with warm pools and cached build steps, so a sandbox can start with dependencies already installed. See [Templates and builds](/docs/templates/).
- **Volumes** that outlive a sandbox. See [Volumes](/docs/volumes/).
- **Previews**: a URL for a server running inside the sandbox, private or public. See [Previews](/docs/previews/).
- **API keys** with `admin` and `sandbox` scopes, quotas, expiry, and immediate revocation. See [API keys](/docs/api-keys/).
- **Operations**: `/healthz`, `/readyz`, Prometheus metrics at `/metrics`, and `sbx doctor` to check the host.

## What it does not do

sbx is a single-node server. It does not schedule across machines, save or restore a sandbox's memory, or ship a Kubernetes driver. A snapshot keeps the filesystem only. Pause and resume work on every backend.

## Next steps

- [Quickstart](/docs/quickstart/): start the server and run your first command.
- [Installation](/docs/installation/): install a release binary, the container image, or the SDK.
- [Configuration](/docs/configuration/): every setting and its default.
