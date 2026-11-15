import { ENV_KEY } from '$lib/catalog';
import { envSource, type SelectedSet, type Winner } from '$lib/env-precedence';

/**
 * `${NAME}` references in an image entry's inline env value (api-v1 v1.27 item 7), read the way the API
 * resolves them (bot-api-server `catalog.ResolveEnvRefs`): `$${` is a literal `${`; `${NAME}` with NAME
 * matching the env key rule is a reference; a `${…}` whose inside is not a key name stays literal; a `${`
 * with no closing `}` is unterminated. Only key names are used. The API stays authoritative.
 */
export type EnvRefScan = { names: string[]; unterminated: boolean };

export function scanEnvRefs(value: string): EnvRefScan {
	const names: string[] = [];
	let i = 0;
	while (i < value.length) {
		if (value.startsWith('$${', i)) {
			i += 3;
			continue;
		}
		if (!value.startsWith('${', i)) {
			i += 1;
			continue;
		}
		const end = value.indexOf('}', i + 2);
		if (end < 0) return { names, unterminated: true };
		const name = value.slice(i + 2, end);
		if (!ENV_KEY.test(name)) {
			i += 2;
			continue;
		}
		if (!names.includes(name)) names.push(name);
		i = end + 1;
	}
	return { names, unterminated: false };
}

export type EnvRefHint = { kind: 'source'; name: string; text: string } | { kind: 'unknown'; name: string; text: string } | { kind: 'unterminated'; text: string };

function sourceText(source: Winner): string {
	if (source.kind === 'env') return 'the inline env';
	if (source.kind === 'secret') return 'the secret env';
	return `env set ${source.name}`;
}

/**
 * One hint per referenced name: where its value comes from, or a warning when nothing sets it for this
 * entry. `envKeys` are the inline env keys, `secretKeys` the secret env keys, `selected` the chosen sets.
 */
export function envRefHints(value: string, envKeys: readonly string[], secretKeys: readonly string[], selected: readonly SelectedSet[]): EnvRefHint[] {
	const scan = scanEnvRefs(value);
	const hints: EnvRefHint[] = scan.names.map((name) => {
		const source = envSource(name, envKeys, secretKeys, selected);
		if (!source) return { kind: 'unknown', name, text: `References \${${name}}, which is not set for this entry.` };
		return { kind: 'source', name, text: `\${${name}} comes from ${sourceText(source)}.` };
	});
	if (scan.unterminated) hints.push({ kind: 'unterminated', text: 'This value has a ${ with no closing }. Write $${ for a literal ${.' });
	return hints;
}
