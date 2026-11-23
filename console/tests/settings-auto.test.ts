import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isHttpError, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { Registry } from '../src/lib/api-types.ts';
import {
	accessLine,
	envSetUsers,
	fileUsers,
	imageRegistryHost,
	registryUsers,
	usedByText,
	fixHref,
	fixLabel,
	githubFailureCode,
	githubNoteCode,
	GITHUB_NOTES,
	isGitHubAuthorizeUrl,
	parseHostPolicySnippet,
	parseImageAccess,
	parseRefusal,
	refusalText,
	refusedTitle,
	parseReadiness,
	readinessSummary,
	type UsedByEntry,
	type AccessLineState
} from '../src/lib/settings-auto.ts';
import { parseRegistry } from '../src/lib/server/shape.ts';
import { githubConnectOf } from '../src/lib/server/image-actions.ts';
import { githubRefused } from '../src/lib/server/settings-auto.ts';
import { GET as githubCallback } from '../src/routes/auth/github/callback/+server.ts';
import { actions as projectActions, load as projectLoad } from '../src/routes/projects/[id]/+page.server.ts';
import SettingsSection from '../src/routes/projects/[id]/SettingsSection.svelte';
import ReadinessList from '../src/routes/projects/[id]/ReadinessList.svelte';
import ReadinessView from '../src/routes/projects/[id]/ReadinessView.svelte';
import ImageAccessLine from '../src/routes/projects/[id]/ImageAccessLine.svelte';
import GitHubConnect from '../src/routes/projects/[id]/GitHubConnect.svelte';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const originalFetch = globalThis.fetch;
const STATE = 'S'.repeat(43);
const AUTHORIZE = 'https://github.com/login/oauth/authorize?client_id=c&scope=read%3Apackages&state=' + STATE;
const LEAK = 'gho_SECRETtokenVALUE';

function redirectOf(err: unknown): { status: number; location: string } {
	assert.equal(isRedirect(err), true, String(err));
	const r = err as { status: number; location: string };
	return { status: r.status, location: r.location };
}

test('image access: parse keeps three fields and the line says what to do', () => {
	assert.deepEqual(parseImageAccess({ registry: 'ghcr.io', access: 'private_ok', login: 'github', token: LEAK }), { registry: 'ghcr.io', access: 'private_ok', login: 'github' });
	assert.equal(parseImageAccess({ registry: 'ghcr.io', access: 'maybe', login: 'none' }), null);
	assert.equal(parseImageAccess({ registry: 'Bad Host', access: 'public', login: 'none' }), null);
	assert.equal(accessLine({ registry: 'docker.io', access: 'public', login: 'none' }).text, 'Public image, no login needed.');
	assert.equal(accessLine({ registry: 'ghcr.io', access: 'private_ok', login: 'github' }).text, 'Private, uses the GitHub connection.');
	assert.deepEqual(accessLine({ registry: 'ghcr.io', access: 'private_no_login', login: 'none' }), { text: 'Private or missing: Connect GitHub.', connect: true, settings: false });
	assert.deepEqual(accessLine({ registry: 'quay.io', access: 'private_no_login', login: 'none' }).settings, true);
	assert.equal(accessLine({ registry: 'ghcr.io', access: 'login_failed', login: 'github' }).connect, true);
	assert.equal(accessLine({ registry: 'ghcr.io', access: 'unknown', login: 'none' }).text, 'Image access could not be checked.');
});

test('readiness: only allowlisted fields survive, and fixes map to buttons and links', () => {
	const parsed = parseReadiness({
		ready: false,
		value: LEAK,
		checks: [
			{ check: 'env_values', status: 'missing', message: 'env A in set app has no value yet; set it in the console', fix: { action: 'fill_value', set: 'app', key: 'A', value: LEAK }, preview: LEAK },
			{ check: 'registry', status: 'missing', message: 'private or missing; connect GitHub', fix: { action: 'connect_github', registry: 'ghcr.io' } },
			{ check: 'host_access', status: 'missing', message: 'no access', fix: { action: 'grant_deploy_access', host_id: '00000000-0000-4000-8000-0000000000aa' } },
			{ check: 'host_policy', status: 'unknown', message: 'checked at deploy', fix: { action: 'teleport' } }
		]
	});
	assert.ok(parsed);
	assert.equal(JSON.stringify(parsed).includes(LEAK), false);
	assert.deepEqual(parsed.checks[0].fix, { action: 'fill_value', set: 'app', key: 'A' });
	assert.deepEqual(parsed.checks[3].fix, { action: 'none' });
	assert.equal(readinessSummary(parsed), '3 items missing. You can still deploy; the deploy may be refused.');
	assert.equal(fixLabel(parsed.checks[0].fix), 'Fill value');
	assert.equal(fixHref(parsed.checks[0].fix, '/projects/p'), '/projects/p?tab=settings#env-sets');
	assert.equal(fixHref({ action: 'add_file', name: 'conf.txt' }, '/projects/p'), '/projects/p?tab=settings#files');
	assert.equal(fixHref(parsed.checks[2].fix, '/projects/p'), '/hosts/00000000-0000-4000-8000-0000000000aa');
	assert.equal(fixLabel({ action: 'host_policy_snippet' }), 'Allow on host');
	assert.equal(fixLabel({ action: 'none' }), null);
	assert.equal(parseReadiness({ ready: true, checks: [{ check: 'mystery', status: 'ok', message: '', fix: {} }] }), null);
	assert.equal(readinessSummary({ ready: true, checks: [] }).startsWith('Ready'), true);
});

