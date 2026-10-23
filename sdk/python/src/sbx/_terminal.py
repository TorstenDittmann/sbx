from __future__ import annotations

import json
from typing import Any, Callable, Iterator, List, Optional, Union


def connect_websocket(url: str) -> Any:
    try:
        from websockets.sync.client import connect
    except ImportError as err:  # pragma: no cover - depends on the environment
        raise RuntimeError(
            'terminals need the websockets package: pip install "sbx-sdk[terminal]"'
        ) from err
    return connect(url, max_size=None)


def _is_closed(err: BaseException) -> bool:
    if isinstance(err, EOFError):
        return True
    try:
        from websockets.exceptions import ConnectionClosed
    except ImportError:  # pragma: no cover
        return False
    return isinstance(err, ConnectionClosed)


class TerminalSession:
    """A WebSocket attached to a PTY. Binary frames are output bytes. Text
    frames are JSON control messages.

    Read output with recv() or by iterating, which stops when the process
    exits or the socket closes. on_output and on_exit callbacks run on the
    thread that reads.
    """

    def __init__(self, id: str, socket: Any) -> None:
        self.id = id
        self.socket = socket
        self.exit_code: Optional[int] = None
        self._output: List[Callable[[bytes], None]] = []
        self._exit: List[Callable[[int], None]] = []

    def send(self, data: Union[str, bytes]) -> None:
        self.socket.send(data.encode("utf-8") if isinstance(data, str) else bytes(data))

    def resize(self, cols: int, rows: int) -> None:
        self.socket.send(json.dumps({"type": "resize", "cols": cols, "rows": rows}))

    def recv(self, timeout: Optional[float] = None) -> Optional[bytes]:
        """Return the next output chunk, or None once the process exited or
        the socket closed. Raises TimeoutError when timeout passes first."""
        while self.exit_code is None:
            try:
                message = self.socket.recv(timeout) if timeout is not None else self.socket.recv()
            except Exception as err:
                if _is_closed(err):
                    return None
                raise
            if isinstance(message, str):
                try:
                    control = json.loads(message)
                except ValueError:
                    continue
                if isinstance(control, dict) and control.get("type") == "exit":
                    self.exit_code = int(control.get("exit_code", 0))
                    for on_exit in list(self._exit):
                        on_exit(self.exit_code)
                continue
            chunk = bytes(message)
            for on_output in list(self._output):
                on_output(chunk)
            return chunk
        return None

    def __iter__(self) -> Iterator[bytes]:
        while True:
            chunk = self.recv()
            if chunk is None:
                return
            yield chunk

    def on_output(self, fn: Callable[[bytes], None]) -> Callable[[], None]:
        self._output.append(fn)
        return lambda: self._output.remove(fn) if fn in self._output else None

    def on_exit(self, fn: Callable[[int], None]) -> Callable[[], None]:
        self._exit.append(fn)
        return lambda: self._exit.remove(fn) if fn in self._exit else None

    def close(self) -> None:
        self.socket.close()

    def __enter__(self) -> "TerminalSession":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()
