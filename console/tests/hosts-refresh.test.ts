// The project and org Hosts tabs re-read their hosts while open (startHostsRefresh, the hosts.json endpoints).
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, test } from 'node:test';
import { fetchHosts, HOSTS_REFRESH_MS, startHostsRefresh } from '../src/lib/hosts-refresh.ts';
import type { VisibilityDoc } from '../src/lib/visibility.ts';
import { GET as orgHostsGET } from '../src/routes/orgs/[id]/hosts.json/+server.ts';
import { GET as projectHostsGET } from '../src/routes/projects/[id]/hosts.json/+server.ts';
import { envelope, fakeEvent, installFetch, ORG, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

const HOST_ID = '00000000-0000-4000-8000-0000000000d1';
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

class FakeDoc implements VisibilityDoc {
	visibilityState: DocumentVisibilityState = 'visible';
	listeners = new Set<() => void>();
	addEventListener(_type: 'visibilitychange', listener: () => void) {
		this.listeners.add(listener);
	}
	removeEventListener(_type: 'visibilitychange', listener: () => void) {
		this.listeners.delete(listener);
	}
	set(state: DocumentVisibilityState) {
		this.visibilityState = state;
		for (const listener of this.listeners) listener();
	}
}

/** Lets rejected refresh promises run their handlers. */
async function settle() {
	for (let i = 0; i < 5; i += 1) await Promise.resolve();
}

test('the interval is 30 seconds', () => {
	assert.equal(HOSTS_REFRESH_MS, 30_000);
});

/** Advances the fake clock by `ms` in 30 s steps, letting each refresh finish before the next tick. */
async function advance(t: { mock: { timers: { tick: (ms: number) => void } } }, ms: number) {
	for (let left = ms; left > 0; left -= Math.min(left, HOSTS_REFRESH_MS)) {
		t.mock.timers.tick(Math.min(left, HOSTS_REFRESH_MS));
		await settle();
	}
}

test('refreshes every 30 s while visible and not at start', async (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	const stop = startHostsRefresh({ doc, refresh: () => void (calls += 1) });
	assert.equal(calls, 0);
	t.mock.timers.tick(29_999);
	assert.equal(calls, 0);
	t.mock.timers.tick(1);
	assert.equal(calls, 1);
	await settle();
	await advance(t, 60_000);
	assert.equal(calls, 3);
	stop();
});

test('no refresh while hidden; becoming visible refreshes once at once, then every 30 s again', async (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	const stop = startHostsRefresh({ doc, refresh: () => void (calls += 1) });
	doc.set('hidden');
	await advance(t, 120_000);
	assert.equal(calls, 0);
	doc.set('visible');
	assert.equal(calls, 1);
	await settle();
	t.mock.timers.tick(29_999);
	assert.equal(calls, 1);
	t.mock.timers.tick(1);
	assert.equal(calls, 2);
	stop();
});

test('a tab hidden at start does not refresh until it is shown', (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	doc.visibilityState = 'hidden';
	let calls = 0;
	const stop = startHostsRefresh({ doc, refresh: () => void (calls += 1) });
	t.mock.timers.tick(90_000);
	assert.equal(calls, 0);
	doc.set('visible');
	assert.equal(calls, 1);
	stop();
});

test('nothing fires after stop, from the timer or the document', (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	const stop = startHostsRefresh({ doc, refresh: () => void (calls += 1) });
	stop();
	assert.equal(doc.listeners.size, 0);
	t.mock.timers.tick(120_000);
	doc.set('hidden');
	doc.set('visible');
	assert.equal(calls, 0);
});

test('a throwing or rejecting refresh does not stop later calls', async (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	const stop = startHostsRefresh({
		doc,
		refresh: () => {
			calls += 1;
			if (calls === 1) throw new Error('boom');
			return Promise.reject(new Error('rejected'));
		}
	});
	await advance(t, 90_000);
	assert.equal(calls, 3);
	stop();
});

test('uses the given timer and interval', () => {
	const doc = new FakeDoc();
	const started: number[] = [];
	let stops = 0;
	let tick: () => void = () => {};
	const stop = startHostsRefresh({
		doc,
		refresh: () => {},
		ms: 5000,
		every: (fn, ms) => {
			started.push(ms);
			tick = fn;
			return () => void (stops += 1);
		}
	});
	assert.deepEqual(started, [5000]);
	tick();
	doc.set('hidden');
	assert.equal(stops, 1);
	doc.set('visible');
	assert.deepEqual(started, [5000, 5000]);
	stop();
	assert.equal(stops, 2);
});

test('a tick while a refresh is pending is skipped, and so is the run on becoming visible', async (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	let finish: () => void = () => {};
	const stop = startHostsRefresh({
		doc,
		refresh: () => {
			calls += 1;
			return new Promise<void>((resolve) => (finish = resolve));
		}
	});
	t.mock.timers.tick(30_000);
	t.mock.timers.tick(30_000);
	t.mock.timers.tick(30_000);
	assert.equal(calls, 1);
	doc.set('hidden');
	doc.set('visible');
	assert.equal(calls, 1);
	finish();
	await settle();
	t.mock.timers.tick(30_000);
	assert.equal(calls, 2);
	stop();
});

test('a visible event while already visible does not double the timer', async (t) => {
	t.mock.timers.enable({ apis: ['setInterval'] });
	const doc = new FakeDoc();
	let calls = 0;
	const stop = startHostsRefresh({ doc, refresh: () => void (calls += 1) });
	doc.set('visible');
	await settle();
	doc.set('visible');
	await settle();
	assert.equal(calls, 2);
	await advance(t, 30_000);
	assert.equal(calls, 3);
	await advance(t, 30_000);
	assert.equal(calls, 4);
	stop();
});

test('fetchHosts reads { hosts } and throws on a failed answer or an odd body', async () => {
	const answer = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status });
	assert.deepEqual(await fetchHosts(answer(200, { hosts: [{ id: 'a' }] }) as never, '/x'), [{ id: 'a' }]);
	await assert.rejects(fetchHosts(answer(503, { message: 'Hosts could not be loaded.' }) as never, '/x'));
	await assert.rejects(fetchHosts(answer(200, { hosts: 'no' }) as never, '/x'));
});

