// Organizations in the console (umbrella docs/spec-organizations.md rev 1 §4.3, approved by Joe 2026-11-14; api-v1 v1.24).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import { accessSummary, grantableProjects, missingFromAllowlist, runnerNote } from '../src/lib/host-access.ts';
import { orgTabOf } from '../src/lib/org-view.ts';
import { isOrgAdmin, orgRoleOf } from '../src/lib/server/roles.ts';
import { parseHostAccess, parseJoinToken, parseMe, parseOrg, parseOrgMember, parseProject } from '../src/lib/server/shape.ts';
import HostAccessEditor from '../src/lib/components/HostAccessEditor.svelte';
import { actions as orgActions, load as orgLoad } from '../src/routes/orgs/[id]/+page.server.ts';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { actions as projectsActions } from '../src/routes/projects/+page.server.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const HOST_ID = '00000000-0000-4000-8000-0000000000b1';
const OTHER_PROJECT = '00000000-0000-4000-8000-0000000000b2';
const TOKEN_ID = '00000000-0000-4000-8000-0000000000b3';
const RAW_ORG = { id: ORG.id, name: ORG.name, slug: ORG.slug, role: 'owner', created_at: ORG.created_at };

function pageData<T>(value: T): Exclude<T, void | undefined | null> {
	if (value == null) assert.fail('missing page data');
	return value as Exclude<T, void | undefined | null>;
}

function orgEvent(url: string, form?: Record<string, string>, orgs = [ORG]) {
	return fakeEvent({ url, session: TOKEN, user: USER, projects: [PROJECT], orgs, params: { id: ORG.id }, form }).event as never;
}

describe('org parsing', () => {
	test('parseMe drops one bad project or org instead of failing the session', () => {
		const good = { ...PROJECT };
		const me = parseMe({
			user: USER,
			projects: [good, { ...good, id: OTHER_PROJECT, role: 'superuser' }],
			orgs: [RAW_ORG, { ...RAW_ORG, role: 'boss' }]
		});
		assert.deepEqual(me?.projects.map((project) => project.id), [PROJECT.id]);
		assert.deepEqual(me?.orgs.map((org) => org.id), [ORG.id]);
		// An API before v1.24 sends no orgs.
		assert.deepEqual(parseMe({ user: USER, projects: [good] })?.orgs, []);
		assert.equal(parseMe({ user: USER, projects: [good], orgs: 'x' }), null);
	});

	test('a project carries org_id and via; a bad value drops it', () => {
		const { org_id: _org, via: _via, ...older } = PROJECT;
		assert.deepEqual([parseProject(older)?.org_id, parseProject(older)?.via], [null, 'member']);
		assert.equal(parseProject({ ...PROJECT, via: 'org' })?.via, 'org');
		assert.equal(parseProject({ ...PROJECT, via: 'share' }), null);
		assert.equal(parseProject({ ...PROJECT, org_id: 'not-a-uuid' }), null);
	});

	test('orgs, members, access rows and token grants parse strictly', () => {
		assert.equal(parseOrg(RAW_ORG)?.role, 'owner');
		assert.equal(parseOrg({ ...RAW_ORG, slug: 'Bad Slug' }), null);
		assert.equal(parseOrgMember({ user_id: USER.id, email: USER.email, name: 'Ada', role: 'member', added_at: ORG.created_at })?.role, 'member');
		assert.equal(parseOrgMember({ user_id: USER.id, email: USER.email, name: 'Ada', role: 'viewer', added_at: ORG.created_at }), null);
		const access = { project_id: PROJECT.id, project_name: 'Lab', level: 'deploy', granted_by: null, granted_at: null };
		assert.equal(parseHostAccess(access)?.level, 'deploy');
		assert.equal(parseHostAccess({ ...access, level: 'owner' }), null);
		const token = {
			id: TOKEN_ID,
			host_name_hint: null,
			created_by: USER.id,
			created_at: ORG.created_at,
			expires_at: null,
			used_at: null,
			used_by_host_id: null,
			revoked_at: null
		};
		assert.equal(parseJoinToken(token)?.grant, null);
		assert.deepEqual(parseJoinToken({ ...token, grant: { project_id: PROJECT.id, level: 'metrics' } })?.grant, { project_id: PROJECT.id, level: 'metrics' });
		assert.equal(parseJoinToken({ ...token, grant: { project_id: PROJECT.id, level: 'all' } }), null);
	});

	test('org roles: owner and admin manage, a member does not', () => {
		assert.equal(isOrgAdmin([ORG], ORG.id), true);
		assert.equal(isOrgAdmin([{ ...ORG, role: 'admin' }], ORG.id), true);
		assert.equal(isOrgAdmin([{ ...ORG, role: 'member' }], ORG.id), false);
		assert.equal(isOrgAdmin([ORG], null), false);
		assert.equal(orgRoleOf([ORG], OTHER_PROJECT), null);
		assert.equal(orgTabOf('hosts', false), 'projects');
		assert.equal(orgTabOf('hosts', true), 'hosts');
		assert.equal(orgTabOf('members', false), 'members');
		assert.equal(orgTabOf('audit', true), 'projects');
	});
});

