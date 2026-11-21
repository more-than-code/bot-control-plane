/**
 * Host config files (api-v1 v1.44, umbrella spec `docs/spec-host-config-files.md` §2.8): the host page's
 * Config files tab. Types, the words the tab shows, the line diff and the "Insert Deploybot link" line.
 * Pure: shared by the server load, the form actions and the components.
 */

export type HostFileStatus = 'in_sync' | 'different' | 'missing' | 'wrong_owner' | 'unknown';

export type HostFileApplyOutcome =
	| 'succeeded'
	| 'unchanged'
	| 'check_failed'
	| 'rolled_back'
	| 'rollback_failed'
	| 'forbidden_spec';

export type HostFileLastApply = {
	version: number;
	outcome: string;
	stage: string | null;
	reason: string | null;
	at: string;
};

/** One row of `GET /v1/orgs/{org}/hosts/{host}/files`: what any member who sees the host may read. */
export type HostFile = {
	name: string;
	path: string;
	unit: string;
	secret: boolean;
	applied_version: number | null;
	latest_version: number | null;
	status: HostFileStatus;
	last_apply: HostFileLastApply | null;
};

/** One saved version. `sha256` is present for a plain file only. `by` is the saver's user id. */
export type HostFileVersion = {
	version: number;
	size: number;
	sha256: string | null;
	by: string;
	at: string;
	applied: boolean;
};

/** A secret file's key: its name and whether it has a value. Never the value. */
export type HostFileKey = { name: string; set: boolean };

/** `GET …/files/{name}` for org owners and admins. */
export type HostFileDetail =
	| { secret: false; content: string; versions: HostFileVersion[] }
	| { secret: true; keys: HostFileKey[]; versions: HostFileVersion[] };

/** The `POST …/apply` answer (also the `worker_busy` payload). */
export type HostFileApplyResult = {
	outcome: string;
	stage: string | null;
	reason: string | null;
	took_ms: number | null;
};

/** What an apply form action returns to the page. `busy` is the 409 `worker_busy` answer. */
export type HostFileApplyData = {
	name: string;
	version: number;
	busy: boolean;
	result: HostFileApplyResult | null;
};

/** The selected file for an org owner or admin. `view` is one version's content for View or Diff. */
export type HostFileSelected = {
	file: HostFile;
	detail: HostFileDetail | null;
	/** The applied version's content (plain files), the diff base; null when nothing is applied or it did not load. */
	appliedContent: string | null;
	view: { version: number; content: string; mode: 'view' | 'diff' } | null;
	message: string | null;
};

export type HostFilesTab = {
	/** The host has an org: the routes exist. */
	available: boolean;
	/** Org owner or admin: content, keys and actions. */
	canEdit: boolean;
	orgId: string | null;
	items: HostFile[];
	message: string | null;
	selected: HostFileSelected | null;
	/** `files.name` named no file in the list. */
	notFound: boolean;
	/** Applies this console started on the host and still tracks (org owners and admins only). */
	runs: HostFileRun[];
};

/** How a tracked apply ended: the API's answer, or the message of the error it gave. */
export type HostFileRunDone = { apply: HostFileApplyData | null; message: string | null };

/**
 * An apply the console started and is still waiting for (`done` null), or one that ended in the last 10 minutes.
 * The apply action returns after about 25 s with `running`; the tab then polls `/hosts/<id>/file-applies`.
 */
export type HostFileRun = {
	name: string;
	version: number;
	startedAt: string;
	done: HostFileRunDone | null;
	/** The console lost the API's answer (a timeout or network error after the call was sent): read the list's last apply. */
	unknown?: boolean;
};

/** `^[a-z][a-z0-9-]{0,31}$` (spec §2.1). */
export const HOST_FILE_NAME = /^[a-z][a-z0-9-]{0,31}$/;
/** A secret file's key (api-v1 v1.44 item 5). */
export const HOST_FILE_KEY = /^[A-Z_][A-Z0-9_]{0,63}$/;
export const HOST_FILE_MAX_BYTES = 65_536;
export const HOST_FILE_VALUE_MAX_BYTES = 4096;
export const HOST_FILE_MAX_KEYS = 128;

