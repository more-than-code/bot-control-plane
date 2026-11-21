/**
 * Host config files (api-v1 v1.44): the Config files tab's reads and its form actions. Every call goes to
 * `/v1/orgs/{org}/hosts/{host}/files…`. A secret file's value is posted to the API and never returned to the
 * page: no load or action result carries one.
 */
import { fail, isHttpError, isRedirect, type RequestEvent } from '@sveltejs/kit';
import {
	byteLength,
	HOST_FILE_MAX_BYTES,
	isHostFileName,
	latestVersion,
	parseSecretChanges,
	secretKeyProblem,
	WORKER_BUSY_TEXT,
	type HostFile,
	type HostFileApplyData,
	type HostFileApplyResult,
	type HostFileDetail,
	type HostFilesTab,
	type HostFileSelected,
	type HostFileKey,
	type HostFileLastApply,
	type HostFileRun,
	type HostFileRunDone,
	type HostFileStatus,
	type HostFileVersion
} from '../host-files';
import { ApiError, apiFetch, loadFailure } from './api';
import { requireUser } from './guard';
import { present } from './redact';
import { isRecord } from './shape';
import { isOrgAdmin } from './roles';
import { isUuid } from './validate';

/**
 * The apply call to the API may take this long: the API waits up to 610 s for the host (the agent task's 600 s
 * plus 10, spec amendment A4), plus a margin. The console's default is 35 s (API_TIMEOUT_MS). The form action
 * itself answers within `fastWaitMs`; the call keeps Node's default dispatcher, so undici may cut it at 300 s
 * (`headersTimeout`) and the run then becomes "result unknown" (see applyHostFile).
 */
export const HOST_FILE_APPLY_WAIT_MS = 620_000;

/**
 * The apply action waits this long for the API before it answers `running` (Cloudflare cuts a request at about
 * 100 s); a finished run is kept this long for the tab's poll. Mutable for tests only.
 */
export const hostFileApplyTimings = { fastWaitMs: 25_000, keepMs: 10 * 60_000 };

const UNEXPECTED = 'The API returned an unexpected response.';
const STATUSES: readonly HostFileStatus[] = ['in_sync', 'different', 'missing', 'wrong_owner', 'unknown'];

function filesPath(orgId: string, hostId: string, name?: string): string {
	const base = `/v1/orgs/${orgId}/hosts/${hostId}/files`;
	return name ? `${base}/${name}` : base;
}

// ---- parsers ---------------------------------------------------------------------------------------------

function str(value: unknown, max: number): string | null {
	return typeof value === 'string' && value.length <= max ? value : null;
}

function optStr(value: unknown, max: number): string | null | undefined {
	if (value == null) return null;
	return str(value, max) ?? undefined;
}

function optInt(value: unknown): number | null | undefined {
	if (value == null) return null;
	return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : undefined;
}

function parseLastApply(value: unknown): HostFileLastApply | null | undefined {
	if (value == null) return null;
	if (!isRecord(value)) return undefined;
	const version = optInt(value.version);
	const outcome = str(value.outcome, 64);
	const stage = optStr(value.stage, 64);
	const reason = optStr(value.reason, 128);
	const at = str(value.at, 64);
	if (version == null || outcome == null || stage === undefined || reason === undefined || at == null) return undefined;
	return { version, outcome, stage, reason, at };
}

export function parseHostFile(value: unknown): HostFile | null {
	if (!isRecord(value)) return null;
	const name = str(value.name, 32);
	const path = str(value.path, 512);
	const unit = str(value.unit, 160);
	const applied = optInt(value.applied_version);
	const latest = optInt(value.latest_version);
	const lastApply = parseLastApply(value.last_apply);
	if (!isHostFileName(name) || path == null || unit == null || typeof value.secret !== 'boolean') return null;
	if (applied === undefined || latest === undefined || lastApply === undefined) return null;
	const status = STATUSES.includes(value.status as HostFileStatus) ? (value.status as HostFileStatus) : 'unknown';
	return { name, path, unit, secret: value.secret, applied_version: applied, latest_version: latest, status, last_apply: lastApply };
}

