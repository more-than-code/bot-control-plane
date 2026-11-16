/**
 * The repository's CHANGELOG.md, parsed for the "What's new" page and the account menu dot (spec-update-awareness §2.4).
 * Restricted format: `## vX.Y.Z — YYYY-MM-DD` headings, each followed by `- ` bullets; inline `code` is the only
 * markup. The text is turned into plain data here and rendered as Svelte elements: no markdown library and no raw HTML.
 * The file itself is read on the server only, in `$lib/server/changelog.ts`.
 */

export type ChangelogPart = { code: boolean; text: string };

export type ChangelogEntry = {
	version: string;
	date: string;
	/** Each bullet as text and inline-code parts, in order. */
	items: ChangelogPart[][];
};

const PART = '(?:0|[1-9]\\d{0,8})';
/** Same rule as the Go parser: X.Y.Z parts without leading zeros. */
const HEADING = new RegExp(`^## v(${PART}\\.${PART}\\.${PART}) — (\\d{4}-\\d{2}-\\d{2})$`);
const BULLET = /^- (\S.*)$/;
const VERSION = /^(\d{1,9})\.(\d{1,9})\.(\d{1,9})$/;

/** Splits one bullet on backticks; an unmatched backtick stays as text. */
export function inlineParts(text: string): ChangelogPart[] {
	const parts: ChangelogPart[] = [];
	let rest = text;
	while (rest.length > 0) {
		const open = rest.indexOf('`');
		const close = open === -1 ? -1 : rest.indexOf('`', open + 1);
		if (open === -1 || close === -1) {
			parts.push({ code: false, text: rest });
			break;
		}
		if (open > 0) parts.push({ code: false, text: rest.slice(0, open) });
		if (close > open + 1) parts.push({ code: true, text: rest.slice(open + 1, close) });
		rest = rest.slice(close + 1);
	}
	return parts;
}

/**
 * Releases, newest first, as written. Prose and a `# ` title before the first heading are skipped; anything else
 * that does not fit the format is dropped rather than shown (the Go test is where a malformed file fails).
 */
export function parseChangelog(text: string): ChangelogEntry[] {
	const entries: ChangelogEntry[] = [];
	// The release the next bullets belong to; a heading that does not fit ends it, so its bullets are dropped too.
	let current: ChangelogEntry | null = null;
	for (const raw of text.split(/\r?\n/)) {
		const line = raw.trimEnd();
		if (line.startsWith('#')) {
			const heading = HEADING.exec(line);
			current = heading ? { version: heading[1], date: heading[2], items: [] } : null;
			if (current) entries.push(current);
			continue;
		}
		const bullet = BULLET.exec(line);
		if (!bullet || !current) continue;
		const parts = inlineParts(bullet[1]);
		// "- ``" has no text to show.
		if (parts.length > 0) current.items.push(parts);
	}
	return entries.filter((entry) => entry.items.length > 0);
}

/** -1, 0 or 1 for two X.Y.Z versions; null when either does not parse. */
export function compareVersions(a: string, b: string): number | null {
	const x = VERSION.exec(a);
	const y = VERSION.exec(b);
	if (!x || !y) return null;
	for (let i = 1; i <= 3; i++) {
		const d = Number(x[i]) - Number(y[i]);
		if (d !== 0) return d < 0 ? -1 : 1;
	}
	return 0;
}

export const SEEN_VERSION_COOKIE = 'bot_seen_version';
export const SEEN_VERSION_MAX_AGE = 31536000;

/** The dot shows while the latest release is newer than the seen marker; a missing or odd marker counts as unseen. */
export function hasUnseenRelease(latest: string | null, seen: string | null | undefined): boolean {
	if (!latest) return false;
	const order = seen ? compareVersions(seen, latest) : null;
	return order === null || order < 0;
}

/** Not HttpOnly: the browser writes it on /whats-new, like `bot_tz`. */
export function seenVersionCookie(version: string, secure: boolean): string {
	const securePart = secure ? '; Secure' : '';
	return `${SEEN_VERSION_COOKIE}=${encodeURIComponent(version)}; Path=/; Max-Age=${SEEN_VERSION_MAX_AGE}; SameSite=Lax${securePart}`;
}

export function rememberSeenVersion(version: string): void {
	if (typeof document === 'undefined' || !VERSION.test(version)) return;
	const secure = typeof location !== 'undefined' && location.protocol === 'https:';
	document.cookie = seenVersionCookie(version, secure);
}
