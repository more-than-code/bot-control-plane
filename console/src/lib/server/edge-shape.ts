import type { DomainAssignment, EdgeRoute, EdgeStatus, HostEdge, OrgDomain, RouteState } from '../api-types';
import { isConfirmationId } from '../image-spec';
import { isDomainName, isDomainPattern, isNoStorePath, isPathPrefix, NO_STORE_MAX, type EdgePlan } from '../edge-routes';
import { isRecord, parseListPayload } from './shape';
import { isUuid } from './validate';

/**
 * Allowlist parsers for api-v1 v1.35 (domains, the host edge, routes). Only the listed fields are kept. A value that
 * does not parse is dropped (a list item) or the whole answer is "unexpected" (a single object).
 */

const CONTROL = /[\u0000-\u001F\u007F]/;
const CONTROL_ALL = new RegExp(CONTROL.source, 'g');
/** A domain id is used in an API path: a uuid, or at least a short token with no path characters. */
const DOMAIN_ID = /^[A-Za-z0-9_-]{1,64}$/;
const LIST_CAP = 200;
const ASSIGNMENT_CAP = 500;

function text(value: unknown, max: number): string | null {
	if (typeof value !== 'string' || value.length > max || CONTROL.test(value)) return null;
	return value;
}

/** Null when absent or null; undefined when present and not a short timestamp string. */
function optionalTime(value: unknown): string | null | undefined {
	if (value == null) return null;
	return text(value, 64) ?? undefined;
}

/** Agent and API detail text for display: control characters become U+FFFD, capped with an ellipsis. */
function displayText(value: string, max: number): string {
	const cleaned = value.replaceAll('\t', ' ').replace(CONTROL_ALL, '�');
	return cleaned.length <= max ? cleaned : `${cleaned.slice(0, max - 1)}…`;
}

export function isDomainId(value: string): boolean {
	return DOMAIN_ID.test(value);
}

function parseAssignment(value: unknown): DomainAssignment | null {
	if (!isRecord(value)) return null;
	const project_id = text(value.project_id, 36);
	if (!project_id || !isUuid(project_id) || !isDomainPattern(value.pattern)) return null;
	const name = typeof value.project_name === 'string' ? displayText(value.project_name, 120) : '';
	return { project_id, project_name: name, pattern: value.pattern };
}

export function parseOrgDomain(value: unknown): OrgDomain | null {
	if (!isRecord(value)) return null;
	const id = text(value.id, 64);
	const name = text(value.name, 253);
	if (!id || !isDomainId(id) || !name || !isDomainName(name)) return null;
	if (value.status !== 'pending' && value.status !== 'verified' && value.status !== 'lapsed') return null;
	const txt_name = text(value.txt_name, 300);
	if (txt_name !== `_deploybot.${name}`) return null;
	const txt_value = text(value.txt_value, 300);
	if (!txt_value || !txt_value.startsWith('deploybot-verify=') || /\s/.test(txt_value)) return null;
	const verified_at = optionalTime(value.verified_at);
	const last_check_at = optionalTime(value.last_check_at);
	if (verified_at === undefined || last_check_at === undefined) return null;
	const raw = value.assignments ?? [];
	if (!Array.isArray(raw) || raw.length > ASSIGNMENT_CAP) return null;
	const assignments: DomainAssignment[] = [];
	for (const item of raw) {
		const assignment = parseAssignment(item);
		if (!assignment) return null;
		assignments.push(assignment);
	}
	return { id, name, status: value.status, txt_name, txt_value, verified_at, last_check_at, assignments };
}

/** `GET /v1/orgs/{id}/domains`: the items that parse and how many did not. Null when the answer is not a list. */
export function parseDomainList(payload: unknown): { items: OrgDomain[]; omitted: number } | null {
	const page = parseListPayload(payload);
	if (!page) return null;
	const raw = page.raw.slice(0, LIST_CAP);
	let omitted = page.raw.length - raw.length;
	const items: OrgDomain[] = [];
	for (const value of raw) {
		const domain = parseOrgDomain(value);
		if (domain) items.push(domain);
		else omitted += 1;
	}
	return { items, omitted };
}