function parseVersion(value: unknown, secret: boolean): HostFileVersion | null {
	if (!isRecord(value)) return null;
	const version = optInt(value.version);
	const size = optInt(value.size);
	const by = str(value.by, 64);
	const at = str(value.at, 64);
	if (version == null || size == null || by == null || at == null || typeof value.applied !== 'boolean') return null;
	// A secret file's versions carry no hash; one sent anyway is dropped, never shown.
	const sha256 = secret ? null : (str(value.sha256, 64) ?? null);
	return { version, size, sha256, by, at, applied: value.applied };
}

function parseVersions(value: unknown, secret: boolean): HostFileVersion[] | null {
	if (!Array.isArray(value)) return null;
	const out: HostFileVersion[] = [];
	for (const item of value.slice(0, 200)) {
		const parsed = parseVersion(item, secret);
		if (parsed) out.push(parsed);
	}
	return out;
}

/** `GET …/files/{name}`. The shape follows the list row's `secret`; a secret answer keeps only key names. */
export function parseHostFileDetail(value: unknown, secret: boolean): HostFileDetail | null {
	if (!isRecord(value)) return null;
	const versions = parseVersions(value.versions, secret);
	if (!versions) return null;
	if (!secret) {
		if (typeof value.content !== 'string') return null;
		return { secret: false, content: value.content, versions };
	}
	if (!Array.isArray(value.keys)) return null;
	const keys: HostFileKey[] = [];
	for (const item of value.keys.slice(0, 256)) {
		if (!isRecord(item) || typeof item.name !== 'string' || typeof item.set !== 'boolean') continue;
		if (secretKeyProblem(item.name) != null) continue;
		keys.push({ name: item.name, set: item.set });
	}
	return { secret: true, keys, versions };
}

export function parseApplyResult(value: unknown): HostFileApplyResult | null {
	if (!isRecord(value)) return null;
	const outcome = str(value.outcome, 64);
	const stage = optStr(value.stage, 64);
	const reason = optStr(value.reason, 128);
	const took = optInt(value.took_ms);
	if (outcome == null || stage === undefined || reason === undefined || took === undefined) return null;
	return { outcome, stage, reason, took_ms: took };
}

// ---- tracked applies ------------------------------------------------------------------------------------

type RunEntry = {
	orgId: string;
	hostId: string;
	name: string;
	version: number;
	startedAt: number;
	done: HostFileRunDone | null;
	/** The call failed after it was sent (timeout, network): the apply may still run; the outcome is read from the list. */
	unknown: boolean;
	/** The HTTP status for the fast path's answer when `done` is a failure. */
	status: number;
	expiresAt: number;
};

/**
 * Applies this console process started, by `org:host:name`. Lost on restart; the tab then falls back to the list.
 * Only org owners and admins of that org start, see or replace one.
 */
const runs = new Map<string, RunEntry>();

function runKey(orgId: string, hostId: string, name: string): string {
	return `${orgId}:${hostId}:${name}`;
}

function sweep(now: number): void {
	for (const [key, entry] of runs) if (entry.expiresAt <= now) runs.delete(key);
}

function runView(entry: RunEntry): HostFileRun {
	return { name: entry.name, version: entry.version, startedAt: new Date(entry.startedAt).toISOString(), done: entry.done, unknown: entry.unknown };
}

/** The tracked applies of a host's files started under `orgId`; callers show them only to that org's owners and admins. */
export function hostFileRuns(orgId: string, hostId: string): HostFileRun[] {
	sweep(Date.now());
	return [...runs.values()].filter((entry) => entry.hostId === hostId && entry.orgId === orgId).map(runView);
}

/** Tests only. */
export function clearHostFileRuns(): void {
	runs.clear();
}

// ---- load ------------------------------------------------------------------------------------------------

export function emptyFilesTab(): HostFilesTab {
	return { available: false, canEdit: false, orgId: null, items: [], message: null, selected: null, notFound: false, runs: [] };
}

function versionParam(raw: string | null): number | null {
	if (raw == null || !/^[1-9][0-9]{0,8}$/.test(raw)) return null;
	return Number(raw);
}

