import assert from "node:assert/strict"
import test from "node:test"

import { Sandbox } from "../src/index.ts"

test("live quickstart", { skip: process.env.SBX_LIVE !== "1" }, async () => {
  const sandbox = await Sandbox.create({ template: "base", ttl: 180, env: { FOO: "bar" } })
  try {
    const result = await sandbox.exec("echo $FOO", { timeout: 20 })
    assert.equal(result.exitCode, 0)
    assert.equal(result.stdout, "bar\n")
    await sandbox.files.write("/workspace/hello.txt", "hi\n")
    assert.equal(await sandbox.files.readText("/workspace/hello.txt"), "hi\n")
  } finally {
    await sandbox.kill()
  }
})