describe('host access view', () => {
	test('grant choices, the runner.toml note and the summary', () => {
		const projects = [
			{ id: PROJECT.id, name: 'Lab', slug: 'lab' },
			{ id: OTHER_PROJECT, name: 'Defect Drainer', slug: 'defect-drainer' }
		];
		assert.deepEqual(grantableProjects(projects, [{ project_id: PROJECT.id }]).map((project) => project.slug), ['defect-drainer']);
		assert.equal(runnerNote('defect-drainer'), 'This host also needs defect-drainer in its runner.toml projects.');
		assert.equal(missingFromAllowlist({ projects: ['lab'] }, 'deploy', 'defect-drainer'), true);
		assert.equal(missingFromAllowlist({ projects: ['lab'] }, 'metrics', 'defect-drainer'), false);
		assert.equal(missingFromAllowlist({ projects: null }, 'deploy', 'defect-drainer'), false);
		assert.equal(accessSummary(null), 'n/a');
		assert.equal(accessSummary([]), 'No projects');
		assert.equal(
			accessSummary([
				{ project_id: PROJECT.id, project_name: 'Lab', level: 'metrics' },
				{ project_id: OTHER_PROJECT, project_name: 'Daikou', level: 'deploy' }
			]),
			'Daikou (deploy), Lab (metrics)'
		);
	});

	test('a 409 on remove offers the reassign confirm, and only that confirm posts reassign', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const props = {
			hostId: HOST_ID,
			hostName: 'daikou',
			rows: [{ project_id: PROJECT.id, project_name: 'Lab', level: 'deploy' as const }],
			projects: [{ id: PROJECT.id, name: 'Lab', slug: 'lab' }],
			setAction: '?/setAccess',
			removeAction: '?/removeAccess'
		};
		const plain = render(HostAccessEditor, { props }).body;
		assert.equal(plain.includes('still has services'), false);
		assert.equal(plain.includes('name="reassign"'), false);
		const conflict = render(HostAccessEditor, { props: { ...props, message: 'conflict', conflictProjectId: PROJECT.id } }).body;
		assert.match(conflict, /Lab still has services on daikou\./);
		assert.equal(conflict.includes('Remove and move services'), true);
	});
});

