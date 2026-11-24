import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import { render } from 'svelte/server';
import type { Notification } from '../src/lib/api-types.ts';
import { formatWhen } from '../src/lib/format.ts';
import { RELEASE_DAY, releaseDateOnly, releaseDateTitle, rowTime, rowTitle } from '../src/lib/notifications.ts';
import { parseNotification } from '../src/lib/server/notifications.ts';
import NotificationsList from '../src/lib/components/NotificationsList.svelte';

// DEF-20261124-bell-release-rows-sink-below-today-s-ite-0kny: a release's `at` is only its CHANGELOG date (midnight
// UTC), so its row shows that date, not an age counted from midnight; and the time stays visible when the detail is
// long, because it sits outside the truncated detail text.

const RELEASE: Notification = {
	key: 'release:0.2.47',
	kind: 'release',
	title: 'Deploybot v0.2.47 is out',
	detail: 'A deploy the host refuses as invalid names the spec field that failed (needs bot-agent 0.2.16), and more.',
	href: '/whats-new#v0.2.47',
	at: '2026-11-24T00:00:00Z',
	read: false
};
const UPDATE: Notification = {
	key: 'host_update:00000000-0000-4000-8000-0000000000a1:0.2.16',
	kind: 'host_update',
	title: 'Update available on pi1',
	detail: 'bot-agent 0.2.16 (this host: 0.2.14)',
	href: '/hosts/00000000-0000-4000-8000-0000000000a1',
	at: '2026-11-24T00:29:00Z',
	read: false
};
const RESOLVED: Notification = {
	key: 'problem:00000000-0000-4000-8000-0000000000f3',
	kind: 'problem',
	title: 'migrate is running on us1-portkey-io',
	detail: 'The next deploy succeeded.',
	href: '/projects/00000000-0000-4000-8000-000000000101?tab=catalog',
	at: '2026-11-24T00:32:00Z',
	read: true,
	level: 'heads_up',
	state: 'resolved'
};

const NOW = Date.parse('2026-11-24T02:30:00Z');

describe('bell row time', () => {
	test('a release shows its date, in UTC, whatever the viewer zone and time', () => {
		assert.equal(rowTime(RELEASE, NOW), '24 Nov');
		assert.equal(rowTime(RELEASE, Date.parse('2026-12-30T23:00:00Z')), '24 Nov');
		assert.equal(rowTime({ ...RELEASE, at: '2026-11-24T23:59:59Z' }, NOW), '24 Nov');
		assert.equal(rowTime({ ...RELEASE, at: 'nope' }, NOW), 'n/a');
		// Pinned to UTC, so a viewer (or test machine) west of UTC never sees 23 Nov.
		assert.equal(RELEASE_DAY.resolvedOptions().timeZone, 'UTC');
	});

	test('the tooltip of a release row is its date only, with no invented clock time', () => {
		assert.equal(releaseDateTitle(RELEASE), 'Released 24 Nov 2026');
		assert.equal(releaseDateTitle({ ...RELEASE, at: 'nope' }), undefined);
		const html = render(NotificationsList, { props: { items: [RELEASE] } }).body;
		assert.match(html, /<time datetime="2026-11-24T00:00:00Z" title="Released 24 Nov 2026"/);
	});

	test('other rows keep their age', () => {
		assert.equal(rowTime(UPDATE, NOW), '2 h ago');
		assert.equal(rowTime(RESOLVED, NOW), 'resolved 1 h ago');
	});

	test('the time follows the truncated detail as a sibling, so a long detail cannot hide it', () => {
		const html = render(NotificationsList, { props: { items: [RELEASE, UPDATE, RESOLVED] } }).body;
		for (const item of [RELEASE, UPDATE, RESOLVED]) {
			const row = new RegExp(
				`<span class="truncate"[^>]*>[^<]*</span> <span aria-hidden="true">·</span><!--\\]--> <time datetime="${item.at}"[^>]*class="shrink-0"`
			);
			assert.match(html, row, item.key);
		}
		assert.match(html, /class="shrink-0">24 Nov<\/time>/);
	});

	test('a row without a detail shows the time alone', () => {
		const html = render(NotificationsList, { props: { items: [{ ...UPDATE, detail: '' }] } }).body;
		assert.match(html, /<time datetime="2026-11-24T00:29:00Z"[^>]*class="shrink-0">2 h ago<\/time>/);
		assert.doesNotMatch(html, /aria-hidden="true">·</);
		// The only truncated text left is the title.
		assert.equal((html.match(/class="truncate[ "]/g) ?? []).length, 1);
	});
});

// api-v1 v1.57 (spec-release-first-served): a release the server has served carries its real time with date_only
// false, and its row shows an age like any other; date_only true, or an API that does not send it, keeps the date.
describe('bell release row with a first-served time', () => {
	test('date_only false shows the age and the full local time on hover', () => {
		const served = { ...RELEASE, at: '2026-11-24T01:24:00Z', date_only: false };
		assert.equal(rowTime(served, NOW), '1 h ago');
		assert.equal(rowTitle(served, 'UTC'), formatWhen(served.at, 'UTC'));
		const html = render(NotificationsList, { props: { items: [served] } }).body;
		const tip = html.match(/<time datetime="2026-11-24T01:24:00Z" title="([^"]*)"/)?.[1];
		assert.ok(tip && tip.includes('2026') && !tip.startsWith('Released'), String(tip));
		assert.match(html, /class="shrink-0">\d+ (min|h|d) ago<\/time>/);
		// A clock ahead of the server's shows "just now", not a negative age.
		assert.equal(rowTime({ ...served, at: '2026-11-24T02:35:00Z' }, NOW), 'just now');
	});

	test('date_only true or missing keeps the date', () => {
		assert.equal(rowTime({ ...RELEASE, date_only: true }, NOW), '24 Nov');
		assert.equal(rowTime(RELEASE, NOW), '24 Nov');
	});

	test('the parser stores date_only false only when the API sends false', () => {
		const wire = { key: 'release:0.2.48', kind: 'release', title: 'Deploybot v0.2.48 is out', detail: 'x', href: '/whats-new#v0.2.48', at: '2026-11-25T03:00:00Z', read: false };
		assert.equal(parseNotification({ ...wire, date_only: false })?.date_only, false);
		assert.equal(parseNotification({ ...wire, date_only: true })?.date_only, true);
		assert.equal(parseNotification(wire)?.date_only, true);
		for (const odd of ['no', null, 0]) assert.equal(parseNotification({ ...wire, date_only: odd })?.date_only, true);
		// Other kinds never carry it.
		const update = parseNotification({ ...wire, key: 'host_update:x:0.2.16', kind: 'host_update', href: '/hosts/x', date_only: false });
		assert.equal(update != null && 'date_only' in update, false);
		// Only a release is ever date-only.
		assert.equal(releaseDateOnly({ ...UPDATE, date_only: true }), false);
	});
});
