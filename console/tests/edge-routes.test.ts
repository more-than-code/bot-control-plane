// Domains, the per-host edge and routes in the console (umbrella docs/spec-domains-edge-routes.md rev 2 §4, api-v1 v1.35).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { HostEdge, ImageEntry, OrgDomain, ProgramEntry } from '../src/lib/api-types.ts';
import {
	assignLinkRow,
	assignmentText,
	cleanDomainName,
	cleanDomainPattern,
	defaultRoutePort,
	isDomainName,
	isDomainPatternFor,
	isNoStorePath,
	isPathPrefix,
	routePorts,
	routesFromDrafts,
	routesFromField,
	routeUrl,
	withoutAssignLink
} from '../src/lib/edge-routes.ts';
import { checkLabel, parseReadiness } from '../src/lib/settings-auto.ts';
import { parseDomainList, parseEdgePlan, parseEdgeRoute, parseHostEdge, parseOrgDomain, parseRouteList } from '../src/lib/server/edge-shape.ts';
import { parseHostPrograms } from '../src/lib/server/shape.ts';
import HostEdgeSection from '../src/routes/hosts/[id]/HostEdgeSection.svelte';
import { actions as hostActions } from '../src/routes/hosts/[id]/+page.server.ts';
import OrgDomainsSection from '../src/routes/orgs/[id]/OrgDomainsSection.svelte';
import { actions as orgActions, load as orgLoad } from '../src/routes/orgs/[id]/+page.server.ts';
import EntryRoutes from '../src/routes/projects/[id]/EntryRoutes.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { page, setPageUrl } from './stubs/app-state.ts';

const HOST_ID = '00000000-0000-4000-8000-0000000000c1';
const DOMAIN_ID = '00000000-0000-4000-8000-0000000000c2';
const CONFIRMATION = 'c'.repeat(43);
const KEY = '00000000-0000-4000-8000-0000000000c3';

const RAW_DOMAIN = {
	id: DOMAIN_ID,
	name: 'defectdrainer.com',
	status: 'pending',
	txt_name: '_deploybot.defectdrainer.com',
	txt_value: 'deploybot-verify=abc123',
	verified_at: null,
	last_check_at: null,
	assignments: [{ project_id: PROJECT.id, project_name: 'Lab', pattern: '@' }]
};

const RAW_ROUTE = {
	hostname: 'defectdrainer.com',
	path_prefix: '/',
	port: 8788,
	client_ip_from: 'cloudflare',
	no_store_paths: ['/evidence/'],
	host_id: HOST_ID,
	state: 'live'
};

const RAW_EDGE = { available: true, enabled: false, status: 'off', detail: '', config_hash: null, applied_at: null, routes: 0 };

function domain(overrides: Partial<OrgDomain> = {}): OrgDomain {
	const parsed = parseOrgDomain(RAW_DOMAIN);
	if (!parsed) assert.fail('fixture domain does not parse');
	return { ...parsed, ...overrides };
}

function imageEntry(ports: Array<{ host: number; container: number }>): ImageEntry {
	return { name: 'serve', kind: 'image', spec: { ports: ports.map((port) => ({ ...port, public: false })) } } as unknown as ImageEntry;
}

function programEntry(port: number | null): ProgramEntry {
	return { name: 'api', kind: 'program', spec: { port } } as unknown as ProgramEntry;
}

function orgEvent(url: string, form?: Record<string, string | readonly string[]>) {
	return fakeEvent({ url, session: TOKEN, user: USER, projects: [PROJECT], orgs: [ORG], params: { id: ORG.id }, form }).event as never;
}

function projectEvent(action: string, form: Record<string, string>) {
	return fakeEvent({ url: `http://console.example/projects/${PROJECT.id}?/${action}`, form, params: { id: PROJECT.id }, user: USER, session: TOKEN }).event as never;
}

