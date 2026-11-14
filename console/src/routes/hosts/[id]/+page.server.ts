import { error, fail, isHttpError, isRedirect, redirect } from '@sveltejs/kit';
import type { Host, HostAccess, ProcessSample, Project } from '$lib/api-types';
import type { HostPage, MetricChart, ProcessSort, ServiceLink } from '$lib/host-view';
import { hostQuery, METRIC_SPECS } from '$lib/host-view';
import { rangeWindow, type RangeKey } from '$lib/ranges';
import { processServiceHref, serviceLabel } from '$lib/service-label';
import { removeAccessAction, setAccessAction } from '$lib/server/access-actions';
import { actionFailure, apiFetch, apiList, loadFailure } from '$lib/server/api';
import { requireUser } from '$lib/server/guard';
import { present } from '$lib/server/redact';
import { isOrgAdmin } from '$lib/server/roles';
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
import type { Actions, PageServerLoad, RequestEvent } from './$types';

type LoadEvent = Parameters<PageServerLoad>[0];

function hostId(params: { id?: string }): string {
	if (!params.id || !isUuid(params.id)) error(404, 'Not found');
	return params.id;
}

const noAccess = {
	canManageAccess: false,
	access: [] as HostAccess[],
	accessProjects: [] as Project[],
	accessMessage: null as string | null
};

type AccessData = Pick<HostPage, 'org' | 'canManageAccess' | 'access' | 'accessProjects' | 'accessMessage'>;

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

async function loadOverview(event: LoadEvent, host: Host, range: RangeKey, processSort: ProcessSort, access: AccessData): Promise<HostPage> {
	event.depends('host:metrics');
	const window = rangeWindow(range, Date.now());
	const metrics = await Promise.all(METRIC_SPECS.map((spec) => loadMetric(event, host.id, spec, window)));
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

/** One host-list page per project. Further pages are not followed. */
const SHARE_SCAN_PROJECTS = 20;

/**
 * The first of at most 20 session projects whose first host page lists the host with deploy access (a project
 * that can see its services there). Projects in the host's org are checked first.
 */
async function projectForServices(event: LoadEvent, host: Host): Promise<string | null> {
	const inOrg = event.locals.projects.filter((project) => host.org_id != null && project.org_id === host.org_id);
	const others = event.locals.projects.filter((project) => !inOrg.includes(project));
	for (const project of [...inOrg, ...others].slice(0, SHARE_SCAN_PROJECTS)) {
		try {
			const payload = await apiFetch(event, `/v1/projects/${project.id}/hosts?limit=200`);
			const parsed = parseListPayload(payload);
			if (!parsed) continue;
			for (const raw of parsed.raw) {
				const item = parseHost(raw);
				if (item?.id === host.id && item.access_level === 'deploy') return project.id;
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

async function loadProcesses(event: RequestEvent, host: Host, range: RangeKey, processSort: ProcessSort, access: AccessData): Promise<HostPage> {
	const [processes, serviceLinks] = await Promise.all([readProcesses(event, host.id, processSort), readServiceLinks(event, host.id)]);
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

/**
 * The host's org (for the breadcrumb) and, for org owners and admins, the Access section: the host's access
 * rows and the org's projects, read together. A failure stays in the section and does not replace the page.
 */
async function loadAccess(event: RequestEvent, host: Host): Promise<AccessData> {
	const org = event.locals.orgs.find((item) => item.id === host.org_id) ?? null;
	const orgView = org ? { id: org.id, name: org.name, role: org.role } : null;
	if (!org || !isOrgAdmin(event.locals.orgs, host.org_id)) return { org: orgView, ...noAccess };
	try {
		const [rows, projects] = await Promise.all([
			apiList(event, `/v1/hosts/${host.id}/access`, parseHostAccess),
			apiList(event, `/v1/orgs/${org.id}/projects`, parseProject)
		]);
		const omitted = rows.omitted + projects.omitted;
		return {
			org: orgView,
			canManageAccess: true,
			access: rows.items,
			accessProjects: projects.items,
			accessMessage: omitted > 0 ? `${omitted} access ${omitted === 1 ? 'row' : 'rows'} could not be read.` : null
		};
	} catch (err) {
		if (isRedirect(err) || isHttpError(err)) throw err;
		return { org: orgView, ...noAccess, canManageAccess: true, accessMessage: loadFailure(err).message };
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
		const access = await loadAccess(event, host);
		if (query.tab === 'processes') return present(event, await loadProcesses(event, host, query.range, query.processSort, access));
		return present(event, await loadOverview(event, host, query.range, query.processSort, access));
	} catch (err) {
		const failure = loadFailure(err);
		return present(event, blank(query.tab, query.range, query.processSort, failure.message));
	}
};

export const actions: Actions = {
	setAccess: setAccessAction('access'),
	removeAccess: removeAccessAction('access'),
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
