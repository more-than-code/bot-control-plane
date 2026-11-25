import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { EnvSet, HostPrograms } from '../src/lib/api-types.ts';
import { imageEnvHashNote } from '../src/lib/catalog.ts';
import {
	GENERATE_KINDS,
	GENERATE_LABELS,
	PASSWORD_LENGTH,
	REVEAL_CLOSED,
	REVEAL_ONCE_NOTE,
	envRowActions,
	generateRequestBody,
	isGenerateKind,
	revealAnswer,
	revealCanClose,
	revealClosed,
	revealFailedText,
	revealOpened,
	revealSettled
} from '../src/lib/env-generate.ts';
import { dotenvPairs, dotenvWarnings, envValueWarnings } from '../src/lib/env-lint.ts';
import { envRefHints, scanEnvRefs } from '../src/lib/env-references.ts';
import { looksSecret, looksSecretLine, secretLookingKeys } from '../src/lib/env-secret-name.ts';
import { envSource } from '../src/lib/env-precedence.ts';
import { parseEnvSet, parseEnvSetList, parseHostPrograms } from '../src/lib/server/shape.ts';
import { addFailed, addIsSecret, addKeyError, addKeyExistsMessage, addTabFailed, initialAddDraft, initialAddVariables, placeholderRule, setAddSecret, setAddSource } from '../src/routes/projects/[id]/env-add-variable.ts';
import EnvSetTable from '../src/routes/projects/[id]/EnvSetTable.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const REVEALED = 'Gen3ratedValueNeverInPageData0123456789ab';
const base = { session: TOKEN, user: USER, projects: [PROJECT], params: { id: PROJECT.id } };
const ACTION_HEADERS = { 'x-sveltekit-action': 'true', accept: 'application/json' };

function source(name: string): string {
	return readFileSync(new URL(`../src/routes/projects/[id]/${name}`, import.meta.url), 'utf8');
}

function decode(value: string): string {
	return value.replaceAll('&amp;', '&').replaceAll('&quot;', '"').replaceAll('&#39;', "'");
}

const SET: EnvSet = {
	name: 'app',
	keys: [
		{ key: 'DB_PASSWORD', is_secret: true, value: null, preview: 'Ab…', reveal_pending: true, needs_value: false, generated: true },
		{ key: 'API_TOKEN', is_secret: true, value: null, preview: 'su…', reveal_pending: false, needs_value: false },
		{ key: 'LOG_LEVEL', is_secret: false, value: 'debug', preview: null, reveal_pending: false, needs_value: false }
	],
	updated_at: '2026-10-28T10:00:00Z',
	updated_by: USER.id
};

const TABLE_PROPS = {
	putAction: '?/putEnvKey',
	deleteAction: '?/deleteEnvKey',
	deleteSetAction: '?/deleteEnvSet',
	makeSecretAction: '?/makeEnvKeysSecret',
	mergeAction: '?/mergeEnvSet',
	revealAction: '?/revealEnvKey'
};