test('host-policy snippet: rebuilt from the pattern and the slug only', () => {
	const snip = parseHostPolicySnippet({ image_pattern: 'ghcr.io/joe/app', project: 'lab', toml: 'evil', command: 'rm -rf /', note: 'The host root applies this.' });
	assert.deepEqual(snip, {
		image_pattern: 'ghcr.io/joe/app',
		project: 'lab',
		toml: 'images = ["ghcr.io/joe/app"]\nprojects = ["lab"]\n',
		command: "sudo ./install.sh install --runner --project lab --image 'ghcr.io/joe/app'",
		note: 'The host root applies this.'
	});
	assert.equal(parseHostPolicySnippet({ image_pattern: 'ghcr.io/joe/*', project: 'lab' }), null);
	assert.equal(parseHostPolicySnippet({ image_pattern: "ghcr.io/joe/app'; rm", project: 'lab' }), null);
});

test('github helpers: only GitHub authorize URLs, short note codes', () => {
	assert.equal(isGitHubAuthorizeUrl(AUTHORIZE), true);
	for (const bad of ['http://github.com/login/oauth/authorize', 'https://evil.example/login/oauth/authorize', 'https://github.com.evil.example/login/oauth/authorize', 'https://u:p@github.com/login/oauth/authorize', 'javascript:alert(1)', 42]) {
		assert.equal(isGitHubAuthorizeUrl(bad), false, String(bad));
	}
	assert.equal(githubNoteCode('connected'), 'connected');
	assert.equal(githubNoteCode('toString'), null);
	assert.equal(githubNoteCode('<script>'), null);
	assert.equal(githubFailureCode('bad_request', 'GitHub did not grant read:packages'), 'scope');
	assert.equal(githubFailureCode('bad_request', 'this GitHub sign-in expired or was already used; start again'), 'expired');
	assert.equal(githubFailureCode('forbidden', 'forbidden'), 'forbidden');
	assert.equal(githubFailureCode('bad_gateway', 'GitHub sign-in failed; try again'), 'failed');
	assert.equal(GITHUB_NOTES.revoke_failed.text, 'Disconnected here; also revoke Deploybot in your GitHub settings.');
});

test('registry rows: source and connected_by, older API is manual', () => {
	const base = { host: 'ghcr.io', username: 'octo', updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id };
	assert.deepEqual(parseRegistry(base), { ...base, source: 'manual', connected_by: null, delete_packages: false });
	assert.deepEqual(parseRegistry({ ...base, source: 'github', connected_by: { id: USER.id, name: 'Ada' }, token: LEAK }), {
		...base,
		source: 'github',
		connected_by: { id: USER.id, name: 'Ada' },
		delete_packages: false
	});
	assert.equal(parseRegistry({ ...base, source: 'manual', connected_by: { id: USER.id, name: 'Ada' } })?.connected_by, null);
	// api-v1 v1.45: delete_packages only for a github row that says true.
	assert.equal(parseRegistry({ ...base, source: 'github', delete_packages: true })?.delete_packages, true);
	assert.equal(parseRegistry({ ...base, source: 'manual', delete_packages: true })?.delete_packages, false);
	assert.equal(parseRegistry({ ...base, source: 'github', delete_packages: 'yes' })?.delete_packages, false);
});

