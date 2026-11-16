import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, test } from 'node:test';
import { isRedirect } from '@sveltejs/kit';
import { createRawSnippet } from 'svelte';
import { render } from 'svelte/server';
import {
	compareVersions,
	hasUnseenRelease,
	inlineParts,
	parseChangelog,
	SEEN_VERSION_COOKIE,
	seenVersionCookie
} from '../src/lib/changelog.ts';
import { agentUpdateText } from '../src/lib/host-view.ts';
import { parseHost } from '../src/lib/server/shape.ts';
import { latestVersion } from '../src/lib/server/changelog.ts';
import AgentVersion from '../src/lib/components/AgentVersion.svelte';
import { load as layoutLoad } from '../src/routes/+layout.server.ts';
import RootLayout from '../src/routes/+layout.svelte';
import { load as whatsNewLoad } from '../src/routes/whats-new/+page.server.ts';
import WhatsNewPage from '../src/routes/whats-new/+page.svelte';
import { fakeEvent, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
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

	test('version order and the unseen rule', () => {
		assert.equal(compareVersions('0.2.9', '0.2.10'), -1);
		assert.equal(compareVersions('0.10.0', '0.9.9'), 1);
		assert.equal(compareVersions('0.2.37', '0.2.37'), 0);
		assert.equal(compareVersions('v0.2.37', '0.2.37'), null);
		assert.equal(hasUnseenRelease('0.2.37', undefined), true);
		assert.equal(hasUnseenRelease('0.2.37', ''), true);
		assert.equal(hasUnseenRelease('0.2.37', 'junk'), true);
		assert.equal(hasUnseenRelease('0.2.37', '0.2.36'), true);
		assert.equal(hasUnseenRelease('0.2.37', '0.2.37'), false);
		assert.equal(hasUnseenRelease('0.2.37', '0.2.38'), false);
		assert.equal(hasUnseenRelease(null, undefined), false);
	});

	test('the seen cookie follows the bot_tz pattern', () => {
		assert.equal(SEEN_VERSION_COOKIE, 'bot_seen_version');
		assert.equal(seenVersionCookie('0.2.37', false), 'bot_seen_version=0.2.37; Path=/; Max-Age=31536000; SameSite=Lax');
		assert.equal(seenVersionCookie('0.2.37', true), 'bot_seen_version=0.2.37; Path=/; Max-Age=31536000; SameSite=Lax; Secure');
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

describe('account menu dot', () => {
	const latest = latestVersion() as string;
	const layoutData = (url: string, cookie?: string, signedIn = true) => {
		const fake = fakeEvent({ url, session: TOKEN, user: signedIn ? USER : null });
		if (cookie !== undefined) fake.jar.set(SEEN_VERSION_COOKIE, cookie);
		return layoutLoad(fake.event as never) as { whatsNew: { version: string | null; unseen: boolean } | null };
	};

	test('the layout load reads bot_seen_version', () => {
		assert.deepEqual(layoutData('http://console.example/projects').whatsNew, { version: latest, unseen: true });
		assert.equal(layoutData('http://console.example/projects', '0.2.36').whatsNew?.unseen, true);
		assert.equal(layoutData('http://console.example/projects', 'odd').whatsNew?.unseen, true);
		assert.equal(layoutData('http://console.example/projects', latest).whatsNew?.unseen, false);
		// The page itself counts as seen, so the dot never shows there.
		assert.equal(layoutData('http://console.example/whats-new').whatsNew?.unseen, false);
		assert.equal(layoutData('http://console.example/projects', undefined, false).whatsNew, null);
	});

	test('the dot is in the server HTML only while unseen', () => {
		setPageUrl('http://console.example/projects');
		setRouteId('/projects');
		const children = createRawSnippet(() => ({ render: () => '<p>child</p>' }));
		const user = { id: USER.id, name: USER.name, avatarUrl: null };
		const base = { user, orgs: [], theme: 'system' as const, timeZone: 'UTC' };
		const unseen = render(RootLayout, { props: { children, data: { ...base, whatsNew: { version: latest, unseen: true } } } }).body;
		assert.equal(unseen.includes('data-unseen-dot'), true);
		assert.equal(unseen.includes('(new release)'), true);
		const seen = render(RootLayout, { props: { children, data: { ...base, whatsNew: { version: latest, unseen: false } } } }).body;
		assert.equal(seen.includes('data-unseen-dot'), false);
	});

	test('the menu item names the version and the page stores the seen cookie', () => {
		const layout = readFileSync(new URL('../src/routes/+layout.svelte', import.meta.url), 'utf8');
		assert.match(layout, /href="\/whats-new"/);
		assert.match(layout, /What's new/);
		assert.match(layout, /v\{data\.whatsNew\.version\}/);
		// The browser check found the menu as narrow as a short name: items wrapped and the version and dot were cut off.
		assert.match(layout, /<DropdownMenu\.Content align="end" class="w-auto min-w-56 max-w-\[calc\(100vw-2rem\)\]">/);
		assert.match(layout, /<span data-whats-new-meta class="ml-auto flex shrink-0 items-center[^"]*">/);
		assert.match(layout, /data-unseen-dot class="size-2 shrink-0 rounded-full bg-primary"/);
		const page = readFileSync(new URL('../src/routes/whats-new/+page.svelte', import.meta.url), 'utf8');
		assert.match(page, /onMount\(\(\) => \{[\s\S]*rememberSeenVersion\(latest\)/);
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

	test('all three host views use AgentVersion beside the agent version', () => {
		for (const file of ['src/routes/projects/[id]/HostsSection.svelte', 'src/routes/orgs/[id]/OrgHostsSection.svelte', 'src/routes/hosts/[id]/OverviewTab.svelte']) {
			const source = readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
			assert.match(source, /<AgentVersion \{host\} \/>/, file);
			assert.equal(source.includes("host.agent_version ?? 'n/a'"), false, file);
		}
	});
});