describe('generate for a new key (api-v1 v1.27 item 1)', () => {
	before(() => useApi('https://api.example'));
	after(() => restoreEnv());
	const url = `http://console.example/projects/${PROJECT.id}?tab=settings&/putEnvKey`;

	test('request bodies: password sends PASSWORD_LENGTH, hex and base64url send the kind only, always secret', () => {
		assert.equal(PASSWORD_LENGTH, 32);
		assert.equal(GENERATE_LABELS.password, `Password, ${PASSWORD_LENGTH} characters`);
		assert.deepEqual(generateRequestBody('password'), { generate: { kind: 'password', length: PASSWORD_LENGTH }, is_secret: true });
		assert.deepEqual(generateRequestBody('hex'), { generate: { kind: 'hex' }, is_secret: true });
		assert.deepEqual(generateRequestBody('base64url'), { generate: { kind: 'base64url' }, is_secret: true });
		assert.deepEqual([...GENERATE_KINDS], ['password', 'hex', 'base64url']);
		assert.equal(isGenerateKind('rsa'), false);
	});

	test('putEnvKey with generate_kind sends generate and no value, then redirects', async () => {
		for (const [kind, op] of [
			['hex', 'add'],
			['password', 'add'],
			['base64url', 'add']
		] as const) {
			const calls = installFetch(() => envelope({ key: 'DB_PASSWORD', is_secret: true, value: null, preview: 'ab…', reveal_pending: true }));
			const form: Record<string, string> = { set: 'app', key: 'DB_PASSWORD', generate_kind: kind, value: 'typed-should-not-be-sent', is_secret: '0' };
			if (op) form.op = op;
			await assert.rejects(
				async () => projectActions.putEnvKey(fakeEvent({ ...base, url, form }).event as never),
				(err: unknown) => isRedirect(err) && err.status === 303
			);
			assert.equal(calls.length, 1);
			assert.equal(calls[0]?.method, 'PUT');
			assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/env-sets/app/keys/DB_PASSWORD`);
			const body = JSON.parse(calls[0]?.body ?? '{}');
			assert.deepEqual(body, generateRequestBody(kind));
			assert.equal('value' in body, false);
		}
	});

	test('an unknown kind is refused before the API; an API refusal shows its message', async () => {
		const calls = installFetch(() => envelope({}));
		const bad = await projectActions.putEnvKey(fakeEvent({ ...base, url, form: { set: 'app', key: 'K', generate_kind: 'rsa' } }).event as never);
		assert.equal(isActionFailure(bad) && bad.status, 400);
		assert.equal(calls.length, 0);
		installFetch(() => envelope(null, 400, 'bad_request', 'make LOG_LEVEL secret before generating a value'));
		const plain = await projectActions.putEnvKey(fakeEvent({ ...base, url, form: { set: 'app', key: 'LOG_LEVEL', generate_kind: 'password' } }).event as never);
		assert.equal(isActionFailure(plain), true);
		if (isActionFailure(plain)) assert.equal((plain.data as unknown as { message: string }).message, 'make LOG_LEVEL secret before generating a value');
		// Generate on an existing key is the API's to refuse; the console passes its message through.
		installFetch(() => envelope(null, 400, 'bad_request', 'DB_PASSWORD already has a value; edit it to change it'));
		const existing = await projectActions.putEnvKey(fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD', op: 'add', generate_kind: 'hex' } }).event as never);
		assert.equal(isActionFailure(existing), true);
		if (isActionFailure(existing)) assert.equal((existing.data as unknown as { message: string }).message, 'DB_PASSWORD already has a value; edit it to change it');
	});

	test('One variable: Generate forces secret, drops a typed value, and keeps no value after a failure', () => {
		const typed = { ...initialAddDraft(), key: 'DB_PASSWORD', value: 'typed' };
		const gen = setAddSource(typed, 'generate');
		assert.equal(gen.value, '');
		assert.equal(addIsSecret(gen), true);
		assert.equal(addIsSecret(typed), false);
		assert.equal(setAddSource(gen, 'value').source, 'value');
		// Generate is for new keys: the existing-key refusal runs before sending, whatever the source.
		assert.equal(addKeyError('DB_PASSWORD', ['DB_PASSWORD']), addKeyExistsMessage('DB_PASSWORD'));
		const failed = addFailed(gen, 'refused');
		assert.equal(failed.retype, false);
		assert.equal(failed.error, 'refused');
		const dialog = source('EnvAddVariablesDialog.svelte');
		assert.match(dialog, /<Input type="hidden" name="generate_kind" value=\{view\.draft\.kind\} \/>/);
		assert.match(dialog, /checked=\{secretShown\}\s*disabled=\{generating \|\| placeholder\.secret !== null\}/);
		assert.match(dialog, /if \(outcome === 'added' && generated\) onGenerated\(key\)/);
		// The toggle and kind select are the shared ValueSourceFields (also on the value-request fill page).
		assert.match(dialog, /<ValueSourceFields\s+idOf=\{\(part\) => id\(`add-\$\{part\}`\)\}\s+source=\{view\.draft\.source\}/);
		const fields = readFileSync(new URL('../src/lib/components/ValueSourceFields.svelte', import.meta.url), 'utf8');
		// The pressed item cannot be unpressed (getter/setter bind ignores ''), and the select is labelled Kind.
		assert.match(fields, /bind:value=\{\s*\(\) => source,/);
		assert.match(fields, /<Label for=\{idOf\('kind'\)\}>Kind<\/Label>/);
		// The value input is not rendered while generating, so nothing typed is posted.
		assert.match(dialog, /\{#if !generating\}[^]*?id=\{id\('add-value'\)\}/);
	});
});

describe('reveal once (api-v1 v1.27 item 3)', () => {
	before(() => useApi('https://api.example'));
	after(() => restoreEnv());
	const url = `http://console.example/projects/${PROJECT.id}?tab=settings&/revealEnvKey`;

	test('the value is only in that action result, and the API path is …/reveal', async () => {
		const calls = installFetch(() => envelope({ key: 'DB_PASSWORD', value: REVEALED }));
		const result = await projectActions.revealEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.equal(calls.length, 1);
		assert.equal(calls[0]?.method, 'POST');
		assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/env-sets/app/keys/DB_PASSWORD/reveal`);
		assert.equal(calls[0]?.url.search, '');
		assert.deepEqual(result, { scope: 'settings', revealed: { key: 'DB_PASSWORD', value: REVEALED } });
		// No message field, so even an applied result would not reach the settings notice.
		assert.equal(Object.hasOwn(result as object, 'message'), false);
		assert.deepEqual(revealAnswer({ type: 'success', data: result }, 'DB_PASSWORD'), { ok: true, value: REVEALED });
		assert.equal(revealAnswer({ type: 'success', data: result }, 'OTHER').ok, false);
	});

	test('a 409 shows the API message; a post without JavaScript is refused before the API', async () => {
		const conflict = 'the value of DB_PASSWORD was already shown or was not generated; edit it to set a new value';
		installFetch(() => envelope(null, 409, 'conflict', conflict));
		const failed = await projectActions.revealEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.equal(isActionFailure(failed) && failed.status, 409);
		if (isActionFailure(failed)) {
			assert.equal((failed.data as unknown as { revealError: string }).revealError, conflict);
			assert.deepEqual(revealAnswer({ type: 'failure', data: failed.data }, 'DB_PASSWORD'), { ok: false, message: conflict });
		}

		const calls = installFetch(() => envelope({ key: 'DB_PASSWORD', value: REVEALED }));
		const plainPost = await projectActions.revealEnvKey(fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD' } }).event as never);
		assert.equal(isActionFailure(plainPost) && plainPost.status, 400);
		assert.equal(calls.length, 0);
		assert.equal(JSON.stringify(plainPost).includes(REVEALED), false);

		// The action header without a JSON Accept (what a page navigation sends) is refused too.
		const htmlAccept = await projectActions.revealEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD' }, requestHeaders: { 'x-sveltekit-action': 'true', accept: 'text/html' } }).event as never
		);
		assert.equal(isActionFailure(htmlAccept) && htmlAccept.status, 400);
		assert.equal(calls.length, 0);

		for (const form of [{ set: 'app', key: 'bad key' }, { set: 'Bad Set', key: 'DB_PASSWORD' }]) {
			const bad = await projectActions.revealEnvKey(fakeEvent({ ...base, url, form, requestHeaders: ACTION_HEADERS }).event as never);
			assert.equal(isActionFailure(bad) && bad.status, 400);
		}
		assert.equal(calls.length, 0);
	});

	test('a malformed answer or a lost one says the view may be used up, never a value', async () => {
		const failedText = revealFailedText('DB_PASSWORD');
		assert.equal(failedText, 'The value could not be shown. If Reveal once is gone, edit DB_PASSWORD to set a new value.');
		const reveal = () =>
			projectActions.revealEnvKey(fakeEvent({ ...base, url, form: { set: 'app', key: 'DB_PASSWORD' }, requestHeaders: ACTION_HEADERS }).event as never);
		for (const payload of [{ key: 'OTHER', value: REVEALED }, { key: 'DB_PASSWORD', value: 42 }, null]) {
			installFetch(() => envelope(payload));
			const result = await reveal();
			assert.equal(isActionFailure(result) && result.status, 502);
			if (isActionFailure(result)) assert.equal((result.data as unknown as { revealError: string }).revealError, failedText);
			assert.equal(JSON.stringify(result).includes(REVEALED), false);
		}
		installFetch(() => {
			throw new TypeError('fetch failed');
		});
		const lost = await reveal();
		assert.equal(isActionFailure(lost) && lost.status, 503);
		if (isActionFailure(lost)) assert.equal((lost.data as unknown as { revealError: string }).revealError, failedText);
		assert.deepEqual(revealAnswer({ type: 'error' }, 'DB_PASSWORD'), { ok: false, message: failedText });
		assert.deepEqual(revealAnswer({ type: 'success', data: {} }, 'DB_PASSWORD'), { ok: false, message: failedText });
	});

	test('dialog state: loading cannot close, the value exists only while shown, and stale answers are dropped', () => {
		const loading = revealOpened('DB_PASSWORD', 1);
		assert.deepEqual(loading, { status: 'loading', key: 'DB_PASSWORD', token: 1 });
		// The answer is the only copy: no close while it is out.
		assert.equal(revealCanClose(loading), false);
		assert.deepEqual(revealClosed(loading), loading);
		const shown = revealSettled(loading, 'DB_PASSWORD', 1, { ok: true, value: REVEALED });
		assert.deepEqual(shown, { status: 'shown', key: 'DB_PASSWORD', token: 1, value: REVEALED });
		assert.equal(revealCanClose(shown), true);
		const closed = revealClosed(shown);
		assert.deepEqual(closed, REVEAL_CLOSED);
		assert.equal(JSON.stringify(closed).includes(REVEALED), false);
		const failed = revealSettled(loading, 'DB_PASSWORD', 1, { ok: false, message: 'no' });
		assert.deepEqual(failed, { status: 'failed', key: 'DB_PASSWORD', token: 1, message: 'no' });
		assert.deepEqual(revealClosed(failed), REVEAL_CLOSED);
		// Closed before the answer: it never lands.
		assert.deepEqual(revealSettled(REVEAL_CLOSED, 'DB_PASSWORD', 1, { ok: true, value: REVEALED }), REVEAL_CLOSED);
		// An answer for another key, or from an older request (a 409 from an earlier open), is ignored.
		assert.deepEqual(revealSettled(revealOpened('OTHER', 1), 'DB_PASSWORD', 1, { ok: true, value: REVEALED }), revealOpened('OTHER', 1));
		const newer = revealOpened('DB_PASSWORD', 2);
		assert.deepEqual(revealSettled(newer, 'DB_PASSWORD', 1, { ok: false, message: 'conflict' }), newer);
		assert.equal(revealSettled(newer, 'DB_PASSWORD', 2, { ok: true, value: REVEALED }).status, 'shown');
		assert.equal(REVEAL_ONCE_NOTE, "You won't see this value again.");
		const dialog = source('EnvRevealDialog.svelte');
		// The result is read from its own fetch, never applied to page.form, and cleared on unmount.
		assert.equal(dialog.includes('applyAction'), false);
		assert.equal(dialog.includes('update('), false);
		assert.equal(dialog.includes('console.'), false);
		assert.match(dialog, /\$effect\(\(\) => \(\) => \{\s*reveal = REVEAL_CLOSED;\s*frame = REVEAL_CLOSED;/);
		assert.match(dialog, /interactOutsideBehavior="ignore"/);
		assert.match(dialog, /escapeKeydownBehavior=\{closable \? 'close' : 'ignore'\}/);
		assert.match(dialog, /showCloseButton=\{closable\}/);
		assert.match(dialog, /disabled=\{!closable\}>Close</);
		assert.match(dialog, /input\.focus\(\);\s*input\.select\(\);/);
		assert.match(dialog, /`Value ready for \$\{frame\.key\}`/);
		assert.match(dialog, /readonly value=\{frame\.value\} class="[^"]*font-mono/);
		assert.match(dialog, /<CopyButton value=\{frame\.value\} label="Copy" \/>/);
	});
});

describe('env row actions', () => {
	test('secret row actions are Edit, Reveal once (only while pending), Regenerate (api-v1 v1.54) and Delete', () => {
		assert.deepEqual(envRowActions({ is_secret: true, reveal_pending: true, generated: true }), { revealOnce: true, regenerate: true });
		assert.deepEqual(envRowActions({ is_secret: true, reveal_pending: false, generated: true }), { revealOnce: false, regenerate: true });
		// v1.56: a hand-entered secret (or an older API without the flag) has no Regenerate.
		assert.deepEqual(envRowActions({ is_secret: true, reveal_pending: false, generated: false }), { revealOnce: false, regenerate: false });
		assert.deepEqual(envRowActions({ is_secret: true, reveal_pending: false }), { revealOnce: false, regenerate: false });
		assert.deepEqual(envRowActions({ is_secret: true, reveal_pending: false, needs_value: true }), { revealOnce: false, regenerate: false });
		assert.deepEqual(envRowActions({ is_secret: false, reveal_pending: true }), { revealOnce: false, regenerate: false });
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = decode(render(EnvSetTable, { props: { set: SET, ...TABLE_PROPS, regenerateAction: '?/regenerateEnvKey' } }).body);
		const rowLabels = (key: string) => [...html.matchAll(new RegExp(`aria-label="([^"]*${key}[^"]*)"`, 'g'))].map((m) => m[1]);
		assert.deepEqual(rowLabels('DB_PASSWORD'), ['Edit DB_PASSWORD', 'Reveal DB_PASSWORD once', 'Regenerate DB_PASSWORD', 'Delete DB_PASSWORD']);
		// v1.56: API_TOKEN was entered by hand, so it is changed with Edit, never regenerated.
		assert.deepEqual(rowLabels('API_TOKEN'), ['Edit API_TOKEN', 'Delete API_TOKEN']);
		assert.equal(rowLabels('LOG_LEVEL').includes('Regenerate LOG_LEVEL'), false);
		// Without the action (an older caller) there is no Regenerate.
		const without = decode(render(EnvSetTable, { props: { set: SET, ...TABLE_PROPS } }).body);
		assert.equal(/aria-label="Regenerate/.test(without), false);
		const table = source('EnvSetTable.svelte');
		assert.match(table, /<EnvRevealDialog setName=\{set\.name\} \{revealKey\} action=\{revealAction\} onClose=\{afterReveal\} \/>/);
		assert.match(table, /<EnvRegenerateDialog setName=\{set\.name\} \{regenKey\} usedBy=\{set\.used_by\} action=\{regenerateAction\} onClose=\{afterRegenerate\} \/>/);
	});

	test('key flags parse; missing is false, a plain key is never pending, a bad flag drops the set', () => {
		const set = (keys: unknown[]) => parseEnvSet({ name: 'app', keys, updated_at: '2026-10-28T10:00:00Z', updated_by: USER.id });
		const parsed = set([
			{ key: 'A', is_secret: true, value: null, preview: 'x…', reveal_pending: true, needs_value: true },
			{ key: 'B', is_secret: false, value: 'v', preview: null, reveal_pending: true },
			{ key: 'C', is_secret: true, value: null, preview: null }
		]);
		assert.deepEqual(
			parsed?.keys.map((item) => [item.key, item.reveal_pending, item.needs_value]),
			[
				['A', true, true],
				['B', false, false],
				['C', false, false]
			]
		);
		assert.equal(set([{ key: 'A', is_secret: true, value: null, preview: null, reveal_pending: 'yes' }]), null);
		assert.equal(set([{ key: 'A', is_secret: true, value: null, preview: null, needs_value: 1 }]), null);
		assert.deepEqual(
			set([{ key: 'A', is_secret: true, value: null, preview: null, reveal_pending: null, needs_value: null }])?.keys.map((item) => [item.reveal_pending, item.needs_value]),
			[[false, false]]
		);
		const list = parseEnvSetList({
			items: [{ name: 'app', keys: [{ key: 'A', is_secret: true, value: null, preview: null, reveal_pending: true, needs_value: true }], updated_at: '2026-10-28T10:00:00Z', updated_by: USER.id }],
			next_cursor: null
		});
		assert.deepEqual(list?.items[0]?.keys[0], { key: 'A', is_secret: true, value: null, preview: null, reveal_pending: true, needs_value: true, generated: false });
		// v1.56: generated is read when present; a non-boolean drops the set.
		assert.equal(set([{ key: 'A', is_secret: true, value: null, preview: null, generated: true }])?.keys[0]?.generated, true);
		assert.equal(set([{ key: 'A', is_secret: true, value: null, preview: null, generated: 'yes' }]), null);
	});
});

describe('paste lint (spec §3.6)', () => {
	test('warnings name the key only', () => {
		const cases: Array<[string, string, string[]]> = [
			['DB', 'postgres://x', []],
			['DB', 'DB=postgres://x', ['DB: the value starts with DB=. Remove the repeated name.']],
			['DB', ' x', ['DB: the value has leading or trailing spaces.']],
			['DB', 'x\t', ['DB: the value has leading or trailing spaces.']],
			['DB', '"x"', ['DB: the value is still wrapped in quotes.']],
			['DB', "'x'", ['DB: the value is still wrapped in quotes.']],
			['DB', '"x\'', []],
			['DB', 'OTHER=x', []],
			['', 'x ', []],
			['', '"x"', []],
			['A', 'A==x', ['A: the value starts with A=. Remove the repeated name.']],
			['A', '"', []]
		];
		for (const [key, value, expected] of cases) {
			const got = envValueWarnings(key, value);
			assert.deepEqual(got, expected, `${key}=${value}`);
			for (const warning of got) assert.equal(value.length > 2 && warning.includes(value.trim()), false);
		}
	});

	test('a paste is read like the API (export, one quote pair, nothing trimmed)', () => {
		assert.deepEqual(dotenvPairs('# c\n\nexport A=1\r\nB="2"\rC=\'"3"\'\nbad line\nD="x\nE=y '), [
			{ key: 'A', value: '1' },
			{ key: 'B', value: '2' },
			{ key: 'C', value: '"3"' },
			{ key: 'E', value: 'y ' }
		]);
		assert.deepEqual(dotenvWarnings('A=A=1\nB=ok\nC=\'"3"\'\nE=y '), [
			'A: the value starts with A=. Remove the repeated name.',
			'C: the value is still wrapped in quotes.',
			'E: the value has leading or trailing spaces.'
		]);
		// Rows the API refuses or reads differently are skipped, not linted.
		const rows: Array<[string, Array<{ key: string; value: string }>]> = [
			['export\tA=1', [{ key: 'A', value: '1' }]],
			['export', []],
			['   A= x', [{ key: 'A', value: ' x' }]],
			['=x', []],
			['lower=x', []],
			['A="', []],
			["A='", []],
			['A=1\nA=2', [{ key: 'A', value: '1' }, { key: 'A', value: '2' }]],
			['A==x', [{ key: 'A', value: '=x' }]]
		];
		for (const [paste, expected] of rows) assert.deepEqual(dotenvPairs(paste), expected, JSON.stringify(paste));
		// A duplicate key is still the API's to refuse; its rows are linted like any other.
		assert.deepEqual(dotenvWarnings('A= 1\nA=2'), ['A: the value has leading or trailing spaces.']);
		for (const file of ['EnvAddVariablesDialog.svelte', 'EnvSetDialog.svelte']) assert.match(source(file), /dotenvWarnings\(/);
	});
});

describe('${NAME} reference hints (api-v1 v1.27 item 7)', () => {
	const sets = [
		{ name: 'db', keys: ['POSTGRES_PASSWORD', 'HOST'] },
		{ name: 'web', keys: ['HOST'] }
	];
	test('grammar matches the API resolver', () => {
		const cases: Array<[string, string[], boolean]> = [
			['plain', [], false],
			['${A}', ['A'], false],
			['$${A}', [], false],
			['$$${A}', [], false],
			['${A}${B}${A}', ['A', 'B'], false],
			['${lower}', [], false],
			['${A:-b}', [], false],
			['${}', [], false],
			['${A${B}', ['B'], false],
			['x${A', [], true],
			['${a${B', [], true],
			['$A $', [], false],
			// Mirrors bot-api-server internal/catalog/envref_test.go.
			['${B}${', ['B'], true],
			['$${HOST}$${', [], false],
			['$${PW}${PW}', ['PW'], false],
			[`\${${'A'.repeat(65)}}`, [], false],
			['${a b', [], true],
			['é${HOST}ü—${B}☃', ['HOST', 'B'], false],
			['$HOME and $ and $$', [], false]
		];
		for (const [value, names, unterminated] of cases) assert.deepEqual(scanEnvRefs(value), { names, unterminated }, value);
	});

	test('envSource: inline beats secret, secret beats a set, a later set beats an earlier one', () => {
		const table: Array<[string, string[], string[], typeof sets, unknown]> = [
			['K', ['K'], ['K'], sets, { kind: 'env' }],
			['K', [], ['K'], [{ name: 'a', keys: ['K'] }], { kind: 'secret' }],
			['HOST', [], [], sets, { kind: 'set', name: 'web' }],
			['POSTGRES_PASSWORD', [], [], sets, { kind: 'set', name: 'db' }],
			['K', [], [], [{ name: 'only', keys: ['K'] }], { kind: 'set', name: 'only' }],
			['K', [' K '], [], [], { kind: 'env' }],
			['K', [], [' K'], [], { kind: 'secret' }],
			['K', [], [], [{ name: 'a', keys: ['K '] }], { kind: 'set', name: 'a' }],
			['MISSING', ['A'], ['B'], sets, null]
		];
		for (const [key, env, secret, selected, expected] of table) assert.deepEqual(envSource(key, env, secret, selected), expected, key);
	});

	test('each name shows its source, unknown names warn, $${ is literal', () => {
		const hints = envRefHints('postgres://u:${POSTGRES_PASSWORD}@${HOST}/${PORT}/${TOKEN}/${INLINE}/$${LIT}', ['INLINE', 'URL'], ['TOKEN'], sets);
		assert.deepEqual(
			hints.map((hint) => hint.text),
			[
				'${POSTGRES_PASSWORD} comes from env set db.',
				'${HOST} comes from env set web.',
				'References ${PORT}, which is not set for this entry.',
				'${TOKEN} comes from the secret env.',
				'${INLINE} comes from the inline env.'
			]
		);
		assert.deepEqual(envRefHints('${A', [], [], []).map((hint) => hint.kind), ['unterminated']);
		assert.deepEqual(envRefHints('$${A}', [], [], []), []);
		const form = source('CatalogImageForm.svelte');
		assert.match(form, /envRefHints\(value, draft\.env\.map\(\(row\) => row\.key\), draft\.secrets\.map\(\(row\) => row\.key\), selectedSets\)/);
		assert.match(form, /id="\{fieldId\}-env-refs-\{index\}"/);
		// "Not set" waits for the sets' keys; selectedSets is built once and shared with the override notes.
		assert.match(form, /return setsStatus === 'ready' \? hints : hints\.filter\(\(hint\) => hint\.kind !== 'unknown'\)/);
		assert.match(form, /envOverrides\(draft\.env\.map\(\(row\) => row\.key\), draft\.secrets, selectedSets\)/);
		assert.equal(form.match(/const selectedSets = /g)?.length, 1);
	});
});

describe('deploy dialog env_hash note (agent-protocol v1.14)', () => {
	const programs = (envHash: boolean, images = true): HostPrograms => ({
		enabled: true,
		run_as: ['joe'],
		allow_shell: false,
		policy_hash: 'p',
		images,
		docker: images,
		files: false,
		logging: false,
		health_policy: false,
		secrets: false,
		projects: null,
		project_listed: null,
		shared_networks: null,
		env_hash: envHash
	});
	const NOTE = "This host's agent does not apply env value changes without Force.";

	test('shown only for a service on a container host whose agent lacks env_hash', () => {
		assert.equal(imageEnvHashNote({ programs: programs(false) }, 'service'), NOTE);
		assert.equal(imageEnvHashNote({ programs: programs(false) }, 'job'), null);
		assert.equal(imageEnvHashNote({ programs: programs(true) }, 'service'), null);
		assert.equal(imageEnvHashNote({ programs: programs(false, false) }, 'service'), null);
		assert.equal(imageEnvHashNote({ programs: null }, 'service'), null);
		const hello = { enabled: true, run_as: [], allow_shell: false, policy_hash: 'p', images: true, docker: true };
		assert.equal(parseHostPrograms(hello)?.env_hash, false);
		assert.equal(parseHostPrograms({ ...hello, env_hash: true })?.env_hash, true);
		assert.equal(parseHostPrograms({ ...hello, env_hash: 'yes' }), null);
		// The Deploy dialog render is in shared-networks.test.ts (a host without env_hash or shared_networks).
	});
});

describe('Needs value badge (api-v1 v1.27 item 4)', () => {
	const PLACEHOLDERS: EnvSet = {
		name: 'mail',
		keys: [
			{ key: 'SMTP_PASSWORD', is_secret: true, value: null, preview: '••••', reveal_pending: false, needs_value: true },
			{ key: 'SMTP_HOST', is_secret: false, value: '', preview: null, reveal_pending: false, needs_value: true },
			{ key: 'SMTP_PORT', is_secret: false, value: '587', preview: null, reveal_pending: false, needs_value: false }
		],
		updated_at: '2026-11-15T10:00:00Z',
		updated_by: USER.id
	};

	test('placeholders show the badge, and Edit is described by it', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(EnvSetTable, { props: { set: PLACEHOLDERS, ...TABLE_PROPS } }).body.replaceAll(/<!--[^>]*-->/g, '');
		assert.equal(html.match(/>Needs value</g)?.length, 2);
		const describedByOf = (key: string) => {
			const at = html.indexOf(`aria-label="Edit ${key}"`);
			const start = html.lastIndexOf('<button', at);
			return html.slice(start, at).match(/aria-describedby="([^"]+)"/)?.[1];
		};
		for (const key of ['SMTP_PASSWORD', 'SMTP_HOST']) {
			const id = describedByOf(key);
			assert.ok(id, `Edit ${key} has no description`);
			assert.equal(html.includes(`id="${id}">Needs value<`), true, `${id} is not the badge`);
		}
		assert.equal(describedByOf('SMTP_PORT'), undefined);
	});
});

describe('secret suggestion by name (spec rev 3 §3.4)', () => {
	test('the name rule: ends in _KEY, _SECRET, _TOKEN or _PASSWORD, or contains PASSWORD; case-insensitive', () => {
		for (const key of ['API_KEY', 'JWT_SECRET', 'GH_TOKEN', 'DB_PASSWORD', 'PASSWORD', 'PASSWORD_FILE', 'MYPASSWORDX', 'api_key', 'Db_Password', ' GH_TOKEN ']) {
			assert.equal(looksSecret(key), true, key);
		}
		for (const key of ['', 'KEY', 'SECRET', 'TOKEN', 'KEYS', 'API_KEYS', 'TOKEN_URL', 'SECRET_PATH', 'HOST', 'PASS', 'DB_PASS']) {
			assert.equal(looksSecret(key), false, key);
		}
	});

	test('paste: new keys that look secret, in order, once; the line names keys only', () => {
		const paste = 'HOST=x\nAPI_KEY=abc\n# OLD_TOKEN=y\nexport DB_PASSWORD="p"\nAPI_KEY=again\nGH_TOKEN=t\n';
		assert.deepEqual(secretLookingKeys(paste), ['API_KEY', 'DB_PASSWORD', 'GH_TOKEN']);
		assert.deepEqual(secretLookingKeys(paste, ['GH_TOKEN']), ['API_KEY', 'DB_PASSWORD']);
		assert.equal(looksSecretLine(['API_KEY', 'DB_PASSWORD']), 'Looks secret: API_KEY, DB_PASSWORD');
		assert.equal(looksSecretLine([]), '');
	});

	test('no Secret box ticks itself, whatever the key name (Joe 2026-11-20)', () => {
		assert.equal(initialAddDraft().secret, false);
		assert.equal(initialAddVariables().pasteSecret, false);
		assert.equal(setAddSecret({ ...initialAddDraft(), key: 'DB_PASSWORD' }, true).secret, true, 'the user can still tick it');
		const add = source('EnvAddVariablesDialog.svelte');
		assert.doesNotMatch(add, /suggestedSecret|addKeyTyped|pasteTyped|Touched/);
		const set = source('EnvSetDialog.svelte');
		assert.doesNotMatch(set, /suggestedSecret|Touched/);
		assert.match(set, /let newKeysSecret = \$state\(false\);/);
		assert.match(add, /const keyLooksSecret = \$derived\(!secretShown && looksSecret\(view\.draft\.key\)/);
		assert.match(add, /<p id=\{id\('add-looks-secret'\)\}/);
	});

	test('the dialogs show the line under the box and link it', () => {
		const add = source('EnvAddVariablesDialog.svelte');
		assert.match(add, /oninput=\{keyTyped\}/);
		assert.match(add, /aria-describedby=\{pasteLooksSecret \? id\('merge-looks-secret'\) : undefined\}/);
		const set = source('EnvSetDialog.svelte');
		assert.match(set, /aria-describedby=\{looksSecretText \? 'env-set-looks-secret' : undefined\}/);
	});
});

describe('filling placeholders in Add variables (api-v1 v1.27 item 4)', () => {
	test('a secret placeholder stays secret; a plain one stays plain and cannot be generated', () => {
		assert.deepEqual(placeholderRule('SMTP_PASSWORD', ['SMTP_PASSWORD'], ['SMTP_HOST']), { secret: true, canGenerate: true, note: 'A secret placeholder stays secret.' });
		assert.deepEqual(placeholderRule(' SMTP_HOST ', ['SMTP_PASSWORD'], ['SMTP_HOST']), { secret: false, canGenerate: false, note: 'This placeholder is plain; make it secret first to generate.' });
		assert.deepEqual(placeholderRule('NEW_KEY', ['SMTP_PASSWORD'], ['SMTP_HOST']), { secret: null, canGenerate: true, note: '' });
		const dialog = source('EnvAddVariablesDialog.svelte');
		assert.match(dialog, /generateDisabled=\{!placeholder\.canGenerate\}/);
		assert.match(readFileSync(new URL('../src/lib/components/ValueSourceFields.svelte', import.meta.url), 'utf8'), /disabled=\{generateDisabled\}/);
		assert.match(dialog, /<p id=\{id\('add-placeholder'\)\}[^>]*>\{placeholder\.note\}<\/p>/);
		assert.equal(/aria-label="Secret"/.test(dialog), false);
	});

	test('a failed add clears a typed value that was secret only because of the placeholder', () => {
		const draft = { ...initialAddDraft(), key: 'SMTP_PASSWORD', value: 'typed-secret', secret: false };
		const failed = addFailed(draft, 'refused', 'form', true);
		assert.equal(failed.value, '');
		assert.equal(failed.retype, true);
		assert.equal(addFailed(draft, 'refused').value, 'typed-secret');
		const view = addTabFailed({ ...initialAddVariables(), draft }, 'refused', true);
		assert.equal(view.draft.value, '');
	});
});
