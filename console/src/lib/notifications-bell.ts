/**
 * The header bell's behaviour (spec-header-org-project-bell §3.2), kept out of the component so it is tested with
 * a stub fetch and goto. `NotificationsBell.svelte` holds the state this reports through `update` and stays thin.
 */
import type { Notification, NotificationList } from './api-types';
import type { Every } from './live-common';
import { BELL_POLL_MS, needActionCount, NOTIFICATIONS_ROUTE, readBody, readNotificationList } from './notifications';

export const MARK_ERROR = 'Could not mark as read.';

export type BellState = {
	/** The last list that loaded; kept when a later refresh fails. Null until the first one loads. */
	list: NotificationList | null;
	/** The latest refresh failed (the error line shows above any kept list). */
	loadFailed: boolean;
	/** "Mark all read" is running. */
	marking: boolean;
	/** The latest mark-read request failed. */
	markFailed: boolean;
};

export function initialBellState(initial: { list: NotificationList | null; failed: boolean } | null = null): BellState {
	return { list: initial?.list ?? null, loadFailed: initial?.failed ?? false, marking: false, markFailed: false };
}

/** The dot and the label's count come from the last list that loaded. */
export function bellUnread(state: BellState): number | null {
	return state.list?.unread ?? null;
}

/** Unread open problems that need action: the dot turns red and the label says so (api-v1 v1.41). */
export function bellNeedAction(state: BellState): number {
	return needActionCount(state.list);
}

/** "Mark all read" is off while it runs and when nothing is unread. */
export function canMarkAll(state: BellState): boolean {
	return !state.marking && (state.list?.unread ?? 0) > 0;
}

/** The timer, page visibility and popover state the bell polls with (`browserPoll` builds them in the browser). */
export type PollDeps = {
	/** Calls fn every ms until the returned stop runs. */
	every: Every;
	/** The tab is visible (`document.visibilityState === 'visible'`). */
	visible: () => boolean;
	/** The popover is open: a tick then skips, so the list does not change under the reader. */
	open: () => boolean;
	/**
	 * Reading while hidden is allowed: desktop notifications on, every console tab hidden and this tab elected
	 * (spec-console-live-updates §0, §2.6). Without it a hidden tab never reads.
	 */
	hiddenPoll?: () => boolean;
};

/** The bell's read interval while hidden, with desktop notifications on (spec-console-live-updates §2.6). */
export const HIDDEN_BELL_MS = 60_000;

/** What `browserPoll` uses from the page; tests pass a stand-in. */
export type PollEnv = {
	setInterval: (fn: () => void, ms: number) => unknown;
	clearInterval: (id: never) => void;
	document: { visibilityState: string };
};

/** The bell's poll deps in the browser: `setInterval`, `document.visibilityState` and the popover's open state. */
export function browserPoll(
	open: () => boolean,
	env: PollEnv = globalThis as unknown as PollEnv,
	hiddenPoll?: () => boolean
): PollDeps {
	return {
		hiddenPoll,
		every: (fn, ms) => {
			const id = env.setInterval(fn, ms);
			return () => env.clearInterval(id as never);
		},
		visible: () => env.document.visibilityState === 'visible',
		open
	};
}

export type BellDeps = {
	fetch: (input: string, init?: RequestInit) => Promise<Response>;
	goto: (href: string) => Promise<unknown>;
	update: (state: BellState) => void;
	/** Each list that loads, with the one before it; `first` for the first list since the page loaded. */
	onLoaded?: (previous: NotificationList | null, next: NotificationList, first: boolean) => void;
	/** The list answered 401 (signed out): every read stops. */
	onSignedOut?: () => void;
};

export function createBell(deps: BellDeps, initial: BellState = initialBellState()) {
	let state = initial;
	// Each refresh takes a number; an answer that is not the latest request's is dropped.
	let latest = 0;
	/** A list has loaded since the page loaded. */
	let primed = false;
	/** A 401: no more reads from this bell. */
	let stopped = false;

	function set(patch: Partial<BellState>) {
		state = { ...state, ...patch };
		deps.update(state);
	}

	async function refresh(): Promise<void> {
		if (stopped) return;
		const id = ++latest;
		let next: NotificationList | null = null;
		try {
			const response = await deps.fetch(NOTIFICATIONS_ROUTE, { headers: { accept: 'application/json' } });
			if (response.status === 401) {
				stopped = true;
				deps.onSignedOut?.();
			}
			next = response.ok ? readNotificationList(await response.json()) : null;
		} catch {
			next = null;
		}
		if (id !== latest) return;
		if (!next) {
			set({ loadFailed: true });
			return;
		}
		const previous = state.list;
		set({ list: next, loadFailed: false });
		deps.onLoaded?.(previous, next, !primed);
		primed = true;
	}

	/** One key marked read; a failure shows the bell's mark error, as for a row click. */
	async function markOne(key: string): Promise<void> {
		const ok = await markRead([key]);
		if (!ok) set({ markFailed: true });
	}

	async function markRead(keys: string[] | 'all'): Promise<boolean> {
		try {
			const response = await deps.fetch(NOTIFICATIONS_ROUTE, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: readBody(keys)
			});
			return response.ok;
		} catch {
			return false;
		}
	}

	return {
		get state() {
			return state;
		},
		refresh,
		/**
		 * Reads again every 2 minutes while the tab is visible, so a problem that opens while the page is open shows
		 * (spec-bell-problems §3.3). A hidden tab or an open popover skips the tick (opening already read the list).
		 * Returns the stop for the component's destroy.
		 */
		poll(env: PollDeps): () => void {
			const stopVisible = env.every(() => {
				if (env.visible() && !env.open()) void refresh();
			}, BELL_POLL_MS);
			const hiddenPoll = env.hiddenPoll;
			if (!hiddenPoll) return stopVisible;
			const stopHidden = env.every(() => {
				if (!env.visible() && hiddenPoll()) void refresh();
			}, HIDDEN_BELL_MS);
			return () => {
				stopVisible();
				stopHidden();
			};
		},
		get stopped() {
			return stopped;
		},
		/** After mount: the first read, so no page load waits on it. */
		start(): Promise<void> {
			return refresh();
		},
		/** Opening reads again and marks nothing; it also clears an old mark error. */
		openChanged(open: boolean): Promise<void> {
			if (!open) return Promise.resolve();
			if (state.markFailed) set({ markFailed: false });
			return refresh();
		},
		/** A row click: mark it read first, then go to its page (also when the mark failed; the row stays unread). */
		async openItem(item: Notification, close: () => void): Promise<void> {
			if (!item.read) await markOne(item.key);
			close();
			await deps.goto(item.href);
			void refresh();
		},
		/**
		 * A problem alert's "View" or its desktop notification click: the same path as a row click for that key, so
		 * the dot and the count clear (spec-console-live-updates §0). The item may already be read (an escalation),
		 * so the key is marked either way; a failed mark still navigates.
		 */
		async openKey(key: string, href: string): Promise<void> {
			await markOne(key);
			await deps.goto(href);
			void refresh();
		},
		async markAll(): Promise<void> {
			if (!canMarkAll(state)) return;
			set({ marking: true, markFailed: false });
			const ok = await markRead('all');
			set({ marking: false, markFailed: !ok });
			if (ok) await refresh();
		}
	};
}