const EDGE_STATUS = new Set<EdgeStatus>(['off', 'pending', 'applied', 'error']);

export function parseHostEdge(value: unknown): HostEdge | null {
	if (!isRecord(value) || typeof value.available !== 'boolean' || typeof value.enabled !== 'boolean') return null;
	if (typeof value.status !== 'string' || !EDGE_STATUS.has(value.status as EdgeStatus)) return null;
	const detail = value.detail == null || value.detail === '' ? null : typeof value.detail === 'string' ? displayText(value.detail, 1000) : undefined;
	if (detail === undefined) return null;
	let config_hash: string | null = null;
	if (value.config_hash != null && value.config_hash !== '') {
		config_hash = text(value.config_hash, 128);
		if (config_hash === null) return null;
	}
	const applied_at = optionalTime(value.applied_at);
	const routes = value.routes ?? 0;
	if (applied_at === undefined || typeof routes !== 'number' || !Number.isInteger(routes) || routes < 0) return null;
	return { available: value.available, enabled: value.enabled, status: value.status as EdgeStatus, detail, config_hash, applied_at, routes };
}

const ROUTE_STATES = new Set<RouteState>(['live', 'pending', 'error']);

export function parseEdgeRoute(value: unknown): EdgeRoute | null {
	if (!isRecord(value)) return null;
	const hostname = text(value.hostname, 253);
	if (!hostname || !isDomainName(hostname)) return null;
	const path_prefix = value.path_prefix == null || value.path_prefix === '' ? '/' : text(value.path_prefix, 256);
	if (!path_prefix || !isPathPrefix(path_prefix)) return null;
	if (typeof value.port !== 'number' || !Number.isInteger(value.port) || value.port < 1 || value.port > 65535) return null;
	if (value.client_ip_from != null && value.client_ip_from !== 'cloudflare') return null;
	const rawPaths = value.no_store_paths ?? [];
	if (!Array.isArray(rawPaths) || rawPaths.length > NO_STORE_MAX) return null;
	const no_store_paths: string[] = [];
	for (const path of rawPaths) {
		if (typeof path !== 'string' || !isNoStorePath(path)) return null;
		no_store_paths.push(path);
	}
	const host_id = value.host_id == null || value.host_id === '' ? null : text(value.host_id, 36);
	if (host_id === null && value.host_id != null && value.host_id !== '') return null;
	if (host_id !== null && !isUuid(host_id)) return null;
	if (typeof value.state !== 'string' || !ROUTE_STATES.has(value.state as RouteState)) return null;
	return {
		hostname,
		path_prefix,
		port: value.port,
		client_ip_from: value.client_ip_from === 'cloudflare' ? 'cloudflare' : null,
		no_store_paths,
		host_id,
		state: value.state as RouteState
	};
}

/** An entry's routes. Null when the answer is not a list. */
export function parseRouteList(payload: unknown): { items: EdgeRoute[]; omitted: number } | null {
	const page = parseListPayload(payload);
	if (!page) return null;
	const raw = page.raw.slice(0, LIST_CAP);
	let omitted = page.raw.length - raw.length;
	const items: EdgeRoute[] = [];
	for (const value of raw) {
		const route = parseEdgeRoute(value);
		if (route) items.push(route);
		else omitted += 1;
	}
	return { items, omitted };
}


export function parseEdgePlan(value: unknown): EdgePlan | null {
	if (!isRecord(value)) return null;
	const confirmation_id = text(value.confirmation_id, 43);
	if (!confirmation_id || !isConfirmationId(confirmation_id)) return null;
	if (typeof value.plan !== 'string' || value.plan.length === 0 || value.plan.length > 8000) return null;
	const expires_at = optionalTime(value.expires_at);
	if (expires_at === undefined) return null;
	// Keep line breaks (one per hostname); other control characters become U+FFFD.
	const plan = value.plan
		.split('\n')
		.map((line) => displayText(line, 8000))
		.join('\n');
	return { confirmation_id, plan, expires_at };
}
