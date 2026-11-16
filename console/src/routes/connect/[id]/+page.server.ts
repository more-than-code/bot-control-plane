import { fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import { ApiError, apiFetch, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { GITHUB_CONNECT_COOKIE, GITHUB_RETURN_COOKIE, githubReturnCookieOptions } from '$lib/server/settings-auto';
import { CONNECT_TEXT, connectView, GITHUB_OFF_MESSAGE, isConnectRequestId, parseConnectRequest, type ConnectRequest, type ConnectView } from '$lib/connect-request';
import { GITHUB_NOTES, githubNoteCode, isGitHubAuthorizeUrl } from '$lib/settings-auto';
import type { Actions, PageServerLoad } from './$types';

/**
 * Connect GitHub started from chat (api-v1 v1.31 items 4, 5 and 7). Signed out, the hook sends the browser to sign-in
 * and back here. The API answers only the person who asked, in a console session, while still owner or admin. The
 * page never redirects on its own: Continue and Decline are the person's choice.
 */

function requestPath(id: string): string {
	return `/v1/registry-connect-requests/${encodeURIComponent(id)}`;
}

type Loaded = { view: ConnectView; request: ConnectRequest | null; message: string | null };

async function loadRequest(event: RequestEvent, id: string): Promise<Loaded> {
	if (!isConnectRequestId(id)) return { view: 'invalid', request: null, message: null };
	try {
		const request = parseConnectRequest(await apiFetch(event, `${requestPath(id)}/page`), id);
		if (!request) return { view: 'error', request: null, message: CONNECT_TEXT.unexpected };
		return { view: connectView(request.state), request, message: null };
	} catch (err) {
		if (err instanceof ApiError && err.code === 'not_found') return { view: 'missing', request: null, message: null };
		return { view: 'error', request: null, message: loadFailure(err).message };
	}
}

export const load: PageServerLoad = async (event) => {
	requireUser(event);
	const loaded = await loadRequest(event, event.params.id);
	// A short note after a GitHub round trip (`?github=<code>`), never API text. A round trip that did not connect
	// keeps the buttons under its note. One that connected after the request ended (expired or declined meanwhile)
	// says the login is saved anyway (api-v1 v1.31 item 6).
	const code = githubNoteCode(event.url.searchParams.get('github'));
	const note = code && code !== 'connected' && loaded.view === 'form' ? GITHUB_NOTES[code] : null;
	const connectedEnded = code === 'connected' && (loaded.view === 'expired' || loaded.view === 'declined');
	return present(event, { ...loaded, note, connectedEnded });
};

/**
 * A refused Continue or Decline. `not_found` means the request is gone for this person; `conflict` (used, expired)
 * and GitHub not configured close the buttons too. Anything else keeps them for another try.
 */
function failure(err: unknown) {
	if (isRedirect(err) || isHttpError(err)) throw err;
	if (err instanceof ApiError && err.code === 'not_found') return fail(404, { message: CONNECT_TEXT.missing, closed: true });
	// Only "not configured" is for good; another `unavailable` (the sign-in store is full) may pass, so the buttons stay.
	if (err instanceof ApiError && err.code === 'unavailable' && err.msg === GITHUB_OFF_MESSAGE) {
		return fail(503, { message: CONNECT_TEXT.off, closed: true });
	}
	const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
	const closed = err instanceof ApiError && err.code === 'conflict';
	return fail(status, { message: loadFailure(err).message, closed });
}

export const actions: Actions = {
	/** Continue (item 5): start Connect GitHub with this request and send the browser to github.com. */
	continue: async (event) => {
		requireUser(event);
		const id = event.params.id;
		if (!isConnectRequestId(id)) return fail(400, { message: CONNECT_TEXT.invalid, closed: true });
		let target: unknown;
		let projectId: string;
		try {
			// The project comes from the API, not the form.
			const request = parseConnectRequest(await apiFetch(event, `${requestPath(id)}/page`), id);
			if (!request) return fail(502, { message: CONNECT_TEXT.unexpected, closed: false });
			projectId = request.project_id;
			const payload = await apiFetch(event, `/v1/projects/${projectId}/registries/github/start`, {
				method: 'POST',
				body: JSON.stringify({ connect_request_id: id })
			});
			target = payload != null && typeof payload === 'object' ? (payload as Record<string, unknown>).authorize_url : null;
		} catch (err) {
			return failure(err);
		}
		if (!isGitHubAuthorizeUrl(target)) return fail(502, { message: CONNECT_TEXT.unexpected, closed: false });
		event.cookies.set(GITHUB_RETURN_COOKIE, projectId, githubReturnCookieOptions(event.url));
		event.cookies.set(GITHUB_CONNECT_COOKIE, id, githubReturnCookieOptions(event.url));
		redirect(303, target);
	},

	/** Decline (item 7). */
	decline: async (event) => {
		requireUser(event);
		const id = event.params.id;
		if (!isConnectRequestId(id)) return fail(400, { message: CONNECT_TEXT.invalid, closed: true });
		try {
			await apiFetch(event, `${requestPath(id)}/decline`, { method: 'POST', body: '{}' });
			return { declined: true as const };
		} catch (err) {
			return failure(err);
		}
	}
};
