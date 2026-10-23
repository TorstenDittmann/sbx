from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, List, Optional, Tuple

# A route returns (status, content_type, body). body may be bytes, str, or a
# JSON-able value.
Route = Callable[["Request"], Tuple[int, str, Any]]


class Request:
    def __init__(self, method: str, path: str, headers: Dict[str, str], body: bytes) -> None:
        self.method = method
        self.path = path
        self.headers = headers
        self.body = body

    def json(self) -> Any:
        return json.loads(self.body or b"{}")


class FakeServer:
    """A local HTTP server for tests. handler gets each Request and returns
    (status, content_type, body)."""

    def __init__(self, handler: Route) -> None:
        self.requests: List[Request] = []
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args: Any) -> None:
                pass

            def _serve(self) -> None:
                body = b""
                if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
                    while True:
                        size = int(self.rfile.readline().strip(), 16)
                        if size == 0:
                            self.rfile.readline()
                            break
                        body += self.rfile.read(size)
                        self.rfile.readline()
                else:
                    length = int(self.headers.get("Content-Length") or 0)
                    body = self.rfile.read(length) if length else b""
                req = Request(self.command, self.path, {k.lower(): v for k, v in self.headers.items()}, body)
                outer.requests.append(req)
                status, content_type, payload = handler(req)
                if isinstance(payload, bytes):
                    data = payload
                elif isinstance(payload, str):
                    data = payload.encode("utf-8")
                elif payload is None:
                    data = b""
                else:
                    data = json.dumps(payload).encode("utf-8")
                self.send_response(status)
                if content_type:
                    self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_PUT = do_DELETE = _serve

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.httpd.server_address[1]}"

    @property
    def last(self) -> Request:
        return self.requests[-1]

    def __enter__(self) -> "FakeServer":
        self.thread.start()
        return self

    def __exit__(self, *exc: Any) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()


def ok(body: Any, status: int = 200) -> Tuple[int, str, Any]:
    return status, "application/json", body


def no_content() -> Tuple[int, str, Any]:
    return 204, "", None


def sse(*events: Tuple[str, Any]) -> Tuple[int, str, Any]:
    text = "".join(f"event: {name}\ndata: {json.dumps(data)}\n\n" for name, data in events)
    return 200, "text/event-stream", text


def sandbox_json(**extra: Any) -> Dict[str, Any]:
    raw = {
        "id": "sbx_test",
        "status": "running",
        "backend": "docker",
        "template": "base",
        "image": "alpine:3.20",
        "cpu": 0.5,
        "memory_mb": 64,
        "disk_mb": 32,
        "pids": 64,
        "network": "none",
        "expires_at": "2026-10-18T20:00:00Z",
        "created_at": "2026-10-18T19:55:00Z",
        "node": "local",
    }
    raw.update(extra)
    return raw


def not_found(req: Optional[Request] = None) -> Tuple[int, str, Any]:
    return 404, "application/json", {"error": {"code": "not_found", "message": "gone"}}
