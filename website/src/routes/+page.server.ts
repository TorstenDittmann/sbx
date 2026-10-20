import { highlight } from "../lib/server/highlight";

const sdk = `import { Sandbox } from "@sbx.torsten.work/sbx"

const box = await Sandbox.create({
  template: "python",
  ttl: "5m",
})

await box.files.write("/workspace/main.py", "print(2 + 2)\\n")
const result = await box.exec(["python", "main.py"])
console.log(result.stdout)
await box.kill()`;

const quickstart = `go build -o sbx ./cmd/sbx
go build -o sbx-agent ./cmd/sbx-agent
./sbx serve

curl -s http://127.0.0.1:8080/v1/sandboxes \\
  -H "Authorization: Bearer $SBX_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"template":"base"}'

curl -s http://127.0.0.1:8080/v1/sandboxes/$ID/exec \\
  -H "Authorization: Bearer $SBX_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"cmd":["echo","hello"]}'`;

const install = `curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx version

docker run --rm -p 8080:8080 \\
  -v /var/run/docker.sock:/var/run/docker.sock \\
  -v sbx-data:/data \\
  ghcr.io/torstendittmann/sbx:latest

npm install @sbx.torsten.work/sbx
bun add @sbx.torsten.work/sbx`;

const client = `import { Sandbox, SbxClient } from "@sbx.torsten.work/sbx"

const client = new SbxClient()
const backends = await client.listBackends()

const volume = await client.createVolume("project", "docker")
const box = await Sandbox.create({
  template: "python",
  ttl: "5m",
  network: { mode: "allow", allow: ["pypi.org"] },
  secrets: { TOKEN: "kept-off-the-response" },
  mounts: [{ volumeId: volume.id, path: "/workspace" }],
})

console.log(box.secretKeys, backends.map((item) => item.name))
const found = await client.listSandboxes({ template: "python", q: box.id })
await box.files.write("/workspace/main.py", "print(2 + 2)\\n")
const result = await box.exec(["python", "main.py"])
console.log(result.stdout, found.length)
await box.kill()`;

export function load() {
	return {
		sdk: highlight(sdk, "typescript"),
		quickstart: highlight(quickstart, "shellscript"),
		install: highlight(install, "shellscript"),
		client: highlight(client, "typescript"),
	};
}