const utf8 = new TextEncoder();

export function byteLength(value: string): number {
	return utf8.encode(value).byteLength;
}

export function isHostFileName(value: unknown): value is string {
	return typeof value === 'string' && HOST_FILE_NAME.test(value);
}

/** The tab's link for one file: `/hosts/<id>?tab=files&files.name=<name>` (also the bell's `host_file` href). */
export function hostFileHref(hostId: string, name: string): string {
	return `/hosts/${hostId}?tab=files&files.name=${encodeURIComponent(name)}`;
}

/** "in sync", "different", … with the StatusBadge colour it borrows. */
export function hostFileStatusBadge(status: string): { status: string; text: string } {
	switch (status) {
		case 'in_sync':
			return { status: 'up', text: 'in sync' };
		case 'different':
			return { status: 'asleep', text: 'different' };
		case 'missing':
			return { status: 'down', text: 'missing' };
		case 'wrong_owner':
			return { status: 'down', text: 'wrong owner' };
		default:
			return { status: 'unknown', text: 'unknown' };
	}
}

/** The last apply's outcome as a StatusBadge: the known outcomes keep their colour; `check_failed` reads "check failed". */
export function hostFileOutcomeBadge(outcome: string): { status: string; text: string } {
	if (outcome === 'check_failed') return { status: 'failed', text: 'check failed' };
	if (outcome === 'worker_busy') return { status: 'conflict', text: 'worker busy' };
	const known = ['succeeded', 'unchanged', 'rolled_back', 'rollback_failed', 'forbidden_spec'];
	if (known.includes(outcome)) return { status: outcome, text: outcome.replaceAll('_', ' ') };
	return { status: 'unknown', text: outcome.replaceAll('_', ' ') || 'unknown' };
}

export function versionText(version: number | null): string {
	return version == null ? 'none' : `v${version}`;
}

/** One piece of a message: plain text, or a reason token shown in monospace. */
export type MessagePart = { text: string; mono?: boolean };

export type ApplyOutcomeMessage = { kind: 'success' | 'info' | 'error'; parts: MessagePart[] };

/** The note while an apply runs (the API waits up to 610 s). */
export function applyProgressText(unit: string): string {
	return `Checking the file, restarting ${unit}, confirming it is healthy… this can take a few minutes.`;
}

export const WORKER_BUSY_TEXT = 'The program on this host is running a job. Applying now interrupts it.';

/** The outcome of an apply in words, with the host's reason token (`bad_repo`, `not_connected`, …) as is. */
export function applyOutcomeMessage(version: number, result: HostFileApplyResult): ApplyOutcomeMessage {
	const reason = result.reason || 'no reason given';
	switch (result.outcome) {
		case 'succeeded':
			return { kind: 'success', parts: [{ text: `Applied v${version}.` }] };
		case 'unchanged':
			return { kind: 'info', parts: [{ text: 'Already on the host; nothing restarted.' }] };
		case 'check_failed':
			return { kind: 'error', parts: [{ text: 'The check refused the file: ' }, { text: reason, mono: true }] };
		case 'rolled_back':
			return {
				kind: 'error',
				parts: [
					{ text: 'The new file failed (' },
					{ text: `${result.stage || 'unknown'}: ${reason}`, mono: true },
					{ text: '); the previous file is back.' }
				]
			};
		case 'rollback_failed':
			return { kind: 'error', parts: [{ text: 'The new file failed and the previous file could not be restored. Check the host.' }] };
		case 'forbidden_spec':
			if (result.stage === 'exec') {
				return {
					kind: 'error',
					parts: [{ text: "A program this file's check runs is not safe to run (see runner.toml); fix its permissions on the host." }]
				};
			}
			return { kind: 'error', parts: [{ text: 'The host refused this file under its policy (runner.toml).' }] };
		default:
			return { kind: 'error', parts: [{ text: 'The host answered ' }, { text: result.outcome || 'nothing', mono: true }, { text: '.' }] };
	}
}

