# Python SDK

`sbx-sdk` is the Python client for sbx. It mirrors the [TypeScript SDK](/docs/sdk/) with Python names, has no runtime dependencies, ships type hints, and runs on Python 3.9+. Calls are synchronous.

```bash
pip install sbx-sdk
pip install "sbx-sdk[terminal]"   # adds websockets, for terminals
```

The import name is `sbx`. The unrelated `sbx` package on PyPI uses the same import name, so do not install both in one environment.

## Connecting

The client reads two environment variables:

| Variable | Default |
|---|---|
| `SBX_URL` | `http://127.0.0.1:8080` |
| `SBX_API_KEY` | Required. |

Or pass them explicitly. Every entry point takes `base_url` and `api_key`, or a ready `client`:

```python
from sbx import Sandbox, SbxClient

client = SbxClient("https://api.example.com", os.environ["SBX_KEY"], timeout=60)
box = Sandbox.create(template="base", client=client)
box = Sandbox.create(template="base", base_url="https://api.example.com", api_key="sbx_...")
```

`timeout` is the socket timeout in seconds for each request. The default waits forever, which suits long execs and streams. `opener` takes a `urllib.request.OpenerDirector`, for proxies or custom TLS.

There are two layers. `Sandbox` is the usual entry point: one object per sandbox, with methods that act on it. `SbxClient` is the low-level HTTP client that covers every route, including keys, templates, volumes, and backends.

## Sandbox

### Create and connect

```python
from sbx import NetworkPolicy, Sandbox

box = Sandbox.create(
    template="python",
    ttl="10m",
    idle_timeout="2m",
    cpu=1,
    memory_mb=1024,
    env={"LOG_LEVEL": "debug"},
    secrets={"OPENAI_API_KEY": os.environ["OPENAI_API_KEY"]},
    labels={"project": "demo"},
    network=NetworkPolicy(mode="allow", allow=["pypi.org", "files.pythonhosted.org"]),
    backend="gvisor",
)

# Later, from somewhere else:
same = Sandbox.connect(box.id)
```

A `Sandbox` is also a context manager that kills the sandbox when the block ends:

```python
with Sandbox.create(template="base", ttl="5m") as box:
    print(box.exec("uname -a").stdout)
```

