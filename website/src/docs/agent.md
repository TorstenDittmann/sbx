# sbx-agent

`sbx-agent` is a small static binary that runs inside every sandbox. The server talks to it for exec, files, background processes, terminals, file watches, archives, and previews. Because the agent does the work, images do not need a shell, `tar`, or anything else installed.

## Do I need it?

| Feature | Without the agent | With the agent |
|---|---|---|
| Exec, files | Through a shell in the image (`sh`, `cat`, `find`, `stat`). | Native. |
| Archives | Needs `tar` in the image. | Packed and extracted by the agent. |
| Processes, terminals, watches, previews | `400 unsupported` | Available. |
| Firecracker | Cannot boot. | The agent is the guest's init. |

The release archives, the install script, and the container image all ship the agent. If you build from source, build it next to `sbx`:

```bash
go build -o sbx ./cmd/sbx
go build -o sbx-agent ./cmd/sbx-agent
```

The server looks for the binary in `docker.agent_bin`, then `SBX_DOCKER_AGENT_BIN`, then `--agent-bin`, then next to the `sbx` executable. The container image sets the older name `SBX_AGENT_BIN=/usr/local/bin/sbx-agent`, so either name set at run time overrides it.

## How it gets in

sbx copies the binary into a named Docker volume, `sbx-agent-bin`, and mounts that volume read-only at `/opt/sbx` in every sandbox. The sandbox entrypoint is `/opt/sbx/sbx-agent init`. On Firecracker the agent is written into the root disk and the kernel boots it as `init`.

Each sandbox gets its own random token for the agent handshake. It lives in the container environment as `SBX_AGENT_TOKEN`, is not part of the env the API returns, and is not inherited by child processes. Callers cannot set `SBX_AGENT_*` variables.

## Background processes

A process keeps running after the request that started it returns, and survives a dropped connection.

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/processes \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["npm","run","dev"],"cwd":"/workspace","timeout_seconds":0}'
```

| Call | Route |
|---|---|
| List | `GET /v1/sandboxes/{id}/processes` |
| Get | `GET /v1/sandboxes/{id}/processes/{procId}` |
| Follow output | `GET …/processes/{procId}/stream` (server-sent events) |
| Write to stdin | `POST …/processes/{procId}/stdin` with raw bytes |
| Signal | `POST …/processes/{procId}/signal` with `{"signal":"TERM"}` (`TERM`, `KILL`, `INT`, `HUP`, `QUIT`) |
| Kill | `DELETE …/processes/{procId}` |

The stream replays output buffered before you attached, from a 256 KiB ring per process, then follows live. `timeout_seconds: 0` means no timeout; the sandbox TTL still ends it.

## Terminals

An interactive PTY, attached over a WebSocket:

```bash
curl -s $SBX_URL/v1/sandboxes/$ID/terminals \
  -H "Authorization: Bearer $SBX_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"cmd":["sh"],"cols":120,"rows":32}'
```

Then connect to `GET /v1/sandboxes/{id}/terminals/{termId}/ws?access_token=sbx_…`. Binary frames carry stdin and output bytes. Text frames are JSON: the client sends `{"type":"resize","cols":80,"rows":24}`, and the server sends `{"type":"exit","exit_code":0}` when the process ends. `POST …/terminals/{termId}/resize` also resizes, and `DELETE …/terminals/{termId}` kills it.

In the SDK:

```ts
const term = await box.startTerminal(["sh"], { cols: 120, rows: 32 })
const session = box.connectTerminal(term.id)
session.onOutput((bytes) => process.stdout.write(bytes))
session.onExit((code) => console.log("exit", code))
session.send("ls -la\n")
```

## File watches

```bash
curl -N "$SBX_URL/v1/sandboxes/$ID/files/watch?path=/workspace&recursive=1" \
  -H "Authorization: Bearer $SBX_API_KEY"
```

A server-sent event stream: `ready`, then `change` events with `type` (`create`, `write`, or `remove`) and `path`. The agent uses inotify. Where inotify is not available, as on some gVisor builds, it polls every 200 ms instead. Atomic writes through the API hide their temporary file and report `write` when the destination already existed, `create` otherwise.

```ts
for await (const event of box.files.watch("/workspace", { recursive: true })) {
  console.log(event.type, event.path)
}
```

## Archives

`POST /v1/sandboxes/{id}/files/archive?path=/workspace` extracts a tar or gzip tar, and `GET` on the same route downloads a directory as gzip tar. The agent rejects any entry whose path leaves the destination or passes through a symlink.

## Protocol

For people writing their own client or backend: one connection carries many streams. Each frame is a 9-byte header (4-byte big-endian payload length, 4-byte big-endian stream id, 1-byte message type) and a payload of at most 1 MiB. Stream 0 is the handshake: the first frame is `Hello` with the sandbox token, and the agent answers `HelloOk` or closes the connection.

Docker and Kata reach the agent by exec-ing `/opt/sbx/sbx-agent dial`, which splices stdin and stdout onto the agent's Unix socket (`/tmp/sbx-agent.sock`). Firecracker uses vsock port 52. `sbx-agent serve -listen ADDR` serves the same protocol on TCP.
