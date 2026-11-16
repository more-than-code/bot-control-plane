import { requireUser } from '$lib/server/guard';
import { changelogEntries } from '$lib/server/changelog';
import type { PageServerLoad } from './$types';

/** "What's new" (spec-update-awareness §2.4): the releases in CHANGELOG.md, newest first. Signed-in users only. */
export const load: PageServerLoad = (event) => {
	requireUser(event);
	return { entries: changelogEntries() };
};