describe('Connect GitHub routes', { concurrency: false }, () => {
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('Connect GitHub follows the registries list github_connect (both values)', async () => {
		for (const flag of [true, false]) {
			installFetch((call) => {
				if (call.url.pathname === `/v1/projects/${PROJECT.id}`) return envelope(PROJECT);
				if (call.url.pathname.endsWith('/registries')) return envelope({ items: [], next_cursor: null, github_connect: flag });
				return envelope({ items: [], next_cursor: null });
			});
			const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings`, params: { id: PROJECT.id }, user: USER, session: TOKEN, projects: [PROJECT] });
			const data = (await projectLoad(event as never)) as { githubConnect: boolean };
			assert.equal(data.githubConnect, flag);
		}
		// An older API sends no flag: hidden.
		assert.equal(githubConnectOf({ items: [] }), false);
		assert.equal(githubConnectOf({ items: [], github_connect: 'yes' }), false);
	});

	test('connectGitHub asks the API, keeps the project in a short cookie and goes to GitHub', async () => {
		const calls = installFetch(() => envelope({ authorize_url: AUTHORIZE }));
		const { event, sets } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings&/connectGitHub`, form: {}, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		const err = await Promise.resolve(projectActions.connectGitHub(event as never)).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		assert.deepEqual(redirectOf(err), { status: 303, location: AUTHORIZE });
		assert.equal(calls[0].method, 'POST');
		assert.equal(calls[0].url.pathname, `/v1/projects/${PROJECT.id}/registries/github/start`);
		assert.equal(calls[0].authorization, `Bearer ${TOKEN}`);
		const cookie = sets.find((op) => op.name === 'bot_github_return');
		assert.equal(cookie?.value, PROJECT.id);
		assert.equal(cookie?.options.httpOnly, true);
		assert.equal(cookie?.options.path, '/auth/github');
		assert.equal(cookie?.options.maxAge, 600);
	});

	test('connectGitHub refuses an authorize URL that is not GitHub', async () => {
		installFetch(() => envelope({ authorize_url: 'https://evil.example/login/oauth/authorize' }));
		const { event, sets } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/connectGitHub`, form: {}, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		const res = (await projectActions.connectGitHub(event as never)) as { status: number; data: { message: string } };
		assert.equal(res.status, 502);
		assert.equal(sets.length, 0);
	});

	test('connectGitHub shows the API refusal (not configured)', async () => {
		installFetch(() => envelope(null, 503, 'unavailable', 'GitHub is not configured'));
		const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/connectGitHub`, form: {}, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		const res = (await projectActions.connectGitHub(event as never)) as { status: number; data: { message: string; scope: string } };
		assert.equal(res.status, 503);
		assert.equal(res.data.message, 'GitHub is not configured');
		assert.equal(res.data.scope, 'settings');
	});

	async function callback(url: string, reply: () => Response, cookie: string | null = PROJECT.id) {
		const calls = installFetch(reply);
		const { event, jar, sets } = fakeEvent({ url, user: USER, session: TOKEN });
		if (cookie) jar.set('bot_github_return', cookie);
		const err = await (githubCallback(event as never) as Promise<Response>).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		return { err, calls, sets };
	}

	test('callback posts code and state and returns to Settings → Registries', async () => {
		const other = '00000000-0000-4000-8000-0000000000ff';
		const { err, calls, sets } = await callback(`http://console.example/auth/github/callback?code=abc123&state=${STATE}`, () =>
			envelope({ project_id: other, host: 'ghcr.io', username: 'octo', source: 'github' })
		);
		// The project comes from the API answer, which is bound to the state.
		assert.deepEqual(redirectOf(err), { status: 303, location: `/projects/${other}?tab=settings&github=connected#registries` });
		assert.equal(calls.length, 1);
		assert.equal(calls[0].url.pathname, '/v1/auth/github/callback');
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { code: 'abc123', state: STATE });
		assert.equal(calls[0].authorization, `Bearer ${TOKEN}`);
		assert.equal(sets.some((op) => op.op === 'delete' && op.name === 'bot_github_return'), true);
	});

	test('callback maps refusals to short codes and never calls the API for a bad state', async () => {
		const cases: Array<[string, () => Response, string]> = [
			[`?error=access_denied&state=${STATE}`, () => assert.fail('api called'), 'denied'],
			['?code=abc&state=short', () => assert.fail('api called'), 'expired'],
			[`?code=abc&state=${STATE}`, () => envelope(null, 400, 'bad_request', 'this GitHub sign-in expired or was already used; start again'), 'expired'],
			[`?code=abc&state=${STATE}`, () => envelope(null, 400, 'bad_request', 'GitHub did not grant read:packages'), 'scope'],
			[`?code=abc&state=${STATE}`, () => envelope(null, 403, 'forbidden', 'forbidden'), 'forbidden'],
			[`?code=abc&state=${STATE}`, () => envelope(null, 502, 'bad_gateway', 'GitHub sign-in failed; try again'), 'failed']
		];
		for (const [query, reply, code] of cases) {
			const { err } = await callback(`http://console.example/auth/github/callback${query}`, reply);
			assert.equal(redirectOf(err).location, `/projects/${PROJECT.id}?tab=settings&github=${code}#registries`, query);
		}
	});

	test('callback without the return cookie answers 400 with the note', async () => {
		const { err } = await callback(`http://console.example/auth/github/callback?error=access_denied`, () => assert.fail('api called'), null);
		assert.equal(isHttpError(err), true);
		assert.equal((err as { status: number }).status, 400);
	});

	test('callback sends a signed-out browser to sign in first', async () => {
		installFetch(() => assert.fail('api called'));
		const { event } = fakeEvent({ url: `http://console.example/auth/github/callback?code=abc&state=${STATE}`, user: null });
		const err = await (githubCallback(event as never) as Promise<Response>).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		assert.match(redirectOf(err).location, /^\/signin/);
	});

	test('Disconnect reports a failed revoke instead of hiding it', async () => {
		installFetch(() => envelope({ revoked: false }));
		const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings&/deleteRegistry`, form: { host: 'ghcr.io' }, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		const res = (await projectActions.deleteRegistry(event as never)) as { scope: string; githubNote: string };
		assert.deepEqual(res, { scope: 'settings', githubNote: 'revoke_failed' });
		installFetch(() => envelope({ revoked: true }));
		const ok = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings&/deleteRegistry`, form: { host: 'ghcr.io' }, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		await assert.rejects(async () => {
			await projectActions.deleteRegistry(ok.event as never);
		}, (e: unknown) => isRedirect(e));
	});

	test('readiness, image access and snippet actions pass names and statuses only', async () => {
		installFetch((call) => {
			if (call.url.pathname.endsWith('/readiness')) {
				assert.equal(call.url.searchParams.get('host_id'), '00000000-0000-4000-8000-0000000000aa');
				return envelope({ ready: true, checks: [{ check: 'env_sets', status: 'ok', message: 'ok', fix: { action: 'none' }, value: LEAK }] });
			}
			if (call.url.pathname.endsWith('/image-access')) return envelope({ registry: 'ghcr.io', access: 'public', login: 'none', token: LEAK });
			return envelope({ image_pattern: 'ghcr.io/joe/app', project: 'lab', toml: LEAK, command: LEAK, note: 'n' });
		});
		const ev = (action: string, form: Record<string, string>) =>
			fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/${action}`, form, params: { id: PROJECT.id }, user: USER, session: TOKEN }).event as never;
		const readiness = await projectActions.catalogReadiness(ev('catalogReadiness', { entry: 'web', host_id: '00000000-0000-4000-8000-0000000000aa' }));
		const access = await projectActions.imageAccess(ev('imageAccess', { image: 'ghcr.io/joe/app:v1' }));
		const snippet = await projectActions.hostPolicySnippet(ev('hostPolicySnippet', { entry: 'web', host_id: '00000000-0000-4000-8000-0000000000aa' }));
		for (const value of [readiness, access, snippet]) assert.equal(JSON.stringify(value).includes(LEAK), false);
		assert.equal((readiness as { readiness: { ready: boolean } }).readiness.ready, true);
		// No Connect GitHub fix: the registries list is not read.
		assert.equal((readiness as { readinessGitHub: boolean }).readinessGitHub, false);
		assert.deepEqual((access as { imageAccess: unknown }).imageAccess, { registry: 'ghcr.io', access: 'public', login: 'none' });
		const bad = (await projectActions.catalogReadiness(ev('catalogReadiness', { entry: 'Bad Name', host_id: '' }))) as { status: number };
		assert.equal(bad.status, 400);
	});
});

describe('Registries card and readiness markup', () => {
	const github: Registry = {
		host: 'ghcr.io',
		username: 'octo',
		updated_at: '2026-11-16T10:00:00Z',
		updated_by: USER.id,
		source: 'github',
		connected_by: { id: USER.id, name: 'Ada' },
		delete_packages: false
	};
	const manual: Registry = { ...github, username: 'bot', source: 'manual', connected_by: null, delete_packages: false };
	const props = (extra: Record<string, unknown>) => ({ project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [], files: [], ...extra });

	test('a GitHub row reads as a connection, with Reconnect and Disconnect', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: props({ registries: [github], githubConnect: true }) }).body;
		assert.match(html, /connected to GitHub as <span[^>]*>octo<\/span> by Ada,/);
		assert.match(html, />\s*Reconnect\s*</);
		assert.match(html, /Disconnect GitHub from ghcr\.io/);
		assert.equal(html.includes('>Refused<'), false);
		// Connected already: no Connect GitHub button.
		assert.equal(/>\s*Connect GitHub\s*</.test(html), false);
		assert.match(html, /action="[^"]*connectGitHub"/);
	});

	test('api-v1 v1.45: a GitHub row says whether it can delete image versions and offers the opt-in', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: props({ registries: [github], githubConnect: true }) }).body;
		assert.match(html, /data-delete-packages="false"/);
		assert.match(html, />\s*Allow deleting image versions\s*</);
		const hidden = /<input[^>]*name="delete_packages"[^>]*>/.exec(html)?.[0] ?? '';
		assert.match(hidden, /value="1"/, html.slice(html.indexOf('data-delete-packages'), html.indexOf('data-delete-packages') + 4000));
		const can = render(SettingsSection, { props: props({ registries: [{ ...github, delete_packages: true }], githubConnect: true }) }).body;
		assert.match(can, /data-delete-packages="true"/);
		assert.equal(can.includes('Allow deleting image versions'), false);
		assert.equal(can.includes('name="delete_packages"'), false);
	});

	test('Connect GitHub is hidden when the console is not set up for it', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: props({ registries: [], githubConnect: false }) }).body;
		assert.equal(html.includes('Connect GitHub'), false);
		const on = render(SettingsSection, { props: props({ registries: [], githubConnect: true }) }).body;
		assert.match(on, /<form method="POST" action="[^"]*connectGitHub"/);
	});

	test('over a manual ghcr.io login, Connect GitHub asks first', () => {
		const html = render(GitHubConnect, { props: { action: '?/connectGitHub', replaces: 'bot' } }).body;
		assert.match(html, /data-slot="alert-dialog-trigger"/);
		// The confirm dialog renders only when open; the trigger alone posts nothing.
		assert.equal(/<form/.test(html), false);
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const card = render(SettingsSection, { props: props({ registries: [manual], githubConnect: true }) }).body;
		assert.match(card, /data-slot="alert-dialog-trigger"[^>]*>[\s\S]*?Connect GitHub/);
	});

	test('notes and the refused badge', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: props({ registries: [github], githubConnect: true, githubRefused: true, githubNote: 'revoke_failed' }) }).body;
		assert.match(html, />Refused</);
		assert.match(html, /Disconnected here; also revoke Deploybot in your GitHub settings\./);
		const connected = render(SettingsSection, { props: props({ registries: [github], githubNote: 'connected' }) }).body;
		assert.match(connected, /Connected to GitHub\./);
	});

	test('the cards the fixes link to have anchors', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: props({ registries: [] }) }).body;
		for (const id of ['env-sets', 'files', 'registries']) assert.match(html, new RegExp(`id="${id}"`));
	});

	test('the readiness list loads on mount and shows the checking state', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const html = render(ReadinessList, { props: { entry: 'web', hostId: null } }).body;
		assert.match(html, /Readiness/);
		assert.match(html, /Checking…/);
		assert.match(html, /action="[^"]*catalogReadiness"/);
		// Check again is an icon button (spec-agent-usability-1 §3 A), named for screen readers and disabled while checking.
		assert.match(html, /<button[^>]*\sdisabled[\s=>][^>]*aria-label="Check readiness again"/);
	});

	test('the deploy drawer, the entry drawer and the program dialogs show readiness; nothing is gated by it', () => {
		const deploy = readFileSync(new URL('../src/routes/projects/[id]/CatalogDeployForm.svelte', import.meta.url), 'utf8');
		assert.match(deploy, /<ReadinessList entry=\{entry\.name\} hostId=[^>]*refreshKey=\{result\?\.resultId \?\? null\}/);
		assert.equal(/deployBlocked = \$derived\([^)]*readiness/i.test(deploy), false);
		const drawer = readFileSync(new URL('../src/routes/projects/[id]/CatalogImageDialog.svelte', import.meta.url), 'utf8');
		// A private-set fix points at the drawer's own "Secrets for this entry" list (v1.33 item 6).
		// A confirmed public-address change re-runs it (refreshKey, api-v1 v1.35 `routes` check).
		assert.match(drawer, /<ReadinessList entry=\{entry\.name\} collapsed=\{readinessCollapsed\} privateKeysId="private-keys-\{entry\.name\}" refreshKey=\{routesSaved\} \/>/);
		// The first scroll of the form below folds the band; a scroll inside the band itself does not.
		assert.match(drawer, /onscrollcapture=\{foldReadinessOnScroll\}/);
		assert.match(drawer, /readinessBand\?\.contains\(target\)/);
		const run = readFileSync(new URL('../src/routes/projects/[id]/CatalogRunDialog.svelte', import.meta.url), 'utf8');
		assert.match(run, /entryKind === 'program' && \(kind === 'run' \|\| kind === 'restart'\)/);
		assert.match(run, /<ReadinessList entry=\{entryName\} \{hostId\} \{hostName\} refreshKey=\{result\} \/>/);
	});
});

test('readiness with a Connect GitHub fix asks the registries list whether the API offers it', async () => {
	useApi('https://api.test');
	try {
		for (const flag of [true, false]) {
			const calls = installFetch((call) => {
				if (call.url.pathname.endsWith('/registries')) return envelope({ items: [], next_cursor: null, github_connect: flag });
				return envelope({ ready: false, checks: [{ check: 'registry', status: 'missing', message: 'private or missing; connect GitHub', fix: { action: 'connect_github', registry: 'ghcr.io' } }] });
			});
			const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/catalogReadiness`, form: { entry: 'web', host_id: '' }, params: { id: PROJECT.id }, user: USER, session: TOKEN });
			const res = (await projectActions.catalogReadiness(event as never)) as { readinessGitHub: boolean };
			assert.equal(res.readinessGitHub, flag);
			assert.equal(calls.some((call) => call.url.pathname.endsWith('/registries')), true);
		}
	} finally {
		globalThis.fetch = originalFetch;
		restoreEnv();
	}
});