async function versionContent(event: RequestEvent, base: string, version: number): Promise<string | null> {
	const payload = await apiFetch(event, `${base}/versions/${version}`);
	return isRecord(payload) && typeof payload.content === 'string' ? payload.content : null;
}

async function loadSelected(event: RequestEvent, orgId: string, hostId: string, file: HostFile, url: URL): Promise<HostFileSelected> {
	const base = filesPath(orgId, hostId, file.name);
	const selected: HostFileSelected = { file, detail: null, appliedContent: null, view: null, message: null };
	try {
		selected.detail = parseHostFileDetail(await apiFetch(event, base), file.secret);
		if (!selected.detail) return { ...selected, message: UNEXPECTED };
		const detail = selected.detail;
		if (detail.secret) return selected;
		// The detail's content is the latest version's.
		const latest = latestVersion(file.latest_version, detail.versions);
		const applied = detail.versions.find((item) => item.applied)?.version ?? file.applied_version;
		const asked = versionParam(url.searchParams.get('files.v'));
		const mode = url.searchParams.get('files.mode') === 'diff' ? 'diff' : 'view';
		const [appliedContent, viewContent] = await Promise.all([
			applied == null ? null : applied === latest ? detail.content : versionContent(event, base, applied),
			asked == null ? null : asked === latest ? detail.content : versionContent(event, base, asked)
		]);
		selected.appliedContent = appliedContent;
		if (asked != null && viewContent != null) selected.view = { version: asked, content: viewContent, mode };
		return selected;
	} catch (err) {
		return { ...selected, message: loadFailure(err).message };
	}
}

/**
 * The Config files tab: the list for anyone who sees the host; the named file's detail and the tracked applies
 * for org owners and admins. Depends on `host:files`, which the tab invalidates after an apply.
 */
export async function loadHostFiles(
	event: RequestEvent & { depends?: (...deps: `${string}:${string}`[]) => void },
	host: { id: string; org_id: string | null },
	canEdit: boolean
): Promise<HostFilesTab> {
	event.depends?.('host:files');
	if (!host.org_id) return emptyFilesTab();
	const tab: HostFilesTab = { ...emptyFilesTab(), available: true, canEdit, orgId: host.org_id };
	if (canEdit) tab.runs = hostFileRuns(host.org_id, host.id);
	try {
		const payload = await apiFetch(event, filesPath(host.org_id, host.id));
		if (!isRecord(payload) || !Array.isArray(payload.items)) return { ...tab, message: UNEXPECTED };
		tab.items = payload.items.slice(0, 64).map(parseHostFile).filter((item) => item != null);
	} catch (err) {
		return { ...tab, message: loadFailure(err).message };
	}
	const name = event.url.searchParams.get('files.name');
	if (!name) return tab;
	const file = tab.items.find((item) => item.name === name);
	if (!file) return { ...tab, notFound: true };
	if (!canEdit) return tab;
	return { ...tab, selected: await loadSelected(event, host.org_id, host.id, file, event.url) };
}

// ---- actions ---------------------------------------------------------------------------------------------

/** Failure data the tab reads. `filesField` names the form it belongs to. Never a secret value. */
type FilesFailure = { filesMessage: string; filesField?: string; content?: string; hostFileApply?: HostFileApplyData };

function failFiles(event: RequestEvent, status: number, data: FilesFailure) {
	return fail(status, present(event, data));
}

/** An API error as a status and message, without throwing (the apply may end after the request did). */
function errorOf(err: unknown): { status: number; message: string } {
	if (err instanceof ApiError) return { status: err.status >= 400 && err.status <= 599 ? err.status : 503, message: err.msg };
	if (isRedirect(err)) return { status: 401, message: 'Sign in again, then check the file.' };
	if (isHttpError(err)) return { status: err.status, message: err.body.message };
	console.error('bot api request failed');
	return { status: 503, message: 'The API is unavailable.' };
}

function apiFailure(event: RequestEvent, err: unknown, extra: Omit<FilesFailure, 'filesMessage'> = {}) {
	if (isRedirect(err) || isHttpError(err)) throw err;
	const { status, message } = errorOf(err);
	return failFiles(event, status, { filesMessage: message, ...extra });
}