describe('edge route rules', () => {
	test('domain names: lowercase labels, at least two, not an IP', () => {
		assert.equal(isDomainName('example.com'), true);
		assert.equal(isDomainName('xn--bcher-kva.example'), true);
		assert.equal(isDomainName('example'), false);
		assert.equal(isDomainName('Example.com'), false);
		assert.equal(isDomainName('192.168.1.1'), false);
		assert.equal(isDomainName('-bad.com'), false);
		assert.equal(isDomainName('*.example.com'), false);
		assert.equal(cleanDomainName('  Example.COM. '), 'example.com');
		assert.equal(cleanDomainName('http://example.com'), null);
	});

	test('path prefix and no-store paths', () => {
		assert.equal(isPathPrefix('/'), true);
		assert.equal(isPathPrefix('/api/v1'), true);
		assert.equal(isPathPrefix('/api/'), false);
		assert.equal(isPathPrefix('api'), false);
		assert.equal(isPathPrefix('/a//b'), false);
		assert.equal(isNoStorePath('/evidence/'), true);
		assert.equal(isNoStorePath('/evidence'), true);
		assert.equal(isNoStorePath('/a b'), false);
		// The API's rule: segments of A-Z a-z 0-9 . _ ~ -, never . or .., and a no-store prefix names at least one segment.
		assert.equal(isPathPrefix('/a@b'), false);
		assert.equal(isPathPrefix('/a/../b'), false);
		assert.equal(isPathPrefix('/.'), false);
		assert.equal(isPathPrefix('/v1.2/x_y~z-w'), true);
		assert.equal(isNoStorePath('/'), false);
		assert.equal(isNoStorePath('/evidence/..'), false);
		assert.equal(isNoStorePath('/a%20b'), false);
	});

	test('drafts become the PUT body; options only when set', () => {
		const built = routesFromDrafts([
			{ hostname: 'DefectDrainer.com', path_prefix: '', port: '8788', cloudflare: true, no_store: '/evidence/\n\n/evidence/' },
			{ hostname: 'daikou.online', path_prefix: '/app', port: '3000', cloudflare: false, no_store: '' }
		]);
		assert.deepEqual(built, {
			ok: true,
			routes: [
				{ hostname: 'defectdrainer.com', path_prefix: '/', port: 8788, client_ip_from: 'cloudflare', no_store_paths: ['/evidence/'] },
				{ hostname: 'daikou.online', path_prefix: '/app', port: 3000 }
			]
		});
	});

	test('drafts: bad host, port, duplicate and too many no-store paths are refused with a sentence', () => {
		const row = { hostname: 'a.example.com', path_prefix: '/', port: '80', cloudflare: false, no_store: '' };
		const badHost = routesFromDrafts([{ ...row, hostname: 'nope' }]);
		assert.equal(badHost.ok, false);
		assert.match((badHost as { message: string }).message, /Route 1: enter a host name/);
		assert.equal(routesFromDrafts([{ ...row, port: '70000' }]).ok, false);
		const dup = routesFromDrafts([row, { ...row }]);
		assert.deepEqual(dup, { ok: false, message: 'https://a.example.com/ is listed twice.' });
		const many = routesFromDrafts([{ ...row, no_store: Array.from({ length: 9 }, (_, i) => `/p${i}`).join('\n') }]);
		assert.equal(many.ok, false);
		assert.equal(routesFromDrafts(Array.from({ length: 17 }, (_, i) => ({ ...row, hostname: `h${i}.example.com` }))).ok, false);
	});

	test('the routes field is re-checked on the server; unknown keys are dropped', () => {
		const raw = JSON.stringify([{ hostname: 'a.example.com', path_prefix: '/', port: 80, extra: 'x', client_ip_from: null }]);
		assert.deepEqual(routesFromField(raw), { ok: true, routes: [{ hostname: 'a.example.com', path_prefix: '/', port: 80 }] });
		assert.equal(routesFromField('{').ok, false);
		assert.equal(routesFromField('{}').ok, false);
		assert.equal(routesFromField(JSON.stringify([{ hostname: 'a.example.com', path_prefix: '/', port: 80, client_ip_from: 'akamai' }])).ok, false);
		assert.deepEqual(routesFromField('[]'), { ok: true, routes: [] });
	});

	test('ports: an image offers its published container ports; a program its own port', () => {
		assert.deepEqual(routePorts(imageEntry([{ host: 18788, container: 8788 }, { host: 13000, container: 3000 }, { host: 13001, container: 3000 }])), [3000, 8788]);
		assert.deepEqual(routePorts(imageEntry([])), []);
		assert.equal(routePorts(programEntry(8080)), null);
		assert.equal(defaultRoutePort(programEntry(8080)), 8080);
		assert.equal(defaultRoutePort(imageEntry([{ host: 1, container: 8788 }])), 8788);
		assert.equal(routeUrl({ hostname: 'a.example.com', path_prefix: '/api' }), 'https://a.example.com/api');
	});
});