describe('Used by notes and the program readiness list', () => {
	const catalog: UsedByEntry[] = [
		{ name: 'api', kind: 'image' as const, spec: { image: 'ghcr.io/joe/api:v1', env_sets: ['app'], secret_env: {}, files: [{ file: 'conf.txt' }] } },
		{ name: 'web', kind: 'image' as const, spec: { image: 'nginx', env_sets: [], secret_env: { TOKEN: 'shared.TOKEN' }, files: [] } },
		{ name: 'job', kind: 'program' as const, spec: { env_sets: ['app'], secret_env: {} } }
	];

	test('usedBy lists entry names, sorted, or says none', () => {
		assert.deepEqual(envSetUsers(catalog, 'app'), ['api', 'job']);
		assert.deepEqual(envSetUsers(catalog, 'shared'), ['web']);
		assert.deepEqual(fileUsers(catalog, 'conf.txt'), ['api']);
		assert.deepEqual(registryUsers(catalog, 'ghcr.io'), ['api']);
		assert.deepEqual(registryUsers(catalog, 'docker.io'), ['web']);
		assert.equal(imageRegistryHost('localhost:5000/app'), 'localhost:5000');
		assert.equal(imageRegistryHost('library/nginx@sha256:' + 'a'.repeat(64)), 'docker.io');
		assert.equal(usedByText([]), 'Not used by any entry');
		assert.equal(usedByText(['api', 'job']), 'Used by api, job');
	});

	test('each Settings card row carries its Used by note', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, {
			props: {
				project: PROJECT,
				canOwner: true,
				canAdmin: true,
				message: null,
				// The notes read names and references only; the full entry shape is not needed here.
				catalog: catalog as never,
				envSets: [
					{ name: 'app', keys: [], updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id },
					{ name: 'spare', keys: [], updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id }
				],
				files: [{ name: 'conf.txt', is_secret: false, size: 3, sha256: 'ab'.repeat(32), updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id }],
				registries: [{ host: 'ghcr.io', username: 'bot', updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id, source: 'manual', connected_by: null, delete_packages: false }]
			}
		}).body;
		const notes = [...html.matchAll(/data-used-by="([a-z-]+)"[^>]*>([^<]*)</g)].map((m) => `${m[1]}: ${m[2]}`);
		assert.deepEqual(notes, ['env-set: Used by api, job', 'env-set: Not used by any entry', 'file: Used by api', 'registry: Used by api']);
	});

});

