import { randomUUID } from 'node:crypto';
import { error, fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import type { DomainPattern, EdgeRoute } from '$lib/api-types';
import { isCatalogName, isIdempotencyKey } from '$lib/catalog';
import { ASSIGNMENTS_MAX, cleanDomainName, isDomainPattern, routesFromField, type DomainPlan } from '$lib/edge-routes';
import { isConfirmationId } from '$lib/image-spec';
import { ApiError, apiFetch } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isUuid } from '$lib/server/validate';
import { backTo } from '$lib/url';
import { isDomainId, parseEdgePlan, parseRouteList } from './edge-shape';

/**
 * Form actions for api-v1 v1.35: org domains (org Settings), the host edge (host page) and an entry's routes (the
 * entry's Edit drawer). Every write goes through the API, which checks roles, assignments, ports and conflicts; a
 * refusal's text is shown as is.
 */

const UNEXPECTED = 'The API returned an unexpected response.';

function uuidParam(params: { id?: string }): string {
	if (!params.id || !isUuid(params.id)) error(404, 'Not found');
	return params.id;
}

function apiStatus(err: unknown): number {
	return err instanceof ApiError && err.status >= 400 && err.status <= 599 ? err.status : 503;
}

function apiMessage(err: unknown): string {
	if (err instanceof ApiError) return err.msg;
	console.error('bot api request failed');
	return 'The API is unavailable.';
}

function rethrow(err: unknown): void {
	if (isRedirect(err) || isHttpError(err)) throw err;
}

// ---- Org domains (org owner or admin, console session only) ----

type DomainField = 'addDomain' | 'domain' | 'assignments';

function domainFailure(event: RequestEvent, status: number, message: string, field: DomainField, domainId: string | null = null) {
	return fail(status, present(event, { scope: 'settings' as const, field, domainId, message }));
}

export async function addDomain(event: RequestEvent) {
	requireUser(event);
	const orgId = uuidParam(event.params);
	const name = cleanDomainName(formText(await event.request.formData(), 'name'));
	if (!name) return domainFailure(event, 400, 'Enter a domain name such as example.com, not an IP address; use the xn-- (punycode) form for non-ASCII names.', 'addDomain');
	try {
		await apiFetch(event, `/v1/orgs/${orgId}/domains`, { method: 'POST', body: JSON.stringify({ name }) });
	} catch (err) {
		rethrow(err);
		return domainFailure(event, apiStatus(err), apiMessage(err), 'addDomain');
	}
	redirect(303, backTo(event.url));
}

export async function verifyDomain(event: RequestEvent) {
	requireUser(event);
	const orgId = uuidParam(event.params);
	const domainId = formText(await event.request.formData(), 'domain_id');
	if (!isDomainId(domainId)) return domainFailure(event, 400, 'That domain is not valid.', 'domain');
	try {
		await apiFetch(event, `/v1/orgs/${orgId}/domains/${domainId}/verify`, { method: 'POST', body: '{}' });
	} catch (err) {
		rethrow(err);
		return domainFailure(event, apiStatus(err), verifyFailureText(err), 'domain', domainId);
	}
	// No redirect: the row learns the outcome at once and the page re-reads the list in the background, so the
	// Verify button never waits on that reload (the stuck "Verifying…" of the bug).
	return present(event, { scope: 'settings' as const, field: 'domainVerified' as const, domainId, message: null });
}

/** The API's "TXT record not found yet: add … = …", followed by when it was looked up and that DNS can lag. */
export const TXT_NOT_FOUND_HINT = 'Deploybot looked up that TXT record just now and did not find it. A new DNS record can take a few minutes to show up; try Verify again then.';

function verifyFailureText(err: unknown): string {
	const text = apiMessage(err);
	if (err instanceof ApiError && err.code === 'conflict' && text.startsWith('TXT record not found yet')) return `${text.replace(/\.?$/, '.')} ${TXT_NOT_FOUND_HINT}`;
	return text;
}

