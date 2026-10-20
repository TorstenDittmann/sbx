import { createCssVariablesTheme, createHighlighter } from "shiki";

export const site = {
	title: "sbx",
	description:
		"Self-host sandboxes. One Go binary, and the same API on Docker, gVisor, Kata, or Firecracker.",
	url: "https://sbx.torsten.work",
	repo: "https://github.com/TorstenDittmann/sbx",
};

// Token colors are CSS variables defined in app.css, so code follows the site theme.
const theme = createCssVariablesTheme({ name: "sbx", variablePrefix: "--code-" });
// Bare shell arguments read as plain text; otherwise whole command lines turn string-colored.
theme.tokenColors!.push({ scope: "string.unquoted.argument", settings: { foreground: "var(--code-foreground)" } });

const highlighter = await createHighlighter({
	themes: [theme],
	langs: ["typescript", "shellscript", "text"],
});

function escapeHtml(text: string): string {
	return text.replaceAll("&", "&amp;").replaceAll('"', "&quot;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

/**
 * Highlights a code block and wraps it with a copy button.
 */
export function highlight(code: string, lang: string): string {
	let language = lang;
	if (!highlighter.getLoadedLanguages().includes(language)) language = "text";
	const html = highlighter.codeToHtml(code, { lang: language, theme: "sbx" });
	return `<div class="codeblock">${html}<button type="button" class="copy" data-copy="${escapeHtml(code)}" aria-label="Copy code"></button></div>`;
}
