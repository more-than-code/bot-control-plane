import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import { render } from 'svelte/server';
import {
	agentUpdateTooltip,
	upgradeLine,
	upgradeOffer,
	upgradeOutcomeText,
	upgradePollNext,
	upgradeReasonText,
	UPGRADE_POLL_LIMIT_MS
} from '../src/lib/agent-upgrade.ts';
import { parseAgentUpgrade, parseHost } from '../src/lib/server/shape.ts';
import AgentVersion from '../src/lib/components/AgentVersion.svelte';
import { GET, POST } from '../src/routes/hosts/[id]/agent-upgrade/+server.ts';
import { envelope, fakeEvent, installFetch, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

// api-v1 v1.48: agent upgrades from the console.

const HOST_ID = '5bdc639e-8430-4c0c-b2b2-031b56272c15';
const ROW = {
	id: '35749389-eb52-4e3a-9e82-2cf8807dbd8c',
	from: '0.2.14',
	to: '0.2.15',
	state: 'rolled_back',
	reason: 'not_reconnected',
	started_by: { id: 'adb6799a-a992-4565-acc0-1791697f4dfe', name: 'Joe' },
	started_at: '2026-11-23T10:00:00Z',
	finished_at: '2026-11-23T10:03:00Z'
};

const base = {
	id: HOST_ID,
	name: 'pi3',
	agent_version: '0.2.14',
	latest_agent_version: '0.2.15',
	update_available: true,
	revoked_at: null
};

describe('upgrade offer and words', () => {
	test('the badge offers Upgrade only to org admins on an opted-in host with an update', () => {
		assert.equal(upgradeOffer({ ...base, agent_upgrade_allowed: true, agent_upgrade: null }, true), 'upgrade');
		assert.equal(upgradeOffer({ ...base, agent_upgrade_allowed: false }, true), 'opt-in');
		assert.equal(upgradeOffer({ ...base, agent_upgrade_allowed: true }, false), 'view');
		assert.equal(upgradeOffer({ ...base, update_available: false, agent_upgrade_allowed: true }, true), 'none');
		assert.equal(upgradeOffer({ ...base, revoked_at: '2026-11-23T00:00:00Z', agent_upgrade_allowed: true }, true), 'none');
		const running = parseAgentUpgrade({ ...ROW, state: 'running', reason: null, finished_at: null });
		assert.equal(upgradeOffer({ ...base, agent_upgrade_allowed: true, agent_upgrade: running }, false), 'running');
	});

	test('tooltips: how to opt in, what Upgrade does, who can upgrade', () => {
		assert.match(agentUpdateTooltip('0.2.15', 'opt-in'), /agent_upgrade = true.*runner\.toml.*0\.2\.15 or later/);
		assert.match(agentUpdateTooltip('0.2.15', 'upgrade'), /puts the old agent back by itself/);
		assert.match(agentUpdateTooltip('0.2.15', 'view'), /owner or admin/);
	});

	test('states and reasons in plain words', () => {
		const row = parseAgentUpgrade(ROW)!;
		assert.equal(upgradeLine(row), '0.2.14 → 0.2.15: rolled back (the new agent did not connect)');
		assert.match(upgradeOutcomeText(row), /rolled back: the new agent did not connect\. The previous agent is running\./);
		assert.equal(upgradeOutcomeText({ ...row, state: 'succeeded', reason: null }), 'bot-agent 0.2.15 is running.');
		assert.match(upgradeOutcomeText({ ...row, state: 'unknown', reason: null }), /did not report back/);
		assert.equal(upgradeReasonText('some_new_token'), 'some new token');
		assert.equal(upgradeReasonText(null), null);
	});

	test('the poll goes on while the row is open or unread, up to the limit', () => {
		assert.equal(upgradePollNext({ state: 'running' }, 0, 1000), true);
		assert.equal(upgradePollNext(null, 0, 1000), true);
		assert.equal(upgradePollNext({ state: 'succeeded' }, 0, 1000), false);
		assert.equal(upgradePollNext({ state: 'running' }, 0, UPGRADE_POLL_LIMIT_MS), false);
	});
});

describe('shape', () => {
	test('parseAgentUpgrade keeps the row and drops an odd reason; a bad row is null', () => {
		assert.deepEqual(parseAgentUpgrade(ROW)?.started_by, { id: ROW.started_by.id, name: 'Joe' });
		assert.equal(parseAgentUpgrade({ ...ROW, reason: '<b>x</b>' })?.reason, null);
		assert.equal(parseAgentUpgrade({ ...ROW, state: 'done' }), null);
		assert.equal(parseAgentUpgrade({ ...ROW, to: 'latest' }), null);
		assert.equal(parseAgentUpgrade({ ...ROW, id: 'x' }), null);
		assert.equal(parseAgentUpgrade(null), null);
	});

	test('parseHost reads agent_upgrade_allowed and agent_upgrade as hints', () => {
		const raw = {
			id: HOST_ID,
			org_id: null,
			name: 'pi3',
			node_type: 'pi',
			os: 'linux',
			arch: 'arm64',
			always_on: true,
			agent_version: '0.2.14',
			status: 'online',
			last_seen_at: null,
			enrolled_at: '2026-11-01T00:00:00Z',
			revoked_at: null,
			access_level: null,
			programs: null
		};
		const set = parseHost({ ...raw, agent_upgrade_allowed: true, agent_upgrade: ROW });
		assert.equal(set?.agent_upgrade_allowed, true);
		assert.equal(set?.agent_upgrade?.state, 'rolled_back');
		const old = parseHost(raw);
		assert.equal(old?.agent_upgrade_allowed, false);
		assert.equal(old?.agent_upgrade, null);
		const odd = parseHost({ ...raw, agent_upgrade_allowed: 'yes', agent_upgrade: { state: 'x' } });
		assert.equal(odd?.id, HOST_ID);
		assert.equal(odd?.agent_upgrade_allowed, false);
		assert.equal(odd?.agent_upgrade, null);
	});
});

describe('AgentVersion', () => {
	test('Upgrade for an org admin on an opted-in host; the opt-in tooltip otherwise; a running note', () => {
		const up = render(AgentVersion, { props: { host: { ...base, agent_upgrade_allowed: true }, canUpgrade: true } }).body;
		assert.equal(up.includes('Upgrade'), true);
		assert.equal(up.includes(agentUpdateTooltip('0.2.15', 'upgrade')), true);
		const optIn = render(AgentVersion, { props: { host: { ...base, agent_upgrade_allowed: false }, canUpgrade: true } }).body;
		assert.equal(optIn.includes(agentUpdateTooltip('0.2.15', 'opt-in')), true);
		assert.equal(/>\s*Upgrade\s*</.test(optIn), false);
		const viewer = render(AgentVersion, { props: { host: { ...base, agent_upgrade_allowed: true } } }).body;
		assert.equal(/>\s*Upgrade\s*</.test(viewer), false);
		const running = parseAgentUpgrade({ ...ROW, state: 'running', reason: null, finished_at: null });
		const busy = render(AgentVersion, { props: { host: { ...base, agent_upgrade_allowed: true, agent_upgrade: running }, canUpgrade: true } }).body;
		assert.equal(busy.includes('Upgrading to 0.2.15'), true);
		assert.equal(/>\s*Upgrade\s*</.test(busy), false);
	});
});

describe('/hosts/[id]/agent-upgrade', () => {
	const API = 'https://api.test';
	const URL_ = `http://console.example/hosts/${HOST_ID}/agent-upgrade`;
	function event(input: { user?: boolean; method?: string; body?: string; origin?: string; type?: string; id?: string }) {
		const fake = fakeEvent({ url: URL_, session: TOKEN, user: input.user === false ? null : USER, params: { id: input.id ?? HOST_ID } });
		const headers = new Headers();
		if (input.origin !== undefined) headers.set('origin', input.origin);
		if (input.type !== undefined) headers.set('content-type', input.type);
		(fake.event as { request: Request }).request = new Request(URL_, { method: input.method ?? 'GET', headers, body: input.body });
		return fake.event as never;
	}
	const ok = { method: 'POST', origin: 'http://console.example', type: 'application/json' };

	test('POST: signed in, same origin, JSON, a version; then the API answer', async () => {
		useApi(API);
		const calls = installFetch(() => envelope({ upgrade_id: ROW.id, state: 'running' }, 202));
		try {
			assert.equal((await POST(event({ ...ok, user: false, body: '{"version":"0.2.15"}' }))).status, 401);
			assert.equal((await POST(event({ ...ok, origin: 'http://evil.example', body: '{"version":"0.2.15"}' }))).status, 403);
			assert.equal((await POST(event({ ...ok, type: 'text/plain', body: '{"version":"0.2.15"}' }))).status, 415);
			assert.equal((await POST(event({ ...ok, body: '{"version":"latest"}' }))).status, 400);
			assert.equal((await POST(event({ ...ok, id: 'nope', body: '{"version":"0.2.15"}' }))).status, 404);
			assert.equal(calls.length, 0);
			const res = await POST(event({ ...ok, body: '{"version":"0.2.15"}' }));
			assert.equal(res.status, 202);
			assert.deepEqual(await res.json(), { upgrade_id: ROW.id, state: 'running' });
			assert.equal(calls[0].method, 'POST');
			assert.equal(calls[0].url.pathname, `/v1/hosts/${HOST_ID}/agent-upgrade`);
			assert.equal(calls[0].body, '{"version":"0.2.15"}');
		} finally {
			restoreEnv();
		}
	});

	test('POST: an API refusal keeps its status and message', async () => {
		useApi(API);
		installFetch(() => envelope({ reason: 'busy' }, 409, 'conflict', 'the host is busy with another change; try again in a minute'));
		try {
			const res = await POST(event({ ...ok, body: '{"version":"0.2.15"}' }));
			assert.equal(res.status, 409);
			assert.deepEqual(await res.json(), { message: 'the host is busy with another change; try again in a minute' });
		} finally {
			restoreEnv();
		}
	});

	test('GET: the history, parsed', async () => {
		useApi(API);
		const calls = installFetch(() => envelope({ items: [ROW, { state: 'bad' }] }));
		try {
			const res = await GET(event({}));
			assert.equal(res.status, 200);
			const body = (await res.json()) as { items: unknown[]; message: string | null };
			assert.equal(body.items.length, 1);
			assert.equal(body.message, null);
			assert.equal(calls[0].url.pathname, `/v1/hosts/${HOST_ID}/agent-upgrades`);
			assert.equal((await GET(event({ user: false }))).status, 401);
		} finally {
			restoreEnv();
		}
	});
});
