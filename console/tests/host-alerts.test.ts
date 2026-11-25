import assert from 'node:assert/strict';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import HostAlertsSection from '../src/routes/hosts/[id]/HostAlertsSection.svelte';
import OverviewTab from '../src/routes/hosts/[id]/OverviewTab.svelte';
import { actions as hostActions, load as hostLoad } from '../src/routes/hosts/[id]/+page.server.ts';
import { alertSettingsFromForm, alertsStagedFromUrl, DEFAULT_ALERT_SETTINGS } from '../src/lib/host-alerts.ts';
import { METRIC_SPECS, overviewHref, parseDiskPath, withAvailablePercent } from '../src/lib/host-view.ts';
import { problemKindOf } from '../src/lib/notifications.ts';
import { parseAlertSettings, parseHostDisks } from '../src/lib/server/host-alerts.ts';
import { envelope, fakeEvent, installFetch, ORG, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';
import type { Host, Org } from '../src/lib/api-types.ts';

// api-v1 v1.49 (spec-host-resource-alerts §6 and §8): the Alerts card, the memory chart's "Free to use" line and the
// disk mount picker.

const HOST_ID = '00000000-0000-4000-8000-0000000000d1';

const HOST = {
	id: HOST_ID,
	org_id: ORG.id,
	name: 'pi1',
	node_type: 'pi',
	os: 'linux',
	arch: 'arm64',
	always_on: true,
	agent_version: '0.2.11',
	status: 'online',
	last_seen_at: '2026-11-23T00:00:00Z',
	enrolled_at: '2026-11-01T00:00:00Z',
	revoked_at: null,
	access_level: null,
	access: null
};

function form(values: Record<string, string>): FormData {
	const data = new FormData();
	for (const [k, v] of Object.entries(values)) data.set(k, v);
	return data;
}

describe('alert settings form and parsers', () => {
	test('empty fields are the default; the switch present is on', () => {
		const got = alertSettingsFromForm(form({ alerts_enabled: '1', mem_min_available_percent: '', disk_max_used_percent: ' 95 ', cpu_max_busy_percent: '' }));
		assert.deepEqual(got, { ok: true, settings: { alerts_enabled: true, mem_min_available_percent: null, disk_max_used_percent: 95, cpu_max_busy_percent: null } });
		const off = alertSettingsFromForm(form({ mem_min_available_percent: '10' }));
		assert.deepEqual(off, { ok: true, settings: { ...DEFAULT_ALERT_SETTINGS, alerts_enabled: false, mem_min_available_percent: 10 } });
	});

	test('out of range or not a whole number is refused with the field named', () => {
		for (const [name, value] of [
			['mem_min_available_percent', '4'],
			['mem_min_available_percent', '51'],
			['disk_max_used_percent', '100'],
			['cpu_max_busy_percent', '49'],
			['cpu_max_busy_percent', '80.5'],
			['cpu_max_busy_percent', 'abc']
		]) {
			const got = alertSettingsFromForm(form({ alerts_enabled: '1', [name]: value }));
			assert.equal(got.ok, false, `${name}=${value}`);
			if (!got.ok) assert.match(got.message, /enter a whole number from \d+ to \d+, or leave it empty for the default\./);
		}
	});

	test('the API shapes are allowlisted', () => {
		assert.deepEqual(parseAlertSettings({ alerts_enabled: true, mem_min_available_percent: null, disk_max_used_percent: 95, cpu_max_busy_percent: null, extra: 1 }), {
			alerts_enabled: true,
			mem_min_available_percent: null,
			disk_max_used_percent: 95,
			cpu_max_busy_percent: null
		});
		assert.equal(parseAlertSettings({ alerts_enabled: 'yes' }), null);
		assert.equal(parseAlertSettings({ alerts_enabled: true, disk_max_used_percent: 100 }), null);
		const none = { total: null, used: null, used_percent: null, at: null };
		assert.deepEqual(parseHostDisks({ items: [{ path: '/', fstype: 'ext4' }, { path: 'bad\u0007', fstype: 'x' }, { path: '/data', fstype: 'xfs' }], next_cursor: null }), [
			{ path: '/', fstype: 'ext4', ...none },
			{ path: '/data', fstype: 'xfs', ...none }
		]);
	});

	test('available percent comes from available and total; disk path and links', () => {
		const series = withAvailablePercent([
			{ name: 'used_percent', points: [{ t: 'a', value: 60 }, { t: 'b', value: null }] },
			{ name: 'available', points: [{ t: 'a', value: 400 }, { t: 'b', value: 10 }] },
			{ name: 'total', points: [{ t: 'a', value: 1000 }, { t: 'b', value: null }] }
		]);
		assert.deepEqual(series.map((s) => s.name), ['used_percent', 'available_percent']);
		assert.deepEqual(series[1].points, [{ t: 'a', value: 40 }, { t: 'b', value: null }]);
		assert.equal(parseDiskPath(null), '/');
		assert.equal(parseDiskPath('/data'), '/data');
		assert.equal(parseDiskPath('a\nb'), '/');
		assert.equal(overviewHref('6h', '/'), '?tab=overview&range=6h');
		assert.equal(overviewHref('1h', '/data'), '?tab=overview&range=1h&disk=%2Fdata');
	});

	test('a host_resource bell item is told apart by its range link', () => {
		const base = { key: 'problem:x', kind: 'problem' as const, title: 't', detail: 'd', at: '2026-11-23T00:00:00Z', read: false };
		assert.equal(problemKindOf({ ...base, href: `/hosts/${HOST_ID}?range=1h` }), 'host_resource');
		assert.equal(problemKindOf({ ...base, href: `/hosts/${HOST_ID}?range=1h&disk=%2Fdata` }), 'host_resource');
		assert.equal(problemKindOf({ ...base, href: `/hosts/${HOST_ID}` }), 'host_offline');
		assert.equal(problemKindOf({ ...base, href: `/hosts/${HOST_ID}?tab=files&files.name=x` }), 'host_file');
	});
});

describe('Alerts card', () => {
	test('admins get a form with the switch, three fields and Save', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(HostAlertsSection, {
			props: { settings: { ...DEFAULT_ALERT_SETTINGS, disk_max_used_percent: 95 }, canEdit: true, actionMessage: 'disk_max_used_percent must be 50–99 or null' }
		}).body;
		assert.match(html, /<h2[^>]*>Alerts<\/h2>/);
		assert.match(html, /saveAlerts/);
		assert.match(html, /name="mem_min_available_percent"/);
		assert.match(html, /name="disk_max_used_percent"[^>]*value="95"|value="95"[^>]*name="disk_max_used_percent"/);
		assert.match(html, /placeholder="Default \(15\)"/);
		assert.match(html, /role="switch"[^>]*aria-checked="true"|aria-checked="true"[^>]*role="switch"/);
		assert.match(html, /aria-describedby="alerts-help alerts-error"/);
		assert.match(html, /name="disk_max_used_percent"[^>]*aria-invalid="true"|aria-invalid="true"[^>]*name="disk_max_used_percent"/);
		assert.match(html, />Save</);
		assert.match(html, /must be 50–99 or null/);
	});

	test('alerts off renders the switch off', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(HostAlertsSection, { props: { settings: { ...DEFAULT_ALERT_SETTINGS, alerts_enabled: false }, canEdit: true } }).body;
		assert.match(html, /aria-checked="false"/);
		assert.doesNotMatch(html, /name="alerts_enabled"[^>]*checked/);
		assert.match(html, /<label[^>]*for="alerts-enabled"[^>]*>(<!---->)?Alerts(<!---->)?<\/label>/);
	});

	test('others see the values read-only', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(HostAlertsSection, { props: { settings: { ...DEFAULT_ALERT_SETTINGS, alerts_enabled: false, cpu_max_busy_percent: 80 }, canEdit: false } }).body;
		assert.doesNotMatch(html, /<form/);
		assert.doesNotMatch(html, />Save</);
		assert.match(html, /data-alerts-readonly/);
		assert.match(html, /Off/);
		assert.match(html, /Default \(15 %\)/);
		assert.match(html, /80 %/);
	});

	test('a failed read shows its message', () => {
		const html = render(HostAlertsSection, { props: { settings: null, canEdit: true, loadMessage: 'The API is unavailable.' } }).body;
		assert.match(html, /The API is unavailable\./);
		assert.doesNotMatch(html, /<form/);
	});
});

