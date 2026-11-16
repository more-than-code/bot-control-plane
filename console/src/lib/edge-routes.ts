import type { CatalogEntry, DomainPattern, DomainStatus, EdgeRoute, EdgeStatus, RouteState } from './api-types';

/**
 * Domains, the per-host edge and routes (api-v1 v1.35, spec `docs/spec-domains-edge-routes.md` rev 2). Shared by the
 * server actions and the components. The checks here are shape checks so a typing slip is caught before the API is
 * asked; the API stays the authority (assigned and verified domains, published ports, conflicts) and its refusal text
 * is shown as is.
 */

/** At most this many routes per entry (api-v1 v1.35 item 3). */
export const ROUTES_MAX = 16;
/** At most this many no-store path prefixes per route. */
export const NO_STORE_MAX = 8;
export const DOMAIN_MAX = 253;
export const PATH_MAX = 256;
/** Domain assignments sent in one PUT. The API checks the projects; this only bounds the form. */
export const ASSIGNMENTS_MAX = 200;

/** The runner.toml line the host's root adds before the edge can be turned on (agent-protocol v1.15). */
export const EDGE_POLICY_LINE = 'edge = true';

export const DOMAIN_PATTERNS = ['@', '*'] as const satisfies readonly DomainPattern[];

const LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const SEGMENT = '[A-Za-z0-9._~-]+';
const PATH_PREFIX = new RegExp(`^/(?:${SEGMENT}(?:/${SEGMENT})*)?$`);
/** At least one segment: a bare `/` is not a no-store prefix. A trailing `/` is allowed. */
const NO_STORE_PATH = new RegExp(`^(?:/${SEGMENT})+/?$`);

/** `.` and `..` are never path segments (the API refuses them). */
function hasDotSegment(value: string): boolean {
	return value.split('/').some((segment) => segment === '.' || segment === '..');
}

/** A lowercase DNS name with at least two labels (IDNA A-labels), not an IP address, at most 253 characters. */
export function isDomainName(value: string): boolean {
	if (value.length === 0 || value.length > DOMAIN_MAX) return false;
	const labels = value.split('.');
	if (labels.length < 2 || !labels.every((label) => LABEL.test(label))) return false;
	// An all-digit last label is an IPv4 address or not a name at all.
	return !/^[0-9]+$/.test(labels[labels.length - 1]);
}

/** Trimmed, lowercased and without a trailing dot; null when it is not a domain name. */
export function cleanDomainName(raw: string): string | null {
	const value = raw.trim().toLowerCase().replace(/\.$/, '');
	return isDomainName(value) ? value : null;
}

/** `/` or `/seg[/seg…]`, segments of `A-Z a-z 0-9 . _ ~ -`. */
export function isPathPrefix(value: string): boolean {
	return value.length <= PATH_MAX && PATH_PREFIX.test(value) && !hasDotSegment(value);
}

/** A path prefix that may end in `/`, such as `/evidence/`. */
export function isNoStorePath(value: string): boolean {
	return value.length <= PATH_MAX && NO_STORE_PATH.test(value) && !hasDotSegment(value);
}

export function isDomainPattern(value: unknown): value is DomainPattern {
	return value === '@' || value === '*';
}

/** A route or domain plan (202). `expires_at` is optional: the domain delete answer has none (api-v1 v1.35 item 1). */
export type EdgePlan = { confirmation_id: string; plan: string; expires_at: string | null };

/** A domain's Delete plan, shown in its row until confirmed. The key makes a repeated Confirm the same request. */
export type DomainPlan = EdgePlan & { domainId: string; idempotency_key: string };

/** One route as the editor holds it. `no_store` is one path per line. */
export type RouteDraft = {
	hostname: string;
	path_prefix: string;
	port: string;
	cloudflare: boolean;
	no_store: string;
};

/** One route of the PUT body. Optional fields are left out when unset. */
export type RouteBody = {
	hostname: string;
	path_prefix: string;
	port: number;
	client_ip_from?: 'cloudflare';
	no_store_paths?: string[];
};

export function routeDraftOf(route: EdgeRoute): RouteDraft {
	return {
		hostname: route.hostname,
		path_prefix: route.path_prefix,
		port: String(route.port),
		cloudflare: route.client_ip_from === 'cloudflare',
		no_store: route.no_store_paths.join('\n')
	};
}

export function blankRouteDraft(port: number | null): RouteDraft {
	return { hostname: '', path_prefix: '/', port: port == null ? '' : String(port), cloudflare: false, no_store: '' };
}

/** `https://host/path`, as the plan and the route list name it. */
export function routeUrl(route: { hostname: string; path_prefix: string }): string {
	return `https://${route.hostname}${route.path_prefix === '' ? '/' : route.path_prefix}`;
}

function portOf(value: string | number): number | null {
	const text = typeof value === 'number' ? String(value) : value.trim();
	if (!/^[0-9]{1,5}$/.test(text)) return null;
	const port = Number(text);
	return port >= 1 && port <= 65535 ? port : null;
}

