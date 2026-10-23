from __future__ import annotations

import io
import os
import unittest
from datetime import timedelta
from unittest import mock

from sbx import NetworkPolicy, Sandbox, SbxClient, SbxError, VolumeMount, parse_duration

from .fakeserver import FakeServer, no_content, not_found, ok, sandbox_json, sse

TEMPLATE = {
    "name": "agent",
    "image": "node:22-bookworm-slim",
    "cpu": 1,
    "memory_mb": 512,
    "disk_mb": 1024,
    "network": "full",
    "setup": ["npm", "install"],
    "setup_timeout_seconds": 120,
    "warm": 2,
    "builtin": False,
    "created_at": "2026-10-19T12:00:00Z",
}
VOLUME = {"id": "vol_abc0123456789ab", "name": "cache", "backend": "docker", "created_at": "2026-10-19T12:00:00Z"}


class DurationTest(unittest.TestCase):
    def test_parse_duration(self) -> None:
        self.assertEqual(parse_duration(90), 90)
        self.assertEqual(parse_duration(1.9), 1)
        self.assertEqual(parse_duration("30s"), 30)
        self.assertEqual(parse_duration("5m"), 300)
        self.assertEqual(parse_duration("1h"), 3600)
        self.assertEqual(parse_duration("1.5m"), 90)
        self.assertEqual(parse_duration("500ms"), 1)
        self.assertEqual(parse_duration("0"), 0)
        self.assertEqual(parse_duration(timedelta(minutes=2)), 120)
        self.assertEqual(parse_duration(timedelta(milliseconds=10)), 1)
        for bad in ("soon", "-1s", "", -1, float("nan"), True):
            with self.assertRaises(ValueError, msg=repr(bad)):
                parse_duration(bad)  # type: ignore[arg-type]


class ConnectionTest(unittest.TestCase):
    def test_env_defaults(self) -> None:
        with mock.patch.dict(os.environ, {"SBX_URL": "http://sbx.test:9000/", "SBX_API_KEY": "sbx_env"}):
            client = SbxClient()
        self.assertEqual(client.base_url, "http://sbx.test:9000")
        self.assertEqual(client.api_key, "sbx_env")
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(SbxClient(api_key="k").base_url, "http://127.0.0.1:8080")
            with self.assertRaises(ValueError):
                SbxClient()


def route(req):
    path, method = req.path, req.method
    if path == "/v1/sandboxes" and method == "POST":
        return ok(sandbox_json(), 201)
    if path.startswith("/v1/sandboxes/sbx_test/exec?stream=1"):
        return sse(
            ("stdout", {"data": "stream"}),
            ("stderr", {"data": "warn"}),
            ("exit", {"exit_code": 3, "duration_ms": 2, "truncated": False}),
        )
    if path == "/v1/sandboxes/sbx_test/exec":
        return ok({"exit_code": 0, "stdout": "hi\n", "stderr": "", "duration_ms": 4, "truncated": False})
    if method == "PUT" and path.startswith("/v1/sandboxes/sbx_test/files/content"):
        return no_content()
    if method == "GET" and path.startswith("/v1/sandboxes/sbx_test/files/content"):
        return 200, "application/octet-stream", b"\x00\x01hello"
    if method == "GET" and path.startswith("/v1/sandboxes/sbx_test/files?"):
        return ok({"path": "/workspace", "entries": [{"name": "a.txt", "path": "/workspace/a.txt", "type": "file", "size": 5}]})
    if method == "DELETE" and path.startswith("/v1/sandboxes/sbx_test/files?"):
        return no_content()
    if method == "POST" and path.startswith("/v1/sandboxes/sbx_test/files/archive"):
        return no_content()
    if method == "GET" and path.startswith("/v1/sandboxes/sbx_test/files/archive"):
        return 200, "application/gzip", b"\x1f\x8b" + b"x" * 100_000
    if method == "GET" and path.startswith("/v1/sandboxes?"):
        return ok({"sandboxes": [sandbox_json()]})
    if method == "GET" and path == "/v1/sandboxes/sbx_test":
        return ok(sandbox_json(mounts=[{"volume_id": "vol_1", "path": "/cache"}]))
    if method == "DELETE" and path == "/v1/sandboxes/sbx_test":
        return ok(sandbox_json(status="stopped"))
    if method == "POST" and path == "/v1/sandboxes/sbx_test/timeout":
        return ok(sandbox_json(expires_at="2026-10-18T21:00:00Z"))
    if method == "POST" and path in ("/v1/sandboxes/sbx_test/pause", "/v1/sandboxes/sbx_test/resume"):
        return ok(sandbox_json(status="paused" if path.endswith("pause") else "running"))
    if method == "PUT" and path == "/v1/sandboxes/sbx_test/secrets":
        return ok(sandbox_json(secret_keys=list(req.json()["secrets"])))
    if method == "GET" and path == "/v1/sandboxes/missing":
        return not_found()
    if method == "GET" and path == "/v1/sandboxes/plain":
        return 502, "text/plain", "bad gateway"
    if method == "POST" and path == "/v1/templates":
        return ok(TEMPLATE, 201)
    if method == "GET" and path == "/v1/templates":
        return ok({"templates": [TEMPLATE]})
    if method == "DELETE" and path == "/v1/templates/agent":
        return no_content()
    if method == "POST" and path == "/v1/volumes":
        return ok(VOLUME, 201)
    if method == "GET" and path == "/v1/volumes":
        return ok({"volumes": [VOLUME]})
    if method == "GET" and path == "/v1/volumes/vol_abc0123456789ab":
        return ok(VOLUME)
    if method == "DELETE" and path == "/v1/volumes/vol_abc0123456789ab":
        return no_content()
    return 500, "text/plain", "no route " + path


