// Host names (umbrella docs/spec-host-names.md rev 1, approved by Joe 2026-11-20; api-v1 v1.38): Rename in the
// org Hosts table for org owners and admins on live hosts, and revoked hosts sorted after live ones.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { Host } from '../src/lib/api-types.ts';
import { canRenameHost, HOST_NAME_HINT, HOST_NAME_MESSAGE, hostLabel, nextRenameToast, revokedLast, validHostName } from '../src/lib/host-name.ts';
import OrgHostsSection from '../src/routes/orgs/[id]/OrgHostsSection.svelte';
import HostsSection from '../src/routes/projects/[id]/HostsSection.svelte';
import { actions as orgActions } from '../src/routes/orgs/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const read = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');
const HOST_ID = '00000000-0000-4000-8000-0000000000c1';

function host(id: string, name: string, status: Host['status']): Host {
	return {
		id,
		org_id: ORG.id,
		name,
		node_type: 'pi',
		os: 'linux',
		arch: 'arm64',
		always_on: true,
		agent_version: '0.2.11',
		status,
		last_seen_at: '2026-11-20T10:00:00Z',
		enrolled_at: '2026-11-13T10:00:00Z',
		revoked_at: status === 'revoked' ? '2026-11-13T11:00:00Z' : null,
		access_level: null,
		access: [],
		programs: null
	};
}

const OLD_PI2 = host('00000000-0000-4000-8000-0000000000c2', 'pi2', 'revoked');
const PI1 = host('00000000-0000-4000-8000-0000000000c3', 'pi1', 'online');
const NEW_PI2 = host(HOST_ID, 'pi2', 'offline');
const PI3 = host('00000000-0000-4000-8000-0000000000c4', 'pi3', 'revoked');
// The API's name order: pi1, pi2 (revoked), pi2, pi3 (revoked).
const API_ORDER = [PI1, OLD_PI2, NEW_PI2, PI3];

function renameEvent(form: Record<string, string>) {
	return fakeEvent({
		url: `http://console.example/orgs/${ORG.id}?tab=hosts&/renameHost`,
		session: TOKEN,
		user: USER,
		projects: [PROJECT],
		orgs: [ORG],
		params: { id: ORG.id },
		form
	}).event as never;
}

function bodyOf(html: string): string {
	return html.replace(/<!--[\s\S]*?-->/g, '');
}

describe('host name rule and order', () => {
	test('the dialog checks the API rule', () => {
		for (const name of ['pi2', 'a', 'pi-2', '0', 'a'.repeat(63)]) assert.equal(validHostName(name), true, name);
		for (const name of ['', 'Pi2', 'pi_2', 'pi 2', ' pi2', 'pi2 ', 'pi.2', 'ü', 'a'.repeat(64)]) assert.equal(validHostName(name), false, name);
		assert.equal(HOST_NAME_HINT, '1–63 characters: lowercase letters, digits and hyphens.');
		assert.equal(HOST_NAME_MESSAGE, 'Name must be 1–63 characters: lowercase letters, digits and -.');
	});

	test('revoked hosts go last, each group keeps its order, and the input is not changed', () => {
		const input = [...API_ORDER];
		assert.deepEqual(revokedLast(input).map((h) => h.id), [PI1.id, NEW_PI2.id, OLD_PI2.id, PI3.id]);
		assert.deepEqual(input, API_ORDER);
		assert.deepEqual(revokedLast([]), []);
		assert.equal(canRenameHost(NEW_PI2), true);
		assert.equal(canRenameHost(OLD_PI2), false);
	});

	test('a revoked host is labelled as revoked, so it differs from a live host of the same name', () => {
		assert.equal(hostLabel(NEW_PI2), 'pi2');
		assert.equal(hostLabel(PI1), 'pi1');
		assert.equal(hostLabel(OLD_PI2), 'pi2 (revoked)');
	});
});

describe('rename toast', () => {
	const rename = { hostId: HOST_ID, from: 'pi2', to: 'pi9' };

	test('an open dialog holds the toast until that host closes or unmounts', () => {
		const held = nextRenameToast(null, { kind: 'succeeded', rename, dialogOpen: true });
		assert.deepEqual(held, { pending: rename, fire: null });
		// Another row closing or unmounting in the same reload does not fire it.
		assert.deepEqual(nextRenameToast(held.pending, { kind: 'closed', hostId: PI1.id }), { pending: rename, fire: null });
		// The renamed row closing (or unmounting because the search no longer matches) fires it once.
		const fired = nextRenameToast(held.pending, { kind: 'closed', hostId: HOST_ID });
		assert.deepEqual(fired, { pending: null, fire: rename });
		assert.deepEqual(nextRenameToast(fired.pending, { kind: 'closed', hostId: HOST_ID }), { pending: null, fire: null });
	});

	test('a dialog closed while the request ran fires now, never at a later close', () => {
		// Escape, then the request succeeds: the earlier close already reported, so the toast fires at once.
		const early = nextRenameToast(nextRenameToast(null, { kind: 'closed', hostId: HOST_ID }).pending, {
			kind: 'succeeded',
			rename,
			dialogOpen: false
		});
		assert.deepEqual(early, { pending: null, fire: rename });
		// A later open and close of that dialog finds nothing pending.
		assert.deepEqual(nextRenameToast(early.pending, { kind: 'closed', hostId: HOST_ID }), { pending: null, fire: null });
	});

	test('a rename to the same name has no toast', () => {
		assert.deepEqual(nextRenameToast(null, { kind: 'succeeded', rename: { ...rename, to: 'pi2' }, dialogOpen: true }), { pending: null, fire: null });
	});
});

