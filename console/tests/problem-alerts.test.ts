import './stubs/sonner-document.ts';
import { toast } from 'svelte-sonner';
import './stubs/sonner-document-done.ts';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { describe, test } from 'node:test';
import { createRawSnippet } from 'svelte';
import { render } from 'svelte/server';
import type { Notification, NotificationList } from '../src/lib/api-types.ts';
import { DESKTOP_PREF_KEY, LIVE_CLAIMED_KEY, type LiveHub } from '../src/lib/desktop-notify.ts';
import { createBell, HIDDEN_BELL_MS } from '../src/lib/notifications-bell.ts';
import { needsAction } from '../src/lib/notifications.ts';
import {
	clearAlertKeys,
	consolePath,
	isOpenNeedsAction,
	createProblemAlerts,
	createProblemToaster,
	deliverProblems,
	openFromClick,
	newNeedsAction,
	problemSummaryText,
	problemToastPlan,
	PROBLEM_TOASTS_MAX,
	readAlerted,
	signOutCleanup,
	TOASTED_KEY
} from '../src/lib/problem-alerts.ts';
import RootLayout from '../src/routes/+layout.svelte';
import { setPageUrl, setRouteId } from './stubs/app-state.ts';

// svelte-sonner's own toast list (the state its Toaster renders), from the same module instance as `toast`. The
// package exports only `toast`, so the file is loaded by its real path.
const statePath = realpathSync(fileURLToPath(new URL('../node_modules/svelte-sonner/dist/toast-state.svelte.js', import.meta.url)));
const { toastState } = (await import(pathToFileURL(statePath).href)) as {
	toastState: { toasts: Array<Record<string, unknown> & { id: string | number }> };
};

// spec-console-live-updates §0 item 1 (§2.3 for problems), §5 items 6 and 10.

function memoryStorage() {
	const map = new Map<string, string>();
	return {
		map,
		getItem: (key: string) => map.get(key) ?? null,
		setItem: (key: string, value: string) => void map.set(key, value),
		removeItem: (key: string) => void map.delete(key)
	};
}

const NEEDS: Notification = {
	key: 'problem:00000000-0000-4000-8000-0000000000f1',
	kind: 'problem',
	title: 'pi2-2 is offline',
	detail: 'No report for 25 min.',
	href: '/hosts/00000000-0000-4000-8000-0000000000a1',
	at: '2026-11-22T10:00:00Z',
	read: false,
	level: 'needs_action',
	state: 'open'
};
const problem = (n: number, patch: Partial<Notification> = {}): Notification =>
	({ ...NEEDS, key: `problem:00000000-0000-4000-8000-00000000f${String(n).padStart(3, '0')}`, title: `p${n}`, ...patch }) as Notification;
const list = (...items: Notification[]): NotificationList => ({ items, unread: items.filter((item) => !item.read).length });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

/** A bell wired to the alerts exactly as `NotificationsBell.svelte` wires it, with stub toasts and no hub. */
function wiredBell(lists: Array<NotificationList | Response>, storage = memoryStorage()) {
	let reads = 0;
	const toasts: string[][] = [];
	const alerts = createProblemAlerts({
		hub: null,
		storage: () => storage,
		toaster: { show: (items) => toasts.push(items.map((item) => item.title)) },
		here: () => '/projects'
	});
	const bell = createBell({
		fetch: async () => {
			const next = lists[Math.min(reads++, lists.length - 1)] ?? list();
			return next instanceof Response ? next : json(next);
		},
		goto: async () => {},
		update: () => {},
		onLoaded: (previous, next, first) => void alerts.onLoaded(previous, next, first),
		onSignedOut: () => alerts.onSignedOut()
	});
	return { bell, alerts, storage, toasts, reads: () => reads };
}

describe('the needs-action diff (§5 item 6)', () => {
	test('only problems newly open at needs_action: not releases, agent updates, heads-ups or resolved problems', () => {
		const release = { key: 'release:0.2.44', kind: 'release', title: 'v0.2.44', detail: '', href: '/whats-new', at: NEEDS.at, read: false } as Notification;
		const update = { ...release, key: 'host_update:x', kind: 'host_update' } as Notification;
		const others = [release, update, problem(2, { level: 'heads_up' }), problem(3, { state: 'resolved', read: true })];
		assert.deepEqual(newNeedsAction(list(), list(...others, NEEDS)), [NEEDS]);
		assert.deepEqual(newNeedsAction(list(NEEDS), list(NEEDS)), []);
	});

	test('the level decides, not the read mark (read marks are kept per key)', () => {
		const read = problem(4, { read: true });
		assert.equal(isOpenNeedsAction(read), true);
		assert.equal(needsAction(read), false, 'the red dot and label still count only unread ones');
		assert.deepEqual(newNeedsAction(list(problem(4, { level: 'heads_up', read: true })), list(read)), [read]);
	});
});

