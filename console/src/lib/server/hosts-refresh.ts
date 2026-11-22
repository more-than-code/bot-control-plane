import { isHttpError, isRedirect, json, type RequestEvent } from '@sveltejs/kit';
import { ApiError, apiList } from './api';
import { parseHost } from './shape';
import { isUuid } from './validate';

/**
 * The Hosts tabs' background re-read (`$lib/hosts-refresh`): one API host list as `{ hosts }`, never cached.
 * A failure is a JSON status with a fixed message and no API detail, so the tab keeps its last list.
 */
const NO_STORE = { 'cache-control': 'no-store' };

function answer(body: unknown, status = 200): Response {
	return json(body, { status, headers: NO_STORE });
}

/** `path(id)` is the API list the page load reads (`/v1/projects/{id}/hosts` or `/v1/orgs/{id}/hosts`). */
export async function hostsResponse(event: RequestEvent, path: (id: string) => string): Promise<Response> {
	if (event.locals.user == null) return answer({ message: 'Sign in required.' }, 401);
	const id = event.params.id ?? '';
	if (!isUuid(id)) return answer({ message: 'Not found.' }, 404);
	try {
		const list = await apiList(event, path(id), parseHost);
		return answer({ hosts: list.items });
	} catch (err) {
		// A 401 from the API (apiFetch's sign-in redirect) is a JSON 401 here: this is a fetch, not a page.
		if (isRedirect(err)) return answer({ message: 'Sign in required.' }, 401);
		if (isHttpError(err)) throw err;
		const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
		return answer({ message: 'Hosts could not be loaded.' }, status);
	}
}