describe('org routes', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('a member sees Projects and Members only; an admin Hosts tab reads org hosts, projects and tokens', async () => {
		installFetch((call) => {
			if (call.url.pathname === `/v1/orgs/${ORG.id}`) return envelope({ ...RAW_ORG, role: 'member' });
			return envelope({ items: [], next_cursor: null });
		});
		const member = pageData(await orgLoad(orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`, undefined, [{ ...ORG, role: 'member' }])));
		assert.equal(member.tab, 'projects');
		assert.equal(member.canAdmin, false);

		const calls = installFetch((call) => {
			if (call.url.pathname === `/v1/orgs/${ORG.id}`) return envelope(RAW_ORG);
			return envelope({ items: [], next_cursor: null });
		});
		const admin = pageData(await orgLoad(orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`)));
		assert.equal(admin.tab, 'hosts');
		assert.deepEqual(
			calls.map((call) => call.url.pathname).filter((path) => path !== `/v1/orgs/${ORG.id}`).sort(),
			[`/v1/orgs/${ORG.id}/hosts`, `/v1/orgs/${ORG.id}/join-tokens`, `/v1/orgs/${ORG.id}/projects`]
		);
	});

	test('an org join token carries the grant only when a project is chosen', async () => {
		const join = { id: TOKEN_ID, token: 'join-plain-0123456789abcdef' };
		const calls = installFetch((call) => {
			if (call.method === 'POST') return envelope(join);
			return envelope({ items: [{ ...PROJECT }], next_cursor: null });
		});
		const granted = await orgActions.createToken(
			orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`, { host_name_hint: '', expires_in_sec: '', grant_project_id: PROJECT.id, grant_level: 'metrics' })
		);
		assert.equal(calls[0]?.url.pathname, `/v1/orgs/${ORG.id}/join-tokens`);
		assert.equal(calls[0]?.body, JSON.stringify({ grant: { project_id: PROJECT.id, level: 'metrics' } }));
		if (!granted || isActionFailure(granted)) assert.fail('expected a token');
		assert.equal(granted.createdSlug, PROJECT.slug);

		const plain = installFetch(() => envelope(join));
		await orgActions.createToken(orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`, { host_name_hint: 'pi9', expires_in_sec: '' }));
		assert.equal(plain[0]?.body, JSON.stringify({ host_name_hint: 'pi9' }));

		const none = installFetch(() => envelope(null, 500, 'error', 'unexpected'));
		const bad = await orgActions.createToken(
			orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts`, { grant_project_id: PROJECT.id, grant_level: 'owner' })
		);
		assert.equal(isActionFailure(bad), true);
		assert.equal(none.length, 0);
	});

	test('remove access: 409 returns the conflict; reassign sends ?reassign=org', async () => {
		installFetch(() => envelope(null, 409, 'conflict', 'project still has services on this host'));
		const conflict = await orgActions.removeAccess(
			orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts&/removeAccess`, { host_id: HOST_ID, project_id: PROJECT.id })
		);
		if (!isActionFailure(conflict)) assert.fail('expected a failure');
		assert.equal(conflict.status, 409);
		assert.deepEqual((conflict.data as { accessConflict?: unknown } | undefined)?.accessConflict, { hostId: HOST_ID, projectId: PROJECT.id });

		const calls = installFetch(() => envelope(null));
		await assert.rejects(
			async () =>
				orgActions.removeAccess(
					orgEvent(`http://console.example/orgs/${ORG.id}?tab=hosts&/removeAccess`, { host_id: HOST_ID, project_id: PROJECT.id, reassign: '1' })
				),
			(err: unknown) => isRedirect(err) && err.location === `/orgs/${ORG.id}?tab=hosts`
		);
		assert.equal(calls[0]?.method, 'DELETE');
		assert.equal(calls[0]?.url.pathname + calls[0]?.url.search, `/v1/hosts/${HOST_ID}/access/${PROJECT.id}?reassign=org`);
	});

	test('a project admin leaves a host with DELETE on its own access row', async () => {
		const calls = installFetch(() => envelope(null));
		await assert.rejects(
			async () =>
				projectActions.leaveAccess(
					fakeEvent({
						url: `http://console.example/projects/${PROJECT.id}?tab=hosts&/leaveAccess`,
						session: TOKEN,
						user: USER,
						projects: [PROJECT],
						params: { id: PROJECT.id },
						form: { host_id: HOST_ID, project_id: PROJECT.id }
					}).event as never
				),
			(err: unknown) => isRedirect(err) && err.status === 303
		);
		assert.equal(calls[0]?.url.pathname, `/v1/hosts/${HOST_ID}/access/${PROJECT.id}`);
		assert.equal(calls[0]?.url.search, '');
	});

	test('delete org needs the typed name; a new project is created in the chosen org', async () => {
		const calls = installFetch((call) => (call.method === 'DELETE' ? envelope(null) : envelope(RAW_ORG)));
		const wrong = await orgActions.deleteOrg(orgEvent(`http://console.example/orgs/${ORG.id}?tab=settings`, { confirm: 'nope' }));
		assert.equal(isActionFailure(wrong), true);
		assert.equal(calls.some((call) => call.method === 'DELETE'), false);

		const created = installFetch(() => envelope({ ...PROJECT }));
		await assert.rejects(
			async () =>
				projectsActions.create(
					fakeEvent({
						url: 'http://console.example/projects?/create',
						session: TOKEN,
						user: USER,
						orgs: [ORG],
						form: { name: 'Lab', org_id: ORG.id }
					}).event as never
				),
			(err: unknown) => isRedirect(err) && err.location === `/projects/${PROJECT.id}`
		);
		assert.equal(created[0]?.url.pathname, `/v1/orgs/${ORG.id}/projects`);

		// Two orgs and none chosen: refused before any call.
		const skipped = installFetch(() => envelope(null, 500, 'error', 'unexpected'));
		const unchosen = await projectsActions.create(
			fakeEvent({
				url: 'http://console.example/projects?/create',
				session: TOKEN,
				user: USER,
				orgs: [ORG, { ...ORG, id: OTHER_PROJECT, name: 'SaasifyEase', slug: 'saasifyease' }],
				form: { name: 'Lab' }
			}).event as never
		);
		assert.equal(isActionFailure(unchosen), true);
		assert.equal(skipped.length, 0);
	});

	test('no Catalog approvals UI is left, and project delete keeps hosts in the org', () => {
		const settings = readFileSync(new URL('../src/routes/projects/[id]/SettingsSection.svelte', import.meta.url), 'utf8');
		assert.equal(settings.includes('Hosts stay in the organization.'), true);
		assert.equal(settings.includes('host credentials'), false);
		const host = readFileSync(new URL('../src/routes/hosts/[id]/+page.server.ts', import.meta.url), 'utf8');
		assert.equal(host.includes('catalog-approvals'), false);
	});
});
