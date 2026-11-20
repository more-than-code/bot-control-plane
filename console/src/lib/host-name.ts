import type { Host } from './api-types';

// Host names (api-v1 v1.38): the enrolment rule, which the rename dialog checks before it posts.

const HOST_NAME_RE = /^[a-z0-9-]{1,63}$/;

/** The rule, as the dialog's hint. */
export const HOST_NAME_HINT = '1–63 characters: lowercase letters, digits and hyphens.';

/** The dialog's and the action's message for a name that breaks the rule: the API's wording, capitalised, with a full stop. */
export const HOST_NAME_MESSAGE = 'Name must be 1–63 characters: lowercase letters, digits and -.';

export function validHostName(name: string): boolean {
	return HOST_NAME_RE.test(name);
}

/**
 * Revoked hosts after live ones, each group in the order it came (the API's name order). A revoked host keeps
 * its name, so a live and a revoked host can share one; this keeps the pair readable.
 */
export function revokedLast<T extends Pick<Host, 'status'>>(hosts: readonly T[]): T[] {
	return [...hosts.filter((host) => host.status !== 'revoked'), ...hosts.filter((host) => host.status === 'revoked')];
}

/** Rename is offered on live hosts only: the API refuses a revoked one. */
export function canRenameHost(host: Pick<Host, 'status'>): boolean {
	return host.status !== 'revoked';
}

/**
 * The host's name for labels, with " (revoked)" on a revoked host: a live and a revoked host can share a name, and
 * their row actions, dialog titles and links must still tell them apart.
 */
export function hostLabel(host: Pick<Host, 'name' | 'status'>): string {
	return host.status === 'revoked' ? `${host.name} (revoked)` : host.name;
}

export type HostRename = { hostId: string; from: string; to: string };

/**
 * The "Renamed <old> to <new>." toast, held by the Hosts section rather than the dialog, because a rename can
 * unmount the row (a search that no longer matches, a sort that moves it to another page).
 * - `succeeded`: while the dialog is open the toast waits for it to close; if it was closed already (Escape while
 *   the request ran) it fires now, so it never fires later at an unrelated close.
 * - `closed`: that host's dialog finished closing or its row unmounted; its waiting toast fires. Another row's
 *   close (or unmount during the same reload) leaves it waiting.
 */
export function nextRenameToast(
	pending: HostRename | null,
	event: { kind: 'succeeded'; rename: HostRename; dialogOpen: boolean } | { kind: 'closed'; hostId: string }
): { pending: HostRename | null; fire: HostRename | null } {
	if (event.kind === 'closed') return pending?.hostId === event.hostId ? { pending: null, fire: pending } : { pending, fire: null };
	if (event.rename.from === event.rename.to) return { pending, fire: null };
	return event.dialogOpen ? { pending: event.rename, fire: null } : { pending: null, fire: event.rename };
}
