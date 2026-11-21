import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, beforeEach, describe, test } from 'node:test';
import { isActionFailure } from '@sveltejs/kit';
import { render } from 'svelte/server';
import HostPage from '../src/routes/hosts/[id]/+page.svelte';
import { actions as hostActions, load as hostLoad } from '../src/routes/hosts/[id]/+page.server.ts';
import {
	applyLabel,
	applyOutcomeMessage,
	diffHunks,
	hasManagedUrl,
	hostFileHref,
	hostFileOutcomeBadge,
	hostFilesForm,
	hostFileStatusBadge,
	insertManagedUrl,
	keysAfter,
	latestVersion,
	lineDiff,
	parseSecretChanges,
	secretKeyProblem,
	secretValueProblem,
	stagedChanges,
	stepWatches,
	plainSaveAllowed,
	secretSaveBody,
	unsavedLeave,
	APPLY_LOST_MS,
	APPLY_LOST_TEXT,
	WORKER_BUSY_TEXT,
	type HostFile,
	type HostFilesTab
} from '../src/lib/host-files.ts';
import { secretInput } from '../src/lib/secret-input.ts';
import { HOST_TABS, hostQuery, parseHostTab } from '../src/lib/host-view.ts';
import { API_TIMEOUT_MS } from '../src/lib/server/api.ts';
import {
	clearHostFileRuns,
	HOST_FILE_APPLY_WAIT_MS,
	hostFileApplyTimings,
	hostFileRuns,
	parseHostFileDetail
} from '../src/lib/server/host-files.ts';
import { GET as fileAppliesGet } from '../src/routes/hosts/[id]/file-applies/+server.ts';
import { envelope, fakeEvent, installFetch, ORG, restoreEnv, TOKEN, useApi, USER, type FetchCall } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';
import type { Org } from '../src/lib/api-types.ts';

// api-v1 v1.44, umbrella spec-host-config-files §2.8.

const HOST_ID = '00000000-0000-4000-8000-0000000000aa';
const USER_ID = '00000000-0000-4000-8000-0000000000ee';
const FAKE = 's3cr3t-FAKE-VALUE';
const FILES = `/v1/orgs/${ORG.id}/hosts/${HOST_ID}/files`;

const HOST = {
	id: HOST_ID,
	org_id: ORG.id,
	name: 'pi3',
	node_type: 'pi',
	os: 'linux',
	arch: 'arm64',
	always_on: true,
	agent_version: '0.2.12',
	status: 'online',
	last_seen_at: '2026-11-22T00:00:00Z',
	enrolled_at: '2026-11-01T00:00:00Z',
	revoked_at: null,
	access_level: 'deploy',
	access: null
};

const POLICY = {
	name: 'dd-policy',
	path: '/etc/dd-worker/policy.toml',
	unit: 'dd-worker.service',
	secret: false,
	applied_version: 1,
	latest_version: 2,
	status: 'different',
	last_apply: { version: 1, outcome: 'succeeded', stage: null, reason: null, at: '2026-11-21T00:00:00Z' }
};
const SECRETS = {
	name: 'dd-secrets',
	path: '/etc/dd-worker/secrets.env',
	unit: 'dd-worker.service',
	secret: true,
	applied_version: null,
	latest_version: 1,
	status: 'unknown',
	last_apply: null
};
const V1 = 'repo = "a"\nlimit = 1\n';
const V2 = 'repo = "a"\nlimit = 2\n';

const ADMIN: Org[] = [ORG];
const MEMBER: Org[] = [{ ...ORG, role: 'member' }];

function handler(call: FetchCall): Response {
	const path = call.url.pathname;
	if (path === `/v1/hosts/${HOST_ID}`) return envelope(HOST);
	if (path === FILES) return envelope({ items: [POLICY, SECRETS] });
	if (path === `${FILES}/dd-policy`) {
		return envelope({
			content: V2,
			versions: [
				{ version: 2, size: V2.length, sha256: 'b'.repeat(64), by: USER_ID, at: '2026-11-22T00:00:00Z', applied: false },
				{ version: 1, size: V1.length, sha256: 'a'.repeat(64), by: USER_ID, at: '2026-11-21T00:00:00Z', applied: true }
			]
		});
	}
	if (path === `${FILES}/dd-policy/versions/1`) return envelope({ content: V1 });
	if (path === `${FILES}/dd-secrets`) {
		// A misbehaving API sending a value or a hash: neither may reach the page.
		return envelope({
			keys: [
				{ name: 'DD_TOKEN', set: true, value: FAKE },
				{ name: 'DD_EXTRA', set: false }
			],
			versions: [{ version: 1, size: 40, sha256: FAKE, by: USER_ID, at: '2026-11-22T00:00:00Z', applied: false }]
		});
	}
	return envelope(null, 500, 'error', 'unexpected');
}

