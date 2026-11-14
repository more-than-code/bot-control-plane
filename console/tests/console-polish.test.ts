import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { render } from 'svelte/server';
import type { JoinToken, ProjectFile, Registry } from '../src/lib/api-types.ts';
import { joinGrantText, joinTokenState } from '../src/lib/org-view.ts';
import TokensSection from '../src/routes/orgs/[id]/TokensSection.svelte';
import SettingsSection from '../src/routes/projects/[id]/SettingsSection.svelte';
import { ORG, PROJECT } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const NOW = Date.parse('2026-11-14T12:00:00Z');
const PAST = '2026-11-14T11:59:59Z';
const FUTURE = '2026-11-15T12:00:00Z';

function token(overrides: Partial<JoinToken> = {}): JoinToken {
	return {
		id: '00000000-0000-4000-8000-000000000e01',
		host_name_hint: 'pi5',
		created_by: '00000000-0000-4000-8000-000000000e02',
		created_at: '2026-11-14T10:00:00Z',
		expires_at: FUTURE,
		used_at: null,
		used_by_host_id: null,
		revoked_at: null,
		grant: null,
		...overrides
	};
}

test('join token state: revoked, then used, then expired, then active', () => {
	assert.equal(joinTokenState(token(), NOW), 'active');
	assert.equal(joinTokenState(token({ expires_at: PAST }), NOW), 'expired');
	// The expiry instant itself is no longer active.
	assert.equal(joinTokenState(token({ expires_at: new Date(NOW).toISOString() }), NOW), 'expired');
	assert.equal(joinTokenState(token({ expires_at: 'not a time' }), NOW), 'expired');
	assert.equal(joinTokenState(token({ expires_at: null }), NOW), 'expired');
	assert.equal(joinTokenState(token({ expires_at: PAST, used_at: PAST }), NOW), 'used');
	assert.equal(joinTokenState(token({ used_at: PAST }), NOW), 'used');
	assert.equal(joinTokenState(token({ expires_at: PAST, used_at: PAST, revoked_at: PAST }), NOW), 'revoked');
	assert.equal(joinTokenState(token({ revoked_at: PAST }), NOW), 'revoked');
});

test('the Join tokens tab offers Revoke only on an active token', () => {
	setPageUrl(`http://console.example/orgs/${ORG.id}?tab=tokens`);
	const active = token({ id: '00000000-0000-4000-8000-000000000e11', host_name_hint: 'live', expires_at: '2999-01-01T00:00:00Z' });
	const expired = token({ id: '00000000-0000-4000-8000-000000000e12', host_name_hint: 'stale', expires_at: '2000-01-01T00:00:00Z' });
	const used = token({ id: '00000000-0000-4000-8000-000000000e13', host_name_hint: 'taken', used_at: '2000-01-01T00:00:00Z' });
	const html = render(TokensSection, { props: { tokens: [active, expired, used], omitted: 0, truncated: false, message: null } }).body;
	assert.equal(html.includes('aria-label="Revoke live"'), true);
	assert.equal(html.includes('Revoke stale'), false);
	assert.equal(html.includes('Revoke taken'), false);
	assert.match(html, />expired</);
	assert.match(html, />used</);
	assert.match(html, />active</);
});

test('the Settings Delete buttons name the file or registry they delete', () => {
	setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
	const file: ProjectFile = { name: 'app.conf', is_secret: false, size: 12, sha256: 'ab'.repeat(32), updated_at: '2026-11-14T10:00:00Z', updated_by: 'joe' };
	const registry: Registry = { host: 'ghcr.io', username: 'joe', updated_at: '2026-11-14T10:00:00Z', updated_by: 'joe' };
	const html = render(SettingsSection, {
		props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [], files: [file], registries: [registry] }
	}).body;
	// Each alert-dialog trigger: its accessible name (aria-label, else its text) and its visible text.
	const triggers = [...html.matchAll(/<button([^>]*data-slot="alert-dialog-trigger"[^>]*)>([\s\S]*?)<\/button>/g)].map((match) => {
		const text = match[2].replace(/<!--[\s\S]*?-->/g, '').replace(/<svg[\s\S]*?<\/svg>/g, '').trim();
		return { name: /aria-label="([^"]*)"/.exec(match[1])?.[1] ?? text, text };
	});
	assert.deepEqual(
		triggers.filter((item) => item.text === 'Delete'),
		[
			{ name: 'Delete file app.conf', text: 'Delete' },
			{ name: 'Delete registry ghcr.io', text: 'Delete' }
		]
	);
});

/**
 * shadcn-svelte's generated primitives style states with `data-<word>:` variants. bits-ui 2 expresses most states
 * through `data-state="…"` and `data-orientation="…"`, so `data-open:` only works when a `@custom-variant` maps it.
 * Those mappings come from `shadcn-svelte/tailwind.css`, which `src/app.css` imports, plus any defined in app.css.
 *
 * A few words need no mapping because the attribute is set bare and Tailwind's built-in `data-<word>:` (`[data-<word>]`)
 * already matches it: bits-ui sets `data-disabled=""`, `data-highlighted=""` and `data-placeholder=""`, and the
 * generated menu components set `data-inset` themselves.
 */
