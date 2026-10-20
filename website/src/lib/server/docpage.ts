import { error } from "@sveltejs/kit";
import { pages, render } from "./docs";

/**
 * Loads one docs page with its neighbours for the previous and next links.
 */
export function docPage(slug: string) {
	const index = pages.findIndex((page) => page.slug === slug);
	if (index < 0) error(404, "Not found");
	const { html, headings } = render(slug);
	return {
		page: pages[index],
		html,
		headings,
		prev: pages[index - 1] ?? null,
		next: pages[index + 1] ?? null,
	};
}
