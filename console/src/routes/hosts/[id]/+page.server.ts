import { error, fail, isHttpError, isRedirect, redirect } from '@sveltejs/kit';
import type { Host, HostAccess, HostEdge, ProcessSample, Project } from '$lib/api-types';
import type { HostPage, MetricChart, ProcessSort, ServiceLink } from '$lib/host-view';
import { hostQuery, METRIC_SPECS } from '$lib/host-view';
import { rangeWindow, type RangeKey } from '$lib/ranges';
import { processServiceHref, serviceLabel } from '$lib/service-label';
import { removeAccessAction, setAccessAction } from '$lib/server/access-actions';
import { actionFailure, apiFetch, apiList, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isOrgAdmin } from '$lib/server/roles';
import { isCatalogHost } from '$lib/catalog';
import {
	parseHost,
	parseHostAccess,
	parseListPayload,
	parseMetricPayload,
	parseProcessSample,
	parseProject,
	parseService,
	parseSnapshot
} from '$lib/server/shape';
import { isUuid } from '$lib/server/validate';
import { setEdge } from '$lib/server/edge-actions';
import { applyHostFile, loadHostFiles, saveHostFile, saveSecretHostFile } from '$lib/server/host-files';
import { parseHostEdge } from '$lib/server/edge-shape';
import type { Actions, PageServerLoad, RequestEvent } from './$types';

type LoadEvent = Parameters<PageServerLoad>[0];

function hostId(params: { id?: string }): string {
	if (!params.id || !isUuid(params.id)) error(404, 'Not found');
	return params.id;
}

const noAccess = {
	canManageAccess: false,
	accessLoaded: false,
	access: [] as HostAccess[],
	accessProjects: [] as Project[],
	accessMessage: null as string | null,
	edge: null as HostEdge | null,
	edgeMessage: null as string | null
};

type AccessData = Pick<HostPage, 'org' | 'canManageAccess' | 'accessLoaded' | 'access' | 'accessProjects' | 'accessMessage' | 'edge' | 'edgeMessage'>;

function blank(tab: HostPage['tab'], range: RangeKey, processSort: ProcessSort, message: string): HostPage {
	return {
		host: null,
		org: null,
		tab,
		range,
		processSort,
		message,
		serviceLinks: {},
		metrics: [],
		processes: null,
		processesMessage: null,
		...noAccess
	};
}

async function loadMetric(event: RequestEvent, id: string, spec: (typeof METRIC_SPECS)[number], window: { from: string; to: string }): Promise<MetricChart> {
	const params = new URLSearchParams({ m: spec.m, from: window.from, to: window.to });
	try {
		const payload = await apiFetch(event, `/v1/hosts/${id}/metrics?${params.toString()}`);
		const parsed = parseMetricPayload(payload, spec.m, spec.series.map((series) => series.name));
		if (!parsed) return { m: spec.m, message: 'The API returned an unexpected response.', step: null, series: [] };
		return { m: spec.m, message: null, step: parsed.step, series: parsed.series };
	} catch (err) {
		return { m: spec.m, message: loadFailure(err).message, step: null, series: [] };
	}
}

async function loadOverview(event: LoadEvent, host: Host, range: RangeKey, processSort: ProcessSort, accessLoad: Promise<AccessData>): Promise<HostPage> {
	event.depends('host:metrics');
	const window = rangeWindow(range, Date.now());
	const [metrics, access] = await Promise.all([Promise.all(METRIC_SPECS.map((spec) => loadMetric(event, host.id, spec, window))), accessLoad]);
	return {
		host,
		tab: 'overview',
		range,
		processSort,
		message: null,
		serviceLinks: {},
		metrics,
		processes: null,
		processesMessage: null,
		...access
	};
}

/** At most this many session projects are scanned for the host, one host-list page each. Further pages are not followed. */
const HOST_SCAN_PROJECTS = 20;

/**
 * The first of at most 20 session projects whose first host page lists the host with deploy access (a project
 * that can see its services there). Projects in the host's org are checked first.
 */
async function projectForServices(event: LoadEvent, host: Host): Promise<string | null> {
	const inOrg = event.locals.projects.filter((project) => host.org_id != null && project.org_id === host.org_id);
	const others = event.locals.projects.filter((project) => !inOrg.includes(project));
	for (const project of [...inOrg, ...others].slice(0, HOST_SCAN_PROJECTS)) {
		try {
			const payload = await apiFetch(event, `/v1/projects/${project.id}/hosts?limit=200`);
			const parsed = parseListPayload(payload);
			if (!parsed) continue;
			for (const raw of parsed.raw) {
				const item = parseHost(raw);
				if (item?.id === host.id && isCatalogHost(item)) return project.id;
			}
		} catch (err) {
			if (isRedirect(err) || isHttpError(err)) throw err;
		}
	}
	return null;
}

async function readServiceLinks(event: RequestEvent, hostId: string): Promise<Record<string, ServiceLink>> {
	try {
		const parsed = parseListPayload(await apiFetch(event, `/v1/hosts/${hostId}/services`));
		if (!parsed) return {};
		const members = new Set(event.locals.projects.map((project) => project.id));
		const links: Record<string, ServiceLink> = {};
		for (const item of parsed.raw.slice(0, 500)) {
			const service = parseService(item);
			if (!service) continue;
			// Only a catalog copy has a row on the project's Catalog & services tab.
			const href =
				service.project_id && members.has(service.project_id) && service.source === 'owned' && service.catalog_entry
					? processServiceHref(service.project_id, hostId, service.catalog_entry)
					: null;
			links[service.id] = { label: serviceLabel(service), href };
		}
		return links;
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return {};
	}
}

