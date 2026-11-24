import type { HostDisk } from './api-types';

/**
 * What fills a host's disk (api-v1 v1.59, spec-disk-usage-breakdown §10): the mounts table order and the Docker
 * usage answer of `POST /v1/hosts/{id}/docker-usage`, read field by field.
 */

export type DockerUsageTotal = { count: number; active: number; size: number; reclaimable: number };

export type DockerUsageImage = { ref: string; id: string; size: number | null; containers: number };
export type DockerUsageVolume = { name: string; size: number | null; in_use: boolean };
export type DockerUsageContainer = { name: string; state: string; size: number | null };

export type DockerUsage = {
	totals: { images: DockerUsageTotal; containers: DockerUsageTotal; volumes: DockerUsageTotal; build_cache: DockerUsageTotal };
	images: DockerUsageImage[];
	volumes: DockerUsageVolume[];
	containers: DockerUsageContainer[];
	/** When the API received the answer (RFC 3339). */
	at: string;
};

/** The console waits this long for the action: the API's 50 s wait plus a margin. */
export const DOCKER_USAGE_WAIT_MS = 55_000;

const LIST_CAP = 10;
const STRING_CAP = 200;
const CONTROL = /[\u0000-\u001F\u007F]/g;

const TOTAL_KEYS = ['images', 'containers', 'volumes', 'build_cache'] as const;
const TOTAL_LABELS: Record<(typeof TOTAL_KEYS)[number], string> = {
	images: 'Images',
	containers: 'Containers',
	volumes: 'Volumes',
	build_cache: 'Build cache'
};

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function count(value: unknown): number | null {
	return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

/** A size: a whole number of bytes, or null when unknown. Undefined when the value is not usable at all. */
function size(value: unknown): number | null | undefined {
	if (value === null) return null;
	return count(value) ?? undefined;
}

function text(value: unknown): string | null {
	if (typeof value !== 'string') return null;
	return value.replace(CONTROL, '�').slice(0, STRING_CAP);
}

function total(value: unknown): DockerUsageTotal | null {
	if (!isRecord(value)) return null;
	const out = { count: count(value.count), active: count(value.active), size: count(value.size), reclaimable: count(value.reclaimable) };
	if (out.count == null || out.active == null || out.size == null || out.reclaimable == null) return null;
	return out as DockerUsageTotal;
}

function list<T>(value: unknown, read: (item: Record<string, unknown>) => T | null): T[] | null {
	if (!Array.isArray(value)) return null;
	const out: T[] = [];
	for (const item of value) {
		if (out.length === LIST_CAP) break;
		const row = isRecord(item) ? read(item) : null;
		if (row) out.push(row);
	}
	return out;
}

/** The docker-usage payload, or null when its totals or lists are not usable. Unknown fields are dropped. */
export function parseDockerUsage(payload: unknown): DockerUsage | null {
	if (!isRecord(payload) || !isRecord(payload.totals) || typeof payload.at !== 'string' || Number.isNaN(Date.parse(payload.at))) return null;
	const totals: Partial<DockerUsage['totals']> = {};
	for (const key of TOTAL_KEYS) {
		const t = total(payload.totals[key]);
		if (!t) return null;
		totals[key] = t;
	}
	const images = list(payload.images, (item) => {
		const ref = text(item.ref);
		const id = text(item.id);
		const bytes = size(item.size);
		const containers = count(item.containers);
		return ref == null || id == null || bytes === undefined || containers == null ? null : { ref, id, size: bytes, containers };
	});
	const volumes = list(payload.volumes, (item) => {
		const name = text(item.name);
		const bytes = size(item.size);
		return name == null || bytes === undefined || typeof item.in_use !== 'boolean' ? null : { name, size: bytes, in_use: item.in_use };
	});
	const containers = list(payload.containers, (item) => {
		const name = text(item.name);
		const state = text(item.state);
		const bytes = size(item.size);
		return name == null || state == null || bytes === undefined ? null : { name, state, size: bytes };
	});
	if (!images || !volumes || !containers) return null;
	return { totals: totals as DockerUsage['totals'], images, volumes, containers, at: payload.at };
}

/** The four total rows in the card's order. */
export function dockerTotalRows(usage: DockerUsage): Array<{ key: string; label: string } & DockerUsageTotal> {
	return TOTAL_KEYS.map((key) => ({ key, label: TOTAL_LABELS[key], ...usage.totals[key] }));
}

/** Docker's whole size and what could be reclaimed, over the four totals. */
export function dockerSums(usage: DockerUsage): { size: number; reclaimable: number } {
	return TOTAL_KEYS.reduce(
		(sum, key) => ({ size: sum.size + usage.totals[key].size, reclaimable: sum.reclaimable + usage.totals[key].reclaimable }),
		{ size: 0, reclaimable: 0 }
	);
}

/** Mounts for the Disk usage table: highest used percent first, unknown last, then by path. */
export function mountsByUse(disks: readonly HostDisk[]): HostDisk[] {
	const pct = (d: HostDisk) => (typeof d.used_percent === 'number' && Number.isFinite(d.used_percent) ? d.used_percent : -1);
	return [...disks].sort((a, b) => pct(b) - pct(a) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
}
