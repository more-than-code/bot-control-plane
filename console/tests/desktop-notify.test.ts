import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import {
	BLOCKED_TEXT,
	browserLiveHub,
	connectHub,
	createLiveHub,
	DESKTOP_PREF_KEY,
	HEARTBEAT_MS,
	LIVE_CLAIMED_KEY,
	NOTIFY_TITLE,
	readClaimed,
	readPref,
	switchView,
	TAB_EXPIRY_MS,
	toggleDesktop,
	type ChannelLike,
	type NotificationApi,
	type Permission
} from '../src/lib/desktop-notify.ts';
import { MAX_KEY, shortHash, tabId } from '../src/lib/live-common.ts';

// spec-console-live-updates §0 item 2 and §2.6 (problems only); §5 item 10.

function memoryStorage() {
	const map = new Map<string, string>();
	return {
		map,
		getItem: (key: string) => map.get(key) ?? null,
		setItem: (key: string, value: string) => void map.set(key, value),
		removeItem: (key: string) => void map.delete(key)
	};
}

const settle = (ms = 0) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** BroadcastChannel stand-in: a post reaches every other channel on the bus asynchronously, like the real one. */
function asyncBus() {
	const channels: ChannelLike[] = [];
	return {
		channel(): ChannelLike {
			const channel: ChannelLike = {
				onmessage: null,
				postMessage: (message) => {
					const copy = structuredClone(message);
					for (const other of channels) if (other !== channel) setTimeout(() => other.onmessage?.({ data: copy }), 0);
				},
				close: () => {}
			};
			channels.push(channel);
			return channel;
		}
	};
}

type Shown = { title: string; body: string; tag: string; closed: boolean; onclick: ((event: Event) => unknown) | null; close: () => void };

function notificationStub(permission: Permission = 'granted') {
	const shown: Shown[] = [];
	let asked = 0;
	let answer: () => Promise<Permission> = async () => 'granted';
	const api: NotificationApi & { permission: Permission } = {
		permission,
		requestPermission: async () => {
			asked++;
			const next = await answer();
			api.permission = next;
			return next;
		},
		create: (title, options) => {
			const note: Shown = { title, body: options.body, tag: options.tag, closed: false, onclick: null, close: () => (note.closed = true) };
			shown.push(note);
			return note;
		}
	};
	return { api, shown, asked: () => asked, answerWith: (next: () => Promise<Permission>) => (answer = next) };
}

function tabs(count: number, opts: { pref?: boolean; ids?: string[] } = {}) {
	const bus = asyncBus();
	const prefs = memoryStorage();
	if (opts.pref !== false) prefs.setItem(DESKTOP_PREF_KEY, 'on');
	const notes = notificationStub();
	const clock = { now: 1_000 };
	const list = Array.from({ length: count }, (_, i) => {
		const state = { visible: true };
		const opened: string[] = [];
		const storage = memoryStorage();
		const hub = createLiveHub({
			tabId: opts.ids?.[i] ?? `tab-${i}`,
			channel: bus.channel(),
			storage: () => storage,
			prefs: () => prefs,
			notifications: notes.api,
			now: () => clock.now,
			visible: () => state.visible,
			open: (href, readKey) => opened.push(`${href} read=${readKey}`),
			wait: () => settle(5)
		});
		return { hub, state, opened, storage };
	});
	return {
		list,
		notes,
		prefs,
		clock,
		async hello() {
			for (const tab of list) tab.hub.hello();
			await settle(5);
		},
		async hide(i: number) {
			clock.now += 1;
			list[i]!.state.visible = false;
			list[i]!.hub.visibilityChanged();
			await settle(5);
		},
		async show(i: number) {
			clock.now += 1;
			list[i]!.state.visible = true;
			list[i]!.hub.visibilityChanged();
			await settle(5);
		}
	};
}

const EVENT = {
	key: 'problem:00000000-0000-4000-8000-0000000000f1',
	text: 'pi2-2 is offline',
	href: '/hosts/00000000-0000-4000-8000-0000000000a1',
	readKey: 'problem:00000000-0000-4000-8000-0000000000f1'
};

