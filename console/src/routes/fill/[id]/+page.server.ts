import { fail, isHttpError, isRedirect } from '@sveltejs/kit';
import { ApiError, apiFetch } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { requestPage } from '$lib/server/request-page';
import { revealFailedText } from '$lib/env-generate';
import { FILL_TEXT, fillBody, isValueRequestId, parseFillRequest, withoutValue } from '$lib/fill-request';
import type { Actions, PageServerLoad } from './$types';

/**
 * The value-request fill page (api-v1 v1.28 items 4–6). Signed out, the hook sends the browser to sign-in
 * and back here. The API answers only the person who asked, in a console session.
 */

const page = requestPage({ prefix: '/v1/value-requests/', parse: parseFillRequest, texts: FILL_TEXT });

export const load: PageServerLoad = (event) => page.load(event);

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
			const payload = await apiFetch(event, `${page.path(id)}/fill`, { method: 'POST', body: JSON.stringify(parsed.body) });
			const record = payload && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
			if (record?.state !== 'filled') return fail(502, { message: FILL_TEXT.unexpected });
			return { saved: true as const, revealPending: record.reveal_pending === true };
		} catch (err) {
			return page.failure(err, (message) => withoutValue(message, value));
		}
	},

	/** Decline (item 6). */
	decline: (event) => page.decline(event),

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
		const loaded = await page.loadRequest(event, event.params.id);
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