| Argument | Type | Maps to |
|---|---|---|
| `template` (first positional) | `str \| Template` | `template`, or a [build](#templates-and-builds) |
| `image` | `str` | `image` |
| `steps` | `list[dict]` | `steps` |
| `cpu` | `float` | `cpu` |
| `memory_mb` | `int` | `memory_mb` |
| `disk_mb` | `int` | `disk_mb` |
| `pids` | `int` | `pids` |
| `ttl` | duration | `ttl_seconds` |
| `idle_timeout` | duration | `idle_timeout_seconds` |
| `env` | `dict[str, str]` | `env` |
| `secrets` | `dict[str, str]` | `secrets` |
| `labels` | `dict[str, str]` | `labels` |
| `network` | `NetworkPolicy \| dict` | `network` (`mode`, `allow`, `deny`, `egress_kbps`, `max_connections`) |
| `backend` | `"docker" \| "gvisor" \| "kata" \| "firecracker"` | `backend` |
| `mounts` | `list[VolumeMount \| dict]` | `mounts` (`volume_id`, `path`) |

Durations accept seconds as a number, a `timedelta`, or a string with a unit: `"500ms"`, `"30s"`, `"5m"`, `"1h"`. They are rounded to whole seconds; anything above 0 and below a second becomes 1. `parse_duration` is exported if you need it.

A `Sandbox` has the fields of the API object: `id`, `status`, `backend`, `template`, `image`, `cpu`, `memory_mb`, `disk_mb`, `pids`, `network`, `egress_allow`, `egress_deny`, `egress_kbps`, `max_connections`, `env`, `labels`, `secret_keys`, `idle_timeout_seconds`, `expires_at`, `created_at`, `node`, and `mounts`. Lifecycle calls update them.

### Lifecycle

```python
box.set_ttl("30m")   # new remaining lifetime
box.pause()
box.resume()
box.refresh()        # re-read every field
box.set_secrets({"TOKEN": "new"})
box.kill()           # delete
```

### Exec

```python
result = box.exec(["python", "-c", "print(2 + 2)"])
result.exit_code    # 0
result.stdout       # "4\n"
result.stderr
result.duration_ms
result.truncated

# A string runs through `sh -lc`.
box.exec("ls -la | wc -l", cwd="/workspace", env={"A": "1"}, timeout="30s")

# stdin as text or bytes
box.exec(["wc", "-c"], stdin=b"\x01\x02\x03")
```

Stream output while it runs:

```python
for event in box.exec_stream("pip install requests"):
    if event.type in ("stdout", "stderr"):
        print(event.data, end="")
    elif event.type == "exit":
        print("exit", event.exit_code)
    elif event.type == "error":
        print(event.code, event.message)
```

### Files

`box.files` wraps the file routes. Paths are absolute.

```python
box.files.write("/workspace/main.py", "print('hi')\n")
box.files.write("/workspace/run.sh", "#!/bin/sh\necho ok\n", 0o755)
text = box.files.read_text("/workspace/main.py")
data = box.files.read("/workspace/data.bin")         # bytes
listing = box.files.list("/workspace")               # FileList(path, entries)
info = box.files.stat("/workspace/main.py")
box.files.mkdir("/workspace/out")
box.files.remove("/workspace/out")

for change in box.files.watch("/workspace", recursive=True):
    print(change.type, change.path)  # create, write, remove
    break                            # stop iterating to close the stream
```

Archives accept bytes, a binary file object, or an iterable of chunks. A file or iterable is streamed instead of read into memory:

```python
with open("src.tar.gz", "rb") as f:
    box.files.upload_archive("/workspace", f)

tgz = box.files.download_archive("/workspace")       # bytes
with open("out.tar.gz", "wb") as f:
    box.files.download_archive("/workspace", f)      # streamed to the file
```

`stat`, `watch`, processes, terminals, and previews need [sbx-agent](/docs/agent/) on the server.

### Processes

```python
proc = box.start_process(["npm", "run", "dev"], timeout=0)
box.write_process_stdin(proc.id, "rs\n")
for event in box.stream_process(proc.id):
    if event.type == "stdout":
        print(event.data, end="")
    if event.type == "exit":
        break
box.signal_process(proc.id, "TERM")
everything = box.list_processes()
```

### Terminals

```python
term = box.start_terminal(["bash"], cols=120, rows=32)

with box.connect_terminal(term.id) as session:
    session.send("echo hello; exit\n")
    session.resize(100, 30)
    for chunk in session:            # bytes, until the shell exits
        sys.stdout.buffer.write(chunk)
    print("exit", session.exit_code)
```

`session.recv(timeout)` reads one chunk, and returns `None` once the process exits or the socket closes. `on_output` and `on_exit` register callbacks that run while you read. `connect_terminal` needs the `terminal` extra, which installs `websockets`. It passes the key as `access_token` in the URL.

### Previews

```python
preview = box.create_preview(3000, public=False)
preview.url          # authenticated /v1 proxy
preview.public_url   # shareable URL, with the token for a private preview
preview.token        # shown once

res = box.preview_fetch(preview.id, "/api/health")
res.status, res.text(), res.json()
box.list_previews()
box.delete_preview(preview.id)
```

`preview_fetch` returns any status instead of raising, like `fetch`. It takes `method`, `body`, and `headers`.

## Templates and builds

`Sandbox.template()` is an immutable builder for [build steps](/docs/templates/#build-steps). Each method returns a new template:

```python
node = (
    Sandbox.template("node:22-bookworm-slim")
    .with_packages("ffmpeg", "git")
    .with_env(NODE_ENV="production")   # build time only
    .workdir("/app")
    .run("npm init -y")
)

with_playwright = node.run("npx playwright install --with-deps chromium")

info = node.build()                       # optional, warms the cache
box = Sandbox.create(node, ttl="5m")
```

| Method | Step |
|---|---|
| `run(command)` | `{"run": ...}`, a `/bin/sh -c` script as root |
| `with_packages(*names)` | `{"packages": [...]}` |
| `with_env(vars, **more)` | `{"env": {...}}`, for later steps only |
| `workdir(dir)` | `{"workdir": ...}`, for later steps only |
| `build(backend=None)` | Calls `POST /v1/builds`. Returns `BuildInfo` (`id`, `image`, `base`, `steps`, `cached`, `duration_ms`). |

`Sandbox.template()` with no argument starts from the `base` template. `Sandbox.template(template="python")` starts from another template, including its own steps. Builds need an admin key unless the server sets `builds.allow_sandbox_keys`.

Named templates, with warm pools:

```python
client = SbxClient()
client.create_template(
    "web",
    "node:22-bookworm-slim",
    steps=[{"packages": ["ffmpeg"]}],
    setup=["sh", "-c", "echo ready"],
    setup_timeout="2m",
    warm=2,
    network="full",
)
box = Sandbox.create("web")

client.list_templates()
client.delete_template("web")
client.build([{"run": "pip install numpy"}], image="python:3.12-slim")
```

## SbxClient

Every `Sandbox` method has a client equivalent that takes the sandbox id first, such as `client.exec(id, cmd)`. The client also has:

### Sandboxes

```python
client.create_sandbox("python", ttl="5m")   # returns SandboxInfo, not a Sandbox
client.get_sandbox(id)
client.list_sandboxes(status="running", template="python", q="demo", labels={"project": "demo"})
client.delete_sandbox(id)
client.set_secrets(id, {"TOKEN": "new"})
```

### Volumes

```python
volume = client.create_volume("cache", "docker")
volume = client.ensure_volume("cache")    # returns the existing one on retry
client.list_volumes()
client.get_volume(volume.id)
client.delete_volume(volume.id)
```

### Keys

Admin key required.

```python
from datetime import datetime, timedelta, timezone
from sbx import KeyNetworkPolicy

created = client.create_key(
    "agent",
    scopes=["sandbox"],
    expires_at=datetime.now(timezone.utc) + timedelta(days=30),
    network=KeyNetworkPolicy(mode="allow", max_mode="allow", allow=["pypi.org"]),
)
created.key  # shown once

client.list_keys()
client.get_key(created.id)
client.revoke_key(created.id)  # stops working immediately
```

### Backends

```python
usable = [b.name for b in client.list_backends() if b.available]
```

## Errors

Failed requests raise `SbxError` with the HTTP `status`, the API's error `code`, and its `message`:

```python
from sbx import SbxError

try:
    Sandbox.create("missing")
except SbxError as err:
    if err.code == "not_found":
        ...
    raise
```

The codes are listed in the [HTTP API reference](/docs/api/#errors).

## Types

Responses are dataclasses, all exported from `sbx`: `SandboxInfo`, `ExecResult`, `ExecEvent`, `FileEntry`, `FileList`, `FileEvent`, `ProcessInfo`, `TerminalInfo`, `TerminalSession`, `PreviewInfo`, `PreviewResponse`, `TemplateInfo`, `BuildInfo`, `VolumeInfo`, `VolumeMount`, `NetworkPolicy`, `KeyInfo`, `KeyNetworkPolicy`, `CreatedKey`, and `BackendInfo`. `SandboxStatus`, `NetworkMode`, and `BuildStep` are type aliases.
