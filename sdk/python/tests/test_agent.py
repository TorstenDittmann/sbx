from __future__ import annotations

import json
import unittest
from typing import Any, List, Union

from sbx import Sandbox, SbxClient, SbxError

from .fakeserver import FakeServer, no_content, ok, sandbox_json, sse

PREVIEW = {
    "id": "prv_0011223344556677",
    "sandbox_id": "sbx_test",
    "port": 3000,
    "url": "http://example/v1/sandboxes/sbx_test/previews/prv_0011223344556677/",
}


def route(req):
    path, method = req.path, req.method
    if method == "POST" and path == "/v1/sandboxes":
        return ok(sandbox_json(), 201)
    if method == "POST" and path == "/v1/sandboxes/sbx_test/exec":
        assert req.json()["stdin"] == "hi"
        return ok({"exit_code": 0, "stdout": "hi", "stderr": "", "duration_ms": 1, "truncated": False})
    if method == "POST" and path == "/v1/sandboxes/sbx_test/processes":
        body = req.json()
        assert body["stdin_base64"] == "YWI=", body
        assert body["timeout_seconds"] == 0, body
        return ok({"id": "proc_1", "pid": 7, "cmd": ["sleep", "30"], "cwd": "/workspace", "tty": False, "status": "running", "exit_code": 0}, 201)
    if method == "GET" and path == "/v1/sandboxes/sbx_test/processes":
        return ok({"processes": [{"id": "proc_1", "pid": 7, "cmd": ["sleep"], "status": "running", "exit_code": 0, "tty": False}]})
    if method == "POST" and path == "/v1/sandboxes/sbx_test/processes/proc_1/stdin":
        assert req.body == b"more"
        return no_content()
    if method == "GET" and path == "/v1/sandboxes/sbx_test/processes/proc_1/stream":
        return sse(("stdout", {"data": "ok"}), ("exit", {"exit_code": 0, "duration_ms": 1, "truncated": False}))
    if method == "POST" and path == "/v1/sandboxes/sbx_test/processes/proc_1/signal":
        assert req.json()["signal"] == "KILL"
        return no_content()
    if method == "GET" and path.startswith("/v1/sandboxes/sbx_test/files/watch"):
        if "broken" in path:
            return sse(("ready", {"path": "/broken"}), ("error", {"code": "unsupported", "message": "no watch"}))
        return sse(("ready", {"path": "/workspace"}), ("change", {"type": "write", "path": "/workspace/a.txt"}))
    if method == "GET" and path.startswith("/v1/sandboxes/sbx_test/files/stat"):
        return ok({"name": "a.txt", "path": "/workspace/a.txt", "type": "file", "size": 1})
    if method == "POST" and path == "/v1/sandboxes/sbx_test/files/mkdir":
        return no_content()
    if method == "POST" and path == "/v1/sandboxes/sbx_test/terminals":
        return ok({"id": "term_1", "pid": 9, "cols": 100, "rows": 40}, 201)
    if method == "POST" and path == "/v1/sandboxes/sbx_test/terminals/term_1/resize":
        return no_content()
    if method == "POST" and path == "/v1/sandboxes/sbx_test/previews":
        return ok(PREVIEW, 201)
    if method == "GET" and path == "/v1/sandboxes/sbx_test/previews":
        return ok({"previews": [PREVIEW]})
    if method == "DELETE" and path == "/v1/sandboxes/sbx_test/previews/prv_0011223344556677":
        return no_content()
    if path == "/v1/sandboxes/sbx_test/previews/prv_0011223344556677/health":
        assert req.headers["authorization"] == "Bearer sbx_testkey"
        return 200, "text/plain", "preview-ok"
    if path == "/v1/sandboxes/sbx_test/previews/prv_0011223344556677/missing":
        return 404, "text/plain", "nope"
    return 500, "text/plain", path


class FakeSocket:
    def __init__(self, url: str) -> None:
        self.url = url
        self.sent: List[Union[str, bytes]] = []
        self.inbox: List[Union[str, bytes]] = []
        self.closed = False

    def send(self, data: Union[str, bytes]) -> None:
        self.sent.append(data)

    def recv(self, timeout: Any = None) -> Union[str, bytes]:
        if not self.inbox:
            raise EOFError
        return self.inbox.pop(0)

    def close(self) -> None:
        self.closed = True


class AgentTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = FakeServer(route).__enter__()
        self.addCleanup(self.server.__exit__)

    def test_processes_watch_and_files(self) -> None:
        box = Sandbox.create(image="alpine:3.20", base_url=self.server.url, api_key="sbx_testkey")
        self.assertEqual(box.exec(["cat"], stdin="hi").stdout, "hi")

        proc = box.start_process(["sleep", "30"], stdin=b"ab", timeout=0)
        self.assertEqual(proc.id, "proc_1")
        self.assertEqual(proc.status, "running")
        self.assertEqual(proc.cwd, "/workspace")
        self.assertEqual(len(box.list_processes()), 1)
        box.write_process_stdin(proc.id, "more")
        self.assertEqual([e.type for e in box.stream_process(proc.id)], ["stdout", "exit"])
        box.signal_process(proc.id, "KILL")

        changes = list(box.files.watch("/workspace", recursive=True))
        self.assertEqual(len(changes), 1)
        self.assertEqual((changes[0].type, changes[0].path), ("write", "/workspace/a.txt"))
        self.assertIn("recursive=1", self.server.last.path)
        with self.assertRaises(SbxError) as caught:
            list(box.files.watch("/broken"))
        self.assertEqual(caught.exception.code, "unsupported")

        self.assertEqual(box.files.stat("/workspace/a.txt").size, 1)
        box.files.mkdir("/workspace/sub", "755")
        self.assertEqual(self.server.last.json(), {"path": "/workspace/sub", "mode": "755"})
        box.files.mkdir("/workspace/sub2", 0o700)
        self.assertEqual(self.server.last.json(), {"path": "/workspace/sub2", "mode": "700"})

    def test_terminal(self) -> None:
        sockets: List[FakeSocket] = []

        def connect(url: str) -> FakeSocket:
            sockets.append(FakeSocket(url))
            return sockets[-1]

        client = SbxClient(self.server.url, "sbx_testkey", websocket_connect=connect)
        box = Sandbox.create("base", client=client)
        term = box.start_terminal(["sh"], cols=100, rows=40)
        self.assertEqual(self.server.last.json(), {"cmd": ["sh"], "cols": 100, "rows": 40})
        box.resize_terminal(term.id, 80, 24)
        self.assertEqual(self.server.last.json(), {"cols": 80, "rows": 24})

        with box.connect_terminal(term.id) as session:
            self.assertEqual(len(sockets), 1)
            socket = sockets[0]
            self.assertTrue(socket.url.startswith("ws://127.0.0.1:"))
            self.assertTrue(socket.url.endswith("/v1/sandboxes/sbx_test/terminals/term_1/ws?access_token=sbx_testkey"))

            output: List[bytes] = []
            exits: List[int] = []
            session.on_output(output.append)
            stop = session.on_exit(exits.append)
            session.resize(120, 40)
            session.send("echo\n")
            self.assertEqual(json.loads(socket.sent[0]), {"type": "resize", "cols": 120, "rows": 40})
            self.assertEqual(socket.sent[1], b"echo\n")

            socket.inbox += [b"o", "not json", b"k", json.dumps({"type": "exit", "exit_code": 2}), b"late"]
            self.assertEqual(list(session), [b"o", b"k"])
            self.assertEqual(b"".join(output), b"ok")
            self.assertEqual(exits, [2])
            self.assertEqual(session.exit_code, 2)
            self.assertIsNone(session.recv())
            stop()
        self.assertTrue(socket.closed)

    def test_terminal_closed_socket(self) -> None:
        client = SbxClient(self.server.url, "sbx_testkey", websocket_connect=FakeSocket)
        session = client.connect_terminal("sbx_test", "term_1")
        self.assertIsNone(session.recv())
        self.assertIsNone(session.exit_code)

    def test_previews(self) -> None:
        box = Sandbox.create("base", base_url=self.server.url, api_key="sbx_testkey")
        preview = box.create_preview(3000, public=True)
        self.assertEqual(self.server.last.json(), {"port": 3000, "public": True})
        self.assertEqual(preview.port, 3000)
        self.assertFalse(preview.public)
        self.assertEqual(len(box.list_previews()), 1)
        res = box.preview_fetch(preview.id, "health")
        self.assertTrue(res.ok)
        self.assertEqual(res.text(), "preview-ok")
        missing = box.preview_fetch(preview.id, "/missing")
        self.assertEqual(missing.status, 404)
        self.assertFalse(missing.ok)
        box.delete_preview(preview.id)
        self.assertEqual(self.server.last.method, "DELETE")


if __name__ == "__main__":
    unittest.main()
