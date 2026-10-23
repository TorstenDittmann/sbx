import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sdk", "python", "src"))

from sbx import Sandbox  # noqa: E402

with Sandbox.create(template="base", ttl="5m") as sandbox:
    print(f"created {sandbox.id} ({sandbox.image}, network {sandbox.network})")

    result = sandbox.exec("echo hello from sbx")
    print(result.stdout.strip())

    sandbox.files.write("/workspace/hello.txt", "hi from the sdk\n")
    print(sandbox.files.read_text("/workspace/hello.txt"))

print(f"status {sandbox.status}")
