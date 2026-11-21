/**
 * Alerts for problems that newly need action (spec-console-live-updates §0 item 1, §2.3 for problems).
 *
 * Each bell read is compared with the one before: a problem that is now open at level needs_action
 * (`isOpenNeedsAction`, read or not) and was not
 * before is alerted once. The first read after a page load alerts nothing. The keys alerted in this tab are kept in
 * its storage (`visitStorage`), so a reload replays nothing; a key that no longer needs action is forgotten, so a
 * recurrence (the API reopens a problem under the same key within 30 minutes) alerts again. While a console tab is
 * visible an alert is a toast (stays until dismissed, "View", "Dismiss"; more than 3 at once give one summary);
 * while every tab is hidden and desktop notifications are on, it is one system notification across tabs instead.
 */
import type { Notification, NotificationList } from './api-types';
import { visitStorage } from './catalog-open';
import { browserLiveHub, browserPrefs, clearPref, disposeBrowserHub, LIVE_CLAIMED_KEY, type LiveEvent, type LiveHub } from './desktop-notify';
import { browserEvery, isKey, shortHash, type KeyStore, type ToastApi } from './live-common';

/** The problem keys alerted in this tab. */
export const TOASTED_KEY = 'deploybot:bell-toasted';
/** At most this many problem toasts at once; more give one summary toast. */
export const PROBLEM_TOASTS_MAX = 3;
const ALERTED_MAX = 200;

type TabStore = KeyStore & Pick<Storage, 'removeItem'>;

/**
 * An open problem at level needs_action, read or not. The alert follows the level, not the read mark: read marks are
 * kept per key, so a problem the user read as heads_up that escalates on the same row (or reopens within 30
 * minutes) comes back read. `needsAction` (unread too) stays for the bell's red dot and label.
 */
export function isOpenNeedsAction(item: Notification): boolean {
	return item.kind === 'problem' && item.state === 'open' && item.level === 'needs_action';
}

/** Items that are open needs_action now and were not in the previous list (all of them after no previous list). */
export function newNeedsAction(previous: NotificationList | null, next: NotificationList): Notification[] {
	const before = new Set((previous?.items ?? []).filter(isOpenNeedsAction).map((item) => item.key));
	return next.items.filter((item) => isOpenNeedsAction(item) && !before.has(item.key));
}

export function readAlerted(storage: KeyStore | null | undefined): Set<string> {
	try {
		const raw = storage?.getItem(TOASTED_KEY);
		if (!raw) return new Set();
		const parsed: unknown = JSON.parse(raw);
		return new Set(Array.isArray(parsed) ? parsed.filter(isKey).slice(-ALERTED_MAX) : []);
	} catch {
		return new Set();
	}
}

export function writeAlerted(storage: KeyStore | null | undefined, keys: ReadonlySet<string>): void {
	try {
		storage?.setItem(TOASTED_KEY, JSON.stringify([...keys].slice(-ALERTED_MAX)));
	} catch {
		// Storage refused: the keys are kept for this read only.
	}
}

/** A key another tab alerted while this one is hidden: its return then replays nothing. */
export function addAlerted(storage: KeyStore | null | undefined, key: string): void {
	if (!isKey(key)) return;
	const keys = readAlerted(storage);
	if (keys.has(key)) return;
	keys.add(key);
	writeAlerted(storage, keys);
}

/** Sign-out or a 401: this tab's alerted and claimed keys go. */
export function clearAlertKeys(storage: Pick<Storage, 'removeItem'> | null | undefined): void {
	for (const key of [TOASTED_KEY, LIVE_CLAIMED_KEY]) {
		try {
			storage?.removeItem(key);
		} catch {
			// Storage refused: nothing more to do.
		}
	}
}

const CONSOLE_PATH = /^\/(?!\/)[^\s\\]*$/;

/** A console path for a summary notification's click; anything else (`//host`, a URL) falls back to `/projects`. */
export function consolePath(path: string): string {
	return CONSOLE_PATH.test(path) ? path : '/projects';
}