describe('v1.52 assignment patterns', () => {
	test('a pattern is @, *, a name below the domain, or *. and a name', () => {
		for (const pattern of ['@', '*', 'app', 'api.dev', '*.dev', 'a-b.c']) assert.equal(isDomainPatternFor('example.com', pattern), true, pattern);
		for (const pattern of ['', '*', '**', 'a..b', '-app', 'app.', '*.*', 'A', 'app/x', null, 7]) {
			if (pattern === '*') continue;
			assert.equal(isDomainPatternFor('example.com', pattern), false, String(pattern));
		}
		// Written out, a pattern may not pass 253 characters.
		assert.equal(isDomainPatternFor('example.com', 'a'.repeat(242)), false);
		assert.equal(isDomainPatternFor('example.com', 'a'.repeat(60)), true);
	});

	test('a typed full name or trailing dot becomes the pattern it means', () => {
		assert.equal(cleanDomainPattern('example.com', ' App.Example.com. '), 'app');
		assert.equal(cleanDomainPattern('example.com', 'Example.com'), '@');
		assert.equal(cleanDomainPattern('example.com', '*.dev.example.com'), '*.dev');
		assert.equal(cleanDomainPattern('example.com', '*'), '*');
		assert.equal(cleanDomainPattern('example.com', 'notadomain..'), null);
		assert.equal(cleanDomainPattern('example.com', 7), null);
	});

	test('assignmentText writes the pattern out', () => {
		assert.equal(assignmentText('example.com', '@'), 'example.com');
		assert.equal(assignmentText('example.com', '*'), '*.example.com');
		assert.equal(assignmentText('example.com', 'app'), 'app.example.com');
		assert.equal(assignmentText('example.com', '*.dev'), '*.dev.example.com');
	});

	test('an assign link opens only for its own domain, project and a pattern that fits', () => {
		const dom = { id: DOMAIN_ID, name: 'defectdrainer.com' };
		const params = (q: Record<string, string>) => new URLSearchParams(q);
		assert.deepEqual(assignLinkRow(params({ 'domains.assign': DOMAIN_ID, 'domains.project': PROJECT.id, 'domains.pattern': 'marketingos' }), dom, [PROJECT.id]), {
			project_id: PROJECT.id,
			pattern: 'marketingos'
		});
		// Another domain's link, an unknown project and a pattern that does not fit all open nothing.
		assert.equal(assignLinkRow(params({ 'domains.assign': CONFIRMATION, 'domains.project': PROJECT.id, 'domains.pattern': 'app' }), dom, [PROJECT.id]), null);
		assert.equal(assignLinkRow(params({ 'domains.assign': DOMAIN_ID, 'domains.project': KEY, 'domains.pattern': 'app' }), dom, [PROJECT.id]), null);
		assert.equal(assignLinkRow(params({ 'domains.assign': DOMAIN_ID, 'domains.project': PROJECT.id, 'domains.pattern': '**' }), dom, [PROJECT.id]), null);
		assert.equal(assignLinkRow(params({}), dom, [PROJECT.id]), null);
	});

	test('the link keys are dropped from the URL, and nothing else', () => {
		const url = new URL(`http://console.example/orgs/${ORG.id}?tab=settings&domains.assign=${DOMAIN_ID}&domains.project=${PROJECT.id}&domains.pattern=app`);
		assert.equal(withoutAssignLink(url).search, '?tab=settings');
		assert.match(url.search, /domains\.assign/);
	});
});