describe('the bell with alerts (§0 item 1, §5 item 6)', () => {
	test('the first read after a load alerts nothing; a new problem alerts once; a repeat read alerts nothing', async () => {
		const h = wiredBell([list(problem(1)), list(problem(1), problem(2)), list(problem(1), problem(2))]);
		await h.bell.start();
		assert.deepEqual(h.toasts, []);
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p2']]);
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p2']]);
		assert.deepEqual([...readAlerted(h.storage)], [problem(2).key], 'the first read alerted nothing, so only p2 is kept');
	});

	test('a failed first read, then a read with a problem: still the first list, so no toast', async () => {
		const h = wiredBell([json({ message: 'down' }, 500), list(problem(2)), list(problem(2))]);
		await h.bell.start();
		await h.bell.refresh();
		assert.deepEqual(h.toasts, []);
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [], 'the problem was there on the first list');
	});

	test('after a reload, a problem already in the tab\u2019s alerted keys alerts nothing', async () => {
		const storage = memoryStorage();
		const before = wiredBell([list(), list(problem(2))], storage);
		await before.bell.start();
		await before.bell.refresh();
		assert.deepEqual(before.toasts, [['p2']]);
		// Reload: a new bell on the same tab storage. Its first list (first=true) and the next one alert nothing.
		const after = wiredBell([list(problem(2)), list(problem(2), problem(3, { level: 'heads_up' }))], storage);
		await after.bell.start();
		await after.bell.refresh();
		assert.deepEqual(after.toasts, []);
		assert.ok(readAlerted(storage).has(problem(2).key), 'still needs action, so still remembered');
	});

	test('a read heads_up problem that escalates to needs_action on the same row alerts once', async () => {
		const heads = problem(7, { level: 'heads_up', read: true });
		const escalated = problem(7, { level: 'needs_action', read: true });
		const h = wiredBell([list(heads), list(escalated), list(escalated)]);
		await h.bell.start();
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p7']]);
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p7']], 'once');
	});

	test('a read needs_action problem in both reads does not alert', async () => {
		const read = problem(8, { read: true });
		const h = wiredBell([list(), list(read), list(read)]);
		await h.bell.start();
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p8']], 'new on the second read');
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p8']], 'present in both reads: nothing more');
		const both = wiredBell([list(read), list(read)]);
		await both.bell.start();
		await both.bell.refresh();
		assert.deepEqual(both.toasts, []);
	});

	test('resolved, then reopened on the same row and still read: alerts again', async () => {
		const open = problem(9, { read: true });
		const resolved = problem(9, { state: 'resolved', read: true });
		const h = wiredBell([list(), list(open), list(resolved), list(open)]);
		await h.bell.start();
		await h.bell.refresh();
		await h.bell.refresh();
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p9'], ['p9']]);
	});

	test('a key that no longer needs action is forgotten, so a recurrence alerts again', async () => {
		const resolved = problem(2, { state: 'resolved', read: true });
		const h = wiredBell([list(), list(problem(2)), list(resolved), list(problem(2))]);
		await h.bell.start();
		await h.bell.refresh();
		await h.bell.refresh();
		assert.equal(readAlerted(h.storage).has(problem(2).key), false, 'forgotten once resolved');
		await h.bell.refresh();
		assert.deepEqual(h.toasts, [['p2'], ['p2']], 'the reopened problem (same key) alerts again');
	});

	test('a 401 stops the bell, clears this tab’s alert keys and the desktop choice; destroy stops the hub', async () => {
		const storage = memoryStorage();
		storage.setItem(TOASTED_KEY, JSON.stringify([NEEDS.key]));
		storage.setItem(LIVE_CLAIMED_KEY, JSON.stringify([NEEDS.key]));
		let reads = 0;
		const prefs = memoryStorage();
		prefs.setItem(DESKTOP_PREF_KEY, 'on');
		let disposed = 0;
		const alerts = createProblemAlerts({
			hub: null,
			storage: () => storage,
			prefs: () => prefs,
			disposeHub: () => void disposed++,
			toaster: { show: () => {} },
			here: () => '/'
		});
		const bell = createBell({
			fetch: async () => (reads++, json({ message: 'Sign in required.' }, 401)),
			goto: async () => {},
			update: () => {},
			onSignedOut: () => alerts.onSignedOut()
		});
		await bell.refresh();
		await bell.refresh();
		assert.equal(reads, 1);
		assert.equal(bell.stopped, true);
		assert.equal(storage.map.size, 0);
		assert.equal(prefs.map.size, 0, 'a session expiry turns desktop notifications off too');
		alerts.dispose();
		assert.equal(disposed, 1, 'the bell\u2019s destroy stops the hub');
	});

	test('hidden: the 60 s read only when the hub allows it; never without a hub', async () => {
		assert.equal(HIDDEN_BELL_MS, 60_000);
		let allowed = false;
		let reads = 0;
		const timers: Array<{ fn: () => void; ms: number }> = [];
		const bell = createBell({ fetch: async () => (reads++, json(list())), goto: async () => {}, update: () => {} });
		bell.poll({ every: (fn, ms) => (timers.push({ fn, ms }), () => {}), visible: () => false, open: () => false, hiddenPoll: () => allowed });
		assert.deepEqual(
			timers.map((t) => t.ms),
			[120_000, 60_000]
		);
		for (const t of timers) t.fn();
		assert.equal(reads, 0, 'off: a hidden tab makes no request');
		allowed = true;
		for (const t of timers) t.fn();
		await new Promise((r) => setTimeout(r, 0));
		assert.equal(reads, 1);
		assert.equal(createProblemAlerts({ hub: null, storage: () => null, toaster: { show: () => {} }, here: () => '/' }).hiddenPoll(), false);
	});
});

