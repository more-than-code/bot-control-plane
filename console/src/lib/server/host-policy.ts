/**
 * Host policy (api-v1 v1.49): the Policy tab's read and its apply action. Both go to
 * `/v1/orgs/{org}/hosts/{host}/policy`, which only org owners and admins may call from the console.
 */
import { error, fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import type { Host } from '../api-types';
import {
	imagePatternProblem,
	isPolicyHash,
	parseHostPolicy,
	parsePolicyApply,
	projectProblem,
	type HostPolicyTab
} from '../host-policy';
import { apiFetch, ApiError, loadFailure } from './api';
import { requireUser } from './guard';
import { present } from './redact';
import { parseHost } from './shape';
import { isOrgAdmin } from './roles';
import { isUuid } from './validate';

/** The API waits up to 60 s for the host's helper to accept the change; this adds a margin. */
export const HOST_POLICY_APPLY_WAIT_MS = 80_000;

const UNEXPECTED = 'The API returned an unexpected response.';

function policyPath(orgId: string, hostId: string): string {
	return `/v1/orgs/${orgId}/hosts/${hostId}/policy`;
}

/** The tab's read, for org owners and admins only (null for anyone else). */
export async function loadHostPolicy(event: RequestEvent, host: Host): Promise<HostPolicyTab | null> {
	if (!host.org_id || !isOrgAdmin(event.locals.orgs, host.org_id)) return null;
	try {
		const policy = parseHostPolicy(await apiFetch(event, policyPath(host.org_id, host.id)));
		return policy ? { policy, message: null } : { policy: null, message: UNEXPECTED };
	} catch (err) {
		return { policy: null, message: loadFailure(err).message };
	}
}

function policyFailure(event: RequestEvent, status: number, message: string) {
	return fail(status, present(event, { policyMessage: message }));
}

/** POST: apply the staged change. The form carries base_hash, edge (1 or 0), and each image and project. */
export async function applyHostPolicy(event: RequestEvent) {
	requireUser(event);
	const hostId = event.params.id;
	if (!hostId || !isUuid(hostId)) error(404, 'Not found');
	const form = await event.request.formData();
	const baseHash = form.get('base_hash');
	const edge = form.get('edge');
	if (!isPolicyHash(baseHash) || (edge !== '1' && edge !== '0')) return policyFailure(event, 400, 'Reload the page and make the change again.');
	const images: string[] = [];
	for (const value of form.getAll('image')) {
		if (typeof value !== 'string' || imagePatternProblem(value, images)) return policyFailure(event, 400, 'An image pattern is not valid. Reload the page and make the change again.');
		images.push(value);
	}
	const projects: string[] = [];
	for (const value of form.getAll('project')) {
		if (typeof value !== 'string' || projectProblem(value, projects)) return policyFailure(event, 400, 'A project slug is not valid. Reload the page and make the change again.');
		projects.push(value);
	}
	let orgId: string | null = null;
	try {
		orgId = parseHost(await apiFetch(event, `/v1/hosts/${hostId}`))?.org_id ?? null;
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return policyFailure(event, err instanceof ApiError ? err.status : 503, err instanceof ApiError ? err.msg : 'The API is unavailable.');
	}
	if (!orgId || !isOrgAdmin(event.locals.orgs, orgId)) return policyFailure(event, 403, 'Only org owners and admins change a host policy.');
	try {
		const answer = await apiFetch(
			event,
			policyPath(orgId, hostId),
			{ method: 'POST', body: JSON.stringify({ base_hash: baseHash, edge: edge === '1', images, projects }) },
			HOST_POLICY_APPLY_WAIT_MS
		);
		const apply = parsePolicyApply(answer);
		if (!apply) return policyFailure(event, 502, UNEXPECTED);
		return present(event, { policyApply: apply });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError) return policyFailure(event, err.status >= 400 && err.status <= 599 ? err.status : 502, err.msg);
		console.error('bot api request failed');
		return policyFailure(event, 503, 'The API is unavailable.');
	}
}
