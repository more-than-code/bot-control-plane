import assert from 'node:assert/strict';
import { describe, test } from 'node:test';
import type { ActionResult } from '@sveltejs/kit';
import {
	confirmCode,
	deleteBlocked,
	deleteMessage,
	ghcrRepository,
	holderText,
	parseImageVersions,
	readImageVersions,
	shortDigest,
	splitStale,
	versionStatus,
	versionsReasonText,
	VERSIONS_FAILED,
	type ImageVersion,
	type ImageVersionsList
} from '../src/lib/image-versions.ts';
import { entryMenuItems } from '../src/lib/row-menu.ts';

const PROJECT = '11111111-1111-4111-8111-111111111111';
const OTHER = '22222222-2222-4222-8222-222222222222';
const HOST = '33333333-3333-4333-8333-333333333333';
const A = 'sha256:' + 'a'.repeat(64);
const B = 'sha256:' + 'b'.repeat(64);

function raw(id: number, digest: string, extra: Record<string, unknown> = {}) {
	return {
		version_id: id,
		digest,
		tags: ['dev', 'sha-1'],
		created_at: '2026-11-01T10:00:00Z',
		updated_at: '2026-11-01T10:00:00Z',
		in_use: [],
		stale: false,
		release: false,
		...extra
	};
}

const holder = { project_id: PROJECT, project_name: 'Proj', entry: 'web', host_id: HOST, host_name: 'pi4', why: 'running' };

describe('image versions (api-v1 v1.45)', () => {
	test('only ghcr.io repositories have versions', () => {
		assert.equal(ghcrRepository('ghcr.io/acme/web:dev'), 'ghcr.io/acme/web');
		assert.equal(ghcrRepository('ghcr.io/acme/web@' + A), 'ghcr.io/acme/web');
		assert.equal(ghcrRepository('ghcr.io/acme'), null);
		assert.equal(ghcrRepository('nginx:1.27'), null);
		assert.equal(ghcrRepository('quay.io/acme/web'), null);
	});

	test('the list parses, drops bad rows and tags, and keeps the reason only when it cannot delete', () => {
		const list = parseImageVersions({
			items: [raw(1, A, { in_use: [holder], tags: ['dev', 'bad tag'] }), raw(2, 'sha256:short'), raw(3, B, { stale: true }), 'x'],
			truncated: false,
			can_delete: true,
			can_delete_reason: 'ignored'
		});
		assert.ok(list);
		assert.deepEqual(
			list.items.map((v) => v.version_id),
			[1, 3]
		);
		assert.deepEqual(list.items[0].tags, ['dev']);
		assert.equal(list.can_delete_reason, null);
		const no = parseImageVersions({ items: [], truncated: true, can_delete: false, can_delete_reason: 'only org owners and admins can delete image versions' });
		assert.equal(no?.can_delete_reason, 'only org owners and admins can delete image versions');
		assert.equal(parseImageVersions({ items: [], truncated: false }), null);
		// A holder that does not parse drops the whole version: it must never look unused.
		const bad = parseImageVersions({ items: [raw(1, A, { in_use: [{ ...holder, why: 'other' }] })], truncated: false, can_delete: true });
		assert.equal(bad?.items.length, 0);
	});

	test('the confirmation, digest and holder texts', () => {
		assert.equal(confirmCode(A), 'aaaaaaaaaaaa');
		assert.equal(shortDigest(B), 'sha256:bbbbbbbbbbbb');
		assert.equal(holderText(holder as never, PROJECT), 'web on pi4 (running)');
		assert.equal(holderText({ ...holder, project_id: OTHER, project_name: 'Other', why: 'rollback_target' } as never, PROJECT), 'Other / web on pi4 (rollback target)');
	});

	test('status, blocked delete, message and stale groups', () => {
		const list = parseImageVersions({
			items: [raw(1, A, { in_use: [holder, { ...holder, host_name: 'pi3' }] }), raw(2, B, { stale: true, release: true, tags: ['release-1'] })],
			truncated: false,
			can_delete: true
		}) as ImageVersionsList;
		const [held, stale] = list.items as [ImageVersion, ImageVersion];
		assert.equal(versionStatus(held, PROJECT), 'In use: web on pi4 (running) and 1 more');
		assert.equal(versionStatus(stale, PROJECT), 'Stale');
		assert.equal(deleteBlocked(list, held, PROJECT), 'In use: web on pi4 (running)');
		assert.equal(deleteBlocked(list, stale, PROJECT), null);
		assert.equal(deleteBlocked({ ...list, can_delete: false, can_delete_reason: 'no scope' }, stale, PROJECT), 'no scope');
		const message = deleteMessage(stale);
		assert.match(message, /removes all of these tags: release-1\./);
		assert.match(message, /release tag/);
		assert.match(message, /cannot be undone/);
		assert.ok(message.includes(B));
		const groups = splitStale(list.items);
		assert.deepEqual(
			[groups.current.map((v) => v.version_id), groups.stale.map((v) => v.version_id)],
			[[1], [2]]
		);
	});

	test('GitHub reasons read as sentences, never GitHub text', () => {
		assert.match(versionsReasonText('delete_refused'), /admin rights on the package/);
		assert.match(versionsReasonText('not_found'), /did not find/);
		assert.match(versionsReasonText('anything'), /Could not reach GitHub/);
	});

	test('the read posts the action and never rejects', async () => {
		const list = { items: [raw(1, A)], truncated: false, can_delete: true, can_delete_reason: null };
		let posted: FormData | null = null;
		const deps = (result: ActionResult | Error) => ({
			fetch: (async (_url: string, init?: RequestInit) => {
				posted = init?.body as FormData;
				if (result instanceof Error) throw result;
				return new Response('x');
			}) as typeof fetch,
			deserialize: () => result as ActionResult,
			action: '?/catalogImageVersions',
			origin: 'http://console.test'
		});
		const ok = await readImageVersions(deps({ type: 'success', status: 200, data: { imageVersions: { list, message: null } } }), 'ghcr.io/acme/web');
		assert.equal(ok.kind === 'state' && ok.state.state === 'ready' && ok.state.list.items.length, 1);
		assert.equal((posted as FormData | null)?.get('image'), 'ghcr.io/acme/web');
		const refused = await readImageVersions(deps({ type: 'failure', status: 400, data: { imageVersions: { list: null, message: 'nope' } } }), 'ghcr.io/acme/web');
		assert.deepEqual(refused, { kind: 'state', state: { state: 'failed', message: 'nope' } });
		const thrown = await readImageVersions(deps(new Error('net')), 'ghcr.io/acme/web');
		assert.deepEqual(thrown, { kind: 'state', state: { state: 'failed', message: VERSIONS_FAILED } });
		const away = await readImageVersions(deps({ type: 'redirect', status: 303, location: '/signin' }), 'ghcr.io/acme/web');
		assert.deepEqual(away, { kind: 'redirect', path: '/signin' });
	});

	test('the entry menu offers Image versions only when asked', () => {
		const keys = (versions: boolean) => entryMenuItems({ name: 'web', deploy: true, edit: true, owner: true, versions }).map((item) => item.key);
		assert.ok(keys(true).includes('versions'));
		assert.equal(keys(false).includes('versions'), false);
		// Delete stays last.
		assert.equal(keys(true).at(-1), 'delete');
	});
});
