/**
 * Generated env values and the show-once reveal (api-v1 v1.27 items 1–3, spec-env-generate §3.2, §3.6).
 * The API makes the value; the console sees it only in the one reveal answer, holds it in dialog state,
 * and drops it on close.
 */

export const GENERATE_KINDS = ['password', 'hex', 'base64url'] as const;
export type GenerateKind = (typeof GENERATE_KINDS)[number];

/** Length of a generated password (api-v1 v1.27 item 1: 16–64, the console always sends this). */
export const PASSWORD_LENGTH = 32;

export const GENERATE_LABELS: Record<GenerateKind, string> = {
	password: `Password, ${PASSWORD_LENGTH} characters`,
	hex: 'Hex, 64 characters',
	base64url: 'Base64url, 43 characters'
};

export function isGenerateKind(value: string): value is GenerateKind {
	return (GENERATE_KINDS as readonly string[]).includes(value);
}

/** `PUT …/keys/{key}` body for a generated value. A generated key is always secret. */
export function generateRequestBody(kind: GenerateKind): { generate: { kind: GenerateKind; length?: number }; is_secret: true } {
	return { generate: kind === 'password' ? { kind, length: PASSWORD_LENGTH } : { kind }, is_secret: true };
}

export const REVEAL_ONCE_NOTE = "You won't see this value again.";

/** A reveal that failed without an API answer may still have used up the one-time view. */
export function revealFailedText(key: string): string {
	return `The value could not be shown. If Reveal once is gone, edit ${key} to set a new value.`;
}

/**
 * The reveal dialog's state. Only `shown` holds a value. `token` ties an answer to the request that
 * asked for it, so an older answer never replaces a newer one.
 */
export type RevealState =
	| { status: 'closed' }
	| { status: 'loading'; key: string; token: number }
	| { status: 'shown'; key: string; token: number; value: string }
	| { status: 'failed'; key: string; token: number; message: string };

export const REVEAL_CLOSED: RevealState = { status: 'closed' };

export function revealOpened(key: string, token: number): RevealState {
	return { status: 'loading', key, token };
}

/** While the request is out the dialog cannot be dismissed: the answer is the only copy of the value. */
export function revealCanClose(state: RevealState): boolean {
	return state.status !== 'loading';
}

/** Close, Escape or the X. Refused while loading; otherwise the value is dropped. */
export function revealClosed(state: RevealState): RevealState {
	return revealCanClose(state) ? REVEAL_CLOSED : state;
}

/**
 * Apply the reveal answer. An answer for another key or an older request, or one that arrives after the
 * dialog closed, is dropped, so a value never lands in a dialog that no longer asked for it.
 */
export function revealSettled(state: RevealState, key: string, token: number, answer: RevealAnswer): RevealState {
	if (state.status !== 'loading' || state.key !== key || state.token !== token) return state;
	if (answer.ok) return { status: 'shown', key, token, value: answer.value };
	return { status: 'failed', key, token, message: answer.message };
}

export type RevealAnswer = { ok: true; value: string } | { ok: false; message: string };

/** Read the deserialized `revealEnvKey` action result. Only a success for `key` carries a value. */
export function revealAnswer(result: { type: string; data?: unknown }, key: string): RevealAnswer {
	const data = result.data != null && typeof result.data === 'object' ? (result.data as Record<string, unknown>) : null;
	if (result.type === 'success') {
		const revealed = data?.revealed;
		if (revealed != null && typeof revealed === 'object') {
			const item = revealed as Record<string, unknown>;
			if (item.key === key && typeof item.value === 'string') return { ok: true, value: item.value };
		}
		return { ok: false, message: revealFailedText(key) };
	}
	if (result.type === 'failure' && typeof data?.revealError === 'string' && data.revealError !== '') return { ok: false, message: data.revealError };
	return { ok: false, message: revealFailedText(key) };
}

/** Reveal once is offered on a secret key only while the API says its generated value is pending. */
export function envRowActions(item: { is_secret: boolean; reveal_pending: boolean }): { revealOnce: boolean } {
	return { revealOnce: item.is_secret && item.reveal_pending };
}
