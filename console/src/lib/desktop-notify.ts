/**
 * Desktop notifications for problems while every console tab is hidden (spec-console-live-updates §0 item 2, §2.6).
 *
 * - Opt-in: the account menu's "Desktop notifications" switch asks the browser's permission only from that click.
 *   The choice is kept per browser in localStorage ("on" or "off", never a token); off when storage fails, and
 *   cleared at sign-out.
 * - Tabs coordinate over `BroadcastChannel('deploybot-live')` (`createLiveHub`): each tab says whether it is visible,
 *   every 60 s and on each change; a tab silent for 3 minutes is forgotten (a crashed tab cannot block anything); the
 *   hidden tab that became hidden last is elected to read the bell; each alert key is claimed once, with a tie-break
 *   to the lower tab id; keys seen in one tab are passed to the hidden others so a return replays nothing.
 * - `createLiveHub` is pure and tested with stubs for Notification, BroadcastChannel and visibility; `browserLiveHub`
 *   wires the browser to it once per page and never throws.
 */
import { isKey, tabId, type Every, type KeyStore } from './live-common';

export const DESKTOP_PREF_KEY = 'deploybot:desktop-notifications';
/** Alert keys claimed for one desktop notification across tabs, in the tab's storage. */
export const LIVE_CLAIMED_KEY = 'deploybot:live-claimed';
export const LIVE_CHANNEL = 'deploybot-live';
export const NOTIFY_TITLE = 'Deploybot';
export const DESKTOP_LABEL = 'Desktop notifications';
export const BLOCKED_TEXT = "Blocked in this browser's settings";
/** Each tab re-announces its state this often. */
export const HEARTBEAT_MS = 60_000;
/** A tab not heard from for this long is forgotten. */
export const TAB_EXPIRY_MS = 3 * 60_000;
const CLAIMED_MAX = 300;
const TAB_ID_MAX = 100;

export type Permission = 'default' | 'granted' | 'denied';

export type NotificationLike = { onclick: ((event: Event) => unknown) | null; close: () => void };

/** What is used of the browser's `Notification`. */
export type NotificationApi = {
	readonly permission: Permission;
	requestPermission: () => Promise<Permission>;
	create: (title: string, options: { body: string; tag: string }) => NotificationLike;
};

export type ChannelLike = {
	postMessage: (message: unknown) => void;
	onmessage: ((event: { data: unknown }) => void) | null;
	close: () => void;
};

/** The stored choice; off when missing or unreadable. */
export function readPref(store: KeyStore | null | undefined): boolean {
	try {
		return store?.getItem(DESKTOP_PREF_KEY) === 'on';
	} catch {
		return false;
	}
}

export function writePref(store: KeyStore | null | undefined, on: boolean): boolean {
	try {
		if (!store) return false;
		store.setItem(DESKTOP_PREF_KEY, on ? 'on' : 'off');
		return true;
	} catch {
		return false;
	}
}

/** Sign-out: the next person on this browser starts with desktop notifications off. */
export function clearPref(store: Pick<Storage, 'removeItem'> | null | undefined): void {
	try {
		store?.removeItem(DESKTOP_PREF_KEY);
	} catch {
		// Storage refused: nothing more to do.
	}
}

/** How the account-menu switch looks: hidden without browser support; "blocked" once the browser denied. */
export type SwitchView = { shown: boolean; checked: boolean; blocked: boolean };

export function switchView(api: Pick<NotificationApi, 'permission'> | null, pref: boolean): SwitchView {
	if (!api) return { shown: false, checked: false, blocked: false };
	const blocked = api.permission === 'denied';
	return { shown: true, checked: !blocked && pref && api.permission === 'granted', blocked };
}

/**
 * The switch was clicked: turning on asks permission (the only caller of `requestPermission`) and stays off unless
 * granted and stored; a dismissed prompt (still `default`) or a throwing request stays off. Turning off stores off.
 */
export async function toggleDesktop(api: NotificationApi | null, store: KeyStore | null, on: boolean): Promise<SwitchView> {
	if (!api) return switchView(null, false);
	if (!on) {
		writePref(store, false);
		return switchView(api, false);
	}
	let permission: Permission = api.permission;
	if (permission === 'default') {
		try {
			permission = await api.requestPermission();
		} catch {
			permission = 'default';
		}
	}
	const granted = permission === 'granted';
	const stored = granted && writePref(store, true);
	if (!granted) writePref(store, false);
	return switchView({ permission }, stored);
}

