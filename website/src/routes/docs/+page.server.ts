import { docPage } from "../../lib/server/docpage";
import { pages } from "../../lib/server/docs";

export function load() {
	return docPage(pages[0].slug);
}