function formText(form: FormData, key: string): string {
	const value = form.get(key);
	return typeof value === 'string' ? value : '';
}

type Target = { orgId: string; hostId: string; name: string };

function target(event: RequestEvent, form: FormData): Target | null {
	const hostId = event.params.id ?? '';
	const orgId = formText(form, 'org');
	const name = formText(form, 'name');
	if (!isUuid(hostId) || !isUuid(orgId) || !isHostFileName(name)) return null;
	return { orgId, hostId, name };
}

/**
 * Save a plain file. The browser sends a textarea's line breaks as CRLF; they are turned back into LF, so
 * the file holds what the admin typed. The typed text comes back on a failure so the editor keeps it.
 */
export async function saveHostFile(event: RequestEvent) {
	requireUser(event);
	const form = await event.request.formData();
	const content = formText(form, 'content').replaceAll('\r\n', '\n');
	const at = target(event, form);
	if (!at) return failFiles(event, 400, { filesMessage: 'Choose a file.', content });
	const size = byteLength(content);
	if (size === 0) return failFiles(event, 400, { filesMessage: 'The file cannot be empty.', content });
	if (size > HOST_FILE_MAX_BYTES) return failFiles(event, 400, { filesMessage: `The file is at most ${HOST_FILE_MAX_BYTES} bytes.`, content });
	if (content.includes('\0')) return failFiles(event, 400, { filesMessage: 'The file cannot contain a NUL character.', content });
	try {
		const payload = await apiFetch(event, filesPath(at.orgId, at.hostId, at.name), { method: 'PUT', body: JSON.stringify({ content }) });
		const version = isRecord(payload) ? optInt(payload.version) : undefined;
		if (version == null) return failFiles(event, 502, { filesMessage: UNEXPECTED, content });
		return present(event, { hostFileSaved: { name: at.name, version } });
	} catch (err) {
		return apiFailure(event, err, { content });
	}
}

function baseVersion(form: FormData): number | null {
	const raw = formText(form, 'base_version');
	return /^(0|[1-9][0-9]{0,8})$/.test(raw) ? Number(raw) : null;
}

/**
 * Save a secret file's staged changes in one PUT `{base_version, set, remove}`. The values go to the API only:
 * neither the answer nor a failure carries one.
 */
export async function saveSecretHostFile(event: RequestEvent) {
	requireUser(event);
	const form = await event.request.formData();
	const at = target(event, form);
	const base = baseVersion(form);
	if (!at || base == null) return failFiles(event, 400, { filesMessage: 'Choose a file.', filesField: 'secret' });
	const changes = parseSecretChanges(formText(form, 'changes'));
	if (typeof changes === 'string') return failFiles(event, 400, { filesMessage: changes, filesField: 'secret' });
	try {
		const body = JSON.stringify({ base_version: base, set: changes.set, remove: changes.remove });
		const payload = await apiFetch(event, filesPath(at.orgId, at.hostId, at.name), { method: 'PUT', body });
		const version = isRecord(payload) ? optInt(payload.version) : undefined;
		if (version == null) return failFiles(event, 502, { filesMessage: UNEXPECTED, filesField: 'secret' });
		return present(event, { hostFileSaved: { name: at.name, version } });
	} catch (err) {
		return apiFailure(event, err, { filesField: 'secret' });
	}
}

function doneOf(name: string, version: number, payload: unknown): { done: HostFileRunDone; status: number } {
	const result = parseApplyResult(payload);
	if (!result) return { done: { apply: null, message: UNEXPECTED }, status: 502 };
	return { done: { apply: { name, version, busy: false, result }, message: null }, status: 200 };
}

function doneOfError(name: string, version: number, err: unknown): { done: HostFileRunDone; status: number } {
	if (err instanceof ApiError && err.code === 'worker_busy') {
		return { done: { apply: { name, version, busy: true, result: parseApplyResult(err.payload) }, message: WORKER_BUSY_TEXT }, status: 409 };
	}
	const { status, message } = errorOf(err);
	return { done: { apply: null, message }, status };
}

