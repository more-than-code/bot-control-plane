import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure } from '@sveltejs/kit';
import { render } from 'svelte/server';
import DiskUsageCard from '../src/routes/hosts/[id]/DiskUsageCard.svelte';
import { actions as hostActions } from '../src/routes/hosts/[id]/+page.server.ts';
import { DOCKER_USAGE_WAIT_MS, dockerSums, dockerTotalRows, mountsByUse, parseDockerUsage, type DockerUsage } from '../src/lib/disk-usage.ts';
import { parseHostDisks } from '../src/lib/server/host-alerts.ts';
import { envelope, fakeEvent, installFetch, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

// api-v1 v1.59 (spec-disk-usage-breakdown §10 item 4): the Disk usage card, its mounts table and Check Docker usage.

const HOST_ID = '00000000-0000-4000-8000-0000000000d2';

const t = (count: number, active: number, size: number, reclaimable: number) => ({ count, active, size, reclaimable });

const PAYLOAD = {
	totals: { images: t(3, 1, 6_000_000_000, 3_000_000_000), containers: t(2, 1, 40, 10), volumes: t(1, 1, 500, 0), build_cache: t(0, 0, 0, 0) },
	images: [{ ref: 'ghcr.io/o/app:1.2', id: '0123456789ab', size: 2000, containers: 1, labels: { secret: 'x' } }],
	volumes: [{ name: 'pgdata', size: null, in_use: true }],
	containers: [{ name: 'deploybot-api', state: 'running', size: 30 }],
	at: '2026-11-24T10:00:05Z',
	extra: 'dropped'
};

describe('docker usage parser', () => {
	test('reads the summary field by field and drops the rest', () => {
		const got = parseDockerUsage(PAYLOAD);
		assert.ok(got);
		assert.deepEqual(got.images, [{ ref: 'ghcr.io/o/app:1.2', id: '0123456789ab', size: 2000, containers: 1 }]);
		assert.deepEqual(got.volumes, [{ name: 'pgdata', size: null, in_use: true }]);
		assert.equal('extra' in got, false);
		assert.deepEqual(
			dockerTotalRows(got).map((row) => [row.label, row.count, row.reclaimable]),
			[
				['Images', 3, 3_000_000_000],
				['Containers', 2, 10],
				['Volumes', 1, 0],
				['Build cache', 0, 0]
			]
		);
		assert.deepEqual(dockerSums(got), { size: 6_000_000_540, reclaimable: 3_000_000_010 });
	});

	test('missing totals or at, or a negative size, is not usable; lists are capped at ten and strings at 200', () => {
		assert.equal(parseDockerUsage(null), null);
		assert.equal(parseDockerUsage({ ...PAYLOAD, at: 'never' }), null);
		assert.equal(parseDockerUsage({ ...PAYLOAD, totals: { ...PAYLOAD.totals, build_cache: undefined } }), null);
		assert.equal(parseDockerUsage({ ...PAYLOAD, images: 'x' }), null);
		const many = Array.from({ length: 12 }, (_, i) => ({ ref: 'r'.repeat(300) + '\u0007', id: String(i), size: 1, containers: 0 }));
		const got = parseDockerUsage({ ...PAYLOAD, images: [{ ref: 'bad', id: 'x', size: -5, containers: 0 }, ...many] });
		assert.ok(got);
		assert.equal(got.images.length, 10);
		assert.equal(got.images[0].id, '0');
		assert.equal(got.images[0].ref.length, 200);
	});

	test('mounts sort by used percent, highest first, unknown last', () => {
		const disks = [
			{ path: '/b', fstype: 'ext4', used_percent: 20 },
			{ path: '/', fstype: 'ext4', used_percent: 59 },
			{ path: '/x', fstype: 'ext4', used_percent: null },
			{ path: '/a', fstype: 'ext4', used_percent: 20 }
		];
		assert.deepEqual(mountsByUse(disks).map((d) => d.path), ['/', '/a', '/b', '/x']);
	});

	test('/disks keeps the v1.59 sizes; an unusable size is null', () => {
		const got = parseHostDisks({
			items: [{ path: '/', fstype: 'ext4', total: 21_000, used: 12_400, used_percent: 59.05, at: '2026-11-24T10:00:00Z' }, { path: '/d', fstype: 'xfs', total: -1, used: 'x', used_percent: 101, at: 'no' }]
		});
		assert.deepEqual(got, [
			{ path: '/', fstype: 'ext4', total: 21_000, used: 12_400, used_percent: 59.05, at: '2026-11-24T10:00:00Z' },
			{ path: '/d', fstype: 'xfs', total: null, used: null, used_percent: null, at: null }
		]);
	});
});

describe('Disk usage card', () => {
	const usage = parseDockerUsage(PAYLOAD) as DockerUsage;
	const disks = [
		{ path: '/data', fstype: 'xfs', total: 4_294_967_296, used: 1_073_741_824, used_percent: 25, at: null },
		{ path: '/', fstype: 'ext4', total: 21_474_836_480, used: 12_884_901_888, used_percent: 60, at: null }
	];

	test('the mounts table, fullest first; no button for others', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?tab=overview`);
		const html = render(DiskUsageCard, { props: { disks, canCheck: false } }).body;
		assert.match(html, /Disk usage/);
		assert.match(html, />Mount</);
		assert.match(html, />Size</);
		assert.ok(html.indexOf('>/<') < html.indexOf('>/data<'), 'root first');
		assert.match(html, /12\.9 GB/); // 12,884,901,888 bytes, decimal as docker system df
		assert.match(html, /60\.0%/);
		assert.doesNotMatch(html, /Check Docker usage/);
		assert.doesNotMatch(html, /dockerUsage/);
	});

	test('no rows shows No disk data', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(DiskUsageCard, { props: { disks: [] } }).body;
		assert.match(html, /No disk data/);
	});

	test('org admins get the button; a result shows totals, the three lists and the reclaim note', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?tab=overview&range=6h`);
		const html = render(DiskUsageCard, { props: { disks, canCheck: true, usage } }).body;
		assert.match(html, /action="\?tab=overview&amp;range=6h&amp;\/dockerUsage"/);
		assert.match(html, /Check Docker usage/);
		for (const word of ['Images', 'Containers', 'Volumes', 'Build cache', 'Reclaimable', 'ghcr.io/o/app:1.2', '0123456789ab', 'pgdata', 'deploybot-api']) {
			assert.ok(html.includes(word), word);
		}
		assert.match(html, /Docker uses 6 GB, of which 3 GB can be reclaimed/); // decimal
		assert.match(html, /docker image prune<\/code>/);
		assert.match(html, /Check a volume before removing it: it may hold data\./);
	});

	test('each mount shows when it was sampled', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(DiskUsageCard, { props: { disks: [{ ...disks[0], at: '2026-11-24T10:00:00Z' }] } }).body;
		assert.match(html, />As of</);
		assert.match(html, /<time datetime="2026-11-24T10:00:00Z"/);
	});

	test('a revoked host has no button; an offline host has it disabled with the reason', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const revoked = render(DiskUsageCard, { props: { disks, canCheck: true, revoked: true, hostStatus: 'revoked' } }).body;
		assert.doesNotMatch(revoked, /Check Docker usage/);
		const offline = render(DiskUsageCard, { props: { disks, canCheck: true, hostStatus: 'offline' } }).body;
		assert.match(offline, /data-docker-offline/);
		assert.match(offline, /<button[^>]*disabled[^>]*>(<!---->)?Check Docker usage/);
		assert.doesNotMatch(offline, /dockerUsage"/);
		assert.match(offline, /This host is offline; Docker usage needs it online\./);
	});

	test('an error is a Notice', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(DiskUsageCard, { props: { disks, canCheck: true, message: 'Docker usage needs bot-agent 0.2.17 or later on this host.' } }).body;
		assert.match(html, /role="alert"[\s\S]*Docker usage needs bot-agent 0\.2\.17/);
	});

	test('the Overview tab places the card after the Snapshot card', () => {
		const overview = readFileSync(new URL('../src/routes/hosts/[id]/OverviewTab.svelte', import.meta.url), 'utf8');
		assert.ok(overview.indexOf('<Card.Title>Snapshot</Card.Title>') < overview.indexOf('<DiskUsageCard'));
	});
});

