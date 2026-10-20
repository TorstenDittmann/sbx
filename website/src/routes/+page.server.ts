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

const install = `curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
sbx serve

npm install @sbx.torsten.work/sbx`;

export function load() {
	return {
		sdk: highlight(sdk, "typescript"),
		install: highlight(install, "shellscript"),
	};
}