describe('rendered readiness, access line and Used by states', () => {
	const view = (extra: Record<string, unknown>) =>
		render(ReadinessView, {
			props: {
				result: null,
				rights: { canAdmin: true, canOrgAdmin: true, githubConnect: true },
				entry: 'web',
				hostId: '00000000-0000-4000-8000-0000000000aa',
				hostName: 'pi4',
				projectPath: `/projects/${PROJECT.id}`,
				connectAction: '?/connectGitHub',
				snippetAction: '?/hostPolicySnippet',
				onRefresh: () => {},
				...extra
			}
		}).body;
	const missing = {
		ready: false,
		checks: [
			{ check: 'env_values', status: 'missing', message: 'env A in set app has no value yet; set it in the console', fix: { action: 'fill_value', set: 'app', key: 'A' } },
			{ check: 'registry', status: 'missing', message: 'private or missing; connect GitHub', fix: { action: 'connect_github', registry: 'ghcr.io' } },
			{ check: 'files', status: 'missing', message: 'file conf.txt does not exist', fix: { action: 'add_file', name: 'conf.txt' } },
			{ check: 'host_access', status: 'missing', message: 'no deploy access', fix: { action: 'grant_deploy_access', host_id: '00000000-0000-4000-8000-0000000000aa' } },
			{ check: 'host_policy', status: 'missing', message: 'this host does not allow image ghcr.io/joe/app', fix: { action: 'host_policy_snippet', host_id: '00000000-0000-4000-8000-0000000000aa' } },
			{ check: 'host_online', status: 'warn', message: 'pi4 is offline; the deploy will wait or fail', fix: { action: 'none' } }
		]
	};

	/** The markup inside the first `<details>` carrying `attr`, or '' when there is none. */
	function detailsOf(html: string, attr: string): string {
		const m = html.match(new RegExp(`<details[^>]*${attr}[^>]*>([\\s\\S]*?)</details>`));
		return m ? m[1] : '';
	}
	/** The markup outside every `<details>`. */
	const outsideDetails = (html: string) => html.replace(/<details[\s\S]*?<\/details>/g, '');
	const ok = (check: string) => ({ check, status: 'ok', message: `${check} is fine`, fix: { action: 'none' } });

	test('ready, error and loading states', () => {
		const ready = view({ result: { ready: true, checks: [{ check: 'env_sets', status: 'ok', message: 'every env set the entry names exists', fix: { action: 'none' } }] } });
		assert.match(ready, /Ready as far as Deploybot can check/);
		assert.match(ready, />OK</);
		assert.equal(/<a [^>]*opens in a new tab/.test(ready), false);
		const failed = view({ message: 'forbidden' });
		assert.match(failed, /role="alert"[\s\S]*forbidden/);
		assert.equal(failed.includes('<details'), false);
		assert.equal(failed.includes('data-check'), false);
		const loading = view({ loading: true, result: missing });
		assert.match(loading, /<p[^>]*role="status"[^>]*>Checking…<\/p>/);
		assert.equal(loading.includes('Missing'), false);
		assert.equal(loading.includes('<details'), false);
		assert.match(loading, /<button[^>]*\sdisabled[\s=>][^>]*aria-label="Check readiness again"/);
		const loadingReady = view({ loading: true, result: { ready: true, checks: [ok('env_sets')] } });
		assert.equal(loadingReady.includes('checks passed') || loadingReady.includes('check passed'), false);
	});

	test('all OK: one status line, the full list only inside a closed Details', () => {
		const checks = [ok('env_sets'), ok('env_values'), ok('files'), { check: 'host_policy', status: 'unknown', message: 'checked at deploy', fix: { action: 'none' } }];
		const html = view({ result: { ready: true, checks } });
		assert.match(html, /<section[^>]*aria-labelledby="[^"]+"[^>]*aria-busy="false"/);
		assert.match(html, /<h3[^>]*class="sr-only"[^>]*>Readiness<\/h3>/);
		// One summary line with the icon and the count of OK checks; `unknown` is not a problem and not counted as passed.
		const lines = [...html.matchAll(/role="status"/g)];
		assert.equal(lines.length, 1);
		assert.match(html, /<p[^>]*role="status"[^>]*data-readiness-line[^>]*>[\s\S]*?<svg[\s\S]*?Ready: 3 checks passed\s*<\/p>/);
		// Closed by default; every row (OK and unknown) is inside it, none outside.
		assert.match(html, /<details(?![^>]*\sopen[\s=>])[^>]*data-readiness-details/);
		const inside = detailsOf(html, 'data-readiness-details');
		assert.match(inside, /<summary[^>]*>Details<\/summary>/);
		assert.equal([...inside.matchAll(/data-check="/g)].length, 4);
		assert.match(inside, /Ready as far as Deploybot can check/);
		assert.equal(outsideDetails(html).includes('data-check'), false);
		assert.equal(html.includes('passed checks'), false);
		// Check again: ghost icon button with a name and a tooltip, enabled.
		assert.match(html, /<button(?![^>]*\sdisabled[\s=>])[^>]*aria-label="Check readiness again"/);
		assert.equal(/>\s*Check again\s*</.test(html), false);
		// One check reads in the singular.
		assert.match(view({ result: { ready: true, checks: [ok('env_sets')] } }), /Ready: 1 check passed/);
	});

	test('a warning is not all OK: it shows outside, the passed ones behind the toggle', () => {
		const html = view({ result: { ready: true, checks: [ok('env_sets'), { check: 'host_online', status: 'warn', message: 'pi4 is offline', fix: { action: 'none' } }] } });
		assert.equal(html.includes('data-readiness-details'), false);
		assert.match(html, /<h3[^>]*class="text-sm font-medium"[^>]*>Readiness<\/h3>/);
		const outside = outsideDetails(html);
		assert.match(outside, /data-check="host_online"/);
		assert.equal(outside.includes('data-check="env_sets"'), false);
		assert.match(detailsOf(html, 'data-readiness-passed'), /<summary[^>]*>Show 1 passed check<\/summary>[\s\S]*data-check="env_sets"/);
	});

	test('missing: only the rows that are not OK outside, and Show N passed checks closed', () => {
		const html = view({ result: { ...missing, checks: [ok('env_sets'), ...missing.checks, ok('networks')] } });
		assert.match(html, /<p[^>]*role="status"[^>]*>5 items missing\. You can still deploy; the deploy may be refused\.<\/p>/);
		assert.equal(html.includes('data-readiness-line'), false);
		const outside = outsideDetails(html);
		const shown = [...outside.matchAll(/data-check="([a-z_]+)"/g)].map((m) => m[1]);
		assert.deepEqual(shown, ['env_values', 'registry', 'files', 'host_access', 'host_policy', 'host_online']);
		assert.match(outside, /Fill value/);
		assert.match(html, /<details(?![^>]*\sopen[\s=>])[^>]*data-readiness-passed/);
		const passed = detailsOf(html, 'data-readiness-passed');
		assert.match(passed, /<summary[^>]*>Show 2 passed checks<\/summary>/);
		assert.deepEqual([...passed.matchAll(/data-check="([a-z_]+)"/g)].map((m) => m[1]), ['env_sets', 'networks']);
		// Nothing passed: no toggle.
		assert.equal(view({ result: missing }).includes('<details'), false);
	});

	test('collapsed (the form beside it scrolled): rows needing action fold under a closed toggle, the summary stays', () => {
		const html = view({ result: { ...missing, checks: [ok('env_sets'), ...missing.checks] }, collapsed: true });
		assert.match(html, /<p[^>]*role="status"[^>]*>5 items missing\. You can still deploy; the deploy may be refused\.<\/p>/);
		assert.equal(/data-check=/.test(outsideDetails(html)), false);
		assert.match(html, /<details(?![^>]*\sopen[\s=>])[^>]*data-readiness-attention/);
		const folded = detailsOf(html, 'data-readiness-attention');
		assert.match(folded, /<summary[^>]*>Show 6 items that need action<\/summary>/);
		assert.deepEqual([...folded.matchAll(/data-check="([a-z_]+)"/g)].map((m) => m[1]), ['env_values', 'registry', 'files', 'host_access', 'host_policy', 'host_online']);
		// Not collapsed: the same rows show outside any toggle, as before.
		assert.equal(view({ result: missing }).includes('data-readiness-attention'), false);
		// All OK and collapsed: still one line, Details closed.
		const ready = view({ result: { ready: true, checks: [ok('env_sets')] }, collapsed: true });
		assert.match(ready, /Ready: 1 check passed/);
		assert.match(ready, /<details(?![^>]*\sopen[\s=>])[^>]*data-readiness-details/);
	});

	test('missing state: admins get buttons and links, others get who to ask', () => {
		const admin = view({ result: missing });
		assert.match(admin, /5 items missing/);
		assert.match(admin, /href="\/projects\/[^"]*\?tab=settings#env-sets"[^>]*>\s*Fill value/);
		assert.match(admin, /<form method="POST" action="\?\/connectGitHub"/);
		assert.match(admin, /href="\/projects\/[^"]*\?tab=settings#files"/);
		assert.match(admin, /href="\/hosts\/00000000-0000-4000-8000-0000000000aa"/);
		assert.match(admin, /Allow on host/);
		assert.equal(admin.includes('data-fix-ask'), false);
		// GitHub not configured: an admin can still add a token.
		const noGitHub = view({ result: missing, rights: { canAdmin: true, canOrgAdmin: true, githubConnect: false } });
		assert.equal(noGitHub.includes('connectGitHub'), false);
		assert.match(noGitHub, /href="[^"]*#registries"[^>]*>\s*Add registry login/);
		const member = view({ result: missing, rights: { canAdmin: false, canOrgAdmin: false, githubConnect: true } });
		for (const ask of ['Ask an admin to fill the value.', 'Ask an admin to connect GitHub.', 'Ask an admin to add the file.', 'Ask an org admin to grant deploy access.', 'Ask an admin for the host policy lines.']) {
			assert.equal(member.includes(ask), true, ask);
		}
		assert.equal(/<a [^>]*opens in a new tab|connectGitHub|Allow on host/.test(member), false);
	});

	test('the image access line: status role, button, link and failure', () => {
		const line = (state: AccessLineState, extra: Record<string, unknown> = {}) =>
			render(ImageAccessLine, { props: { id: 'img-access', state, settingsHref: '/projects/p?tab=settings#registries', ...extra } }).body;
		const pub = line({ result: { registry: 'docker.io', access: 'public', login: 'none' } });
		assert.match(pub, /<p id="img-access" role="status"/);
		assert.match(pub, /Public image, no login needed\./);
		const priv: AccessLineState = { result: { registry: 'ghcr.io', access: 'private_no_login', login: 'none' } };
		const button = line(priv, { canConnect: true, onConnect: () => {} });
		assert.match(button, /<button[^>]*>[\s\S]*Connect GitHub/);
		const link = line(priv, { canConnect: false });
		assert.equal(/<button/.test(link), false);
		assert.match(link, /href="\/projects\/p\?tab=settings#registries"/);
		assert.match(line({ failed: true }), /Image access could not be checked\./);
		const form = readFileSync(new URL('../src/routes/projects/[id]/CatalogImageForm.svelte', import.meta.url), 'utf8');
		assert.match(form, /accessShown \? accessId : null/);
		assert.match(form, /target="_blank"/);
	});

	test('Used by says usage is unknown when the catalog is incomplete', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, {
			props: {
				project: PROJECT,
				canOwner: true,
				canAdmin: true,
				message: null,
				catalog: [],
				catalogKnown: false,
				envSets: [{ name: 'app', keys: [], updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id }],
				files: [],
				registries: []
			}
		}).body;
		assert.match(html, /data-used-by="env-set"[^>]*>Usage unknown</);
		assert.equal(html.includes('Not used by any entry'), false);
		assert.equal(usedByText(['api'], false), 'Used by api (the list may be incomplete)');
	});

	test('the Registries card stacks its actions under the description below sm', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, { props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [], files: [], registries: [] } }).body;
		assert.match(html, /max-sm:has-data-\[slot=card-action\]:grid-cols-1/);
		assert.match(html, /data-slot="card-action"[^>]*max-sm:col-start-1[^"]*max-sm:row-start-3/);
	});

	test('a streamed refused badge does not block the render', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const github: Registry = { host: 'ghcr.io', username: 'octo', updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id, source: 'github', connected_by: { id: USER.id, name: 'Ada' }, delete_packages: false };
		const html = render(SettingsSection, {
			props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [], files: [], registries: [github], githubRefused: new Promise<boolean>(() => {}) }
		}).body;
		assert.match(html, /connected to GitHub as/);
		assert.equal(html.includes('>Refused<'), false);
	});
});

