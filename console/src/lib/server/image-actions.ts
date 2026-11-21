import { error, fail, isHttpError, isRedirect, redirect, type RequestEvent } from '@sveltejs/kit';
import { randomUUID } from 'node:crypto';
import { ENV_KEY, byteLength, duplicateEntryMessage, isCatalogName, isIdempotencyKey } from '$lib/catalog';
import { pastedText } from '$lib/file-request';
import { privateKeyRevealPath, privateSetEntry } from '$lib/private-keys';
import {
	IMAGE_ACTION_WAIT_MS,
	STILL_RUNNING,
	historyTimeoutMs,
	draftForBrowser,
	imageDraftFromForm,
	imageRequestBody,
	imageSpecFromDraft,
	isConfirmationId,
	isFileName,
	isFullDigest,
	isRegistryHost,
	isSetName,
	redactSubmitted,
	submittedSecrets,
	type ImageDraft,
	type KnownSet
} from '$lib/image-spec';
import { generateRequestBody, isGenerateKind, revealFailedText } from '$lib/env-generate';
import { imageRepository, isTaggedRef, tagLookupError, type ImageDigestResult, type ImageTagsResult, type TagLookupError } from '$lib/image-tags';
import type { EnvSet, ProjectFile, Registry } from '$lib/api-types';
import type { ConflictTimes, ImageDeployState, ImageEditorState } from '$lib/project-action';
import { ApiError, apiFetch } from '$lib/server/api';
import { formText } from '$lib/server/form';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import {
	parseDeployPayload,
	parseEnvSetList,
	parseImageDeployList,
	parseImageDigest,
	parseImageTags,
	parseProjectFile,
	parseProjectFileContent,
	parseProjectFileList,
	parseRegistry,
	parseRegistryList,
	parseRemovePayload
} from '$lib/server/shape';
import { isUuid } from '$lib/server/validate';
import { backTo } from '$lib/url';

const UNEXPECTED = 'The API returned an unexpected response.';
const DOTENV_MAX = 256 * 1024;
const FILE_MAX = 64 * 1024;

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

function stillRunning(err: unknown): boolean {
	return err instanceof ApiError && (err.status === 504 || err.code === 'timeout');
}

/** The times a running-operation conflict carries (api-v1 v1.42 item 2), or null. */
function conflictTimes(err: unknown): ConflictTimes | null {
	if (!(err instanceof ApiError) || err.code !== 'conflict' || typeof err.payload !== 'object' || err.payload === null) return null;
	const payload = err.payload as Record<string, unknown>;
	const started = payload.started_at;
	const ends = payload.ends_by;
	if (typeof started !== 'string' || typeof ends !== 'string' || started.length > 64 || ends.length > 64) return null;
	if (!Number.isFinite(Date.parse(started)) || !Number.isFinite(Date.parse(ends))) return null;
	return { started_at: started, ends_by: ends };
}

/**
 * A failed deploy, rollback, remove or confirm. Past the wait cap the answer is
 * still running (`capped`). That and a running-operation conflict reload
 * History, which then lists the running row; `alwaysHistory` reloads it for
 * any failure.
 */
async function imageActionFailure(
	event: RequestEvent,
	err: unknown,
	id: string,
	base: Omit<ImageDeployState, 'resultId'>,
	opts: { capped: boolean; alwaysHistory: boolean; started: number }
) {
	const running = opts.capped && stillRunning(err);
	const conflict = conflictTimes(err);
	const message = running ? STILL_RUNNING : apiMessage(err);
	const history = running || conflict || opts.alwaysHistory ? await historyFor(event, id, base.name, base.hostId, opts.started) : null;
	return deployFailure(event, apiStatus(err), { ...base, history, message, conflict });
}

function editorFailure(event: RequestEvent, status: number, mode: 'create' | 'edit', name: string | null, draft: ImageDraft, field: string | null, message: string) {
	const imageEditor: ImageEditorState = { mode, name, draft: draftForBrowser(draft), message, field };
	return fail(status, present(event, { scope: 'catalog' as const, imageEditor }));
}

function deployState(input: Omit<ImageDeployState, 'resultId'>): ImageDeployState {
	return { ...input, resultId: randomUUID() };
}

function deployFailure(event: RequestEvent, status: number, state: Omit<ImageDeployState, 'resultId'>) {
	return fail(status, present(event, { scope: 'catalog' as const, imageDeploy: deployState(state) }));
}