/** `project_id` and `pattern` come as parallel lists, one pair per row. */
export function assignmentsFromForm(form: FormData): { ok: true; assignments: Array<{ project_id: string; pattern: DomainPattern }> } | { ok: false; message: string } {
	const projects = form.getAll('project_id');
	const patterns = form.getAll('pattern');
	if (projects.length !== patterns.length || projects.length > ASSIGNMENTS_MAX) return { ok: false, message: 'The assignments are not valid.' };
	const assignments: Array<{ project_id: string; pattern: DomainPattern }> = [];
	const seen = new Set<string>();
	for (const [index, project] of projects.entries()) {
		const pattern = patterns[index];
		if (typeof project !== 'string' || !isUuid(project)) return { ok: false, message: `Row ${index + 1}: choose a project.` };
		if (!isDomainPattern(pattern)) return { ok: false, message: `Row ${index + 1}: choose the domain itself or its subdomains.` };
		const pair = `${project} ${pattern}`;
		if (seen.has(pair)) continue;
		seen.add(pair);
		assignments.push({ project_id: project, pattern });
	}
	return { ok: true, assignments };
}

export async function setDomainAssignments(event: RequestEvent) {
	requireUser(event);
	const orgId = uuidParam(event.params);
	const form = await event.request.formData();
	const domainId = formText(form, 'domain_id');
	if (!isDomainId(domainId)) return domainFailure(event, 400, 'That domain is not valid.', 'assignments');
	const built = assignmentsFromForm(form);
	if (!built.ok) return domainFailure(event, 400, built.message, 'assignments', domainId);
	try {
		await apiFetch(event, `/v1/orgs/${orgId}/domains/${domainId}/assignments`, {
			method: 'PUT',
			body: JSON.stringify({ assignments: built.assignments })
		});
	} catch (err) {
		rethrow(err);
		return domainFailure(event, apiStatus(err), apiMessage(err), 'assignments', domainId);
	}
	redirect(303, backTo(event.url));
}

/** Delete asks the API for a plan naming every route that would stop (202); nothing is removed until Confirm. */
export async function deleteDomain(event: RequestEvent) {
	requireUser(event);
	const orgId = uuidParam(event.params);
	const domainId = formText(await event.request.formData(), 'domain_id');
	if (!isDomainId(domainId)) return domainFailure(event, 400, 'That domain is not valid.', 'domain');
	// The API binds the plan to this key: the DELETE carries it and the confirm must send it again.
	const key = randomUUID();
	try {
		const plan = parseEdgePlan(
			await apiFetch(event, `/v1/orgs/${orgId}/domains/${domainId}`, { method: 'DELETE', body: JSON.stringify({ idempotency_key: key }) })
		);
		if (!plan) return domainFailure(event, 502, UNEXPECTED, 'domain', domainId);
		const domainPlan: DomainPlan = { ...plan, domainId, idempotency_key: key };
		return present(event, { scope: 'settings' as const, domainPlan });
	} catch (err) {
		rethrow(err);
		return domainFailure(event, apiStatus(err), apiMessage(err), 'domain', domainId);
	}
}

export async function confirmDomainDelete(event: RequestEvent) {
	requireUser(event);
	uuidParam(event.params);
	const form = await event.request.formData();
	const domainId = formText(form, 'domain_id');
	const confirmationId = formText(form, 'confirmation_id');
	const key = formText(form, 'idempotency_key');
	if (!isDomainId(domainId) || !isConfirmationId(confirmationId) || !isIdempotencyKey(key)) {
		return domainFailure(event, 400, 'That confirmation is not valid.', 'domain', isDomainId(domainId) ? domainId : null);
	}
	try {
		await apiFetch(event, `/v1/confirmations/${confirmationId}`, { method: 'POST', body: JSON.stringify({ idempotency_key: key }) });
	} catch (err) {
		rethrow(err);
		return domainFailure(event, apiStatus(err), apiMessage(err), 'domain', domainId);
	}
	redirect(303, backTo(event.url));
}

