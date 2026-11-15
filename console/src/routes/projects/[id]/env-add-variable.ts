import { ENV_KEY } from '$lib/catalog';
import type { GenerateKind } from '$lib/env-generate';
import { looksSecret, secretLookingKeys, suggestedSecret } from '$lib/env-secret-name';
import { RETYPE_VALUE } from './env-set-panel';

/** Draft of the One variable tab. Secret is off by default (Joe 2026-10-25). */
export type AddDraft = {
	key: string;
	value: string;
	secret: boolean;
	error: string;
	/** `key`: the Key field is wrong. `form`: anything else (API messages). */
	errorField: 'key' | 'form' | null;
	/** A secret value was cleared after a failure and must be pasted again. */
	retype: boolean;
	/** `generate`: the API makes the value (api-v1 v1.27 item 1); the key is secret and no value is sent. */
	source: 'value' | 'generate';
	kind: GenerateKind;
	/** The user set Secret themself, so the name suggestion no longer changes it (spec rev 3 §3.4). */
	secretTouched: boolean;
};

export function initialAddDraft(): AddDraft {
	return { key: '', value: '', secret: false, error: '', errorField: null, retype: false, source: 'value', kind: 'password', secretTouched: false };
}

/** Switch between typing a value and generating one. Generate drops a typed value so it is never sent. */
export function setAddSource(draft: AddDraft, source: 'value' | 'generate'): AddDraft {
	if (source === draft.source) return draft;
	return source === 'generate' ? { ...draft, source, value: '', retype: false } : { ...draft, source };
}

/** Secret as shown and sent: always on for a generated value. */
export function addIsSecret(draft: AddDraft): boolean {
	return draft.source === 'generate' || draft.secret;
}

/** Flip Secret. The typed value stays; the paste-again hint no longer applies. */
export function setAddSecret(draft: AddDraft, secret: boolean): AddDraft {
	return { ...draft, secret, retype: false, secretTouched: true };
}

/** Typing a key pre-ticks Secret when the name looks secret, until the user sets Secret themself. */
export function addKeyTyped(draft: AddDraft): AddDraft {
	const secret = suggestedSecret(draft.secret, draft.secretTouched, looksSecret(draft.key));
	return secret === draft.secret ? draft : { ...draft, secret };
}

/** Typing a value ends the paste-again hint. */
export function addValueTyped(draft: AddDraft): AddDraft {
	return draft.retype ? { ...draft, retype: false } : draft;
}

export const ADD_KEY_MESSAGE = 'Keys use A-Z, digits, and underscore, and start with a letter or underscore.';

export function addKeyExistsMessage(key: string): string {
	return `${key} is already in this set. Use Edit on its row to change the value.`;
}

/** Checked before sending. The API stays authoritative (it would overwrite an existing key). */
export function addKeyError(key: string, existing: readonly string[] = []): string | null {
	if (!ENV_KEY.test(key)) return ADD_KEY_MESSAGE;
	return existing.includes(key) ? addKeyExistsMessage(key) : null;
}

/** A Key refused before sending: nothing was sent, so the typed value stays. */
export function addKeyRefused(draft: AddDraft, message: string): AddDraft {
	return { ...draft, error: message, errorField: 'key', retype: false };
}

/**
 * A failed add keeps the key and the message. A secret value is cleared and asked for again. `secret` is the
 * effective flag as shown (it can be on for a secret placeholder while the draft's own box is off).
 */
export function addFailed(draft: AddDraft, message: string, errorField: 'key' | 'form' = 'form', secret = addIsSecret(draft)): AddDraft {
	// A generated value or a plain one has nothing to retype.
	if (draft.source === 'generate' || !secret) return { ...draft, error: message, errorField, retype: false };
	return { ...draft, value: '', error: message, errorField, retype: draft.value !== '' };
}

export function addErrorText(draft: AddDraft): string {
	if (draft.error === '') return '';
	return draft.retype ? `${draft.error} ${RETYPE_VALUE}` : draft.error;
}

export type AddSettleEffects = {
	/** Set the panel status line ("Added {KEY}."). */
	added: () => void;
	/** Close the dialog (a no-op when it is already closed). */
	close: () => void;
	failed: () => void | Promise<void>;
	update: () => Promise<void>;
	tick: () => Promise<void>;
	focusStatus: () => void;
};

/**
 * The Add variable form's `use:enhance` result, in order. A success closes the dialog, then waits for
 * the reload (SvelteKit's navigation resets focus), then focuses the status line, but only when the
 * dialog was still open when the result arrived. A failure that arrives after a dismiss is ignored.
 */
