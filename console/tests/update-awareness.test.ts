import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, test } from 'node:test';
import { isRedirect } from '@sveltejs/kit';
import { createRawSnippet } from 'svelte';
import { render } from 'svelte/server';
import {
	compareVersions,
	inlineParts,
	parseChangelog,
	SEEN_VERSION_COOKIE
} from '../src/lib/changelog.ts';
import { agentUpdateTooltip } from '../src/lib/agent-upgrade.ts';

const agentUpdateText = (latest: string) => agentUpdateTooltip(latest, 'view');
import { parseHost } from '../src/lib/server/shape.ts';
import { changelogEntries, latestVersion } from '../src/lib/server/changelog.ts';
import { IMPORT_TIMEOUT_MS } from '../src/lib/server/notifications.ts';
import AgentVersion from '../src/lib/components/AgentVersion.svelte';
import { load as layoutLoad } from '../src/routes/+layout.server.ts';
import RootLayout from '../src/routes/+layout.svelte';
import { load as whatsNewLoad } from '../src/routes/whats-new/+page.server.ts';
import WhatsNewPage from '../src/routes/whats-new/+page.svelte';
import { envelope, fakeEvent, installFetch, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl, setRouteId } from './stubs/app-state.ts';

// spec-update-awareness §2.4, §2.5 and §6 (console).

const CHANGELOG = readFileSync(new URL('../../CHANGELOG.md', import.meta.url), 'utf8');
const PACKAGE = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')) as { version: string };
const strip = (html: string) => html.replace(/<!--[\s\S]*?-->/g, '');

