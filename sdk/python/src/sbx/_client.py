from __future__ import annotations

import base64
import json
import os
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime
from typing import (
    IO,
    TYPE_CHECKING,
    Any,
    Callable,
    Dict,
    Iterable,
    Iterator,
    List,
    Mapping,
    Optional,
    Sequence,
    Tuple,
    Type,
    Union,
)

from ._duration import Duration, parse_duration
from ._terminal import TerminalSession, connect_websocket
from ._types import (
    Backend,
    BackendInfo,
    BuildInfo,
    BuildStep,
    CreatedKey,
    ExecEvent,
    ExecResult,
    FileEntry,
    FileEvent,
    FileList,
    KeyInfo,
    KeyNetworkPolicy,
    MountArg,
    NetworkArg,
    NetworkPolicy,
    PreviewInfo,
    PreviewResponse,
    ProcessInfo,
    SandboxInfo,
    SbxError,
    Signal,
    TemplateInfo,
    TerminalInfo,
    VolumeInfo,
    VolumeMount,
)

if TYPE_CHECKING:
    from ._template import Template

Command = Union[str, Sequence[str]]
Content = Union[str, bytes, bytearray, memoryview]
Mode = Union[int, str]
# An archive upload is bytes, a binary file object, or an iterable of byte
# chunks. A file or iterable is streamed with chunked transfer encoding.
ArchiveBody = Union[bytes, bytearray, memoryview, IO[bytes], Iterable[bytes]]

DEFAULT_URL = "http://127.0.0.1:8080"


def _q(value: str) -> str:
    return urllib.parse.quote(value, safe="")


def _command_args(cmd: Command) -> List[str]:
    if isinstance(cmd, str):
        return ["sh", "-lc", cmd]
    args = list(cmd)
    if not args:
        raise ValueError("cmd must not be empty")
    return args


def _mode_query(mode: Optional[Mode]) -> Optional[str]:
    if mode is None:
        return None
    if isinstance(mode, int):
        return format(mode, "o")
    return mode[2:] if mode.startswith("0o") else mode


def _to_bytes(content: Content) -> bytes:
    if isinstance(content, str):
        return content.encode("utf-8")
    return bytes(content)


def _network(policy: NetworkArg, cls: Type[NetworkPolicy] = NetworkPolicy) -> Dict[str, Any]:
    if isinstance(policy, NetworkPolicy):
        return policy.to_json()
    return cls(**policy).to_json()


def _mount(mount: MountArg) -> Dict[str, str]:
    if isinstance(mount, VolumeMount):
        return mount.to_json()
    return VolumeMount(volume_id=mount["volume_id"], path=mount["path"]).to_json()


def _exec_body(
    cmd: Command,
    *,
    cwd: Optional[str] = None,
    env: Optional[Mapping[str, str]] = None,
    timeout: Optional[Duration] = None,
    stdin: Optional[Content] = None,
) -> Dict[str, Any]:
    body: Dict[str, Any] = {"cmd": _command_args(cmd)}
    if cwd:
        body["cwd"] = cwd
    if env:
        body["env"] = dict(env)
    if timeout is not None:
        body["timeout_seconds"] = parse_duration(timeout)
    if isinstance(stdin, str):
        body["stdin"] = stdin
    elif stdin is not None:
        body["stdin_base64"] = base64.b64encode(bytes(stdin)).decode("ascii")
    return body