/** One route checked and shaped for the API, or the first problem as a sentence. `n` is 1-based. */
function routeBodyOf(input: { hostname: string; path_prefix: string; port: string | number; cloudflare: boolean; no_store: string[] }, n: number): RouteBody | string {
	const hostname = input.hostname.trim().toLowerCase().replace(/\.$/, '');
	if (!isDomainName(hostname)) return `Route ${n}: enter a host name such as app.example.com.`;
	const path_prefix = input.path_prefix.trim() === '' ? '/' : input.path_prefix.trim();
	if (!isPathPrefix(path_prefix)) return `Route ${n}: the path must be / or start with / and name path segments, such as /api.`;
	const port = portOf(input.port);
	if (port == null) return `Route ${n}: choose a port (1–65535).`;
	const paths = input.no_store.map((path) => path.trim()).filter((path) => path !== '');
	if (paths.length > NO_STORE_MAX) return `Route ${n}: at most ${NO_STORE_MAX} no-store paths.`;
	const bad = paths.find((path) => !isNoStorePath(path));
	if (bad != null) return `Route ${n}: ${bad.slice(0, 80)} is not a path such as /evidence/.`;
	const body: RouteBody = { hostname, path_prefix, port };
	if (input.cloudflare) body.client_ip_from = 'cloudflare';
	if (paths.length > 0) body.no_store_paths = [...new Set(paths)];
	return body;
}

function checkList(bodies: RouteBody[]): string | null {
	if (bodies.length > ROUTES_MAX) return `At most ${ROUTES_MAX} routes per entry.`;
	const seen = new Set<string>();
	for (const body of bodies) {
		const url = routeUrl(body);
		if (seen.has(url)) return `${url} is listed twice.`;
		seen.add(url);
	}
	return null;
}

/** The editor's rows as the PUT body's `routes`, or the first problem. */
export function routesFromDrafts(drafts: readonly RouteDraft[]): { ok: true; routes: RouteBody[] } | { ok: false; message: string } {
	const routes: RouteBody[] = [];
	for (const [index, draft] of drafts.entries()) {
		const body = routeBodyOf({ ...draft, no_store: draft.no_store.split('\n') }, index + 1);
		if (typeof body === 'string') return { ok: false, message: body };
		routes.push(body);
	}
	const problem = checkList(routes);
	return problem ? { ok: false, message: problem } : { ok: true, routes };
}

/**
 * The `routes` form field (JSON from the editor), checked again on the server with an allowlist: only the known keys
 * are read, anything else is dropped. Null with a message when it is not a valid list.
 */
export function routesFromField(raw: string): { ok: true; routes: RouteBody[] } | { ok: false; message: string } {
	const invalid = { ok: false as const, message: 'The routes are not valid.' };
	if (raw.length === 0 || raw.length > 64_000) return invalid;
	let value: unknown;
	try {
		value = JSON.parse(raw);
	} catch {
		return invalid;
	}
	if (!Array.isArray(value)) return invalid;
	if (value.length > ROUTES_MAX) return { ok: false, message: `At most ${ROUTES_MAX} routes per entry.` };
	const routes: RouteBody[] = [];
	for (const [index, item] of value.entries()) {
		if (item == null || typeof item !== 'object' || Array.isArray(item)) return invalid;
		const record = item as Record<string, unknown>;
		if (typeof record.hostname !== 'string' || typeof record.path_prefix !== 'string') return invalid;
		if (typeof record.port !== 'number' && typeof record.port !== 'string') return invalid;
		if (record.client_ip_from != null && record.client_ip_from !== 'cloudflare') return invalid;
		const paths = record.no_store_paths ?? [];
		if (!Array.isArray(paths) || paths.length > NO_STORE_MAX || !paths.every((path) => typeof path === 'string')) return invalid;
		const body = routeBodyOf(
			{ hostname: record.hostname, path_prefix: record.path_prefix, port: record.port, cloudflare: record.client_ip_from === 'cloudflare', no_store: paths as string[] },
			index + 1
		);
		if (typeof body === 'string') return { ok: false, message: body };
		routes.push(body);
	}
	const problem = checkList(routes);
	return problem ? { ok: false, message: problem } : { ok: true, routes };
}

/**
 * The ports a route of this entry may use: an image's published container ports (a route needs a host port on
 * 127.0.0.1, api-v1 v1.35 item 3), sorted and unique. Null for a program, whose port is typed (its spec port, if
 * any, is the default).
 */
export function routePorts(entry: CatalogEntry): number[] | null {
	if (entry.kind !== 'image') return null;
	return [...new Set(entry.spec.ports.map((port) => port.container))].sort((a, b) => a - b);
}

/** The port a new route row starts with. */
export function defaultRoutePort(entry: CatalogEntry): number | null {
	if (entry.kind === 'program') return entry.spec.port;
	return routePorts(entry)?.[0] ?? null;
}

/** StatusBadge colour and word for a route's state. */
export function routeStateBadge(state: RouteState): { status: string; text: string } {
	switch (state) {
		case 'live':
			return { status: 'up', text: 'Live' };
		case 'error':
			return { status: 'down', text: 'Error' };
		default:
			return { status: 'asleep', text: 'Pending' };
	}
}

export function domainStatusBadge(status: DomainStatus): { status: string; text: string } {
	switch (status) {
		case 'verified':
			return { status: 'up', text: 'Verified' };
		case 'lapsed':
			return { status: 'down', text: 'Lapsed' };
		default:
			return { status: 'asleep', text: 'Pending' };
	}
}

export function edgeStatusBadge(status: EdgeStatus): { status: string; text: string } {
	switch (status) {
		case 'applied':
			return { status: 'up', text: 'Applied' };
		case 'pending':
			return { status: 'asleep', text: 'Pending' };
		case 'error':
			return { status: 'down', text: 'Error' };
		default:
			return { status: 'offline', text: 'Off' };
	}
}

/** What a domain assignment covers, in words. */
export function assignmentText(domain: string, pattern: DomainPattern): string {
	return pattern === '@' ? domain : `*.${domain}`;
}