describe('v1.35 parsers', () => {
	test('a domain keeps only the listed fields and checks the TXT name', () => {
		const parsed = parseOrgDomain({ ...RAW_DOMAIN, secret: 'x' });
		assert.equal(parsed?.txt_value, 'deploybot-verify=abc123');
		assert.equal(Object.hasOwn(parsed ?? {}, 'secret'), false);
		assert.equal(parseOrgDomain({ ...RAW_DOMAIN, txt_name: '_deploybot.other.com' }), null);
		assert.equal(parseOrgDomain({ ...RAW_DOMAIN, status: 'ok' }), null);
		assert.equal(parseOrgDomain({ ...RAW_DOMAIN, id: '../x' }), null);
		assert.equal(parseOrgDomain({ ...RAW_DOMAIN, assignments: [{ project_id: PROJECT.id, pattern: '**' }] }), null);
		const list = parseDomainList({ items: [RAW_DOMAIN, { ...RAW_DOMAIN, status: 'nope' }] });
		assert.equal(list?.items.length, 1);
		assert.equal(list?.omitted, 1);
	});

	test('the host edge', () => {
		assert.deepEqual(parseHostEdge(RAW_EDGE), { available: true, enabled: false, status: 'off', detail: null, config_hash: null, applied_at: null, routes: 0 });
		assert.equal(parseHostEdge({ ...RAW_EDGE, status: 'running' }), null);
		assert.equal(parseHostEdge({ ...RAW_EDGE, routes: -1 }), null);
		assert.equal(parseHostEdge({ ...RAW_EDGE, detail: 'port 443\u0007' })?.detail, 'port 443�');
	});

	test('routes and plans', () => {
		assert.deepEqual(parseEdgeRoute(RAW_ROUTE), RAW_ROUTE);
		assert.equal(parseEdgeRoute({ ...RAW_ROUTE, state: 'up' }), null);
		assert.equal(parseEdgeRoute({ ...RAW_ROUTE, port: 0 }), null);
		assert.equal(parseEdgeRoute({ ...RAW_ROUTE, host_id: 'pi3' }), null);
		assert.equal(parseRouteList({ items: [RAW_ROUTE, {}] })?.omitted, 1);
		assert.deepEqual(parseEdgePlan({ confirmation_id: CONFIRMATION, plan: 'routes for serve:\nadd https://a/' }), {
			confirmation_id: CONFIRMATION,
			plan: 'routes for serve:\nadd https://a/',
			expires_at: null
		});
		assert.equal(parseEdgePlan({ confirmation_id: 'short', plan: 'x' }), null);
	});

	test('programs.edge: missing is null, malformed is no policy', () => {
		const base = { enabled: true, run_as: [], allow_shell: false, policy_hash: 'p' };
		assert.equal(parseHostPrograms(base)?.edge, null);
		assert.deepEqual(parseHostPrograms({ ...base, edge: { enabled: true, image: 'caddy@sha256:abc' } })?.edge, { enabled: true, image: 'caddy@sha256:abc' });
		assert.equal(parseHostPrograms({ ...base, edge: { enabled: 'yes' } }), null);
	});

	test('readiness knows the routes check', () => {
		const readiness = parseReadiness({ ready: true, checks: [{ check: 'routes', status: 'warn', message: 'edge off on pi3', fix: { action: 'none' } }] });
		assert.equal(readiness?.checks[0].check, 'routes');
		assert.equal(checkLabel('routes'), 'Public addresses');
	});
});

