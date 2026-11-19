import { isHttpError, isRedirect, json, type RequestEvent } from '@sveltejs/kit';
import { ApiError } from '$lib/server/api';
import { loadNotifications, markRead, parseReadBody } from '$lib/server/notifications';
import type { RequestHandler } from './$types';

/**
 * The bell's data (spec-header-org-project-bell §3.2): GET proxies `GET /v1/me/notifications`, POST proxies
 * `POST /v1/me/notifications/read` (api-v1 v1.37). Signed-in callers only, same origin, never cached.
 */
const NO_STORE = { 'cache-control': 'no-store' };

function answer(body: unknown, status = 200): Response {
	return json(body, { status, headers: NO_STORE });
}

function failure(err: unknown): Response {
	// A 401 from the API (apiFetch's sign-in redirect) is a JSON 401 here: the bell is a fetch, not a page.
	if (isRedirect(err)) return answer({ message: 'Sign in required.' }, 401);
	if (isHttpError(err)) throw err;
	const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
	return answer({ message: 'Notifications could not be loaded.' }, status);
}

function signedIn(event: RequestEvent): boolean {
	return event.locals.user != null;
}

export const GET: RequestHandler = async (event) => {
	if (!signedIn(event)) return answer({ message: 'Sign in required.' }, 401);
	try {
		const list = await loadNotifications(event);
		if (!list) return answer({ message: 'Notifications could not be loaded.' }, 502);
		return answer(list);
	} catch (err) {
		return failure(err);
	}
};

export const POST: RequestHandler = async (event) => {
	if (!signedIn(event)) return answer({ message: 'Sign in required.' }, 401);
	// A JSON POST is outside SvelteKit's form CSRF check, so the origin is checked here.
	if (event.request.headers.get('origin') !== event.url.origin) return answer({ message: 'Forbidden.' }, 403);
	if (!(event.request.headers.get('content-type') ?? '').startsWith('application/json')) {
		return answer({ message: 'Send JSON.' }, 415);
	}
	let raw: unknown;
	try {
		raw = await event.request.json();
	} catch {
		return answer({ message: 'Send keys or all.' }, 400);
	}
	const body = parseReadBody(raw);
	if (!body) return answer({ message: 'Send keys or all.' }, 400);
	try {
		await markRead(event, body);
		return new Response(null, { status: 204, headers: NO_STORE });
	} catch (err) {
		return failure(err);
	}
};
