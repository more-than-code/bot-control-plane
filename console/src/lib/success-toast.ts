import { toast } from 'svelte-sonner';
import { SAVED_HEALTH_CHECKS } from './health-checks';

// Success confirmations show as a toast at the bottom of the window (the Toaster in
// the root layout). Errors stay inline. Each runs once, after its dialog has finished
// closing, so the toast is not announced from inside the dialog.

/** "Saved {entry}." after an image save. `imageSaveView` has already trimmed the name and dropped a blank one. */
export function toastImageSaved(name: string): void {
	toast.success(`Saved ${name}.`);
}

/** "Saved health checks." after a health-check save. */
export function toastHealthSaved(): void {
	toast.success(SAVED_HEALTH_CHECKS);
}

/** "Added {email}." after the Add member dialog succeeds. */
export function toastMemberAdded(email: string): void {
	toast.success(`Added ${email}.`);
}

/** "Saved public addresses." after an entry's routes are confirmed (api-v1 v1.35). */
export function toastRoutesSaved(): void {
	toast.success('Saved public addresses.');
}

/** "Renamed {old} to {new}." after a host rename (api-v1 v1.38). */
export function toastHostRenamed(from: string, to: string): void {
	toast.success(`Renamed ${from} to ${to}.`);
}

/** "Removed {host}." after a revoked host is removed from the lists (api-v1 v1.40). */
export function toastHostRemoved(name: string): void {
	toast.success(`Removed ${name}.`);
}

/** "Deleted version sha256:… (n tags)." after an image version delete (api-v1 v1.45). */
export function toastVersionDeleted(shortDigest: string, tags: number): void {
	toast.success(`Deleted version ${shortDigest} (${tags} ${tags === 1 ? 'tag' : 'tags'}).`);
}

/** "Saved alerts." after a host's Alerts card is saved (api-v1 v1.49). */
export function toastAlertsSaved(): void {
	toast.success('Saved alerts.');
}
