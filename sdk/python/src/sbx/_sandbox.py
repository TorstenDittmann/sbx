from __future__ import annotations

import dataclasses
from typing import IO, Any, Callable, Dict, Iterator, List, Mapping, Optional, Sequence, Union, overload

from ._client import ArchiveBody, Command, Content, Mode, SbxClient
from ._duration import Duration
from ._template import Template
from ._terminal import TerminalSession
from ._types import (
    Backend,
    BuildStep,
    ExecEvent,
    ExecResult,
    FileEntry,
    FileEvent,
    FileList,
    MountArg,
    NetworkArg,
    PreviewInfo,
    PreviewResponse,
    ProcessInfo,
    SandboxInfo,
    SandboxStatus,
    SbxError,
    Signal,
    TerminalInfo,
    VolumeMount,
)


class SandboxFiles:
    """The file routes of one sandbox. Paths are absolute."""

    def __init__(self, client: SbxClient, id: str) -> None:
        self._client = client
        self._id = id

    def list(self, path: str = "/workspace") -> FileList:
        return self._client.list_files(self._id, path)

    def read(self, path: str) -> bytes:
        return self._client.read_file(self._id, path)

    def read_text(self, path: str, encoding: str = "utf-8") -> str:
        return self.read(path).decode(encoding)

    def write(self, path: str, content: Content, mode: Optional[Mode] = None) -> None:
        self._client.write_file(self._id, path, content, mode)

    def remove(self, path: str) -> None:
        self._client.remove_file(self._id, path)

    def stat(self, path: str) -> FileEntry:
        return self._client.stat_file(self._id, path)

    def mkdir(self, path: str, mode: Optional[Mode] = None) -> None:
        self._client.mkdir(self._id, path, mode)

    def watch(self, path: str = "/workspace", *, recursive: bool = False) -> Iterator[FileEvent]:
        return self._client.watch(self._id, path, recursive=recursive)

    def upload_archive(self, path: str, content: ArchiveBody) -> None:
        self._client.upload_archive(self._id, path, content)

    def download_archive(self, path: str, dest: Optional[IO[bytes]] = None) -> Optional[bytes]:
        return self._client.download_archive(self._id, path, dest)


def _template_builder(
    base: Optional[str] = None, *, image: Optional[str] = None, template: Optional[str] = None
) -> Template:
    """Start an image builder. A string is a base image. Without an argument
    the builder starts from the built-in "base" template."""
    if base is not None:
        return Template(image=base)
    if image or template:
        return Template(image=image, template=template)
    return Template(template="base")


class _TemplateField:
    # Sandbox.template(...) is the builder, as in the TypeScript SDK, while
    # box.template is the sandbox's template name.

    @overload
    def __get__(self, obj: None, owner: Any) -> Callable[..., Template]: ...

    @overload
    def __get__(self, obj: "Sandbox", owner: Any) -> Optional[str]: ...

    def __get__(self, obj: Optional["Sandbox"], owner: Any) -> Any:
        if obj is None:
            return _template_builder
        return obj.__dict__.get("template")

    def __set__(self, obj: "Sandbox", value: Optional[str]) -> None:
        obj.__dict__["template"] = value