function emptyDeploy(name: string, hostId: string, workload: ImageDeployState['workload'], waitSec: number, message: string | null): Omit<ImageDeployState, 'resultId'> {
	return {
		name,
		hostId,
		workload,
		waitSec,
		outcome: null,
		status: null,
		lines: [],
		history: null,
		confirmation: null,
		confirmKind: null,
		message
	};
}

function waitOf(workload: ImageDeployState['workload'], raw: string): number {
	const parsed = /^[0-9]+$/.test(raw) ? Number(raw) : NaN;
	if (workload === 'job') return parsed >= 10 && parsed <= 3600 ? parsed : 600;
	return parsed >= 5 && parsed <= 600 ? parsed : 30;
}

function named(form: FormData): string {
	const name = formText(form, 'name');
	return isCatalogName(name) ? name : '';
}

async function historyFor(event: RequestEvent, id: string, name: string, hostId: string, started?: number): Promise<ImageDeployState['history']> {
	const timeoutMs = started === undefined ? undefined : historyTimeoutMs(started, Date.now());
	try {
		const payload = await apiFetch(event, `/v1/projects/${id}/catalog/${name}/deploys?host_id=${hostId}`, undefined, timeoutMs);
		return parseImageDeployList(payload)?.items ?? null;
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return null;
	}
}

export async function saveImageEntry(event: RequestEvent, mode: 'create' | 'edit', form: FormData, id: string) {
	const built = imageDraftFromForm(form);
	const name = mode === 'edit' ? (isCatalogName(formText(form, 'entry_name')) ? formText(form, 'entry_name') : null) : null;
	if (mode === 'edit' && !name) return editorFailure(event, 400, mode, null, built.draft, null, 'That catalog entry is not valid.');
	if (built.message) return editorFailure(event, 400, mode, name, built.draft, built.field, built.message);
	const check = imageSpecFromDraft(built.draft, undefined, mode === 'create', undefined, name);
	if (!check.ok) return editorFailure(event, 400, mode, name, built.draft, check.field, check.message);
	const body = imageRequestBody(check.spec, check.name);
	const path = mode === 'create' ? `/v1/projects/${id}/catalog` : `/v1/projects/${id}/catalog/${name}`;
	try {
		await apiFetch(event, path, { method: mode === 'create' ? 'POST' : 'PUT', body: JSON.stringify(body) });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (mode === 'create' && check.name && err instanceof ApiError && err.code === 'conflict') {
			return editorFailure(event, 409, mode, name, built.draft, null, duplicateEntryMessage(check.name));
		}
		const message = redactSubmitted(apiMessage(err), submittedSecrets(built.draft));
		return editorFailure(event, apiStatus(err), mode, name, built.draft, null, message);
	}
}

function deployContext(form: FormData): { name: string; hostId: string; workload: 'service' | 'job'; waitSec: number; key: string } | { message: string } {
	const name = named(form);
	if (!name) return { message: 'That catalog entry is not valid.' };
	const hostId = formText(form, 'host_id');
	if (!isUuid(hostId)) return { message: 'Choose a host.' };
	const workload = formText(form, 'workload') === 'job' ? 'job' : 'service';
	const waitSec = waitOf(workload, formText(form, 'wait_sec'));
	const key = formText(form, 'idempotency_key');
	if (!isIdempotencyKey(key)) return { message: 'The idempotency key is not valid.' };
	return { name, hostId, workload, waitSec, key };
}

export async function catalogDeploy(event: RequestEvent) {
	const started = Date.now();
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const ctx = deployContext(form);
	if ('message' in ctx) return fail(400, present(event, { scope: 'catalog' as const, message: ctx.message }));
	const base = emptyDeploy(ctx.name, ctx.hostId, ctx.workload, ctx.waitSec, null);
	try {
		const payload = await apiFetch(
			event,
			`/v1/projects/${id}/catalog/${ctx.name}/deploy`,
			{
				method: 'POST',
				body: JSON.stringify({ host_id: ctx.hostId, force: formText(form, 'force') === '1', idempotency_key: ctx.key })
			},
			IMAGE_ACTION_WAIT_MS
		);
		const parsed = parseDeployPayload(payload);
		if (!parsed) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
		const history = await historyFor(event, id, ctx.name, ctx.hostId, started);
		if (parsed.kind === 'confirmation') {
			return present(event, {
				scope: 'catalog' as const,
				imageDeploy: deployState({ ...base, history, confirmation: parsed.confirmation, confirmKind: 'deploy' })
			});
		}
		return present(event, {
			scope: 'catalog' as const,
			imageDeploy: deployState({ ...base, history, outcome: parsed.deploy, status: parsed.status, lines: parsed.lines })
		});
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return imageActionFailure(event, err, id, base, { capped: true, alwaysHistory: true, started });
	}
}