describe('org Hosts table', () => {
	const props = { omitted: 0, truncated: false, projects: [], message: null };

	test('Rename shows on live hosts only, and revoked hosts list last', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=hosts`);
		const html = bodyOf(render(OrgHostsSection, { props: { ...props, hosts: API_ORDER } }).body);
		const renames = [...html.matchAll(/aria-label="Rename ([^"]+)"/g)].map((m) => m[1]);
		assert.deepEqual(renames, ['pi1', 'pi2']);
		const order = [...html.matchAll(/href="\/hosts\/([^"]+)"/g)].map((m) => m[1]);
		assert.deepEqual(order, [PI1.id, NEW_PI2.id, OLD_PI2.id, PI3.id]);
	});

	test('a revoked row says so in its access action and link, so a same-name live row is distinct', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=hosts`);
		const html = bodyOf(render(OrgHostsSection, { props: { ...props, hosts: API_ORDER } }).body);
		const access = [...html.matchAll(/aria-label="Manage access ([^"]+)"/g)].map((m) => m[1]);
		assert.deepEqual(access, ['pi1', 'pi2', 'pi2 (revoked)', 'pi3 (revoked)']);
		assert.match(html, new RegExp(`href="/hosts/${OLD_PI2.id}" aria-label="pi2 \\(revoked\\)">pi2</a>`));
		assert.match(html, new RegExp(`href="/hosts/${NEW_PI2.id}">pi2</a>`));
		assert.match(read('src/routes/orgs/[id]/OrgHostsSection.svelte'), /<Dialog\.Title>Access to \{hostLabel\(host\)\}<\/Dialog\.Title>/);
	});

	test('a failed rename is routed to that host dialog only (dialogs render on the client, so this checks the wiring)', () => {
		const page = read('src/routes/orgs/[id]/+page.svelte');
		assert.match(page, /action\?\.field === 'renameHost' && action\.hostId \? \{ hostId: action\.hostId, message: action\.message \?\? null \} : null/);
		assert.match(page, /\{renameFailure\}/);
		const section = read('src/routes/orgs/[id]/OrgHostsSection.svelte');
		assert.match(section, /failed=\{renameFailure\?\.hostId === host\.id\}/);
		assert.match(section, /message=\{renameFailure\?\.hostId === host\.id \? renameFailure\.message : null\}/);
		// A dialog message does not also show as the section Notice: renameHost sets `field`.
		assert.match(page, /action\?\.scope === scope && !action\.field/);
		const dialog = read('src/routes/orgs/[id]/RenameHost.svelte');
		assert.match(dialog, /let open = \$state\(untrack\(\(\) => failed\)\)/);
		assert.match(dialog, /HOST_NAME_HINT/);
		assert.match(dialog, /aria-describedby=\{shownMessage \? `\$\{messageId\} \$\{hintId\}` : hintId\}/);
	});

	test('the dialog source: Pencil RowAction, one name field, rule check before posting, toast after close', () => {
		const dialog = read('src/routes/orgs/[id]/RenameHost.svelte');
		assert.match(dialog, /<RowAction label="Rename \{name\}" icon=\{Pencil\}>/);
		assert.equal(dialog.match(/name="name"/g)?.length, 1);
		assert.match(dialog, /maxlength=\{63\}/);
		assert.match(dialog, /if \(!validHostName\(to\)\) \{\s*localMessage = HOST_NAME_MESSAGE;\s*cancel\(\);/);
		assert.match(dialog, /onOpenChangeComplete=\{closeComplete\}/);
		// The section holds the toast: success is reported before the reload, a close or unmount reports closed.
		assert.match(dialog, /if \(outcome === 'success'\) onRenamed\(\{ hostId, from, to \}, open\);\s*await update\(\);/);
		assert.match(dialog, /onDestroy\(\(\) => onClosed\(\)\)/);
		assert.doesNotMatch(dialog, /toastHostRenamed/);
		assert.match(read('src/lib/success-toast.ts'), /toast\.success\(`Renamed \$\{from\} to \$\{to\}\.`\)/);
		// Any close resets the draft and hides the old messages.
		assert.match(dialog, /function closeComplete\(isOpen: boolean\) \{\s*if \(isOpen\) return;[\s\S]*?draft = name;\s*localMessage = null;\s*serverMessageHidden = true;\s*onClosed\(\);/);
		// The name field: no auto-capitalisation or correction, invalid and described by the message while one shows.
		assert.match(dialog, /autocapitalize="none"/);
		assert.match(dialog, /autocorrect="off"/);
		assert.match(dialog, /spellcheck=\{false\}/);
		assert.match(dialog, /aria-invalid=\{shownMessage \? 'true' : undefined\}/);
		assert.match(dialog, /aria-describedby=\{shownMessage \? `\$\{messageId\} \$\{hintId\}` : hintId\}/);
		assert.match(dialog, /<div id=\{messageId\}><Notice kind="error">\{shownMessage\}<\/Notice><\/div>/);
		assert.match(dialog, /cancel\(\);\s*void tick\(\)\.then\(\(\) => input\?\.focus\(\)\);/);
		const section = read('src/routes/orgs/[id]/OrgHostsSection.svelte');
		assert.match(section, /toastHostRenamed\(next\.fire\.from, next\.fire\.to\)/);
		assert.match(section, /\{#if canRenameHost\(host\)\}\s*<RenameHost/);
		assert.match(section, /action=\{actionUrlFrom\(page\.url, 'renameHost'\)\}/);
	});
});

test('project Hosts table lists revoked hosts last', () => {
	setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=hosts`);
	const html = bodyOf(
		render(HostsSection, {
			props: { hosts: API_ORDER, omitted: 0, truncated: false, canAdmin: false, canOrgAdmin: false, projectId: PROJECT.id, orgId: ORG.id, message: null, hostsMessage: null }
		}).body
	);
	const order = [...html.matchAll(/href="\/hosts\/([^"]+)"/g)].map((m) => m[1]);
	assert.deepEqual(order, [PI1.id, NEW_PI2.id, OLD_PI2.id, PI3.id]);
	// Rename lives in the org Hosts table only (spec-host-names 9, question 1).
	assert.equal(html.includes('Rename '), false);
	assert.match(html, new RegExp(`href="/hosts/${OLD_PI2.id}" aria-label="pi2 \\(revoked\\)">pi2</a>`));
	const leaves = bodyOf(
		render(HostsSection, {
			props: { hosts: API_ORDER, omitted: 0, truncated: false, canAdmin: true, canOrgAdmin: false, projectId: PROJECT.id, orgId: ORG.id, message: null, hostsMessage: null }
		}).body
	);
	assert.deepEqual([...leaves.matchAll(/aria-label="Leave ([^"]+)"/g)].map((m) => m[1]), ['pi1', 'pi2', 'pi2 (revoked)', 'pi3 (revoked)']);
});

// Rename is admin-only end to end: a member's ?tab=hosts falls back to Projects, so the org Hosts table (and its
// Rename) never renders for them. Asserted in tests/orgs.test.ts ('a member sees Projects and Members only; …').

describe('renameHost action', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('a bad name or host id is refused without calling the API', async () => {
		const calls = installFetch(() => envelope(null));
		for (const name of ['', 'Pi2', 'pi_2', 'a'.repeat(64)]) {
			const result = await orgActions.renameHost(renameEvent({ host_id: HOST_ID, name }));
			if (!isActionFailure(result)) assert.fail('expected a failure');
			assert.equal(result.status, 400);
			assert.deepEqual(result.data, { scope: 'hosts', field: 'renameHost', hostId: HOST_ID, message: HOST_NAME_MESSAGE });
		}
		const badId = await orgActions.renameHost(renameEvent({ host_id: 'nope', name: 'pi2' }));
		assert.equal(isActionFailure(badId), true);
		assert.equal(calls.length, 0);
	});

	test('an API refusal passes its message through to that host dialog', async () => {
		const message = 'a host named pi2 already exists in this organization';
		installFetch(() => envelope(null, 409, 'conflict', message));
		const conflict = await orgActions.renameHost(renameEvent({ host_id: HOST_ID, name: 'pi2' }));
		if (!isActionFailure(conflict)) assert.fail('expected a failure');
		assert.equal(conflict.status, 409);
		assert.deepEqual(conflict.data, { scope: 'hosts', field: 'renameHost', hostId: HOST_ID, message });

		installFetch(() => envelope(null, 409, 'conflict', 'this host is revoked'));
		const revoked = await orgActions.renameHost(renameEvent({ host_id: HOST_ID, name: 'pi9' }));
		if (!isActionFailure(revoked)) assert.fail('expected a failure');
		assert.equal((revoked.data as unknown as { message?: string }).message, 'this host is revoked');
	});

	test('success sends PATCH with the name only and reloads the Hosts tab', async () => {
		const calls = installFetch(() => envelope({ id: HOST_ID }));
		await assert.rejects(
			async () => orgActions.renameHost(renameEvent({ host_id: HOST_ID, name: 'pi2' })),
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/orgs/${ORG.id}?tab=hosts`
		);
		assert.equal(calls.length, 1);
		assert.equal(calls[0]?.method, 'PATCH');
		assert.equal(calls[0]?.url.pathname, `/v1/hosts/${HOST_ID}`);
		assert.equal(calls[0]?.body, JSON.stringify({ name: 'pi2' }));
	});
});
