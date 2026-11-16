/**
 * Shapes shared by the request fill pages: `/fill/[id]` (value requests, api-v1 v1.28) and
 * `/fill-file/[id]` (file requests, v1.30). An agent asked; the person who asked answers here.
 */

/** The 43-character base64url id the API mints for every request type. */
const REQUEST_ID = /^[A-Za-z0-9_-]{43}$/;

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Pending, declined and expired are shared; `filled` is the done state of value and file requests. */
export const REQUEST_STATES = ['pending', 'filled', 'declined', 'expired'] as const;

export type RequestState = (typeof REQUEST_STATES)[number];

/** What a fill page shows: the form, or one problem with no form. `error` is a load failure: its message only. */
export type RequestView = 'form' | 'used' | 'expired' | 'missing' | 'invalid' | 'error';

export function isRequestId(value: unknown): value is string {
	return typeof value === 'string' && REQUEST_ID.test(value);
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