class ClientTest(unittest.TestCase):
    def setUp(self) -> None:
        self.server = FakeServer(route).__enter__()
        self.addCleanup(self.server.__exit__)
        self.client = SbxClient(self.server.url, "sbx_testkey")

    def test_create_maps_options(self) -> None:
        created = self.client.create_sandbox("base", ttl="5m", memory_mb=128, labels={"user": "1"})
        self.assertEqual(created.id, "sbx_test")
        self.assertEqual(created.memory_mb, 64)
        self.assertEqual(created.node, "local")
        self.assertEqual(self.server.last.headers["authorization"], "Bearer sbx_testkey")
        self.assertEqual(self.server.last.headers["content-type"], "application/json")
        self.assertEqual(
            self.server.last.json(), {"template": "base", "ttl_seconds": 300, "memory_mb": 128, "labels": {"user": "1"}}
        )

        self.client.create_sandbox(
            image="alpine:3.20",
            idle_timeout=timedelta(minutes=1),
            network=NetworkPolicy(mode="allow", allow=[], deny=["example.net"], egress_kbps=1000, max_connections=8),
            mounts=[VolumeMount("vol_abc0123456789ab", "/cache"), {"volume_id": "vol_2", "path": "/data"}],
            secrets={"TOKEN": "sekret"},
            backend="gvisor",
        )
        self.assertEqual(
            self.server.last.json(),
            {
                "image": "alpine:3.20",
                "idle_timeout_seconds": 60,
                "network": {"mode": "allow", "allow": [], "deny": ["example.net"], "egress_kbps": 1000, "max_connections": 8},
                "mounts": [{"volume_id": "vol_abc0123456789ab", "path": "/cache"}, {"volume_id": "vol_2", "path": "/data"}],
                "secrets": {"TOKEN": "sekret"},
                "backend": "gvisor",
            },
        )

        self.client.create_sandbox(network={"mode": "full", "egress_kbps": 10})
        self.assertEqual(self.server.last.json(), {"network": {"mode": "full", "egress_kbps": 10}})

    def test_exec(self) -> None:
        result = self.client.exec("sbx_test", "echo hi", timeout="20s", cwd="/tmp", env={"A": "1"})
        self.assertEqual(result.stdout, "hi\n")
        self.assertEqual(result.exit_code, 0)
        body = self.server.last.json()
        self.assertEqual(body["cmd"], ["sh", "-lc", "echo hi"])
        self.assertEqual(body["timeout_seconds"], 20)
        self.assertEqual(body["cwd"], "/tmp")
        self.assertEqual(body["env"], {"A": "1"})

        self.client.exec("sbx_test", ["wc", "-c"], stdin=b"\x01\x02\x03")
        self.assertEqual(self.server.last.json(), {"cmd": ["wc", "-c"], "stdin_base64": "AQID"})
        with self.assertRaises(ValueError):
            self.client.exec("sbx_test", [])

        events = list(self.client.exec_stream("sbx_test", ["echo", "stream"]))
        self.assertEqual([e.type for e in events], ["stdout", "stderr", "exit"])
        self.assertEqual(events[0].data, "stream")
        self.assertEqual(events[2].exit_code, 3)
        self.assertEqual(events[2].duration_ms, 2)

    def test_files(self) -> None:
        self.client.write_file("sbx_test", "/workspace/a b.txt", "hello", 0o644)
        self.assertIn("path=%2Fworkspace%2Fa+b.txt", self.server.last.path)
        self.assertIn("mode=644", self.server.last.path)
        self.assertEqual(self.server.last.body, b"hello")
        self.assertEqual(self.server.last.headers["content-type"], "application/octet-stream")
        self.client.write_file("sbx_test", "/workspace/run.sh", b"#!/bin/sh", "0o755")
        self.assertIn("mode=755", self.server.last.path)

        self.assertEqual(self.client.read_file("sbx_test", "/workspace/a.txt"), b"\x00\x01hello")
        listing = self.client.list_files("sbx_test", "/workspace")
        self.assertEqual(listing.entries[0].name, "a.txt")
        self.client.remove_file("sbx_test", "/workspace/a.txt")
        self.assertEqual(self.server.last.method, "DELETE")

        box = Sandbox.connect("sbx_test", client=self.client)
        self.assertEqual(box.mounts, [VolumeMount("vol_1", "/cache")])
        self.assertEqual(box.files.read_text("/workspace/a.txt"), "\x00\x01hello")

    def test_archives_stream(self) -> None:
        self.client.upload_archive("sbx_test", "/workspace", b"tarbytes")
        self.assertEqual(self.server.last.body, b"tarbytes")
        self.client.upload_archive("sbx_test", "/workspace", io.BytesIO(b"from-a-file"))
        self.assertEqual(self.server.last.body, b"from-a-file")
        self.assertEqual(self.server.last.headers.get("transfer-encoding"), "chunked")
        self.client.upload_archive("sbx_test", "/workspace", iter([b"one-", b"two"]))
        self.assertEqual(self.server.last.body, b"one-two")

        whole = self.client.download_archive("sbx_test", "/workspace")
        assert whole is not None
        self.assertEqual(len(whole), 100_002)
        sink = io.BytesIO()
        self.assertIsNone(self.client.download_archive("sbx_test", "/workspace", sink))
        self.assertEqual(sink.getvalue(), whole)

    def test_errors(self) -> None:
        with self.assertRaises(SbxError) as caught:
            self.client.get_sandbox("missing")
        self.assertEqual(caught.exception.status, 404)
        self.assertEqual(caught.exception.code, "not_found")
        self.assertEqual(str(caught.exception), "gone")
        with self.assertRaises(SbxError) as caught:
            self.client.get_sandbox("plain")
        self.assertEqual(caught.exception.status, 502)
        self.assertEqual(caught.exception.code, "driver")

    def test_list_and_lifecycle(self) -> None:
        found = self.client.list_sandboxes(template="base", q="alp", labels={"user": "1"}, status="running")
        self.assertEqual(len(found), 1)
        path = self.server.last.path
        for part in ("template=base", "q=alp", "label=user%3A1", "status=running"):
            self.assertIn(part, path)

        box = Sandbox.connect("sbx_test", base_url=self.server.url, api_key="sbx_testkey")
        self.assertEqual(box.id, "sbx_test")
        box.set_ttl("1h")
        self.assertEqual(self.server.last.json(), {"ttl_seconds": 3600})
        self.assertEqual(box.expires_at, "2026-10-18T21:00:00Z")
        box.pause()
        self.assertEqual(box.status, "paused")
        box.resume()
        self.assertEqual(box.status, "running")
        info = box.set_secrets({"TOKEN": "next"})
        self.assertEqual(info.secret_keys, ["TOKEN"])
        self.assertEqual(box.secret_keys, ["TOKEN"])
        box.kill()
        self.assertEqual(box.status, "stopped")

    def test_context_manager_kills(self) -> None:
        with Sandbox.create("base", client=self.client) as box:
            self.assertEqual(box.status, "running")
        self.assertEqual(self.server.last.method, "DELETE")
        self.assertEqual(box.status, "stopped")

    def test_volumes(self) -> None:
        volume = self.client.create_volume("cache")
        self.assertEqual(volume.id, "vol_abc0123456789ab")
        self.assertEqual(self.server.last.json(), {"name": "cache"})
        self.client.ensure_volume("cache", "docker")
        self.assertEqual(self.server.last.json(), {"name": "cache", "backend": "docker", "if_exists": "reuse"})
        self.assertEqual(len(self.client.list_volumes()), 1)
        self.assertEqual(self.client.get_volume(volume.id).backend, "docker")
        self.assertIsNone(self.client.delete_volume(volume.id))
        self.assertEqual((self.server.last.method, self.server.last.path), ("DELETE", "/v1/volumes/vol_abc0123456789ab"))

    def test_templates(self) -> None:
        template = self.client.create_template(
            "agent", "node:22-bookworm-slim", network="full", setup=["npm", "install"], setup_timeout="2m", warm=2
        )
        self.assertEqual(template.warm, 2)
        self.assertEqual(template.setup_timeout_seconds, 120)
        self.assertEqual(
            self.server.last.json(),
            {
                "name": "agent",
                "image": "node:22-bookworm-slim",
                "network": "full",
                "setup": ["npm", "install"],
                "setup_timeout_seconds": 120,
                "warm": 2,
            },
        )
        self.assertEqual(self.client.list_templates()[0].setup[0], "npm")
        self.client.delete_template("agent")
        self.assertEqual(self.server.last.method, "DELETE")


