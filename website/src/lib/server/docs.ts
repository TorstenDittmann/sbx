import { Marked, type Tokens } from "marked";
import { highlight } from "./highlight";

export type DocLink = { slug: string; title: string; description: string };
export type DocSection = { title: string; pages: DocLink[] };
export type Heading = { id: string; text: string };

// The sidebar order. The first page is served at /docs/.
export const sections: DocSection[] = [
	{
		title: "Getting started",
		pages: [
			{ slug: "introduction", title: "Introduction", description: "What sbx is and how the pieces fit together." },
			{ slug: "quickstart", title: "Quickstart", description: "Start the server, create a sandbox, and run a command." },
			{ slug: "installation", title: "Installation", description: "Install the binaries, the container image, or an SDK." },
		],
	},
	{
		title: "Concepts",
		pages: [
			{ slug: "sandboxes", title: "Sandboxes", description: "Lifecycle, resources, TTLs, labels, secrets, and search." },
			{ slug: "api-keys", title: "API keys", description: "Admin and sandbox scopes, quotas, expiry, and revocation." },
			{ slug: "backends", title: "Backends", description: "Docker, gVisor, Kata, and Firecracker, and how to pick one." },
			{ slug: "network", title: "Network", description: "Network modes, allow and deny lists, and what stays blocked." },
			{ slug: "templates", title: "Templates and builds", description: "Built-in templates, warm pools, and cached build steps." },
			{ slug: "volumes", title: "Volumes", description: "Named volumes that outlive a sandbox." },
			{ slug: "previews", title: "Previews", description: "Publish a port from inside a sandbox, privately or publicly." },
			{ slug: "agent", title: "sbx-agent", description: "Processes, terminals, file watches, and archives." },
		],
	},
	{
		title: "Self-hosting",
		pages: [
			{ slug: "configuration", title: "Configuration", description: "Every setting in sbx.yaml, its environment variable, and its default." },
			{ slug: "deployment", title: "Deployment", description: "Run sbx with Docker, systemd, and a reverse proxy." },
			{ slug: "dashboard", title: "Dashboard", description: "The web UI at /ui/: sandboxes, terminals, logs, and keys." },
			{ slug: "cli", title: "CLI", description: "Run the server, manage keys, and create, exec into, copy to, and remove sandboxes." },
		],
	},
	{
		title: "Reference",
		pages: [
			{ slug: "api", title: "HTTP API", description: "Every route, its body, and its errors." },
			{ slug: "sdk", title: "TypeScript SDK", description: "The @sbx.torsten.work/sbx client for Node and Bun." },
			{ slug: "python-sdk", title: "Python SDK", description: "The sbx-sdk client for Python 3.9+." },
		],
	},
];

export const pages = sections.flatMap((section) => section.pages);

const sources = import.meta.glob<string>("../../docs/*.md", { query: "?raw", import: "default", eager: true });

function source(slug: string): string {
	const text = sources[`../../docs/${slug}.md`];
	if (text == null) throw new Error(`docs page ${slug} has no markdown file`);
	return text;
}

// Code fence names in the markdown, mapped to the languages Shiki loads.
const languages: Record<string, string> = {
	ts: "typescript",
	typescript: "typescript",
	js: "typescript",
	bash: "shellscript",
	sh: "shellscript",
	shell: "shellscript",
	yaml: "yaml",
	yml: "yaml",
	json: "json",
	py: "python",
	python: "python",
};

function slugify(text: string): string {
	return text
		.toLowerCase()
		.replace(/<[^>]+>/g, "")
		.replace(/&[a-z]+;/g, "")
		.replace(/[^a-z0-9\s-]/g, "")
		.trim()
		.replace(/\s+/g, "-");
}

function plain(html: string): string {
	return html
		.replace(/<[^>]+>/g, "")
		.replaceAll("&lt;", "<")
		.replaceAll("&gt;", ">")
		.replaceAll("&quot;", '"')
		.replaceAll("&#39;", "'")
		.replaceAll("&amp;", "&");
}

/**
 * Renders one docs page. h2 headings become the on-page outline.
 */
export function render(slug: string): { html: string; headings: Heading[] } {
	const headings: Heading[] = [];
	const used = new Map<string, number>();
	const marked = new Marked({
		gfm: true,
		renderer: {
			code({ text, lang }: Tokens.Code) {
				return highlight(text, languages[lang ?? ""] ?? "text");
			},
			heading({ tokens, depth }: Tokens.Heading) {
				const inner = this.parser.parseInline(tokens);
				if (depth === 1) return `<h1>${inner}</h1>\n`;
				let id = slugify(inner) || "section";
				const seen = used.get(id) ?? 0;
				used.set(id, seen + 1);
				if (seen > 0) id = `${id}-${seen}`;
				if (depth === 2) headings.push({ id, text: plain(inner) });
				return `<h${depth} id="${id}"><a class="anchor" href="#${id}">${inner}</a></h${depth}>\n`;
			},
			table(token: Tokens.Table) {
				const cell = (c: Tokens.TableCell) => this.parser.parseInline(c.tokens);
				const head = token.header.map((c) => `<th scope="col">${cell(c)}</th>`).join("");
				const body = token.rows.map((row) => `<tr>${row.map((c) => `<td>${cell(c)}</td>`).join("")}</tr>`).join("\n");
				return `<div class="table"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>\n`;
			},
		},
	});
	// The page title comes from the sidebar, so a leading h1 in the file is dropped.
	const markdown = source(slug).replace(/^# .*\n+/, "");
	const html = marked.parse(markdown, { async: false });
	return { html, headings };
}
