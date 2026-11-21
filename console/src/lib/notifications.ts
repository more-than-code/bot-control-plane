/**
 * The header bell (spec-header-org-project-bell §3.2): its accessible name and the console route it reads.
 * The browser calls only the console route `/notifications`, never the API.
 */
import type { Notification, NotificationList, ProblemKind } from './api-types';

/** The console route the bell reads (GET) and marks read through (POST). */
export const NOTIFICATIONS_ROUTE = '/notifications';

export const NOTIFICATIONS_ERROR = 'Notifications could not be loaded.';

/**
 * "Notifications, N unread, M need action", without the last part when nothing unread needs action, or
 * "Notifications" when nothing is unread or the list did not load.
 */
export function bellLabel(unread: number | null, needAction = 0): string {
	if (unread == null || unread <= 0) return 'Notifications';
	return needAction > 0 ? `Notifications, ${unread} unread, ${needAction} need action` : `Notifications, ${unread} unread`;
}

/** An unread open problem that needs action (api-v1 v1.41): the bell dot turns red while one exists. */
export function needsAction(item: Notification): boolean {
	return item.kind === 'problem' && item.state === 'open' && item.level === 'needs_action' && !item.read;
}

/** How many unread open problems need action in the list (0 when it did not load). */
export function needActionCount(list: NotificationList | null): number {
	return list?.items.filter(needsAction).length ?? 0;
}

/**
 * A problem item's kind, from its link (bell items carry no problem kind, api-v1 v1.41 item 3): a host page is
 * `host_offline`, a host's Config files tab `host_file` (api-v1 v1.44), the registries section `registry_login`,
 * anything else (a catalog entry) `deploy`.
 */
export function problemKindOf(item: Notification): ProblemKind | null {
	if (item.kind !== 'problem') return null;
	if (item.href.startsWith('/hosts/') && /[?&]tab=files(&|#|$)/.test(item.href)) return 'host_file';
	if (item.href.startsWith('/hosts/')) return 'host_offline';
	if (item.href.endsWith('#registries')) return 'registry_login';
	return 'deploy';
}

/** How often the bell reads again while the tab is visible (spec-bell-problems §3.3). */
export const BELL_POLL_MS = 2 * 60_000;

function span(ms: number): string {
	const minutes = Math.max(1, Math.floor(ms / 60_000));
	if (minutes < 60) return `${minutes} min`;
	const hours = Math.floor(minutes / 60);
	if (hours < 48) return `${hours} h`;
	return `${Math.floor(hours / 24)} d`;
}

/** A problem row's age: "open 25 min" or "resolved 3 h ago"; null for other kinds or a bad time. */
export function problemAge(item: Notification, now: number = Date.now()): string | null {
	if (item.kind !== 'problem') return null;
	const at = Date.parse(item.at);
	if (!Number.isFinite(at) || !Number.isFinite(now)) return null;
	const ms = Math.max(0, now - at);
	return item.state === 'resolved' ? `resolved ${span(ms)} ago` : `open ${span(ms)}`;
}

/** The list the console route answers, or null for anything else (the bell then shows its error line). */
export function readNotificationList(value: unknown): NotificationList | null {
	if (value == null || typeof value !== 'object' || Array.isArray(value)) return null;
	const record = value as Record<string, unknown>;
	if (!Array.isArray(record.items) || typeof record.unread !== 'number') return null;
	return value as NotificationList;
}

/** The read request body: one key from a row click, or every item from "Mark all read". */
export function readBody(keys: string[] | 'all'): string {
	return JSON.stringify(keys === 'all' ? { all: true } : { keys });
}
