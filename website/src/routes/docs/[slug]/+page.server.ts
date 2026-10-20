import { docPage } from "../../../lib/server/docpage";
import { pages } from "../../../lib/server/docs";
import type { EntryGenerator } from "./$types";

// The first page is served at /docs/, so it gets no slug route.
export const entries: EntryGenerator = () => pages.slice(1).map((page) => ({ slug: page.slug }));

export function load({ params }) {
	return docPage(params.slug);
}
