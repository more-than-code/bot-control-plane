// The repository's CHANGELOG.md, inlined at build time (Vite `?raw`). Server only, so the browser never asks the dev
// server for a file outside console/.
import text from '../../../../CHANGELOG.md?raw';
import { parseChangelog, type ChangelogEntry } from '$lib/changelog';

const entries: ChangelogEntry[] = parseChangelog(text);

export function changelogEntries(): ChangelogEntry[] {
	return entries;
}

/** The newest release's version (X.Y.Z), or null when the file has none. */
export function latestVersion(): string | null {
	return entries[0]?.version ?? null;
}
