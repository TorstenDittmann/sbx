# sbx-agent

`sbx-agent` is a static binary that runs inside every sandbox. The server talks to it, so Docker and gVisor (and later backends that have no `docker exec`) use the same exec, file, terminal, and preview calls.

## Transport

One connection carries many streams. Each frame is a 9-byte header plus a payload of at most 1 MiB:

- 4 bytes, big-endian payload length
- 4 bytes, big-endian stream id
- 1 byte message type

Stream 0 is the handshake. The client opens the other streams. The first frame is `Hello` with the sandbox token. The agent answers `HelloOk` or closes the connection.

Docker reaches the agent by exec-ing `/opt/sbx/sbx-agent dial`, which splices its stdin and stdout onto the Unix socket the agent listens on (`/tmp/sbx-agent.sock` by default). The same handshake works on a plain TCP connection: `sbx-agent serve -listen ADDR`. vsock is not implemented.

The token is 32 random bytes, hex-encoded, stored in the container environment as `SBX_AGENT_TOKEN`. It is not part of the sandbox env returned by the API. Callers cannot set `SBX_AGENT_*` variables. Child processes do not inherit them.

## Injection

sbx copies the binary into the named volume `sbx-agent-bin` with a short-lived `alpine:3.20` helper, then bind-mounts that volume read-only at `/opt/sbx`. The sandbox entrypoint is `/opt/sbx/sbx-agent init`. Images do not need a shell for exec, files, terminals, or previews.

The server looks for the binary in `docker.agent_bin`, `SBX_AGENT_BIN`, `--agent-bin`, and finally next to the `sbx` executable. The container image sets `SBX_AGENT_BIN=/usr/local/bin/sbx-agent`. When no binary is configured, exec and files stay on the engine API and the new routes return `unsupported`.

## What the agent does

- Foreground exec with streaming stdout and stderr, stdin, a timeout, and an exit code. A timeout is exit 137.
- Background processes that survive a dropped dial. They can be listed, signaled, written to, and reattached. Output is replayed from a 256 KiB ring.
- Interactive PTY sessions: resize, and a WebSocket on `GET /v1/sandboxes/{id}/terminals/{termId}/ws`. Binary frames are bytes. Text frames are JSON (`resize` from the client, `exit` from the server).
- File list, read, write, stat, delete, and mkdir. Watches use Linux inotify. If inotify cannot be created, the agent polls every 200 ms so a watch still works on gVisor builds without it. Atomic writes through the agent hide the `.sbx-tmp` file and report `write` when the destination already existed, otherwise `create`.
- Preview: the server dials `127.0.0.1:port` inside the sandbox and reverse-proxies `/v1/sandboxes/{id}/previews/{previewId}/`. That works with `network: none`. Public path and wildcard-host routing are in [NETWORK.md](NETWORK.md).

Browsers cannot set `Authorization` on a WebSocket, so authenticated routes also accept `access_token` as a query parameter. Request logs record the path only.

## Deferred

- vsock, and the Firecracker, Kata, and Kubernetes dialers. The framed connection is the seam.
- Archives still invoke `tar` inside the image.
- Egress allow-lists are still rejected.
- A background process with `timeout_seconds: 0` is not killed by the agent. The sandbox TTL still stops it.