class KeysAndBackendsTest(unittest.TestCase):
    def test_keys(self) -> None:
        key = {
            "id": "key_1",
            "name": "ci",
            "prefix": "sbx_ab",
            "scopes": ["sandbox"],
            "created_at": "2026-10-20T00:00:00Z",
            "expires_at": "2027-01-01T00:00:00Z",
            "network": {"mode": "allow", "max_mode": "allow", "allow": ["pypi.org"]},
        }

        def handler(req):
            if req.method == "POST" and req.path == "/v1/keys":
                return ok({**key, "key": "sbx_secret"}, 201)
            if req.method == "GET" and req.path == "/v1/keys":
                return ok({"keys": [key]})
            if req.method == "GET" and req.path == "/v1/keys/key_1":
                return ok({**key, "revoked_at": "2026-10-21T00:00:00Z"})
            if req.method == "DELETE" and req.path == "/v1/keys/key_1":
                return no_content()
            return not_found()

        from datetime import datetime, timezone

        from sbx import KeyNetworkPolicy

        with FakeServer(handler) as server:
            client = SbxClient(server.url, "sbx_test")
            created = client.create_key(
                "ci",
                scopes=["sandbox"],
                expires_at=datetime(2027, 1, 1, tzinfo=timezone.utc),
                network=KeyNetworkPolicy(mode="allow", max_mode="allow", allow=["pypi.org"]),
            )
            self.assertEqual(created.key, "sbx_secret")
            self.assertEqual(created.network.max_mode, "allow")
            self.assertEqual(
                server.last.json(),
                {
                    "name": "ci",
                    "scopes": ["sandbox"],
                    "network": {"mode": "allow", "allow": ["pypi.org"], "max_mode": "allow"},
                    "expires_at": "2027-01-01T00:00:00+00:00",
                },
            )
            client.create_key("dict", network={"mode": "none", "max_mode": "full"})
            self.assertEqual(server.last.json()["network"], {"mode": "none", "max_mode": "full"})
            keys = client.list_keys()
            self.assertEqual(keys[0].expires_at, "2027-01-01T00:00:00Z")
            client.revoke_key("key_1")
            self.assertEqual(client.get_key("key_1").revoked_at, "2026-10-21T00:00:00Z")
            calls = [f"{r.method} {r.path}" for r in server.requests[2:]]
            self.assertEqual(calls, ["GET /v1/keys", "DELETE /v1/keys/key_1", "GET /v1/keys/key_1"])

    def test_backends(self) -> None:
        backend = {
            "name": "docker",
            "available": True,
            "isolation": "container",
            "pause": True,
            "snapshot": False,
            "exec": True,
            "pty": True,
            "files": True,
            "watch": True,
            "preview": True,
            "networks": ["none", "full"],
            "requirements": ["docker"],
        }
        with FakeServer(lambda req: ok({"backends": [backend]})) as server:
            rows = SbxClient(server.url, "sbx_test").list_backends()
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0].name, "docker")
        self.assertTrue(rows[0].available)
        self.assertEqual(rows[0].networks, ["none", "full"])


if __name__ == "__main__":
    unittest.main()
