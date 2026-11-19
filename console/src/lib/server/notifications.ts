/**
 * Notifications for the header bell (api-v1 v1.37 items 1 and 2), read and marked through the console route
 * `/notifications`. Also the one-time import of the retired What's new cookie (spec-header-org-project-bell §4 B).
 */
import type { RequestEvent } from '@sveltejs/kit';
import type { Notification, NotificationList } from '../api-types';
import { compareVersions, SEEN_VERSION_COOKIE } from '../changelog';
import { apiFetch } from './api';
import { isRecord } from './shape';

const MAX_ITEMS = 20;
const MAX_KEYS = 50;
const MAX_KEY = 200;
const CONTROL = /[\u0000-\u001F\u007F]/;
/** A console path: one leading slash, no backslash, no whitespace. */
const CONSOLE_PATH = /^\/(?!\/)[^\s\\]*$/;

function line(value: unknown, max: number): string | null {
	if (typeof value !== 'string' || value.length > max || CONTROL.test(value)) return null;
	return value;
}

/** One item, or null when it does not fit the contract (it is then left out, not the whole list). */
export function parseNotification(value: unknown): Notification | null {
	if (!isRecord(value)) return null;
	const key = line(value.key, MAX_KEY);
	const title = line(value.title, 300);
	const detail = line(value.detail, 500) ?? '';
	const href = line(value.href, 500);
	const at = line(value.at, 64);
	if (!key || !title || !href || !CONSOLE_PATH.test(href) || !at || Number.isNaN(Date.parse(at))) return null;
	if (value.kind !== 'release' && value.kind !== 'host_update') return null;
	if (typeof value.read !== 'boolean') return null;
	return { key, kind: value.kind, title, detail, href, at, read: value.read };
}

/** `unread` is recounted from the items kept, so the dot never disagrees with the rows. */
export function parseNotificationList(payload: unknown): NotificationList | null {
	if (!isRecord(payload) || !Array.isArray(payload.items)) return null;
	const items = payload.items
		.slice(0, MAX_ITEMS)
		.map(parseNotification)
		.filter((item): item is Notification => item != null);
	return { items, unread: items.filter((item) => !item.read).length };
}

export async function loadNotifications(event: RequestEvent): Promise<NotificationList | null> {
	return parseNotificationList(await apiFetch(event, '/v1/me/notifications'));
}

/** The read body the console accepts from the bell: `{keys}` (1–50 keys, 1–200 characters each) or `{all:true}`. */
export function parseReadBody(value: unknown): { keys: string[] } | { all: true } | null {
	if (!isRecord(value)) return null;
	const hasKeys = 'keys' in value;
	const hasAll = 'all' in value;
	if (hasKeys === hasAll) return null;
	if (hasAll) return value.all === true ? { all: true } : null;
	if (!Array.isArray(value.keys) || value.keys.length < 1 || value.keys.length > MAX_KEYS) return null;
	const keys = value.keys.map((key) => line(key, MAX_KEY));
	if (keys.some((key) => !key)) return null;
	return { keys: keys as string[] };
}

export async function markRead(
	event: RequestEvent,
	body: { keys: string[] } | { all: true },
	timeoutMs?: number
): Promise<void> {
	await apiFetch(event, '/v1/me/notifications/read', { method: 'POST', body: JSON.stringify(body) }, timeoutMs);
}

/** The API shows at most this many release items (api-v1 v1.37 item 1). */
const MAX_RELEASE_ITEMS = 5;
/** The one-time import must not slow every page while the API is failing. */
export const IMPORT_TIMEOUT_MS = 2_000;

/**
 * The retired What's new cookie, imported once: every release item (the newest five releases) whose version is at or
 * below the seen version is marked read, so an upgrade does not resurface releases the user had already seen. The
 * cookie goes once the API has the marks (or when it names no such release or does not parse); a failed or slow
 * write (2 s) keeps it for the next page load. `versions` is the changelog, newest first.
 */
export async function importSeenVersion(event: RequestEvent, versions: readonly string[]): Promise<void> {
	const seen = event.cookies.get(SEEN_VERSION_COOKIE);
	if (seen === undefined) return;
	const keys = versions
		.slice(0, MAX_RELEASE_ITEMS)
		.filter((version) => {
			const order = compareVersions(version, seen);
			return order != null && order <= 0;
		})
		.map((version) => `release:${version}`);
	if (keys.length > 0) {
		try {
			await markRead(event, { keys }, IMPORT_TIMEOUT_MS);
		} catch {
			return;
		}
	}
	event.cookies.delete(SEEN_VERSION_COOKIE, { path: '/' });
}
