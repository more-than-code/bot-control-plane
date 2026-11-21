import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, test } from 'node:test';
import { render } from 'svelte/server';
import type { Notification } from '../src/lib/api-types.ts';
import { BELL_POLL_MS, bellLabel, needActionCount, problemAge, problemKindOf } from '../src/lib/notifications.ts';
import { bellNeedAction, browserPoll, createBell, initialBellState } from '../src/lib/notifications-bell.ts';
import { parseNotification, parseNotificationList } from '../src/lib/server/notifications.ts';
import NotificationsBell from '../src/lib/components/NotificationsBell.svelte';
import NotificationsList from '../src/lib/components/NotificationsList.svelte';

// spec-bell-problems §3.3; api-v1 v1.41 item 3.

const HOST = '00000000-0000-4000-8000-0000000000a1';
const PROJECT = '00000000-0000-4000-8000-000000000101';

const HOST_OPEN: Notification = {
	key: 'problem:00000000-0000-4000-8000-0000000000f1',
	kind: 'problem',
	title: 'pi2-2 is offline',
	detail: 'No report for 25 min.',
	href: `/hosts/${HOST}`,
	at: '2026-11-21T08:00:00Z',
	read: false,
	level: 'needs_action',
	state: 'open'
};
const REGISTRY_OPEN: Notification = {
	...HOST_OPEN,
	key: 'problem:00000000-0000-4000-8000-0000000000f2',
	title: 'ghcr.io login was refused',
	detail: 'Token expired or revoked. Reconnect it in Settings.',
	href: `/projects/${PROJECT}?tab=settings#registries`
};
const DEPLOY_RESOLVED: Notification = {
	...HOST_OPEN,
	key: 'problem:00000000-0000-4000-8000-0000000000f3',
	title: 'web is running on pi1',
	detail: 'The next deploy succeeded.',
	href: `/projects/${PROJECT}?catalog.host=${HOST}&catalog.q=web&tab=catalog`,
	at: '2026-11-21T05:25:00Z',
	read: true,
	level: 'heads_up',
	state: 'resolved'
};
const RELEASE: Notification = {
	key: 'release:0.2.42',
	kind: 'release',
	title: 'Deploybot v0.2.42 is out',
	detail: 'Problems in the bell.',
	href: '/whats-new#v0.2.42',
	at: '2026-11-21T00:00:00Z',
	read: false
};

describe('bell problem rows', () => {
	test('kind from the link, age as open or resolved', () => {
		assert.equal(problemKindOf(HOST_OPEN), 'host_offline');
		assert.equal(problemKindOf(REGISTRY_OPEN), 'registry_login');
		assert.equal(problemKindOf(DEPLOY_RESOLVED), 'deploy');
		assert.equal(problemKindOf(RELEASE), null);
		const now = Date.parse('2026-11-21T08:25:30Z');
		assert.equal(problemAge(HOST_OPEN, now), 'open 25 min');
		assert.equal(problemAge(DEPLOY_RESOLVED, now), 'resolved 3 h ago');
		assert.equal(problemAge({ ...HOST_OPEN, at: '2026-11-21T08:25:10Z' }, now), 'open 1 min');
		assert.equal(problemAge({ ...HOST_OPEN, at: '2026-11-18T08:00:00Z' }, now), 'open 3 d');
		assert.equal(problemAge(RELEASE, now), null);
		assert.equal(problemAge({ ...HOST_OPEN, at: 'nope' }, now), null);
	});

	test('icons by kind, detail and age, resolved rows muted, needs action marked', () => {
		const html = render(NotificationsList, { props: { items: [HOST_OPEN, REGISTRY_OPEN, DEPLOY_RESOLVED, RELEASE] } }).body;
		assert.match(html, /lucide-server-off/);
		assert.match(html, /lucide-key-round/);
		assert.match(html, /lucide-rotate-ccw/);
		assert.match(html, /lucide-sparkles/);
		assert.match(html, /No report for 25 min\. <span aria-hidden="true">·<\/span>/);
		assert.match(html, />open \d+ (min|h|d)<\/time>/);
		assert.match(html, />resolved \d+ (min|h|d) ago<\/time>/);
		assert.match(html, /data-state="resolved"[^>]*class="[^"]*text-muted-foreground/);
		assert.match(html, /data-state="open"[^>]*class="[^"]*text-foreground/);
		assert.equal((html.match(/<span class="sr-only">Needs action:<\/span>/g) ?? []).length, 2);
		assert.match(html, /bg-destructive/);
		// The rows link to the place that fixes the problem.
		assert.match(html, new RegExp(`href="/hosts/${HOST}"`));
		assert.match(html, /href="\/projects\/[^"]+\?tab=settings#registries"/);
		const source = readFileSync(new URL('../src/lib/components/NotificationsList.svelte', import.meta.url), 'utf8');
		assert.doesNotMatch(source, /#[0-9a-fA-F]{3,8}\b|rgb\(|oklch\(/);
	});
});

