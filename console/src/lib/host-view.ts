import type { Host, HostAccess, HostEdge, OrgRole, Project, ProcessItem, ProcessSample } from './api-types';
import { isCatalogHost } from './catalog';
import { parseRange, type RangeKey } from './ranges';
import type { Column } from './table';

export const METRIC_REFRESH_MS = 30_000;

export const HOST_TABS = ['overview', 'processes'] as const;
export type HostTab = (typeof HOST_TABS)[number];

export type ProcessSort = 'cpu' | 'memory';

export type MetricFormat = 'percent' | 'rate' | 'load' | 'temp';

export type MetricName = 'cpu' | 'mem' | 'disk' | 'net' | 'diskio' | 'system' | 'temp';

export type MetricSpec = {
	m: MetricName;
	title: string;
	yMin: number | null;
	yMax: number | null;
	format: MetricFormat;
	series: ReadonlyArray<{ name: string; label: string; color: string }>;
};

/** Series names are the api-v1 set. Colors are the existing chart tokens. */
export const METRIC_SPECS: readonly MetricSpec[] = [
	{
		m: 'cpu',
		title: 'CPU',
		yMin: 0,
		yMax: 100,
		format: 'percent',
		series: [{ name: 'usage_percent', label: 'CPU', color: 'var(--chart-1)' }]
	},
	{
		m: 'mem',
		title: 'Memory',
		yMin: 0,
		yMax: 100,
		format: 'percent',
		series: [{ name: 'used_percent', label: 'Memory', color: 'var(--chart-2)' }]
	},
	{
		m: 'disk',
		title: 'Disk',
		yMin: 0,
		yMax: 100,
		format: 'percent',
		series: [{ name: 'used_percent', label: 'Used', color: 'var(--chart-3)' }]
	},
	{
		m: 'net',
		title: 'Network',
		yMin: 0,
		yMax: null,
		format: 'rate',
		series: [
			{ name: 'rx_bps', label: 'Receive', color: 'var(--chart-1)' },
			{ name: 'tx_bps', label: 'Transmit', color: 'var(--chart-5)' }
		]
	},
	{
		m: 'diskio',
		title: 'Disk IO',
		yMin: 0,
		yMax: null,
		format: 'rate',
		series: [
			{ name: 'read_bps', label: 'Read', color: 'var(--chart-2)' },
			{ name: 'write_bps', label: 'Write', color: 'var(--chart-4)' }
		]
	},
	{
		m: 'system',
		title: 'Load',
		yMin: 0,
		yMax: null,
		format: 'load',
		series: [
			{ name: 'load1', label: 'load1', color: 'var(--chart-5)' },
			{ name: 'load5', label: 'load5', color: 'var(--chart-1)' },
			{ name: 'load15', label: 'load15', color: 'var(--chart-2)' }
		]
	},
	{
		m: 'temp',
		title: 'Temperature',
		yMin: null,
		yMax: null,
		format: 'temp',
		series: [{ name: 'celsius', label: 'Temperature', color: 'var(--chart-4)' }]
	}
];

export type MetricPoint = { t: string; value: number | null };

export type MetricChart = {
	m: MetricName;
	message: string | null;
	step: number | null;
	series: Array<{ name: string; points: MetricPoint[] }>;
};

/** Label and optional project Services link for one process row. */
export type ServiceLink = {
	label: string;
	href: string | null;
};

export type HostPage = {
	host: Host | null;
	tab: HostTab;
	range: RangeKey;
	processSort: ProcessSort;
	message: string | null;
	serviceLinks: Record<string, ServiceLink>;
	metrics: MetricChart[];
	processes: ProcessSample | null;
	processesMessage: string | null;
	/** The host's org, when the caller is in it: the breadcrumb and the access section. */
	org: { id: string; name: string; role: OrgRole } | null;
	/** Org owner or admin: the Access section (api-v1 v1.24 item 2). Hidden for everyone else. */
	canManageAccess: boolean;
	/** Both access reads answered; otherwise the section shows only its message. */
	accessLoaded: boolean;
	access: HostAccess[];
	/** The org's projects the access form may grant (it needs their slugs for the runner.toml note). */
	accessProjects: Project[];
	accessMessage: string | null;
	/** The host's edge (api-v1 v1.35 item 2), read for org owners and admins only. */
	edge?: HostEdge | null;
	/** The edge read failed. */
	edgeMessage?: string | null;
};

export function parseHostTab(value: string | null): HostTab {
	if (value === 'processes') return value;
	return 'overview';
}

export function parseProcessSort(value: string | null): ProcessSort {
	return value === 'memory' ? 'memory' : 'cpu';
}

/**
 * API `sort` for the process sample. `processes.sort=cpu|memory` wins.
 * Any other table column still requests `cpu` (the page sorts those rows).
 * With no table sort, the legacy `sort` param is used and unknown values fall back to cpu.
 */
export function processRequestSort(params: URLSearchParams): ProcessSort {
	const tableSort = params.get('processes.sort');
	if (tableSort === 'cpu' || tableSort === 'memory') return tableSort;
	if (tableSort != null && tableSort !== '') return 'cpu';
	return parseProcessSort(params.get('sort'));
}

export function hostQuery(url: URL): { tab: HostTab; range: RangeKey; processSort: ProcessSort } {
	return {
		tab: parseHostTab(url.searchParams.get('tab')),
		range: parseRange(url.searchParams.get('range')),
		processSort: processRequestSort(url.searchParams)
	};
}

export function serviceDomId(serviceId: string): string {
	return `service-${serviceId}`;
}