class Sandbox:
    """One sandbox plus the calls that use it.

    Use it as a context manager to kill the sandbox when the block ends.
    """

    id: str
    status: SandboxStatus
    backend: str
    template = _TemplateField()
    image: str
    cpu: float
    memory_mb: int
    disk_mb: int
    pids: int
    network: str
    egress_allow: Optional[List[str]]
    egress_deny: Optional[List[str]]
    egress_kbps: Optional[int]
    max_connections: Optional[int]
    env: Optional[Dict[str, str]]
    labels: Optional[Dict[str, str]]
    secret_keys: Optional[List[str]]
    idle_timeout_seconds: Optional[int]
    expires_at: str
    created_at: str
    node: str
    mounts: Optional[List[VolumeMount]]

    def __init__(self, client: SbxClient, info: SandboxInfo) -> None:
        self.client = client
        self._apply(info)
        self.files = SandboxFiles(client, info.id)

    def __repr__(self) -> str:
        return f"Sandbox(id={self.id!r}, status={self.status!r}, image={self.image!r})"

    def __enter__(self) -> "Sandbox":
        return self

    def __exit__(self, *exc: Any) -> None:
        try:
            self.kill()
        except SbxError as err:
            if err.status != 404:
                raise

    @classmethod
    def create(
        cls,
        template: Union[str, Template, None] = None,
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
        client: Optional[SbxClient] = None,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
    ) -> "Sandbox":
        """Create a sandbox. template is a template name or a Template, whose
        steps build once and then come from cache."""
        conn = client or SbxClient(base_url, api_key)
        info = conn.create_sandbox(
            template,
            image=image,
            steps=steps,
            cpu=cpu,
            memory_mb=memory_mb,
            disk_mb=disk_mb,
            pids=pids,
            ttl=ttl,
            idle_timeout=idle_timeout,
            env=env,
            network=network,
            backend=backend,
            labels=labels,
            mounts=mounts,
            secrets=secrets,
        )
        return cls(conn, info)

    @classmethod
    def connect(
        cls,
        id: str,
        *,
        client: Optional[SbxClient] = None,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
    ) -> "Sandbox":
        conn = client or SbxClient(base_url, api_key)
        return cls(conn, conn.get_sandbox(id))

    def refresh(self) -> SandboxInfo:
        return self._apply(self.client.get_sandbox(self.id))

    def exec(
        self,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> ExecResult:
        return self.client.exec(self.id, cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)

    def exec_stream(
        self,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> Iterator[ExecEvent]:
        return self.client.exec_stream(self.id, cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)

    def kill(self) -> SandboxInfo:
        return self._apply(self.client.delete_sandbox(self.id))

    def pause(self) -> SandboxInfo:
        return self._apply(self.client.pause(self.id))

    def resume(self) -> SandboxInfo:
        return self._apply(self.client.resume(self.id))

    def set_ttl(self, ttl: Duration) -> SandboxInfo:
        return self._apply(self.client.set_ttl(self.id, ttl))

    def set_secrets(self, secrets: Mapping[str, str]) -> SandboxInfo:
        return self._apply(self.client.set_secrets(self.id, secrets))

    def start_process(
        self,
        cmd: Command,
        *,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
        stdin: Optional[Content] = None,
    ) -> ProcessInfo:
        return self.client.start_process(self.id, cmd, cwd=cwd, env=env, timeout=timeout, stdin=stdin)

    def list_processes(self) -> List[ProcessInfo]:
        return self.client.list_processes(self.id)

    def signal_process(self, proc_id: str, signal: Signal) -> None:
        self.client.signal_process(self.id, proc_id, signal)

    def write_process_stdin(self, proc_id: str, data: Content) -> None:
        self.client.write_process_stdin(self.id, proc_id, data)

    def stream_process(self, proc_id: str) -> Iterator[ExecEvent]:
        return self.client.stream_process(self.id, proc_id)

    def start_terminal(
        self,
        cmd: Command,
        *,
        cols: Optional[int] = None,
        rows: Optional[int] = None,
        cwd: Optional[str] = None,
        env: Optional[Mapping[str, str]] = None,
        timeout: Optional[Duration] = None,
    ) -> TerminalInfo:
        return self.client.start_terminal(self.id, cmd, cols=cols, rows=rows, cwd=cwd, env=env, timeout=timeout)

    def resize_terminal(self, term_id: str, cols: int, rows: int) -> None:
        self.client.resize_terminal(self.id, term_id, cols, rows)

    def connect_terminal(self, term_id: str) -> TerminalSession:
        return self.client.connect_terminal(self.id, term_id)

    def create_preview(self, port: int, *, public: bool = False) -> PreviewInfo:
        return self.client.create_preview(self.id, port, public=public)

    def list_previews(self) -> List[PreviewInfo]:
        return self.client.list_previews(self.id)

    def delete_preview(self, preview_id: str) -> None:
        self.client.delete_preview(self.id, preview_id)

    def preview_fetch(
        self,
        preview_id: str,
        path: str = "/",
        *,
        method: str = "GET",
        body: Optional[Content] = None,
        headers: Optional[Mapping[str, str]] = None,
    ) -> PreviewResponse:
        return self.client.preview_fetch(self.id, preview_id, path, method=method, body=body, headers=headers)

    def _apply(self, info: SandboxInfo) -> SandboxInfo:
        for f in dataclasses.fields(info):
            setattr(self, f.name, getattr(info, f.name))
        return info