export async function catalogRollback(event: RequestEvent) {
	const started = Date.now();
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const ctx = deployContext(form);
	if ('message' in ctx) return fail(400, present(event, { scope: 'catalog' as const, message: ctx.message }));
	const digest = formText(form, 'digest');
	const base = emptyDeploy(ctx.name, ctx.hostId, ctx.workload, ctx.waitSec, null);
	if (!isFullDigest(digest)) return deployFailure(event, 400, { ...base, message: 'That digest is not valid.' });
	try {
		const payload = await apiFetch(
			event,
			`/v1/projects/${id}/catalog/${ctx.name}/rollback`,
			{ method: 'POST', body: JSON.stringify({ host_id: ctx.hostId, digest, idempotency_key: ctx.key }) },
			IMAGE_ACTION_WAIT_MS
		);
		const parsed = parseDeployPayload(payload);
		if (!parsed) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
		const history = await historyFor(event, id, ctx.name, ctx.hostId, started);
		if (parsed.kind === 'confirmation') {
			return present(event, {
				scope: 'catalog' as const,
				imageDeploy: deployState({ ...base, history, confirmation: parsed.confirmation, confirmKind: 'rollback' })
			});
		}
		return present(event, {
			scope: 'catalog' as const,
			imageDeploy: deployState({ ...base, history, outcome: parsed.deploy, status: parsed.status, lines: parsed.lines })
		});
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return imageActionFailure(event, err, id, base, { capped: true, alwaysHistory: false, started });
	}
}

export async function catalogRemove(event: RequestEvent) {
	const started = Date.now();
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const ctx = deployContext(form);
	if ('message' in ctx) return fail(400, present(event, { scope: 'catalog' as const, message: ctx.message }));
	const base = emptyDeploy(ctx.name, ctx.hostId, ctx.workload, ctx.waitSec, null);
	try {
		const payload = await apiFetch(
			event,
			`/v1/projects/${id}/catalog/${ctx.name}/remove`,
			{ method: 'POST', body: JSON.stringify({ host_id: ctx.hostId, idempotency_key: ctx.key }) },
			IMAGE_ACTION_WAIT_MS
		);
		const parsed = parseRemovePayload(payload);
		if (!parsed) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
		const history = await historyFor(event, id, ctx.name, ctx.hostId, started);
		if (parsed.kind === 'confirmation') {
			return present(event, {
				scope: 'catalog' as const,
				imageDeploy: deployState({ ...base, history, confirmation: parsed.confirmation, confirmKind: 'remove' })
			});
		}
		return present(event, { scope: 'catalog' as const, imageDeploy: deployState({ ...base, history, status: parsed.status }) });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return imageActionFailure(event, err, id, base, { capped: true, alwaysHistory: false, started });
	}
}

export async function catalogConfirm(event: RequestEvent) {
	const started = Date.now();
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const ctx = deployContext(form);
	if ('message' in ctx) return fail(400, present(event, { scope: 'catalog' as const, message: ctx.message }));
	const confirmationId = formText(form, 'confirmation_id');
	const kind = formText(form, 'confirm_kind');
	const base = emptyDeploy(ctx.name, ctx.hostId, ctx.workload, ctx.waitSec, null);
	if (!isConfirmationId(confirmationId)) return deployFailure(event, 400, { ...base, message: 'That confirmation is not valid.' });
	if (kind !== 'deploy' && kind !== 'rollback' && kind !== 'remove') {
		return deployFailure(event, 400, { ...base, message: 'That confirmation is not valid.' });
	}
	try {
		const payload = await apiFetch(
			event,
			`/v1/confirmations/${confirmationId}`,
			{ method: 'POST', body: JSON.stringify({ idempotency_key: ctx.key }) },
			IMAGE_ACTION_WAIT_MS
		);
		const history = await historyFor(event, id, ctx.name, ctx.hostId, started);
		if (kind === 'remove') {
			const parsed = parseRemovePayload(payload);
			if (!parsed) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
			if (parsed.kind === 'confirmation') {
				return present(event, {
					scope: 'catalog' as const,
					imageDeploy: deployState({ ...base, history, confirmation: parsed.confirmation, confirmKind: 'remove' })
				});
			}
			return present(event, { scope: 'catalog' as const, imageDeploy: deployState({ ...base, history, status: parsed.status }) });
		}
		const parsed = parseDeployPayload(payload);
		if (!parsed) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
		if (parsed.kind === 'confirmation') {
			return present(event, {
				scope: 'catalog' as const,
				imageDeploy: deployState({ ...base, history, confirmation: parsed.confirmation, confirmKind: kind })
			});
		}
		return present(event, {
			scope: 'catalog' as const,
			imageDeploy: deployState({ ...base, history, outcome: parsed.deploy, status: parsed.status, lines: parsed.lines })
		});
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return imageActionFailure(event, err, id, base, { capped: true, alwaysHistory: false, started });
	}
}