/** A sample the processes table can show. Null `sampled_at` and no rows is "no sample yet". */
export function hasProcessSample(sample: ProcessSample | null): sample is ProcessSample & { sampled_at: string } {
	return sample != null && sample.sampled_at != null && (sample.items.length > 0 || sample.skipped > 0);
}

/** Hosts the Catalog & services host filter offers: hosts this project has `deploy` access to. */
export function serviceHostReachable(host: Pick<Host, 'access_level'>): boolean {
	return isCatalogHost(host);
}

/** Label shown in the Service column. Empty when the process has no service. */
export function processServiceText(
	row: { service_id: string | null },
	links: Readonly<Record<string, { label: string }>>
): string {
	if (!row.service_id) return '';
	return links[row.service_id]?.label ?? 'service';
}

export const PROCESS_COLUMNS: Column<ProcessItem>[] = [
	{ key: 'pid', label: 'PID', numeric: true, sortable: true, searchable: true, value: (row) => row.pid },
	{ key: 'process', label: 'Process', sortable: true, searchable: true, value: (row) => row.process_name },
	{
		key: 'service',
		label: 'Service',
		sortable: true,
		searchable: true,
		value: (row) => (row.service_id ? 'service' : '')
	},
	{ key: 'user', label: 'User', sortable: true, searchable: true, value: (row) => row.user },
	{ key: 'command', label: 'Command', sortable: true, searchable: true, value: (row) => row.cmdline ?? '' },
	{ key: 'cpu', label: 'CPU %', numeric: true, sortable: true, firstDir: 'desc', value: (row) => row.cpu_usage },
	{ key: 'rss', label: 'RSS', numeric: true, sortable: true, firstDir: 'desc', value: (row) => row.memory_rss },
	{ key: 'memory', label: 'Mem %', numeric: true, sortable: true, firstDir: 'desc', value: (row) => row.memory_usage },
	{ key: 'threads', label: 'Threads', numeric: true, sortable: true, firstDir: 'desc', value: (row) => row.num_threads },
	{ key: 'started', label: 'Started', sortable: true, value: (row) => row.started_at }
];

/** Process columns with the Service search and sort value taken from the label map. */
export function processColumns(links: Readonly<Record<string, { label: string }>>): Column<ProcessItem>[] {
	return PROCESS_COLUMNS.map((column) =>
		column.key === 'service' ? { ...column, value: (row) => processServiceText(row, links) } : column
	);
}

/** One time axis. A series missing a timestamp, or a null value, is a gap. */
export function alignSeries(series: ReadonlyArray<{ points: readonly MetricPoint[] }>): {
	times: string[];
	columns: Array<Array<number | null>>;
} {
	const times: string[] = [];
	const seen = new Set<string>();
	for (const item of series) {
		for (const point of item.points) {
			if (seen.has(point.t)) continue;
			seen.add(point.t);
			times.push(point.t);
		}
	}
	times.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
	const columns = series.map((item) => {
		const byTime = new Map(item.points.map((point) => [point.t, point.value]));
		return times.map((t) => (byTime.has(t) ? (byTime.get(t) ?? null) : null));
	});
	return { times, columns };
}

/**
 * The host page "Host policy" line (spec-projects-gate-programs §3.4): `Programs on, Images off, Projects: a, b`.
 * The project list is present only for org owners and admins (api-v1 v1.24 item 1, amended); the line shows when
 * it is, or when the API sent the caller `access` (an org admin whose agent did not report a list: "not reported").
 * Null for any other caller.
 */
export function hostPolicyLine(host: Pick<Host, 'access' | 'programs'>): string | null {
	if (host.programs?.projects == null && host.access == null) return null;
	const programs = host.programs;
	const projects = programs?.projects == null ? 'not reported' : programs.projects.length === 0 ? 'none' : programs.projects.join(', ');
	// No policy from the agent at all: nothing is known about programs or images either.
	if (programs == null) return `Programs not reported, Images not reported, Projects: ${projects}`;
	const onOff = (value: boolean) => (value ? 'on' : 'off');
	return `Programs ${onOff(programs.enabled)}, Images ${onOff(programs.images)}, Projects: ${projects}`;
}

/** The host page "Shared networks" item: one line per network, or a status when there are none. */
export type HostSharedNetworks = { networks: string[] } | { status: 'none' | 'not reported' };

/**
 * The host page "Shared networks" item (api-v1 v1.25): one line per network, `dd (10.200.10.0/24): daikou,
 * defect-drainer`, with members only when the API sent them (org owners and admins; "no projects" when empty).
 * With no networks to list, "none" or "not reported" shows only where the Host policy line shows (see
 * hostPolicyLine), because only then may the caller see the whole policy; otherwise null.
 */
export function hostSharedNetworks(host: Pick<Host, 'access' | 'programs'>): HostSharedNetworks | null {
	const shared = host.programs?.shared_networks ?? null;
	if (shared != null && shared.length > 0) {
		return {
			networks: shared.map((network) => {
				const head = `${network.name} (${network.subnet})`;
				if (network.projects == null) return head;
				return `${head}: ${network.projects.length === 0 ? 'no projects' : network.projects.join(', ')}`;
			})
		};
	}
	const policyShown = hostPolicyLine(host) != null;
	if (!policyShown) return null;
	return { status: shared == null ? 'not reported' : 'none' };
}

/** The "Update available" badge's tooltip (api-v1 v1.34): names the newest bot-agent release. */
export function agentUpdateText(latest: string): string {
	return `bot-agent ${latest} is available. Upgrading a host is an operator step.`;
}
