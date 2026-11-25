import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import { blankImageDraft, cleanEnvSources, imageSpecFromDraft } from '../src/lib/image-spec.ts';
import type { CatalogEntry, EnvSet, Host, HostPrograms, ImageEntry, ImageSpec } from '../src/lib/api-types.ts';
import { addNetworkByName, networkChoices, networksLimitText, openSharedNetworks, projectNetworkRows, toggleNetwork } from '../src/lib/catalog.ts';
import { fillSubject, parseFillRequest } from '../src/lib/fill-request.ts';
import { isRequestHandle, isRequestId } from '../src/lib/request-shape.ts';
import { openRequestHref, parseOpenRequests, OPEN_REQUEST_TEXT } from '../src/lib/open-requests.ts';
import { afterPrivateReveal, parsePrivateKeys, privateKeyNotes, privateKeyRevealPath, privateKeysFolded, privateSetEntry } from '../src/lib/private-keys.ts';
import {
	checkLabel,
	fixHref,
	fixLabel,
	NETWORK_SNIPPET_NO_ACCESS,
	networkSnippetHeading,
	networkSnippetHosts,
	networkSnippetProblem,
	parseNetworkSnippet,
	parseReadiness,
	privateFixEntry,
	sameSnippetAsk,
	snippetHostLabel
} from '../src/lib/settings-auto.ts';
import { parseEnvSet, parseEnvSetList, parseImageSpec } from '../src/lib/server/shape.ts';
import { actions as fillActions, load as fillLoad } from '../src/routes/fill/[id]/+page.server.ts';
import { load as fileLoad } from '../src/routes/fill-file/[id]/+page.server.ts';
import FillPage from '../src/routes/fill/[id]/+page.svelte';
import { load as requestsLoad } from '../src/routes/requests/+page.server.ts';
import RequestsPage from '../src/routes/requests/+page.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import CatalogImageForm from '../src/routes/projects/[id]/CatalogImageForm.svelte';
import NetworksCard from '../src/routes/projects/[id]/NetworksCard.svelte';
import ReadinessView from '../src/routes/projects/[id]/ReadinessView.svelte';
import PrivateKeys from '../src/routes/projects/[id]/PrivateKeys.svelte';
import SettingsSection from '../src/routes/projects/[id]/SettingsSection.svelte';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

// spec-agent-usability-1 rev 3 (G, H, F2 and the extras), api-v1 v1.33 items 4–8: console only.

const originalFetch = globalThis.fetch;
const HANDLE = 'ab'.repeat(32);
const ID = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde';
const HOST_ID = '00000000-0000-4000-8000-0000000000aa';

function policy(overrides: Partial<HostPrograms> = {}): HostPrograms {
	return {
		enabled: true,
		run_as: ['joe'],
		allow_shell: false,
		policy_hash: 'policy-1',
		images: true,
		docker: true,
		files: false,
		logging: false,
		health_policy: false,
		secrets: false,
		projects: null,
		project_listed: null,
		shared_networks: null,
		env_hash: false,
		...overrides
	};
}

function host(name: string, programs: HostPrograms | null, overrides: Partial<Host> = {}): Host {
	return {
		id: HOST_ID,
		org_id: PROJECT.org_id ?? PROJECT.id,
		name,
		node_type: 'pi',
		os: 'linux',
		arch: 'arm64',
		always_on: true,
		agent_version: '0.2.9',
		status: 'online',
		last_seen_at: '2026-11-14T00:00:00Z',
		enrolled_at: '2026-11-01T00:00:00Z',
		revoked_at: null,
		access_level: 'deploy',
		access: null,
		programs,
		...overrides
	};
}

function imageEntry(name: string, networks?: string[]): ImageEntry {
	const spec = {
		kind: 'service',
		image: 'nginx:alpine',
		command: null,
		entrypoint: null,
		ports: [],
		env: {},
		env_sets: [],
		secret_env: {},
		volumes: [],
		files: [],
		restart: 'unless-stopped',
		user: '',
		healthy_after_sec: 30,
		memory_mb: null,
		timeout_sec: 600,
		healthcheck: null,
		logging: null,
		health_policy: null,
		...(networks ? { networks } : {})
	} as ImageSpec;
	return {
		id: '00000000-0000-4000-8000-0000000000e1',
		project_id: PROJECT.id,
		name,
		kind: 'image',
		spec,
		spec_hash: 'a'.repeat(64),
		command_display: 'nginx:alpine',
		created_by: USER.id,
		created_at: '2026-11-14T00:00:00Z',
		updated_at: '2026-11-14T00:00:00Z',
		my_actions: ['deploy']
	} as ImageEntry;
}

function rawSet(name: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
	return {
		name,
		keys: [{ key: 'A', is_secret: false, value: '1' }],
		updated_at: '2026-11-16T00:00:00Z',
		updated_by: USER.id,
		...extra
	};
}