def create_body(
    template: Union[str, "Template", None] = None,
    *,
    image: Optional[str] = None,
    steps: Optional[Sequence[BuildStep]] = None,
    cpu: Optional[float] = None,
    memory_mb: Optional[int] = None,
    disk_mb: Optional[int] = None,
    pids: Optional[int] = None,
    ttl: Optional[Duration] = None,
    idle_timeout: Optional[Duration] = None,
    env: Optional[Mapping[str, str]] = None,
    network: Optional[NetworkArg] = None,
    backend: Optional[Backend] = None,
    labels: Optional[Mapping[str, str]] = None,
    mounts: Optional[Sequence[MountArg]] = None,
    secrets: Optional[Mapping[str, str]] = None,
) -> Dict[str, Any]:
    from ._template import Template

    body: Dict[str, Any] = {}
    all_steps: List[BuildStep] = []
    if isinstance(template, Template):
        if template.template:
            body["template"] = template.template
        if template.image:
            body["image"] = template.image
        all_steps.extend(template.steps)
    elif template:
        body["template"] = template
    if image:
        body["image"] = image
    if steps:
        all_steps.extend(steps)
    if all_steps:
        body["steps"] = [dict(step) for step in all_steps]
    if cpu is not None:
        body["cpu"] = cpu
    if memory_mb is not None:
        body["memory_mb"] = memory_mb
    if disk_mb is not None:
        body["disk_mb"] = disk_mb
    if pids is not None:
        body["pids"] = pids
    if ttl is not None:
        body["ttl_seconds"] = parse_duration(ttl)
    if idle_timeout is not None:
        body["idle_timeout_seconds"] = parse_duration(idle_timeout)
    if env:
        body["env"] = dict(env)
    if network is not None:
        body["network"] = _network(network)
    if backend:
        body["backend"] = backend
    if labels:
        body["labels"] = dict(labels)
    if mounts is not None:
        body["mounts"] = [_mount(m) for m in mounts]
    if secrets:
        body["secrets"] = dict(secrets)
    return body


def _error_from(err: urllib.error.HTTPError) -> SbxError:
    code = "driver"
    message = err.reason if isinstance(err.reason, str) and err.reason else "request failed"
    try:
        raw = err.read()
        parsed = json.loads(raw) if raw else {}
        detail = parsed.get("error") or {}
        code = detail.get("code") or code
        message = detail.get("message") or message
    except (ValueError, AttributeError, OSError):
        # Keep the reason when the body is not JSON.
        pass
    finally:
        err.close()
    return SbxError(err.code, code, message)


def _sse_events(response: Any) -> Iterator[Tuple[str, Dict[str, Any]]]:
    """Yield (event, data) pairs from a text/event-stream response."""
    event = ""
    data: List[str] = []
    for raw in response:
        line = raw.decode("utf-8").rstrip("\r\n")
        if line == "":
            if event:
                yield event, json.loads("\n".join(data)) if data else {}
            event, data = "", []
        elif line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:"):
            data.append(line[5:].strip())
    if event:
        yield event, json.loads("\n".join(data)) if data else {}


def _exec_events(response: Any) -> Iterator[ExecEvent]:
    for kind, raw in _sse_events(response):
        if kind == "stdout":
            yield ExecEvent(type="stdout", data=str(raw.get("data", "")))
        elif kind == "stderr":
            yield ExecEvent(type="stderr", data=str(raw.get("data", "")))
        elif kind == "exit":
            yield ExecEvent(
                type="exit",
                exit_code=int(raw.get("exit_code", 0)),
                duration_ms=int(raw.get("duration_ms", 0)),
                truncated=bool(raw.get("truncated", False)),
            )
        else:
            yield ExecEvent(type="error", code=str(raw.get("code", "driver")), message=str(raw.get("message", "")))