async function load(url: string, orgs: Org[], fetchHandler: (call: FetchCall) => Response = handler) {
	const calls = installFetch(fetchHandler);
	const data = (await hostLoad(
		fakeEvent({ url, session: TOKEN, user: USER, orgs, params: { id: HOST_ID } }).event as never
	)) as Record<string, unknown> & { files: HostFilesTab | null };
	return { calls, data };
}

async function act(
	name: keyof typeof hostActions,
	form: Record<string, string>,
	fetchHandler: (call: FetchCall) => Response | Promise<Response>,
	orgs: Org[] = ADMIN
) {
	const calls = installFetch(fetchHandler);
	const result = await hostActions[name](
		fakeEvent({
			url: `http://console.example/hosts/${HOST_ID}?tab=files&files.name=${form.name ?? ''}&/${String(name)}`,
			session: TOKEN,
			user: USER,
			orgs,
			params: { id: HOST_ID },
			form
		}).event as never
	);
	return { calls, result };
}

function renderPage(data: Record<string, unknown>, form: unknown = null): string {
	setPageUrl(`http://console.example/hosts/${HOST_ID}?tab=files`);
	return render(HostPage, { props: { data: data as never, form: form as never } }).body.replaceAll('&amp;', '&').replace(/<!--[\s\S]*?-->/g, '');
}

