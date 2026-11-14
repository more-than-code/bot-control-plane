// Org console slice review, batch 1 (umbrella tasks/notes/review-org-console-batch1.md, Main Dev 2026-11-14): items 22 and 27–33.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isHttpError, isRedirect } from '@sveltejs/kit';
import { createRawSnippet } from 'svelte';
import { render } from 'svelte/server';
import { isCatalogHost } from '../src/lib/catalog.ts';
import { orgTabOf } from '../src/lib/org-view.ts';
import { uiOrgRole } from '../src/lib/server/roles.ts';
import { parseHost, parseJoinGrant } from '../src/lib/server/shape.ts';
import RootLayout from '../src/routes/+layout.svelte';
import { actions as hostActions, load as hostLoad } from '../src/routes/hosts/[id]/+page.server.ts';
import OrgPage from '../src/routes/orgs/[id]/+page.svelte';
import { actions as orgActions, load as orgLoad } from '../src/routes/orgs/[id]/+page.server.ts';
import OrgHostsSection from '../src/routes/orgs/[id]/OrgHostsSection.svelte';
import OrgMembersSection from '../src/routes/orgs/[id]/OrgMembersSection.svelte';
import OrgSettingsSection from '../src/routes/orgs/[id]/OrgSettingsSection.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import HostsSection from '../src/routes/projects/[id]/HostsSection.svelte';
import type { Host, OrgMember } from '../src/lib/api-types.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER, type FetchCall } from './harness.ts';
import { setPageUrl, setRouteId } from './stubs/app-state.ts';

const HOST_ID = '00000000-0000-4000-8000-0000000000c1';
const OTHER_ID = '00000000-0000-4000-8000-0000000000c2';
const MEMBER_ID = '00000000-0000-4000-8000-0000000000c3';
const RAW_ORG = { id: ORG.id, name: ORG.name, slug: ORG.slug, role: 'owner', created_at: ORG.created_at };

const RAW_HOST = {
	id: HOST_ID,
	org_id: ORG.id,
	name: 'daikou',
	node_type: 'vm',
	os: 'linux',
	arch: 'amd64',
	always_on: true,
	agent_version: '0.2.8',
	status: 'online',
	last_seen_at: '2026-11-14T00:00:00Z',
	enrolled_at: '2026-11-01T00:00:00Z',
	revoked_at: null,
	access_level: 'deploy',
	access: null,
	programs: null
};

const HOST = parseHost(RAW_HOST) as Host;
const ACCESS_ROW = { project_id: PROJECT.id, project_name: 'Lab', level: 'deploy', granted_by: USER.id, granted_at: '2026-11-14T00:00:00Z' };

function pageData<T>(value: T): Exclude<T, void | undefined | null> {
	if (value == null) assert.fail('missing page data');
	return value as Exclude<T, void | undefined | null>;
}

function labels(html: string): string[] {
	return [...html.matchAll(/aria-label="([^"]*)"/g)].map((match) => match[1].replaceAll('&#39;', "'"));
}

function orgEvent(url: string, form?: Record<string, string>, orgs = [ORG], params: Record<string, string> = { id: ORG.id }) {
	return fakeEvent({ url, session: TOKEN, user: USER, projects: [PROJECT], orgs, params, form }).event as never;
}

function hostEvent(url: string, orgs = [ORG], form?: Record<string, string>) {
	return fakeEvent({ url, session: TOKEN, user: USER, projects: [PROJECT], orgs, params: { id: HOST_ID }, form }).event as never;
}

function metricsOk(call: FetchCall) {
	return call.url.pathname.endsWith('/metrics') ? envelope(null, 503, 'unavailable', 'down') : null;
}

