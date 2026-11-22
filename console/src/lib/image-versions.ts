import type { ActionResult } from '@sveltejs/kit';
import { imageRepository } from '$lib/image-tags';
import { sameOriginPath } from '$lib/log-view';

/**
 * Image versions of a ghcr.io image (api-v1 v1.45): the package versions with their holders and stale marks, and
 * the delete of one unheld version. Pure helpers shared by the server actions and the dialog.
 */

export const GHCR = 'ghcr.io';
export const VERSIONS_FAILED = 'Image versions could not be read. Close the dialog and try again.';
export const VERSIONS_CAP = 500;

const DIGEST = /^sha256:[0-9a-f]{64}$/;
const TAG = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export type HoldWhy = 'running' | 'rollback_target' | 'recent_deploy';

export type VersionHolder = {
	project_id: string;
	project_name: string;
	entry: string;
	host_id: string;
	host_name: string;
	why: HoldWhy;
};

export type ImageVersion = {
	version_id: number;
	digest: string;
	tags: string[];
	created_at: string;
	updated_at: string;
	in_use: VersionHolder[];
	stale: boolean;
	release: boolean;
};

export type ImageVersionsList = {
	items: ImageVersion[];
	truncated: boolean;
	can_delete: boolean;
	can_delete_reason: string | null;
};

/** The dialog's state: loading, a list, or a message. */
export type VersionsState = { state: 'loading' } | { state: 'ready'; list: ImageVersionsList } | { state: 'failed'; message: string };

/** The repository of a ghcr.io image (no tag or digest), or null for any other registry. */
export function ghcrRepository(image: string): string | null {
	const repo = imageRepository(image);
	if (repo == null || !repo.startsWith(GHCR + '/')) return null;
	return repo.split('/').length >= 3 ? repo : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value != null && typeof value === 'object' && !Array.isArray(value);
}

function isTime(value: unknown): value is string {
	return typeof value === 'string' && value.length <= 64 && !Number.isNaN(Date.parse(value));
}

function parseHolder(value: unknown): VersionHolder | null {
	if (!isRecord(value)) return null;
	const { project_id, project_name, entry, host_id, host_name, why } = value;
	if (typeof project_id !== 'string' || !UUID.test(project_id) || typeof host_id !== 'string' || !UUID.test(host_id)) return null;
	if (typeof project_name !== 'string' || typeof entry !== 'string' || typeof host_name !== 'string') return null;
	if (why !== 'running' && why !== 'rollback_target' && why !== 'recent_deploy') return null;
	return { project_id, project_name: project_name.slice(0, 200), entry: entry.slice(0, 64), host_id, host_name: host_name.slice(0, 200), why };
}

/** One version, or null when it does not have the v1.45 shape. Tags that break the tag rule are dropped. */
export function parseImageVersion(value: unknown): ImageVersion | null {
	if (!isRecord(value)) return null;
	const { version_id, digest, tags, created_at, updated_at, in_use, stale, release } = value;
	if (typeof version_id !== 'number' || !Number.isSafeInteger(version_id) || version_id <= 0) return null;
	if (typeof digest !== 'string' || !DIGEST.test(digest) || !Array.isArray(tags) || !Array.isArray(in_use)) return null;
	if (!isTime(created_at) || !isTime(updated_at) || typeof stale !== 'boolean' || typeof release !== 'boolean') return null;
	const holders: VersionHolder[] = [];
	for (const item of in_use) {
		const holder = parseHolder(item);
		if (!holder) return null;
		holders.push(holder);
	}
	const kept = tags.filter((tag): tag is string => typeof tag === 'string' && TAG.test(tag));
	return { version_id, digest, tags: kept, created_at, updated_at, in_use: holders, stale, release };
}

/** The list payload, or null when it is not the v1.45 shape. More than VERSIONS_CAP rows marks it truncated. */
export function parseImageVersions(payload: unknown): ImageVersionsList | null {
	if (!isRecord(payload) || !Array.isArray(payload.items) || typeof payload.truncated !== 'boolean' || typeof payload.can_delete !== 'boolean') {
		return null;
	}
	const reason = payload.can_delete_reason;
	if (reason != null && typeof reason !== 'string') return null;
	const items: ImageVersion[] = [];
	for (const raw of payload.items.slice(0, VERSIONS_CAP)) {
		const item = parseImageVersion(raw);
		if (item) items.push(item);
	}
	return {
		items,
		truncated: payload.truncated || payload.items.length > VERSIONS_CAP,
		can_delete: payload.can_delete,
		can_delete_reason: payload.can_delete ? null : (reason ?? 'You cannot delete image versions here.').slice(0, 300)
	};
}