test('the refused badge checks every ghcr.io repository once', async () => {
	useApi('https://api.test');
	try {
		const asked: string[] = [];
		installFetch((call) => {
			const image = call.url.searchParams.get('image') ?? '';
			asked.push(image);
			return envelope({ registry: 'ghcr.io', access: image.startsWith('ghcr.io/joe/b') ? 'login_failed' : 'private_ok', login: 'github' });
		});
		const github: Registry = { host: 'ghcr.io', username: 'octo', updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id, source: 'github', connected_by: null, delete_packages: false };
		const entry = (name: string, image: string) => ({ name, kind: 'image', spec: { image } }) as never;
		const catalog = [entry('a1', 'ghcr.io/joe/a:v1'), entry('a2', 'ghcr.io/joe/a:v2'), entry('b', 'ghcr.io/joe/b:v1'), entry('hub', 'nginx')];
		const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings`, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		assert.deepEqual(await githubRefused(event as never, PROJECT.id, [github], catalog), { refusals: [] });
		assert.deepEqual(asked.sort(), ['ghcr.io/joe/a:v1', 'ghcr.io/joe/b:v1']);
		assert.equal(await githubRefused(event as never, PROJECT.id, [{ ...github, source: 'manual' }], catalog), false);
	} finally {
		globalThis.fetch = originalFetch;
		restoreEnv();
	}
});

// api-v1 v1.52: why ghcr.io refused the GitHub connection.
const GRANT = 'https://github.com/settings/connections/applications/Iv1.abc123';

test('image access keeps an allowlisted refusal on a refused GitHub connection only', () => {
	const org = { reason: 'org_restricted', owner: 'kea-studio', account: 'octo-cat', url: GRANT };
	assert.deepEqual(parseImageAccess({ registry: 'ghcr.io', access: 'login_failed', login: 'github', refusal: org }), {
		registry: 'ghcr.io',
		access: 'login_failed',
		login: 'github',
		refusal: org
	});
	// Only GitHub's grant page passes, and an org refusal needs its organization.
	assert.deepEqual(parseRefusal({ reason: 'org_restricted', owner: 'kea-studio', url: 'https://evil.example/x' }), { reason: 'org_restricted', owner: 'kea-studio' });
	assert.equal(parseRefusal({ reason: 'org_restricted', owner: 'kea studio' }), null);
	assert.equal(parseRefusal({ reason: 'later' }), null);
	assert.equal(parseImageAccess({ registry: 'ghcr.io', access: 'private_ok', login: 'github', refusal: org })?.refusal, undefined);
	assert.equal(parseImageAccess({ registry: 'ghcr.io', access: 'login_failed', login: 'manual', refusal: org })?.refusal, undefined);
});

test('the image line names the organization and links GitHub instead of Connect GitHub', () => {
	const base = { registry: 'ghcr.io', access: 'login_failed', login: 'github' } as const;
	assert.deepEqual(accessLine({ ...base, refusal: { reason: 'org_restricted', owner: 'kea-studio', url: GRANT } }), {
		text: 'ghcr.io refused the GitHub connection: the kea-studio organization has not granted the Deploybot GitHub app access.',
		connect: false,
		settings: false,
		github: GRANT
	});
	const noAccess = accessLine({ ...base, refusal: { reason: 'no_package_access', owner: 'kea-studio', account: 'octo-cat' } });
	assert.equal(noAccess.text, 'ghcr.io refused the GitHub connection: The GitHub account octo-cat cannot see the kea-studio package. Give it read access to the package on GitHub, or reconnect as an account that can.');
	assert.equal(noAccess.connect, true);
	assert.equal(accessLine({ ...base, refusal: { reason: 'github_sign_in' } }).text, 'The stored login for ghcr.io was refused: reconnect GitHub.');
});

test('a github_org_access fix links GitHub and is dropped without the link', () => {
	const parsed = parseReadiness({
		ready: false,
		checks: [
			{ check: 'registry', status: 'missing', message: 'm', fix: { action: 'github_org_access', registry: 'ghcr.io', owner: 'kea-studio', url: GRANT } },
			{ check: 'registry', status: 'missing', message: 'm', fix: { action: 'github_org_access', owner: 'kea-studio', url: 'https://evil.example' } }
		]
	});
	assert.ok(parsed);
	assert.equal(fixLabel(parsed.checks[0].fix), 'Grant on GitHub');
	assert.equal(fixHref(parsed.checks[0].fix, '/projects/p'), GRANT);
	assert.deepEqual(parsed.checks[1].fix, { action: 'none' });
});

test('the refused badge carries the reasons, organizations first, once each', async () => {
	useApi('https://api.test');
	try {
		installFetch((call) => {
			const image = call.url.searchParams.get('image') ?? '';
			const refusal = image.includes('/kea-studio/')
				? { reason: 'org_restricted', owner: 'kea-studio', url: GRANT }
				: { reason: 'no_package_access', owner: 'other', account: 'octo' };
			return envelope({ registry: 'ghcr.io', access: 'login_failed', login: 'github', refusal });
		});
		const github: Registry = { host: 'ghcr.io', username: 'octo', updated_at: '2026-11-16T10:00:00Z', updated_by: USER.id, source: 'github', connected_by: null, delete_packages: false };
		const entry = (name: string, image: string) => ({ name, kind: 'image', spec: { image } }) as never;
		const catalog = [entry('o', 'ghcr.io/other/x:v1'), entry('a', 'ghcr.io/kea-studio/a:v1'), entry('b', 'ghcr.io/kea-studio/b:v1')];
		const { event } = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=settings`, params: { id: PROJECT.id }, user: USER, session: TOKEN });
		const state = await githubRefused(event as never, PROJECT.id, [github], catalog);
		assert.deepEqual(state, {
			refusals: [
				{ reason: 'org_restricted', owner: 'kea-studio', url: GRANT },
				{ reason: 'no_package_access', owner: 'other', account: 'octo' }
			]
		});
		assert.match(refusedTitle(state), /organization has not granted/);
		assert.equal(refusedTitle(true), 'ghcr.io refused this login for an image in the catalog. Reconnect GitHub.');
		assert.equal(refusalText({ reason: 'github_sign_in' }), null);

		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(SettingsSection, {
			props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [], files: [], registries: [github], githubRefused: state }
		}).body;
		assert.match(html, />Refused</);
		assert.match(html, /data-refusal="org_restricted"[^>]*>\s*The kea-studio organization has not granted the Deploybot GitHub app access, so Reconnect will not help\./);
		assert.ok(html.includes(`href="${GRANT}"`));
		assert.match(html, /data-refusal="no_package_access"/);
	} finally {
		globalThis.fetch = originalFetch;
		restoreEnv();
	}
});