export async function settleAdd(open: boolean, resultType: string, fx: AddSettleEffects): Promise<'added' | 'failed' | 'ignored' | 'other'> {
	if (resultType === 'failure') {
		if (!open) return 'ignored';
		await fx.failed();
		return 'failed';
	}
	if (resultType === 'redirect') {
		fx.added();
		fx.close();
		await fx.update();
		// Dismissed before the result arrived: focus is where the user left it.
		if (!open) return 'added';
		await fx.tick();
		fx.focusStatus();
		return 'added';
	}
	await fx.update();
	return 'other';
}

/** The Add variables dialog's two tabs: one key and value, or a pasted block (api-v1 v1.19 merge). */
export type AddTab = 'one' | 'paste';

/** Everything the Add variables dialog holds besides `open`. The paste may hold secrets. */
export type AddVariablesView = {
	tab: AddTab;
	draft: AddDraft;
	paste: string;
	pasteSecret: boolean;
	/** The user set New keys are secret themself (see AddDraft.secretTouched). */
	pasteSecretTouched: boolean;
	pasteError: string;
};

/** Every open starts here: the One variable tab, both inputs empty, both Secret boxes unticked. */
export function initialAddVariables(): AddVariablesView {
	return { tab: 'one', draft: initialAddDraft(), paste: '', pasteSecret: false, pasteSecretTouched: false, pasteError: '' };
}

/** Editing the paste pre-ticks New keys are secret when a new key looks secret, until the user sets the box. */
export function pasteTyped(view: AddVariablesView, existing: readonly string[]): AddVariablesView {
	const pasteSecret = suggestedSecret(view.pasteSecret, view.pasteSecretTouched, secretLookingKeys(view.paste, existing).length > 0);
	return pasteSecret === view.pasteSecret ? view : { ...view, pasteSecret };
}

/** Closing (Cancel, Escape, the X, or after a save) drops the key, the value and the paste, and goes back to One variable. */
export function closeAddVariables(_view: AddVariablesView): AddVariablesView {
	return initialAddVariables();
}

function withoutAddError(draft: AddDraft): AddDraft {
	return { ...draft, error: '', errorField: null, retype: false };
}

/** Switching tabs drops both errors, so neither tab shows a stale one. Typed input stays. */
export function switchAddTab(view: AddVariablesView, tab: AddTab): AddVariablesView {
	return { ...view, tab, draft: withoutAddError(view.draft), pasteError: '' };
}

/** An Add variable failure shows on the One variable tab (a secret value is asked for again). */
export function addTabFailed(view: AddVariablesView, message: string, secret = addIsSecret(view.draft)): AddVariablesView {
	return { ...view, tab: 'one', draft: addFailed(view.draft, message, 'form', secret), pasteError: '' };
}

/**
 * The Secret box and Generate for the typed key (api-v1 v1.27 item 4): a secret placeholder stays secret; a plain
 * placeholder stays plain and cannot be generated (a generated value is secret). `note` explains it.
 */
export function placeholderRule(key: string, secretPlaceholders: readonly string[], plainPlaceholders: readonly string[]): { secret: boolean | null; canGenerate: boolean; note: string } {
	const k = key.trim();
	if (secretPlaceholders.includes(k)) return { secret: true, canGenerate: true, note: 'A secret placeholder stays secret.' };
	if (plainPlaceholders.includes(k)) return { secret: false, canGenerate: false, note: 'This placeholder is plain; make it secret first to generate.' };
	return { secret: null, canGenerate: true, note: '' };
}

/** An Add variables failure shows on the Paste several tab. The paste is not kept. */
export function pasteTabFailed(view: AddVariablesView, message: string): AddVariablesView {
	return { ...view, tab: 'paste', draft: withoutAddError(view.draft), paste: '', pasteError: message };
}

export type MergeSettleEffects = {
	/** Blank the paste. Runs first on every result, so a secret never outlives the request. */
	clearPaste: () => void;
	/** Set the set's status line ("Saved variables in {set}."). */
	merged: () => void;
	/** Close the dialog (a no-op when it is already closed). */
	close: () => void;
	/** Show the message on the Paste several tab. */
	failed: () => void | Promise<void>;
	update: () => Promise<void>;
	tick: () => Promise<void>;
	focusStatus: () => void;
};

/**
 * The Paste several form's `use:enhance` result, in order, like `settleAdd`. The paste is blanked first.
 * A success closes, waits for the reload, then focuses the status line if the dialog was still open.
 * A failure stays on the Paste several tab, unless the dialog was dismissed meanwhile (then it is ignored).
 */
export async function settleMerge(open: boolean, resultType: string, fx: MergeSettleEffects): Promise<'merged' | 'failed' | 'ignored' | 'other'> {
	fx.clearPaste();
	if (resultType === 'redirect') {
		fx.merged();
		fx.close();
		await fx.update();
		if (!open) return 'merged';
		await fx.tick();
		fx.focusStatus();
		return 'merged';
	}
	if (resultType === 'failure') {
		if (!open) return 'ignored';
		await fx.failed();
		return 'failed';
	}
	await fx.update();
	return 'other';
}
