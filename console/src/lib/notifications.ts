/**
 * The header bell (spec-header-org-project-bell §3.2): its accessible name and the console route it reads.
 * The browser calls only the console route `/notifications`, never the API.
 */
import type { NotificationList } from './api-types';

/** The console route the bell reads (GET) and marks read through (POST). */
export const NOTIFICATIONS_ROUTE = '/notifications';

export const NOTIFICATIONS_ERROR = 'Notifications could not be loaded.';

/** "Notifications, N unread", or "Notifications" when nothing is unread or the list did not load. */
export function bellLabel(unread: number | null): string {
	return unread != null && unread > 0 ? `Notifications, ${unread} unread` : 'Notifications';
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
