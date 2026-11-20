// Remove a revoked host from the lists (umbrella docs/spec-host-remove.md rev 1, approved by Joe 2026-11-20; api-v1
// v1.40): a Remove row action on revoked hosts in the org Hosts table, the removeHost form action, the toast, and the
// host page line for an archived host.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { Host } from '../src/lib/api-types.ts';
import { afterHostRemoved, ORG_HOSTS_HEADING_ID } from '../src/lib/host-name.ts';
import { parseHost } from '../src/lib/server/shape.ts';
import OrgHostsSection from '../src/routes/orgs/[id]/OrgHostsSection.svelte';
import HostPage from '../src/routes/hosts/[id]/+page.svelte';
import { actions as orgActions } from '../src/routes/orgs/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const read = (path: string) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');
const HOST_ID = '00000000-0000-4000-8000-0000000000d1';

function host(id: string, name: string, status: Host['status'], archived_at: string | null = null): Host {
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
		archived_at,
		access_level: null,
		access: [],
		programs: null
	};
}

const LIVE = host('00000000-0000-4000-8000-0000000000d2', 'pi1', 'online');
const GONE = host(HOST_ID, 'pi2', 'revoked');

function bodyOf(html: string): string {
	return html.replace(/<!--[\s\S]*?-->/g, '');
}

function removeEvent(form: Record<string, string>) {
	return fakeEvent({
		url: `http://console.example/orgs/${ORG.id}?tab=hosts&/removeHost`,
		session: TOKEN,
		user: USER,
		projects: [PROJECT],
		orgs: [ORG],
		params: { id: ORG.id },
		form
	}).event as never;
}

describe('archived_at on a host', () => {
	test('parseHost reads archived_at, and an older API without it is null', () => {
		const wire = { ...GONE, archived_at: '2026-11-20T09:00:00Z' };
		assert.equal(parseHost(wire)?.archived_at, '2026-11-20T09:00:00Z');
		const { archived_at: _drop, ...older } = wire;
		assert.equal(parseHost(older)?.archived_at, null);
		assert.equal(parseHost({ ...wire, archived_at: null })?.archived_at, null);
		assert.equal(parseHost({ ...wire, archived_at: 7 }), null);
	});
});

describe('org Hosts table Remove', () => {
	const props = { omitted: 0, truncated: false, projects: [], message: null };

	test('Remove shows on revoked hosts only, labelled with the revoked host name', () => {
		setPageUrl(`http://console.example/orgs/${ORG.id}?tab=hosts`);
		const html = bodyOf(render(OrgHostsSection, { props: { ...props, hosts: [LIVE, GONE] } }).body);
		const removes = [...html.matchAll(/aria-label="Remove ([^"]+)"/g)].map((m) => m[1]);
		assert.deepEqual(removes, ['pi2 (revoked)']);
		// The live host keeps Revoke credential and has no Remove.
		assert.deepEqual([...html.matchAll(/aria-label="Revoke credential ([^"]+)"/g)].map((m) => m[1]), ['pi1']);
	});

	test('the source: Trash2 ConfirmAction with the exact confirm text, removeHost action, toast and focus after success', () => {
		const section = read('src/routes/orgs/[id]/OrgHostsSection.svelte');
		assert.match(
			section,
			/message="Remove \{removedLabel\} from all host lists\? Its history and audit records stay\. The console cannot bring it back\."/
		);
		assert.match(section, /rowLabel="Remove \{hostLabel\(host\)\}"/);
		assert.match(section, /icon=\{Trash2\}/);
		assert.match(section, /action=\{actionUrlFrom\(page\.url, 'removeHost'\)\}/);
		assert.match(section, /confirmLabel="Remove"/);
		// A failure closes the dialog and focuses the section Notice, as the org page's other ConfirmActions do.
		assert.match(section, /closeOnFailure\s*failureFocusId=\{ORG_ERROR_ID\}\s*id="remove-host-/);
		assert.match(section, /\{#if host\.status !== 'revoked'\}[\s\S]*?\{:else\}[\s\S]*?id="remove-host-/);
		assert.match(section, /\{@const removedLabel = hostLabel\(host\)\}/);
		assert.match(section, /onSuccess=\{\(\) => removed\(removedLabel\)\}/);
		assert.match(section, /afterHostRemoved\(label, toastHostRemoved, /);
		assert.match(section, /<h2 id=\{ORG_HOSTS_HEADING_ID\} tabindex="-1"/);
		assert.match(read('src/lib/success-toast.ts'), /toast\.success\(`Removed \$\{name\}\.`\)/);
	});
});

describe('removeHost action', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('a bad host id is refused without calling the API', async () => {
		const calls = installFetch(() => envelope(null));
		const result = await orgActions.removeHost(removeEvent({ host_id: 'nope' }));
		if (!isActionFailure(result)) assert.fail('expected a failure');
		assert.equal(result.status, 400);
		assert.deepEqual(result.data, { scope: 'hosts', message: 'That host is not valid.' });
		assert.equal(calls.length, 0);
	});

	test('an API refusal shows as the Hosts section notice', async () => {
		installFetch(() => envelope(null, 409, 'conflict', 'revoke this host first'));
		const result = await orgActions.removeHost(removeEvent({ host_id: HOST_ID }));
		if (!isActionFailure(result)) assert.fail('expected a failure');
		assert.equal(result.status, 409);
		const data = result.data as unknown as { scope?: string; message?: string; field?: string };
		assert.equal(data.scope, 'hosts');
		assert.equal(data.message, 'revoke this host first');
		assert.equal(data.field, undefined);
	});

	test('success posts the archive with no body and reloads the Hosts tab', async () => {
		const calls = installFetch(() => envelope({ id: HOST_ID }));
		await assert.rejects(
			async () => orgActions.removeHost(removeEvent({ host_id: HOST_ID })),
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/orgs/${ORG.id}?tab=hosts`
		);
		assert.equal(calls.length, 1);
		assert.equal(calls[0]?.method, 'POST');
		assert.equal(calls[0]?.url.pathname, `/v1/hosts/${HOST_ID}/archive`);
		assert.equal(calls[0]?.body, null);
	});
});

describe('host page', () => {
	function pageHtml(h: Host): string {
		setPageUrl(`http://console.example/hosts/${h.id}?tab=processes`);
		return render(HostPage, {
			props: {
				data: {
					host: h,
					range: '1h',
					processSort: 'cpu',
					tab: 'processes',
					message: null,
					processes: null,
					processesMessage: null,
					serviceLinks: {},
					metrics: []
				} as never,
				form: null
			}
		}).body;
	}

	test('after Remove: one toast with the revoked label, then focus on the Hosts heading', () => {
		const calls: string[] = [];
		afterHostRemoved(
			'pi2 (revoked)',
			(label) => calls.push(`toast:${label}`),
			(id) => calls.push(`focus:${id}`)
		);
		assert.deepEqual(calls, ['toast:pi2 (revoked)', 'focus:org-hosts-heading']);
		assert.equal(ORG_HOSTS_HEADING_ID, 'org-hosts-heading');
	});

	test('an archived host shows the removed line; a revoked one does not', () => {
		const archived = bodyOf(pageHtml({ ...GONE, archived_at: '2026-11-20T09:00:00Z' }));
		assert.match(archived, /<p class="text-muted-foreground mb-4 text-sm">This host was removed from the lists on <time[^>]*>[^<]+<\/time>\.<\/p>/);
		assert.equal(bodyOf(pageHtml(GONE)).includes('removed from the lists'), false);
	});
});
