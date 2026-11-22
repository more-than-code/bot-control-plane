/**
 * The project and org Hosts tabs re-read their hosts while open, so status, agent version and "Update available"
 * stay as current as the bell. Only the host list is fetched (`hosts.json` beside the page), never the whole load,
 * so a failed read leaves the page as it was. Kept out of the pages so it is tested with a stub document and timer.
 */
import type { Host } from './api-types';
import { browserEvery, type Every } from './live-common';
import type { VisibilityDoc } from './visibility';

/** How often an open, visible Hosts tab re-reads its hosts. */
export const HOSTS_REFRESH_MS = 30_000;

/**
 * Calls `refresh` every `ms` while the document is not hidden. Hidden stops the timer; visible again refreshes at
 * once and restarts it. One refresh at a time: a tick while one is pending is skipped. A refresh that throws or
 * rejects is ignored. The returned stop ends it for good.
 */
export function startHostsRefresh({
	doc,
	refresh,
	every = browserEvery,
	ms = HOSTS_REFRESH_MS
}: {
	doc: VisibilityDoc;
	refresh: () => unknown;
	every?: Every;
	ms?: number;
}): () => void {
	let stopTimer: (() => void) | undefined;
	let stopped = false;
	let inFlight = false;
	const run = () => {
		if (stopped || inFlight) return;
		inFlight = true;
		let pending: Promise<unknown>;
		try {
			pending = Promise.resolve(refresh());
		} catch {
			// A failed refresh leaves the timer running.
			pending = Promise.resolve();
		}
		pending
			.catch(() => {})
			.finally(() => {
				inFlight = false;
			});
	};
	const pause = () => {
		stopTimer?.();
		stopTimer = undefined;
	};
	const resume = () => {
		pause();
		stopTimer = every(run, ms);
	};
	const onVisibility = () => {
		if (stopped) return;
		if (doc.visibilityState === 'hidden') {
			pause();
			return;
		}
		run();
		resume();
	};
	if (doc.visibilityState !== 'hidden') resume();
	doc.addEventListener('visibilitychange', onVisibility);
	return () => {
		stopped = true;
		pause();
		doc.removeEventListener('visibilitychange', onVisibility);
	};
}

/** Reads `{ hosts }` from a Hosts tab's `hosts.json`; throws on a failed answer or an odd body. */
export async function fetchHosts(fetcher: typeof fetch, url: string): Promise<Host[]> {
	const response = await fetcher(url, { headers: { accept: 'application/json' } });
	if (!response.ok) throw new Error(`hosts ${response.status}`);
	const body = (await response.json()) as { hosts?: unknown };
	if (!Array.isArray(body?.hosts)) throw new Error('hosts body');
	return body.hosts as Host[];
}