describe('F2 handles and the /requests page', () => {
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('a fill, file or connect page id may be h- and 64 lowercase hex', () => {
		assert.equal(isRequestId(ID), true);
		assert.equal(isRequestId(`h-${HANDLE}`), true);
		for (const bad of [HANDLE, `h-${HANDLE.toUpperCase()}`, `h-${HANDLE}0`, `h-${HANDLE.slice(1)}`, `x-${HANDLE}`, `h-${'g'.repeat(64)}`]) {
			assert.equal(isRequestId(bad), false, bad);
		}
		assert.equal(isRequestHandle(HANDLE), true);
		assert.equal(isRequestHandle(`h-${HANDLE}`), false);
	});

	test('the fill and file pages pass a handle to the API unchanged', async () => {
		const calls = installFetch(() => envelope({}, 404, 'not_found', 'not found'));
		const id = `h-${HANDLE}`;
		const ev = (path: string) => fakeEvent({ url: `http://console.example/${path}/${id}`, session: TOKEN, user: USER, params: { id } }).event as never;
		assert.equal((await fillLoad(ev('fill')) as { view: string }).view, 'missing');
		assert.equal((await fileLoad(ev('fill-file')) as { view: string }).view, 'missing');
		assert.deepEqual(
			calls.map((call) => call.url.pathname),
			[`/v1/value-requests/${id}/fill`, `/v1/file-requests/${id}/fill`]
		);
	});

	test('open requests parse: handles only, bad rows dropped, newest first, links by type', () => {
		const row = (extra: Record<string, unknown>) => ({
			type: 'value',
			handle: HANDLE,
			project_id: PROJECT.id,
			project_name: 'Lab',
			subject: 'app.API_KEY',
			expires_at: '2026-11-16T10:15:00Z',
			created_at: '2026-11-16T10:00:00Z',
			...extra
		});
		const parsed = parseOpenRequests({
			items: [
				row({}),
				row({ type: 'file', handle: 'cd'.repeat(32), subject: 'conf.txt', created_at: '2026-11-16T10:05:00Z' }),
				row({ type: 'connect', handle: 'ef'.repeat(32), subject: 'ghcr.io', created_at: '2026-11-16T09:00:00Z' }),
				row({ type: 'other', handle: '01'.repeat(32) }),
				row({ handle: `h-${HANDLE}` }),
				row({ handle: '23'.repeat(32), project_id: 'nope' })
			]
		});
		assert.ok(parsed);
		assert.deepEqual(parsed.map((item) => item.type), ['file', 'value', 'connect']);
		assert.deepEqual(parsed.map(openRequestHref), [`/fill-file/h-${'cd'.repeat(32)}`, `/fill/h-${HANDLE}`, `/connect/h-${'ef'.repeat(32)}`]);
		assert.equal(parseOpenRequests({}), null);
		assert.equal(parseOpenRequests([]), null);
	});

	test('/requests loads GET /v1/me/requests and renders rows with links, or the empty state', async () => {
		const calls = installFetch(() =>
			envelope({
				items: [{ type: 'value', handle: HANDLE, project_id: PROJECT.id, project_name: 'Lab', subject: 'app.API_KEY', expires_at: '2026-11-16T10:15:00Z', created_at: '2026-11-16T10:00:00Z' }]
			})
		);
		const event = fakeEvent({ url: 'http://console.example/requests', session: TOKEN, user: USER }).event as never;
		const data = (await requestsLoad(event)) as { requests: unknown[]; message: string | null };
		assert.equal(calls[0]?.url.pathname, '/v1/me/requests');
		assert.equal(data.requests.length, 1);
		setPageUrl('http://console.example/requests');
		const html = render(RequestsPage, { props: { data } as never }).body;
		assert.match(html, new RegExp(`href="/fill/h-${HANDLE}"`));
		// The link's name says what and where, for a screen reader's links list.
		assert.match(html, /<span class="sr-only"> app\.API_KEY in Lab<\/span>/);
		assert.equal(html.includes('app.API_KEY'), true);
		const empty = render(RequestsPage, { props: { data: { requests: [], message: null } } as never }).body;
		assert.equal(empty.includes('No open requests.'), true);
		assert.equal(OPEN_REQUEST_TEXT.empty.startsWith('No open requests.'), true);

		installFetch(() => envelope(null, 403, 'forbidden', 'forbidden'));
		const refused = (await requestsLoad(event)) as { requests: unknown[]; message: string | null };
		assert.equal(refused.requests.length, 0);
		assert.equal(refused.message, 'forbidden');
	});
});

