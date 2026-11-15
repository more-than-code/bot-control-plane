import { fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import { ApiError, apiFetch, loadFailure } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { revealFailedText } from '$lib/env-generate';
import { FILL_TEXT, fillBody, fillView, isValueRequestId, parseFillRequest, withoutValue, type FillRequest, type FillView } from '$lib/fill-request';
import type { Actions, PageServerLoad } from './$types';

/**
 * The value-request fill page (api-v1 v1.28 items 4–6). Signed out, the hook sends the browser to sign-in
 * and back here. The API answers only the person who asked, in a console session.
 */

function requestPath(id: string): string {
	return `/v1/value-requests/${encodeURIComponent(id)}`;
}

type Loaded = { view: FillView; request: FillRequest | null; message: string | null };

async function loadRequest(event: RequestEvent, id: string): Promise<Loaded> {
	if (!isValueRequestId(id)) return { view: 'invalid', request: null, message: null };
	try {
		const request = parseFillRequest(await apiFetch(event, `${requestPath(id)}/fill`), id);
		if (!request) return { view: 'error', request: null, message: FILL_TEXT.unexpected };
		return { view: fillView(request.state), request, message: null };
	} catch (err) {
		if (err instanceof ApiError && err.code === 'not_found') return { view: 'missing', request: null, message: null };
		// The API could not be read: only its message, not the "belongs to the account that asked" text.
		return { view: 'error', request: null, message: loadFailure(err).message };
	}
}

export const load: PageServerLoad = async (event) => {
	requireUser(event);
	return present(event, await loadRequest(event, event.params.id));
};

/**
 * A failed fill or decline. `conflict` (used, expired, key has a value) carries the API's own text and
 * `not_found` the missing text; both mean the request can no longer be filled here, so `closed` hides the form.
 * Anything else (a bad value, the API unreachable) keeps the form for another try.
 */
function failure(err: unknown, value: string) {
	if (isRedirect(err) || isHttpError(err)) throw err;
	if (err instanceof ApiError && err.code === 'not_found') return fail(404, { message: FILL_TEXT.missing, closed: true });
	const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
	const closed = err instanceof ApiError && err.code === 'conflict';
	return fail(status, { message: withoutValue(loadFailure(err).message, value), closed });
}

export const actions: Actions = {
	/** Fill (item 5). The typed value is posted once to the API and never returned. */
	save: async (event) => {
		requireUser(event);
		const id = event.params.id;
		if (!isValueRequestId(id)) return fail(400, { message: FILL_TEXT.invalid });
		const form = await event.request.formData();
		const value = formText(form, 'value');
		const parsed = fillBody(formText(form, 'source'), value, formText(form, 'kind'));
		if (!parsed.ok) return fail(400, { message: parsed.message });
		try {
			const payload = await apiFetch(event, `${requestPath(id)}/fill`, { method: 'POST', body: JSON.stringify(parsed.body) });
			const record = payload && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
			if (record?.state !== 'filled') return fail(502, { message: FILL_TEXT.unexpected });
			return { saved: true as const, revealPending: record.reveal_pending === true };
		} catch (err) {
			return failure(err, value);
		}
	},

	/** Decline (item 6). */
	decline: async (event) => {
		requireUser(event);
		const id = event.params.id;
		if (!isValueRequestId(id)) return fail(400, { message: FILL_TEXT.invalid });
		try {
			await apiFetch(event, `${requestPath(id)}/decline`, { method: 'POST', body: '{}' });
			return { declined: true as const };
		} catch (err) {
			return failure(err, '');
		}
	},

	/**
	 * Show a generated value once (v1.27 item 3), for the reveal dialog's own fetch only, as the project
	 * page's revealEnvKey. The project, set and key come from the API, not the form.
	 */
	reveal: async (event) => {
		requireUser(event);
		const form = await event.request.formData();
		const key = formText(form, 'key');
		const accept = event.request.headers.get('accept') ?? '';
		if (event.request.headers.get('x-sveltekit-action') !== 'true' || !accept.includes('application/json')) {
			return fail(400, { revealError: 'Showing a value needs JavaScript.' });
		}
		const loaded = await loadRequest(event, event.params.id);
		const request = loaded.request;
		if (!request || request.state !== 'filled' || request.key !== key) return fail(404, { revealError: revealFailedText(key) });
		const path = `/v1/projects/${request.project_id}/env-sets/${encodeURIComponent(request.set)}/keys/${encodeURIComponent(request.key)}/reveal`;
		try {
			const payload = await apiFetch(event, path, { method: 'POST', body: '{}' });
			const item = payload != null && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
			if (!item || item.key !== key || typeof item.value !== 'string') return fail(502, { revealError: revealFailedText(key) });
			return { revealed: { key, value: item.value } };
		} catch (err) {
			if (isRedirect(err) || isHttpError(err)) throw err;
			// A conflict (already shown) names the key only; anything else may have used up the one view.
			const answered = err instanceof ApiError && err.code === 'conflict';
			return fail(err instanceof ApiError ? err.status : 503, { revealError: answered ? err.msg : revealFailedText(key) });
		}
	}
};