export async function catalogDeploys(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = named(form);
	const hostId = formText(form, 'host_id');
	const workload = formText(form, 'workload') === 'job' ? 'job' : 'service';
	const waitSec = waitOf(workload, formText(form, 'wait_sec'));
	if (!name) return fail(400, present(event, { scope: 'catalog' as const, message: 'That catalog entry is not valid.' }));
	if (!isUuid(hostId)) return fail(400, present(event, { scope: 'catalog' as const, message: 'Choose a host.' }));
	const base = emptyDeploy(name, hostId, workload, waitSec, null);
	try {
		const payload = await apiFetch(event, `/v1/projects/${id}/catalog/${name}/deploys?host_id=${hostId}`);
		const list = parseImageDeployList(payload);
		if (!list) return deployFailure(event, 502, { ...base, message: UNEXPECTED });
		return present(event, { scope: 'catalog' as const, imageDeploy: deployState({ ...base, history: list.items }) });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return deployFailure(event, apiStatus(err), { ...base, message: apiMessage(err) });
	}
}

export async function catalogEnvSets(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	try {
		const payload = await apiFetch(event, `/v1/projects/${id}/env-sets`);
		const list = parseEnvSetList(payload);
		if (!list) return fail(502, present(event, { scope: 'catalog' as const, catalogSets: [], catalogSetsMessage: UNEXPECTED }));
		const catalogSets: KnownSet[] = list.items.map((set) => ({ name: set.name, keys: set.keys.map((item) => item.key) }));
		return present(event, { scope: 'catalog' as const, catalogSets, catalogSetsMessage: null });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'catalog' as const, catalogSets: [], catalogSetsMessage: apiMessage(err) }));
	}
}

function lookupError(err: unknown): TagLookupError {
	if (!(err instanceof ApiError)) {
		console.error('bot api request failed');
		return 'failed';
	}
	const reason = err.payload != null && typeof err.payload === 'object' ? (err.payload as Record<string, unknown>).reason : undefined;
	return tagLookupError(err.status, err.code, err.msg, reason);
}

/** Registry tags for the Image field (api-v1 v1.16). `image` is a repository; the result is a message key, never registry text. */
export async function catalogImageTags(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const image = formText(await event.request.formData(), 'image');
	const empty = (state: TagLookupError): ImageTagsResult => ({ state, tags: [], truncated: false });
	if (imageRepository(image) !== image) return fail(400, present(event, { scope: 'catalog' as const, catalogImageTags: empty('failed') }));
	try {
		const parsed = parseImageTags(await apiFetch(event, `/v1/projects/${id}/image-tags?image=${encodeURIComponent(image)}`));
		if (!parsed) return fail(502, present(event, { scope: 'catalog' as const, catalogImageTags: empty('failed') }));
		const catalogImageTags: ImageTagsResult = { state: 'ready', tags: parsed.tags, truncated: parsed.truncated };
		return present(event, { scope: 'catalog' as const, catalogImageTags });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'catalog' as const, catalogImageTags: empty(lookupError(err)) }));
	}
}

/** The digest of a picked tag (api-v1 v1.16). `image` must be `<repo>:<tag>` with no digest. */
export async function catalogImageDigest(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const image = formText(await event.request.formData(), 'image');
	const empty = (state: TagLookupError): ImageDigestResult => ({ state, digest: null });
	if (!isTaggedRef(image)) return fail(400, present(event, { scope: 'catalog' as const, catalogImageDigest: empty('failed') }));
	try {
		const digest = parseImageDigest(await apiFetch(event, `/v1/projects/${id}/image-digest?image=${encodeURIComponent(image)}`));
		if (!digest) return fail(502, present(event, { scope: 'catalog' as const, catalogImageDigest: empty('failed') }));
		const catalogImageDigest: ImageDigestResult = { state: 'ready', digest };
		return present(event, { scope: 'catalog' as const, catalogImageDigest });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'catalog' as const, catalogImageDigest: empty(lookupError(err)) }));
	}
}

const PASTE_AGAIN = 'Paste the value again.';

