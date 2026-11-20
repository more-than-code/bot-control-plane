import { dotenvPairs } from '$lib/env-lint';

/**
 * Secret suggestion by name (spec-env-generate rev 3 §3.4, phase B): a key that ends in _KEY, _SECRET, _TOKEN or
 * _PASSWORD, or contains PASSWORD, looks secret. Case-insensitive on the key as typed. It only shows the muted
 * "Looks secret: …" line; no Secret box ever ticks itself (Joe 2026-11-20).
 */
export function looksSecret(key: string): boolean {
	const upper = key.trim().toUpperCase();
	if (upper === '') return false;
	return upper.includes('PASSWORD') || ['_KEY', '_SECRET', '_TOKEN'].some((suffix) => upper.endsWith(suffix));
}

/** Keys of a paste that are new to the set and look secret, in paste order, each once. */
export function secretLookingKeys(dotenv: string, existing: readonly string[] = []): string[] {
	const out: string[] = [];
	for (const { key } of dotenvPairs(dotenv)) {
		if (!existing.includes(key) && !out.includes(key) && looksSecret(key)) out.push(key);
	}
	return out;
}

/** The muted line under a paste box, or '' when no new key looks secret. */
export function looksSecretLine(keys: readonly string[]): string {
	return keys.length > 0 ? `Looks secret: ${keys.join(', ')}` : '';
}
