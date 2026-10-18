import { Sandbox } from "../sdk/typescript/src/index.ts"

const sandbox = await Sandbox.create({ template: "base", ttl: "5m" })
console.log(`created ${sandbox.id} (${sandbox.image}, network ${sandbox.network})`)

const result = await sandbox.exec("echo hello from sbx")
console.log(result.stdout.trim())

await sandbox.files.write("/workspace/hello.txt", "hi from the sdk\n")
console.log(await sandbox.files.readText("/workspace/hello.txt"))

const stopped = await sandbox.kill()
console.log(`status ${stopped.status}`)