describe('the switch (§5 item 10)', () => {
	test('off by default; hidden without browser support; reading the view never asks', () => {
		const prefs = memoryStorage();
		const notes = notificationStub('default');
		assert.equal(readPref(prefs), false);
		assert.deepEqual(switchView(null, true), { shown: false, checked: false, blocked: false });
		assert.deepEqual(switchView(notes.api, readPref(prefs)), { shown: true, checked: false, blocked: false });
		assert.equal(notes.asked(), 0);
	});

	test('turning on asks from the click and stores on only when granted; off never asks', async () => {
		const prefs = memoryStorage();
		const notes = notificationStub('default');
		assert.deepEqual(await toggleDesktop(notes.api, prefs, true), { shown: true, checked: true, blocked: false });
		assert.equal(prefs.map.get(DESKTOP_PREF_KEY), 'on');
		assert.equal((await toggleDesktop(notes.api, prefs, false)).checked, false);
		assert.equal(readPref(prefs), false);
		assert.equal(notes.asked(), 1);
	});

	test('a dismissed prompt (still default) shows off, and asks again next time', async () => {
		const prefs = memoryStorage();
		const notes = notificationStub('default');
		notes.answerWith(async () => 'default');
		assert.deepEqual(await toggleDesktop(notes.api, prefs, true), { shown: true, checked: false, blocked: false });
		assert.equal(readPref(prefs), false);
		await toggleDesktop(notes.api, prefs, true);
		assert.equal(notes.asked(), 2);
	});

	test('a requestPermission that throws shows off', async () => {
		const prefs = memoryStorage();
		const notes = notificationStub('default');
		notes.answerWith(async () => {
			throw new Error('not allowed');
		});
		assert.deepEqual(await toggleDesktop(notes.api, prefs, true), { shown: true, checked: false, blocked: false });
		assert.equal(readPref(prefs), false);
	});

	test('denied: the blocked line, off', async () => {
		const notes = notificationStub('default');
		notes.answerWith(async () => 'denied');
		assert.deepEqual(await toggleDesktop(notes.api, memoryStorage(), true), { shown: true, checked: false, blocked: true });
		assert.equal(BLOCKED_TEXT, "Blocked in this browser's settings");
	});

	test('storage that fails keeps it off', async () => {
		const broken = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		};
		assert.equal(readPref(broken), false);
		assert.equal((await toggleDesktop(notificationStub('granted').api, broken, true)).checked, false);
	});
});

describe('the hub over an async bus (§2.6, §5 item 10)', () => {
	test('the tab hidden last is elected to read the bell; none while any tab is visible; none with it off', async () => {
		const t = tabs(3);
		await t.hello();
		await t.hide(0);
		await t.hide(1);
		assert.equal(t.list[0]!.hub.hiddenPoll(), false, 'tab 2 is visible');
		await t.hide(2);
		assert.deepEqual(
			t.list.map((tab) => tab.hub.hiddenPoll()),
			[false, false, true]
		);
		await t.show(2);
		await t.hide(2);
		await t.hide(0);
		assert.deepEqual(
			t.list.map((tab) => tab.hub.hiddenPoll()),
			[true, false, false]
		);
		const off = tabs(2, { pref: false });
		await off.hello();
		await off.hide(0);
		await off.hide(1);
		assert.deepEqual(
			off.list.map((tab) => tab.hub.hiddenPoll()),
			[false, false]
		);
	});

	test('a tab silent for 3 minutes is forgotten: it no longer blocks hidden reads or stays leader', async () => {
		const t = tabs(2);
		await t.hello();
		await t.hide(0);
		// Tab 1 stays visible, then goes silent (crashed): no heartbeat.
		assert.equal(t.list[0]!.hub.hiddenPoll(), false);
		t.clock.now += TAB_EXPIRY_MS + 1;
		assert.equal(t.list[0]!.hub.hiddenPoll(), true);
		assert.equal(HEARTBEAT_MS, 60_000);
	});

	test('a heartbeat keeps a tab known past 3 minutes', async () => {
		const t = tabs(2);
		await t.hello();
		await t.hide(0);
		t.clock.now += TAB_EXPIRY_MS - 1_000;
		t.list[1]!.hub.heartbeat();
		await settle(5);
		t.clock.now += 2_000;
		assert.equal(t.list[0]!.hub.hiddenPoll(), false, 'tab 1 is still visible');
	});

	test('a page restored from the back-forward cache says hello again', async () => {
		const t = tabs(2);
		const doc = new EventTarget();
		const win = new EventTarget();
		const beats: Array<() => void> = [];
		const stop = connectHub(t.list[0]!.hub, { document: doc, window: win, every: (fn) => (beats.push(fn), () => {}) });
		await settle(5);
		// Tab 1 learned tab 0 is visible; tab 0 then goes hidden and tab 1 forgets it (expiry).
		await t.hide(0);
		t.list[1]!.state.visible = false;
		t.clock.now += TAB_EXPIRY_MS + 1;
		assert.equal(t.list[1]!.hub.leader(), 'tab-1');
		const restored = new Event('pageshow') as Event & { persisted?: boolean };
		restored.persisted = true;
		win.dispatchEvent(restored);
		await settle(5);
		assert.equal(t.list[1]!.hub.leader(), 'tab-0', 'tab 0 is known again (hidden last)');
		assert.equal(beats.length, 1, 'one 60 s heartbeat');
		stop();
	});

	test('every tab hidden: three tabs give one notification; a click focuses and opens the page', async () => {
		const t = tabs(3);
		await t.hello();
		await t.hide(0);
		await t.hide(1);
		await t.hide(2);
		const results = await Promise.all(t.list.map((tab) => tab.hub.notifyOnce(EVENT)));
		assert.equal(results.filter(Boolean).length, 1);
		assert.equal(t.notes.shown.length, 1);
		const note = t.notes.shown[0]!;
		assert.deepEqual([note.title, note.body, note.tag], [NOTIFY_TITLE, EVENT.text, EVENT.key]);
		note.onclick?.(new Event('click'));
		const opener = t.list.find((tab) => tab.opened.length > 0)!;
		assert.deepEqual(opener.opened, [`${EVENT.href} read=${EVENT.key}`], 'the clicked tab opens the page and marks the key read');
		assert.equal(note.closed, true);
	});

	test('claim race: two tabs claiming at once, the lower tab id wins', async () => {
		const t = tabs(2, { ids: ['b-tab', 'a-tab'] });
		const [first, second] = await Promise.all([t.list[0]!.hub.claim('k'), t.list[1]!.hub.claim('k')]);
		assert.deepEqual([first, second], [false, true]);
		assert.equal(await t.list[1]!.hub.claim('k'), false, 'claimed already');
	});

	test('a released key may alert again, in every tab', async () => {
		const t = tabs(2);
		assert.equal(await t.list[0]!.hub.claim(EVENT.key), true);
		await settle(5);
		assert.equal(await t.list[1]!.hub.claim(EVENT.key), false);
		t.list[0]!.hub.release(EVENT.key);
		await settle(5);
		assert.equal(await t.list[1]!.hub.claim(EVENT.key), true);
	});

	test('hidden tabs learn seen keys; a visible tab does not', async () => {
		const t = tabs(3);
		const seen: string[][] = [[], [], []];
		t.list.forEach((tab, i) => tab.hub.onSeen((key) => seen[i]!.push(key)));
		await t.hide(1);
		await t.hide(2);
		t.list[2]!.hub.markSeen(EVENT.key);
		await settle(5);
		assert.deepEqual(seen, [[], [EVENT.key], []]);
	});

	test('keys over 200 characters are dropped on receipt and on read', async () => {
		const t = tabs(2);
		const long = 'k'.repeat(MAX_KEY + 1);
		t.list[0]!.hub.receive({ type: 'claim', tab: 'x', key: long });
		t.list[0]!.hub.receive({ type: 'claim', tab: 'x', key: 'ok' });
		assert.deepEqual([...readClaimed(t.list[0]!.storage)], ['ok']);
		const store = memoryStorage();
		store.setItem(LIVE_CLAIMED_KEY, JSON.stringify([long, 'fine', 7]));
		assert.deepEqual([...readClaimed(store)], ['fine']);
		assert.equal(await t.list[0]!.hub.claim(long), false);
		assert.equal(shortHash('a,b').length, 8);
	});

	test('a 401 stops hidden reads and notifications', async () => {
		const t = tabs(1);
		await t.hide(0);
		assert.equal(t.list[0]!.hub.hiddenPoll(), true);
		t.list[0]!.hub.stop();
		assert.equal(t.list[0]!.hub.hiddenPoll(), false);
		assert.equal(t.list[0]!.hub.notifying(), false);
	});
});