describe('roles and parsing (items 22, 28, 33)', () => {
	test('uiOrgRole takes the lower of the session and payload roles', () => {
		assert.equal(uiOrgRole('owner', 'member'), 'member');
		assert.equal(uiOrgRole('member', 'owner'), 'member');
		assert.equal(uiOrgRole('admin', 'admin'), 'admin');
		assert.equal(uiOrgRole(null, 'admin'), 'admin');
		assert.equal(uiOrgRole('admin', null), 'admin');
		assert.equal(uiOrgRole(null, null), null);
		assert.equal(orgTabOf('tokens', false), 'projects');
		assert.equal(orgTabOf('settings', false), 'projects');
	});

	test('parseJoinGrant refuses a non-object or a bad project id', () => {
		assert.equal(parseJoinGrant('deploy'), undefined);
		assert.equal(parseJoinGrant(['x']), undefined);
		assert.equal(parseJoinGrant({ project_id: 'nope', level: 'deploy' }), undefined);
		assert.equal(parseJoinGrant(undefined), null);
	});

	test('parseHost access: not an array or over the cap fails the host; a bad row is dropped', () => {
		assert.equal(parseHost({ ...RAW_HOST, access: 'all' }), null);
		const row = { project_id: PROJECT.id, project_name: 'Lab', level: 'deploy' };
		assert.equal(parseHost({ ...RAW_HOST, access: Array.from({ length: 501 }, () => row) }), null);
		assert.deepEqual(parseHost({ ...RAW_HOST, access: [row, { ...row, level: 'owner' }] })?.access, [row]);
	});

	test('parseHost reads only access_level; the removed v1.24 mirrors are ignored (api-v1 v1.26)', () => {
		const { access_level: _level, ...legacy } = RAW_HOST;
		assert.equal(parseHost({ ...legacy, relation: 'shared', share_level: 'deploy' })?.access_level, null);
		assert.equal(parseHost({ ...legacy, relation: 'owner', share_level: null })?.access_level, null);
		assert.equal(parseHost({ ...RAW_HOST, access_level: 'metrics', relation: 'owner', share_level: 'deploy' })?.access_level, 'metrics');
		assert.equal(isCatalogHost({ access_level: 'metrics' }), false);
		assert.equal(isCatalogHost({ access_level: null }), false);
		assert.equal(isCatalogHost({ access_level: 'deploy' }), true);
	});
});

