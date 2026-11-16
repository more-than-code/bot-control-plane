import { error, fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import type { CatalogEntry, Registry } from '$lib/api-types';
import { ApiError, apiFetch } from '$lib/server/api';
import { githubConnectOf } from '$lib/server/image-actions';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isUuid } from '$lib/server/validate';
import { isCatalogName } from '$lib/catalog';
import { imageRepository } from '$lib/image-tags';
import {
	GITHUB_REGISTRY,
	imageRegistryHost,
	isGitHubAuthorizeUrl,
	parseHostPolicySnippet,
	parseImageAccess,
	parseReadiness,
	type HostPolicySnippet,
	type ImageAccessResult,
	type Readiness
} from '$lib/settings-auto';

/** The project a Connect GitHub returns to, kept for the callback (spec-settings-automatic §3.1). */
export const GITHUB_RETURN_COOKIE = 'bot_github_return';
/** The connect request a Connect GitHub started from chat returns to (api-v1 v1.31), kept for the callback. */
export const GITHUB_CONNECT_COOKIE = 'bot_github_connect';
const GITHUB_RETURN_MAX_AGE = 600;
const UNEXPECTED = 'The API returned an unexpected response.';

export function githubReturnCookieOptions(url: URL) {
	return { httpOnly: true, sameSite: 'lax' as const, path: '/auth/github', maxAge: GITHUB_RETURN_MAX_AGE, secure: url.protocol === 'https:' };
}

function projectIdOf(params: { id?: string }): string {
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

/** Connect GitHub (and Reconnect): ask the API for GitHub's authorize URL and send the browser there. */
export async function connectGitHub(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	let target: unknown;
	try {
		const payload = await apiFetch(event, `/v1/projects/${id}/registries/github/start`, { method: 'POST', body: '{}' });
		target = payload != null && typeof payload === 'object' ? (payload as Record<string, unknown>).authorize_url : null;
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'settings' as const, message: apiMessage(err) }));
	}
	if (!isGitHubAuthorizeUrl(target)) return fail(502, present(event, { scope: 'settings' as const, message: UNEXPECTED }));
	event.cookies.set(GITHUB_RETURN_COOKIE, id, githubReturnCookieOptions(event.url));
	// A Settings start returns to Settings, even after an abandoned start from a connect page.
	if (event.cookies.get(GITHUB_CONNECT_COOKIE) != null) event.cookies.delete(GITHUB_CONNECT_COOKIE, githubReturnCookieOptions(event.url));
	redirect(303, target);
}

/** One image-access lookup for the Image field. The result is names and statuses only. */
export async function imageAccess(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const image = formText(await event.request.formData(), 'image').trim();
	const none = (status: number) => fail(status, present(event, { scope: 'catalog' as const, imageAccess: null as ImageAccessResult | null }));
	if (image === '' || image.length > 512 || /\s/.test(image)) return none(400);
	try {
		const result = parseImageAccess(await apiFetch(event, `/v1/projects/${id}/image-access?image=${encodeURIComponent(image)}`));
		if (!result) return none(502);
		// Only a line that offers Connect GitHub needs to know whether the API has it.
		const wantsConnect = result.registry === GITHUB_REGISTRY && (result.access === 'private_no_login' || result.access === 'login_failed');
		const imageAccessGitHub = wantsConnect ? await githubConnectFor(event, id) : false;
		return present(event, { scope: 'catalog' as const, imageAccess: result, imageAccessGitHub });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return none(apiStatus(err));
	}
}

/** The readiness list of one entry, for a host when `host_id` is set. */
export async function catalogReadiness(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const entry = formText(form, 'entry');
	const hostId = formText(form, 'host_id');
	const failed = (status: number, message: string) =>
		fail(status, present(event, { scope: 'catalog' as const, readiness: null as Readiness | null, readinessMessage: message }));
	if (!isCatalogName(entry) || (hostId !== '' && !isUuid(hostId))) return failed(400, 'That entry or host is not valid.');
	const query = hostId ? `?host_id=${hostId}` : '';
	try {
		const readiness = parseReadiness(await apiFetch(event, `/v1/projects/${id}/catalog/${entry}/readiness${query}`));
		if (!readiness) return failed(502, UNEXPECTED);
		// Only a Connect GitHub fix needs to know whether the API offers it (the registries list's github_connect).
		const readinessGitHub = readiness.checks.some((check) => check.fix.action === 'connect_github') ? await githubConnectFor(event, id) : false;
		return present(event, { scope: 'catalog' as const, readiness, readinessMessage: null as string | null, readinessGitHub });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return failed(apiStatus(err), apiMessage(err));
	}
}

/** The runner.toml lines for the host's root (Allow on host). Text only. */
export async function hostPolicySnippet(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const entry = formText(form, 'entry');
	const hostId = formText(form, 'host_id');
	const failed = (status: number, message: string) =>
		fail(status, present(event, { scope: 'catalog' as const, hostPolicy: null as HostPolicySnippet | null, hostPolicyMessage: message }));
	if (!isCatalogName(entry) || !isUuid(hostId)) return failed(400, 'That entry or host is not valid.');
	try {
		const snippet = parseHostPolicySnippet(await apiFetch(event, `/v1/projects/${id}/catalog/${entry}/host-policy-snippet?host_id=${hostId}`));
		if (!snippet) return failed(502, UNEXPECTED);
		return present(event, { scope: 'catalog' as const, hostPolicy: snippet, hostPolicyMessage: null as string | null });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return failed(apiStatus(err), apiMessage(err));
	}
}

async function githubConnectFor(event: RequestEvent, id: string): Promise<boolean> {
	try {
		return githubConnectOf(await apiFetch(event, `/v1/projects/${id}/registries`));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return false;
	}
}

/**
 * Whether ghcr.io refuses the project's GitHub connection, for the Registries card badge. Only with a GitHub row, it
 * asks image-access once per ghcr.io repository in the catalog (cached lookups, in parallel); any `login_failed` is a
 * refusal. No such image, or a failed lookup, does not count.
 */
export async function githubRefused(event: RequestEvent, id: string, registries: Registry[], catalog: CatalogEntry[]): Promise<boolean> {
	if (!registries.some((row) => row.host === GITHUB_REGISTRY && row.source === 'github')) return false;
	const byRepository = new Map<string, string>();
	for (const entry of catalog) {
		if (entry.kind !== 'image' || imageRegistryHost(entry.spec.image) !== GITHUB_REGISTRY) continue;
		const repository = imageRepository(entry.spec.image) ?? entry.spec.image;
		if (!byRepository.has(repository)) byRepository.set(repository, entry.spec.image);
	}
	const answers = await Promise.all(
		[...byRepository.values()].map(async (image) => {
			try {
				return parseImageAccess(await apiFetch(event, `/v1/projects/${id}/image-access?image=${encodeURIComponent(image)}`))?.access === 'login_failed';
			} catch (err) {
				if (isRedirect(err) || isHttpError(err)) throw err;
				return false;
			}
		})
	);
	return answers.some(Boolean);
}