describe('alert deep link (api-v1 v1.60)', () => {
	const base = { ...DEFAULT_ALERT_SETTINGS, disk_max_used_percent: 95 };
	const staged = (q: string) => alertsStagedFromUrl(new URLSearchParams(q), base);

	test('stages only the fields the link names, with default for null', () => {
		const out = staged('alerts.enabled=off&alerts.disk=default&alerts.cpu=80');
		assert.deepEqual(out.values, { alerts_enabled: false, mem_min_available_percent: null, disk_max_used_percent: null, cpu_max_busy_percent: 80 });
		assert.deepEqual(out.changed, ['alerts_enabled', 'disk_max_used_percent', 'cpu_max_busy_percent']);
		assert.deepEqual(out.changes, ['Alerts on → off', 'Disk used at or above 95 % → Default (90 %)', 'CPU busy at or above Default (90 %) → 80 %']);
		assert.equal(out.dropped, 0);
	});

	test('drops out-of-range, unreadable and repeated values; a value equal to the stored one is no change', () => {
		const out = staged('alerts.enabled=maybe&alerts.mem=4&alerts.cpu=8.5&alerts.disk=99&alerts.disk=98');
		assert.deepEqual(out.values, base);
		assert.equal(out.dropped, 4);
		assert.deepEqual(out.changes, []);
		assert.deepEqual(staged('alerts.disk=95').changed, []);
		assert.deepEqual(staged('').values, base);
	});

	test('an admin sees the staged values, the notice and a Save that drops the link', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?alerts.enabled=off&alerts.disk=99#alerts`);
		const html = render(HostAlertsSection, { props: { settings: base, canEdit: true } }).body;
		assert.match(html, /data-alerts-staged/);
		assert.match(html, /A link filled in this change: Alerts on → off; Disk used at or above 95 % → 99 %\. Nothing is saved until you tap Save\./);
		assert.match(html, /name="disk_max_used_percent"[^>]*value="99"|value="99"[^>]*name="disk_max_used_percent"/);
		assert.match(html, /aria-checked="false"/);
		assert.equal((html.match(/data-alerts-changed/g) ?? []).length, 2);
		assert.match(html, /action="\?\/saveAlerts"/);
		assert.doesNotMatch(html, /action="[^"]*alerts\.disk/);
	});

	test('a bad value in the link is reported and left out', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?alerts.mem=1`);
		const html = render(HostAlertsSection, { props: { settings: base, canEdit: true } }).body;
		assert.match(html, /1 value in the link was not valid and was left out\./);
		assert.doesNotMatch(html, /data-alerts-staged/);
	});

	test('someone who cannot save sees the stored values and one line, nothing staged', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?alerts.enabled=off`);
		const html = render(HostAlertsSection, { props: { settings: base, canEdit: false } }).body;
		assert.doesNotMatch(html, /<form/);
		assert.match(html, /This link proposes an alert change; an org owner or admin can apply it\./);
		assert.match(html, /<dd>On<\/dd>/);
		assert.doesNotMatch(html, /data-alerts-staged/);
	});

	test('without a link nothing is staged', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}`);
		const html = render(HostAlertsSection, { props: { settings: base, canEdit: false } }).body;
		assert.doesNotMatch(html, /data-alerts-link-readonly/);
	});
});

describe('Overview disk picker and memory chart', () => {
	test('more than one mount shows the picker and the mount in the disk chart title', () => {
		setPageUrl(`http://console.example/hosts/${HOST_ID}?tab=overview&disk=%2Fdata`);
		const props = { host: HOST as unknown as Host, range: '1h' as const, metrics: [], snapshot: null };
		const one = render(OverviewTab, { props: { ...props, disks: [{ path: '/', fstype: 'ext4' }], diskPath: '/' } }).body;
		assert.doesNotMatch(one, /data-disk-picker/);
		const two = render(OverviewTab, {
			props: { ...props, disks: [{ path: '/', fstype: 'ext4' }, { path: '/data', fstype: 'xfs' }], diskPath: '/data' }
		}).body;
		assert.match(two, /data-disk-picker/);
		assert.match(two, /href="\?tab=overview&amp;range=1h&amp;disk=%2Fdata"[^>]*aria-current="page"|aria-current="page"[^>]*href="\?tab=overview&amp;range=1h&amp;disk=%2Fdata"/);
		assert.match(two, /Disk \/data/);
	});
});