describe('bell: host config file problems (api-v1 v1.44 item 8)', () => {
	const HOST_FILE_OPEN: Notification = {
		...HOST_OPEN,
		key: 'problem:00000000-0000-4000-8000-0000000000f4',
		title: 'dd-policy on pi3 could not be restored',
		detail: 'The last apply was rollback_failed.',
		href: `/hosts/${HOST}?tab=files&files.name=dd-policy`
	};

	test('a host link to the Config files tab is host_file, other host links stay host_offline', () => {
		assert.equal(problemKindOf(HOST_FILE_OPEN), 'host_file');
		assert.equal(problemKindOf({ ...HOST_FILE_OPEN, href: `/hosts/${HOST}?files.name=x&tab=files` }), 'host_file');
		assert.equal(problemKindOf({ ...HOST_FILE_OPEN, href: `/hosts/${HOST}?tab=filesystem` }), 'host_offline');
		assert.equal(problemKindOf(HOST_OPEN), 'host_offline');
	});

	test('its row has the file warning icon and links to the file', () => {
		const html = render(NotificationsList, { props: { items: [HOST_FILE_OPEN] } }).body.replaceAll('&amp;', '&');
		assert.match(html, /lucide-file-(warning|exclamation-point)/);
		assert.doesNotMatch(html, /lucide-server-off/);
		assert.match(html, new RegExp(`href="/hosts/${HOST}\\?tab=files&files\\.name=dd-policy"`));
		assert.equal(needActionCount({ items: [HOST_FILE_OPEN], unread: 1 } as never), 1);
	});
});

describe('bell dot and label', () => {
	test('"N unread, M need action", without the second part at 0', () => {
		assert.equal(bellLabel(3, 2), 'Notifications, 3 unread, 2 need action');
		assert.equal(bellLabel(3, 0), 'Notifications, 3 unread');
		assert.equal(bellLabel(3), 'Notifications, 3 unread');
		assert.equal(bellLabel(0, 0), 'Notifications');
		assert.equal(bellLabel(null), 'Notifications');
	});

	test('need action counts unread open needs_action problems only', () => {
		const list = {
			items: [
				HOST_OPEN,
				{ ...REGISTRY_OPEN, read: true },
				{ ...REGISTRY_OPEN, key: 'problem:x', level: 'heads_up' as const },
				DEPLOY_RESOLVED,
				{ ...DEPLOY_RESOLVED, key: 'problem:y', level: 'needs_action' as const },
				RELEASE
			],
			unread: 3
		};
		assert.equal(needActionCount(list), 1);
		assert.equal(needActionCount(null), 0);
		assert.equal(bellNeedAction({ ...initialBellState(), list }), 1);
	});

	test('red dot (destructive) for an unread open needs_action problem, else the normal dot', () => {
		const html = (items: Notification[]) =>
			render(NotificationsBell, {
				props: { initial: { list: { items, unread: items.filter((i) => !i.read).length }, failed: false } }
			}).body;
		const red = html([HOST_OPEN, RELEASE]);
		assert.match(red, /aria-label="Notifications, 2 unread, 1 need action"/);
		assert.match(red, /data-bell-dot="needs-action"[^>]*class="[^"]*bg-destructive/);
		const normal = html([{ ...HOST_OPEN, level: 'heads_up' }, RELEASE]);
		assert.match(normal, /aria-label="Notifications, 2 unread"/);
		assert.match(normal, /data-bell-dot="unread"[^>]*class="[^"]*bg-primary/);
		assert.doesNotMatch(normal, /bg-destructive/);
		const read = html([{ ...HOST_OPEN, read: true }, DEPLOY_RESOLVED]);
		assert.equal(read.includes('data-bell-dot'), false);
	});
});

