/**
 * Shapes shared by the request fill pages: `/fill/[id]` (value requests, api-v1 v1.28) and
 * `/fill-file/[id]` (file requests, v1.30). An agent asked; the person who asked answers here.
 */

/** The 43-character base64url id the API mints for every request type. */
const REQUEST_ID = /^[A-Za-z0-9_-]{43}$/;
/**
 * A handle (api-v1 v1.33 item 8): `h-` and the stored SHA-256 of the id, 64 lowercase hex. The `/requests` page links
 * by it; the API answers it for the request's own signed-in user only. It is passed to the API unchanged.
 */
const REQUEST_HANDLE = /^h-[0-9a-f]{64}$/;

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Pending, declined and expired are shared; `filled` is the done state of value and file requests. */
export const REQUEST_STATES = ['pending', 'filled', 'declined', 'expired'] as const;

export type RequestState = (typeof REQUEST_STATES)[number];

/** What a fill page shows: the form, or one problem with no form. `error` is a load failure: its message only. */
export type RequestView = 'form' | 'used' | 'expired' | 'missing' | 'invalid' | 'error';

/** A request id or a handle (`h-` + 64 hex): what a fill, file or connect page accepts in its path. */
export function isRequestId(value: unknown): value is string {
	return typeof value === 'string' && (REQUEST_ID.test(value) || REQUEST_HANDLE.test(value));
}

/** A bare handle (64 lowercase hex), as `GET /v1/me/requests` sends it. */
export function isRequestHandle(value: unknown): value is string {
	return typeof value === 'string' && REQUEST_HANDLE.test(`h-${value}`);
}

export function isRequestState(value: unknown): value is RequestState {
	return typeof value === 'string' && (REQUEST_STATES as readonly string[]).includes(value);
}

/** A string field of at most max characters, or null. */
export function boundedText(record: Record<string, unknown>, key: string, max: number): string | null {
	const value = record[key];
	return typeof value === 'string' && value.length <= max ? value : null;
}

/** The view for a loaded request: pending shows the form; any done or declined state is used. */
export function requestView(state: RequestState): RequestView {
	if (state === 'pending') return 'form';
	if (state === 'expired') return 'expired';
	return 'used';
}