describe('G: private sets, used_by, env_pairs', () => {
	const fill = (set: string) => ({
		request_id: ID,
		project_id: PROJECT.id,
		project_name: 'Lab',
		set,
		key: 'GOOGLE_CLIENT_SECRET',
		state: 'pending',
		expires_at: '2026-11-16T10:15:00Z',
		created_at: '2026-11-16T10:00:00Z',
		reason: ''
	});

	test('the fill page names a private set by its entry', () => {
		assert.deepEqual(fillSubject('@serve'), { label: 'Entry', name: 'serve' });
		assert.deepEqual(fillSubject('services'), { label: 'Env set', name: 'services' });
		const request = parseFillRequest(fill('@serve'), ID);
		assert.equal(request?.set, '@serve');
		assert.equal(parseFillRequest(fill('@Bad'), ID), null);
		assert.equal(parseFillRequest(fill('@'), ID), null);
		const html = render(FillPage, { props: { data: { view: 'form', request, message: null }, form: null } as never }).body;
		assert.match(html, /<dt[^>]*>Entry<\/dt>\s*<dd[^>]*data-fill-subject[^>]*>serve<\/dd>/);
		assert.equal(html.includes('>Env set<'), false);
		const shared = render(FillPage, { props: { data: { view: 'form', request: parseFillRequest(fill('services'), ID), message: null }, form: null } as never }).body;
		assert.match(shared, /<dt[^>]*>Env set<\/dt>\s*<dd[^>]*data-fill-subject[^>]*>services<\/dd>/);
	});

	test('env-set lists skip private sets without counting them; used_by is kept when well formed', () => {
		const list = parseEnvSetList({ items: [rawSet('app', { used_by: ['web', 'api', 'web'] }), rawSet('@serve')], next_cursor: null });
		assert.ok(list);
		assert.deepEqual(list.items.map((set) => set.name), ['app']);
		assert.equal(list.omitted, 0);
		assert.deepEqual(list.items[0]?.used_by, ['api', 'web']);
		assert.equal(Object.hasOwn(parseEnvSet(rawSet('app')) ?? {}, 'used_by'), false);
		assert.equal(Object.hasOwn(parseEnvSet(rawSet('app', { used_by: ['Bad Name'] })) ?? {}, 'used_by'), false);
	});

	test('the Env sets card hides a private set and prefers used_by over the catalog guess', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const set = (name: string, used_by?: string[]): EnvSet => ({ name, keys: [], updated_at: '2026-11-16T00:00:00Z', updated_by: USER.id, ...(used_by ? { used_by } : {}) });
		const html = render(SettingsSection, {
			props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [set('app', ['api', 'web']), set('@serve')], files: [], catalog: [] }
		}).body;
		assert.equal(html.includes('@serve'), false);
		assert.equal(html.includes('Used by api, web'), true);
		const guessed = render(SettingsSection, {
			props: { project: PROJECT, canOwner: true, canAdmin: true, message: null, envSets: [set('app')], files: [], catalog: [] }
		}).body;
		assert.equal(guessed.includes('Not used by any entry'), true);
	});

	test('private keys: names and flags only, sorted; missing is none, malformed is null', () => {
		const keys = parsePrivateKeys({
			name: 'serve',
			private_keys: [
				{ key: 'Z_TOKEN', is_secret: true, needs_value: true, reveal_pending: false, value: 'leak' },
				{ key: 'A_ID', is_secret: false }
			]
		});
		assert.deepEqual(keys, [
			{ key: 'A_ID', is_secret: false, needs_value: false, reveal_pending: false, generated: false },
			{ key: 'Z_TOKEN', is_secret: true, needs_value: true, reveal_pending: false, generated: false }
		]);
		assert.equal(JSON.stringify(keys).includes('leak'), false);
		assert.deepEqual(parsePrivateKeys({ name: 'serve' }), []);
		assert.equal(parsePrivateKeys({ private_keys: [{ key: 'lower', is_secret: true }] }), null);
		assert.deepEqual(privateKeyNotes({ key: 'K', is_secret: true, needs_value: true, reveal_pending: true, generated: true }), ['secret', 'needs a value', 'generated, not shown yet']);
	});

	test('the catalogPrivateKeys action reads the entry and returns no value', async () => {
		useApi('https://api.test');
		const calls = installFetch(() => envelope({ name: 'serve', private_keys: [{ key: 'K', is_secret: true, needs_value: false, reveal_pending: false, value: 'leak' }] }));
		const ev = fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/catalogPrivateKeys`, form: { entry: 'serve' }, params: { id: PROJECT.id }, user: USER, session: TOKEN }).event as never;
		const result = (await projectActions.catalogPrivateKeys(ev)) as { privateKeys: unknown };
		assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/serve`);
		assert.deepEqual(result.privateKeys, [{ key: 'K', is_secret: true, needs_value: false, reveal_pending: false, generated: false }]);
		assert.equal(JSON.stringify(result).includes('leak'), false);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('readiness accepts env_pairs, labelled "Env pairs", and a warn renders as Check', () => {
		const r = parseReadiness({ ready: true, checks: [{ check: 'env_pairs', status: 'warn', message: 'GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET come from different places', fix: { action: 'none' } }] });
		assert.ok(r);
		assert.equal(checkLabel('env_pairs'), 'Env pairs');
		const html = render(ReadinessView, {
			props: { result: r, rights: { canAdmin: true, canOrgAdmin: true, githubConnect: false }, entry: 'serve', projectPath: `/projects/${PROJECT.id}`, connectAction: '?/c', snippetAction: '?/s' }
		}).body;
		assert.equal(html.includes('Env pairs'), true);
		assert.equal(html.includes('>Check<'), true);
	});

	test('the OK badge uses the up token for border and text (readable in dark mode)', () => {
		const r = parseReadiness({ ready: true, checks: [{ check: 'files', status: 'ok', message: 'fine', fix: { action: 'none' } }] });
		const html = render(ReadinessView, {
			props: { result: r, rights: { canAdmin: true, canOrgAdmin: true, githubConnect: false }, entry: 'serve', projectPath: `/projects/${PROJECT.id}`, connectAction: '?/c', snippetAction: '?/s' }
		}).body;
		const badge = /data-slot="badge"[^"]*class="([^"]*)"><!---->OK</.exec(html)?.[1] ?? '';
		assert.match(badge, /border-status-up/);
		assert.match(badge, /text-status-up/);
		assert.equal(/\btext-foreground\b/.test(badge), false);
	});
});

describe('H: shared networks in the console', () => {
	const hosts = [
		host('pi3', policy({ shared_networks: [{ name: 'dd', subnet: '10.200.10.0/24' }, { name: 'closed', subnet: '10.200.11.0/24', projects: ['other'] }] })),
		host('pi4', policy({ shared_networks: [{ name: 'dd', subnet: '10.200.20.0/24' }, { name: 'aa', subnet: '10.200.30.0/24', projects: ['lab'] }] }), { id: '00000000-0000-4000-8000-0000000000ab' })
	];

	test('choices: open networks A–Z with where, then saved names no host opens, marked', () => {
		const choices = networkChoices(openSharedNetworks(hosts, 'lab'), ['gone', 'dd']);
		assert.deepEqual(choices, [
			{ name: 'aa', where: ['pi4 (10.200.30.0/24)'], open: true },
			{ name: 'dd', where: ['pi3 (10.200.10.0/24)', 'pi4 (10.200.20.0/24)'], open: true },
			{ name: 'gone', where: [], open: false }
		]);
		assert.equal(toggleNetwork('dd', 'aa', true), 'dd, aa');
		assert.equal(toggleNetwork('dd, aa', 'dd', false), 'aa');
		assert.equal(toggleNetwork('dd', 'dd', true), 'dd');
	});

	test('the form renders a checkbox per choice and marks a saved name that is not open', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const html = render(CatalogImageForm, {
			props: { mode: 'edit', entry: imageEntry('serve', ['dd', 'gone']), hosts, projectSlug: 'lab', saveAction: '?/catalogUpdate', sets: [], setsStatus: 'ready', editor: null }
		}).body;
		for (const name of ['aa', 'dd', 'gone']) assert.match(html, new RegExp(`data-network-choice="${name}"`));
		assert.equal(html.includes('data-network-choice="closed"'), false);
		assert.equal(html.includes('Not open on any host'), true);
		assert.match(/<input[^>]*name="networks"[^>]*>/.exec(html)?.[0] ?? '', /value="dd, gone"/);
	});

	test('Networks card: rows per host and network with the entries that join, and the empty state', () => {
		const catalog: CatalogEntry[] = [imageEntry('web', ['dd']), imageEntry('api', ['dd', 'aa']), imageEntry('job')];
		assert.deepEqual(projectNetworkRows(hosts, 'lab', catalog), [
			{ host: 'pi3', name: 'dd', subnet: '10.200.10.0/24', entries: ['api', 'web'] },
			{ host: 'pi4', name: 'aa', subnet: '10.200.30.0/24', entries: ['api'] },
			{ host: 'pi4', name: 'dd', subnet: '10.200.20.0/24', entries: ['api', 'web'] }
		]);
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const html = render(NetworksCard, { props: { hosts, projectSlug: 'lab', catalog, snippetAction: '?/networkSnippet' } }).body;
		assert.equal(html.includes('10.200.30.0/24'), true);
		assert.equal(html.includes('No shared network is open to this project.'), false);
		const empty = render(NetworksCard, { props: { hosts: [host('pi3', policy({ shared_networks: [] }))], projectSlug: 'lab', catalog: [], snippetAction: '?/networkSnippet' } }).body;
		assert.equal(empty.replaceAll('&#39;', "'").includes("No shared network is open to this project. Ask the host's root to add one."), true);
		assert.equal(empty.includes('Ask for a network'), true);
	});

	test('the snippet action checks its fields and calls the v1.33 item 7 route', async () => {
		assert.equal(networkSnippetProblem('dd', '10.200.10.0/24'), null);
		assert.notEqual(networkSnippetProblem('DD', '10.200.10.0/24'), null);
		assert.notEqual(networkSnippetProblem('dd', '10.200.10.0'), null);
		assert.notEqual(networkSnippetProblem('dd', '300.1.1.0/24'), null);
		assert.equal(parseNetworkSnippet({ snippet: 'shared_networks = []\n' }), 'shared_networks = []\n');
		assert.equal(parseNetworkSnippet({ snippet: '' }), null);

		useApi('https://api.test');
		const calls = installFetch(() => envelope({ snippet: 'shared_networks = [{ name = "dd", subnet = "10.200.10.0/24" }]\n' }));
		const ev = (form: Record<string, string>) =>
			fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/networkSnippet`, form, params: { id: PROJECT.id }, user: USER, session: TOKEN }).event as never;
		const ok = (await projectActions.networkSnippet(ev({ host_id: HOST_ID, network: 'dd', subnet: '10.200.10.0/24' }))) as { networkSnippet: string };
		assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/hosts/${HOST_ID}/network-snippet`);
		assert.equal(calls[0]?.url.searchParams.get('network'), 'dd');
		assert.equal(calls[0]?.url.searchParams.get('subnet'), '10.200.10.0/24');
		assert.equal(ok.networkSnippet.includes('dd'), true);
		const bad = (await projectActions.networkSnippet(ev({ host_id: 'x', network: 'dd', subnet: '10.200.10.0/24' }))) as { status: number };
		assert.equal(bad.status, 400);
		assert.equal(calls.length, 1);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});
});