describe('page wiring and bounds', () => {
	test('connectHub\u2019s disposer removes the listeners and stops the heartbeat', async () => {
		const t = tabs(2);
		const doc = new EventTarget();
		const win = new EventTarget();
		let beats = 0;
		let stopped = 0;
		let beat = () => {};
		const stop = connectHub(t.list[0]!.hub, {
			document: doc,
			window: win,
			every: (fn) => {
				beat = () => {
					beats++;
					fn();
				};
				return () => void stopped++;
			}
		});
		beat();
		assert.equal(beats, 1);
		stop();
		assert.equal(stopped, 1, 'the heartbeat stops');
		// Hidden now, but the hub no longer hears visibility changes or page hide.
		await settle(5);
		t.list[0]!.state.visible = false;
		doc.dispatchEvent(new Event('visibilitychange'));
		win.dispatchEvent(new Event('pagehide'));
		await settle(5);
		t.list[1]!.state.visible = false;
		assert.equal(t.list[1]!.hub.anyVisible(), true, 'tab 1 still thinks tab 0 is visible: its listeners are gone');
	});

	test('the in-memory claims are bounded like the stored ones (300)', async () => {
		const t = tabs(1);
		for (let i = 0; i < 305; i++) t.list[0]!.hub.receive({ type: 'claim', tab: 'other', key: `k${i}` });
		const stored = readClaimed(t.list[0]!.storage);
		assert.equal(stored.size, 300);
		assert.equal(stored.has('k0'), false);
		assert.equal(await t.list[0]!.hub.claim('k0'), true, 'the oldest claim was dropped from memory too');
	});
});

describe('robustness', () => {
	test('a tab id without crypto.randomUUID (an insecure origin)', () => {
		assert.match(tabId({ getRandomValues: (array) => array }), /^[0-9a-f]{32}$/);
		assert.ok(tabId({}).length > 4);
		assert.ok(
			tabId({
				randomUUID: () => {
					throw new Error('insecure');
				}
			}).length > 4
		);
	});

	test('no hub on the server, and never a throw', () => {
		assert.equal(browserLiveHub({ storage: () => null, open: () => {}, every: () => () => {} }), null);
	});
});