/** One alert: its key, the toast's text and its page. */
/** One alert: its key, the toast's text, its page, and the bell key a click marks read (none for a summary). */
export type LiveEvent = { key: string; text: string; href: string; readKey?: string };

export type LiveHubEnv = {
	tabId: string;
	channel: ChannelLike | null;
	/** This tab's storage (`visitStorage`) for the claimed keys. */
	storage: () => KeyStore | null;
	/** The per-browser store of the switch. */
	prefs: () => KeyStore | null;
	notifications: NotificationApi | null;
	now: () => number;
	visible: () => boolean;
	/** Focuses this tab and opens a console page (a notification click), marking `readKey` read when given. */
	open: (href: string, readKey?: string) => void;
	/** The claim tie-break's wait (default `CLAIM_WAIT_MS`). */
	wait?: () => Promise<void>;
};

type TabState = { visible: boolean; hiddenAt: number; heardAt: number };

type Message =
	| { type: 'state'; tab: string; visible: boolean; hiddenAt: number }
	| { type: 'hello'; tab: string }
	| { type: 'bye'; tab: string }
	| { type: 'claim'; tab: string; key: string }
	| { type: 'release'; key: string }
	| { type: 'seen'; key: string };

function isTab(value: unknown): value is string {
	return typeof value === 'string' && value !== '' && value.length <= TAB_ID_MAX;
}

/** A message from another tab, checked field by field; anything else is dropped. */
function readMessage(value: unknown): Message | null {
	if (value == null || typeof value !== 'object') return null;
	const m = value as Record<string, unknown>;
	switch (m.type) {
		case 'state':
			return isTab(m.tab) && typeof m.visible === 'boolean' && typeof m.hiddenAt === 'number' && Number.isFinite(m.hiddenAt)
				? { type: 'state', tab: m.tab, visible: m.visible, hiddenAt: m.hiddenAt }
				: null;
		case 'hello':
		case 'bye':
			return isTab(m.tab) ? { type: m.type, tab: m.tab } : null;
		case 'claim':
			return isTab(m.tab) && isKey(m.key) ? { type: 'claim', tab: m.tab, key: m.key } : null;
		case 'seen':
		case 'release':
			return isKey(m.key) ? { type: m.type, key: m.key } : null;
		default:
			return null;
	}
}

/** The claimed keys in the tab's storage; keys over 200 characters are not read back. */
export function readClaimed(store: KeyStore | null | undefined): Set<string> {
	try {
		const raw = store?.getItem(LIVE_CLAIMED_KEY);
		const parsed: unknown = raw ? JSON.parse(raw) : [];
		return new Set(Array.isArray(parsed) ? parsed.filter(isKey).slice(-CLAIMED_MAX) : []);
	} catch {
		return new Set();
	}
}

/** How long a claim waits for a rival claim from another tab before it counts (one channel round trip, with margin). */
export const CLAIM_WAIT_MS = 50;
const claimWait = () => new Promise<void>((resolve) => setTimeout(resolve, CLAIM_WAIT_MS));

