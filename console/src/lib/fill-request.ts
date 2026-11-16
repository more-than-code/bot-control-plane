/**
 * The value-request fill page (api-v1 v1.28, spec-env-request-value §3.3). An agent asked for an env
 * value; the person who asked fills it here. The value goes from this form to the API and nowhere
 * else: it is never in page data, `form`, a URL, or a log.
 */
import { GENERATE_KINDS, isGenerateKind, PASSWORD_LENGTH, type GenerateKind } from '$lib/env-generate';
import { boundedText, isRequestId, isRequestState, requestView, UUID, type RequestState, type RequestView } from '$lib/request-shape';

const SET_NAME = /^[a-z0-9][a-z0-9-]{0,47}$/;
const ENV_KEY = /^[A-Z_][A-Z0-9_]{0,63}$/;
export type FillState = RequestState;

export type FillRequest = {
	request_id: string;
	project_id: string;
	project_name: string;
	set: string;
	key: string;
	state: FillState;
	expires_at: string;
	/** The agent's note. Plain text only: render it as text, never as HTML or a link. */
	reason: string;
};

/** What the page shows: the form, or one problem with no form. `error` is a load failure: its message only. */
export type FillView = RequestView;

export const FILL_TEXT = {
	used: 'This request was already used. You can close this tab.',
	expired: 'This request has expired. Ask the agent for a new one.',
	missing: 'This request was not found. It belongs to the account that asked for it, so sign in as that person. You must also still be an owner or admin of the project.',
	invalid: 'This link is not valid. Check that you copied all of it.',
	saved: 'Saved. You can close this tab.',
	declined: 'Declined. The agent will see that you declined. You can close this tab.',
	unexpected: 'The API returned an unexpected response.',
	noValue: 'Enter a value, or choose Generate.',
	safety: "The value goes straight to Deploybot and is stored encrypted. The agent never sees it."
} as const;

export const isValueRequestId = isRequestId;

const text = boundedText;

/** `GET /v1/value-requests/{id}/fill`. Anything unexpected is null (fail closed). */
export function parseFillRequest(payload: unknown, id: string): FillRequest | null {
	if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
	const record = payload as Record<string, unknown>;
	const project_id = text(record, 'project_id', 36);
	const project_name = text(record, 'project_name', 200);
	const set = text(record, 'set', 48);
	const key = text(record, 'key', 64);
	const state = text(record, 'state', 16);
	const expires_at = text(record, 'expires_at', 64);
	const reason = text(record, 'reason', 1000);
	if (record.request_id !== id || !project_id || !UUID.test(project_id) || project_name == null) return null;
	if (!set || !SET_NAME.test(set) || !key || !ENV_KEY.test(key) || !expires_at || reason == null) return null;
	if (!isRequestState(state)) return null;
	return { request_id: id, project_id, project_name, set, key, state, expires_at, reason };
}

/** The view for a loaded request. */
export const fillView = requestView;

export type FillSource = 'value' | 'generate';

export type FillBody = { value: string } | { generate: { kind: GenerateKind; length?: number } };

/**
 * The fill body from the posted form: `source` is `value` (with `value`) or `generate` (with `kind`).
 * A typed value is sent as typed; the API checks its rules and never echoes it.
 */
export function fillBody(source: string, value: string, kind: string): { ok: true; body: FillBody } | { ok: false; message: string } {
	if (source === 'generate') {
		if (!isGenerateKind(kind)) return { ok: false, message: `Choose ${GENERATE_KINDS.join(', ')}.` };
		return { ok: true, body: { generate: kind === 'password' ? { kind, length: PASSWORD_LENGTH } : { kind } } };
	}
	if (value === '') return { ok: false, message: FILL_TEXT.noValue };
	return { ok: true, body: { value } };
}

/** Replace a typed value in a message, should one ever come back. */
export function withoutValue(message: string, value: string): string {
	return value === '' ? message : message.split(value).join('[redacted]');
}
