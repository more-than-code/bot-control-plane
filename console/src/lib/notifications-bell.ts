/**
 * The header bell's behaviour (spec-header-org-project-bell §3.2), kept out of the component so it is tested with
 * a stub fetch and goto. `NotificationsBell.svelte` holds the state this reports through `update` and stays thin.
 */
import type { Notification, NotificationList } from './api-types';
import { NOTIFICATIONS_ROUTE, readBody, readNotificationList } from './notifications';

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

/** "Mark all read" is off while it runs and when nothing is unread. */
export function canMarkAll(state: BellState): boolean {
	return !state.marking && (state.list?.unread ?? 0) > 0;
}

export type BellDeps = {
	fetch: (input: string, init?: RequestInit) => Promise<Response>;
	goto: (href: string) => Promise<unknown>;
	update: (state: BellState) => void;
};

export function createBell(deps: BellDeps, initial: BellState = initialBellState()) {
	let state = initial;
	// Each refresh takes a number; an answer that is not the latest request's is dropped.
	let latest = 0;

	function set(patch: Partial<BellState>) {
		state = { ...state, ...patch };
		deps.update(state);
	}

	async function refresh(): Promise<void> {
		const id = ++latest;
		let next: NotificationList | null = null;
		try {
			const response = await deps.fetch(NOTIFICATIONS_ROUTE, { headers: { accept: 'application/json' } });
			next = response.ok ? readNotificationList(await response.json()) : null;
		} catch {
			next = null;
		}
		if (id !== latest) return;
		if (next) set({ list: next, loadFailed: false });
		else set({ loadFailed: true });
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
			if (!item.read) {
				const ok = await markRead([item.key]);
				if (!ok) set({ markFailed: true });
			}
			close();
			await deps.goto(item.href);
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