describe('host config files: helpers', () => {
	test('the files tab parses and is listed', () => {
		assert.deepEqual([...HOST_TABS], ['overview', 'processes', 'files']);
		assert.equal(parseHostTab('files'), 'files');
		assert.equal(parseHostTab('nope'), 'overview');
		assert.equal(hostQuery(new URL(`http://x/hosts/${HOST_ID}?tab=files&files.name=dd-policy`)).tab, 'files');
		assert.equal(hostFileHref(HOST_ID, 'dd-policy'), `/hosts/${HOST_ID}?tab=files&files.name=dd-policy`);
	});

	test('status and outcome words', () => {
		assert.deepEqual(hostFileStatusBadge('in_sync'), { status: 'up', text: 'in sync' });
		assert.deepEqual(hostFileStatusBadge('different'), { status: 'asleep', text: 'different' });
		assert.deepEqual(hostFileStatusBadge('missing'), { status: 'down', text: 'missing' });
		assert.deepEqual(hostFileStatusBadge('wrong_owner'), { status: 'down', text: 'wrong owner' });
		assert.deepEqual(hostFileStatusBadge('???'), { status: 'unknown', text: 'unknown' });
		assert.deepEqual(hostFileOutcomeBadge('check_failed'), { status: 'failed', text: 'check failed' });
		assert.deepEqual(hostFileOutcomeBadge('rolled_back'), { status: 'rolled_back', text: 'rolled back' });
		assert.equal(applyLabel(1, 3), 'Roll back to v1');
		assert.equal(applyLabel(3, 3), 'Apply v3');
	});

	test('apply outcome text, with the reason token in monospace', () => {
		const text = (outcome: string, stage: string | null = null, reason: string | null = null) => {
			const message = applyOutcomeMessage(4, { outcome, stage, reason, took_ms: 10 });
			return { kind: message.kind, text: message.parts.map((part) => part.text).join(''), mono: message.parts.filter((part) => part.mono).map((part) => part.text) };
		};
		assert.deepEqual(text('succeeded'), { kind: 'success', text: 'Applied v4.', mono: [] });
		assert.deepEqual(text('unchanged'), { kind: 'info', text: 'Already on the host; nothing restarted.', mono: [] });
		assert.deepEqual(text('check_failed', 'check', 'bad_repo'), { kind: 'error', text: 'The check refused the file: bad_repo', mono: ['bad_repo'] });
		assert.deepEqual(text('rolled_back', 'health', 'not_connected'), {
			kind: 'error',
			text: 'The new file failed (health: not_connected); the previous file is back.',
			mono: ['health: not_connected']
		});
		assert.equal(text('rollback_failed').text, 'The new file failed and the previous file could not be restored. Check the host.');
		assert.equal(text('rollback_failed').kind, 'error');
		assert.equal(text('forbidden_spec').text, 'The host refused this file under its policy (runner.toml).');
	});

	test('line diff marks removed and added lines and folds long unchanged runs', () => {
		assert.deepEqual(lineDiff(V1, V2), [
			{ op: ' ', text: 'repo = "a"' },
			{ op: '-', text: 'limit = 1' },
			{ op: '+', text: 'limit = 2' },
			{ op: ' ', text: '' }
		]);
		assert.deepEqual(lineDiff('', 'a'), [{ op: '+', text: 'a' }]);
		assert.deepEqual(lineDiff('a\nb\nc', 'a\nc\nd'), [
			{ op: ' ', text: 'a' },
			{ op: '-', text: 'b' },
			{ op: ' ', text: 'c' },
			{ op: '+', text: 'd' }
		]);
		const long = Array.from({ length: 20 }, (_, index) => `l${index}`);
		const changed = [...long];
		changed[10] = 'changed';
		const hunks = diffHunks(lineDiff(long.join('\n'), changed.join('\n')), 2);
		assert.deepEqual(hunks[0], { op: 'skip', count: 8 });
		assert.deepEqual(hunks.at(-1), { op: 'skip', count: 7 });
		assert.equal(hunks.filter((item) => item.op === '-' || item.op === '+').length, 2);
	});

	test('Insert Deploybot link adds one top-level managed_url line and keeps the rest', () => {
		const url = `https://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy`;
		assert.equal(insertManagedUrl('a = 1', url), `a = 1\nmanaged_url = "${url}"\n`);
		assert.equal(insertManagedUrl('a = 1\n', url), `a = 1\nmanaged_url = "${url}"\n`);
		assert.equal(insertManagedUrl('', url), `managed_url = "${url}"\n`);
		// Before the first table, so it stays a top-level key.
		assert.equal(insertManagedUrl('# c\na = 1\n[repo]\nx = 2\n', url), `# c\na = 1\nmanaged_url = "${url}"\n[repo]\nx = 2\n`);
		assert.equal(insertManagedUrl('a = 1\n[[bin]]\nx = 1\n', url), `a = 1\nmanaged_url = "${url}"\n[[bin]]\nx = 1\n`);
		assert.equal(insertManagedUrl('a = 1\n[repo] # main\n', url), `a = 1\nmanaged_url = "${url}"\n[repo] # main\n`);
		// A line of a multi-line array is not a table header.
		assert.equal(insertManagedUrl('ports = [\n  [1, 2],\n]\n', url), `ports = [\n  [1, 2],\n]\nmanaged_url = "${url}"\n`);
		assert.equal(hasManagedUrl(insertManagedUrl('a = 1', url)), true);
		assert.equal(hasManagedUrl('a = 1'), false);
	});

	test('latest version is the higher of the list and the versions', () => {
		assert.equal(latestVersion(2, [{ version: 1 }, { version: 3 }]), 3);
		assert.equal(latestVersion(4, [{ version: 1 }]), 4);
		assert.equal(latestVersion(null, []), null);
	});

	test('staged secret changes: one body, at least one key left, checked again on the server', () => {
		const staged = { B_KEY: { op: 'remove' as const }, A_KEY: { op: 'set' as const, value: FAKE } };
		assert.deepEqual(stagedChanges(staged), { set: { A_KEY: FAKE }, remove: ['B_KEY'] });
		assert.equal(keysAfter([{ name: 'B_KEY', set: true }], staged), 1);
		assert.equal(keysAfter([{ name: 'B_KEY', set: true }], { B_KEY: { op: 'remove' } }), 0);
		assert.deepEqual(parseSecretChanges(JSON.stringify({ set: { A_KEY: 'x' }, remove: ['B_KEY'] })), { set: { A_KEY: 'x' }, remove: ['B_KEY'] });
		assert.equal(parseSecretChanges(JSON.stringify({ set: {}, remove: [] })), 'Nothing to save.');
		assert.equal(typeof parseSecretChanges(JSON.stringify({ set: { bad: 'x' } })), 'string');
		assert.equal(typeof parseSecretChanges(JSON.stringify({ set: { A_KEY: 'a\nb' } })), 'string');
		assert.equal(parseSecretChanges(JSON.stringify({ set: { A_KEY: 'x' }, remove: ['A_KEY'] })), 'A_KEY is both set and removed.');
		assert.equal(parseSecretChanges('nope'), 'Nothing to save.');
		// A rejected value is never named in the message.
		assert.equal(String(parseSecretChanges(JSON.stringify({ set: { A_KEY: `${FAKE}\n` } }))).includes(FAKE), false);
	});

	test('the poll step: a finished run settles, a lost one falls back to the last apply, then gives up', () => {
		const item = (at: string | null): HostFile => ({
			...(POLICY as unknown as HostFile),
			last_apply: at ? { version: 2, outcome: 'rolled_back', stage: 'health', reason: 'not_connected', at } : null
		});
		const since = Date.parse('2026-11-22T00:00:00Z');
		const watch = { name: 'dd-policy', version: 2, since, lastApplyAt: '2026-11-21T00:00:00Z' };
		const done = { apply: { name: 'dd-policy', version: 2, busy: false, result: { outcome: 'succeeded', stage: null, reason: null, took_ms: 1 } }, message: null };
		const finished = stepWatches([watch], [{ name: 'dd-policy', version: 2, startedAt: 'x', done }], [], since + 2000);
		assert.deepEqual(finished, { watches: [], outcomes: [{ name: 'dd-policy', done }], lost: false });
		const going = stepWatches([watch], [{ name: 'dd-policy', version: 2, startedAt: 'x', done: null }], [], since + 2000);
		assert.deepEqual(going.watches, [watch]);
		const lostSame = stepWatches([watch], [], [item('2026-11-21T00:00:00Z')], since + 2000);
		assert.equal(lostSame.lost, true);
		assert.deepEqual(lostSame.watches, [watch]);
		const lostChanged = stepWatches([watch], [], [item('2026-11-22T01:00:00Z')], since + 2000);
		assert.equal(lostChanged.outcomes[0]?.done.apply?.result?.outcome, 'rolled_back');
		const gaveUp = stepWatches([watch], [], [item('2026-11-21T00:00:00Z')], since + APPLY_LOST_MS + 1);
		// A reload's watch has the current last apply as its snapshot: one newer than its start still settles it.
		const reloaded = stepWatches([{ ...watch, lastApplyAt: '2026-11-22T00:05:00Z' }], [], [item('2026-11-22T00:05:00Z')], since + 400_000);
		assert.equal(reloaded.outcomes.length, 1);
		assert.deepEqual(gaveUp.outcomes, [{ name: 'dd-policy', done: { apply: null, message: APPLY_LOST_TEXT } }]);
	});

	test('the secret value input is masked, never bound, and the dialog only stages', () => {
		assert.equal(secretInput(true, false).type, 'password');
		assert.equal(secretInput(true, true).maskClass.length > 0, true);
		const source = readFileSync(new URL('../src/routes/hosts/[id]/SecretKeyDialog.svelte', import.meta.url), 'utf8');
		const valueInput = source.slice(source.indexOf('name="value"'), source.indexOf('/>', source.indexOf('name="value"')));
		assert.match(valueInput, /type=\{valueInput\.type\}/);
		assert.doesNotMatch(valueInput, /value=|bind:value/);
		assert.doesNotMatch(source, /method="POST"|use:enhance/);
	});

	test('editors never carry a draft or staged keys to another file', () => {
		// The page remounts the editor per file ({#key file.name}), so its state starts empty for file B.
		const tab = readFileSync(new URL('../src/routes/hosts/[id]/ConfigFilesTab.svelte', import.meta.url), 'utf8');
		const keyed = tab.slice(tab.indexOf('{#key file.name}'), tab.indexOf('{/key}'));
		assert.match(keyed, /<PlainFileEditor/);
		assert.match(keyed, /<SecretFileEditor/);
		// And even a reused editor posts nothing for another file than the one it was opened for.
		const staged = { DD_TOKEN: { op: 'set' as const, value: FAKE } };
		const keys = [{ name: 'DD_TOKEN', set: true }];
		assert.deepEqual(secretSaveBody('dd-secrets', 'dd-secrets', keys, staged), { set: { DD_TOKEN: FAKE }, remove: [] });
		assert.equal(secretSaveBody('dd-secrets', 'dd-other', keys, staged), null);
		assert.equal(secretSaveBody('dd-secrets', 'dd-secrets', keys, {}), null);
		assert.equal(secretSaveBody('dd-secrets', 'dd-secrets', keys, { DD_TOKEN: { op: 'remove' } }), null);
		assert.equal(plainSaveAllowed('dd-policy', 'dd-policy', V1, V2), true);
		assert.equal(plainSaveAllowed('dd-policy', 'dd-other', V1, V2), false);
		assert.equal(plainSaveAllowed('dd-policy', 'dd-policy', V2, V2), false);
		for (const file of ['PlainFileEditor', 'SecretFileEditor']) {
			const source = readFileSync(new URL(`../src/routes/hosts/[id]/${file}.svelte`, import.meta.url), 'utf8');
			assert.match(source, /beforeNavigate\(/, `${file} guards unsaved changes`);
			assert.match(source, /unsavedLeave\(/, `${file} uses the shared leave rule`);
		}
	});

	test('leaving with unsaved changes: same file goes, unload prompts, a link warns once', () => {
		const here = new URL(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-secrets`);
		const to = (query: string) => new URL(`http://console.example/hosts/${HOST_ID}?${query}`);
		const base = { unsaved: true, saving: false, type: 'link', here, name: 'dd-secrets', warned: false };
		assert.equal(unsavedLeave({ ...base, unsaved: false, to: to('tab=files&files.name=dd-policy') }), 'go');
		assert.equal(unsavedLeave({ ...base, saving: true, to: to('tab=files&files.name=dd-policy') }), 'go');
		assert.equal(unsavedLeave({ ...base, to: to('tab=files&files.name=dd-secrets&filekeys.q=DD') }), 'go');
		assert.equal(unsavedLeave({ ...base, to: to('tab=files&files.name=dd-policy') }), 'warn');
		assert.equal(unsavedLeave({ ...base, warned: true, to: to('tab=files&files.name=dd-policy') }), 'go');
		assert.equal(unsavedLeave({ ...base, type: 'leave', to: null }), 'prompt');
	});

	test('secret key and value rules', () => {
		assert.equal(secretKeyProblem('DD_TOKEN'), null);
		assert.equal(secretKeyProblem('_X1'), null);
		assert.notEqual(secretKeyProblem('dd_token'), null);
		assert.notEqual(secretKeyProblem('1X'), null);
		assert.notEqual(secretKeyProblem('A'.repeat(65)), null);
		assert.equal(secretValueProblem('x'), null);
		assert.notEqual(secretValueProblem(''), null);
		assert.notEqual(secretValueProblem('a\nb'), null);
		assert.notEqual(secretValueProblem('a\0b'), null);
		assert.notEqual(secretValueProblem('x'.repeat(4097)), null);
	});

	test('a secret detail keeps key names only; the form reader ignores other actions', () => {
		const detail = parseHostFileDetail({ keys: [{ name: 'K', set: true, value: FAKE }], versions: [{ version: 1, size: 3, sha256: FAKE, by: USER_ID, at: 'x', applied: true }] }, true);
		assert.equal(JSON.stringify(detail).includes(FAKE), false);
		assert.deepEqual(hostFilesForm({ message: 'other' }), { message: null, field: null, content: null, apply: null, saved: null, running: null });
		assert.equal(hostFilesForm({ filesMessage: 'x', filesField: 'key:add' }).field, 'key:add');
	});
});

describe('host config files: load and actions', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(async () => {
		useApi('https://api.test');
		// undici registers its global Agent on its first fetch, as the console's first API call does in production.
		await originalFetch('data:,x');
	});
	beforeEach(() => {
		clearHostFileRuns();
		hostFileApplyTimings.fastWaitMs = 25_000;
	});
	after(() => {
		globalThis.fetch = originalFetch;
		hostFileApplyTimings.fastWaitMs = 25_000;
		clearHostFileRuns();
		restoreEnv();
	});

	test('a member gets the list only, even with files.name', async () => {
		const { calls, data } = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy`, MEMBER);
		assert.equal(data.tab, 'files');
		assert.equal(data.files?.canEdit, false);
		assert.deepEqual(data.files?.items.map((item) => item.name), ['dd-policy', 'dd-secrets']);
		assert.equal(data.files?.selected, null);
		assert.deepEqual(data.files?.runs, []);
		assert.deepEqual(
			calls.map((call) => call.url.pathname).filter((path) => path.includes('/files')),
			[FILES]
		);
	});

	test('an admin opening a plain file reads its detail and the applied version for the diff', async () => {
		const { calls, data } = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy&files.v=1&files.mode=diff`, ADMIN);
		const selected = data.files?.selected;
		assert.equal(selected?.file.name, 'dd-policy');
		assert.equal(selected?.detail?.secret, false);
		assert.equal(selected?.appliedContent, V1);
		assert.deepEqual(selected?.view, { version: 1, content: V1, mode: 'diff' });
		assert.ok(calls.some((call) => call.url.pathname === `${FILES}/dd-policy/versions/1`));
	});

	test('a secret file loads key names and never a value or hash', async () => {
		const { data } = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-secrets`, ADMIN);
		const detail = data.files?.selected?.detail;
		assert.equal(detail?.secret, true);
		assert.deepEqual(detail && detail.secret ? detail.keys : null, [
			{ name: 'DD_TOKEN', set: true },
			{ name: 'DD_EXTRA', set: false }
		]);
		assert.equal(JSON.stringify(data).includes(FAKE), false);
	});

	test('a host without an org reads nothing and says so', async () => {
		const { calls, data } = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, ADMIN, (call) =>
			call.url.pathname === `/v1/hosts/${HOST_ID}` ? envelope({ ...HOST, org_id: null }) : envelope(null, 500, 'error', 'unexpected')
		);
		assert.equal(data.files?.available, false);
		assert.equal(calls.some((call) => call.url.pathname.includes('/files')), false);
		assert.match(renderPage(data), /Config files are not available for this host/);
	});

	test('Save sends the text as typed (LF), and a failure keeps it', async () => {
		const ok = await act('saveFile', { org: ORG.id, name: 'dd-policy', content: 'a = 1\r\nb = 2\r\n' }, (call) => {
			assert.equal(call.method, 'PUT');
			assert.equal(call.url.pathname, `${FILES}/dd-policy`);
			assert.deepEqual(JSON.parse(call.body ?? ''), { content: 'a = 1\nb = 2\n' });
			return envelope({ version: 3 });
		});
		assert.deepEqual(ok.result, { hostFileSaved: { name: 'dd-policy', version: 3 } });
		const bad = await act('saveFile', { org: ORG.id, name: 'dd-policy', content: 'oops' }, () => envelope(null, 400, 'bad_request', 'content is not UTF-8'));
		assert.ok(isActionFailure(bad.result));
		assert.deepEqual(bad.result.data, { filesMessage: 'content is not UTF-8', content: 'oops' });
		const empty = await act('saveFile', { org: ORG.id, name: 'dd-policy', content: '' }, () => envelope({ version: 9 }));
		assert.equal(empty.calls.length, 0);
		assert.ok(isActionFailure(empty.result));
		const nul = await act('saveFile', { org: ORG.id, name: 'dd-policy', content: 'a\0b' }, () => envelope({ version: 9 }));
		assert.equal(nul.calls.length, 0);
		assert.ok(isActionFailure(nul.result));
	});

	test('Save of a secret file sends all staged changes in one PUT and never returns a value', async () => {
		const changes = JSON.stringify({ set: { DD_TOKEN: FAKE, NEW_KEY: FAKE }, remove: ['DD_EXTRA'] });
		const ok = await act('saveSecretFile', { org: ORG.id, name: 'dd-secrets', base_version: '1', changes }, (call) => {
			assert.equal(call.method, 'PUT');
			assert.deepEqual(JSON.parse(call.body ?? ''), { base_version: 1, set: { DD_TOKEN: FAKE, NEW_KEY: FAKE }, remove: ['DD_EXTRA'] });
			return envelope({ version: 2 });
		});
		assert.deepEqual(ok.result, { hostFileSaved: { name: 'dd-secrets', version: 2 } });
		assert.equal(ok.calls.length, 1);
		const conflict = await act('saveSecretFile', { org: ORG.id, name: 'dd-secrets', base_version: '0', changes }, () =>
			envelope(null, 409, 'conflict', 'The file changed since you opened it.')
		);
		assert.ok(isActionFailure(conflict.result));
		assert.equal(conflict.result.status, 409);
		assert.deepEqual(conflict.result.data, { filesMessage: 'The file changed since you opened it.', filesField: 'secret' });
		assert.equal(JSON.stringify(conflict.result).includes(FAKE), false);
		const twoLines = await act(
			'saveSecretFile',
			{ org: ORG.id, name: 'dd-secrets', base_version: '1', changes: JSON.stringify({ set: { DD_TOKEN: `${FAKE}\nx` } }) },
			() => envelope({ version: 9 })
		);
		assert.equal(twoLines.calls.length, 0);
		assert.ok(isActionFailure(twoLines.result));
		assert.equal(JSON.stringify(twoLines.result).includes(FAKE), false);
	});

	test('apply: the fast path answers the outcome; the call may wait past the API\'s 610 s', async () => {
		assert.ok(HOST_FILE_APPLY_WAIT_MS > 610_000);
		assert.ok(HOST_FILE_APPLY_WAIT_MS > API_TIMEOUT_MS);
		assert.ok(APPLY_LOST_MS > 610_000);
		const ok = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '1' }, (call) => {
			assert.equal(call.url.pathname, `${FILES}/dd-policy/apply`);
			assert.deepEqual(JSON.parse(call.body ?? ''), { version: 1 });
			return envelope({ outcome: 'rolled_back', stage: 'health', reason: 'not_connected', took_ms: 61000 });
		});
		assert.deepEqual(ok.result, {
			hostFileApply: { name: 'dd-policy', version: 1, busy: false, result: { outcome: 'rolled_back', stage: 'health', reason: 'not_connected', took_ms: 61000 } }
		});
		// Answered in time: nothing is left to poll.
		assert.deepEqual(hostFileRuns(ORG.id, HOST_ID), []);
		const busy = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, () =>
			envelope({ outcome: 'worker_busy', stage: 'idle', reason: 'busy', took_ms: 5 }, 409, 'worker_busy', 'busy')
		);
		assert.ok(isActionFailure(busy.result));
		assert.equal(busy.result.status, 409);
		const busyData = busy.result.data as unknown as { filesMessage: string; hostFileApply: { busy: boolean } };
		assert.equal(busyData.hostFileApply.busy, true);
		assert.equal(busyData.filesMessage, WORKER_BUSY_TEXT);
		const forced = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2', force: '1' }, (call) => {
			assert.deepEqual(JSON.parse(call.body ?? ''), { version: 2, force: true });
			return envelope({ outcome: 'succeeded', stage: null, reason: null, took_ms: 30000 });
		});
		assert.equal((forced.result as { hostFileApply: { result: { outcome: string } } }).hostFileApply.result.outcome, 'succeeded');
		const offline = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, () => envelope(null, 503, 'host_offline', 'pi3 is offline.'));
		assert.ok(isActionFailure(offline.result));
		assert.deepEqual(offline.result.data, { filesMessage: 'pi3 is offline.' });
	});

	test('apply: a slow one answers running, the poll and a reload see it, then pick up the result', async () => {
		hostFileApplyTimings.fastWaitMs = 20;
		let finish: (response: Response) => void = () => {};
		const slow = new Promise<Response>((resolve) => (finish = resolve));
		const started = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, (call) => (call.url.pathname.endsWith('/apply') ? slow : handler(call)));
		const running = (started.result as { hostFileRunning: { name: string; version: number; done: unknown } }).hostFileRunning;
		assert.equal(running.name, 'dd-policy');
		assert.equal(running.version, 2);
		assert.equal(running.done, null);

		// A second Apply while it runs starts nothing new.
		const again = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, () => assert.fail('no second apply call'));
		assert.equal(again.calls.length, 0);
		assert.ok('hostFileRunning' in (again.result as object));

		// A reload while it runs: the load lists the run and the page shows the progress note.
		const reload = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy`, ADMIN);
		assert.equal(reload.data.files?.runs.length, 1);
		assert.equal(reload.data.files?.runs[0]?.done, null);
		assert.match(renderPage(reload.data), /Checking the file, restarting dd-worker\.service, confirming it is healthy/);
		// Members get no runs.
		const member = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, MEMBER);
		assert.deepEqual(member.data.files?.runs, []);

		const poll = async (orgs: Org[]) => {
			const { event } = fakeEvent({ url: `http://console.example/hosts/${HOST_ID}/file-applies?org=${ORG.id}`, session: TOKEN, user: USER, orgs, params: { id: HOST_ID } });
			const response = await fileAppliesGet(event as never);
			return { status: response.status, body: (await response.json()) as { runs: Array<{ done: unknown }> } };
		};
		assert.equal((await poll(MEMBER)).status, 403);
		assert.equal((await poll(ADMIN)).body.runs[0]?.done, null);

		finish(envelope({ outcome: 'succeeded', stage: null, reason: null, took_ms: 200000 }));
		await new Promise((resolve) => setTimeout(resolve, 10));
		const after = await poll(ADMIN);
		assert.deepEqual(after.body.runs[0]?.done, {
			apply: { name: 'dd-policy', version: 2, busy: false, result: { outcome: 'succeeded', stage: null, reason: null, took_ms: 200000 } },
			message: null
		});
	});

	test('apply: a call lost after the action answered keeps the run as unknown; the last apply settles it', async () => {
		hostFileApplyTimings.fastWaitMs = 20;
		let lose: (err: Error) => void = () => {};
		const lost = new Promise<Response>((_, reject) => (lose = reject));
		const started = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, (call) => (call.url.pathname.endsWith('/apply') ? lost : handler(call)));
		const running = (started.result as { hostFileRunning: { startedAt: string } }).hostFileRunning;
		// undici's headers timeout surfaces as a failed fetch, not an API answer.
		lose(Object.assign(new TypeError('fetch failed'), { cause: { code: 'UND_ERR_HEADERS_TIMEOUT' } }));
		await new Promise((resolve) => setTimeout(resolve, 10));
		const { event } = fakeEvent({ url: `http://console.example/hosts/${HOST_ID}/file-applies?org=${ORG.id}`, session: TOKEN, user: USER, orgs: ADMIN, params: { id: HOST_ID } });
		const body = (await (await fileAppliesGet(event as never)).json()) as { runs: Array<{ done: unknown; unknown: boolean }> };
		assert.equal(body.runs.length, 1);
		assert.equal(body.runs[0]?.done, null);
		assert.equal(body.runs[0]?.unknown, true);

		const since = Date.parse(running.startedAt);
		const watch = { name: 'dd-policy', version: 2, since, lastApplyAt: POLICY.last_apply.at };
		const listed = (at: string): HostFile => ({
			...(POLICY as unknown as HostFile),
			last_apply: { version: 2, outcome: 'succeeded', stage: null, reason: null, at }
		});
		// Still the old last apply: keep waiting and read the list again.
		const waiting = stepWatches([watch], body.runs as never, [listed(POLICY.last_apply.at)], since + 3000);
		assert.deepEqual(waiting.watches, [watch]);
		assert.equal(waiting.lost, true);
		// The API recorded the apply: the watch settles with its outcome.
		const done = stepWatches([watch], body.runs as never, [listed(new Date(since + 200_000).toISOString())], since + 203_000);
		assert.equal(done.outcomes[0]?.done.apply?.result?.outcome, 'succeeded');
		assert.deepEqual(done.watches, []);
		// The unknown run does not block a new apply (the API answers conflict if one still runs).
		hostFileApplyTimings.fastWaitMs = 25_000;
		const again = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, () => envelope(null, 409, 'conflict', 'dd-policy is already being applied on dd-worker.service.'));
		assert.equal(again.calls.length, 1);
		assert.ok(isActionFailure(again.result));
		assert.deepEqual(again.result.data, { filesMessage: 'dd-policy is already being applied on dd-worker.service.' });
	});

	test('apply runs belong to one org: members and other orgs cannot start, see or replace one', async () => {
		const OTHER: Org = { ...ORG, id: '00000000-0000-4000-8000-0000000000b0', name: 'Other', slug: 'other' };
		hostFileApplyTimings.fastWaitMs = 20;
		const slow = new Promise<Response>(() => {});
		await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '2' }, (call) => (call.url.pathname.endsWith('/apply') ? slow : handler(call)));
		assert.equal(hostFileRuns(ORG.id, HOST_ID).length, 1);

		// A member of the org, and an admin of another org naming this org: refused before the Map or the API.
		for (const orgs of [MEMBER, [OTHER]]) {
			const refused = await act('applyFile', { org: ORG.id, name: 'dd-policy', version: '1' }, () => assert.fail('no API call'), orgs);
			assert.equal(refused.calls.length, 0);
			assert.ok(isActionFailure(refused.result));
			assert.equal(refused.result.status, 403);
			assert.equal(JSON.stringify(refused.result).includes('hostFileRunning'), false);
		}
		// Another org's admin posting their own org gets their own key; the API refuses the host, the run stays.
		const elsewhere = await act('applyFile', { org: OTHER.id, name: 'dd-policy', version: '1' }, () => envelope(null, 404, 'not_found', 'Not found.'), [OTHER]);
		assert.equal(elsewhere.calls.length, 1);
		assert.ok(isActionFailure(elsewhere.result));
		const kept = hostFileRuns(ORG.id, HOST_ID);
		assert.equal(kept.length, 1);
		assert.equal(kept[0]?.version, 2);
		assert.deepEqual(hostFileRuns(OTHER.id, HOST_ID), []);

		// Neither sees it through the poll route or the load.
		const poll = async (orgs: Org[], org: string) => {
			const { event } = fakeEvent({ url: `http://console.example/hosts/${HOST_ID}/file-applies?org=${org}`, session: TOKEN, user: USER, orgs, params: { id: HOST_ID } });
			const response = await fileAppliesGet(event as never);
			return { status: response.status, body: (await response.json()) as { runs?: unknown[] } };
		};
		assert.equal((await poll([OTHER], ORG.id)).status, 403);
		assert.deepEqual((await poll([OTHER], OTHER.id)).body.runs, []);
		assert.equal((await poll(MEMBER, ORG.id)).status, 403);
		const memberLoad = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, MEMBER);
		assert.deepEqual(memberLoad.data.files?.runs, []);
		const otherLoad = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, [OTHER]);
		assert.deepEqual(otherLoad.data.files?.runs, []);
		const adminLoad = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, ADMIN);
		assert.equal(adminLoad.data.files?.runs.length, 1);
	});

	test('the page: list for members, editor and versions for admins, keys without values, outcomes', async () => {
		const member = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy`, MEMBER);
		const memberHtml = renderPage(member.data);
		assert.match(memberHtml, /aria-current="page"[^>]*>Config files/);
		assert.match(memberHtml, /\/etc\/dd-worker\/policy\.toml/);
		assert.match(memberHtml, />\s*different\s*</);
		assert.equal(memberHtml.includes('limit = 2'), false);
		assert.equal(memberHtml.includes('Apply v'), false);
		assert.equal(memberHtml.includes('Open dd-policy'), false);

		const admin = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-policy`, ADMIN);
		const adminHtml = renderPage(admin.data);
		assert.match(adminHtml, /name="content"/);
		assert.match(adminHtml, /limit = 2/);
		assert.match(adminHtml, /data-op="remove">- limit = 1</);
		assert.match(adminHtml, /data-op="add">\+ limit = 2</);
		assert.match(adminHtml, /Insert Deploybot link/);
		// The managed_url line appears only when the admin clicks the button.
		assert.equal(adminHtml.includes('managed_url'), false);
		assert.match(adminHtml, /Apply v2/);
		assert.match(adminHtml, /Roll back to v1/);
		assert.match(adminHtml, /View v1/);
		assert.match(adminHtml, /data-sveltekit-noscroll/);
		// One live region for progress and outcome, there before any apply.
		assert.match(adminHtml, /aria-live="polite"[^>]*data-apply-status/);

		const busyHtml = renderPage(admin.data, { filesMessage: WORKER_BUSY_TEXT, hostFileApply: { name: 'dd-policy', version: 2, busy: true, result: null } });
		assert.match(busyHtml, /The program on this host is running a job\. Applying now interrupts it\./);
		assert.match(busyHtml, /Apply anyway/);

		const failedHtml = renderPage(admin.data, {
			hostFileApply: { name: 'dd-policy', version: 2, busy: false, result: { outcome: 'check_failed', stage: 'check', reason: 'bad_repo', took_ms: 4 } }
		});
		assert.match(failedHtml, /The check refused the file: <code class="font-mono">bad_repo<\/code>/);

		const keptHtml = renderPage(admin.data, { filesMessage: 'too big', content: 'my unsaved text' });
		assert.match(keptHtml, /my unsaved text/);

		const secret = await load(`http://console.example/hosts/${HOST_ID}?tab=files&files.name=dd-secrets`, ADMIN);
		const secretHtml = renderPage(secret.data, { filesMessage: 'conflict', filesField: 'secret' });
		assert.match(secretHtml, /DD_TOKEN/);
		assert.match(secretHtml, />\s*unset\s*</);
		assert.match(secretHtml, /Add key/);
		assert.equal(secretHtml.includes(FAKE), false);
		assert.equal(secretHtml.includes('name="content"'), false);
	});

	test('an empty list says the host declares no config files', async () => {
		const { data } = await load(`http://console.example/hosts/${HOST_ID}?tab=files`, ADMIN, (call) =>
			call.url.pathname === FILES ? envelope({ items: [] }) : handler(call)
		);
		assert.match(renderPage(data), /This host declares no config files/);
	});
});