describe('dockerUsage action', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	function event() {
		return fakeEvent({ url: `http://console.example/hosts/${HOST_ID}?/dockerUsage`, method: 'POST', session: TOKEN, user: USER, form: {}, params: { id: HOST_ID } }).event as never;
	}

	test('posts docker-usage and returns the parsed summary', async () => {
		const calls = installFetch(() => envelope(PAYLOAD));
		const got = (await hostActions.dockerUsage(event())) as { dockerUsage: DockerUsage };
		assert.equal(calls.length, 1);
		assert.equal(calls[0].method, 'POST');
		assert.equal(calls[0].url.pathname, `/v1/hosts/${HOST_ID}/docker-usage`);
		assert.equal(got.dockerUsage.images[0].ref, 'ghcr.io/o/app:1.2');
	});

	test('an API refusal keeps its status and message for the card', async () => {
		installFetch(() => envelope(null, 409, 'docker_usage_disabled', "Docker usage needs images or edge turned on in this host's runner.toml"));
		const got = await hostActions.dockerUsage(event());
		assert.ok(isActionFailure(got));
		assert.equal(got.status, 409);
		assert.equal((got.data as { dockerUsageMessage?: string } | undefined)?.dockerUsageMessage, "Docker usage needs images or edge turned on in this host's runner.toml");
	});

	test('host_offline reads as the host being offline', async () => {
		installFetch(() => envelope(null, 503, 'host_offline', 'host unavailable'));
		const got = await hostActions.dockerUsage(event());
		assert.ok(isActionFailure(got));
		assert.equal(got.status, 503);
		assert.equal((got.data as { dockerUsageMessage?: string } | undefined)?.dockerUsageMessage, 'This host is offline; Docker usage needs it online.');
	});

	test('the API call waits 55 s', () => {
		const action = readFileSync(new URL('../src/lib/server/disk-usage.ts', import.meta.url), 'utf8');
		assert.match(action, /docker-usage`, \{ method: 'POST' \}, DOCKER_USAGE_WAIT_MS\)/);
		assert.equal(DOCKER_USAGE_WAIT_MS, 55_000);
	});

	test('an unusable answer is 502', async () => {
		installFetch(() => envelope({ totals: {} }));
		const got = await hostActions.dockerUsage(event());
		assert.ok(isActionFailure(got));
		assert.equal(got.status, 502);
	});
});

describe('decimal bytes on the disk usage card (as docker system df)', () => {
	test('1000-based units', async () => {
		const { formatDecimalBytes } = await import('../src/lib/format.ts');
		assert.equal(formatDecimalBytes(999), '999 B');
		assert.equal(formatDecimalBytes(6_100_000_000), '6.1 GB');
		assert.equal(formatDecimalBytes(21_001_486_336), '21 GB');
		assert.equal(formatDecimalBytes(1_500_000), '1.5 MB');
		assert.equal(formatDecimalBytes(-1), 'n/a');
		assert.equal(formatDecimalBytes(null), 'n/a');
	});

	test('a value that rounds up to 1000 moves to the next unit', async () => {
		const { formatDecimalBytes } = await import('../src/lib/format.ts');
		assert.equal(formatDecimalBytes(999_999), '1 MB');
		assert.equal(formatDecimalBytes(999_499), '999 kB');
		assert.equal(formatDecimalBytes(999_999_999_999), '1 TB');
	});
});
