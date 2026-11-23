import { json, type RequestEvent } from '@sveltejs/kit';
import { loadAgentUpgrades, startAgentUpgrade } from '$lib/server/agent-upgrade';
import { isUuid } from '$lib/server/validate';
import type { RequestHandler } from './$types';

/**
 * The "Update available" badge's Upgrade (api-v1 v1.48): POST starts an upgrade (`{"version"}`, the latest the
 * host read named), GET returns the host's upgrade history for the poll while it runs. The API decides who may
 * upgrade. Signed-in callers only, same origin, never cached.
 */
const NO_STORE = { 'cache-control': 'no-store' };
const VERSION = /^\d{1,4}\.\d{1,4}\.\d{1,4}$/;

function answer(body: unknown, status = 200): Response {
	return json(body, { status, headers: NO_STORE });
}

function hostId(event: RequestEvent): string | null {
	const id = event.params.id ?? '';
	return isUuid(id) ? id : null;
}

export const GET: RequestHandler = async (event) => {
	if (event.locals.user == null) return answer({ message: 'Sign in required.' }, 401);
	const id = hostId(event);
	if (!id) return answer({ message: 'Not found.' }, 404);
	try {
		return answer(await loadAgentUpgrades(event, id));
	} catch {
		return answer({ message: 'Sign in required.' }, 401);
	}
};

export const POST: RequestHandler = async (event) => {
	if (event.locals.user == null) return answer({ message: 'Sign in required.' }, 401);
	// A JSON POST is outside SvelteKit's form CSRF check, so the origin is checked here.
	if (event.request.headers.get('origin') !== event.url.origin) return answer({ message: 'Forbidden.' }, 403);
	if (!(event.request.headers.get('content-type') ?? '').startsWith('application/json')) return answer({ message: 'Send JSON.' }, 415);
	const id = hostId(event);
	if (!id) return answer({ message: 'Not found.' }, 404);
	let raw: unknown;
	try {
		raw = await event.request.json();
	} catch {
		return answer({ message: 'Send the version.' }, 400);
	}
	const version = typeof raw === 'object' && raw !== null && 'version' in raw ? (raw as { version: unknown }).version : null;
	if (typeof version !== 'string' || !VERSION.test(version)) return answer({ message: 'Send the version.' }, 400);
	try {
		const result = await startAgentUpgrade(event, id, version);
		if (!result.ok) return answer({ message: result.message }, result.status);
		return answer({ upgrade_id: result.upgradeId, state: 'running' }, 202);
	} catch {
		// apiFetch's sign-in redirect: this is a fetch, not a page.
		return answer({ message: 'Sign in required.' }, 401);
	}
};
