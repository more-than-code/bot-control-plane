import { apiFetch, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { OPEN_REQUEST_TEXT, parseOpenRequests, type OpenRequest } from '$lib/open-requests';
import type { PageServerLoad } from './$types';

/**
 * Open requests (api-v1 v1.33 item 8): the signed-in user's pending value, file and connect requests, each linking to
 * its fill page by handle. Names only: no reason, value or URL comes from the API.
 */
export const load: PageServerLoad = async (event) => {
	requireUser(event);
	try {
		const items = parseOpenRequests(await apiFetch(event, '/v1/me/requests'));
		if (!items) return present(event, { requests: [] as OpenRequest[], message: OPEN_REQUEST_TEXT.unexpected as string | null });
		return present(event, { requests: items, message: null as string | null });
	} catch (err) {
		return present(event, { requests: [] as OpenRequest[], message: loadFailure(err).message as string | null });
	}
};
