import { fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import { DOCKER_USAGE_WAIT_MS, parseDockerUsage } from '$lib/disk-usage';
import { ApiError, apiFetch } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isUuid } from '$lib/server/validate';

/** The API's host_offline text is "host unavailable"; the card says what it means here. */
export const HOST_OFFLINE_MESSAGE = 'This host is offline; Docker usage needs it online.';

/**
 * The Disk usage card's "Check Docker usage" (api-v1 v1.59 item 2): posts docker-usage with a 55 s wait. The API
 * decides who may (org owners and admins); its fixed message goes into the card's Notice.
 */
export async function checkDockerUsage(event: RequestEvent) {
	requireUser(event);
	const hostId = event.params.id;
	if (!hostId || !isUuid(hostId)) return fail(404, present(event, { dockerUsageMessage: 'Not found.' }));
	try {
		const payload = await apiFetch(event, `/v1/hosts/${hostId}/docker-usage`, { method: 'POST' }, DOCKER_USAGE_WAIT_MS);
		const dockerUsage = parseDockerUsage(payload);
		if (!dockerUsage) return fail(502, present(event, { dockerUsageMessage: 'The API returned an unexpected response.' }));
		return present(event, { dockerUsage });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
		const message = err instanceof ApiError ? (err.code === 'host_offline' ? HOST_OFFLINE_MESSAGE : err.msg) : 'The API is unavailable.';
		return fail(status, present(event, { dockerUsageMessage: message }));
	}
}