describe('Files dialog CRLF', () => {
	test('pasted text posts with LF line breaks', async () => {
		useApi('https://api.test');
		const calls = installFetch(() => envelope({ name: 'conf.txt', is_secret: false, size: 4, updated_at: '2026-11-16T00:00:00Z', updated_by: USER.id }));
		const ev = fakeEvent({
			url: `http://console.example/projects/${PROJECT.id}?/saveProjectFile`,
			form: { name: 'conf.txt', content: 'a=1\r\nb=2\r\n', secret: '0' },
			params: { id: PROJECT.id },
			user: USER,
			session: TOKEN
		}).event as never;
		await projectActions.saveProjectFile(ev);
		const put = calls.find((call) => call.method === 'PUT');
		assert.ok(put);
		assert.deepEqual(JSON.parse(put.body ?? '{}'), { content: 'a=1\nb=2\n', secret: false });
		globalThis.fetch = originalFetch;
		restoreEnv();
	});
});

describe('UI review fixes', () => {
	const rights = { canAdmin: true, canOrgAdmin: true, githubConnect: false };
	const privateFix = { check: 'env_values', status: 'missing', message: 'env K in set @serve has no value yet', fix: { action: 'fill_value', set: '@serve', key: 'K' } };
	const view = (extra: Record<string, unknown>) =>
		render(ReadinessView, {
			props: { result: parseReadiness({ ready: false, checks: [privateFix] }), rights, entry: 'serve', projectPath: `/projects/${PROJECT.id}`, connectAction: '?/c', snippetAction: '?/s', ...extra }
		}).body;

	test('1: a shown snippet keeps what was asked; any field change makes it stale', () => {
		const result = { hostName: 'pi3', network: 'dd', subnet: '10.200.10.0/24', snippet: 'x' };
		assert.equal(networkSnippetHeading(result).includes('on pi3 (network dd, 10.200.10.0/24)'), true);
		const ask = { hostId: HOST_ID, network: 'dd', subnet: '10.200.10.0/24' };
		assert.equal(sameSnippetAsk(ask, { ...ask }), true);
		assert.equal(sameSnippetAsk(ask, { ...ask, network: ' dd ' }), true);
		assert.equal(sameSnippetAsk(ask, { ...ask, hostId: '00000000-0000-4000-8000-0000000000ab' }), false);
		assert.equal(sameSnippetAsk(ask, { ...ask, network: 'de' }), false);
		assert.equal(sameSnippetAsk(ask, { ...ask, subnet: '10.200.11.0/24' }), false);
		assert.equal(sameSnippetAsk(null, ask), false);
	});

	test('2: an unread host list is not the empty state, the hint names the read hosts, and Add by name follows the rule and the limit', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const unknown = render(NetworksCard, { props: { hosts: [], hostsIncomplete: true, projectSlug: 'lab', catalog: [], snippetAction: '?/networkSnippet' } }).body;
		assert.equal(unknown.includes('Hosts could not be read, so networks are unknown.'), true);
		assert.equal(unknown.includes('No shared network is open to this project.'), false);

		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const props = { mode: 'edit', entry: imageEntry('serve'), projectSlug: 'lab', saveAction: '?/catalogUpdate', sets: [], setsStatus: 'ready', editor: null };
		const partial = render(CatalogImageForm, { props: { ...props, hosts: [], hostsIncomplete: true } as never }).body;
		assert.equal(partial.includes('The choices come only from the hosts that were read.'), true);
		assert.match(partial, /id="catalog-image-serve-network-by-name"/);
		const whole = render(CatalogImageForm, { props: { ...props, hosts: [] } as never }).body;
		assert.equal(whole.includes('network-by-name'), false);

		assert.deepEqual(addNetworkByName('dd', ' ee ', 4), { ok: true, text: 'dd, ee' });
		assert.deepEqual(addNetworkByName('dd', 'dd', 4), { ok: true, text: 'dd' });
		assert.equal(addNetworkByName('dd', 'Bad_Name', 4).ok, false);
		assert.deepEqual(addNetworkByName('a, b, c, d', 'e', 4), { ok: false, message: networksLimitText(4) });
	});

	test('5: at the limit a live line says so, and the unticked choices are disabled and dimmed', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const open = ['a', 'b', 'c', 'd', 'e'].map((name, i) => ({ name, subnet: `10.200.${i}.0/24` }));
		const hosts = [host('pi3', policy({ shared_networks: open }))];
		const base = { mode: 'edit', projectSlug: 'lab', saveAction: '?/catalogUpdate', sets: [], setsStatus: 'ready', editor: null, hosts };
		const full = render(CatalogImageForm, { props: { ...base, entry: imageEntry('serve', ['a', 'b', 'c', 'd']) } as never }).body;
		assert.match(full, /<p[^>]*aria-live="polite"[^>]*data-networks-limit[^>]*>Limit of 4 reached\. Clear one to choose another\.<\/p>/);
		const choiceE = /<label[^>]*data-network-choice="e"[^>]*>/.exec(full)?.[0] ?? '';
		assert.match(choiceE, /opacity-60/);
		assert.match(full, /id="catalog-image-serve-network-e"[^>]*disabled|disabled[^>]*id="catalog-image-serve-network-e"/);
		const choiceA = /<label[^>]*data-network-choice="a"[^>]*>/.exec(full)?.[0] ?? '';
		assert.equal(choiceA.includes('opacity-60'), false);
		const room = render(CatalogImageForm, { props: { ...base, entry: imageEntry('serve', ['a']) } as never }).body;
		assert.match(room, /data-networks-limit[^>]*><\/p>/);
	});

	test('7: a private-set fix keeps its @ name and opens the entry, not Settings → Env sets', () => {
		const parsed = parseReadiness({ ready: false, checks: [privateFix] });
		const fix = parsed?.checks[0]?.fix;
		assert.ok(fix);
		assert.equal(fix.set, '@serve');
		assert.equal(privateFixEntry(fix), 'serve');
		assert.equal(fixLabel(fix), 'Open entry secrets');
		assert.equal(fixHref(fix, '/projects/p'), '/projects/p?tab=catalog&catalog.q=serve');
		assert.equal(privateFixEntry({ action: 'fill_value', set: 'app', key: 'K' }), null);
		assert.equal(fixHref({ action: 'fill_value', set: 'app', key: 'K' }, '/projects/p'), '/projects/p?tab=settings#env-sets');

		// In the entry's Edit drawer: a button that shows the list there. In Deploy: a button that opens the drawer.
		const inEdit = view({ privateKeysId: 'private-keys-serve' });
		assert.match(inEdit, /<button[^>]*>(?:<!---->)*Open entry secrets/);
		assert.equal(inEdit.includes('tab=settings#env-sets'), false);
		const inDeploy = view({ onOpenEntry: () => {} });
		assert.match(inDeploy, /<button[^>]*>(?:<!---->)*Open entry secrets/);
		// Without either: a link to the entry in the catalog.
		const bare = view({});
		assert.match(bare, /href="\/projects\/[^"]*\?tab=catalog&amp;catalog\.q=serve"/);
	});

	test('3: the secrets list folds when the band is collapsed or long', () => {
		assert.equal(privateKeysFolded(3, false), false);
		assert.equal(privateKeysFolded(3, true), true);
		assert.equal(privateKeysFolded(6, false), true);
	});
});

