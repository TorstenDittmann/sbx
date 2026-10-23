from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Literal, Mapping, Optional, Union

SandboxStatus = Literal["creating", "running", "paused", "stopped", "error", "warm"]
NetworkMode = Literal["none", "full", "allow"]
Backend = Literal["docker", "gvisor", "kata", "firecracker"]
Signal = Literal["TERM", "KILL", "INT", "HUP", "QUIT"]

# BuildStep is one template build step: exactly one of {"run": str},
# {"packages": [str]}, {"env": {str: str}}, or {"workdir": str}.
BuildStep = Dict[str, Any]


class SbxError(Exception):
    """A failed request. status is the HTTP status and code the API's error code."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message

    def __repr__(self) -> str:
        return f"SbxError(status={self.status}, code={self.code!r}, message={self.message!r})"


@dataclass(frozen=True)
class VolumeMount:
    volume_id: str
    path: str

    def to_json(self) -> Dict[str, str]:
        return {"volume_id": self.volume_id, "path": self.path}

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "VolumeMount":
        return cls(volume_id=raw["volume_id"], path=raw["path"])


@dataclass
class NetworkPolicy:
    mode: Optional[NetworkMode] = None
    allow: Optional[List[str]] = None
    deny: Optional[List[str]] = None
    egress_kbps: Optional[int] = None
    max_connections: Optional[int] = None

    def to_json(self) -> Dict[str, Any]:
        body: Dict[str, Any] = {}
        if self.mode:
            body["mode"] = self.mode
        if self.allow is not None:
            body["allow"] = list(self.allow)
        if self.deny is not None:
            body["deny"] = list(self.deny)
        if self.egress_kbps is not None:
            body["egress_kbps"] = self.egress_kbps
        if self.max_connections is not None:
            body["max_connections"] = self.max_connections
        return body


@dataclass
class KeyNetworkPolicy(NetworkPolicy):
    """A key's network policy. max_mode makes the key's mode and allow list a
    ceiling instead of a default."""

    max_mode: Optional[NetworkMode] = None

    def to_json(self) -> Dict[str, Any]:
        body = super().to_json()
        if self.max_mode:
            body["max_mode"] = self.max_mode
        return body


@dataclass
class SandboxInfo:
    id: str
    status: SandboxStatus
    backend: str
    image: str
    cpu: float
    memory_mb: int
    disk_mb: int
    pids: int
    network: str
    expires_at: str
    created_at: str
    node: str
    template: Optional[str] = None
    egress_allow: Optional[List[str]] = None
    egress_deny: Optional[List[str]] = None
    egress_kbps: Optional[int] = None
    max_connections: Optional[int] = None
    env: Optional[Dict[str, str]] = None
    labels: Optional[Dict[str, str]] = None
    secret_keys: Optional[List[str]] = None
    idle_timeout_seconds: Optional[int] = None
    mounts: Optional[List[VolumeMount]] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "SandboxInfo":
        mounts = raw.get("mounts")
        return cls(
            id=raw["id"],
            status=raw["status"],
            backend=raw["backend"],
            template=raw.get("template"),
            image=raw["image"],
            cpu=raw["cpu"],
            memory_mb=raw["memory_mb"],
            disk_mb=raw["disk_mb"],
            pids=raw["pids"],
            network=raw["network"],
            egress_allow=raw.get("egress_allow"),
            egress_deny=raw.get("egress_deny"),
            egress_kbps=raw.get("egress_kbps"),
            max_connections=raw.get("max_connections"),
            env=raw.get("env"),
            labels=raw.get("labels"),
            secret_keys=raw.get("secret_keys"),
            idle_timeout_seconds=raw.get("idle_timeout_seconds"),
            expires_at=raw["expires_at"],
            created_at=raw["created_at"],
            node=raw["node"],
            mounts=[VolumeMount.from_json(m) for m in mounts] if mounts is not None else None,
        )


@dataclass
class VolumeInfo:
    id: str
    name: str
    backend: str
    created_at: str

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "VolumeInfo":
        return cls(id=raw["id"], name=raw["name"], backend=raw["backend"], created_at=raw["created_at"])


@dataclass
class BuildInfo:
    # id is the content hash of the base image and steps. It is empty when no
    # step runs a command, and image is then the base itself.
    id: str
    image: str
    base: str
    steps: List[BuildStep]
    cached: bool
    duration_ms: int

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "BuildInfo":
        return cls(
            id=raw["id"],
            image=raw["image"],
            base=raw["base"],
            steps=raw.get("steps") or [],
            cached=raw["cached"],
            duration_ms=raw["duration_ms"],
        )


@dataclass
class TemplateInfo:
    name: str
    image: str
    cpu: float
    memory_mb: int
    disk_mb: int
    network: str
    warm: int
    builtin: bool
    created_at: str
    description: Optional[str] = None
    env: Optional[Dict[str, str]] = None
    backend: Optional[str] = None
    setup: List[str] = field(default_factory=list)
    setup_timeout_seconds: int = 0
    steps: List[BuildStep] = field(default_factory=list)

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "TemplateInfo":
        return cls(
            name=raw["name"],
            image=raw["image"],
            description=raw.get("description"),
            cpu=raw["cpu"],
            memory_mb=raw["memory_mb"],
            disk_mb=raw["disk_mb"],
            env=raw.get("env"),
            backend=raw.get("backend"),
            network=raw["network"],
            setup=raw.get("setup") or [],
            setup_timeout_seconds=raw.get("setup_timeout_seconds") or 0,
            warm=raw["warm"],
            steps=raw.get("steps") or [],
            builtin=raw["builtin"],
            created_at=raw["created_at"],
        )


@dataclass
class BackendInfo:
    name: str
    available: bool
    isolation: str
    pause: bool
    snapshot: bool
    exec: bool
    pty: bool
    files: bool
    watch: bool
    preview: bool
    networks: List[str]
    requirements: List[str]
    detail: Optional[str] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "BackendInfo":
        return cls(
            name=raw["name"],
            available=raw["available"],
            detail=raw.get("detail"),
            isolation=raw["isolation"],
            pause=raw["pause"],
            snapshot=raw["snapshot"],
            exec=raw["exec"],
            pty=raw["pty"],
            files=raw["files"],
            watch=raw["watch"],
            preview=raw["preview"],
            networks=raw.get("networks") or [],
            requirements=raw.get("requirements") or [],
        )


@dataclass
class ExecResult:
    exit_code: int
    stdout: str
    stderr: str
    duration_ms: int
    truncated: bool

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "ExecResult":
        return cls(
            exit_code=raw["exit_code"],
            stdout=raw["stdout"],
            stderr=raw["stderr"],
            duration_ms=raw["duration_ms"],
            truncated=raw["truncated"],
        )


@dataclass
class ExecEvent:
    type: Literal["stdout", "stderr", "exit", "error"]
    data: str = ""
    exit_code: Optional[int] = None
    duration_ms: Optional[int] = None
    truncated: Optional[bool] = None
    code: Optional[str] = None
    message: Optional[str] = None


@dataclass
class ProcessInfo:
    id: str
    pid: int
    cmd: List[str]
    tty: bool
    status: Literal["running", "exited"]
    exit_code: int
    cwd: Optional[str] = None
    started_at: Optional[str] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "ProcessInfo":
        return cls(
            id=raw["id"],
            pid=raw["pid"],
            cmd=raw["cmd"],
            cwd=raw.get("cwd"),
            tty=bool(raw.get("tty")),
            status=raw["status"],
            exit_code=raw["exit_code"],
            started_at=raw.get("started_at"),
        )


@dataclass
class TerminalInfo:
    id: str
    pid: int
    cols: int
    rows: int

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "TerminalInfo":
        return cls(id=raw["id"], pid=raw["pid"], cols=raw["cols"], rows=raw["rows"])


@dataclass
class PreviewInfo:
    id: str
    sandbox_id: str
    port: int
    public: bool
    url: str
    public_url: Optional[str] = None
    host: Optional[str] = None
    token: Optional[str] = None
    created_at: Optional[str] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "PreviewInfo":
        return cls(
            id=raw["id"],
            sandbox_id=raw["sandbox_id"],
            port=raw["port"],
            public=bool(raw.get("public")),
            url=raw["url"],
            public_url=raw.get("public_url"),
            host=raw.get("host"),
            token=raw.get("token"),
            created_at=raw.get("created_at"),
        )


@dataclass
class PreviewResponse:
    """The response from a request through a preview. It is returned for any
    status, like fetch, so check status yourself."""

    status: int
    headers: Dict[str, str]
    body: bytes

    @property
    def ok(self) -> bool:
        return 200 <= self.status < 300

    def text(self, encoding: str = "utf-8") -> str:
        return self.body.decode(encoding)

    def json(self) -> Any:
        import json

        return json.loads(self.body)


@dataclass
class KeyInfo:
    """An API key without its secret."""

    id: str
    name: str
    prefix: str
    scopes: List[str]
    network: KeyNetworkPolicy
    created_at: Optional[str] = None
    last_used_at: Optional[str] = None
    expires_at: Optional[str] = None
    revoked_at: Optional[str] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "KeyInfo":
        return cls(**_key_fields(raw))


@dataclass
class CreatedKey(KeyInfo):
    """A new API key. key is the secret and is only returned once."""

    key: str = ""

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "CreatedKey":
        return cls(**_key_fields(raw), key=raw["key"])


def _key_fields(raw: Mapping[str, Any]) -> Dict[str, Any]:
    network = raw.get("network") or {}
    return {
        "id": raw["id"],
        "name": raw["name"],
        "prefix": raw["prefix"],
        "scopes": raw.get("scopes") or [],
        "created_at": raw.get("created_at"),
        "last_used_at": raw.get("last_used_at"),
        "expires_at": raw.get("expires_at"),
        "revoked_at": raw.get("revoked_at"),
        "network": KeyNetworkPolicy(
            mode=network.get("mode"),
            max_mode=network.get("max_mode"),
            allow=network.get("allow"),
            deny=network.get("deny"),
            egress_kbps=network.get("egress_kbps"),
            max_connections=network.get("max_connections"),
        ),
    }


@dataclass
class FileEvent:
    """A filesystem change inside a sandbox. type is create, write, or remove."""

    type: str
    path: str


@dataclass
class FileEntry:
    name: str
    path: str
    type: Literal["file", "dir", "symlink"]
    size: int
    mode: Optional[str] = None
    mtime: Optional[str] = None

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "FileEntry":
        return cls(
            name=raw["name"],
            path=raw["path"],
            type=raw["type"],
            size=raw.get("size", 0),
            mode=raw.get("mode"),
            mtime=raw.get("mtime"),
        )


@dataclass
class FileList:
    path: str
    entries: List[FileEntry]

    @classmethod
    def from_json(cls, raw: Mapping[str, Any]) -> "FileList":
        return cls(path=raw["path"], entries=[FileEntry.from_json(e) for e in raw.get("entries") or []])


NetworkArg = Union[NetworkPolicy, Mapping[str, Any]]
MountArg = Union[VolumeMount, Mapping[str, str]]
