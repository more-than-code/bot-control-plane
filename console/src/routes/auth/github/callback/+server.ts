import { error, isHttpError, isRedirect, redirect } from '@sveltejs/kit';
import { ApiError, apiFetch } from '$lib/server/api';
import { requireApi, requireUser } from '$lib/server/guard';
import { isRecord } from '$lib/server/shape';
import { GITHUB_RETURN_COOKIE, githubReturnCookieOptions } from '$lib/server/settings-auto';
import { isUuid } from '$lib/server/validate';
import { githubFailureCode, GITHUB_NOTES, type GitHubNoteCode } from '$lib/settings-auto';
import type { RequestHandler } from './$types';

const OAUTH_CODE = /^[\x21-\x7E]{1,512}$/;
const STATE = /^[A-Za-z0-9_-]{43}$/;

/**
 * GitHub returns here after Connect GitHub (api-v1 v1.29 item 3). The code and state go to the API, which checks the
 * state is this user's and unused; the browser then returns to the project's Settings → Registries with a short
 * `?github=` code. The token never reaches the console.
 */
export const GET: RequestHandler = async (event) => {
	requireApi();
	requireUser(event);
	const project = event.cookies.get(GITHUB_RETURN_COOKIE) ?? '';
	event.cookies.delete(GITHUB_RETURN_COOKIE, githubReturnCookieOptions(event.url));
	function back(code: GitHubNoteCode, id: string = project): never {
		if (!isUuid(id)) error(400, GITHUB_NOTES[code].text);
		redirect(303, `/projects/${id}?tab=settings&github=${code}#registries`);
	}

	const params = event.url.searchParams;
	if (params.get('error')) back('denied');
	const code = params.get('code') ?? '';
	const state = params.get('state') ?? '';
	if (!OAUTH_CODE.test(code) || !STATE.test(state)) back('expired');

	let payload: unknown;
	try {
		payload = await apiFetch(event, '/v1/auth/github/callback', { method: 'POST', body: JSON.stringify({ code, state }) });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError) back(githubFailureCode(err.code, err.msg));
		console.error('bot api request failed');
		back('failed');
	}
	const projectId = isRecord(payload) && typeof payload.project_id === 'string' ? payload.project_id : '';
	back('connected', isUuid(projectId) ? projectId : project);
};
