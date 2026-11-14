import type { ActionResult } from '@sveltejs/kit';
import { sameOriginPath } from '$lib/log-view';
import type { CatalogPermissionsState } from '$lib/project-action';

export const PERMISSIONS_FAILED = 'Permissions could not be read. Close the drawer and try again.';

export type PermissionsReadDeps = {
	fetch: typeof fetch;
	deserialize: (text: string) => ActionResult;
	/** The `catalogPermissionsRead` action URL. */
	action: string;
	/** `location.origin`, for a redirect (an expired session goes to sign-in). */
	origin: string;
};

export type PermissionsRead = { kind: 'state'; state: CatalogPermissionsState } | { kind: 'redirect'; path: string };

function failed(name: string, message: string): PermissionsRead {
	return { kind: 'state', state: { name, grants: [], message, loaded: false } };
}

/**
 * Reads one entry's grants for the Permissions drawer by posting the `catalogPermissionsRead` action with `fetch`.
 * A plain action request runs only the action: the page load (catalog, copies, container states) does not re-run,
 * and nothing navigates. The server calls the API; the browser never does. Never rejects.
 */
export async function readPermissions(deps: PermissionsReadDeps, name: string, signal?: AbortSignal): Promise<PermissionsRead> {
	try {
		const body = new FormData();
		body.set('name', name);
		const response = await deps.fetch(deps.action, {
			method: 'POST',
			body,
			headers: { accept: 'application/json', 'x-sveltekit-action': 'true' },
			signal
		});
		const result = deps.deserialize(await response.text());
		if (result.type === 'redirect') {
			const path = sameOriginPath(result.location, deps.origin);
			return path == null ? failed(name, PERMISSIONS_FAILED) : { kind: 'redirect', path };
		}
		if (result.type === 'error') return failed(name, PERMISSIONS_FAILED);
		const state = (result.data as { permissions?: CatalogPermissionsState } | undefined)?.permissions;
		if (!state || !Array.isArray(state.grants)) return failed(name, PERMISSIONS_FAILED);
		// Only a success carries grants to edit; a refusal (a non-owner, a missing entry) is a message with no Save.
		if (result.type === 'success') return { kind: 'state', state: { name, grants: state.grants, message: null, loaded: true } };
		return failed(name, state.message || PERMISSIONS_FAILED);
	} catch {
		return failed(name, PERMISSIONS_FAILED);
	}
}

/** What the drawer does with the save action's redirect. */
export type SaveRedirect = { kind: 'saved' } | { kind: 'navigate'; path: string } | { kind: 'update' };

/**
 * A successful save redirects back to this page (`backTo`): the drawer then closes without following it, since the
 * grants show only in the drawer. Any other path on this site (an expired session going to sign-in) is followed.
 * A location off this site is left to SvelteKit's own handling (`update()`).
 */
export function saveRedirectOutcome(location: string, page: URL): SaveRedirect {
	const path = sameOriginPath(location, page.origin);
	if (path == null) return { kind: 'update' };
	if (new URL(path, page.origin).pathname === page.pathname) return { kind: 'saved' };
	return { kind: 'navigate', path };
}