describe('loads (items 27, 28)', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('host page: an org admin reads access and org projects; a failure keeps the page; omitted rows are counted', async () => {
		const handler = (access: () => Response) => (call: FetchCall) =>
			metricsOk(call) ??
			(call.url.pathname === `/v1/hosts/${HOST_ID}`
				? envelope(RAW_HOST)
				: call.url.pathname === `/v1/hosts/${HOST_ID}/access`
					? access()
					: call.url.pathname === `/v1/orgs/${ORG.id}/projects`
						? envelope({ items: [PROJECT], next_cursor: null })
						: envelope(null, 500, 'error', 'unexpected'));
		const calls = installFetch(handler(() => envelope({ items: [ACCESS_ROW], next_cursor: null })));
		const admin = pageData(await hostLoad(hostEvent(`http://console.example/hosts/${HOST_ID}`)));
		assert.equal(admin.canManageAccess, true);
		assert.equal(admin.accessLoaded, true);
		assert.equal(admin.access.length, 1);
		assert.equal(admin.accessMessage, null);
		assert.ok(calls.some((call) => call.url.pathname === `/v1/orgs/${ORG.id}/projects`));

		installFetch(handler(() => envelope(null, 503, 'unavailable', 'access is down')));
		const failed = pageData(await hostLoad(hostEvent(`http://console.example/hosts/${HOST_ID}`)));
		assert.equal(failed.host?.id, HOST_ID);
		assert.equal(failed.message, null);
		assert.equal(failed.canManageAccess, true);
		assert.equal(failed.accessLoaded, false);
		assert.equal(failed.accessMessage, 'access is down');

		installFetch(handler(() => envelope({ items: [ACCESS_ROW, { ...ACCESS_ROW, level: 'owner' }], next_cursor: null })));
		const omitted = pageData(await hostLoad(hostEvent(`http://console.example/hosts/${HOST_ID}`)));
		assert.equal(omitted.accessMessage, '1 access row could not be read.');

		// A project admin who is not in the host's org: no org and no access reads.
		const none = installFetch(handler(() => assert.fail('no access read')));
		const outsider = pageData(await hostLoad(hostEvent(`http://console.example/hosts/${HOST_ID}`, [])));
		assert.equal(outsider.org, null);
		assert.equal(outsider.canManageAccess, false);
		assert.equal(none.some((call) => call.url.pathname.endsWith('/access') || call.url.pathname.includes('/orgs/')), false);
	});

	test('org page: one failed list keeps the rest; an org read failure is the shell; a bad id is 404; a member reads little', async () => {
		installFetch((call) => {
			if (call.url.pathname === `/v1/orgs/${ORG.id}`) return envelope(RAW_ORG);
			if (call.url.pathname === `/v1/orgs/${ORG.id}/hosts`) return envelope(null, 503, 'unavailable', 'hosts are down');
			return envelope({ items: [PROJECT], next_cursor: null });
		});
		const partial = pageData(await orgLoad(orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`)));
		assert.equal(partial.hosts.message, 'hosts are down');
		assert.equal(partial.projects.items.length, 1);
		assert.equal(partial.projects.message, null);

		installFetch(() => envelope(null, 503, 'unavailable', 'org is down'));
		const shell = pageData(await orgLoad(orgEvent(`http://console.example/orgs/${ORG.id}`)));
		assert.equal(shell.org, null);
		assert.equal(shell.message, 'org is down');

		await assert.rejects(
			async () => orgLoad(orgEvent('http://console.example/orgs/nope', undefined, [ORG], { id: 'nope' })),
			(err: unknown) => isHttpError(err) && err.status === 404
		);

		const calls = installFetch((call) =>
			call.url.pathname === `/v1/orgs/${ORG.id}` ? envelope({ ...RAW_ORG, role: 'member' }) : envelope({ items: [], next_cursor: null })
		);
		await orgLoad(orgEvent(`http://console.example/orgs/${ORG.id}?tab=projects`, undefined, [{ ...ORG, role: 'member' }]));
		const paths = calls.map((call) => call.url.pathname);
		assert.deepEqual(paths.sort(), [`/v1/orgs/${ORG.id}`, `/v1/orgs/${ORG.id}/projects`]);
	});
});

describe('actions (items 29, 31, 32)', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('bad input is refused before any API call', async () => {
		const org = `http://console.example/orgs/${ORG.id}`;
		const cases: Array<[string, () => unknown]> = [
			['setAccess host', () => orgActions.setAccess(orgEvent(org, { host_id: 'x', project_id: PROJECT.id, level: 'deploy' }))],
			['setAccess project', () => orgActions.setAccess(orgEvent(org, { host_id: HOST_ID, project_id: 'x', level: 'deploy' }))],
			['removeAccess host', () => orgActions.removeAccess(orgEvent(org, { host_id: 'x', project_id: PROJECT.id }))],
			['removeAccess project', () => orgActions.removeAccess(orgEvent(org, { host_id: HOST_ID, project_id: 'x' }))],
			['createToken grant', () => orgActions.createToken(orgEvent(org, { grant_project_id: 'x', grant_level: 'deploy' }))],
			['changeRole user', () => orgActions.changeRole(orgEvent(org, { user_id: 'x', role: 'admin' }))],
			['changeRole role', () => orgActions.changeRole(orgEvent(org, { user_id: MEMBER_ID, role: 'owner' }))],
			['removeMember user', () => orgActions.removeMember(orgEvent(org, { user_id: 'x' }))],
			['transfer user', () => orgActions.transfer(orgEvent(org, { user_id: 'x' }))],
			['revokeToken', () => orgActions.revokeToken(orgEvent(org, { token_id: 'x' }))],
			['revokeCredential', () => orgActions.revokeCredential(orgEvent(org, { host_id: 'x' }))],
			['addMember role', () => orgActions.addMember(orgEvent(org, { email: 'a@example.com', role: 'owner' }))],
			['addMember email', () => orgActions.addMember(orgEvent(org, { email: 'not-an-email', role: 'member' }))],
			['rename empty', () => orgActions.rename(orgEvent(org, { name: '   ' }))],
			[
				'leaveAccess other project',
				() =>
					projectActions.leaveAccess(
						fakeEvent({
							url: `http://console.example/projects/${PROJECT.id}?tab=hosts`,
							session: TOKEN,
							user: USER,
							projects: [PROJECT],
							params: { id: PROJECT.id },
							form: { host_id: HOST_ID, project_id: OTHER_ID }
						}).event as never
					)
			]
		];
		for (const [name, run] of cases) {
			const calls = installFetch(() => envelope(null, 500, 'error', 'should not be called'));
			const result = await run();
			assert.equal(isActionFailure(result), true, name);
			assert.equal(calls.length, 0, name);
		}
	});

	test('409 paths: a second non-conflict failure clears the conflict; host page and Leave reassign', async () => {
		installFetch(() => envelope(null, 500, 'error', 'boom'));
		const second = await orgActions.removeAccess(orgEvent(`http://console.example/orgs/${ORG.id}`, { host_id: HOST_ID, project_id: PROJECT.id, reassign: '1' }));
		if (!isActionFailure(second)) assert.fail('expected a failure');
		assert.equal((second.data as unknown as { accessConflict: unknown }).accessConflict, null);
		assert.equal((second.data as unknown as { message: string }).message, 'boom');

		installFetch(() => envelope(null, 409, 'conflict', 'services remain'));
		const host = await hostActions.removeAccess(hostEvent(`http://console.example/hosts/${HOST_ID}`, [ORG], { host_id: HOST_ID, project_id: PROJECT.id }));
		if (!isActionFailure(host)) assert.fail('expected a conflict');
		assert.deepEqual((host.data as unknown as { accessConflict: unknown }).accessConflict, { hostId: HOST_ID, projectId: PROJECT.id });

		const leave = installFetch(() => envelope(null));
		await assert.rejects(
			async () =>
				projectActions.leaveAccess(
					fakeEvent({
						url: `http://console.example/projects/${PROJECT.id}?tab=hosts`,
						session: TOKEN,
						user: USER,
						projects: [PROJECT],
						params: { id: PROJECT.id },
						form: { host_id: HOST_ID, project_id: PROJECT.id, reassign: '1' }
					}).event as never
				),
			(err: unknown) => isRedirect(err)
		);
		assert.equal(leave[0]?.url.search, '?reassign=org');
	});

	test('success paths send the right request and redirect', async () => {
		const org = `http://console.example/orgs/${ORG.id}?tab=members`;
		const redirectTo = (location: string) => (err: unknown) => isRedirect(err) && err.status === 303 && err.location === location;

		const self = installFetch(() => envelope(null));
		await assert.rejects(async () => orgActions.removeMember(orgEvent(org, { user_id: USER.id })), redirectTo('/projects'));
		assert.equal(self[0]?.method, 'DELETE');

		const role = installFetch(() => envelope(null));
		await assert.rejects(async () => orgActions.changeRole(orgEvent(org, { user_id: MEMBER_ID, role: 'admin' })), redirectTo(`/orgs/${ORG.id}?tab=members`));
		assert.equal(role[0]?.method, 'PATCH');
		assert.equal(role[0]?.body, JSON.stringify({ role: 'admin' }));

		const transfer = installFetch(() => envelope(null));
		await assert.rejects(async () => orgActions.transfer(orgEvent(org, { user_id: MEMBER_ID })), redirectTo(`/orgs/${ORG.id}?tab=members`));
		assert.equal(transfer[0]?.url.pathname, `/v1/orgs/${ORG.id}/transfer-ownership`);
		assert.equal(transfer[0]?.body, JSON.stringify({ user_id: MEMBER_ID }));

		const token = installFetch(() => envelope(null));
		await assert.rejects(
			async () => orgActions.revokeToken(orgEvent(`http://console.example/orgs/${ORG.id}?tab=tokens`, { token_id: MEMBER_ID })),
			redirectTo(`/orgs/${ORG.id}?tab=tokens`)
		);
		assert.equal(token[0]?.url.pathname, `/v1/orgs/${ORG.id}/join-tokens/${MEMBER_ID}`);

		const rename = installFetch(() => envelope(null));
		await assert.rejects(
			async () => orgActions.rename(orgEvent(`http://console.example/orgs/${ORG.id}?tab=settings`, { name: 'Lab Org' })),
			redirectTo(`/orgs/${ORG.id}?tab=settings`)
		);
		assert.equal(rename[0]?.body, JSON.stringify({ name: 'Lab Org' }));

		const project = installFetch(() => envelope({ ...PROJECT }));
		await assert.rejects(async () => orgActions.createProject(orgEvent(`http://console.example/orgs/${ORG.id}`, { name: 'Lab' })), redirectTo(`/projects/${PROJECT.id}`));
		assert.equal(project[0]?.url.pathname, `/v1/orgs/${ORG.id}/projects`);
	});
});