/** The value of one pasted line as the API reads it: a leading `export ` dropped, quotes or an unquoted ` #` comment removed. */
function dotenvLineValue(line: string): string | null {
	const body = line.trim().replace(/^export\s+/, '');
	const eq = body.indexOf('=');
	if (eq === -1) return null;
	const raw = body.slice(eq + 1).trim();
	const quoted = /^(['"])(.*?)\1/.exec(raw);
	if (quoted) return quoted[2] ?? '';
	return raw.replace(/\s+#.*$/, '');
}

/** Every value in a paste, to redact from an API message. CRLF and lone CR line ends count as line breaks. */
function dotenvSecrets(dotenv: string): string[] {
	const secrets = dotenv === '' ? [] : [dotenv];
	for (const line of dotenv.split(/\r\n|\r|\n/)) {
		const value = dotenvLineValue(line);
		if (value) secrets.push(value);
	}
	return secrets;
}

function settingsFailure(event: RequestEvent, status: number, message: string, settingsField: 'dotenv' | 'token' | 'file' | null = null) {
	const text = settingsField ? `${message} ${PASTE_AGAIN}` : message;
	return fail(status, present(event, { scope: 'settings' as const, message: text, settingsField }));
}

/** An Add variables failure. `settingsName` is the set, so only that set's dialog shows it. */
function mergeFailure(event: RequestEvent, status: number, name: string, message: string) {
	return fail(status, present(event, { scope: 'settings' as const, message: `${message} ${PASTE_AGAIN}`, settingsField: 'merge' as const, settingsName: name }));
}

function fileSecrets(content: string): string[] {
	const secrets = content === '' ? [] : [content];
	for (const line of content.split('\n')) {
		const value = line.trim();
		if (value.length >= 8) secrets.push(value);
	}
	return secrets;
}

/** Redact the uncut API message, then apply the 500-character cap. `readEnvelope` has already capped `ApiError.msg`. */
function shownFileError(err: unknown, content: string): string {
	const full = err instanceof ApiError ? err.fullMsg : 'The API is unavailable.';
	if (!(err instanceof ApiError)) console.error('bot api request failed');
	const redacted = redactSubmitted(full, fileSecrets(content));
	return redacted.length > 500 ? redacted.slice(0, 500) : redacted;
}

function filePath(id: string, name: string): string | null {
	if (!isFileName(name)) return null;
	return `/v1/projects/${id}/files/${encodeURIComponent(name)}`;
}

/** `PUT …/env-sets/{set}` (api-v1 v1.19). The console never sends the default `replace`. */
type EnvSetPutBody = { dotenv: string; new_keys_secret: boolean; mode: 'create' | 'merge' };

export async function saveEnvSet(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = formText(form, 'set');
	const dotenv = formText(form, 'dotenv');
	if (!isSetName(name)) return settingsFailure(event, 400, 'Env set names use lowercase letters, digits, and hyphens.', 'dotenv');
	if (dotenv.length === 0 || new TextEncoder().encode(dotenv).byteLength > DOTENV_MAX) {
		return settingsFailure(event, 400, 'Paste a dotenv file of at most 256 KiB.', 'dotenv');
	}
	// `create` (api-v1 v1.19): an existing set is refused, never replaced.
	const body: EnvSetPutBody = { dotenv, new_keys_secret: formText(form, 'new_keys_secret') !== '0', mode: 'create' };
	try {
		await apiFetch(event, `/v1/projects/${id}/env-sets/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(body) });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError && err.code === 'conflict') {
			const message = `An env set named ${name} already exists. Use Add variables on that set.`;
			return fail(409, present(event, { scope: 'settings' as const, message, settingsField: 'dotenv' as const }));
		}
		return settingsFailure(event, apiStatus(err), redactSubmitted(apiMessage(err), dotenvSecrets(dotenv)), 'dotenv');
	}
}

/** Add variables (api-v1 v1.19 `merge`): pasted keys are added or updated, the other keys in the set stay. */
export async function mergeEnvSet(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = formText(form, 'set');
	const dotenv = formText(form, 'dotenv');
	if (!isSetName(name)) return settingsFailure(event, 400, 'That env set is not valid.');
	if (dotenv.length === 0 || new TextEncoder().encode(dotenv).byteLength > DOTENV_MAX) {
		return mergeFailure(event, 400, name, 'Paste the variables, at most 256 KiB.');
	}
	const body: EnvSetPutBody = { dotenv, new_keys_secret: formText(form, 'new_keys_secret') !== '0', mode: 'merge' };
	try {
		await apiFetch(event, `/v1/projects/${id}/env-sets/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(body) });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return mergeFailure(event, apiStatus(err), name, redactSubmitted(apiMessage(err), dotenvSecrets(dotenv)));
	}
}

function envKeyPath(id: string, name: string, key: string): string | null {
	if (!isSetName(name) || !ENV_KEY.test(key)) return null;
	return `/v1/projects/${id}/env-sets/${encodeURIComponent(name)}/keys/${encodeURIComponent(key)}`;
}

/** The value stays in the body. It is never put in the URL or in the error text. */
function envValueError(value: string): string | null {
	if (value.includes('\0') || /[\n\r]/.test(value) || new TextEncoder().encode(value).byteLength > 4096) {
		return 'A value cannot contain a line break or NUL, and must be at most 4096 bytes.';
	}
	return null;
}

export async function putEnvKey(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = formText(form, 'set');
	const key = formText(form, 'key');
	const value = formText(form, 'value');
	const path = envKeyPath(id, name, key);
	if (!path) return settingsFailure(event, 400, 'That env set key is not valid.');
	// api-v1 v1.27 item 1: Generate (One variable, new keys only) sends no value; the API makes one.
	const kind = formText(form, 'generate_kind');
	if (kind !== '') {
		if (!isGenerateKind(kind)) return settingsFailure(event, 400, 'Choose what to generate.');
		try {
			await apiFetch(event, path, { method: 'PUT', body: JSON.stringify(generateRequestBody(kind)) });
			redirect(303, backTo(event.url));
		} catch (err) {
			if (isRedirect(err) || isHttpError(err)) throw err;
			return settingsFailure(event, apiStatus(err), apiMessage(err));
		}
	}
	const invalid = envValueError(value);
	if (invalid) return settingsFailure(event, 400, invalid);
	const adding = formText(form, 'op') === 'add';
	const payload: { value: string; is_secret?: boolean } = { value };
	if (adding) payload.is_secret = formText(form, 'is_secret') !== '0';
	try {
		await apiFetch(event, path, { method: 'PUT', body: JSON.stringify(payload) });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), redactSubmitted(apiMessage(err), value === '' ? [] : [value]));
	}
}

