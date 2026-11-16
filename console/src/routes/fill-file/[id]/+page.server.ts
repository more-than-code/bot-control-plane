import { fail } from '@sveltejs/kit';
import { ApiError, apiFetch, loadFailure } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { requestPage } from '$lib/server/request-page';
import { FILE_FILL_TEXT, fileFillBody, isFileExistsMessage, isFileRequestId, parseFileFillRequest, postedContent } from '$lib/file-request';
import type { Actions, PageServerLoad } from './$types';

/**
 * The file-request fill page (api-v1 v1.30 items 4–6). Signed out, the hook sends the browser to
 * sign-in and back here. The API answers only the person who asked, in a console session.
 */

const page = requestPage({ prefix: '/v1/file-requests/', parse: parseFileFillRequest, texts: FILE_FILL_TEXT });

export const load: PageServerLoad = (event) => page.load(event);

/**
 * A failed save. The file-exists conflict leaves the request pending: the form goes, but `exists` keeps
 * Decline so the person can close the request. Other failures are the shared mapping. The API never
 * echoes content, so nothing is redacted here.
 */
function saveFailure(err: unknown) {
	if (err instanceof ApiError && err.code === 'conflict' && isFileExistsMessage(err.msg)) {
		return fail(409, { message: loadFailure(err).message, closed: false, exists: true });
	}
	return page.failure(err);
}

export const actions: Actions = {
	/** Fill (item 5). The content is posted once to the API and never returned. */
	save: async (event) => {
		requireUser(event);
		const id = event.params.id;
		if (!isFileRequestId(id)) return fail(400, { message: FILE_FILL_TEXT.invalid });
		const form = await event.request.formData();
		// A picked file comes byte-exact as content_b64; typed or pasted text has its CRLF made LF.
		const content = postedContent(formText(form, 'content_b64'), formText(form, 'content'));
		if (content == null) return fail(400, { message: FILE_FILL_TEXT.notText });
		const parsed = fileFillBody(content, formText(form, 'secret'));
		if (!parsed.ok) return fail(400, { message: parsed.message });
		try {
			const payload = await apiFetch(event, `${page.path(id)}/fill`, { method: 'POST', body: JSON.stringify(parsed.body) });
			const record = payload && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
			if (record?.state !== 'filled' || typeof record.secret !== 'boolean') return fail(502, { message: FILE_FILL_TEXT.unexpected });
			return { saved: true as const, secret: record.secret };
		} catch (err) {
			return saveFailure(err);
		}
	},

	/** Decline (item 6). Its form is empty: no content is ever posted with a decline. */
	decline: (event) => page.decline(event)
};
