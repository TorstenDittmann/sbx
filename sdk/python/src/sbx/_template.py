from __future__ import annotations

from typing import TYPE_CHECKING, Any, Mapping, Optional, Sequence, Tuple

from ._types import Backend, BuildInfo, BuildStep

if TYPE_CHECKING:
    from ._client import SbxClient


class Template:
    """An immutable list of build steps on a base image or template.

    Every method returns a new Template, so one base can branch into variants.
    sbx builds the steps once, caches the image by content hash, and starts
    later sandboxes from that image. with_env and workdir apply to later steps
    only; they are not set in sandboxes. /workspace is mounted over at
    runtime, so build into another directory such as /app.
    """

    __slots__ = ("image", "template", "steps")

    image: Optional[str]
    template: Optional[str]
    steps: Tuple[BuildStep, ...]

    def __init__(
        self,
        *,
        image: Optional[str] = None,
        template: Optional[str] = None,
        steps: Sequence[BuildStep] = (),
    ) -> None:
        object.__setattr__(self, "image", image)
        object.__setattr__(self, "template", template)
        object.__setattr__(self, "steps", tuple(dict(step) for step in steps))

    def __setattr__(self, name: str, value: Any) -> None:
        raise AttributeError("Template is immutable")

    def __repr__(self) -> str:
        base = f"image={self.image!r}" if self.image else f"template={self.template!r}"
        return f"Template({base}, steps={list(self.steps)!r})"

    def __eq__(self, other: object) -> bool:
        if not isinstance(other, Template):
            return NotImplemented
        return (self.image, self.template, self.steps) == (other.image, other.template, other.steps)

    def run(self, command: str) -> "Template":
        """Add a shell command, run with /bin/sh -c as root."""
        if not command:
            raise ValueError("command must not be empty")
        return self._with({"run": command})

    def with_packages(self, *names: str) -> "Template":
        """Install system packages with apt-get, apk, dnf, or yum, whichever the base image has."""
        if not names:
            raise ValueError("with_packages needs at least one package")
        return self._with({"packages": list(names)})

    def with_env(self, vars: Optional[Mapping[str, str]] = None, **more: str) -> "Template":
        """Set build-time variables for later steps. They are part of the
        content hash, so a change rebuilds. Pass runtime variables to create."""
        merged = {**(vars or {}), **more}
        if not merged:
            raise ValueError("with_env needs at least one variable")
        return self._with({"env": merged})

    def workdir(self, dir: str) -> "Template":
        """Set the working directory for later steps."""
        return self._with({"workdir": dir})

    def build(
        self,
        *,
        backend: Optional[Backend] = None,
        client: Optional["SbxClient"] = None,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
    ) -> BuildInfo:
        """Build and cache the image ahead of time. Sandbox.create builds on
        its own, so this only warms the cache."""
        from ._client import SbxClient

        conn = client or SbxClient(base_url, api_key)
        return conn.build(list(self.steps), image=self.image, template=self.template, backend=backend)

    def _with(self, step: BuildStep) -> "Template":
        return Template(image=self.image, template=self.template, steps=(*self.steps, step))