/**
 * Show a generated value once (api-v1 v1.27 item 3). The value is only in this action's result, which the
 * reveal dialog reads from its own fetch and never applies to the page: it is not in page data, `form`,
 * a URL, or a log. A post that is not that fetch (no `x-sveltekit-action` header, or an Accept without
 * JSON, as a form post without JavaScript sends) is refused before the API is called, so the value never
 * renders into a page. Failures use `revealError`, not the settings message.
 */
export async function revealEnvKey(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const key = formText(form, 'key');
	const path = envKeyPath(id, formText(form, 'set'), key);
	if (!path) return fail(400, { scope: 'settings' as const, revealError: 'That env set key is not valid.' });
	const accept = event.request.headers.get('accept') ?? '';
	if (event.request.headers.get('x-sveltekit-action') !== 'true' || !accept.includes('application/json')) {
		return fail(400, { scope: 'settings' as const, revealError: 'Showing a value needs JavaScript.' });
	}
	try {
		const payload = await apiFetch(event, `${path}/reveal`, { method: 'POST', body: '{}' });
		const item = payload != null && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
		if (!item || item.key !== key || typeof item.value !== 'string') return fail(502, { scope: 'settings' as const, revealError: revealFailedText(key) });
		return { scope: 'settings' as const, revealed: { key, value: item.value } };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		// An API answer (a conflict: already shown or not generated) carries its message, which names the key only.
		// A timeout or a lost answer may have used up the one-time view, so it says how to get a new value.
		const message = apiMessage(err);
		const answered = err instanceof ApiError && !stillRunning(err) && err.code !== 'unavailable';
		return fail(apiStatus(err), { scope: 'settings' as const, revealError: answered ? message : revealFailedText(key) });
	}
}

/**
 * Show a generated entry-private value once (api-v1 v1.33), for the Secrets band's reveal dialog fetch only, as
 * revealEnvKey: the value goes to that fetch and never into page data or `form`. The dialog posts `set` `@<entry>`.
 */