describe('problem toasts on the real svelte-sonner state (§0 item 1)', () => {
	test('each: the title, error style, assertive, dismissible, stays until dismissed, View and Dismiss', () => {
		const went: string[][] = [];
		const toaster = createProblemToaster(toast, { openProblem: (key, href) => went.push([key, href]), openBell: () => {} });
		toaster.show([NEEDS]);
		const shown = toastState.toasts.find((item) => item.id === `bell-${NEEDS.key}`);
		assert.ok(shown);
		assert.equal(shown.type, 'error');
		assert.equal(shown.title, NEEDS.title);
		assert.equal(shown.duration, Infinity);
		assert.equal(shown.important, true);
		// svelte-sonner's Toast ignores its close, swipe and cancel when dismissible is false; ours never sets it.
		assert.notEqual(shown.dismissible, false);
		assert.notEqual(shown.dismissable, false);
		const view = shown.action as { label: string; onClick: (event: MouseEvent) => void };
		const dismiss = shown.cancel as { label: string };
		assert.deepEqual([view.label, dismiss.label], ['View', 'Dismiss']);
		assert.equal(toaster.showing, 1);
		view.onClick(new Event('click') as MouseEvent);
		assert.deepEqual(went, [[NEEDS.key, NEEDS.href]]);
		assert.equal(toaster.showing, 0, 'View frees the slot');
		// The close button or a swipe call onDismiss.
		toaster.show([problem(9)]);
		const swiped = toastState.toasts.find((item) => item.id === `bell-${problem(9).key}`)!;
		(swiped.onDismiss as (t: unknown) => void)(swiped);
		assert.equal(toaster.showing, 0);
		toast.dismiss();
	});

	test('View and Dismiss free their slot: after three clicks a new problem still toasts on its own', () => {
		const went: string[][] = [];
		const toaster = createProblemToaster(toast, { openProblem: (key, href) => went.push([key, href]), openBell: () => {} });
		toaster.show([problem(21), problem(22), problem(23)]);
		assert.equal(toaster.showing, 3);
		const byId = (n: number) => toastState.toasts.find((item) => item.id === `bell-${problem(n).key}`)!;
		// The onClick handlers svelte-sonner calls before it removes the toast (it does not call onDismiss then).
		(byId(21).action as { onClick: (event: MouseEvent) => void }).onClick(new Event('click') as MouseEvent);
		(byId(22).cancel as { onClick: (event: MouseEvent) => void }).onClick(new Event('click') as MouseEvent);
		(byId(23).cancel as { onClick: (event: MouseEvent) => void }).onClick(new Event('click') as MouseEvent);
		assert.equal(toaster.showing, 0);
		assert.deepEqual(went, [[problem(21).key, problem(21).href]], 'View opens that problem; Dismiss opens nothing');
		toaster.show([problem(24)]);
		assert.ok(byId(24), 'its own toast, not a summary');
		assert.equal(toastState.toasts.filter((item) => String(item.id).startsWith('bell-problems-summary-')).length, 0);
		assert.equal(toaster.showing, 1);
		toast.dismiss();
	});

	test('more than 3 at once: one summary counting the toasts already showing, a new key per read', () => {
		assert.deepEqual(problemToastPlan([problem(1), problem(2)], 1), { kind: 'each', items: [problem(1), problem(2)] });
		assert.deepEqual(problemToastPlan([problem(4)], 3), { kind: 'summary', count: 4 });
		assert.equal(problemSummaryText(1), '1 new problem needs action');
		let opened = 0;
		const toaster = createProblemToaster(toast, { openProblem: () => assert.fail('a summary marks nothing'), openBell: () => opened++ });
		toaster.show([problem(1), problem(2)]);
		toaster.show([problem(3), problem(4)]);
		toaster.show([problem(5), problem(6)]);
		const summaries = toastState.toasts.filter((item) => String(item.id).startsWith('bell-problems-summary-'));
		assert.equal(summaries.length, 2, 'a later summary does not replace the earlier one');
		assert.equal(new Set(summaries.map((item) => item.id)).size, 2);
		assert.deepEqual(
			summaries.map((item) => item.title),
			['4 new problems need action', '4 new problems need action'],
			'2 showing + 2 new'
		);
		(summaries[0]!.action as { onClick: () => void }).onClick();
		assert.equal(opened, 1);
		assert.equal(PROBLEM_TOASTS_MAX, 3);
		toast.dismiss();
	});
});