describe('changelog', () => {
	test('the newest CHANGELOG.md release is the console package version (§5 item 1)', () => {
		const entries = parseChangelog(CHANGELOG);
		assert.ok(entries.length >= 2);
		assert.equal(entries[0].version, PACKAGE.version);
		assert.equal(latestVersion(), PACKAGE.version);
		for (let i = 1; i < entries.length; i++) assert.equal(compareVersions(entries[i].version, entries[i - 1].version), -1);
	});

	test('parses headings and bullets, skips the title and prose, drops what does not fit', () => {
		const entries = parseChangelog('# Changelog\n\nProse.\n\n## v1.10.0 — 2026-11-20\n- New `tool` and `x`.\n- Plain.\n* star\n## bad heading\n## v1.9.0 — 2026-11-01\n\n## v1.8.0 — 2026-10-01\r\n- Old.\r\n');
		assert.deepEqual(
			entries.map((e) => [e.version, e.date, e.items.length]),
			[
				['1.10.0', '2026-11-20', 2],
				['1.8.0', '2026-10-01', 1]
			]
		);
		assert.deepEqual(entries[0].items[0], [
			{ code: false, text: 'New ' },
			{ code: true, text: 'tool' },
			{ code: false, text: ' and ' },
			{ code: true, text: 'x' },
			{ code: false, text: '.' }
		]);
	});

	test('an empty bullet is skipped, and headings follow the Go rule (no leading zeros, 9 digits)', () => {
		const entries = parseChangelog('## v1.0.0 — 2026-11-20\n- ``\n- Kept.\n## v0.02.0 — 2026-11-01\n- Dropped.\n## v1234567890.0.0 — 2026-11-01\n- Dropped.\n## v0.0.1 — 2026-10-01\n- Old.\n');
		assert.deepEqual(
			entries.map((e) => [e.version, e.items.length]),
			[
				['1.0.0', 1],
				['0.0.1', 1]
			]
		);
	});

	test('inline code: an unmatched backtick stays text, markup is never interpreted', () => {
		assert.deepEqual(inlineParts('a `b'), [{ code: false, text: 'a `b' }]);
		assert.deepEqual(inlineParts('<b>x</b> `<i>`'), [
			{ code: false, text: '<b>x</b> ' },
			{ code: true, text: '<i>' }
		]);
	});

	test('version order', () => {
		assert.equal(compareVersions('0.2.9', '0.2.10'), -1);
		assert.equal(compareVersions('0.10.0', '0.9.9'), 1);
		assert.equal(compareVersions('0.2.37', '0.2.37'), 0);
		assert.equal(compareVersions('v0.2.37', '0.2.37'), null);
	});

	test('the retired seen cookie keeps its name for the one-time import', () => {
		assert.equal(SEEN_VERSION_COOKIE, 'bot_seen_version');
	});

	test('no markdown library, no {@html}, no localStorage', () => {
		for (const file of ['src/lib/changelog.ts', 'src/lib/server/changelog.ts', 'src/routes/whats-new/+page.svelte', 'src/routes/+layout.svelte']) {
			const source = readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
			assert.equal(source.includes('{@html'), false, file);
			assert.equal(source.includes('localStorage'), false, file);
			assert.equal(/from ['"](marked|markdown-it|mdsvex|remark)/.test(source), false, file);
		}
		// The file is read on the server only.
		const client = readFileSync(new URL('../src/lib/changelog.ts', import.meta.url), 'utf8');
		assert.equal(client.includes('?raw'), false);
	});
});

describe('/whats-new', () => {
	test('a signed-out visitor is sent to sign-in', () => {
		useApi('https://api.example');
		try {
			const event = fakeEvent({ url: 'http://console.example/whats-new' }).event as never;
			assert.throws(
				() => whatsNewLoad(event),
				(err: unknown) => isRedirect(err) && err.status === 303 && err.location.startsWith('/signin')
			);
		} finally {
			restoreEnv();
		}
	});

	test('renders every release, newest first, with inline code as <code>', () => {
		useApi('https://api.example');
		try {
			const event = fakeEvent({ url: 'http://console.example/whats-new', session: TOKEN, user: USER }).event as never;
			const data = whatsNewLoad(event) as { entries: ReturnType<typeof parseChangelog> };
			assert.deepEqual(data.entries, parseChangelog(CHANGELOG));
			setPageUrl('http://console.example/whats-new');
			const html = strip(render(WhatsNewPage, { props: { data } as never }).body);
			assert.match(html, /<h1[^>]*>What's new<\/h1>/);
			let last = -1;
			for (const entry of data.entries) {
				const at = html.indexOf(`id="release-${entry.version}"`);
				assert.ok(at > last, entry.version);
				last = at;
				assert.equal(html.includes(entry.date), true);
			}
			assert.match(html, /<code[^>]*>whats_new<\/code>/);
			assert.match(html, new RegExp(`<time datetime="${data.entries[0].date}">${data.entries[0].date}</time>`));
			assert.match(html, /<span aria-hidden="true">·<\/span>/);
		} finally {
			restoreEnv();
		}
	});
});

// spec-header-org-project-bell §3.2 and §4 option B: the dot moved to the bell; the cookie is imported once.
describe('What\'s new moves to the bell', () => {
	const latest = latestVersion() as string;
	const API = 'https://api.test';

	async function layoutWith(cookie: string | undefined, status = 200) {
		useApi(API);
		const calls = installFetch(() => (status === 200 ? envelope(null) : envelope(null, status, 'error', 'down')));
		const fake = fakeEvent({ url: 'http://console.example/projects', session: TOKEN, user: USER });
		if (cookie !== undefined) fake.jar.set(SEEN_VERSION_COOKIE, cookie);
		try {
			const data = (await layoutLoad(fake.event as never)) as { whatsNew: { version: string | null } | null };
			return { data, calls, jar: fake.jar, sets: fake.sets };
		} finally {
			restoreEnv();
		}
	}

	test('the layout load passes the version only, no unseen flag', async () => {
		const { data, calls } = await layoutWith(undefined);
		assert.deepEqual(data.whatsNew, { version: latest });
		assert.equal(calls.length, 0);
	});

	const newest = changelogEntries().slice(0, 5).map((entry) => entry.version);

	test('a cookie naming the newest release marks every release item read once, then goes', async () => {
		const { calls, jar, sets } = await layoutWith(latest);
		assert.equal(calls.length, 1);
		assert.equal(calls[0].method, 'POST');
		assert.equal(calls[0].url.pathname, '/v1/me/notifications/read');
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { keys: newest.map((v) => `release:${v}`) });
		assert.equal(jar.has(SEEN_VERSION_COOKIE), false);
		assert.deepEqual(sets.find((op) => op.name === SEEN_VERSION_COOKIE), { op: 'delete', name: SEEN_VERSION_COOKIE, options: { path: '/' } });
	});

	test('after an upgrade, the releases at or below the seen version are marked, the newer ones stay unread', async () => {
		const { calls, jar } = await layoutWith(newest[1]);
		assert.equal(calls.length, 1);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { keys: newest.slice(1).map((v) => `release:${v}`) });
		assert.equal(jar.has(SEEN_VERSION_COOKIE), false);
	});

	test('the import is bounded at 2 s; a timeout keeps the cookie', async () => {
		assert.equal(IMPORT_TIMEOUT_MS, 2000);
		useApi(API);
		const calls = installFetch(
			(call) =>
				new Promise<Response>((_, reject) => {
					call.signal?.addEventListener('abort', () => reject(call.signal?.reason));
				})
		);
		const fake = fakeEvent({ url: 'http://console.example/projects', session: TOKEN, user: USER });
		fake.jar.set(SEEN_VERSION_COOKIE, latest);
		const started = Date.now();
		// AbortSignal.timeout uses an unref'd timer, and the stub fetch holds no
		// handle, so nothing keeps the event loop alive while the load waits: Node
		// would exit and cancel this test and every one after it. A server always
		// holds the loop open; here a ref'd timer stands in for it.
		const keepAlive = setTimeout(() => {}, 10_000);
		try {
			await layoutLoad(fake.event as never);
		} finally {
			clearTimeout(keepAlive);
			restoreEnv();
		}
		const took = Date.now() - started;
		assert.equal(calls.length, 1);
		assert.ok(took >= 1900 && took < 5000, `took ${took} ms`);
		assert.equal(fake.jar.get(SEEN_VERSION_COOKIE), latest);
	});

	test('an older or odd cookie is dropped without a read mark', async () => {
		for (const value of ['0.0.1', 'odd']) {
			const { calls, jar } = await layoutWith(value);
			assert.equal(calls.length, 0, value);
			assert.equal(jar.has(SEEN_VERSION_COOKIE), false, value);
		}
	});

	test('a failed read mark keeps the cookie for the next load', async () => {
		const { calls, jar } = await layoutWith(latest, 503);
		assert.equal(calls.length, 1);
		assert.equal(jar.get(SEEN_VERSION_COOKIE), latest);
	});

	test('the account menu has a plain What\'s new item and no dot', () => {
		setPageUrl('http://console.example/projects');
		setRouteId('/projects');
		const children = createRawSnippet(() => ({ render: () => '<p>child</p>' }));
		const user = { id: USER.id, name: USER.name, avatarUrl: null };
		const html = render(RootLayout, {
			props: { children, data: { user, orgs: [], projects: [], theme: 'system' as const, timeZone: 'UTC', whatsNew: { version: latest } } }
		}).body;
		assert.equal(html.includes('data-unseen-dot'), false);
		assert.equal(html.includes('(new release)'), false);
		const layout = readFileSync(new URL('../src/routes/+layout.svelte', import.meta.url), 'utf8');
		assert.match(layout, /href="\/whats-new"/);
		assert.match(layout, /What's new/);
		assert.match(layout, /v\{data\.whatsNew\.version\}/);
		assert.match(layout, /<DropdownMenu\.Content align="end" class="w-auto min-w-56 max-w-\[calc\(100vw-2rem\)\]">/);
		assert.doesNotMatch(layout, /data-unseen-dot|SEEN_VERSION_COOKIE|unseen/);
		const server = readFileSync(new URL('../src/routes/+layout.server.ts', import.meta.url), 'utf8');
		assert.doesNotMatch(server, /hasUnseenRelease|unseen/);
	});

	test('/whats-new no longer stores a cookie and anchors each release for the bell', () => {
		const page = readFileSync(new URL('../src/routes/whats-new/+page.svelte', import.meta.url), 'utf8');
		assert.doesNotMatch(page, /rememberSeenVersion|document\.cookie/);
		assert.match(page, /<section id="v\{entry\.version\}"/);
	});
});

describe('agent update hint (api-v1 v1.34)', () => {
	const raw = {
		id: '00000000-0000-4000-8000-0000000000aa',
		org_id: '00000000-0000-4000-8000-0000000000a0',
		name: 'pi1',
		node_type: 'pi',
		os: 'linux',
		arch: 'arm64',
		always_on: true,
		agent_version: '0.2.9',
		status: 'online',
		last_seen_at: null,
		enrolled_at: '2026-11-01T00:00:00Z',
		revoked_at: null,
		access_level: 'deploy',
		programs: null
	};

	test('parseHost reads latest_agent_version and update_available as a hint', () => {
		const set = parseHost({ ...raw, latest_agent_version: '0.2.10', update_available: true });
		assert.equal(set?.latest_agent_version, '0.2.10');
		assert.equal(set?.update_available, true);
		const current = parseHost({ ...raw, agent_version: '0.2.10', latest_agent_version: '0.2.10', update_available: false });
		assert.equal(current?.update_available, false);
		const unset = parseHost({ ...raw, latest_agent_version: null, update_available: false });
		assert.equal(unset?.latest_agent_version, null);
		assert.equal(unset?.update_available, false);
		// An older API, or an odd value: the host stays, with no hint.
		const old = parseHost(raw);
		assert.equal(old?.latest_agent_version, null);
		assert.equal(old?.update_available, false);
		const odd = parseHost({ ...raw, latest_agent_version: '<b>1</b>', update_available: true });
		assert.equal(odd?.id, raw.id);
		assert.equal(odd?.update_available, false);
		assert.equal(parseHost({ ...raw, latest_agent_version: '0.2.10', update_available: 'yes' })?.update_available, false);
	});

	test('the badge shows only for update_available, with the latest version in its tooltip text', () => {
		const shown = render(AgentVersion, { props: { host: { agent_version: '0.2.9', latest_agent_version: '0.2.10', update_available: true } } }).body;
		assert.match(shown, /data-agent-update="0.2.10"/);
		assert.equal(shown.includes('Update available'), true);
		assert.equal(shown.includes(agentUpdateText('0.2.10')), true);
		// A button trigger (tap toggles it), described once by the sr-only text.
		assert.match(shown, /<button[^>]*aria-describedby="([^"]+)"[^>]*data-agent-update="0.2.10"/);
		const describedBy = /aria-describedby="([^"]+)"/.exec(shown)?.[1];
		assert.match(shown, new RegExp(`<span id="${describedBy}" class="sr-only">${agentUpdateText('0.2.10').replace(/\./g, '\\.')}</span>`));
		assert.equal(shown.split(agentUpdateText('0.2.10')).length - 1, 1);
		assert.equal(agentUpdateText('0.2.10').includes('0.2.10'), true);
		const none = render(AgentVersion, { props: { host: { agent_version: '0.2.10', latest_agent_version: '0.2.10', update_available: false } } }).body;
		assert.equal(none.includes('Update available'), false);
		assert.equal(none.includes('0.2.10'), true);
		const missing = render(AgentVersion, { props: { host: { agent_version: null } } }).body;
		assert.equal(missing.includes('n/a'), true);
		assert.equal(missing.includes('Update available'), false);
	});

	test('a revoked host the API marks update_available false shows no Update available', () => {
		const revoked = parseHost({ ...raw, status: 'revoked', revoked_at: '2026-11-10T00:00:00Z', latest_agent_version: '0.2.10', update_available: false });
		assert.equal(revoked?.status, 'revoked');
		assert.equal(revoked?.latest_agent_version, '0.2.10');
		assert.equal(revoked?.update_available, false);
		const html = render(AgentVersion, { props: { host: revoked! } }).body;
		assert.equal(html.includes('Update available'), false);
		assert.equal(html.includes('data-agent-update'), false);
		assert.equal(html.includes('0.2.9'), true);
	});

	test('all three host views use AgentVersion beside the agent version', () => {
		for (const file of ['src/routes/projects/[id]/HostsSection.svelte', 'src/routes/orgs/[id]/OrgHostsSection.svelte', 'src/routes/hosts/[id]/OverviewTab.svelte']) {
			const source = readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
			// api-v1 v1.48: with the caller's upgrade right (and icon-only in the tables).
			assert.match(source, /<AgentVersion \{host\} [^>]*canUpgrade[^>]*\/>/, file);
			assert.equal(source.includes("host.agent_version ?? 'n/a'"), false, file);
		}
	});
});
