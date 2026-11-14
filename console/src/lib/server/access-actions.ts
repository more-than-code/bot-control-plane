import { fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import type { AccessLevel } from '$lib/api-types';
import type { AccessActionData } from '$lib/host-access';
import { ApiError, actionFailure, apiFetch } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { cleanExpiry, cleanHint, isUuid } from '$lib/server/validate';
import { backTo } from '$lib/url';

/**
 * Host access and credential writes and the join-token body (api-v1 v1.24 item 3), shared by the host page,
 * the org page and the project Hosts tab ("Leave"). Every access form posts `host_id` and `project_id`; the API
 * decides who may do it.
 */
export function cleanAccessLevel(value: string): AccessLevel | null {
	return value === 'metrics' || value === 'deploy' ? value : null;
}

function refused(event: RequestEvent, scope: string, hostId: string, message: string, status = 400) {
	const data: AccessActionData = { scope, message, accessHostId: hostId, accessConflict: null };
	return fail(status, present(event, data));
}

function failed(event: RequestEvent, scope: string, hostId: string, err: unknown) {
	if (isRedirect(err) || isHttpError(err)) throw err;
	if (!(err instanceof ApiError)) return actionFailure(event, err, scope);
	const status = err.status >= 400 && err.status <= 599 ? err.status : 503;
	return refused(event, scope, hostId, err.msg, status);
}

/** Grant or change a project's level on a host: one upsert (`PUT …/access/{project_id}`). Org admin+. */
export function setAccessAction(scope: string) {
	return async (event: RequestEvent) => {
		requireUser(event);
		const form = await event.request.formData();
		const hostId = formText(form, 'host_id');
		const projectId = formText(form, 'project_id');
		const level = cleanAccessLevel(formText(form, 'level'));
		if (!isUuid(hostId)) return refused(event, scope, '', 'That host is not valid.');
		if (!isUuid(projectId)) return refused(event, scope, hostId, 'Choose a project.');
		if (!level) return refused(event, scope, hostId, 'Choose metrics or deploy.');
		try {
			await apiFetch(event, `/v1/hosts/${hostId}/access/${projectId}`, { method: 'PUT', body: JSON.stringify({ level }) });
		} catch (err) {
			return failed(event, scope, hostId, err);
		}
		redirect(303, backTo(event.url));
	};
}

/**
 * Remove a project's access (org admin+), or leave a host (admin+ of that project). A 409 while the project
 * still has services there comes back as `accessConflict`; only the second confirm sends `reassign=org`. That
 * second confirm is UX only: the API is the authority on who may reassign.
 *
 * `pinToPage`: the project page's Leave, where the posted project must be the page's own project (`params.id`).
 */
export function removeAccessAction(scope: string, options: { pinToPage?: boolean } = {}) {
	return async (event: RequestEvent) => {
		requireUser(event);
		const form = await event.request.formData();
		const hostId = formText(form, 'host_id');
		const projectId = formText(form, 'project_id');
		const reassign = formText(form, 'reassign') === '1';
		if (!isUuid(hostId)) return refused(event, scope, '', 'That host is not valid.');
		if (!isUuid(projectId)) return refused(event, scope, hostId, 'That project is not valid.');
		if (options.pinToPage && projectId !== event.params.id) return refused(event, scope, hostId, 'That project is not valid.');
		const query = reassign ? '?reassign=org' : '';
		try {
			await apiFetch(event, `/v1/hosts/${hostId}/access/${projectId}${query}`, { method: 'DELETE' });
		} catch (err) {
			if (!reassign && err instanceof ApiError && err.code === 'conflict') {
				const data: AccessActionData = { scope, message: err.msg, accessHostId: hostId, accessConflict: { hostId, projectId } };
				return fail(409, present(event, data));
			}
			return failed(event, scope, hostId, err);
		}
		redirect(303, backTo(event.url));
	};
}

/** Revoke a host's credential (org admin+). The agent connection closes for every project that uses it. */
export function revokeCredentialAction(scope: string) {
	return async (event: RequestEvent) => {
		requireUser(event);
		const hostId = formText(await event.request.formData(), 'host_id');
		if (!isUuid(hostId)) return fail(400, present(event, { scope, message: 'That host is not valid.' }));
		try {
			await apiFetch(event, `/v1/hosts/${hostId}/credential`, { method: 'DELETE' });
		} catch (err) {
			return actionFailure(event, err, scope);
		}
		redirect(303, backTo(event.url));
	};
}

export type JoinGrantBody = { project_id: string; level: AccessLevel };
export type JoinTokenBody = { host_name_hint?: string; expires_in_sec?: number; grant?: JoinGrantBody };

/** `POST /v1/orgs/{id}/join-tokens` body from the Add a host form: the optional hint and expiry, and `grant` when given. */
export function buildJoinTokenBody(form: FormData, grant: JoinGrantBody | null): { ok: true; body: JoinTokenBody } | { ok: false; message: string } {
	const hintText = formText(form, 'host_name_hint');
	const hint = hintText.trim() ? cleanHint(hintText) : null;
	if (hintText.trim() && !hint) return { ok: false, message: 'Host name hint must be 1–80 characters.' };
	const expiry = cleanExpiry(formText(form, 'expires_in_sec'));
	if (!expiry.ok) return { ok: false, message: 'Expiry must be between 300 and 604800 seconds, or blank.' };
	const body: JoinTokenBody = {};
	if (grant) body.grant = grant;
	if (hint) body.host_name_hint = hint;
	if (expiry.seconds != null) body.expires_in_sec = expiry.seconds;
	return { ok: true, body };
}
