from __future__ import annotations

import os
import unittest

from sbx import Sandbox


@unittest.skipUnless(os.environ.get("SBX_LIVE") == "1", "set SBX_LIVE=1 with a running server")
class LiveTest(unittest.TestCase):
    def test_quickstart(self) -> None:
        with Sandbox.create(template="base", ttl=180, env={"FOO": "bar"}) as box:
            result = box.exec("echo $FOO", timeout=20)
            self.assertEqual(result.exit_code, 0)
            self.assertEqual(result.stdout, "bar\n")
            box.files.write("/workspace/hello.txt", "hi\n")
            self.assertEqual(box.files.read_text("/workspace/hello.txt"), "hi\n")


if __name__ == "__main__":
    unittest.main()
