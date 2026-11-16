/**
 * The file-request fill page (api-v1 v1.30, spec-file-request §3.3). An agent asked for a managed
 * file; the person who asked pastes it or picks it from disk here. The content goes from this form to
 * the API and nowhere else: it is never in page data, `form`, a URL, or a log. The ids, states and
 * most texts are the value-request page's (`$lib/fill-request`).
 */
import type { SubmitFunction } from '@sveltejs/kit';
import { FILL_TEXT } from '$lib/fill-request';
import { boundedText, isRequestId, isRequestState, UUID, type RequestState } from '$lib/request-shape';

/** The v1.9 managed file name rule. */
const FILE_NAME = /^[a-z0-9][a-z0-9._-]{0,63}$/;
/** The API's conflict for a file that exists: it leaves the request pending (api-v1 v1.30 item 5). */
const FILE_EXISTS = /^file [a-z0-9][a-z0-9._-]{0,63} already exists; replace it in the console$/;

/** The v1.9 content limit, in UTF-8 bytes. */
export const FILE_MAX_BYTES = 65536;

export type FileFillState = RequestState;

export type FileFillRequest = {
	request_id: string;
	project_id: string;
	project_name: string;
	name: string;
	/** The flag the agent asked for. A secret request cannot be filled as plain. */
	secret: boolean;
	state: FileFillState;
	expires_at: string;
	/** The agent's note. Plain text only: render it as text, never as HTML or a link. */
	reason: string;
};

export const FILE_FILL_TEXT = {
	...FILL_TEXT,
	noContent: 'Paste the file content, or choose a file.',
	notText: 'This file is too large or not text',
	safety: 'The file goes straight to Deploybot. A secret file is stored encrypted. The agent never sees the content.'
} as const;

/** The id rule is every request type's (43 characters of base64url). */
export const isFileRequestId = isRequestId;

const text = boundedText;

/** `GET /v1/file-requests/{id}/fill`. Anything unexpected is null (fail closed). */
export function parseFileFillRequest(payload: unknown, id: string): FileFillRequest | null {
	if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
	const record = payload as Record<string, unknown>;
	const project_id = text(record, 'project_id', 36);
	const project_name = text(record, 'project_name', 200);
	const name = text(record, 'name', 64);
	const state = text(record, 'state', 16);
	const expires_at = text(record, 'expires_at', 64);
	const reason = text(record, 'reason', 1000);
	if (record.request_id !== id || !project_id || !UUID.test(project_id) || project_name == null) return null;
	if (!name || !FILE_NAME.test(name) || typeof record.secret !== 'boolean' || !expires_at || reason == null) return null;
	if (!isRequestState(state)) return null;
	return { request_id: id, project_id, project_name, name, secret: record.secret, state, expires_at, reason };
}

/** UTF-8 byte length. */
export function byteLength(value: string): number {
	return new TextEncoder().encode(value).byteLength;
}

/**
 * The fill body from the posted form. `secret` is the form's flag (`1` or `0`); an empty one means the
 * request's own flag. The API checks the rules again and never echoes the content.
 */
export function fileFillBody(content: string, secret: string): { ok: true; body: { content: string; secret?: boolean } } | { ok: false; message: string } {
	if (content === '') return { ok: false, message: FILE_FILL_TEXT.noContent };
	if (content.includes('\0') || byteLength(content) > FILE_MAX_BYTES) return { ok: false, message: FILE_FILL_TEXT.notText };
	if (secret === '1') return { ok: true, body: { content, secret: true } };
	if (secret === '0') return { ok: true, body: { content, secret: false } };
	return { ok: true, body: { content } };
}

/**
 * A local file read in the browser: its bytes as UTF-8 text, or null when it is larger than 64 KiB,
 * holds a NUL, or is not valid UTF-8. Nothing is read in on null.
 */
export function decodeLocalText(bytes: Uint8Array): string | null {
	if (bytes.byteLength > FILE_MAX_BYTES || bytes.includes(0)) return null;
	try {
		// ignoreBOM keeps a byte-order mark as content, so the file is stored as it is on disk.
		return new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
	} catch {
		return null;
	}
}

/** The longest base64 text of FILE_MAX_BYTES bytes. */
export const FILE_MAX_BASE64 = Math.ceil(FILE_MAX_BYTES / 3) * 4;

const BASE64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

/**
 * A picked file travels as base64 of its UTF-8 bytes (`content_b64`), never as a textarea value: a
 * browser sends textarea line breaks as CRLF, and a picked file must be stored byte-exact.
 */
export function encodeBase64Utf8(text: string): string {
	const bytes = new TextEncoder().encode(text);
	let binary = '';
	for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
	return btoa(binary);
}

/** The server side of `content_b64`: the exact text, or null when it is malformed, too large, or not text. */
export function decodeBase64Utf8(value: string): string | null {
	if (value.length > FILE_MAX_BASE64 || !BASE64.test(value)) return null;
	let binary: string;
	try {
		binary = atob(value);
	} catch {
		return null;
	}
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
	return decodeLocalText(bytes);
}

/** Typed or pasted text: the browser sent its line breaks as CRLF; the file gets LF. */
export function pastedText(value: string): string {
	return value.replace(/\r\n/g, '\n');
}

/**
 * The content posted by the form: the picked file's exact text when `content_b64` is present (null when
 * that field is malformed), else the textarea with CRLF made LF.
 */
export function postedContent(contentB64: string, typed: string): string | null {
	if (contentB64 !== '') return decodeBase64Utf8(contentB64);
	return pastedText(typed);
}

/** Read a picked file. The size is checked before any byte is read. */
export async function readLocalTextFile(file: Blob): Promise<string | null> {
	if (file.size > FILE_MAX_BYTES) return null;
	try {
		return decodeLocalText(new Uint8Array(await file.arrayBuffer()));
	} catch {
		return null;
	}
}

/** The file-exists conflict: the request stays pending, so the page keeps Decline. */
export function isFileExistsMessage(message: string): boolean {
	return FILE_EXISTS.test(message);
}

/** A picked file (exact text) and the picker's error. Null `picked` is paste mode. */
export type PickState = { picked: { name: string; text: string } | null; error: string | null };

/** Paste mode, no error: the start, and after Clear file. */
export const NO_PICK: PickState = { picked: null, error: null };

/**
 * The state after a pick read `text` (null when refused). A refused pick also drops an earlier picked
 * file, so the page is back in paste mode with the error shown.
 */
export function pickOutcome(name: string, text: string | null): PickState {
	if (text == null) return { picked: null, error: FILE_FILL_TEXT.notText };
	return { picked: { name, text }, error: null };
}

/** The secret flag posted: a secret request stays secret; a plain one is secret only when ticked. */
export function secretFlag(requestSecret: boolean, ticked: boolean): boolean {
	return requestSecret || ticked;
}

/** Whether the person may change the Secret checkbox: only on a plain request (never secret to plain). */
export function secretEditable(requestSecret: boolean): boolean {
	return !requestSecret;
}

/**
 * A use:enhance handler that posts once: while a post is pending a second submit is cancelled, so a
 * double click cannot post twice. `setPending` drives the disabled state of Save and Decline.
 */
export function submitOnce(isPending: () => boolean, setPending: (pending: boolean) => void): SubmitFunction {
	return ({ cancel }) => {
		if (isPending()) {
			cancel();
			return;
		}
		setPending(true);
		return async ({ update }) => {
			try {
				await update();
			} finally {
				setPending(false);
			}
		};
	};
}
