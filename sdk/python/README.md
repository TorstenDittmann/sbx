# sbx-sdk

Python client for [sbx](https://github.com/TorstenDittmann/sbx), a single-binary sandbox server.

It mirrors the [TypeScript SDK](https://github.com/TorstenDittmann/sbx/tree/main/sdk/typescript) with Python names: `memory_mb` instead of `memoryMb`, keyword arguments instead of option objects, and iterators instead of async iterators. It has no runtime dependencies and runs on Python 3.9+. Terminals need the optional `websockets` extra.

```bash
pip install sbx-sdk
pip install "sbx-sdk[terminal]"   # for connect_terminal
```

The import name is `sbx`. The unrelated `sbx` package on PyPI uses the same import name, so do not install both in one environment.

```python
from sbx import Sandbox

with Sandbox.create(template="base", ttl="5m") as box:
    result = box.exec(["echo", "hello"])
    print(result.stdout)
```

The `with` block kills the sandbox when it ends. Without it, call `box.kill()`.

`SBX_URL` defaults to `http://127.0.0.1:8080`. `SBX_API_KEY` is required unless you pass `api_key`. Every entry point takes `base_url`, `api_key`, or a ready `client=SbxClient(...)`. `SbxClient` also takes `timeout` (seconds per request; the default waits forever, which suits long execs and streams) and `opener`, a `urllib.request.OpenerDirector` for proxies or custom TLS.

Durations accept seconds, a `timedelta`, or a string with a unit: `"500ms"`, `"30s"`, `"5m"`, `"1h"`. They are rounded to whole seconds, and anything above 0 and below a second becomes 1. A string command runs through `sh -lc`; a list runs as is.

```python
box.exec("ls -la | wc -l", cwd="/workspace", env={"A": "1"}, timeout="30s")
box.exec(["wc", "-c"], stdin=b"\x01\x02\x03")

for event in box.exec_stream("pip install requests"):
    if event.type in ("stdout", "stderr"):
        print(event.data, end="")
    elif event.type == "exit":
        print("exit", event.exit_code)
```

`box.files` wraps the file routes. `box.files.watch` yields `FileEvent(type, path)` for `create`, `write`, and `remove`; stop iterating to close the stream. `upload_archive` accepts a tar or gzip tar as bytes, a binary file object, or an iterable of chunks, and streams the last two. `download_archive` returns a gzip tar, or copies it into `dest` when you pass a file object.

```python
box.files.write("/workspace/run.sh", "#!/bin/sh\necho ok\n", 0o755)
print(box.files.read_text("/workspace/run.sh"))
with open("src.tar.gz", "rb") as f:
    box.files.upload_archive("/workspace", f)
with open("out.tar.gz", "wb") as f:
    box.files.download_archive("/workspace", f)
```

Processes, terminals, and previews:

```python
proc = box.start_process(["python", "-m", "http.server", "3000"], timeout=0)
preview = box.create_preview(3000)
print(box.preview_fetch(preview.id, "/").status)
box.signal_process(proc.id, "TERM")

term = box.start_terminal(["sh"], cols=120, rows=32)
with box.connect_terminal(term.id) as session:
    session.send("echo hello; exit\n")
    for chunk in session:          # ends when the shell exits
        print(chunk.decode(), end="")
    print("exit", session.exit_code)
```

`Sandbox.template(image)` builds a reusable image. Each method returns a new template, so one base can branch into variants. sbx builds the steps once, caches them by content hash, and later creates start from the cached image. `.build()` only warms the cache.

```python
base = (
    Sandbox.template("python:3.12-slim")
    .with_packages("git")
    .with_env(PIP_NO_CACHE_DIR="1")   # build time only
    .workdir("/app")
    .run("pip install requests")
)
base.build()                      # optional
box = Sandbox.create(base, ttl="5m")
```

`Sandbox.template()` with no argument starts from the `base` template, and `Sandbox.template(template="python")` from another one. Builds need an admin key unless the server sets `builds.allow_sandbox_keys`.

`SbxClient` covers every route, with the sandbox id first for sandbox calls (`client.exec(id, cmd)`), plus volumes (`create_volume`, `ensure_volume`), templates (`create_template` with `warm` and `setup`), keys, backends, and `list_sandboxes(template=, q=, labels=)`. Failed requests raise `SbxError` with `status`, `code`, and `message`.

Run the tests with no install step:

```bash
PYTHONPATH=src python -m unittest discover -s tests -t .
```

The live test is skipped unless `SBX_LIVE=1`, and needs a running server plus `SBX_API_KEY`.