describe('rendered visibility (item 30)', () => {
	test('project Hosts: Leave for a project admin, Revoke for an org admin of that org, neither for a viewer', () => {
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=hosts`);
		const base = { hosts: [HOST], omitted: 0, truncated: false, projectId: PROJECT.id, orgId: ORG.id, message: null, hostsMessage: null };
		const admin = labels(render(HostsSection, { props: { ...base, canAdmin: true, canOrgAdmin: false } }).body);
		assert.equal(admin.includes('Leave daikou'), true);
		assert.equal(admin.includes('Revoke credential daikou'), false);
		const orgAdmin = labels(render(HostsSection, { props: { ...base, canAdmin: true, canOrgAdmin: true } }).body);
		assert.equal(orgAdmin.includes('Revoke credential daikou'), true);
		const otherOrg = labels(render(HostsSection, { props: { ...base, orgId: OTHER_ID, canAdmin: true, canOrgAdmin: true } }).body);
		assert.equal(otherOrg.includes('Revoke credential daikou'), false);
		const revoked = labels(render(HostsSection, { props: { ...base, hosts: [{ ...HOST, status: 'revoked' as const }], canAdmin: true, canOrgAdmin: true } }).body);
		assert.equal(revoked.includes('Revoke credential daikou'), false);
		const viewer = labels(render(HostsSection, { props: { ...base, canAdmin: false, canOrgAdmin: false } }).body);
		assert.equal(viewer.some((label) => label.includes('daikou')), false);

		const conflict = render(HostsSection, {
			props: { ...base, canAdmin: true, canOrgAdmin: false, accessFailure: { scope: 'hosts', message: 'x', accessHostId: HOST_ID, accessConflict: { hostId: HOST_ID, projectId: PROJECT.id } } }
		}).body;
		assert.equal(conflict.includes('Move services and leave'), true);
		const source = readFileSync(new URL('../src/routes/projects/[id]/HostsSection.svelte', import.meta.url), 'utf8');
		assert.match(source, /label="Move services and leave"[\s\S]*?\{ name: 'reassign', value: '1' \}/);
	});

	test('org Hosts: Manage access and Revoke credential per host', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=hosts`);
		const html = labels(render(OrgHostsSection, { props: { hosts: [HOST], omitted: 0, truncated: false, projects: [], message: null } }).body);
		assert.equal(html.includes('Manage access daikou'), true);
		assert.equal(html.includes('Revoke credential daikou'), true);
	});

	test('org tabs per role, Delete for the owner only, self-leave and owner rows in Members', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}`);
		const list = { items: [], omitted: 0, truncated: false, message: null };
		const data = (canAdmin: boolean) => ({
			org: { ...ORG, role: canAdmin ? 'admin' : 'member' },
			message: null,
			tab: 'projects',
			canAdmin,
			canOwner: false,
			userId: USER.id,
			projects: list,
			hosts: list,
			members: list,
			tokens: list
		});
		const member = render(OrgPage, { props: { data: data(false) as never, form: null } }).body;
		assert.equal(member.includes('?tab=hosts'), false);
		assert.equal(member.includes('?tab=settings'), false);
		assert.equal(member.includes('?tab=members'), true);
		const admin = render(OrgPage, { props: { data: data(true) as never, form: null } }).body;
		for (const tab of ['hosts', 'tokens', 'settings']) assert.equal(admin.includes(`?tab=${tab}`), true, tab);

		const settings = (canOwner: boolean) => render(OrgSettingsSection, { props: { org: ORG, canOwner, message: null } }).body;
		assert.equal(settings(true).includes('Delete organization'), true);
		assert.equal(settings(false).includes('Delete organization'), false);

		const members: OrgMember[] = [
			{ user_id: MEMBER_ID, email: 'owner@example.com', name: 'Olu', role: 'owner', added_at: ORG.created_at },
			{ user_id: USER.id, email: USER.email, name: USER.name, role: 'member', added_at: ORG.created_at }
		];
		const rows = labels(
			render(OrgMembersSection, { props: { members, omitted: 0, truncated: false, canAdmin: false, canOwner: false, currentUserId: USER.id, message: null } }).body
		);
		assert.equal(rows.includes('Leave organization'), true);
		assert.equal(rows.some((label) => label.includes('owner@example.com')), false);
	});

	test('the header org switcher names the org and starts with its purpose', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}`);
		setRouteId('/orgs/[id]');
		const children = createRawSnippet(() => ({ render: () => '<p>child</p>' }));
		const user = { id: USER.id, name: USER.name, avatarUrl: null };
		const html = render(RootLayout, {
			props: { children, data: { user, orgs: [{ id: ORG.id, name: ORG.name }], theme: 'system' as const, timeZone: 'UTC' } }
		}).body;
		assert.match(html.replace(/<!--[\s\S]*?-->/g, ''), /<span class="sr-only">Switch organization:<\/span>\s*<span class="min-w-0 truncate">Home Lab<\/span>/);
		// The org links are in the menu content, which mounts only when it opens.
	});
});