describe('delivery with every tab hidden (§2.6 for problems)', () => {
	function hubStub(notifying: boolean) {
		const seen: string[] = [];
		const notified: Array<{ key: string; text: string; href: string }> = [];
		return {
			seen,
			notified,
			hub: {
				notifying: () => notifying,
				markSeen: (key: string) => void seen.push(key),
				notifyOnce: async (event: { key: string; text: string; href: string }) => (notified.push(event), true)
			}
		};
	}

	test('notifications instead of toasts; one hashed summary for more than 3; the keys go to the hidden tabs', async () => {
		let toasts = 0;
		const toaster = { show: () => void toasts++ };
		const h = hubStub(true);
		await deliverProblems(h.hub, [problem(1), problem(2)], { toaster, here: '/projects' });
		assert.deepEqual(
			h.notified.map((event) => [event.text, event.href]),
			[
				['p1', NEEDS.href],
				['p2', NEEDS.href]
			]
		);
		await deliverProblems(h.hub, [1, 2, 3, 4].map((n) => problem(n + 10)), { toaster, here: '/projects' });
		const summary = h.notified.at(-1)!;
		assert.equal(summary.text, '4 new problems need action');
		assert.match(summary.key, /^problems:[0-9a-f]{8}$/);
		assert.equal(summary.href, '/projects');
		assert.equal(toasts, 0, 'no toast queued for later');
		assert.equal(h.seen.length, 6);
	});

	test('a summary notification opens only a console path; `//host` falls back to /projects', async () => {
		assert.equal(consolePath('/hosts/x?tab=overview'), '/hosts/x?tab=overview');
		assert.equal(consolePath('//evil.example/x'), '/projects');
		assert.equal(consolePath('https://evil.example'), '/projects');
		assert.equal(consolePath('/a\\b'), '/projects');
		const h = hubStub(true);
		await deliverProblems(h.hub, [1, 2, 3, 4].map((n) => problem(n + 30)), { toaster: { show: () => {} }, here: '//evil.example' });
		assert.equal(h.notified.at(-1)?.href, '/projects');
	});

	test('keys another tab alerted while this one is hidden go into this tab\u2019s alerted keys', () => {
		const storage = memoryStorage();
		let listener: ((key: string) => void) | null = null;
		const hub = { onSeen: (fn: (key: string) => void) => ((listener = fn), () => (listener = null)) } as unknown as LiveHub;
		const alerts = createProblemAlerts({ hub, storage: () => storage, toaster: { show: () => {} }, here: () => '/' });
		const stop = alerts.watchSeen();
		listener!(NEEDS.key);
		assert.ok(readAlerted(storage).has(NEEDS.key));
		stop();
		assert.equal(listener, null);
	});

	test('a visible tab toasts as before', async () => {
		let toasts = 0;
		const h = hubStub(false);
		await deliverProblems(h.hub, [problem(1)], { toaster: { show: () => void toasts++ }, here: '/' });
		assert.equal(toasts, 1);
		assert.equal(h.notified.length, 0);
	});
});