describe('v1.35 actions', () => {
	before(() => useApi('https://api.test'));
	after(() => restoreEnv());

	const settings = `http://console.example/orgs/${ORG.id}?tab=settings`;

	test('org Settings loads the domains for an admin', async () => {
		const calls = installFetch((call) => {
			if (call.url.pathname === `/v1/orgs/${ORG.id}`) return envelope({ id: ORG.id, name: ORG.name, slug: ORG.slug, role: 'owner', created_at: ORG.created_at });
			if (call.url.pathname.endsWith('/domains')) return envelope({ items: [RAW_DOMAIN] });
			return envelope({ items: [] });
		});
		const data = (await orgLoad(orgEvent(settings))) as { domains: { items: OrgDomain[] } };
		assert.equal(data.domains.items[0].name, 'defectdrainer.com');
		assert.equal(calls.some((call) => call.url.pathname === `/v1/orgs/${ORG.id}/domains`), true);
	});

	test('add domain posts the cleaned name; a bad name never reaches the API', async () => {
		const calls = installFetch(() => envelope(RAW_DOMAIN, 201));
		await assert.rejects(async () => orgActions.addDomain(orgEvent(`${settings}&/addDomain`, { name: ' DefectDrainer.com ' })), (err: unknown) => isRedirect(err));
		assert.equal(calls[0].method, 'POST');
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { name: 'defectdrainer.com' });
		const none = installFetch(() => envelope(null));
		const bad = await orgActions.addDomain(orgEvent(`${settings}&/addDomain`, { name: '10.0.0.1' }));
		assert.equal(isActionFailure(bad), true);
		assert.equal(none.length, 0);
	});

	test('add domain held by another org shows the API text in the dialog', async () => {
		installFetch(() => envelope(null, 409, 'conflict', 'this domain is verified by another organization'));
		const result = await orgActions.addDomain(orgEvent(`${settings}&/addDomain`, { name: 'example.com' }));
		if (!isActionFailure(result)) assert.fail('expected a failure');
		assert.deepEqual(result.data, { scope: 'settings', field: 'addDomain', domainId: null, message: 'this domain is verified by another organization' });
	});

	test('verify: a missing TXT record keeps the API text on that domain', async () => {
		const text = 'TXT record not found yet: add _deploybot.defectdrainer.com = deploybot-verify=abc123';
		const calls = installFetch(() => envelope(null, 409, 'conflict', text));
		const result = await orgActions.verifyDomain(orgEvent(`${settings}&/verifyDomain`, { domain_id: DOMAIN_ID }));
		if (!isActionFailure(result)) assert.fail('expected a failure');
		assert.equal(calls[0].url.pathname, `/v1/orgs/${ORG.id}/domains/${DOMAIN_ID}/verify`);
		assert.deepEqual(result.data, { scope: 'settings', field: 'domain', domainId: DOMAIN_ID, message: text });
	});

	test('assignments are sent as pairs; a bad pattern or project is refused first', async () => {
		const calls = installFetch(() => envelope(null));
		await assert.rejects(
			async () =>
				orgActions.setDomainAssignments(
					orgEvent(`${settings}&/setDomainAssignments`, { domain_id: DOMAIN_ID, project_id: [PROJECT.id, PROJECT.id, PROJECT.id], pattern: ['@', '*', '@'] })
				),
			(err: unknown) => isRedirect(err)
		);
		assert.equal(calls[0].method, 'PUT');
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), {
			assignments: [
				{ project_id: PROJECT.id, pattern: '@' },
				{ project_id: PROJECT.id, pattern: '*' }
			]
		});
		const none = installFetch(() => envelope(null));
		const bad = await orgActions.setDomainAssignments(orgEvent(`${settings}&/setDomainAssignments`, { domain_id: DOMAIN_ID, project_id: [PROJECT.id], pattern: ['**'] }));
		assert.equal(isActionFailure(bad), true);
		const mismatched = await orgActions.setDomainAssignments(orgEvent(`${settings}&/setDomainAssignments`, { domain_id: DOMAIN_ID, project_id: [PROJECT.id], pattern: [] }));
		assert.equal(isActionFailure(mismatched), true);
		assert.equal(none.length, 0);
	});

	test('a name assignment is sent as a pattern, typed as the full name or relative (v1.52)', async () => {
		const calls = installFetch(() => envelope(null));
		await assert.rejects(
			async () =>
				orgActions.setDomainAssignments(
					orgEvent(`${settings}&/setDomainAssignments`, {
						domain_id: DOMAIN_ID,
						domain_name: 'defectdrainer.com',
						project_id: [PROJECT.id, PROJECT.id],
						pattern: ['marketingos.defectdrainer.com', '*.dev']
					})
				),
			(err: unknown) => isRedirect(err)
		);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), {
			assignments: [
				{ project_id: PROJECT.id, pattern: 'marketingos' },
				{ project_id: PROJECT.id, pattern: '*.dev' }
			]
		});
		const none = installFetch(() => envelope(null));
		const bad = await orgActions.setDomainAssignments(
			orgEvent(`${settings}&/setDomainAssignments`, { domain_id: DOMAIN_ID, domain_name: 'defectdrainer.com', project_id: [PROJECT.id], pattern: ['a..b'] })
		);
		assert.equal(isActionFailure(bad), true);
		assert.equal(none.length, 0);
	});

	test('delete sends a key with the DELETE, returns the plan, and confirm sends the same key', async () => {
		const plan = 'delete defectdrainer.com:\nstop https://defectdrainer.com/ (serve, project Lab)';
		const deleted = installFetch(() => envelope({ confirmation_id: CONFIRMATION, plan }, 202));
		const result = (await orgActions.deleteDomain(orgEvent(`${settings}&/deleteDomain`, { domain_id: DOMAIN_ID }))) as {
			domainPlan: { domainId: string; plan: string; idempotency_key: string };
		};
		assert.equal(result.domainPlan.domainId, DOMAIN_ID);
		assert.equal(result.domainPlan.plan, plan);
		assert.match(result.domainPlan.idempotency_key, /^[0-9a-f-]{36}$/);
		assert.equal(deleted[0].method, 'DELETE');
		const issuedKey = JSON.parse(deleted[0].body ?? '').idempotency_key;
		assert.equal(issuedKey, result.domainPlan.idempotency_key);

		// The fake API refuses a confirm whose key is not the one that issued the plan, as the real one does.
		const calls = installFetch((call) =>
			JSON.parse(call.body ?? '{}').idempotency_key === issuedKey
				? envelope({ name: 'defectdrainer.com', deleted: true, routes_removed: 1 })
				: envelope(null, 409, 'conflict', 'idempotency_key does not match the request that issued this confirmation')
		);
		await assert.rejects(
			async () =>
				orgActions.confirmDomainDelete(
					orgEvent(`${settings}&/confirmDomainDelete`, { domain_id: DOMAIN_ID, confirmation_id: CONFIRMATION, idempotency_key: result.domainPlan.idempotency_key })
				),
			(err: unknown) => isRedirect(err) && err.location === `/orgs/${ORG.id}?tab=settings`
		);
		assert.equal(calls[0].url.pathname, `/v1/confirmations/${CONFIRMATION}`);

		// A second Delete is a new request with a new key.
		const again = installFetch(() => envelope({ confirmation_id: CONFIRMATION, plan }, 202));
		await orgActions.deleteDomain(orgEvent(`${settings}&/deleteDomain`, { domain_id: DOMAIN_ID }));
		assert.notEqual(JSON.parse(again[0].body ?? '').idempotency_key, issuedKey);
	});

	test('edge on: the occupant refusal is shown as is, outside the page notice', async () => {
		const text = 'port 443 is used by daikou-caddy (project Daikou); stop it, wait until the host no longer reports that port (up to a minute), then try again';
		const calls = installFetch(() => envelope(null, 409, 'conflict', text));
		const event = fakeEvent({ url: `http://console.example/hosts/${HOST_ID}?/setEdge`, form: { enabled: '1' }, params: { id: HOST_ID }, user: USER, session: TOKEN }).event as never;
		const result = await hostActions.setEdge(event);
		if (!isActionFailure(result)) assert.fail('expected a failure');
		assert.deepEqual(result.data, { edgeMessage: text });
		assert.equal(calls[0].method, 'PUT');
		assert.equal(calls[0].url.pathname, `/v1/hosts/${HOST_ID}/edge`);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { enabled: true });
	});

	test('entry routes: read, plan and confirm', async () => {
		installFetch(() => envelope({ items: [RAW_ROUTE] }));
		const read = (await projectActions.catalogRoutes(projectEvent('catalogRoutes', { entry: 'serve' }))) as { routes: unknown[] };
		assert.deepEqual(read.routes, [RAW_ROUTE]);

		const plan = 'routes for serve: add https://defectdrainer.com/ → port 8788 on ip-10-0-30-14';
		const calls = installFetch(() => envelope({ confirmation_id: CONFIRMATION, plan, expires_at: '2026-11-17T00:10:00Z' }, 202));
		const routes = JSON.stringify([{ hostname: 'defectdrainer.com', path_prefix: '/', port: 8788, client_ip_from: 'cloudflare', no_store_paths: ['/evidence/'] }]);
		const saved = (await projectActions.catalogRoutesSave(projectEvent('catalogRoutesSave', { entry: 'serve', routes, idempotency_key: KEY }))) as {
			routesPlan: { plan: string };
		};
		assert.equal(saved.routesPlan.plan, plan);
		assert.equal(calls[0].method, 'PUT');
		assert.equal(calls[0].url.pathname, `/v1/projects/${PROJECT.id}/catalog/serve/routes`);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { routes: JSON.parse(routes), idempotency_key: KEY });

		// The confirm must carry the key of the PUT that issued the plan; the fake API refuses any other, as the real one does.
		const confirm = installFetch((call) =>
			JSON.parse(call.body ?? '{}').idempotency_key === KEY
				? envelope({ entry: 'serve', routes: [RAW_ROUTE] })
				: envelope(null, 409, 'conflict', 'idempotency_key does not match the request that issued this confirmation')
		);
		const done = (await projectActions.catalogRoutesConfirm(
			projectEvent('catalogRoutesConfirm', { entry: 'serve', confirmation_id: CONFIRMATION, idempotency_key: KEY })
		)) as { routesConfirmed: boolean };
		assert.equal(done.routesConfirmed, true);
		assert.equal(confirm[0].url.pathname, `/v1/confirmations/${CONFIRMATION}`);
		const other = await projectActions.catalogRoutesConfirm(
			projectEvent('catalogRoutesConfirm', { entry: 'serve', confirmation_id: CONFIRMATION, idempotency_key: 'another-key' })
		);
		assert.equal(isActionFailure(other), true);
	});

	test('the route editor confirms with the key of the review that issued the plan, and each review makes a new key', () => {
		const source = readFileSync(new URL('../src/routes/projects/[id]/EntryRoutes.svelte', import.meta.url), 'utf8');
		assert.doesNotMatch(source, /confirmKey/);
		const review = source.slice(source.indexOf('function review('), source.indexOf('function takePlan('));
		assert.match(review, /reviewKey = crypto\.randomUUID\(\);/);
		assert.match(review, /idempotency_key: reviewKey/);
		const confirmPlan = source.slice(source.indexOf('function confirmPlan('), source.indexOf('function takeConfirm('));
		assert.match(confirmPlan, /idempotency_key: reviewKey/);
		assert.doesNotMatch(confirmPlan, /randomUUID/);
	});

	test('entry routes: the API refusal text comes back; bad input never reaches the API', async () => {
		const text = 'port 8788 is not published; add a ports line for it (127.0.0.1 is enough)';
		installFetch(() => envelope(null, 400, 'bad_request', text));
		const refused = await projectActions.catalogRoutesSave(
			projectEvent('catalogRoutesSave', { entry: 'serve', routes: JSON.stringify([{ hostname: 'a.example.com', path_prefix: '/', port: 8788 }]), idempotency_key: KEY })
		);
		if (!isActionFailure(refused)) assert.fail('expected a failure');
		assert.equal((refused.data as unknown as { routesMessage: string }).routesMessage, text);

		const none = installFetch(() => envelope(null));
		for (const form of [
			{ entry: 'Bad Name', routes: '[]', idempotency_key: KEY },
			{ entry: 'serve', routes: '[]', idempotency_key: 'bad key' },
			{ entry: 'serve', routes: '[{"hostname":"nope","path_prefix":"/","port":80}]', idempotency_key: KEY }
		]) {
			assert.equal(isActionFailure(await projectActions.catalogRoutesSave(projectEvent('catalogRoutesSave', form))), true);
		}
		assert.equal(isActionFailure(await projectActions.catalogRoutesConfirm(projectEvent('catalogRoutesConfirm', { entry: 'serve', confirmation_id: 'x', idempotency_key: KEY }))), true);
		assert.equal(none.length, 0);
	});
});

