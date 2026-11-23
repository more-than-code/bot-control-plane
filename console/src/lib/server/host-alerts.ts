import { fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import type { HostAlertSettings, HostDisk } from '$lib/api-types';
import { ALERT_FIELDS, alertFieldValid, alertSettingsFromForm, DEFAULT_ALERT_SETTINGS } from '$lib/host-alerts';
import { ApiError, apiFetch, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isRecord, parseListPayload } from '$lib/server/shape';
import { isUuid } from '$lib/server/validate';
import { backTo } from '$lib/url';

/** Host resource alerts (api-v1 v1.49): settings, mounts and the save action. Allowlist parsers. */

const CONTROL = /[\u0000-\u001F\u007F]/;
const DISK_CAP = 50;

export function parseAlertSettings(value: unknown): HostAlertSettings | null {
	if (!isRecord(value) || typeof value.alerts_enabled !== 'boolean') return null;
	const out: HostAlertSettings = { ...DEFAULT_ALERT_SETTINGS, alerts_enabled: value.alerts_enabled };
	for (const field of ALERT_FIELDS) {
		const raw = value[field.name] ?? null;
		if (!alertFieldValid(field, raw)) return null;
		out[field.name] = raw;
	}
	return out;
}

/** `GET /v1/hosts/{id}/disks`: mounts that parse, at most 50, sorted by the API. Null when not a list. */
export function parseHostDisks(payload: unknown): HostDisk[] | null {
	const page = parseListPayload(payload);
	if (!page) return null;
	const out: HostDisk[] = [];
	for (const item of page.raw.slice(0, DISK_CAP)) {
		if (!isRecord(item) || typeof item.path !== 'string' || typeof item.fstype !== 'string') continue;
		if (item.path === '' || item.path.length > 1024 || CONTROL.test(item.path) || item.fstype.length > 64 || CONTROL.test(item.fstype)) continue;
		out.push({ path: item.path, fstype: item.fstype });
	}
	return out;
}

export async function loadAlertSettings(event: RequestEvent, hostId: string): Promise<{ alerts: HostAlertSettings | null; alertsMessage: string | null }> {
	try {
		const alerts = parseAlertSettings(await apiFetch(event, `/v1/hosts/${hostId}/alert-settings`));
		return alerts ? { alerts, alertsMessage: null } : { alerts: null, alertsMessage: 'The API returned an unexpected response.' };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { alerts: null, alertsMessage: loadFailure(err).message };
	}
}

/** The host's mounts for the disk chart picker. A failure leaves the picker out (the chart shows /). */
export async function loadHostDisks(event: RequestEvent, hostId: string): Promise<HostDisk[]> {
	try {
		return parseHostDisks(await apiFetch(event, `/v1/hosts/${hostId}/disks`)) ?? [];
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return [];
	}
}

/** The Alerts card's Save (org owners and admins; the API decides). */
export async function saveAlerts(event: RequestEvent) {
	requireUser(event);
	const hostId = event.params.id;
	if (!hostId || !isUuid(hostId)) return fail(404, present(event, { alertsMessage: 'Not found.' }));
	const built = alertSettingsFromForm(await event.request.formData());
	if (!built.ok) return fail(400, present(event, { alertsMessage: built.message }));
	try {
		await apiFetch(event, `/v1/hosts/${hostId}/alert-settings`, { method: 'PUT', body: JSON.stringify(built.settings) });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		const status = err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
		const message = err instanceof ApiError ? err.msg : 'The API is unavailable.';
		return fail(status, present(event, { alertsMessage: message }));
	}
	redirect(303, backTo(event.url));
}
