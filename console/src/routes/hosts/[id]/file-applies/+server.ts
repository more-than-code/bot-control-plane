import { json } from '@sveltejs/kit';
import { isOrgAdmin } from '$lib/server/roles';
import { hostFileRuns } from '$lib/server/host-files';
import { isUuid } from '$lib/server/validate';
import type { RequestHandler } from './$types';

/**
 * The Config files tab's poll while an apply runs (api-v1 v1.44): the applies this console process tracks for
 * the host, `?org=<org id>`, for org owners and admins of that org. No API call; never cached.
 */
const NO_STORE = { 'cache-control': 'no-store' };

export const GET: RequestHandler = async (event) => {
	if (event.locals.user == null) return json({ message: 'Sign in required.' }, { status: 401, headers: NO_STORE });
	const hostId = event.params.id ?? '';
	const orgId = event.url.searchParams.get('org') ?? '';
	if (!isUuid(hostId) || !isUuid(orgId)) return json({ message: 'Not found.' }, { status: 404, headers: NO_STORE });
	if (!isOrgAdmin(event.locals.orgs, orgId)) return json({ message: 'Forbidden.' }, { status: 403, headers: NO_STORE });
	return json({ runs: hostFileRuns(orgId, hostId) }, { headers: NO_STORE });
};
