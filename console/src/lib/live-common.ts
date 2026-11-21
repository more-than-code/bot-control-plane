/**
 * Small pieces the problem alerts share (spec-console-live-updates §0): the storage, toast and timer shapes, the
 * key cap, a short hash and a tab id that works on insecure origins too.
 */

/** A string store: the tab's `visitStorage`, or the per-browser store of the desktop switch. */
export type KeyStore = Pick<Storage, 'getItem' | 'setItem'>;

/** What the alerts use from svelte-sonner's `toast` (tests pass a stand-in). */
export type ToastApi = {
	error: (message: string, options: Record<string, unknown>) => string | number;
	dismiss: (id?: string | number) => unknown;
};

/** Calls fn every ms until the returned stop runs. */
export type Every = (fn: () => void, ms: number) => () => void;

export const browserEvery: Every = (fn, ms) => {
	const id = setInterval(fn, ms);
	return () => clearInterval(id);
};

/** Alert keys longer than this are not kept, passed on or read back. */
export const MAX_KEY = 200;

export function isKey(value: unknown): value is string {
	return typeof value === 'string' && value !== '' && value.length <= MAX_KEY;
}

/**
 * A random id for this tab. `crypto.randomUUID` exists only on secure origins (https, localhost); elsewhere it falls
 * back to `crypto.getRandomValues`, then to Math.random (an id for coordinating tabs, never a secret).
 */
export function tabId(source: Partial<Pick<Crypto, 'randomUUID' | 'getRandomValues'>> | undefined = globalThis.crypto): string {
	try {
		if (typeof source?.randomUUID === 'function') return source.randomUUID();
		if (typeof source?.getRandomValues === 'function') {
			return Array.from(source.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join('');
		}
	} catch {
		// Fall through to the plain id.
	}
	return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

/** A short stable digest (FNV-1a, 32 bit, hex), so a summary key of many problem keys stays under `MAX_KEY`. */
export function shortHash(text: string): string {
	let hash = 0x811c9dc5;
	for (let index = 0; index < text.length; index += 1) {
		hash ^= text.charCodeAt(index);
		hash = Math.imul(hash, 0x01000193) >>> 0;
	}
	return hash.toString(16).padStart(8, '0');
}