/**
 * The call itself failed (apiFetch's own timeout, or a network error such as undici's 300 s headers timeout), as
 * opposed to an answer from the API. The apply may still be running on the host.
 */
function lostCall(err: unknown): boolean {
	return err instanceof ApiError && ((err.code === 'timeout' && err.status === 504) || (err.code === 'unavailable' && err.status === 503));
}

function answer(event: RequestEvent, done: HostFileRunDone, status: number) {
	if (done.apply && !done.apply.busy) return present(event, { hostFileApply: done.apply });
	if (done.apply?.busy) return failFiles(event, 409, { filesMessage: done.message ?? WORKER_BUSY_TEXT, hostFileApply: done.apply });
	return failFiles(event, status, { filesMessage: done.message ?? UNEXPECTED });
}

/**
 * Apply one saved version (a rollback is an older one). The API call may take up to 610 s, longer than a proxy
 * keeps a request open, so the action waits at most `fastWaitMs`: if the apply ends by then it answers as usual,
 * otherwise it answers `{hostFileRunning}` and the call carries on in this process (tracked in `runs`) while the
 * tab polls. A 409 `worker_busy` comes back as `busy`, so the tab offers "Apply anyway" (`force=1`).
 */
export async function applyHostFile(event: RequestEvent) {
	requireUser(event);
	const form = await event.request.formData();
	const at = target(event, form);
	const version = versionParam(formText(form, 'version'));
	if (!at || version == null) return failFiles(event, 400, { filesMessage: 'Choose a version to apply.' });
	const now = Date.now();
	sweep(now);
	// Only an org owner or admin of the posted org touches the runs (the API checks the host belongs to it).
	if (!isOrgAdmin(event.locals.orgs, at.orgId)) return failFiles(event, 403, { filesMessage: 'Only organization owners and admins can apply config files.' });
	const key = runKey(at.orgId, at.hostId, at.name);
	const running = runs.get(key);
	// A run whose result is unknown does not block: the API answers `conflict` if that apply is still going.
	if (running && !running.done && !running.unknown) return present(event, { hostFileRunning: runView(running) });

	const force = formText(form, 'force') === '1';
	const body = JSON.stringify(force ? { version, force: true } : { version });
	// A running entry outlives the API's whole wait; a finished one is kept `keepMs` for the poll.
	const entry: RunEntry = {
		...at,
		version,
		startedAt: now,
		done: null,
		unknown: false,
		status: 200,
		expiresAt: now + HOST_FILE_APPLY_WAIT_MS + hostFileApplyTimings.keepMs
	};
	runs.set(key, entry);
	/** Set once the action has answered `running`: from then on a lost call means "result unknown", not a failure. */
	let detached = false;
	const work = apiFetch(event, `${filesPath(at.orgId, at.hostId, at.name)}/apply`, { method: 'POST', body }, HOST_FILE_APPLY_WAIT_MS)
		.then((payload) => doneOf(at.name, version, payload))
		.catch((err: unknown) => {
			if (detached && lostCall(err)) return null;
			return doneOfError(at.name, version, err);
		})
		.then((settled) => {
			if (settled) {
				entry.done = settled.done;
				entry.status = settled.status;
			} else {
				// The request was sent and the answer lost; the API still records the apply in the file's last apply.
				entry.unknown = true;
			}
			entry.expiresAt = Date.now() + hostFileApplyTimings.keepMs;
			return settled;
		});
	let timer: ReturnType<typeof setTimeout> | undefined;
	const waited = new Promise<null>((resolve) => {
		timer = setTimeout(() => resolve(null), hostFileApplyTimings.fastWaitMs);
	});
	const settled = await Promise.race([work, waited]);
	clearTimeout(timer);
	if (!settled) {
		detached = true;
		return present(event, { hostFileRunning: runView(entry) });
	}
	// Answered in time: this request carries the outcome, so nothing is left to poll.
	if (runs.get(key) === entry) runs.delete(key);
	return answer(event, settled.done, settled.status);
}