export function createLiveHub(env: LiveHubEnv) {
	const tabs = new Map<string, TabState>();
	const claimed = readClaimed(env.storage());
	/** The lowest other tab id that claimed each key. */
	const rivals = new Map<string, string>();
	const seenListeners = new Set<(key: string) => void>();
	let me = { visible: env.visible(), hiddenAt: env.visible() ? 0 : env.now() };
	let stopped = false;

	function post(message: Message): void {
		try {
			env.channel?.postMessage(message);
		} catch {
			// A closed channel: this tab works alone.
		}
	}

	function addClaim(key: string): void {
		claimed.add(key);
		// Bounded in memory like in storage: the oldest claims go first.
		while (claimed.size > CLAIMED_MAX) {
			const oldest = claimed.values().next().value as string;
			claimed.delete(oldest);
			rivals.delete(oldest);
		}
		try {
			env.storage()?.setItem(LIVE_CLAIMED_KEY, JSON.stringify([...claimed].slice(-CLAIMED_MAX)));
		} catch {
			// Kept in memory only.
		}
	}

	function dropClaim(key: string): void {
		if (!claimed.delete(key)) return;
		rivals.delete(key);
		try {
			env.storage()?.setItem(LIVE_CLAIMED_KEY, JSON.stringify([...claimed]));
		} catch {
			// Kept in memory only.
		}
	}

	function announce(): void {
		post({ type: 'state', tab: env.tabId, visible: me.visible, hiddenAt: me.hiddenAt });
	}

	/** The other tabs heard from in the last 3 minutes; older ones are forgotten. */
	function liveTabs(): Map<string, TabState> {
		const cutoff = env.now() - TAB_EXPIRY_MS;
		for (const [tab, state] of tabs) if (state.heardAt < cutoff) tabs.delete(tab);
		return tabs;
	}

	function receive(data: unknown): void {
		const message = readMessage(data);
		if (!message) return;
		switch (message.type) {
			case 'state':
				if (message.tab !== env.tabId) tabs.set(message.tab, { visible: message.visible, hiddenAt: message.hiddenAt, heardAt: env.now() });
				return;
			case 'hello':
				if (message.tab !== env.tabId) announce();
				return;
			case 'bye':
				tabs.delete(message.tab);
				return;
			case 'claim': {
				if (message.tab === env.tabId) return;
				addClaim(message.key);
				const rival = rivals.get(message.key);
				if (rival == null || message.tab < rival) rivals.set(message.key, message.tab);
				return;
			}
			case 'seen':
				if (!env.visible()) for (const listener of seenListeners) listener(message.key);
				return;
			case 'release':
				dropClaim(message.key);
				return;
		}
	}

	if (env.channel) env.channel.onmessage = (event) => receive(event.data);

	/** Notifications are on: the switch, the permission, and no 401 in this tab. */
	function enabled(): boolean {
		return !stopped && env.notifications?.permission === 'granted' && readPref(env.prefs());
	}

	function anyVisible(): boolean {
		if (env.visible()) return true;
		for (const tab of liveTabs().values()) if (tab.visible) return true;
		return false;
	}

	/** The hidden tab that polls the bell: the one that became hidden last (ties: the smaller tab id). */
	function leader(): string {
		let best = { tab: env.tabId, at: me.hiddenAt };
		for (const [tab, state] of liveTabs()) {
			if (state.visible) continue;
			if (state.hiddenAt > best.at || (state.hiddenAt === best.at && tab < best.tab)) best = { tab, at: state.hiddenAt };
		}
		return best.tab;
	}

	/**
	 * Claims one alert key across tabs: false when another tab claimed it first; otherwise post the claim, wait
	 * `CLAIM_WAIT_MS` for a rival claim, and yield if a tab with a lower id claimed the same key meanwhile.
	 */
	async function claim(key: string): Promise<boolean> {
		if (!isKey(key) || claimed.has(key)) return false;
		addClaim(key);
		post({ type: 'claim', tab: env.tabId, key });
		await (env.wait ?? claimWait)();
		const rival = rivals.get(key);
		return rival == null || rival > env.tabId;
	}

	function markSeen(key: string): void {
		if (isKey(key)) post({ type: 'seen', key });
	}

	function notify(event: LiveEvent): void {
		const api = env.notifications;
		if (!api) return;
		try {
			const note = api.create(NOTIFY_TITLE, { body: event.text, tag: event.key });
			note.onclick = () => {
				env.open(event.href, event.readKey);
				note.close();
			};
		} catch {
			// The browser refused (permission revoked meanwhile): nothing is shown.
		}
	}

	return {
		receive,
		get stopped() {
			return stopped;
		},
		enabled,
		anyVisible,
		leader,
		claim,
		/** The page's visibility changed: say so (hiding makes this tab the bell's poller). */
		visibilityChanged(): void {
			const visible = env.visible();
			me = { visible, hiddenAt: visible ? me.hiddenAt : env.now() };
			announce();
		},
		/** On load, and on a return from the back-forward cache: ask the other tabs for their state and give ours. */
		hello(): void {
			post({ type: 'hello', tab: env.tabId });
			announce();
		},
		/** Every 60 s: still here. */
		heartbeat(): void {
			announce();
		},
		/** On page hide for good. */
		bye(): void {
			post({ type: 'bye', tab: env.tabId });
		},
		/** The bell may read while hidden: on, no console tab visible, and this tab elected. */
		hiddenPoll(): boolean {
			return !env.visible() && enabled() && !anyVisible() && leader() === env.tabId;
		},
		/** Alerts become system notifications now: on, and every console tab hidden. */
		notifying(): boolean {
			return !env.visible() && enabled() && !anyVisible();
		},
		/** Tell the hidden tabs this key was alerted, so a return there replays nothing. */
		markSeen,
		/** One system notification for this alert, if this tab wins its claim. */
		async notifyOnce(event: LiveEvent): Promise<boolean> {
			markSeen(event.key);
			if (!(await claim(event.key))) return false;
			notify(event);
			return true;
		},
		/** The problem no longer needs action: a recurrence may alert again, in every tab. */
		release(key: string): void {
			if (!isKey(key)) return;
			dropClaim(key);
			post({ type: 'release', key });
		},
		/** Keys seen in another tab while this one is hidden. Returns the unsubscribe. */
		onSeen(listener: (key: string) => void): () => void {
			seenListeners.add(listener);
			return () => seenListeners.delete(listener);
		},
		/** A 401: no more hidden polling or notifications in this tab. */
		stop(): void {
			stopped = true;
		}
	};
}