describe('sign-out (§0 item 2)', () => {
	test('clears this tab’s alert keys and the desktop notifications choice', () => {
		const tab = memoryStorage();
		const prefs = memoryStorage();
		tab.setItem(TOASTED_KEY, '[]');
		tab.setItem(LIVE_CLAIMED_KEY, '[]');
		tab.setItem('deploybot:catalog-open:x', '[]');
		prefs.setItem(DESKTOP_PREF_KEY, 'on');
		signOutCleanup(tab, prefs);
		assert.deepEqual([...tab.map.keys()], ['deploybot:catalog-open:x']);
		assert.equal(prefs.map.size, 0);
		signOutCleanup(null, null);
		clearAlertKeys({
			removeItem: () => {
				throw new Error('blocked');
			}
		});
	});

	test('the signed-in shell renders with the bell and the account menu', () => {
		setPageUrl('http://console.example/projects');
		setRouteId('/projects');
		const children = createRawSnippet(() => ({ render: () => '<p>child</p>' }));
		const data = {
			user: { id: 'u', email: 'ada@example.com', name: 'Ada', avatarUrl: null },
			orgs: [],
			projects: [],
			theme: 'system' as const,
			timeZone: 'UTC',
			whatsNew: null
		};
		const body = render(RootLayout, { props: { children, data } }).body;
		assert.ok(body.includes('aria-label="Notifications"'));
		assert.ok(body.includes('action="/signout"'));
	});
});

describe('View and a notification click mark that problem read, like a bell row (§0)', () => {
	type Call = { method: string; body: string | null };

	function bellWithCalls(markStatus = 204) {
		const calls: Call[] = [];
		const went: string[] = [];
		const bell = createBell({
			fetch: async (_input, init) => {
				calls.push({ method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : null });
				if (init?.method === 'POST') return new Response(null, { status: markStatus });
				return json(list(NEEDS));
			},
			goto: async (href) => void went.push(`goto ${href}`),
			update: () => {}
		});
		return { bell, calls, went };
	}

	test('View marks exactly that key read, then navigates, then refreshes the bell', async () => {
		const h = bellWithCalls();
		const toaster = createProblemToaster(toast, { openProblem: (key, href) => h.bell.openKey(key, href), openBell: () => {} });
		toaster.show([NEEDS, problem(41)]);
		const view = toastState.toasts.find((item) => item.id === `bell-${NEEDS.key}`)!.action as { onClick: (event: MouseEvent) => void };
		view.onClick(new Event('click') as MouseEvent);
		await new Promise((r) => setTimeout(r, 0));
		assert.deepEqual(h.calls[0], { method: 'POST', body: JSON.stringify({ keys: [NEEDS.key] }) });
		assert.deepEqual(h.went, [`goto ${NEEDS.href}`]);
		assert.equal(h.calls[1]?.method, 'GET', 'the bell reads again after navigating');
		assert.equal(h.calls.length, 2);
		toast.dismiss();
	});

	test('Dismiss marks nothing', () => {
		const h = bellWithCalls();
		const toaster = createProblemToaster(toast, { openProblem: (key, href) => h.bell.openKey(key, href), openBell: () => {} });
		toaster.show([problem(42)]);
		(toastState.toasts.find((item) => item.id === `bell-${problem(42).key}`)!.cancel as { onClick: (e: MouseEvent) => void }).onClick(
			new Event('click') as MouseEvent
		);
		assert.equal(h.calls.length, 0);
		toast.dismiss();
	});

	test('a failed mark still navigates and shows the bell\u2019s mark error, like a row click', async () => {
		const h = bellWithCalls(503);
		await h.bell.openKey(NEEDS.key, NEEDS.href);
		assert.deepEqual(h.went, [`goto ${NEEDS.href}`]);
		assert.equal(h.bell.state.markFailed, true);
	});

	test('a notification click marks its problem read; a summary notification only navigates', () => {
		const opened: string[][] = [];
		const actions = { openProblem: (key: string, href: string) => void opened.push(['open', key, href]), goto: (href: string) => void opened.push(['goto', href]) };
		openFromClick(actions, NEEDS.href, NEEDS.key);
		openFromClick(actions, '/projects');
		assert.deepEqual(opened, [
			['open', NEEDS.key, NEEDS.href],
			['goto', '/projects']
		]);
	});

	test('problem notifications carry their key to mark; the summary carries none', async () => {
		const notified: Array<{ key: string; readKey?: string }> = [];
		const hub = { notifying: () => true, markSeen: () => {}, notifyOnce: async (event: { key: string; readKey?: string }) => (notified.push(event), true) };
		await deliverProblems(hub, [problem(43)], { toaster: { show: () => {} }, here: '/projects' });
		await deliverProblems(hub, [1, 2, 3, 4].map((n) => problem(n + 50)), { toaster: { show: () => {} }, here: '/projects' });
		assert.equal(notified[0]?.readKey, problem(43).key);
		assert.equal(notified[1]?.readKey, undefined);
	});
});
