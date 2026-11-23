import assert from 'node:assert/strict';
import { after, before, describe, test } from 'node:test';
import { isActionFailure } from '@sveltejs/kit';
import { render } from 'svelte/server';
import HostPage from '../src/routes/hosts/[id]/+page.svelte';
import { actions as hostActions, load as hostLoad } from '../src/routes/hosts/[id]/+page.server.ts';
import {
	changeIsEmpty,
	changeLines,
	imagePatternProblem,
	isImagePattern,
	parseHostPolicy,
	policyDiff,
	projectProblem,
	stagedFromUrl,
	type HostPolicyTab
} from '../src/lib/host-policy.ts';
import { HOST_POLICY_APPLY_WAIT_MS } from '../src/lib/server/host-policy.ts';
import { envelope, fakeEvent, installFetch, ORG, restoreEnv, TOKEN, useApi, USER, type FetchCall } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';
import type { Org } from '../src/lib/api-types.ts';

// api-v1 v1.49: a host's console-managed policy (spec-console-host-policy).

const HOST_ID = '00000000-0000-4000-8000-0000000000ab';
const HASH = 'a'.repeat(64);
const PATH = `/v1/orgs/${ORG.id}/hosts/${HOST_ID}/policy`;

const HOST = {
	id: HOST_ID,
	org_id: ORG.id,
	name: 'pi3',
	node_type: 'pi',
	os: 'linux',
	arch: 'arm64',
	always_on: true,
	agent_version: '0.2.15',
	status: 'online',
	last_seen_at: '2026-11-23T00:00:00Z',
	enrolled_at: '2026-11-01T00:00:00Z',
	revoked_at: null,
	access_level: 'deploy',
	access: null
};

const MANAGED = { managed: true, policy_hash: HASH, edge: false, images: ['ghcr.io/acme/*'], projects: ['web'], apply: null };
const APPLY = {
	state: 'running',
	started_at: '2026-11-23T00:00:00Z',
	ends_by: '2026-11-23T00:02:30Z',
	started_by: { id: USER.id, name: 'Joe' },
	change: { edge_from: false, edge_to: true, images_added: [], images_removed: [], projects_added: [], projects_removed: [] },
	finished_at: null
};

const ADMIN: Org[] = [ORG];
const MEMBER: Org[] = [{ ...ORG, role: 'member' }];

function handler(policy: unknown) {
	return (call: FetchCall): Response => {
		const path = call.url.pathname;
		if (path === `/v1/hosts/${HOST_ID}`) return envelope(HOST);
		if (path === PATH && call.method === 'GET') return envelope(policy);
		if (path === PATH && call.method === 'POST') return envelope(APPLY, 202);
		if (path.endsWith('/edge')) return envelope({ enabled: false, available: false, status: 'off', routes: 0, applied_at: null, detail: null, config_hash: null });
		if (path.endsWith('/access') || path.endsWith('/projects')) return envelope({ items: [] });
		return envelope(null, 500, 'error', 'unexpected');
	};
}

async function load(url: string, orgs: Org[], policy: unknown = MANAGED) {
	const calls = installFetch(handler(policy));
	const data = (await hostLoad(fakeEvent({ url, session: TOKEN, user: USER, orgs, params: { id: HOST_ID } }).event as never)) as Record<string, unknown> & {
		policy?: HostPolicyTab | null;
	};
	return { calls, data };
}

function renderPage(url: string, data: Record<string, unknown>, form: unknown = null): string {
	setPageUrl(url);
	return render(HostPage, { props: { data: data as never, form: form as never } }).body.replaceAll('&amp;', '&').replace(/<!--[\s\S]*?-->/g, '');
}

describe('host policy: helpers', () => {
	test('image patterns follow the runner.toml rule', () => {
		for (const ok of ['ghcr.io/acme/*', 'ghcr.io/acme/web', 'localhost:5000/a/*', 'docker.io/library/caddy']) assert.equal(isImagePattern(ok), true, ok);
		for (const bad of ['', 'acme', '*/acme', 'ghcr.io/', 'ghcr.io/Acme', 'ghcr.io/ac*me', 'ghcr.io/a b', 'ghcr.io/a@sha256', 'ghcr.io:0/a']) {
			assert.equal(isImagePattern(bad), false, bad);
		}
		assert.match(imagePatternProblem('ghcr.io/acme/*', ['ghcr.io/acme/*']) ?? '', /already/);
		assert.equal(projectProblem('web-2', []), null);
		assert.match(projectProblem('Web', []) ?? '', /slug/);
	});

	test('a link stages a change and drops what is not valid', () => {
		const base = { edge: false, images: ['ghcr.io/acme/*'], projects: ['web'] };
		const params = new URLSearchParams('policy.edge=on&policy.add_image=ghcr.io/beta/*&policy.add_image=ghcr.io/acme/*&policy.add_image=BAD&policy.add_project=api');
		const { values, dropped } = stagedFromUrl(params, base);
		assert.deepEqual(values, { edge: true, images: ['ghcr.io/acme/*', 'ghcr.io/beta/*'], projects: ['web', 'api'] });
		assert.equal(dropped, 1);
		assert.deepEqual(base.images, ['ghcr.io/acme/*'], 'the base is not changed');
		const diff = policyDiff(base, values);
		assert.deepEqual(changeLines(diff), ['Turn on the edge', 'Allow images ghcr.io/beta/*', 'Allow project api']);
		assert.equal(changeIsEmpty(policyDiff(base, base)), true);
	});

	test('the read is parsed strictly', () => {
		assert.deepEqual(parseHostPolicy(MANAGED), MANAGED);
		assert.equal(parseHostPolicy({ ...MANAGED, policy_hash: 'nope' }), null);
		assert.equal(parseHostPolicy({ ...MANAGED, images: 'x' }), null);
		assert.equal(parseHostPolicy({ ...MANAGED, apply: APPLY })?.apply?.state, 'running');
		assert.equal(parseHostPolicy({ ...MANAGED, apply: { ...APPLY, state: 'weird' } })?.apply?.state, 'unknown');
	});
});