// ---- Host edge (owner or admin of the host's org, console session only) ----

export async function setEdge(event: RequestEvent) {
	requireUser(event);
	const hostId = uuidParam(event.params);
	const value = formText(await event.request.formData(), 'enabled');
	if (value !== '1' && value !== '0') return fail(400, present(event, { edgeMessage: 'Choose on or off.' }));
	try {
		await apiFetch(event, `/v1/hosts/${hostId}/edge`, { method: 'PUT', body: JSON.stringify({ enabled: value === '1' }) });
	} catch (err) {
		rethrow(err);
		// The runner.toml and port-occupant refusals are the API's own text, shown as is.
		return fail(apiStatus(err), present(event, { edgeMessage: apiMessage(err) }));
	}
	redirect(303, backTo(event.url));
}

// ---- Entry routes (project owner or admin) ----

function routesFailure(event: RequestEvent, status: number, message: string) {
	return fail(status, present(event, { scope: 'catalog' as const, routesMessage: message }));
}

/** An entry's routes, for the Public addresses section of its Edit drawer. */
export async function catalogRoutes(event: RequestEvent) {
	requireUser(event);
	const id = uuidParam(event.params);
	const entry = formText(await event.request.formData(), 'entry');
	if (!isCatalogName(entry)) return routesFailure(event, 400, 'That entry is not valid.');
	try {
		const list = parseRouteList(await apiFetch(event, `/v1/projects/${id}/catalog/${entry}/routes`));
		if (!list) return routesFailure(event, 502, UNEXPECTED);
		return present(event, { scope: 'catalog' as const, routes: list.items as EdgeRoute[], routesOmitted: list.omitted });
	} catch (err) {
		rethrow(err);
		return routesFailure(event, apiStatus(err), apiMessage(err));
	}
}

/** Replace an entry's routes: the API answers with a plan (202) naming every hostname that goes live or stops. */
export async function catalogRoutesSave(event: RequestEvent) {
	requireUser(event);
	const id = uuidParam(event.params);
	const form = await event.request.formData();
	const entry = formText(form, 'entry');
	const key = formText(form, 'idempotency_key');
	if (!isCatalogName(entry)) return routesFailure(event, 400, 'That entry is not valid.');
	if (!isIdempotencyKey(key)) return routesFailure(event, 400, 'The idempotency key is not valid.');
	const built = routesFromField(formText(form, 'routes'));
	if (!built.ok) return routesFailure(event, 400, built.message);
	try {
		const plan = parseEdgePlan(
			await apiFetch(event, `/v1/projects/${id}/catalog/${entry}/routes`, {
				method: 'PUT',
				body: JSON.stringify({ routes: built.routes, idempotency_key: key })
			})
		);
		if (!plan) return routesFailure(event, 502, UNEXPECTED);
		return present(event, { scope: 'catalog' as const, routesPlan: plan });
	} catch (err) {
		rethrow(err);
		// Hostname outside an assigned verified domain, unpublished port, "already routed": the API's text.
		return routesFailure(event, apiStatus(err), apiMessage(err));
	}
}

export async function catalogRoutesConfirm(event: RequestEvent) {
	requireUser(event);
	uuidParam(event.params);
	const form = await event.request.formData();
	const entry = formText(form, 'entry');
	const confirmationId = formText(form, 'confirmation_id');
	const key = formText(form, 'idempotency_key');
	if (!isCatalogName(entry) || !isConfirmationId(confirmationId) || !isIdempotencyKey(key)) {
		return routesFailure(event, 400, 'That confirmation is not valid.');
	}
	try {
		await apiFetch(event, `/v1/confirmations/${confirmationId}`, { method: 'POST', body: JSON.stringify({ idempotency_key: key }) });
		return present(event, { scope: 'catalog' as const, routesConfirmed: true });
	} catch (err) {
		rethrow(err);
		return routesFailure(event, apiStatus(err), apiMessage(err));
	}
}