const BARE_ATTRIBUTES = new Set(['disabled', 'highlighted', 'placeholder', 'inset']);

function svelteFiles(dir: string): string[] {
	return readdirSync(dir).flatMap((name) => {
		const path = join(dir, name);
		if (statSync(path).isDirectory()) return svelteFiles(path);
		return name.endsWith('.svelte') || name.endsWith('.ts') ? [path] : [];
	});
}

/** `@custom-variant data-<word>` rules in the CSS, after comments are removed (a commented-out rule maps nothing). */
function customVariants(css: string): Map<string, string> {
	const found = new Map<string, string>();
	const live = css.replace(/\/\*[\s\S]*?\*\//g, '');
	for (const match of live.matchAll(/@custom-variant\s+data-([a-z][a-z0-9-]*)\s*(\([^)]*\)|\{[\s\S]*?\n\})/g)) {
		found.set(match[1], match[2]);
	}
	return found;
}

/**
 * The `data-<word>` variants used in `sources` that no `@custom-variant` in `css` maps and that are not set bare.
 * `data-[…]:` is an arbitrary attribute selector and needs no mapping; `group-data-x/name:` and `not-data-x:` do.
 */
function unmappedVariants(css: readonly string[], sources: readonly string[]): string[] {
	const mapped = new Map(css.flatMap((text) => [...customVariants(text)]));
	const used = new Set<string>();
	for (const source of sources) {
		for (const match of source.matchAll(/(?<![\w[-])(?:[a-z]+-)?data-([a-z][a-z0-9-]*)(?:\/[\w-]+)?:/g)) used.add(match[1]);
	}
	return [...used].filter((word) => !mapped.has(word) && !BARE_ATTRIBUTES.has(word)).sort();
}

test('the variant check reports unmapped and commented-out variants', () => {
	const css = '@custom-variant data-open {\n\t&:where([data-state="open"]) {\n\t\t@slot;\n\t}\n}\n/* @custom-variant data-selected {\n\t&:where([data-selected="true"]) {\n\t\t@slot;\n\t}\n} */';
	assert.deepEqual(unmappedVariants([css], ['class="data-open:block data-foo:flex data-selected:bg-muted data-[state=on]:bg-muted data-disabled:opacity-50"']), [
		'foo',
		'selected'
	]);
	assert.deepEqual(unmappedVariants([css, '@custom-variant data-foo (&[data-state=foo]);'], ['group-data-foo/x:flex not-data-open:hidden']), []);
});

test('every data-<word>: variant used in ui/ is mapped to the attribute bits-ui sets', () => {
	const root = fileURLToPath(new URL('..', import.meta.url));
	const appCss = readFileSync(join(root, 'src/app.css'), 'utf8');
	assert.match(appCss, /@import "shadcn-svelte\/tailwind.css";/, 'app.css imports the shadcn-svelte variant mappings');
	// The CSS import resolves through the package's `style` export, which `require.resolve` does not read.
	const shadcnDir = join(root, 'node_modules/shadcn-svelte');
	const shadcnPackage = JSON.parse(readFileSync(join(shadcnDir, 'package.json'), 'utf8'));
	const shadcnCss = readFileSync(join(shadcnDir, shadcnPackage.exports['./tailwind.css'].style), 'utf8');
	const sources = svelteFiles(join(root, 'src/lib/components/ui')).map((file) => readFileSync(file, 'utf8'));
	assert.ok(sources.some((source) => source.includes('data-open:')) && sources.some((source) => source.includes('data-checked:')), 'the scan sees the generated variants');

	const missing = unmappedVariants([shadcnCss, appCss], sources);
	assert.deepEqual(missing, [], `data-* variants with no @custom-variant: ${missing.join(', ')}`);

	// The state words read bits-ui's data-state; the orientation words read data-orientation.
	const mapped = new Map([...customVariants(shadcnCss), ...customVariants(appCss)]);
	for (const [word, state] of [
		['active', 'active'],
		['checked', 'checked'],
		['unchecked', 'unchecked'],
		['open', 'open'],
		['closed', 'closed']
	]) {
		assert.match(mapped.get(word) ?? '', new RegExp(`\\[data-state="?${state}"?\\]`), `data-${word}`);
	}
	for (const word of ['horizontal', 'vertical']) {
		assert.match(mapped.get(word) ?? '', new RegExp(`\\[data-orientation="?${word}"?\\]`), `data-${word}`);
	}
});

test('a created token grant reads as "<project> (<level>)", with a fallback for an unknown project', () => {
	const names = { [PROJECT.id]: 'Lab' };
	assert.equal(joinGrantText({ project_id: PROJECT.id, level: 'metrics' }, names), 'Lab (metrics)');
	assert.equal(joinGrantText({ project_id: '00000000-0000-4000-8000-0000000000ff', level: 'deploy' }, names), 'a project (deploy)');
	assert.equal(joinGrantText(null, names), null);
});
