import { ENV_KEY } from '$lib/catalog';

/**
 * Paste lint (spec-env-generate §3.6): muted warnings for values that are probably pasted wrong.
 * Warnings only; the save is still allowed. A warning names the key, never the value.
 */

/** The warnings for one value. Empty when nothing looks wrong. */
export function envValueWarnings(key: string, value: string): string[] {
	if (key === '') return [];
	const warnings: string[] = [];
	if (value.startsWith(`${key}=`)) warnings.push(`${key}: the value starts with ${key}=. Remove the repeated name.`);
	if (value !== value.trim()) warnings.push(`${key}: the value has leading or trailing spaces.`);
	if (value.length >= 2) {
		const first = value[0];
		if ((first === '"' || first === "'") && value[value.length - 1] === first) warnings.push(`${key}: the value is still wrapped in quotes.`);
	}
	return warnings;
}

/**
 * The key and value of each pasted line as the API reads it (api-v1 v1.19 dotenv): CRLF and CR are line
 * breaks, blank and `#` lines are skipped, a leading `export ` is dropped, one pair of matching quotes is
 * removed, and nothing is trimmed from the value. Malformed lines are skipped here; the API reports
 * them (and reserved or duplicate keys).
 */
export function dotenvPairs(dotenv: string): Array<{ key: string; value: string }> {
	const pairs: Array<{ key: string; value: string }> = [];
	for (const line of dotenv.split(/\r\n|\r|\n/)) {
		const trimmed = line.trim();
		if (trimmed === '' || trimmed.startsWith('#')) continue;
		let body = line.replace(/^[ \t]+/, '');
		const exported = /^export[ \t]+/.exec(body);
		if (exported) body = body.slice(exported[0].length);
		const eq = body.indexOf('=');
		if (eq <= 0) continue;
		const key = body.slice(0, eq);
		if (!ENV_KEY.test(key)) continue;
		let value = body.slice(eq + 1);
		if (value.length >= 2) {
			const first = value[0];
			if ((first === '"' || first === "'") && value[value.length - 1] === first) value = value.slice(1, -1);
			else if (first === '"' || first === "'") continue;
		} else if (value === '"' || value === "'") continue;
		pairs.push({ key, value });
	}
	return pairs;
}

/** Warnings for a whole paste, in line order. */
export function dotenvWarnings(dotenv: string): string[] {
	return dotenvPairs(dotenv).flatMap((pair) => envValueWarnings(pair.key, pair.value));
}