async function readProcesses(event: RequestEvent, id: string, sort: ProcessSort): Promise<{ sample: ProcessSample | null; message: string | null }> {
	try {
		const payload = await apiFetch(event, `/v1/hosts/${id}/processes?sort=${sort}&limit=200`);
		const sample = parseProcessSample(payload);
		if (!sample) return { sample: null, message: 'The API returned an unexpected response.' };
		return { sample, message: null };
	} catch (err) {
		return { sample: null, message: loadFailure(err).message };
	}
}

async function loadProcesses(event: RequestEvent, host: Host, range: RangeKey, processSort: ProcessSort, accessLoad: Promise<AccessData>): Promise<HostPage> {
	const [processes, serviceLinks, access] = await Promise.all([readProcesses(event, host.id, processSort), readServiceLinks(event, host.id), accessLoad]);
	return {
		host,
		tab: 'processes',
		range,
		processSort,
		message: null,
		serviceLinks,
		metrics: [],
		processes: processes.sample,
		processesMessage: processes.message,
		...access
	};
}

/** Config files (api-v1 v1.44): the list for anyone who sees the host; content, keys and actions for org owners and admins. */
async function loadFiles(event: LoadEvent, host: Host, range: RangeKey, processSort: ProcessSort, accessLoad: Promise<AccessData>): Promise<HostPage> {
	const canEdit = isOrgAdmin(event.locals.orgs, host.org_id);
	const [files, access] = await Promise.all([loadHostFiles(event, host, canEdit), accessLoad]);
	return {
		host,
		tab: 'files',
		range,
		processSort,
		message: null,
		serviceLinks: {},
		metrics: [],
		processes: null,
		processesMessage: null,
		files,
		...access
	};
}

/**
 * The host's org (for the breadcrumb) and, for org owners and admins, the Access section: the host's access
 * rows and the org's projects, read together. A failure stays in the section and does not replace the page.
 */
/** The host's edge (api-v1 v1.35 item 2), for org owners and admins. A failure stays in the Edge section. */
async function loadEdge(event: RequestEvent, hostId: string): Promise<{ edge: HostEdge | null; edgeMessage: string | null }> {
	try {
		const edge = parseHostEdge(await apiFetch(event, `/v1/hosts/${hostId}/edge`));
		return edge ? { edge, edgeMessage: null } : { edge: null, edgeMessage: 'The API returned an unexpected response.' };
	} catch (err) {
		return { edge: null, edgeMessage: loadFailure(err).message };
	}
}

async function loadAccess(event: RequestEvent, host: Host): Promise<AccessData> {
	const org = event.locals.orgs.find((item) => item.id === host.org_id) ?? null;
	const orgView = org ? { id: org.id, name: org.name, role: org.role } : null;
	if (!org || !isOrgAdmin(event.locals.orgs, host.org_id)) return { org: orgView, ...noAccess };
	const edgeRead = loadEdge(event, host.id);
	try {
		const [rows, projects, edge] = await Promise.all([
			apiList(event, `/v1/hosts/${host.id}/access`, parseHostAccess),
			apiList(event, `/v1/orgs/${org.id}/projects`, parseProject),
			edgeRead
		]);
		const notes = [
			rows.omitted > 0 ? `${rows.omitted} access ${rows.omitted === 1 ? 'row' : 'rows'} could not be read.` : null,
			projects.omitted > 0 ? `${projects.omitted} ${projects.omitted === 1 ? 'project' : 'projects'} could not be read.` : null
		].filter((note) => note != null);
		return {
			org: orgView,
			canManageAccess: true,
			accessLoaded: true,
			access: rows.items,
			accessProjects: projects.items,
			accessMessage: notes.length > 0 ? notes.join(' ') : null,
			...edge
		};
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		// The table and the grant form need both reads; without them only the message shows.
		return { org: orgView, ...noAccess, canManageAccess: true, accessMessage: loadFailure(err).message, ...(await edgeRead) };
	}
}

export const load: PageServerLoad = async (event) => {
	requireUser(event);
	const id = hostId(event.params);
	const query = hostQuery(event.url);
	try {
		const host = parseHost(await apiFetch(event, `/v1/hosts/${id}`));
		if (!host) return present(event, blank(query.tab, query.range, query.processSort, 'The API returned an unexpected response.'));
		if (event.url.searchParams.get('tab') === 'services') {
			const project = await projectForServices(event, host);
			if (project) redirect(303, `/projects/${project}?tab=catalog&catalog.host=${host.id}`);
		}
		// The access reads run alongside the tab's own reads.
		const access = loadAccess(event, host);
		if (query.tab === 'processes') return present(event, await loadProcesses(event, host, query.range, query.processSort, access));
		if (query.tab === 'files') return present(event, await loadFiles(event, host, query.range, query.processSort, access));
		return present(event, await loadOverview(event, host, query.range, query.processSort, access));
	} catch (err) {
		const failure = loadFailure(err);
		return present(event, blank(query.tab, query.range, query.processSort, failure.message));
	}
};

export const actions: Actions = {
	setAccess: setAccessAction('access'),
	removeAccess: removeAccessAction('access'),
	setEdge,
	saveFile: saveHostFile,
	saveSecretFile: saveSecretHostFile,
	applyFile: applyHostFile,
	snapshot: async (event) => {
		requireUser(event);
		const id = hostId(event.params);
		try {
			const snapshot = parseSnapshot(await apiFetch(event, `/v1/hosts/${id}/snapshot`, { method: 'POST' }));
			if (!snapshot) return fail(502, present(event, { message: 'The API returned an unexpected response.' }));
			return present(event, { snapshot });
		} catch (err) {
			return actionFailure(event, err);
		}
	}
};