/** "Apply" for the latest version (or a newer one), "Roll back to vN" for an older one. */
export function applyLabel(version: number, latest: number | null): string {
	return latest != null && version < latest ? `Roll back to v${version}` : `Apply v${version}`;
}

// ---- Line diff -------------------------------------------------------------------------------------------

export type DiffLine = { op: ' ' | '+' | '-'; text: string };
/** A diff to show: changed lines with up to `context` unchanged lines around them; `skip` stands for the rest. */
export type DiffItem = DiffLine | { op: 'skip'; count: number };

/** Above this many cells (lines × lines after trimming the common ends) the middle is shown as removed then added. */
const DIFF_CELL_CAP = 250_000;

function splitLines(text: string): string[] {
	return text.length === 0 ? [] : text.split('\n');
}

/** Lines of `before` and `after` with `-`, `+` or ` ` (longest common subsequence on whole lines). */
export function lineDiff(before: string, after: string): DiffLine[] {
	const a = splitLines(before);
	const b = splitLines(after);
	let head = 0;
	while (head < a.length && head < b.length && a[head] === b[head]) head += 1;
	let tail = 0;
	while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail += 1;
	const midA = a.slice(head, a.length - tail);
	const midB = b.slice(head, b.length - tail);
	const out: DiffLine[] = a.slice(0, head).map((text) => ({ op: ' ', text }));
	if (midA.length * midB.length > DIFF_CELL_CAP) {
		for (const text of midA) out.push({ op: '-', text });
		for (const text of midB) out.push({ op: '+', text });
	} else {
		out.push(...middleDiff(midA, midB));
	}
	for (const text of a.slice(a.length - tail)) out.push({ op: ' ', text });
	return out;
}

function middleDiff(a: string[], b: string[]): DiffLine[] {
	const n = a.length;
	const m = b.length;
	// lcs[i][j]: common lines of a[i..] and b[j..], one flat array.
	const width = m + 1;
	const lcs = new Uint32Array((n + 1) * width);
	for (let i = n - 1; i >= 0; i -= 1) {
		for (let j = m - 1; j >= 0; j -= 1) {
			lcs[i * width + j] = a[i] === b[j] ? lcs[(i + 1) * width + j + 1] + 1 : Math.max(lcs[(i + 1) * width + j], lcs[i * width + j + 1]);
		}
	}
	const out: DiffLine[] = [];
	let i = 0;
	let j = 0;
	while (i < n && j < m) {
		if (a[i] === b[j]) {
			out.push({ op: ' ', text: a[i] });
			i += 1;
			j += 1;
		} else if (lcs[(i + 1) * width + j] >= lcs[i * width + j + 1]) {
			out.push({ op: '-', text: a[i] });
			i += 1;
		} else {
			out.push({ op: '+', text: b[j] });
			j += 1;
		}
	}
	for (; i < n; i += 1) out.push({ op: '-', text: a[i] });
	for (; j < m; j += 1) out.push({ op: '+', text: b[j] });
	return out;
}

export function diffChanged(lines: readonly DiffLine[]): boolean {
	return lines.some((line) => line.op !== ' ');
}

/** Keep `context` unchanged lines around each change; longer unchanged runs become one `skip`. */
export function diffHunks(lines: readonly DiffLine[], context = 3): DiffItem[] {
	const keep = new Array<boolean>(lines.length).fill(false);
	lines.forEach((line, index) => {
		if (line.op === ' ') return;
		for (let k = Math.max(0, index - context); k <= Math.min(lines.length - 1, index + context); k += 1) keep[k] = true;
	});
	const out: DiffItem[] = [];
	let skipped = 0;
	lines.forEach((line, index) => {
		if (keep[index]) {
			if (skipped > 0) out.push({ op: 'skip', count: skipped });
			skipped = 0;
			out.push(line);
		} else {
			skipped += 1;
		}
	});
	if (skipped > 0) out.push({ op: 'skip', count: skipped });
	return out;
}