class SbxClient:
    """The low-level HTTP client. Sandbox is the usual entry point.

    base_url defaults to SBX_URL, then http://127.0.0.1:8080. api_key defaults
    to SBX_API_KEY and is required. timeout is the socket timeout in seconds
    for each request (None waits forever, which suits long execs and streams).
    opener is a urllib OpenerDirector for proxies or tests.
    """

    def __init__(
        self,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
        *,
        timeout: Optional[float] = None,
        opener: Optional[urllib.request.OpenerDirector] = None,
        websocket_connect: Optional[Callable[[str], Any]] = None,
    ) -> None:
        base = base_url or os.environ.get("SBX_URL") or DEFAULT_URL
        key = api_key or os.environ.get("SBX_API_KEY") or ""
        if not key:
            raise ValueError("an API key is required (pass api_key or set SBX_API_KEY)")
        self.base_url = base.rstrip("/")
        self.api_key = key
        self.timeout = timeout
        self._opener = opener or urllib.request.build_opener()
        self._websocket_connect = websocket_connect

    def __repr__(self) -> str:
        return f"SbxClient(base_url={self.base_url!r})"

    # Volumes

    def create_volume(self, name: str, backend: Optional[str] = None, *, if_exists: Optional[str] = None) -> VolumeInfo:
        """Create a named volume. A name the key already uses fails with 409,
        unless if_exists is "reuse", which returns the existing volume instead."""
        body: Dict[str, Any] = {"name": name}
        if backend:
            body["backend"] = backend
        if if_exists:
            body["if_exists"] = if_exists
        return VolumeInfo.from_json(self._json("POST", "/v1/volumes", body))

    def ensure_volume(self, name: str, backend: Optional[str] = None) -> VolumeInfo:
        """Get the volume with this name, creating it if needed. Safe to call again on retry."""
        return self.create_volume(name, backend, if_exists="reuse")

    def list_volumes(self) -> List[VolumeInfo]:
        return [VolumeInfo.from_json(v) for v in self._json("GET", "/v1/volumes")["volumes"]]

    def get_volume(self, id: str) -> VolumeInfo:
        return VolumeInfo.from_json(self._json("GET", f"/v1/volumes/{_q(id)}"))

    def delete_volume(self, id: str) -> None:
        self._json("DELETE", f"/v1/volumes/{_q(id)}")

    # Templates and builds

    def create_template(
        self,
        name: str,
        image: str,
        *,
        description: Optional[str] = None,
        cpu: Optional[float] = None,
        memory_mb: Optional[int] = None,
        disk_mb: Optional[int] = None,
        env: Optional[Mapping[str, str]] = None,
        backend: Optional[Backend] = None,
        network: Optional[str] = None,
        setup: Optional[Sequence[str]] = None,
        setup_timeout: Optional[Duration] = None,
        warm: Optional[int] = None,
        steps: Optional[Sequence[BuildStep]] = None,
    ) -> TemplateInfo:
        body: Dict[str, Any] = {"name": name, "image": image}
        if description:
            body["description"] = description
        if cpu is not None:
            body["cpu"] = cpu
        if memory_mb is not None:
            body["memory_mb"] = memory_mb
        if disk_mb is not None:
            body["disk_mb"] = disk_mb
        if env:
            body["env"] = dict(env)
        if backend:
            body["backend"] = backend
        if network:
            body["network"] = network
        if setup is not None:
            body["setup"] = list(setup)
        if setup_timeout is not None:
            body["setup_timeout_seconds"] = parse_duration(setup_timeout)
        if warm is not None:
            body["warm"] = warm
        if steps is not None:
            body["steps"] = [dict(step) for step in steps]
        return TemplateInfo.from_json(self._json("POST", "/v1/templates", body))

    def build(
        self,
        steps: Sequence[BuildStep],
        *,
        image: Optional[str] = None,
        template: Optional[str] = None,
        backend: Optional[Backend] = None,
    ) -> BuildInfo:
        """Build and cache an image from a base and steps. A create with the
        same base and steps then starts from the cached image."""
        body: Dict[str, Any] = {"steps": [dict(step) for step in steps]}
        if image:
            body["image"] = image
        if template:
            body["template"] = template
        if backend:
            body["backend"] = backend
        return BuildInfo.from_json(self._json("POST", "/v1/builds", body))

    def list_templates(self) -> List[TemplateInfo]:
        return [TemplateInfo.from_json(t) for t in self._json("GET", "/v1/templates")["templates"]]

    def delete_template(self, name: str) -> None:
        self._json("DELETE", f"/v1/templates/{_q(name)}")

    # Keys

    def create_key(
        self,
        name: str,
        *,
        scopes: Optional[Sequence[str]] = None,
        network: Optional[Union[KeyNetworkPolicy, Mapping[str, Any]]] = None,
        expires_at: Optional[Union[datetime, str]] = None,
    ) -> CreatedKey:
        body: Dict[str, Any] = {"name": name}
        if scopes is not None:
            body["scopes"] = list(scopes)
        if network is not None:
            body["network"] = _network(network, KeyNetworkPolicy)
        if expires_at is not None:
            body["expires_at"] = expires_at.isoformat() if isinstance(expires_at, datetime) else expires_at
        return CreatedKey.from_json(self._json("POST", "/v1/keys", body))

    def list_keys(self) -> List[KeyInfo]:
        return [KeyInfo.from_json(k) for k in self._json("GET", "/v1/keys")["keys"]]

    def get_key(self, id: str) -> KeyInfo:
        return KeyInfo.from_json(self._json("GET", f"/v1/keys/{_q(id)}"))

    def revoke_key(self, id: str) -> None:
        """Revoke a key. It stops authenticating immediately."""
        self._json("DELETE", f"/v1/keys/{_q(id)}")

    # Backends

    def list_backends(self) -> List[BackendInfo]:
        return [BackendInfo.from_json(b) for b in self._json("GET", "/v1/backends")["backends"]]

    # Sandboxes

    def create_sandbox(self, template: Union[str, "Template", None] = None, **options: Any) -> SandboxInfo:
        """Create a sandbox and return its info. Takes the same options as Sandbox.create."""
        return SandboxInfo.from_json(self._json("POST", "/v1/sandboxes", create_body(template, **options)))

    def get_sandbox(self, id: str) -> SandboxInfo:
        return SandboxInfo.from_json(self._json("GET", f"/v1/sandboxes/{_q(id)}"))

    def list_sandboxes(
        self,
        *,
        status: Optional[str] = None,
        backend: Optional[str] = None,
        template: Optional[str] = None,
        q: Optional[str] = None,
        labels: Optional[Mapping[str, str]] = None,
    ) -> List[SandboxInfo]:
        params: List[Tuple[str, str]] = []
        if status:
            params.append(("status", status))
        if backend:
            params.append(("backend", backend))
        if template:
            params.append(("template", template))
        if q:
            params.append(("q", q))
        for key, value in (labels or {}).items():
            params.append(("label", f"{key}:{value}"))
        suffix = f"?{urllib.parse.urlencode(params)}" if params else ""
        return [SandboxInfo.from_json(s) for s in self._json("GET", f"/v1/sandboxes{suffix}")["sandboxes"]]

    def delete_sandbox(self, id: str) -> SandboxInfo:
        return SandboxInfo.from_json(self._json("DELETE", f"/v1/sandboxes/{_q(id)}"))

    def set_secrets(self, id: str, secrets: Mapping[str, str]) -> SandboxInfo:
        return SandboxInfo.from_json(self._json("PUT", f"/v1/sandboxes/{_q(id)}/secrets", {"secrets": dict(secrets)}))

    def set_ttl(self, id: str, ttl: Duration) -> SandboxInfo:
        body = {"ttl_seconds": parse_duration(ttl)}
        return SandboxInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/timeout", body))

    def pause(self, id: str) -> SandboxInfo:
        return SandboxInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/pause"))

    def resume(self, id: str) -> SandboxInfo:
        return SandboxInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/resume"))

    # Exec

    def exec(
        self,
        id: str,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> ExecResult:
        """Run a command and wait for it. A string runs through sh -lc."""
        body = _exec_body(cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)
        return ExecResult.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/exec", body))

    def exec_stream(
        self,
        id: str,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> Iterator[ExecEvent]:
        """Run a command and yield stdout, stderr, exit, and error events as they arrive."""
        body = _exec_body(cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)
        with self._open("POST", f"/v1/sandboxes/{_q(id)}/exec?stream=1", _json_bytes(body), "application/json") as res:
            yield from _exec_events(res)

    # Files

    def list_files(self, id: str, path: str) -> FileList:
        return FileList.from_json(self._json("GET", f"/v1/sandboxes/{_q(id)}/files?{_path_query(path)}"))

    def read_file(self, id: str, path: str) -> bytes:
        return self._bytes("GET", f"/v1/sandboxes/{_q(id)}/files/content?{_path_query(path)}")

    def write_file(self, id: str, path: str, content: Content, mode: Optional[Mode] = None) -> None:
        query = _path_query(path, mode=_mode_query(mode))
        self._bytes("PUT", f"/v1/sandboxes/{_q(id)}/files/content?{query}", _to_bytes(content), "application/octet-stream")

    def remove_file(self, id: str, path: str) -> None:
        self._bytes("DELETE", f"/v1/sandboxes/{_q(id)}/files?{_path_query(path)}")

    def upload_archive(self, id: str, path: str, content: ArchiveBody) -> None:
        """Upload a tar or gzip tar. A file object or an iterable of chunks is
        streamed. With sbx-agent, extraction does not use a tar binary."""
        if isinstance(content, (bytearray, memoryview)):
            content = bytes(content)
        self._bytes("POST", f"/v1/sandboxes/{_q(id)}/files/archive?{_path_query(path)}", content, "application/octet-stream")

    def download_archive(self, id: str, path: str, dest: Optional[IO[bytes]] = None) -> Optional[bytes]:
        """Download a directory as gzip tar. With dest, the archive is copied
        into that binary file object and None is returned. With sbx-agent,
        packing does not use a tar binary."""
        url = f"/v1/sandboxes/{_q(id)}/files/archive?{_path_query(path)}"
        if dest is None:
            return self._bytes("GET", url)
        with self._open("GET", url) as res:
            while True:
                chunk = res.read(64 * 1024)
                if not chunk:
                    break
                dest.write(chunk)
        return None

    def stat_file(self, id: str, path: str) -> FileEntry:
        return FileEntry.from_json(self._json("GET", f"/v1/sandboxes/{_q(id)}/files/stat?{_path_query(path)}"))

    def mkdir(self, id: str, path: str, mode: Optional[Mode] = None) -> None:
        body: Dict[str, Any] = {"path": path}
        mode_value = _mode_query(mode)
        if mode_value:
            body["mode"] = mode_value
        self._json("POST", f"/v1/sandboxes/{_q(id)}/files/mkdir", body)

    def watch(self, id: str, path: str, *, recursive: bool = False) -> Iterator[FileEvent]:
        """Yield create, write, and remove events. The agent uses inotify and
        polls if inotify is unavailable. Stop iterating to close the stream."""
        query = _path_query(path, recursive="1" if recursive else None)
        with self._open("GET", f"/v1/sandboxes/{_q(id)}/files/watch?{query}") as res:
            for kind, raw in _sse_events(res):
                if kind == "change":
                    yield FileEvent(type=str(raw.get("type", "")), path=str(raw.get("path", "")))
                elif kind == "error":
                    raise SbxError(502, str(raw.get("code", "driver")), str(raw.get("message", "watch failed")))

    # Processes

    def start_process(
        self,
        id: str,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> ProcessInfo:
        body = _exec_body(cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)
        return ProcessInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/processes", body))

    def list_processes(self, id: str) -> List[ProcessInfo]:
        body = self._json("GET", f"/v1/sandboxes/{_q(id)}/processes")
        return [ProcessInfo.from_json(p) for p in body.get("processes") or []]

    def signal_process(self, id: str, proc_id: str, signal: Signal) -> None:
        self._json("POST", f"/v1/sandboxes/{_q(id)}/processes/{_q(proc_id)}/signal", {"signal": signal})

    def write_process_stdin(self, id: str, proc_id: str, data: Content) -> None:
        url = f"/v1/sandboxes/{_q(id)}/processes/{_q(proc_id)}/stdin"
        self._bytes("POST", url, _to_bytes(data), "application/octet-stream")

    def stream_process(self, id: str, proc_id: str) -> Iterator[ExecEvent]:
        with self._open("GET", f"/v1/sandboxes/{_q(id)}/processes/{_q(proc_id)}/stream") as res:
            yield from _exec_events(res)

    # Terminals

    def start_terminal(
        self,
        id: str,
        cmd: Command,
        *,
        cols: Optional[int] = None,
        rows: Optional[int] = None,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
    ) -> TerminalInfo:
        # The terminal route takes no timeout or stdin; it rejects unknown fields.
        body = _exec_body(cmd, cwd=cwd, env=env)
        if cols is not None:
            body["cols"] = cols
        if rows is not None:
            body["rows"] = rows
        return TerminalInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/terminals", body))

    def resize_terminal(self, id: str, term_id: str, cols: int, rows: int) -> None:
        self._json("POST", f"/v1/sandboxes/{_q(id)}/terminals/{_q(term_id)}/resize", {"cols": cols, "rows": rows})

    def connect_terminal(self, id: str, term_id: str) -> TerminalSession:
        """Attach to a PTY over a WebSocket. Needs the websockets package
        (pip install "sbx-sdk[terminal]") unless websocket_connect was passed."""
        url = (
            f"{_ws_base(self.base_url)}/v1/sandboxes/{_q(id)}/terminals/{_q(term_id)}/ws"
            f"?access_token={_q(self.api_key)}"
        )
        connect = self._websocket_connect or connect_websocket
        return TerminalSession(term_id, connect(url))

    # Previews

    def create_preview(self, id: str, port: int, *, public: bool = False) -> PreviewInfo:
        body: Dict[str, Any] = {"port": port}
        if public:
            body["public"] = True
        return PreviewInfo.from_json(self._json("POST", f"/v1/sandboxes/{_q(id)}/previews", body))

    def list_previews(self, id: str) -> List[PreviewInfo]:
        body = self._json("GET", f"/v1/sandboxes/{_q(id)}/previews")
        return [PreviewInfo.from_json(p) for p in body.get("previews") or []]

    def delete_preview(self, id: str, preview_id: str) -> None:
        self._json("DELETE", f"/v1/sandboxes/{_q(id)}/previews/{_q(preview_id)}")

    def preview_fetch(
        self,
        id: str,
        preview_id: str,
        path: str = "/",
        *,
        method: str = "GET",
        body: Optional[Content] = None,
        headers: Optional[Mapping[str, str]] = None,
    ) -> PreviewResponse:
        """Send a request through a preview with this client's key. Any status
        is returned, not raised."""
        suffix = path if path.startswith("/") else f"/{path}"
        url = f"{self.base_url}/v1/sandboxes/{_q(id)}/previews/{_q(preview_id)}{suffix}"
        req = urllib.request.Request(url, data=_to_bytes(body) if body is not None else None, method=method)
        for name, value in (headers or {}).items():
            req.add_header(name, value)
        req.add_header("Authorization", f"Bearer {self.api_key}")
        try:
            res = self._opener.open(req, timeout=self.timeout)
        except urllib.error.HTTPError as err:
            res = err
        with res:
            return PreviewResponse(status=res.status, headers=dict(res.headers.items()), body=res.read())

    # Transport

    def _open(
        self,
        method: str,
        path: str,
        body: Optional[ArchiveBody] = None,
        content_type: Optional[str] = None,
    ) -> Any:
        req = urllib.request.Request(f"{self.base_url}{path}", data=body, method=method)
        req.add_header("Authorization", f"Bearer {self.api_key}")
        if body is not None and content_type:
            req.add_header("Content-Type", content_type)
        try:
            return self._opener.open(req, timeout=self.timeout)
        except urllib.error.HTTPError as err:
            raise _error_from(err) from None

    def _bytes(self, method: str, path: str, body: Optional[ArchiveBody] = None, content_type: Optional[str] = None) -> bytes:
        with self._open(method, path, body, content_type) as res:
            data: bytes = res.read()
            return data

    def _json(self, method: str, path: str, body: Any = None) -> Any:
        if body is None:
            raw = self._bytes(method, path)
        else:
            raw = self._bytes(method, path, _json_bytes(body), "application/json")
        return json.loads(raw) if raw else None


def _json_bytes(body: Any) -> bytes:
    return json.dumps(body).encode("utf-8")


def _path_query(path: str, **extra: Optional[str]) -> str:
    params = [("path", path)]
    params.extend((k, v) for k, v in extra.items() if v)
    return urllib.parse.urlencode(params)


def _ws_base(http_base: str) -> str:
    if http_base.startswith("https://"):
        return "wss://" + http_base[len("https://") :]
    if http_base.startswith("http://"):
        return "ws://" + http_base[len("http://") :]
    return http_base