export function problemSummaryText(count: number): string {
	return count === 1 ? '1 new problem needs action' : `${count} new problems need action`;
}

/**
 * One toast per new problem, or one summary when they would make more than 3 problem toasts at once. The summary
 * counts the problem toasts already showing too.
 */
export type ProblemToastPlan = { kind: 'each'; items: Notification[] } | { kind: 'summary'; count: number } | null;

export function problemToastPlan(fresh: readonly Notification[], showing: number): ProblemToastPlan {
	if (fresh.length === 0) return null;
	if (showing + fresh.length > PROBLEM_TOASTS_MAX) return { kind: 'summary', count: showing + fresh.length };
	return { kind: 'each', items: [...fresh] };
}

/**
 * The problem toasts: error style (announced assertively), stay until dismissed, dismissible, "View" to the
 * problem's page and "Dismiss". Each summary has its own id, so a later one never replaces an unread one.
 */
/** What the alerts do on a click: open a problem (mark its key read, go, refresh the bell), go somewhere, open the bell. */
export type AlertActions = {
	openProblem: (key: string, href: string) => unknown;
	goto: (href: string) => unknown;
	openBell: () => void;
};

export function createProblemToaster(api: Pick<ToastApi, 'error'>, actions: Pick<AlertActions, 'openProblem' | 'openBell'>) {
	const showing = new Set<string | number>();
	let summaries = 0;
	function done(id: string | number) {
		return () => showing.delete(id);
	}
	const base = { duration: Number.POSITIVE_INFINITY, important: true };
	return {
		get showing() {
			return showing.size;
		},
		show(fresh: readonly Notification[]): void {
			const plan = problemToastPlan(fresh, showing.size);
			if (!plan) return;
			if (plan.kind === 'summary') {
				summaries += 1;
				api.error(problemSummaryText(plan.count), {
					...base,
					id: `bell-problems-summary-${summaries}`,
					action: { label: 'Open the bell', onClick: () => actions.openBell() },
					cancel: { label: 'Dismiss', onClick: () => {} }
				});
				return;
			}
			for (const item of plan.items) {
				const id = `bell-${item.key}`;
				// svelte-sonner removes a toast after its action or cancel click without calling onDismiss, so both
				// buttons drop it from `showing` themselves; onDismiss covers the close button and a swipe.
				api.error(item.title, {
					...base,
					id,
					action: {
						label: 'View',
						onClick: () => {
							showing.delete(id);
							void actions.openProblem(item.key, item.href);
						}
					},
					cancel: { label: 'Dismiss', onClick: done(id) },
					onDismiss: done(id),
					onAutoClose: done(id)
				});
				showing.add(id);
			}
		}
	};
}

export type ProblemToaster = ReturnType<typeof createProblemToaster>;

/**
 * New problems from one read: toasts while a console tab is visible (or without notifications); with every tab hidden
 * and notifications on, one notification each, or one "N new problems need action" for more than 3, under a hashed
 * key. Either way the hidden tabs learn the keys, so a return there replays nothing.
 */
export async function deliverProblems(
	hub: Pick<LiveHub, 'notifying' | 'markSeen' | 'notifyOnce'> | null,
	items: readonly Notification[],
	input: { toaster: Pick<ProblemToaster, 'show'>; here: string }
): Promise<void> {
	for (const item of items) hub?.markSeen(item.key);
	if (!hub || !hub.notifying()) {
		input.toaster.show(items);
		return;
	}
	const events: LiveEvent[] = items.map((item) => ({ key: item.key, text: item.title, href: item.href, readKey: item.key }));
	if (events.length > PROBLEM_TOASTS_MAX) {
		const key = `problems:${shortHash(events.map((event) => event.key).join(','))}`;
		await hub.notifyOnce({ key, text: problemSummaryText(events.length), href: consolePath(input.here) });
		return;
	}
	for (const event of events) await hub.notifyOnce(event);
}