/** The 12 hex characters the delete confirmation must repeat. */
export function confirmCode(digest: string): string {
	return digest.replace(/^sha256:/, '').slice(0, 12);
}

/** `sha256:` and the first 12 hex characters. */
export function shortDigest(digest: string): string {
	return 'sha256:' + confirmCode(digest);
}

const WHY: Record<HoldWhy, string> = {
	running: 'running',
	rollback_target: 'rollback target',
	recent_deploy: 'recent deploy'
};

/** "web on pi4 (running)", with the project name when it is not this project. */
export function holderText(holder: VersionHolder, projectId: string): string {
	const entry = holder.project_id === projectId ? holder.entry : `${holder.project_name} / ${holder.entry}`;
	return `${entry} on ${holder.host_name} (${WHY[holder.why]})`;
}

/** The Status cell: In use, Stale, or Unused, with the first holder. */
export function versionStatus(version: ImageVersion, projectId: string): string {
	if (version.in_use.length > 0) {
		const more = version.in_use.length > 1 ? ` and ${version.in_use.length - 1} more` : '';
		return `In use: ${holderText(version.in_use[0], projectId)}${more}`;
	}
	return version.stale ? 'Stale' : 'Unused';
}

/** Why a version's Delete is not offered, or null when it is. */
export function deleteBlocked(list: ImageVersionsList, version: ImageVersion, projectId: string): string | null {
	if (!list.can_delete) return list.can_delete_reason;
	if (version.in_use.length > 0) return `In use: ${holderText(version.in_use[0], projectId)}`;
	return null;
}

/** The delete dialog's text: every tag that goes with it, the digest, and that it cannot be undone. */
export function deleteMessage(version: ImageVersion): string {
	const tags = version.tags.length === 0 ? 'It has no tags.' : `Deleting this version removes all of these tags: ${version.tags.join(', ')}.`;
	const release = version.release ? ' This version has a release tag.' : '';
	return `Delete ${version.digest} from GitHub? ${tags}${release} This cannot be undone.`;
}

/** Current versions first, stale ones in their own group (collapsed by default). */
export function splitStale(items: readonly ImageVersion[]): { current: ImageVersion[]; stale: ImageVersion[] } {
	return { current: items.filter((item) => !item.stale), stale: items.filter((item) => item.stale) };
}

export type VersionsReadDeps = {
	fetch: typeof fetch;
	deserialize: (text: string) => ActionResult;
	/** The `catalogImageVersions` action URL. */
	action: string;
	origin: string;
};

export type VersionsRead = { kind: 'state'; state: VersionsState } | { kind: 'redirect'; path: string };

/**
 * Reads the versions by posting the `catalogImageVersions` action with `fetch`: only the action runs, so the page
 * load does not re-run. The server calls the API; the browser never does. Never rejects.
 */
export async function readImageVersions(deps: VersionsReadDeps, image: string, signal?: AbortSignal): Promise<VersionsRead> {
	const failed = (message: string): VersionsRead => ({ kind: 'state', state: { state: 'failed', message } });
	try {
		const body = new FormData();
		body.set('image', image);
		const response = await deps.fetch(deps.action, {
			method: 'POST',
			body,
			headers: { accept: 'application/json', 'x-sveltekit-action': 'true' },
			signal
		});
		const result = deps.deserialize(await response.text());
		if (result.type === 'redirect') {
			const path = sameOriginPath(result.location, deps.origin);
			return path == null ? failed(VERSIONS_FAILED) : { kind: 'redirect', path };
		}
		if (result.type === 'error') return failed(VERSIONS_FAILED);
		const data = (result.data as { imageVersions?: { list?: unknown; message?: unknown } } | undefined)?.imageVersions;
		if (result.type === 'success') {
			const list = parseImageVersions(data?.list);
			return list ? { kind: 'state', state: { state: 'ready', list } } : failed(VERSIONS_FAILED);
		}
		return failed(typeof data?.message === 'string' && data.message !== '' ? data.message : VERSIONS_FAILED);
	} catch {
		return failed(VERSIONS_FAILED);
	}
}

/** The API's `registry_unavailable` reasons for this route, in the person's words. */
export function versionsReasonText(reason: unknown): string {
	switch (reason) {
		case 'not_found':
			return 'GitHub did not find this package, or the GitHub login cannot see it.';
		case 'delete_refused':
			return "GitHub refused the delete. You need admin rights on the package, and the org must allow Deploybot's GitHub app.";
		case 'timeout':
			return 'GitHub did not answer in time. Try again.';
		default:
			return 'Could not reach GitHub. Try again.';
	}
}