export async function revealPrivateKey(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const key = formText(form, 'key');
	const entry = privateSetEntry(formText(form, 'set'));
	const path = entry == null ? null : privateKeyRevealPath(id, entry, key);
	if (!path) return fail(400, { scope: 'catalog' as const, revealError: 'That entry key is not valid.' });
	const accept = event.request.headers.get('accept') ?? '';
	if (event.request.headers.get('x-sveltekit-action') !== 'true' || !accept.includes('application/json')) {
		return fail(400, { scope: 'catalog' as const, revealError: 'Showing a value needs JavaScript.' });
	}
	try {
		const payload = await apiFetch(event, path, { method: 'POST', body: '{}' });
		const item = payload != null && typeof payload === 'object' ? (payload as Record<string, unknown>) : null;
		if (!item || item.key !== key || typeof item.value !== 'string') return fail(502, { scope: 'catalog' as const, revealError: revealFailedText(key) });
		return { scope: 'catalog' as const, revealed: { key, value: item.value } };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		// As revealEnvKey: an API answer (already shown, not generated, not found) names the key only; a lost answer
		// may have used up the one view, so it says how to get a new value.
		const answered = err instanceof ApiError && !stillRunning(err) && err.code !== 'unavailable';
		return fail(apiStatus(err), { scope: 'catalog' as const, revealError: answered ? apiMessage(err) : revealFailedText(key) });
	}
}

export async function deleteEnvKey(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const path = envKeyPath(id, formText(form, 'set'), formText(form, 'key'));
	if (!path) return settingsFailure(event, 400, 'That env set key is not valid.');
	try {
		await apiFetch(event, path, { method: 'DELETE' });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), apiMessage(err));
	}
}

/** Mark plain keys secret (api-v1 v1.17). The API checks the keys; the reload shows the previews. */
export async function makeEnvKeysSecret(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = formText(form, 'set');
	const keys = form.getAll('keys').filter((item): item is string => typeof item === 'string');
	if (!isSetName(name) || keys.length === 0) return settingsFailure(event, 400, 'Choose a plain key to make secret.');
	try {
		await apiFetch(event, `/v1/projects/${id}/env-sets/${encodeURIComponent(name)}/secret`, { method: 'POST', body: JSON.stringify({ keys }) });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), apiMessage(err));
	}
}

export async function deleteEnvSet(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const name = formText(await event.request.formData(), 'set');
	if (!isSetName(name)) return settingsFailure(event, 400, 'That env set is not valid.');
	try {
		await apiFetch(event, `/v1/projects/${id}/env-sets/${name}`, { method: 'DELETE' });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		if (err instanceof ApiError && err.code === 'conflict') {
			return settingsFailure(
				event,
				409,
				`Env set ${name} is still used by catalog entries. Remove it from those entries first, or delete its variables one by one.`
			);
		}
		return settingsFailure(event, apiStatus(err), apiMessage(err));
	}
}

function registryPath(id: string, host: string): string | null {
	if (!isRegistryHost(host)) return null;
	if (/[%/?#\s]/.test(host)) return null;
	return `/v1/projects/${id}/registries/${host}`;
}

export async function saveRegistry(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const host = formText(form, 'host').trim();
	const username = formText(form, 'username');
	const token = formText(form, 'token');
	const path = registryPath(id, host);
	if (!path) return settingsFailure(event, 400, 'Enter a registry host, such as ghcr.io.', 'token');
	if (username.length === 0 || username.length > 255 || /[\u0000\n\r]/.test(username)) {
		return settingsFailure(event, 400, 'Enter a username.', 'token');
	}
	if (token.length === 0 || new TextEncoder().encode(token).byteLength > 4096 || /[\u0000\n\r]/.test(token)) {
		return settingsFailure(event, 400, 'Enter a token.', 'token');
	}
	try {
		const payload = await apiFetch(event, path, { method: 'PUT', body: JSON.stringify({ username, token }) });
		if (!parseRegistry(payload)) return settingsFailure(event, 502, UNEXPECTED, 'token');
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), redactSubmitted(apiMessage(err), token === '' ? [] : [token]), 'token');
	}
}

export async function deleteRegistry(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const host = formText(await event.request.formData(), 'host').trim();
	const path = registryPath(id, host);
	if (!path) return settingsFailure(event, 400, 'That registry is not valid.');
	try {
		const payload = await apiFetch(event, path, { method: 'DELETE' });
		// Disconnect of a GitHub login (api-v1 v1.29 item 4): a failed revoke at GitHub is reported, not hidden.
		if (payload != null && typeof payload === 'object' && (payload as Record<string, unknown>).revoked === false) {
			return present(event, { scope: 'settings' as const, githubNote: 'revoke_failed' as const });
		}
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), apiMessage(err));
	}
}