describe('rotation and entry on the value fill page (v1.33 items 3 and 6)', () => {
	const base = {
		request_id: ID,
		project_id: PROJECT.id,
		project_name: 'Lab',
		set: 'services',
		key: 'STRIPE_API_KEY',
		state: 'pending',
		expires_at: '2026-11-16T10:15:00Z',
		created_at: '2026-11-16T10:00:00Z',
		reason: ''
	};
	const page = (request: unknown) => render(FillPage, { props: { data: { view: 'form', request, message: null }, form: null } as never }).body;

	test('replace and entry parse tolerantly', () => {
		const old = parseFillRequest(base, ID);
		assert.equal(old?.replace, false);
		assert.equal(old?.entry, '');
		assert.equal(parseFillRequest({ ...base, replace: true }, ID)?.replace, true);
		assert.equal(parseFillRequest({ ...base, replace: 'yes' }, ID)?.replace, false);
		assert.equal(parseFillRequest({ ...base, set: '@serve', entry: 'serve' }, ID)?.entry, 'serve');
		assert.equal(parseFillRequest({ ...base, set: '@serve', entry: 'Bad Name' }, ID)?.entry, '');
		assert.equal(parseFillRequest({ ...base, entry: 7 }, ID)?.entry, '');
	});

	test('the entry field wins over the @ name; the @ name is the fallback', () => {
		assert.deepEqual(fillSubject('@serve', 'web'), { label: 'Entry', name: 'web' });
		assert.deepEqual(fillSubject('@serve', ''), { label: 'Entry', name: 'serve' });
		const html = page(parseFillRequest({ ...base, set: '@serve', entry: 'web' }, ID));
		assert.match(html, /data-fill-subject[^>]*>web<\/dd>/);
	});

	test('a rotation says Replace the value of KEY, warns, and the button reads Replace value', () => {
		const html = page(parseFillRequest({ ...base, replace: true }, ID)).replaceAll('&#39;', "'");
		assert.equal(html.includes('Replace the value of STRIPE_API_KEY'), true);
		assert.equal(html.includes("Saving replaces the current value. The old value can't be recovered."), true);
		assert.match(html, /<button[^>]*type="submit"[^>]*>(?:<!---->)*Replace value/);
		const plain = page(parseFillRequest(base, ID)).replaceAll('&#39;', "'");
		assert.equal(plain.includes('Replace the value of'), false);
		assert.equal(plain.includes('Saving replaces the current value.'), false);
		assert.equal(plain.includes('Replace value'), false);
		assert.equal(plain.includes('Enter a value'), true);
	});
});

