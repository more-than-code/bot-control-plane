// New organization in the console (umbrella docs/spec-new-org.md rev 1 §4–§5, approved by Joe 2026-11-22).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import { startSubmit, submitResultStep } from '../src/lib/dialog-open.ts';
import { headerNav, mobileOrgItems, type NavOrg, type NavProject } from '../src/lib/header-nav.ts';
import NewOrganization from '../src/lib/components/NewOrganization.svelte';
import { actions } from '../src/routes/projects/+page.server.ts';
import { envelope, fakeEvent, installFetch, ORG, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

const NEW_ORG = '00000000-0000-4000-8000-0000000000e1';
const CREATED = { id: NEW_ORG, name: 'Kea Studio', slug: 'kea-studio', role: 'owner', created_at: '2026-11-22T00:00:00Z' };

function createOrg(form: Record<string, string>, user = USER) {
	return actions.createOrg(
		fakeEvent({ url: 'http://console.example/projects?/createOrg', session: TOKEN, user, orgs: [ORG], form }).event as never
	);
}

function failureData(result: unknown): { status: number; message: unknown } {
	assert.equal(isActionFailure(result), true);
	const failure = result as { status: number; data: { message?: unknown } };
	return { status: failure.status, message: failure.data.message };
}

describe('createOrg action', () => {
	before(() => useApi('https://api.test'));
	after(() => restoreEnv());

	test('posts {"name"} once to /v1/orgs and lands on the new org Projects tab', async () => {
		const calls = installFetch(() => envelope(CREATED, 201));
		await assert.rejects(
			async () => createOrg({ name: '  Kea Studio  ' }),
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/orgs/${NEW_ORG}?tab=projects`
		);
		assert.equal(calls.length, 1);
		assert.equal(calls[0]?.method, 'POST');
		assert.equal(calls[0]?.url.pathname, '/v1/orgs');
		assert.equal(calls[0]?.authorization, `Bearer ${TOKEN}`);
		assert.equal(calls[0]?.body, JSON.stringify({ name: 'Kea Studio' }));
	});

	test('an empty or 81-character name fails inline with no API call', async () => {
		const calls = installFetch(() => envelope(CREATED, 201));
		for (const name of ['', '   ', 'x'.repeat(81)]) {
			const failure = failureData(await createOrg({ name }));
			assert.equal(failure.status, 400, JSON.stringify(name));
			assert.equal(failure.message, 'Enter an organization name (1–80 characters).');
		}
		const missing = failureData(await createOrg({}));
		assert.equal(missing.status, 400);
		assert.equal(calls.length, 0);
	});

	test('an 80-character name is sent', async () => {
		const calls = installFetch(() => envelope(CREATED, 201));
		await assert.rejects(async () => createOrg({ name: 'x'.repeat(80) }), (err: unknown) => isRedirect(err));
		assert.equal(calls.length, 1);
	});

	test('a 400 or 409 from the API keeps its status and message for the dialog', async () => {
		for (const [status, code, msg] of [
			[400, 'invalid_request', 'name is not valid'],
			[409, 'conflict', 'an organization with that slug already exists']
		] as const) {
			installFetch(() => envelope(null, status, code, msg));
			const failure = failureData(await createOrg({ name: 'Kea Studio' }));
			assert.equal(failure.status, status);
			assert.equal(failure.message, msg);
		}
	});

	test('a malformed answer is a 502', async () => {
		for (const payload of [null, { id: 'not-a-uuid', name: 'Kea Studio' }, { ...CREATED, slug: 'Bad Slug' }]) {
			installFetch(() => envelope(payload, 201));
			const failure = failureData(await createOrg({ name: 'Kea Studio' }));
			assert.equal(failure.status, 502);
			assert.equal(failure.message, 'The API returned an unexpected response.');
		}
	});

	test('a signed-out post is sent to sign-in with no API call', async () => {
		const calls = installFetch(() => envelope(CREATED, 201));
		await assert.rejects(
			async () =>
				actions.createOrg(
					fakeEvent({ url: 'http://console.example/projects?/createOrg', form: { name: 'Kea Studio' } }).event as never
				),
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location.startsWith('/signin')
		);
		assert.equal(calls.length, 0);
	});
});

describe('NewOrganization dialog', () => {
	test('a trigger that opens a dialog; the form stays inside it until open', () => {
		const body = render(NewOrganization, { props: { action: '?/createOrg' } }).body;
		assert.match(body, /<button\b[^>]*>[\s\S]*New organization[\s\S]*<\/button>/);
		assert.match(body, /aria-expanded="false"/);
		assert.equal(body.includes('name="name"'), false);
		const hidden = render(NewOrganization, { props: { action: '?/createOrg', showTrigger: false } }).body;
		assert.equal(hidden.includes('New organization'), false);
	});

	test('source: one Name field (80) described by the owner line, Create and Cancel, and the shared helpers', () => {
		const source = readFileSync(new URL('../src/lib/components/NewOrganization.svelte', import.meta.url), 'utf8');
		assert.match(source, /<Input id="org-name" name="name" required maxlength=\{80\}[^>]*aria-describedby="org-name-help"/);
		assert.equal(source.match(/<Input\b/g)?.length, 1);
		assert.match(
			source,
			/<Dialog\.Description id="org-name-help">You will be its owner\. Add projects, members and hosts from its page\.<\/Dialog\.Description>/
		);
		assert.match(source, /<Button type="submit" disabled=\{saving\}>Create<\/Button>/);
		assert.match(source, /<Dialog\.Close[^>]*>Cancel<\/Dialog\.Close>/);
		assert.match(source, /startSubmit\(saving\)[\s\S]*submitResultStep\(\{/);
	});

	test('the header and /projects post to createOrg with ownResult', () => {
		const header = readFileSync(new URL('../src/lib/components/HeaderNav.svelte', import.meta.url), 'utf8');
		assert.match(header, /<NewOrganization[\s\S]*action="\/projects\?\/createOrg"[\s\S]*showTrigger=\{false\}[\s\S]*ownResult/);
		const page = readFileSync(new URL('../src/routes/projects/+page.svelte', import.meta.url), 'utf8');
		assert.match(page, /<NewOrganization action=\{actionUrlFrom\(page\.url, 'createOrg'\)\} ownResult \/>/);
		// Outside the askOrgAdmin branch: every signed-in user sees it.
		assert.ok(page.indexOf('<NewOrganization') > page.indexOf('{/if}'));
	});
});

describe('create dialog submit helpers', () => {
	const FALLBACK = 'The organization could not be created.';
	const step = (type: string, ownResult = true, message: unknown = undefined) =>
		submitResultStep({ type, ownResult, message, fallback: FALLBACK });

	test('the guard: the first submit starts saving, a second while saving is cancelled', () => {
		assert.deepEqual(startSubmit(false), { cancel: false, saving: true });
		assert.deepEqual(startSubmit(true), { cancel: true, saving: true });
	});

	test('an ownResult failure stays in the dialog with its message, no update, saving reset', () => {
		assert.deepEqual(step('failure', true, 'an organization with that slug already exists'), {
			ownMessage: 'an organization with that slug already exists',
			update: false,
			settle: null,
			saving: false
		});
		assert.equal(step('failure', true, 42).ownMessage, FALLBACK);
		assert.equal(step('failure', true).ownMessage, FALLBACK);
	});

	test('a failure without ownResult updates the page form and settles as a failure', () => {
		assert.deepEqual(step('failure', false, 'no'), { ownMessage: null, update: true, settle: 'failure', saving: false });
	});

	test('a redirect follows it and keeps Create disabled, so a second click sends nothing', () => {
		const redirect = step('redirect');
		assert.deepEqual(redirect, { ownMessage: null, update: true, settle: 'success', saving: true });
		assert.equal(startSubmit(redirect.saving).cancel, true);
	});

	test('an error resets saving and settles as a failure; a success settles and resets', () => {
		assert.deepEqual(step('error'), { ownMessage: null, update: true, settle: 'failure', saving: false });
		assert.deepEqual(step('success'), { ownMessage: null, update: true, settle: 'success', saving: false });
		assert.equal(startSubmit(step('error').saving).cancel, false);
	});
});

describe('header model: New organization', () => {
	const A = '00000000-0000-4000-8000-0000000000a1';
	const B = '00000000-0000-4000-8000-0000000000b1';
	const C = '00000000-0000-4000-8000-0000000000c1';
	const OTHER = '00000000-0000-4000-8000-0000000000d1';
	const P1 = '00000000-0000-4000-8000-000000000101';
	const P9 = '00000000-0000-4000-8000-000000000109';
	const ORGS: NavOrg[] = [
		{ id: A, name: 'Alpha Org', role: 'owner' },
		{ id: B, name: 'Beta Org', role: 'admin' },
		{ id: C, name: 'Gamma Org', role: 'member' }
	];
	const PROJECTS: NavProject[] = [
		{ id: P1, name: 'api', org_id: A, org_name: 'Alpha Org' },
		{ id: P9, name: 'shared', org_id: OTHER, org_name: 'Partner Org' }
	];

	test('offered where the org items render: every org member (owner, admin, member), not a project-only member', () => {
		for (const id of [A, B, C]) {
			const nav = headerNav({ path: `/orgs/${id}`, orgs: ORGS, projects: PROJECTS });
			assert.equal(nav.org?.menu, true, id);
			assert.equal(mobileOrgItems(nav), true, id);
		}
		// A project-only member with no org: no Org menu (the name is text) and no phone org items, so no New
		// organization in the header; /projects still offers it.
		const projectOnly = headerNav({ path: `/projects/${P9}`, orgs: [], projects: PROJECTS });
		assert.equal(projectOnly.org?.menu, false);
		assert.equal(mobileOrgItems(projectOnly), false);
		assert.equal('canCreateOrg' in projectOnly, false);
		const source = readFileSync(new URL('../src/lib/components/HeaderNav.svelte', import.meta.url), 'utf8');
		// orgItems (which holds New organization) renders only inside the Org menu and the phone org group.
		assert.equal(source.match(/\{@render orgItems\(/g)?.length, 2);
		assert.match(source, /\{#if nav\.org\.menu\}[\s\S]*?\{@render orgItems\(ORG_ID\)\}/);
		assert.match(source, /\{#if mobileOrgItems\(nav\)\}[\s\S]*?\{@render orgItems\(PROJECT_MOBILE_ID\)\}/);
	});

	test('the Org menu: org list, New organization, separator, org page, All projects; the phone menu reuses it', () => {
		const source = readFileSync(new URL('../src/lib/components/HeaderNav.svelte', import.meta.url), 'utf8');
		const start = source.indexOf('{#snippet orgItems(from: string)}');
		const items = source.slice(start, source.indexOf('{#snippet projectItems'));
		const order = ['{#each nav.orgs as org', 'New organization', '<DropdownMenu.Separator />', 'nav.orgPage', 'All projects'].map((text) =>
			items.indexOf(text)
		);
		assert.ok(order.every((index) => index >= 0), JSON.stringify(order));
		assert.deepEqual([...order].sort((a, b) => a - b), order);
		assert.match(items, /<DropdownMenu\.Item onSelect=\{\(\) => newOrg\(from\)\}><Plus class="size-4" \/>New organization/);
		// Desktop and phone render the same items; the phone one before the project items.
		assert.match(source, /\{@render orgItems\(ORG_ID\)\}/);
		const mobile = source.slice(source.indexOf('id={PROJECT_MOBILE_ID}'));
		assert.ok(mobile.indexOf('{@render orgItems(PROJECT_MOBILE_ID)}') >= 0);
		assert.ok(mobile.indexOf('{@render orgItems(PROJECT_MOBILE_ID)}') < mobile.indexOf('{@render projectItems(PROJECT_MOBILE_ID)}'));
		// The phone menu has org items (so New organization) whenever the caller is in an org.
		assert.equal(mobileOrgItems(headerNav({ path: `/orgs/${C}`, orgs: ORGS, projects: PROJECTS })), true);
	});
});
