from __future__ import annotations

import unittest

from sbx import Sandbox, SbxClient, Template

from .fakeserver import FakeServer, ok, sandbox_json


class TemplateTest(unittest.TestCase):
    def test_immutable_and_branches(self) -> None:
        base = Sandbox.template("node:22").with_packages("ffmpeg", "git").with_env({"NODE_ENV": "production"}).workdir("/app")
        install = base.run("npm install")
        test = base.run("npm test")
        self.assertEqual(len(base.steps), 3)
        self.assertEqual(install.steps[-1], {"run": "npm install"})
        self.assertEqual(test.steps[-1], {"run": "npm test"})
        self.assertEqual(install.image, "node:22")
        self.assertIsInstance(base.steps, tuple)
        with self.assertRaises(AttributeError):
            base.image = "other"  # type: ignore[misc]
        self.assertEqual(Sandbox.template().template, "base")
        self.assertEqual(Sandbox.template(template="python").template, "python")
        self.assertEqual(Sandbox.template(image="alpine").image, "alpine")
        self.assertEqual(base.with_env(A="1").steps[-1], {"env": {"A": "1"}})
        self.assertEqual(Template(image="x").run("a"), Template(image="x").run("a"))
        with self.assertRaises(ValueError):
            base.with_packages()
        with self.assertRaises(ValueError):
            base.run("")
        with self.assertRaises(ValueError):
            base.with_env({})

    def test_create_and_build_send_steps(self) -> None:
        def handler(req):
            if req.path == "/v1/builds":
                body = req.json()
                return ok({"id": "abc", "image": "sbx-template:abc", "base": "node:22", "steps": body["steps"], "cached": False, "duration_ms": 1200}, 201)
            return ok(sandbox_json(image="sbx-template:abc"), 201)

        with FakeServer(handler) as server:
            client = SbxClient(server.url, "sbx_testkey")
            tpl = Sandbox.template("node:22").with_packages("git").workdir("/app").run("npm install")
            built = tpl.build(client=client)
            self.assertEqual(built.image, "sbx-template:abc")
            self.assertEqual(built.duration_ms, 1200)
            self.assertEqual(server.requests[0].path, "/v1/builds")
            self.assertEqual(
                server.requests[0].json(),
                {"image": "node:22", "steps": [{"packages": ["git"]}, {"workdir": "/app"}, {"run": "npm install"}]},
            )

            box = Sandbox.create(tpl, client=client, ttl="5m", env={"PORT": "3000"})
            self.assertEqual(box.image, "sbx-template:abc")
            self.assertEqual(
                server.requests[1].json(),
                {
                    "image": "node:22",
                    "steps": [{"packages": ["git"]}, {"workdir": "/app"}, {"run": "npm install"}],
                    "ttl_seconds": 300,
                    "env": {"PORT": "3000"},
                },
            )

            Sandbox.create(Template(template="python").run("pip install requests"), client=client, steps=[{"run": "echo extra"}])
            self.assertEqual(
                server.requests[2].json(),
                {"template": "python", "steps": [{"run": "pip install requests"}, {"run": "echo extra"}]},
            )

            client.build([{"run": "pip install numpy"}], image="python:3.12-slim", backend="gvisor")
            self.assertEqual(
                server.last.json(), {"steps": [{"run": "pip install numpy"}], "image": "python:3.12-slim", "backend": "gvisor"}
            )


if __name__ == "__main__":
    unittest.main()