describe('B: an entry with private secrets saves, and keeps them', () => {
	const own = { key: 'GOOGLE_CLIENT_SECRET', set: '@serve', name: 'GOOGLE_CLIENT_SECRET' };
	const shared = { key: 'DB_PW', set: 'db', name: 'PASSWORD' };
	const known = [{ name: 'db', keys: ['PASSWORD'] }];

	test('a stored spec with an @ ref parses (it used to drop the entry)', () => {
		const spec = { ...imageEntry('serve').spec, secret_env: { K: '@serve.K', P: 'db.PASSWORD' } };
		assert.deepEqual(parseImageSpec(spec)?.secret_env, { K: '@serve.K', P: 'db.PASSWORD' });
		assert.equal(parseImageSpec({ ...spec, secret_env: { K: '@Bad.K' } }), null);
	});

	test('cleanEnvSources keeps the entry\'s own @ refs unchanged and refuses any other @x', () => {
		const ok = cleanEnvSources([], [own, shared], known, 'serve');
		assert.deepEqual(ok, { ok: true, envSets: [], secretEnv: { GOOGLE_CLIENT_SECRET: '@serve.GOOGLE_CLIENT_SECRET', DB_PW: 'db.PASSWORD' } });
		const other = cleanEnvSources([], [{ ...own, set: '@web' }], known, 'serve');
		assert.equal(other.ok, false);
		// On create (no entry), and without known sets, an @ ref is refused too.
		assert.equal(cleanEnvSources([], [own], known, null).ok, false);
		assert.equal(cleanEnvSources([], [own], undefined, null).ok, false);
		assert.equal(cleanEnvSources([], [own], undefined, 'serve').ok, true);
	});

	test('the container draft saves with a private ref; removing that row drops only its key', () => {
		const draft = { ...blankImageDraft(), image: 'nginx', secrets: [own, shared] };
		const saved = imageSpecFromDraft(draft, known, false, undefined, 'serve');
		assert.equal(saved.ok, true);
		assert.deepEqual(saved.ok && saved.spec.secret_env, { GOOGLE_CLIENT_SECRET: '@serve.GOOGLE_CLIENT_SECRET', DB_PW: 'db.PASSWORD' });
		const removed = imageSpecFromDraft({ ...draft, secrets: [shared] }, known, false, undefined, 'serve');
		assert.deepEqual(removed.ok && removed.spec.secret_env, { DB_PW: 'db.PASSWORD' });
		assert.equal(imageSpecFromDraft({ ...draft, secrets: [{ ...own, set: '@web' }] }, known, false, undefined, 'serve').ok, false);
	});

	test('the edit form shows an own private ref read-only and posts it back unchanged', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const entry = imageEntry('serve');
		const withPrivate = { ...entry, spec: { ...entry.spec, secret_env: { GOOGLE_CLIENT_SECRET: '@serve.GOOGLE_CLIENT_SECRET', DB_PW: 'db.PASSWORD' } } };
		const html = render(CatalogImageForm, {
			props: { mode: 'edit', entry: withPrivate, hosts: [], projectSlug: 'lab', saveAction: '?/catalogUpdate', sets: known, setsStatus: 'ready', editor: null } as never
		}).body;
		const row = /<div[^>]*data-private-secret="GOOGLE_CLIENT_SECRET"[^>]*>[\s\S]*?<\/div>/.exec(html)?.[0] ?? '';
		assert.match(row, /type="hidden"[^>]*value="@serve"|value="@serve"[^>]*type="hidden"/);
		assert.equal(row.includes('private, managed for this entry'), true);
		assert.equal(row.includes('role="radiogroup"'), false);
		assert.match(html, /aria-label="Remove private secret GOOGLE_CLIENT_SECRET"/);
	});

	test('the server PUTs the private ref unchanged, and refuses another entry\'s', async () => {
		useApi('https://api.test');
		const calls = installFetch(() => envelope({}));
		const form = (set: string) => ({
			entry_kind: 'image',
			name: 'serve',
			entry_name: 'serve',
			workload: 'service',
			image: 'nginx',
			restart: 'unless-stopped',
			secret_key: ['GOOGLE_CLIENT_SECRET', 'DB_PW'],
			secret_set: [set, 'db'],
			secret_name: ['GOOGLE_CLIENT_SECRET', 'PASSWORD']
		});
		const ev = (set: string) =>
			fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogUpdate`, session: TOKEN, user: USER, projects: [PROJECT], params: { id: PROJECT.id }, form: form(set) }).event as never;
		const saved = await Promise.resolve(projectActions.catalogUpdate(ev('@serve'))).catch((err: unknown) => err);
		assert.equal(isRedirect(saved), true);
		const put = calls.find((call) => call.method === 'PUT');
		assert.deepEqual(JSON.parse(put?.body ?? '{}').spec.secret_env, { GOOGLE_CLIENT_SECRET: '@serve.GOOGLE_CLIENT_SECRET', DB_PW: 'db.PASSWORD' });
		const before = calls.length;
		const refused = (await projectActions.catalogUpdate(ev('@web'))) as { status: number };
		assert.equal(refused.status, 400);
		assert.equal(calls.length, before);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});
});

describe('B: the program save path keeps its own private refs', () => {
	test('a program update PUTs @<entry>.K unchanged, and an @other ref answers 400 with no API call', async () => {
		useApi('https://api.test');
		const programHost = host('pi1', policy());
		const calls = installFetch((call) => {
			if (call.url.pathname.endsWith('/hosts')) return envelope({ items: [programHost], next_cursor: null });
			return envelope({});
		});
		const ev = (set: string) =>
			fakeEvent({
				url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogUpdate`,
				session: TOKEN,
				user: USER,
				projects: [PROJECT],
				params: { id: PROJECT.id },
				form: {
					editor_mode: 'edit',
					entry_name: 'tra',
					command: 'node server.js',
					dir: '/srv/tra',
					run_as: 'joe',
					restart: 'on-failure',
					secret_key: ['K', 'DB_PW'],
					secret_set: [set, 'db'],
					secret_name: ['K', 'PASSWORD']
				}
			}).event as never;
		const saved = await Promise.resolve(projectActions.catalogUpdate(ev('@tra'))).catch((err: unknown) => err);
		assert.equal(isRedirect(saved), true);
		const put = calls.find((call) => call.method === 'PUT');
		assert.equal(put?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/tra`);
		assert.deepEqual(JSON.parse(put?.body ?? '{}').secret_env, { K: '@tra.K', DB_PW: 'db.PASSWORD' });

		const writes = () => calls.filter((call) => call.method !== 'GET').length;
		const before = writes();
		const refused = (await projectActions.catalogUpdate(ev('@other'))) as { status: number };
		assert.equal(refused.status, 400);
		assert.equal(writes(), before);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});
});

describe('private-key reveal (api-v1 v1.33) and snippet access', () => {
	const headers = { 'x-sveltekit-action': 'true', accept: 'application/json' };
	const filled = (extra: Record<string, unknown>) => ({
		request_id: ID,
		project_id: PROJECT.id,
		project_name: 'Lab',
		set: '@serve',
		key: 'K',
		state: 'filled',
		expires_at: '2026-11-16T10:15:00Z',
		created_at: '2026-11-16T10:00:00Z',
		reason: '',
		...extra
	});
	const fillEv = (form: Record<string, string>) =>
		fakeEvent({ url: `http://console.example/fill/${ID}`, session: TOKEN, user: USER, projects: [PROJECT], params: { id: ID }, form, requestHeaders: headers }).event as never;

	test('paths: entry and key rules', () => {
		assert.equal(privateSetEntry('@serve'), 'serve');
		assert.equal(privateSetEntry('serve'), null);
		assert.equal(privateSetEntry('@Bad'), null);
		assert.equal(privateKeyRevealPath('p', 'serve', 'K'), '/v1/projects/p/catalog/serve/private-keys/K/reveal');
		assert.equal(privateKeyRevealPath('p', '../x', 'K'), null);
		assert.equal(privateKeyRevealPath('p', 'serve', 'k/..'), null);
	});

	test('1: the fill page reveals a private value through the entry route, preferring the request entry', async () => {
		useApi('https://api.test');
		for (const [extra, entry] of [[{}, 'serve'], [{ entry: 'web' }, 'web']] as const) {
			const calls = installFetch((call) => {
				if (call.method === 'GET') return envelope(filled(extra));
				return envelope({ key: 'K', value: 'generated-value' });
			});
			const shown = await fillActions.reveal(fillEv({ key: 'K' }));
			assert.deepEqual(shown, { revealed: { key: 'K', value: 'generated-value' } });
			const post = calls.find((call) => call.method === 'POST');
			assert.equal(post?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/${entry}/private-keys/K/reveal`);
		}
		// A shared set keeps the env-set route.
		const calls = installFetch((call) => (call.method === 'GET' ? envelope(filled({ set: 'services' })) : envelope({ key: 'K', value: 'v' })));
		await fillActions.reveal(fillEv({ key: 'K' }));
		assert.equal(calls.find((call) => call.method === 'POST')?.url.pathname, `/v1/projects/${PROJECT.id}/env-sets/services/keys/K/reveal`);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('2: the Secrets band reveal action calls the route once, shows a conflict, and needs the dialog fetch', async () => {
		useApi('https://api.test');
		const ev = (form: Record<string, string>, withHeaders = true) =>
			fakeEvent({
				url: `http://console.example/projects/${PROJECT.id}?/revealPrivateKey`,
				form,
				params: { id: PROJECT.id },
				user: USER,
				session: TOKEN,
				requestHeaders: withHeaders ? headers : undefined
			}).event as never;
		let calls = installFetch(() => envelope({ key: 'K', value: 'generated-value' }));
		const shown = (await projectActions.revealPrivateKey(ev({ set: '@serve', key: 'K' }))) as { revealed: { key: string; value: string } };
		assert.deepEqual(shown.revealed, { key: 'K', value: 'generated-value' });
		assert.equal(calls[0]?.method, 'POST');
		assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/serve/private-keys/K/reveal`);

		const text = 'the value of K was already shown or was not generated; edit it to set a new value';
		installFetch(() => envelope(null, 409, 'conflict', text));
		const refused = (await projectActions.revealPrivateKey(ev({ set: '@serve', key: 'K' }))) as { status: number; data: { revealError: string } };
		assert.equal(refused.status, 409);
		assert.equal(refused.data.revealError, text);

		calls = installFetch(() => envelope({ key: 'K', value: 'x' }));
		assert.equal(((await projectActions.revealPrivateKey(ev({ set: 'shared', key: 'K' }))) as { status: number }).status, 400);
		assert.equal(((await projectActions.revealPrivateKey(ev({ set: '@serve', key: 'K' }, false))) as { status: number }).status, 400);
		assert.equal(calls.length, 0);
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('2: the band offers Reveal once only for a pending generated secret, to admins', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		// A server render shows the loading status only (the list comes from the post), so the rule is checked in source.
		assert.equal(render(PrivateKeys, { props: { entry: 'serve' } }).body.includes('Loading secrets'), true);
		const source = readFileSync(new URL('../src/routes/projects/[id]/PrivateKeys.svelte', import.meta.url), 'utf8');
		assert.match(source, /row\.reveal_pending && row\.is_secret && canAdmin/);
		const pending = { key: 'K', is_secret: true, needs_value: false, reveal_pending: true, generated: true };
		const other = { ...pending, key: 'L' };
		assert.deepEqual(afterPrivateReveal([pending, other], 'K'), [{ ...pending, reveal_pending: false }, other]);
		assert.deepEqual(privateKeyNotes(afterPrivateReveal([pending], 'K')[0]!), ['secret']);
		assert.match(source, /label="Reveal \{row\.key\} once"/);
		assert.match(source, /<EnvRevealDialog setName="@\{entry\}" \{revealKey\} action=\{revealAction\}/);
	});

	test('4: snippet hosts are the deploy ones, and not_found reads as no deploy access', async () => {
		const hosts = [
			host('pi3', policy(), { id: '00000000-0000-4000-8000-0000000000a1' }),
			host('pi4', policy(), { id: '00000000-0000-4000-8000-0000000000a2', access_level: 'metrics' }),
			host('pi5', null, { id: '00000000-0000-4000-8000-0000000000a3' })
		];
		assert.deepEqual(
			networkSnippetHosts(hosts, (item) => item.programs != null),
			[{ id: '00000000-0000-4000-8000-0000000000a1', name: 'pi3' }]
		);
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=settings`);
		const none = render(NetworksCard, { props: { hosts: [hosts[1]!], projectSlug: 'lab', catalog: [], snippetAction: '?/networkSnippet' } }).body;
		assert.equal(none.includes('Ask for a network'), false);

		useApi('https://api.test');
		installFetch(() => envelope(null, 404, 'not_found', 'not found'));
		const ev = fakeEvent({
			url: `http://console.example/projects/${PROJECT.id}?/networkSnippet`,
			form: { host_id: HOST_ID, network: 'dd', subnet: '10.200.10.0/24' },
			params: { id: PROJECT.id },
			user: USER,
			session: TOKEN
		}).event as never;
		const refused = (await projectActions.networkSnippet(ev)) as { status: number; data: { networkSnippetMessage: string } };
		assert.equal(refused.status, 404);
		assert.equal(refused.data.networkSnippetMessage, NETWORK_SNIPPET_NO_ACCESS);
		assert.equal(NETWORK_SNIPPET_NO_ACCESS, "This project can't deploy to that host.");
		globalThis.fetch = originalFetch;
		restoreEnv();
	});
});

describe('Ask for a network: the Host select shows a name', () => {
	test('the trigger text is the chosen host\'s name, never its id', () => {
		const hosts = [
			{ id: 'f18530f2-19b3-4000-8000-000000000001', name: 'pi3-2' },
			{ id: '00000000-0000-4000-8000-0000000000a2', name: 'pi4' }
		];
		assert.equal(snippetHostLabel(hosts, 'f18530f2-19b3-4000-8000-000000000001'), 'pi3-2');
		assert.equal(snippetHostLabel(hosts, ''), 'Choose a host');
		assert.equal(snippetHostLabel(hosts, 'gone'), 'Choose a host');
		// The dialog content renders only while open, so the trigger markup is checked in the source.
		const source = readFileSync(new URL('../src/routes/projects/[id]/NetworkSnippetDialog.svelte', import.meta.url), 'utf8');
		assert.match(source, /<Select\.Trigger id="network-snippet-host" class="w-full">\{snippetHostLabel\(hosts, hostId\)\}<\/Select\.Trigger>/);
		assert.equal(source.includes('<Select.Value'), false);
	});
});