describe('host page load and saveAlerts', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	async function load(url: string, orgs: Org[]) {
		const calls = installFetch((call) => {
			const path = call.url.pathname;
			if (path === `/v1/hosts/${HOST_ID}`) return envelope(HOST);
			if (path.endsWith('/alert-settings')) return envelope({ alerts_enabled: true, mem_min_available_percent: 10, disk_max_used_percent: null, cpu_max_busy_percent: null });
			if (path.endsWith('/disks')) return envelope({ items: [{ path: '/', fstype: 'ext4' }, { path: '/data', fstype: 'xfs' }] });
			if (path.endsWith('/metrics')) {
				const m = call.url.searchParams.get('m');
				const pt = (v: number) => [['2026-11-23T00:00:00Z', v]];
				const series =
					m === 'mem'
						? [
								{ name: 'used_percent', unit: '%', points: pt(60) },
								{ name: 'used', unit: 'bytes', points: pt(600) },
								{ name: 'total', unit: 'bytes', points: pt(1000) },
								{ name: 'available', unit: 'bytes', points: pt(400) }
							]
						: [{ name: 'used_percent', unit: '%', points: pt(1) }];
				return envelope({ m, from: 'x', to: 'y', step: 60, series });
			}
			return envelope(null, 404, 'not_found', 'not found');
		});
		const data = (await hostLoad(fakeEvent({ url, session: TOKEN, user: USER, orgs, params: { id: HOST_ID } }).event as never)) as Record<string, unknown>;
		return { calls, data };
	}

	test('alerts for every viewer, editable for org owners and admins; disk path and memory available', async () => {
		const admin = await load(`http://console.example/hosts/${HOST_ID}?tab=overview&disk=%2Fdata`, [ORG]);
		assert.deepEqual(admin.data.alerts, { alerts_enabled: true, mem_min_available_percent: 10, disk_max_used_percent: null, cpu_max_busy_percent: null });
		assert.equal(admin.data.canEditAlerts, true);
		assert.equal(admin.data.diskPath, '/data');
		assert.equal((admin.data.disks as unknown[]).length, 2);
		const disk = admin.calls.find((call) => call.url.pathname.endsWith('/metrics') && call.url.searchParams.get('m') === 'disk');
		assert.equal(disk?.url.searchParams.get('path'), '/data');
		const mem = (admin.data.metrics as Array<{ m: string; series: Array<{ name: string; points: Array<{ value: number | null }> }> }>).find((c) => c.m === 'mem');
		assert.deepEqual(mem?.series.map((s) => s.name), ['used_percent', 'available_percent']);
		assert.equal(mem?.series[1].points[0].value, 40);

		const member = await load(`http://console.example/hosts/${HOST_ID}?tab=processes`, [{ ...ORG, role: 'member' }]);
		assert.equal(member.data.canEditAlerts, false);
		assert.ok(member.data.alerts);
		assert.equal(member.calls.some((call) => call.url.pathname.endsWith('/disks')), false);
	});

	test('a host with exactly one mount charts it without ?disk=', async () => {
		const calls = installFetch((call) => {
			const path = call.url.pathname;
			if (path === `/v1/hosts/${HOST_ID}`) return envelope(HOST);
			if (path.endsWith('/alert-settings')) return envelope({ alerts_enabled: true, mem_min_available_percent: null, disk_max_used_percent: null, cpu_max_busy_percent: null });
			if (path.endsWith('/disks')) return envelope({ items: [{ path: '/srv', fstype: 'xfs' }] });
			if (path.endsWith('/metrics')) return envelope({ m: call.url.searchParams.get('m'), from: 'x', to: 'y', step: 60, series: [] });
			return envelope(null, 404, 'not_found', 'not found');
		});
		const data = (await hostLoad(fakeEvent({ url: `http://console.example/hosts/${HOST_ID}`, session: TOKEN, user: USER, orgs: [ORG], params: { id: HOST_ID } }).event as never)) as Record<string, unknown>;
		assert.equal(data.diskPath, '/srv');
		const disk = calls.find((call) => call.url.pathname.endsWith('/metrics') && call.url.searchParams.get('m') === 'disk');
		assert.equal(disk?.url.searchParams.get('path'), '/srv');
	});

	test('saveAlerts sends the four fields and redirects; a bad field never reaches the API', async () => {
		const calls = installFetch(() => envelope({ alerts_enabled: true, mem_min_available_percent: null, disk_max_used_percent: 95, cpu_max_busy_percent: null }));
		await assert.rejects(
			async () =>
				hostActions.saveAlerts(
					fakeEvent({
						url: `http://console.example/hosts/${HOST_ID}?/saveAlerts`,
						method: 'POST',
						session: TOKEN,
						user: USER,
						form: { alerts_enabled: '1', mem_min_available_percent: '', disk_max_used_percent: '95', cpu_max_busy_percent: '' },
						params: { id: HOST_ID }
					}).event as never
				),
			(err: unknown) => isRedirect(err) && err.status === 303
		);
		assert.equal(calls.length, 1);
		assert.equal(calls[0].method, 'PUT');
		assert.equal(calls[0].url.pathname, `/v1/hosts/${HOST_ID}/alert-settings`);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { alerts_enabled: true, mem_min_available_percent: null, disk_max_used_percent: 95, cpu_max_busy_percent: null });

		const none = installFetch(() => envelope(null));
		const bad = await hostActions.saveAlerts(
			fakeEvent({
				url: `http://console.example/hosts/${HOST_ID}?/saveAlerts`,
				method: 'POST',
				session: TOKEN,
				user: USER,
				form: { alerts_enabled: '1', cpu_max_busy_percent: '101' },
				params: { id: HOST_ID }
			}).event as never
		);
		assert.ok(isActionFailure(bad));
		assert.equal(none.length, 0);

		installFetch(() => envelope(null, 403, 'forbidden', 'forbidden'));
		const refused = await hostActions.saveAlerts(
			fakeEvent({
				url: `http://console.example/hosts/${HOST_ID}?/saveAlerts`,
				method: 'POST',
				session: TOKEN,
				user: USER,
				form: { alerts_enabled: '1' },
				params: { id: HOST_ID }
			}).event as never
		);
		assert.ok(isActionFailure(refused));
		assert.equal((refused as unknown as { status: number; data: { alertsMessage: string } }).status, 403);
		assert.equal((refused as unknown as { data: { alertsMessage: string } }).data.alertsMessage, 'forbidden');
	});
});

describe('memory chart labels (Joe 2026-11-24: "Available 92.9%" read as usage)', () => {
	test('Used is first and warm; the second line is "Free to use" in green', () => {
		const mem = METRIC_SPECS.find((spec) => spec.m === 'mem');
		assert.ok(mem);
		assert.deepEqual(
			mem.series.map((s) => [s.name, s.label, s.color]),
			[
				['used_percent', 'Used', 'var(--chart-3)'],
				['available_percent', 'Free to use', 'var(--chart-5)']
			]
		);
	});
});