describe('host policy: load, page and action', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('a member never reads the policy and gets Overview', async () => {
		const { calls, data } = await load(`http://console.example/hosts/${HOST_ID}?tab=policy`, MEMBER);
		assert.equal(data.tab, 'overview');
		assert.equal(calls.some((call) => call.url.pathname === PATH), false);
	});

	test('an admin sees the tab with a staged link change', async () => {
		const url = `http://console.example/hosts/${HOST_ID}?tab=policy&policy.edge=on&policy.add_image=ghcr.io/beta/*`;
		const { data } = await load(url, ADMIN);
		assert.equal(data.tab, 'policy');
		assert.equal(data.policy?.policy?.policy_hash, HASH);
		const html = renderPage(url, data);
		assert.match(html, /data-policy-tab/);
		assert.match(html, /Turn on the edge/);
		assert.match(html, /Allow images ghcr.io\/beta\/\*/);
		assert.match(html, /aria-current="page"[^>]*>[\s\S]*?Policy/);
	});

	test('an unmanaged host shows the one opt-in line', async () => {
		const url = `http://console.example/hosts/${HOST_ID}?tab=policy`;
		const { data } = await load(url, ADMIN, { ...MANAGED, managed: false, policy_hash: null, images: [] });
		const html = renderPage(url, data);
		assert.match(html, /data-policy-unmanaged/);
		assert.match(html, /console_policy = true/);
		assert.doesNotMatch(html, /data-policy-diff/);
	});

	test('apply posts the whole staged policy with the long wait', async () => {
		const calls = installFetch(handler(MANAGED));
		const result = await hostActions.applyPolicy(
			fakeEvent({
				url: `http://console.example/hosts/${HOST_ID}?tab=policy&/applyPolicy`,
				session: TOKEN,
				user: USER,
				orgs: ADMIN,
				params: { id: HOST_ID },
				form: { base_hash: HASH, edge: '1', image: ['ghcr.io/acme/*', 'ghcr.io/beta/*'], project: ['web'] }
			}).event as never
		);
		assert.equal(isActionFailure(result), false);
		assert.equal((result as { policyApply: { state: string } }).policyApply.state, 'running');
		const post = calls.find((call) => call.url.pathname === PATH && call.method === 'POST');
		assert.ok(post);
		assert.deepEqual(JSON.parse(post.body ?? ''), { base_hash: HASH, edge: true, images: ['ghcr.io/acme/*', 'ghcr.io/beta/*'], projects: ['web'] });
		assert.ok(HOST_POLICY_APPLY_WAIT_MS > 70_000);
	});

	test('apply refuses a bad form before calling the API, and a member', async () => {
		for (const [form, orgs] of [
			[{ base_hash: 'x', edge: '1' }, ADMIN],
			[{ base_hash: HASH, edge: '1', image: ['NOPE'] }, ADMIN],
			[{ base_hash: HASH, edge: '1' }, MEMBER]
		] as const) {
			const calls = installFetch(handler(MANAGED));
			const result = await hostActions.applyPolicy(
				fakeEvent({ url: `http://console.example/hosts/${HOST_ID}?tab=policy&/applyPolicy`, session: TOKEN, user: USER, orgs: [...orgs], params: { id: HOST_ID }, form }).event as never
			);
			assert.equal(isActionFailure(result), true);
			assert.equal(calls.some((call) => call.url.pathname === PATH && call.method === 'POST'), false);
		}
	});

	test('a refusal keeps the API text', async () => {
		installFetch((call) =>
			call.url.pathname === PATH ? envelope(null, 409, 'conflict', "the host's policy changed since this page loaded; reload and make the change again") : handler(MANAGED)(call)
		);
		const result = await hostActions.applyPolicy(
			fakeEvent({ url: `http://console.example/hosts/${HOST_ID}?tab=policy&/applyPolicy`, session: TOKEN, user: USER, orgs: ADMIN, params: { id: HOST_ID }, form: { base_hash: HASH, edge: '0' } }).event as never
		);
		assert.equal(isActionFailure(result), true);
		assert.match(String((result as { data: { policyMessage: string } }).data.policyMessage), /changed since this page loaded/);
	});
});
