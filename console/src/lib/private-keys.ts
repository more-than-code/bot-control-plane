/**
 * An entry's private keys (api-v1 v1.33 item 6): the keys of its private set `@<entry>`, as `GET /catalog/{name}`
 * lists them. Names and flags only: the API never sends a value here, and nothing here would keep one.
 */
export type PrivateKey = { key: string; is_secret: boolean; needs_value: boolean; reveal_pending: boolean };

const ENV_KEY = /^[A-Z_][A-Z0-9_]{0,63}$/;

/**
 * `private_keys` of a catalog entry. Missing (an API older than v1.33) is an empty list; anything malformed is
 * null (the drawer then says it could not be read). Sorted by key.
 */
export function parsePrivateKeys(entry: unknown): PrivateKey[] | null {
	if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return null;
	const raw = (entry as Record<string, unknown>).private_keys;
	if (raw == null) return [];
	if (!Array.isArray(raw) || raw.length > 256) return null;
	const out: PrivateKey[] = [];
	for (const item of raw) {
		if (!item || typeof item !== 'object' || Array.isArray(item)) return null;
		const v = item as Record<string, unknown>;
		if (typeof v.key !== 'string' || !ENV_KEY.test(v.key) || typeof v.is_secret !== 'boolean') return null;
		if (out.some((row) => row.key === v.key)) return null;
		out.push({ key: v.key, is_secret: v.is_secret, needs_value: v.needs_value === true, reveal_pending: v.reveal_pending === true });
	}
	return out.sort((a, b) => a.key.localeCompare(b.key));
}

/** The short state words after a key: its kind, then what it still needs. */
export function privateKeyNotes(row: PrivateKey): string[] {
	const notes = [row.is_secret ? 'secret' : 'plain'];
	if (row.needs_value) notes.push('needs a value');
	if (row.reveal_pending) notes.push('generated, not shown yet');
	return notes;
}

/** More keys than this fold into a closed list in the drawer's pinned band. */
export const PRIVATE_KEYS_OPEN_MAX = 5;

/** Whether the list shows as a closed `<details>`: the form has scrolled, or the list is long. */
export function privateKeysFolded(count: number, collapsed: boolean): boolean {
	return collapsed || count > PRIVATE_KEYS_OPEN_MAX;
}

const ENTRY_NAME = /^[a-z0-9][a-z0-9-]{0,47}$/;

/** The entry of a private set name `@<entry>`, or null. */
export function privateSetEntry(set: string): string | null {
	return set.startsWith('@') && ENTRY_NAME.test(set.slice(1)) ? set.slice(1) : null;
}

/**
 * `POST /v1/projects/{id}/catalog/{name}/private-keys/{key}/reveal` (api-v1 v1.33, show once, console only), or null
 * when the entry name or the key does not follow its rule.
 */
export function privateKeyRevealPath(projectId: string, entry: string, key: string): string | null {
	if (!ENTRY_NAME.test(entry) || !ENV_KEY.test(key)) return null;
	return `/v1/projects/${projectId}/catalog/${entry}/private-keys/${key}/reveal`;
}

/** After Reveal once (shown or refused), the row is an ordinary secret: no longer pending. The reload confirms it. */
export function afterPrivateReveal(keys: readonly PrivateKey[], key: string): PrivateKey[] {
	return keys.map((row) => (row.key === key ? { ...row, reveal_pending: false } : row));
}
