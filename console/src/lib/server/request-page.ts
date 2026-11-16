import { fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import { ApiError, apiFetch, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isRequestId, requestView, type RequestState, type RequestView } from '$lib/request-shape';

/**
 * The server side every request fill page shares (`/fill/[id]`, `/fill-file/[id]`): load the page
 * data, map a failed answer or decline, and decline. Each page keeps its own save action.
 */

export type RequestPageTexts = {
	/** Not found: another user's request, or the creator is no longer owner or admin. */
	missing: string;
	invalid: string;
	unexpected: string;
};

export type LoadedRequest<R> = { view: RequestView; request: R | null; message: string | null };

export function requestPage<R extends { state: RequestState }>(options: {
	/** The API path of a request, before the id: `/v1/value-requests/`. */
	prefix: string;
	parse: (payload: unknown, id: string) => R | null;
	texts: RequestPageTexts;
}) {
	const { prefix, parse, texts } = options;

	function path(id: string): string {
		return `${prefix}${encodeURIComponent(id)}`;
	}

	async function loadRequest(event: RequestEvent, id: string): Promise<LoadedRequest<R>> {
		if (!isRequestId(id)) return { view: 'invalid', request: null, message: null };
		try {
			const request = parse(await apiFetch(event, `${path(id)}/fill`), id);
			if (!request) return { view: 'error', request: null, message: texts.unexpected };
			return { view: requestView(request.state), request, message: null };
		} catch (err) {
			if (err instanceof ApiError && err.code === 'not_found') return { view: 'missing', request: null, message: null };
			// The API could not be read: only its message, not the "belongs to the account that asked" text.
			return { view: 'error', request: null, message: loadFailure(err).message };
		}
	}

	/**
	 * A failed answer or decline. `conflict` (used, expired, the subject taken meanwhile) carries the API's
	 * own text and `not_found` the missing text; both mean the request can no longer be answered here, so
	 * `closed` hides the form. Anything else (a bad body, the API unreachable) keeps the form for another
	 * try. `redact` is typed input to replace in the message, should it ever come back.
	 */
	function failure(err: unknown, redact: (message: string) => string = (message) => message) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError && err.code === 'not_found') return fail(404, { message: texts.missing, closed: true });
		const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
		const closed = err instanceof ApiError && err.code === 'conflict';
		return fail(status, { message: redact(loadFailure(err).message), closed });
	}

	return {
		path,
		loadRequest,
		failure,

		load: async (event: RequestEvent & { params: { id: string } }) => {
			requireUser(event);
			return present(event, await loadRequest(event, event.params.id));
		},

		/** Decline. Its form is empty: nothing typed is ever posted with a decline. */
		decline: async (event: RequestEvent & { params: { id: string } }) => {
			requireUser(event);
			const id = event.params.id;
			if (!isRequestId(id)) return fail(400, { message: texts.invalid });
			try {
				await apiFetch(event, `${path(id)}/decline`, { method: 'POST', body: '{}' });
				return { declined: true as const };
			} catch (err) {
				return failure(err);
			}
		}
	};
}