describe('v1.35 renders', () => {
	test('a pending domain shows its TXT record, Verify and the assignment', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=settings`);
		const html = render(OrgDomainsSection, { props: { domains: [domain()], projects: [PROJECT] } }).body;
		assert.match(html, /_deploybot\.defectdrainer\.com/);
		assert.match(html, /deploybot-verify=abc123/);
		assert.match(html, /Copy value/);
		assert.match(html, /Verify/);
		assert.match(html, /Pending/);
		assert.match(html, /→ Lab/);
	});

	test('an assignment of one name is written out in the row (v1.52)', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=settings`);
		const html = render(OrgDomainsSection, {
			props: { domains: [domain({ assignments: [{ project_id: PROJECT.id, project_name: 'Lab', pattern: 'marketingos' }] })], projects: [PROJECT] }
		}).body;
		assert.match(html, /marketingos\.defectdrainer\.com/);
		assert.doesNotMatch(html, /\*\.defectdrainer\.com/);
	});

	test('a lapsed domain warns; a verified one hides the TXT record', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=settings`);
		const lapsed = render(OrgDomainsSection, { props: { domains: [domain({ status: 'lapsed' })], projects: [] } }).body;
		assert.match(lapsed, /no new route is accepted/);
		const verified = render(OrgDomainsSection, { props: { domains: [domain({ status: 'verified', verified_at: '2026-11-17T00:00:00Z' })], projects: [] } }).body;
		assert.doesNotMatch(verified, /deploybot-verify=/);
		assert.doesNotMatch(verified, />Verify</);
	});

	test('row buttons name their domain; a row refusal is a focusable notice', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=settings`);
		const html = render(OrgDomainsSection, { props: { domains: [domain()], projects: [], rowMessage: 'nope', rowMessageId: DOMAIN_ID } }).body;
		assert.match(html, /aria-label="Verify defectdrainer\.com"/);
		assert.match(html, /aria-label="Assign defectdrainer\.com"/);
		assert.match(html, /aria-label="Delete defectdrainer\.com"/);
		assert.match(html, new RegExp(`id="domain-error-${DOMAIN_ID}" tabindex="-1"`));
		assert.match(html, />nope</);
		// The plan is shown in an alert dialog only after Delete answered; nothing of it renders before.
		assert.doesNotMatch(html, /Confirm delete/);
	});

	const OUTSIDE_REFUSAL = 'port 443 is used by a process outside Deploybot; stop it, wait until the host no longer reports that port (up to a minute), then try again';

	test('edge not available: the runner.toml line with Copy, Turn on disabled', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const edge: HostEdge = { available: false, enabled: false, status: 'off', detail: null, config_hash: null, applied_at: null, routes: 0 };
		const html = render(HostEdgeSection, { props: { hostName: 'pi3', edge, actionMessage: OUTSIDE_REFUSAL } }).body;
		assert.match(html, /edge = true/);
		assert.match(html, /Copy runner\.toml line/);
		assert.match(html, /Turn on edge/);
		assert.match(html, /disabled/);
		// The whole refusal shows, the what-to-do tail included (spec-release-v0239 F3).
		assert.ok(html.includes(OUTSIDE_REFUSAL));
		// F2: the card says where certificates come from and that a proxy in front is fine.
		assert.match(html, /Certificates come from Let(&#39;|')s Encrypt, so ports 80 and 443 must reach this host from the internet, directly or through a proxy such as Cloudflare\./);
		assert.doesNotMatch(html, /The host must be reachable on 80 and 443/);
	});

	test('edge on: status, routes count and Turn off', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const edge: HostEdge = { available: true, enabled: true, status: 'applied', detail: null, config_hash: 'f'.repeat(64), applied_at: '2026-11-17T00:00:00Z', routes: 2 };
		const html = render(HostEdgeSection, { props: { hostName: 'pi3', edge } }).body;
		assert.match(html, /Applied/);
		assert.match(html, /2 routes/);
		assert.match(html, /Turn off edge/);
		assert.doesNotMatch(html, /edge = true/);
		// Turning off stops every public address on the host: the trigger is destructive.
		assert.match(html, /<button[^>]*class="[^"]*destructive[^"]*"[^>]*>[\s\S]*?Turn off edge/);
	});

	test('public addresses render for admins only', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
		const data = page.data as Record<string, unknown>;
		data.canAdmin = true;
		const html = render(EntryRoutes, { props: { entry: imageEntry([{ host: 18788, container: 8788 }]) } }).body;
		assert.match(html, /Public addresses/);
		assert.match(html, /Edit public addresses/);
		assert.match(html, /catalogRoutes/);
		data.canAdmin = false;
		const viewer = render(EntryRoutes, { props: { entry: imageEntry([]) } }).body;
		assert.doesNotMatch(viewer, /Public addresses/);
		delete data.canAdmin;
	});
});