export type LiveHub = ReturnType<typeof createLiveHub>;

/** The browser's Notification, or null when unsupported. */
export function browserNotifications(): NotificationApi | null {
	if (typeof window === 'undefined' || typeof Notification === 'undefined') return null;
	return {
		get permission() {
			return Notification.permission as Permission;
		},
		requestPermission: () => Notification.requestPermission() as Promise<Permission>,
		create: (title, options) => new Notification(title, options)
	};
}

/** The per-browser store of the switch, or null when blocked. */
export function browserPrefs(): Storage | null {
	try {
		return typeof localStorage === 'undefined' ? null : localStorage;
	} catch {
		return null;
	}
}

/** The page events and timer `connectHub` uses (tests pass EventTargets and a stub timer). */
export type HubPage = {
	document: EventTarget;
	window: EventTarget;
	every: Every;
};

/** Wires a hub to the page: visibility, the 60 s heartbeat, back-forward cache returns and page hide. */
export function connectHub(hub: LiveHub, page: HubPage): () => void {
	const onVisibility = () => hub.visibilityChanged();
	const onShow = (event: Event) => {
		if ((event as Event & { persisted?: boolean }).persisted) hub.hello();
	};
	const onHide = () => hub.bye();
	page.document.addEventListener('visibilitychange', onVisibility);
	page.window.addEventListener('pageshow', onShow);
	page.window.addEventListener('pagehide', onHide);
	const stopBeat = page.every(() => hub.heartbeat(), HEARTBEAT_MS);
	hub.hello();
	return () => {
		page.document.removeEventListener('visibilitychange', onVisibility);
		page.window.removeEventListener('pageshow', onShow);
		page.window.removeEventListener('pagehide', onHide);
		stopBeat();
	};
}

let shared: { hub: LiveHub; dispose: () => void } | null = null;

/** Stops the page's hub (heartbeat, listeners, channel); the next `browserLiveHub` starts a new one. */
export function disposeBrowserHub(): void {
	shared?.dispose();
	shared = null;
}

/**
 * The page's one hub, created on first use in the browser; null on the server or when anything about it fails, so
 * the bell keeps working without it.
 */
export function browserLiveHub(deps: { storage: () => KeyStore | null; open: (href: string, readKey?: string) => void; every: Every }): LiveHub | null {
	if (shared) return shared.hub;
	try {
		if (typeof window === 'undefined' || typeof document === 'undefined') return null;
		let channel: ChannelLike | null = null;
		try {
			channel = typeof BroadcastChannel === 'undefined' ? null : (new BroadcastChannel(LIVE_CHANNEL) as unknown as ChannelLike);
		} catch {
			channel = null;
		}
		const hub = createLiveHub({
			tabId: tabId(),
			channel,
			storage: deps.storage,
			prefs: browserPrefs,
			notifications: browserNotifications(),
			now: () => Date.now(),
			visible: () => document.visibilityState === 'visible',
			open: (href, readKey) => {
				window.focus();
				deps.open(href, readKey);
			}
		});
		const disconnect = connectHub(hub, { document, window, every: deps.every });
		shared = {
			hub,
			dispose: () => {
				disconnect();
				hub.bye();
				channel?.close();
			}
		};
		return hub;
	} catch {
		return null;
	}
}