export type ProblemAlertsDeps = {
	/** The page's hub (null without desktop notifications support or on the server). */
	hub: LiveHub | null;
	storage: () => TabStore | null;
	/** The per-browser store of the desktop switch, cleared on a 401 like at sign-out. */
	prefs?: () => Pick<Storage, 'removeItem'> | null;
	/** Stops the hub's page wiring (heartbeat and listeners) when the bell is destroyed. */
	disposeHub?: () => void;
	toaster: Pick<ProblemToaster, 'show'>;
	/** The current console page, for a summary notification's click. */
	here: () => string;
};

/** The bell's alert wiring: what `createBell` and its poll need (`onLoaded`, `onSignedOut`, `hiddenPoll`). */
export function createProblemAlerts(deps: ProblemAlertsDeps) {
	return {
		/** Each list that loads: alert what newly needs action (never on the first), forget what no longer does. */
		onLoaded(previous: NotificationList | null, next: NotificationList, first: boolean): Promise<void> {
			const storage = deps.storage();
			const alerted = readAlerted(storage);
			const fresh = first ? [] : newNeedsAction(previous, next).filter((item) => !alerted.has(item.key));
			for (const item of fresh) alerted.add(item.key);
			const still = new Set(next.items.filter(isOpenNeedsAction).map((item) => item.key));
			for (const key of [...alerted]) {
				if (still.has(key)) continue;
				alerted.delete(key);
				deps.hub?.release(key);
			}
			writeAlerted(storage, alerted);
			if (fresh.length === 0) return Promise.resolve();
			return deliverProblems(deps.hub, fresh, { toaster: deps.toaster, here: deps.here() });
		},
		/**
		 * A 401 (signed out, or the session expired): hidden polling and notifications stop, and this tab's alert keys
		 * and the desktop choice go, so whoever signs in next starts off.
		 */
		onSignedOut(): void {
			deps.hub?.stop();
			signOutCleanup(deps.storage(), deps.prefs?.() ?? null);
		},
		/** The bell is destroyed: the hub's heartbeat and page listeners stop. */
		dispose(): void {
			deps.disposeHub?.();
		},
		/** The bell may read while hidden (desktop notifications, §2.6). */
		hiddenPoll(): boolean {
			return deps.hub?.hiddenPoll() ?? false;
		},
		/** Keys other tabs alerted while this one is hidden. Returns the unsubscribe. */
		watchSeen(): () => void {
			return deps.hub?.onSeen((key) => addAlerted(deps.storage(), key)) ?? (() => {});
		}
	};
}

/** A desktop notification click: a problem goes through `openProblem` (marked read); a summary only navigates. */
export function openFromClick(actions: Pick<AlertActions, 'openProblem' | 'goto'>, href: string, readKey?: string): unknown {
	return readKey ? actions.openProblem(readKey, href) : actions.goto(href);
}

/** Sign-out: this tab's alert keys and the desktop notifications choice go (the next person starts off). */
export function signOutCleanup(tab: Pick<Storage, 'removeItem'> | null, prefs: Pick<Storage, 'removeItem'> | null): void {
	clearAlertKeys(tab);
	clearPref(prefs);
}

/**
 * The bell's alerts in the browser: the page's hub (none on the server, or when it cannot start), the tab's storage
 * and the real toaster. `NotificationsBell.svelte` passes its controller's `openKey`, navigation and its popover.
 */
export function browserProblemAlerts(input: { toast: Pick<ToastApi, 'error'> } & AlertActions) {
	const hub = browserLiveHub({ storage: visitStorage, open: (href, readKey) => void openFromClick(input, href, readKey), every: browserEvery });
	return createProblemAlerts({
		hub,
		storage: visitStorage,
		prefs: browserPrefs,
		disposeHub: disposeBrowserHub,
		toaster: createProblemToaster(input.toast, { openProblem: input.openProblem, openBell: input.openBell }),
		here: () => (typeof location === 'undefined' ? '/projects' : consolePath(`${location.pathname}${location.search}`))
	});
}
