"""Python client for sbx, a single-binary sandbox server.

    from sbx import Sandbox

    with Sandbox.create(template="base", ttl="5m") as box:
        print(box.exec(["echo", "hello"]).stdout)
"""

from ._client import SbxClient
from ._duration import parse_duration
from ._sandbox import Sandbox, SandboxFiles
from ._template import Template
from ._terminal import TerminalSession
from ._types import (
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
    NetworkMode,
    NetworkPolicy,
    PreviewInfo,
    PreviewResponse,
    ProcessInfo,
    SandboxInfo,
    SandboxStatus,
    SbxError,
    TemplateInfo,
    TerminalInfo,
    VolumeInfo,
    VolumeMount,
)

__version__ = "0.2.1"

__all__ = [
    "BackendInfo",
    "BuildInfo",
    "BuildStep",
    "CreatedKey",
    "ExecEvent",
    "ExecResult",
    "FileEntry",
    "FileEvent",
    "FileList",
    "KeyInfo",
    "KeyNetworkPolicy",
    "NetworkMode",
    "NetworkPolicy",
    "PreviewInfo",
    "PreviewResponse",
    "ProcessInfo",
    "Sandbox",
    "SandboxFiles",
    "SandboxInfo",
    "SandboxStatus",
    "SbxClient",
    "SbxError",
    "Template",
    "TemplateInfo",
    "TerminalInfo",
    "TerminalSession",
    "VolumeInfo",
    "VolumeMount",
    "parse_duration",
]