// ---- managed_url -----------------------------------------------------------------------------------------

const MANAGED_URL_LINE = /^\s*managed_url\s*=/m;

/** A real `[name]` or `[[name]]` header line (bare, dotted or quoted keys, an optional comment), not an array line. */
const TOML_HEADER = /^[ \t]*(?:\[[ \t]*[A-Za-z0-9_.\-"' ]+[ \t]*\]|\[\[[ \t]*[A-Za-z0-9_.\-"' ]+[ \t]*\]\])[ \t]*(?:#.*)?$/m;

export function hasManagedUrl(text: string): boolean {
	return MANAGED_URL_LINE.test(text);
}

export function managedUrlLine(url: string): string {
	return `managed_url = "${url.replaceAll('\\', '\\\\').replaceAll('"', '\\"')}"`;
}

/**
 * The editor text with a `managed_url = "<url>"` line added, used only when the admin clicks "Insert Deploybot
 * link". The line goes before the first `[table]` or `[[table]]` header line so it stays a top-level TOML key; with no table it is
 * appended at the end. The rest of the text is kept byte for byte.
 */
export function insertManagedUrl(text: string, url: string): string {
	const line = managedUrlLine(url);
	const header = TOML_HEADER.exec(text);
	if (header) return `${text.slice(0, header.index)}${line}\n${text.slice(header.index)}`;
	if (text.length === 0) return `${line}\n`;
	return text.endsWith('\n') ? `${text}${line}\n` : `${text}\n${line}\n`;
}

// ---- secret keys -----------------------------------------------------------------------------------------

/** The rule check for a secret value before posting: null when it is fine, else the message. */
export function secretValueProblem(value: string): string | null {
	if (value.length === 0) return 'Enter a value.';
	if (/[\r\n]/.test(value)) return 'A value is one line.';
	if (value.includes('\0')) return 'A value cannot contain a NUL character.';
	if (byteLength(value) > HOST_FILE_VALUE_MAX_BYTES) return `A value is at most ${HOST_FILE_VALUE_MAX_BYTES} bytes.`;
	return null;
}

export function secretKeyProblem(key: string): string | null {
	if (!HOST_FILE_KEY.test(key)) return 'A key is upper-case letters, digits and underscores, starting with a letter or underscore (at most 64).';
	return null;
}

// ---- form results ----------------------------------------------------------------------------------------

/** What the tab reads from the page's `form` after a files action. Other actions' results give all nulls. */
export type HostFilesForm = {
	message: string | null;
	/** The dialog a failure belongs to: `key:<KEY>` or `key:add`. */
	field: string | null;
	/** A failed Save's text, so the editor keeps it. */
	content: string | null;
	apply: HostFileApplyData | null;
	saved: { name: string; version: number } | null;
	/** The apply did not end within the action's wait; the tab polls for it. */
	running: HostFileRun | null;
};

export function hostFilesForm(form: unknown): HostFilesForm {
	const out: HostFilesForm = { message: null, field: null, content: null, apply: null, saved: null, running: null };
	if (form == null || typeof form !== 'object') return out;
	const record = form as Record<string, unknown>;
	if (typeof record.filesMessage === 'string') out.message = record.filesMessage;
	if (typeof record.filesField === 'string') out.field = record.filesField;
	if (typeof record.content === 'string') out.content = record.content;
	if (record.hostFileApply != null && typeof record.hostFileApply === 'object') out.apply = record.hostFileApply as HostFileApplyData;
	if (record.hostFileSaved != null && typeof record.hostFileSaved === 'object') out.saved = record.hostFileSaved as HostFilesForm['saved'];
	if (record.hostFileRunning != null && typeof record.hostFileRunning === 'object') out.running = record.hostFileRunning as HostFileRun;
	return out;
}

/** The query for one file, optionally one version in View or Diff mode. */
export function hostFileQuery(hostId: string, name: string, version?: number, mode?: 'view' | 'diff'): string {
	const base = hostFileHref(hostId, name);
	return version == null ? base : `${base}&files.v=${version}&files.mode=${mode ?? 'view'}`;
}

/** The newest saved version: the list's `latest_version` or the highest in the version list, whichever is higher. */
export function latestVersion(listLatest: number | null, versions: readonly { version: number }[]): number | null {
	let latest = listLatest;
	for (const item of versions) if (latest == null || item.version > latest) latest = item.version;
	return latest;
}

// ---- secret staging --------------------------------------------------------------------------------------

/**
 * A secret file's changes staged in the page before one Save: a value to set (held in the page's memory only,
 * never in load data or the DOM) or a key to remove.
 */
export type StagedChange = { op: 'set'; value: string } | { op: 'remove' };
export type SecretChanges = { set: Record<string, string>; remove: string[] };

export function stagedChanges(staged: Readonly<Record<string, StagedChange>>): SecretChanges {
	const set: Record<string, string> = {};
	const remove: string[] = [];
	for (const key of Object.keys(staged).sort()) {
		const change = staged[key];
		if (change.op === 'set') set[key] = change.value;
		else remove.push(key);
	}
	return { set, remove };
}

/** How many keys the file keeps after the staged changes (it must keep at least one). */
export function keysAfter(keys: readonly HostFileKey[], staged: Readonly<Record<string, StagedChange>>): number {
	const names = new Set(keys.map((key) => key.name));
	for (const [key, change] of Object.entries(staged)) {
		if (change.op === 'set') names.add(key);
		else names.delete(key);
	}
	return names.size;
}

/** The `changes` field the Save posts, checked again on the server. A string is the problem to show. */
export function parseSecretChanges(raw: string): SecretChanges | string {
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return 'Nothing to save.';
	}
	if (value == null || typeof value !== 'object' || Array.isArray(value)) return 'Nothing to save.';
	const record = value as Record<string, unknown>;
	const rawSet = record.set ?? {};
	const rawRemove = record.remove ?? [];
	if (rawSet == null || typeof rawSet !== 'object' || Array.isArray(rawSet) || !Array.isArray(rawRemove)) return 'Nothing to save.';
	const set: Record<string, string> = {};
	for (const [key, item] of Object.entries(rawSet as Record<string, unknown>)) {
		const problem = secretKeyProblem(key) ?? (typeof item === 'string' ? secretValueProblem(item) : 'Enter a value.');
		if (problem) return `${key}: ${problem}`;
		set[key] = item as string;
	}
	const remove: string[] = [];
	for (const key of rawRemove) {
		if (typeof key !== 'string' || secretKeyProblem(key) != null) return 'A removed key is not valid.';
		if (key in set) return `${key} is both set and removed.`;
		if (!remove.includes(key)) remove.push(key);
	}
	const count = Object.keys(set).length + remove.length;
	if (count === 0) return 'Nothing to save.';
	if (count > HOST_FILE_MAX_KEYS) return `At most ${HOST_FILE_MAX_KEYS} keys at once.`;
	return { set, remove };
}

// ---- apply watch -----------------------------------------------------------------------------------------

/** An apply the tab waits for, with the list's last apply time when it started (the fallback if the run is lost). */
export type ApplyWatch = { name: string; version: number; since: number; lastApplyAt: string | null };

/** How long the tab waits for a run it cannot see end before it gives up (the API waits up to 610 s). */
export const APPLY_LOST_MS = 12 * 60_000;
/** A last apply this long before the watch started still counts as its result (clock skew between the servers). */
const APPLY_SKEW_MS = 5000;
export const APPLY_POLL_MS = 3000;
/** One more read of the tab this long after an apply settles (the host's status frame may land a moment later). */
export const APPLY_REREAD_MS = 5000;
export const APPLY_LOST_TEXT = 'The console lost track of this apply. Check Last apply in the list.';

export type WatchOutcome = { name: string; done: HostFileRunDone };

/**
 * One poll step. `runs` is the console's tracked runs (null when the read failed). A run with `done` settles its
 * watch. A watch with no run (the console restarted) or an `unknown` one settles when the list's last apply
 * changes or is newer than the watch, or gives up after APPLY_LOST_MS.
 */
export function stepWatches(
	watches: readonly ApplyWatch[],
	runs: readonly HostFileRun[] | null,
	items: readonly HostFile[],
	now: number
): { watches: ApplyWatch[]; outcomes: WatchOutcome[]; lost: boolean } {
	const kept: ApplyWatch[] = [];
	const outcomes: WatchOutcome[] = [];
	let lost = false;
	for (const watch of watches) {
		const run = runs?.find((item) => item.name === watch.name) ?? null;
		if (run?.done) {
			outcomes.push({ name: watch.name, done: run.done });
			continue;
		}
		if ((run && !run.unknown) || runs == null) {
			if (now - watch.since > APPLY_LOST_MS) outcomes.push({ name: watch.name, done: { apply: null, message: APPLY_LOST_TEXT } });
			else kept.push(watch);
			continue;
		}
		const last = items.find((item) => item.name === watch.name)?.last_apply ?? null;
		// The run is gone (console restart) or its result is unknown: the API's apply row in the list settles it.
		const lastAt = last ? Date.parse(last.at) : Number.NaN;
		if (last && (last.at !== watch.lastApplyAt || lastAt >= watch.since - APPLY_SKEW_MS)) {
			const result = { outcome: last.outcome, stage: last.stage, reason: last.reason, took_ms: null };
			outcomes.push({ name: watch.name, done: { apply: { name: watch.name, version: last.version, busy: last.outcome === 'worker_busy', result }, message: null } });
		} else if (now - watch.since > APPLY_LOST_MS) {
			outcomes.push({ name: watch.name, done: { apply: null, message: APPLY_LOST_TEXT } });
		} else {
			lost = true;
			kept.push(watch);
		}
	}
	return { watches: kept, outcomes, lost };
}

// ---- editor guards ---------------------------------------------------------------------------------------

/**
 * Leaving an editor with unsaved changes: `go`, `prompt` (cancel a page unload, so the browser asks), or `warn`
 * (stop this link once and show a note; the next one goes). Links that keep the same file open keep the editor
 * and its changes, and the editor's own Save never asks.
 */
export function unsavedLeave(input: {
	unsaved: boolean;
	saving: boolean;
	type: string;
	to: URL | null;
	here: URL;
	name: string;
	warned: boolean;
}): 'go' | 'prompt' | 'warn' {
	if (!input.unsaved || input.saving) return 'go';
	const to = input.to;
	if (to && to.pathname === input.here.pathname && to.searchParams.get('files.name') === input.name) return 'go';
	if (input.type === 'leave') return 'prompt';
	return input.warned ? 'go' : 'warn';
}

/**
 * The staged changes a secret file's Save may post: only an editor's own file's (`owner` is the file the editor
 * was opened for), never empty and never leaving the file without a key. Null means post nothing.
 */
export function secretSaveBody(
	owner: string,
	name: string,
	keys: readonly HostFileKey[],
	staged: Readonly<Record<string, StagedChange>>
): SecretChanges | null {
	if (owner !== name || Object.keys(staged).length === 0 || keysAfter(keys, staged) === 0) return null;
	return stagedChanges(staged);
}

/** A plain file's Save may post the editor text only for the file it was opened for, and only when it changed. */
export function plainSaveAllowed(owner: string, name: string, draft: string, content: string): boolean {
	return owner === name && draft !== content;
}