describe('bell polling', () => {
	/** A bell polling through `browserPoll` with a stand-in timer, document and popover state. */
	function harness() {
		let calls = 0;
		const timers = new Map<number, { fn: () => void; ms: number }>();
		let nextId = 1;
		const page = { visibilityState: 'visible' };
		let open = false;
		const bell = createBell({
			fetch: async () => {
				calls++;
				return new Response(JSON.stringify({ items: [], unread: 0 }), { status: 200 });
			},
			goto: async () => {},
			update: () => {}
		});
		const stop = bell.poll(
			browserPoll(() => open, {
				setInterval: (fn, ms) => {
					const id = nextId++;
					timers.set(id, { fn, ms });
					return id;
				},
				clearInterval: (id: never) => void timers.delete(id),
				document: page
			})
		);
		const tick = async () => {
			for (const t of timers.values()) t.fn();
			await new Promise((r) => setTimeout(r, 0));
		};
		return {
			bell,
			calls: () => calls,
			tick,
			timers,
			stop,
			hide: (hidden: boolean) => (page.visibilityState = hidden ? 'hidden' : 'visible'),
			setOpen: (next: boolean) => (open = next)
		};
	}

	test('every 2 minutes while visible; a hidden tab skips the tick; destroy stops it', async () => {
		const h = harness();
		assert.equal(BELL_POLL_MS, 120_000);
		assert.deepEqual([...h.timers.values()].map((t) => t.ms), [120_000]);
		assert.equal(h.calls(), 0, 'polling does not read at once');
		await h.tick();
		assert.equal(h.calls(), 1);
		h.hide(true);
		await h.tick();
		assert.equal(h.calls(), 1);
		h.hide(false);
		await h.tick();
		assert.equal(h.calls(), 2);
		h.stop();
		assert.equal(h.timers.size, 0, 'the stop clears the interval');
	});

	test('an open popover skips the tick; opening reads, closing reads nothing', async () => {
		const h = harness();
		h.setOpen(true);
		await h.bell.openChanged(true);
		assert.equal(h.calls(), 1, 'opening reads');
		await h.tick();
		assert.equal(h.calls(), 1, 'no refresh while open');
		h.setOpen(false);
		await h.bell.openChanged(false);
		assert.equal(h.calls(), 1, 'closing reads nothing');
		await h.tick();
		assert.equal(h.calls(), 2, 'closed again: the tick reads');
	});

	test('the component polls through browserPoll with its open state', () => {
		// onMount does not run in a server render; this one line is the wiring the tests above cover.
		const source = readFileSync(new URL('../src/lib/components/NotificationsBell.svelte', import.meta.url), 'utf8');
		assert.match(source, /controller\.poll\(browserPoll\(\(\) => open,/);
	});
});

describe('proxy: problem items (api-v1 v1.41 item 3)', () => {
	test('problem items need a level and a state; a resolved one is read', () => {
		assert.deepEqual(parseNotification(HOST_OPEN), HOST_OPEN);
		assert.deepEqual(parseNotification({ ...DEPLOY_RESOLVED, read: false }), DEPLOY_RESOLVED);
		assert.equal(parseNotification({ ...HOST_OPEN, level: 'urgent' }), null);
		assert.equal(parseNotification({ ...HOST_OPEN, level: undefined }), null);
		assert.equal(parseNotification({ ...HOST_OPEN, state: 'closed' }), null);
		assert.equal(parseNotification({ ...HOST_OPEN, href: 'https://evil.example/' }), null);
		assert.equal(parseNotification({ ...HOST_OPEN, kind: 'cert' }), null);
		// Other kinds keep their shape: no level or state is added.
		assert.deepEqual(parseNotification({ ...RELEASE, level: 'needs_action', state: 'open' }), RELEASE);
	});

	test('open problems are never cut: more than 20 items pass, unread recounted', () => {
		const open = Array.from({ length: 25 }, (_, i) => ({ ...HOST_OPEN, key: `problem:${i}` }));
		const list = parseNotificationList({ items: [...open, RELEASE, DEPLOY_RESOLVED], unread: 0 });
		assert.equal(list?.items.length, 27);
		assert.equal(list?.unread, 26);
	});
});