export async function catalogFiles(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	try {
		const list = parseProjectFileList(await apiFetch(event, `/v1/projects/${id}/files`));
		if (!list) return fail(502, present(event, { scope: 'catalog' as const, catalogFiles: [], catalogFilesMessage: UNEXPECTED }));
		const catalogFiles = list.items.map((file) => ({ name: file.name }));
		return present(event, { scope: 'catalog' as const, catalogFiles, catalogFilesMessage: null });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'catalog' as const, catalogFiles: [], catalogFilesMessage: apiMessage(err) }));
	}
}

export async function saveProjectFile(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const form = await event.request.formData();
	const name = formText(form, 'name').trim();
	// The Files dialog is a paste box: the browser sends its line breaks as CRLF; the file gets LF, as on the fill-file page.
	const content = pastedText(formText(form, 'content'));
	const secret = formText(form, 'secret') !== '0';
	const path = filePath(id, name);
	if (!path) return settingsFailure(event, 400, 'File names use lowercase letters, digits, dots, underscores, and hyphens.', 'file');
	if (content.length === 0 || content.includes('\0') || byteLength(content) > FILE_MAX) {
		return settingsFailure(event, 400, 'Paste a text file of 1 to 64 KiB, with no NUL.', 'file');
	}
	try {
		const payload = await apiFetch(event, path, { method: 'PUT', body: JSON.stringify({ content, secret }) });
		if (!parseProjectFile(payload)) return settingsFailure(event, 502, UNEXPECTED, 'file');
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), shownFileError(err, content), 'file');
	}
}

export async function viewProjectFile(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const name = formText(await event.request.formData(), 'name').trim();
	const path = filePath(id, name);
	if (!path) return settingsFailure(event, 400, 'That file is not valid.');
	try {
		const file = parseProjectFileContent(await apiFetch(event, path));
		if (!file) return settingsFailure(event, 502, UNEXPECTED);
		return present(event, { scope: 'settings' as const, viewedFile: { name: file.name, content: file.content } });
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return settingsFailure(event, apiStatus(err), apiMessage(err));
	}
}

export async function deleteProjectFile(event: RequestEvent) {
	requireUser(event);
	const id = projectIdOf(event.params);
	const name = formText(await event.request.formData(), 'name').trim();
	const path = filePath(id, name);
	if (!path) return settingsFailure(event, 400, 'That file is not valid.');
	try {
		await apiFetch(event, path, { method: 'DELETE' });
		redirect(303, backTo(event.url));
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return fail(apiStatus(err), present(event, { scope: 'settings' as const, message: apiMessage(err), settingsName: name }));
	}
}

export async function loadProjectSettings(event: RequestEvent, id: string): Promise<{
	envSets: EnvSet[];
	envSetsMessage: string | null;
	registries: Registry[];
	registriesMessage: string | null;
	/** api-v1 v1.29 item 5: the API has Connect GitHub configured. */
	githubConnect: boolean;
	files: ProjectFile[];
	filesMessage: string | null;
}> {
	const [env, registries, files] = await Promise.all([readEnvSets(event, id), readRegistries(event, id), readProjectFiles(event, id)]);
	return {
		envSets: env.items,
		envSetsMessage: env.message,
		registries: registries.items,
		registriesMessage: registries.message,
		githubConnect: registries.githubConnect,
		files: files.items,
		filesMessage: files.message
	};
}

async function readEnvSets(event: RequestEvent, id: string) {
	try {
		const list = parseEnvSetList(await apiFetch(event, `/v1/projects/${id}/env-sets`));
		if (!list) return { items: [], message: UNEXPECTED };
		return { items: list.items, message: null };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { items: [], message: apiMessage(err) };
	}
}

async function readProjectFiles(event: RequestEvent, id: string) {
	try {
		const list = parseProjectFileList(await apiFetch(event, `/v1/projects/${id}/files`));
		if (!list) return { items: [], message: UNEXPECTED };
		return { items: list.items, message: null };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { items: [], message: apiMessage(err) };
	}
}

/** The registries list's `github_connect` (api-v1 v1.29 item 5). Anything but `true` (an older API) hides Connect GitHub. */
export function githubConnectOf(payload: unknown): boolean {
	return payload != null && typeof payload === 'object' && (payload as Record<string, unknown>).github_connect === true;
}

async function readRegistries(event: RequestEvent, id: string) {
	try {
		const payload = await apiFetch(event, `/v1/projects/${id}/registries`);
		const list = parseRegistryList(payload);
		const githubConnect = githubConnectOf(payload);
		if (!list) return { items: [], message: UNEXPECTED, githubConnect };
		return { items: list.items, message: null, githubConnect };
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { items: [], message: apiMessage(err), githubConnect: false };
	}
}