test('both pages start the refresh only in the browser while Hosts shows, from hosts.json', () => {
	for (const [file, url] of [
		['src/routes/projects/[id]/+page.svelte', '/projects/${data.project.id}/hosts.json'],
		['src/routes/orgs/[id]/+page.svelte', '/orgs/${data.org.id}/hosts.json']
	] as const) {
		const source = readFileSync(new URL(`../${file}`, import.meta.url), 'utf8');
		assert.match(source, /import \{ fetchHosts, startHostsRefresh \} from '\$lib\/hosts-refresh';/, file);
		assert.match(source, /const hostsOpen = \$derived\(scope === 'hosts' && /, file);
		assert.match(source, /if \(!browser \|\| !hostsOpen \|\| /, file);
		assert.equal(source.includes(url), true, file);
		assert.equal(source.includes('invalidate('), false, file);
	}
});

const hostsEvent = (path: string, id: string, user = true) =>
	fakeEvent({ url: `http://console.example${path}`, session: TOKEN, user: user ? USER : null, projects: [PROJECT], orgs: [ORG], params: { id } })
		.event as never;

after(() => restoreEnv());

test('hosts.json returns the parsed API host list, no-store, for a project and an org', async () => {
	useApi('https://api.test');
	for (const [handler, id, apiPath] of [
		[projectHostsGET, PROJECT.id, `/v1/projects/${PROJECT.id}/hosts`],
		[orgHostsGET, ORG.id, `/v1/orgs/${ORG.id}/hosts`]
	] as const) {
		const calls = installFetch(() => envelope({ items: [RAW_HOST, { id: 'bad' }], next_cursor: null }));
		const response = await handler(hostsEvent('/x/hosts.json', id));
		assert.equal(response.status, 200);
		assert.equal(response.headers.get('cache-control'), 'no-store');
		const body = (await response.json()) as { hosts: { id: string; name: string }[] };
		assert.deepEqual(body.hosts.map((host) => [host.id, host.name]), [[HOST_ID, 'daikou']]);
		assert.equal(calls[0]?.url.pathname, apiPath);
		assert.equal(calls[0]?.authorization, `Bearer ${TOKEN}`);
	}
});

test('hosts.json: an API failure is an error status with a fixed message and no API detail', async () => {
	useApi('https://api.test');
	for (const [handler, id] of [
		[projectHostsGET, PROJECT.id],
		[orgHostsGET, ORG.id]
	] as const) {
		installFetch(() => envelope(null, 503, 'unavailable', 'database down at 10.0.0.5'));
		const failed = await handler(hostsEvent('/x/hosts.json', id));
		assert.equal(failed.status, 503);
		assert.deepEqual(await failed.json(), { message: 'Hosts could not be loaded.' });

		installFetch(() => envelope(null, 403, 'forbidden', 'not an org admin'));
		const refused = await handler(hostsEvent('/x/hosts.json', id));
		assert.equal(refused.status, 403);
		assert.deepEqual(await refused.json(), { message: 'Hosts could not be loaded.' });
	}
});

test('hosts.json refuses a signed-out caller and a bad id without calling the API', async () => {
	useApi('https://api.test');
	const calls = installFetch(() => envelope({ items: [], next_cursor: null }));
	assert.equal((await projectHostsGET(hostsEvent('/x/hosts.json', PROJECT.id, false))).status, 401);
	assert.equal((await orgHostsGET(hostsEvent('/x/hosts.json', 'not-a-uuid'))).status, 404);
	assert.equal(calls.length, 0);
});
