// spec-deploy-stuck §2.3 (api-v1 v1.42): the 80 s wait cap, the running row
// in History and its polling, and conflict times in local time.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure } from '@sveltejs/kit';
import { render } from 'svelte/server';
import type { Host, ImageDeploy, ImageEntry } from '../src/lib/api-types.ts';
import { formatClock } from '../src/lib/format.ts';
import {
	IMAGE_ACTION_WAIT_MS,
	RUNNING_POLL_GRACE_MS,
	RUNNING_POLL_MS,
	STILL_RUNNING,
	finishedOutcome,
	historyPollDelay,
	historyTimeoutMs,
	localConflictMessage,
	runningRow
} from '../src/lib/image-spec.ts';
import { hasRunningConflict, reuseIdempotencyKey } from '../src/lib/catalog.ts';
import { parseDeployPayload, parseImageDeploy, parseImageDeployList } from '../src/lib/server/shape.ts';
import type { ImageDeployState } from '../src/lib/project-action.ts';
import CatalogDeployForm from '../src/routes/projects/[id]/CatalogDeployForm.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';
import { setPageUrl } from './stubs/app-state.ts';

const HOST_ID = '00000000-0000-4000-8000-0000000000aa';
const ENTRY_ID = '00000000-0000-4000-8000-0000000000c1';
const HASH = 'ab'.repeat(32);
const KEY = 'stuck-key-1';
const RUN_ID = '00000000-0000-4000-8000-0000000000f1';
const DONE_ID = '00000000-0000-4000-8000-0000000000f2';
const CONFIRMATION_ID = 'Abcdefghijklmnopqrstuvwxyz0123456789_-ABCDE';
const CONFLICT_MSG = 'A deploy, rollback or remove of web on pi1 has been running since 12:00 UTC. Try again after 12:09 UTC.';

function digest(ch: string): string {
	return `sha256:${ch.repeat(64)}`;
}

function runningWire(endsBy = '2026-10-14T12:09:00Z') {
	return {
		id: RUN_ID,
		entry_id: ENTRY_ID,
		host_id: HOST_ID,
		digest: null,
		spec_hash: HASH,
		outcome: 'in_flight',
		exit_code: null,
		started_at: '2026-10-14T12:00:00Z',
		finished_at: null,
		by: USER.id,
		via: 'console',
		ends_by: endsBy
	};
}

function finishedWire(id = DONE_ID, outcome = 'succeeded') {
	return {
		id,
		entry_id: ENTRY_ID,
		host_id: HOST_ID,
		digest: digest('a'),
		spec_hash: HASH,
		outcome,
		exit_code: 0,
		started_at: '2026-10-14T11:00:00Z',
		finished_at: '2026-10-14T11:01:00Z',
		by: USER.id,
		via: 'console',
		ends_by: null
	};
}

function imageEntry(): ImageEntry {
	return {
		id: ENTRY_ID,
		project_id: PROJECT.id,
		name: 'web',
		kind: 'image',
		spec: {
			kind: 'service',
			image: 'ghcr.io/acme/web:1',
			command: null,
			entrypoint: null,
			ports: [],
			env: {},
			env_sets: [],
			secret_env: {},
			volumes: [],
			files: [],
			restart: 'unless-stopped',
			user: '',
			healthy_after_sec: 30,
			memory_mb: null,
			timeout_sec: 600,
			healthcheck: null,
			logging: null,
			health_policy: null
		},
		spec_hash: HASH,
		command_display: 'ghcr.io/acme/web:1',
		created_by: USER.id,
		created_at: '2026-10-14T00:00:00Z',
		updated_at: '2026-10-14T00:00:00Z',
		my_actions: ['deploy']
	};
}

function host(): Host {
	return {
		id: HOST_ID,
		org_id: PROJECT.id,
		name: 'pi1',
		node_type: 'pi',
		os: 'linux',
		arch: 'arm64',
		always_on: true,
		agent_version: '1.2.0',
		status: 'online',
		last_seen_at: '2026-10-14T00:00:00Z',
		enrolled_at: '2026-10-13T00:00:00Z',
		revoked_at: null,
		access_level: 'deploy',
		access: null,
		programs: { enabled: true, run_as: ['joe'], allow_shell: true, policy_hash: 'policy-1', images: true, docker: true, files: false, logging: false, health_policy: false, secrets: false, projects: null, project_listed: null, shared_networks: null, env_hash: false }
	};
}

function stateWith(history: ImageDeploy[], extra: Partial<ImageDeployState> = {}): ImageDeployState {
	return {
		name: 'web',
		resultId: '00000000-0000-4000-8000-000000000199',
		hostId: HOST_ID,
		workload: 'service',
		waitSec: 30,
		outcome: null,
		status: null,
		lines: [],
		history,
		confirmation: null,
		confirmKind: null,
		message: null,
		...extra
	};
}

function formHtml(result: ImageDeployState): string {
	return render(CatalogDeployForm, {
		props: {
			entry: imageEntry(),
			hosts: [host()],
			deployAction: '?/catalogDeploy',
			rollbackAction: '?/catalogRollback',
			removeAction: '?/catalogRemove',
			confirmAction: '?/catalogConfirm',
			historyAction: '?/catalogDeploys',
			result
		}
	}).body;
}

/** AbortSignal.timeout that fires at once for the wait cap and never for other calls. */
function capTimesOut(): { seen: number[]; restore: () => void } {
	const seen: number[] = [];
	const original = AbortSignal.timeout;
	const replacement = (ms: number) => {
		seen.push(ms);
		if (ms === IMAGE_ACTION_WAIT_MS) return AbortSignal.abort(new DOMException('The operation was aborted due to timeout', 'TimeoutError'));
		return new AbortController().signal;
	};
	Object.defineProperty(AbortSignal, 'timeout', { configurable: true, writable: true, value: replacement });
	return {
		seen,
		restore() {
			Object.defineProperty(AbortSignal, 'timeout', { configurable: true, writable: true, value: original });
		}
	};
}

function failureDeploy(result: unknown): ImageDeployState {
	assert.equal(isActionFailure(result), true);
	if (!isActionFailure(result)) throw new Error('not a failure');
	return (result.data as unknown as { imageDeploy: ImageDeployState }).imageDeploy;
}

const base = { session: TOKEN, user: USER, projects: [PROJECT], params: { id: PROJECT.id } };

describe('deploy stuck', { concurrency: false }, () => {
	const originalFetch = globalThis.fetch;

	before(() => {
		useApi('https://api.test');
		setPageUrl(`http://console.example/projects/${PROJECT.id}?tab=catalog`);
	});

	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('the history parser accepts a running row and keeps finished rows unchanged', () => {
		const running = parseImageDeploy(runningWire());
		assert.equal(running?.outcome, 'in_flight');
		assert.equal(running?.ends_by, '2026-10-14T12:09:00Z');
		assert.equal(running?.finished_at, null);
		assert.equal(running?.digest, null);
		assert.equal(parseImageDeploy(finishedWire())?.ends_by, null);
		// An API before v1.42 sends no ends_by.
		const { ends_by: _omit, ...older } = finishedWire();
		assert.equal(parseImageDeploy(older)?.ends_by, null);
		// A running row needs ends_by and no finish time; a finished row has no ends_by.
		assert.equal(parseImageDeploy({ ...runningWire(), ends_by: null }), null);
		assert.equal(parseImageDeploy({ ...runningWire(), finished_at: '2026-10-14T12:01:00Z' }), null);
		assert.equal(parseImageDeploy({ ...runningWire(), ends_by: 'soon' }), null);
		assert.equal(parseImageDeploy({ ...finishedWire(), ends_by: '2026-10-14T12:09:00Z' }), null);
		assert.equal(parseImageDeploy({ ...finishedWire(), ends_by: 42 }), null);
		const list = parseImageDeployList({ items: [runningWire(), finishedWire()], next_cursor: null });
		assert.deepEqual(
			list?.items.map((item) => item.outcome),
			['in_flight', 'succeeded']
		);
		// A deploy result is always finished.
		const status = { container: 'web', state: 'running', exit_code: null, restart_count: 0, started_at: null, finished_at: null, digest: null, ports: [] };
		assert.equal(parseDeployPayload({ deploy: runningWire(), status, lines: [] }), null);
		assert.equal(parseDeployPayload({ deploy: finishedWire(), status, lines: [] })?.kind, 'deploy');
	});

	test('deploy, rollback, remove and confirm wait at most 80 s, then say still running and reload History', async () => {
		assert.equal(IMAGE_ACTION_WAIT_MS, 80_000);
		const spy = capTimesOut();
		const calls = installFetch((call) => {
			if (call.signal?.aborted) return Promise.reject(call.signal.reason instanceof Error ? call.signal.reason : new DOMException('aborted', 'AbortError'));
			if (call.url.pathname.endsWith('/deploys')) return envelope({ items: [runningWire(), finishedWire()], next_cursor: null });
			return envelope(null, 500, 'error', 'unexpected');
		});
		try {
			const form = { name: 'web', host_id: HOST_ID, workload: 'service', wait_sec: '30', idempotency_key: KEY };
			const deployed = failureDeploy(
				await projectActions.catalogDeploy(
					fakeEvent({ ...base, url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogDeploy`, form }).event as never
				)
			);
			const rolled = failureDeploy(
				await projectActions.catalogRollback(
					fakeEvent({
						...base,
						url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogRollback`,
						form: { ...form, digest: digest('a') }
					}).event as never
				)
			);
			const confirmed = failureDeploy(
				await projectActions.catalogConfirm(
					fakeEvent({
						...base,
						url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogConfirm`,
						form: { ...form, confirmation_id: CONFIRMATION_ID, confirm_kind: 'rollback' }
					}).event as never
				)
			);
			// Remove uses the same cap and still-running path (spec-deploy-stuck §9 item 4).
			const removed = failureDeploy(
				await projectActions.catalogRemove(
					fakeEvent({ ...base, url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogRemove`, form }).event as never
				)
			);
			for (const state of [deployed, rolled, confirmed, removed]) {
				assert.equal(state.message, STILL_RUNNING);
				assert.equal(state.history?.[0]?.outcome, 'in_flight');
				assert.equal(state.history?.[0]?.ends_by, '2026-10-14T12:09:00Z');
			}
			// Each History read after the cap has its own short timeout (at most 10 s).
			assert.deepEqual(spy.seen, [IMAGE_ACTION_WAIT_MS, 10_000, IMAGE_ACTION_WAIT_MS, 10_000, IMAGE_ACTION_WAIT_MS, 10_000, IMAGE_ACTION_WAIT_MS, 10_000]);
			assert.equal(calls.filter((call) => call.url.pathname.endsWith('/deploys')).length, 4);
		} finally {
			spy.restore();
		}
	});

	test('a running-operation conflict keeps the API message, carries its times and reloads History', async () => {
		const calls = installFetch((call) => {
			if (call.url.pathname.endsWith('/deploys')) return envelope({ items: [runningWire()], next_cursor: null });
			return envelope({ started_at: '2026-10-14T12:00:00Z', ends_by: '2026-10-14T12:09:00Z' }, 409, 'conflict', CONFLICT_MSG);
		});
		const form = { name: 'web', host_id: HOST_ID, workload: 'service', wait_sec: '30', idempotency_key: KEY };
		for (const [name, extra] of [
			['catalogDeploy', {}],
			['catalogRollback', { digest: digest('a') }],
			['catalogRemove', {}],
			['catalogConfirm', { confirmation_id: CONFIRMATION_ID, confirm_kind: 'deploy' }]
		] as const) {
			const action = projectActions[name] as (event: never) => Promise<unknown>;
			const state = failureDeploy(
				await action(fakeEvent({ ...base, url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/${name}`, form: { ...form, ...extra } }).event as never)
			);
			assert.equal(state.message, CONFLICT_MSG, name);
			assert.deepEqual(state.conflict, { started_at: '2026-10-14T12:00:00Z', ends_by: '2026-10-14T12:09:00Z' }, name);
			assert.equal(state.history?.[0]?.outcome, 'in_flight', name);
		}
		assert.equal(calls.filter((call) => call.url.pathname.endsWith('/deploys')).length, 4);

		// The other two conflicts carry no times.
		installFetch((call) => {
			if (call.url.pathname.endsWith('/deploys')) return envelope({ items: [], next_cursor: null });
			return envelope(null, 409, 'conflict', 'This request key was already used for a different request.');
		});
		const reused = failureDeploy(
			await projectActions.catalogDeploy(
				fakeEvent({ ...base, url: `http://console.example/projects/${PROJECT.id}?tab=catalog&/catalogDeploy`, form }).event as never
			)
		);
		assert.equal(reused.message, 'This request key was already used for a different request.');
		assert.equal(reused.conflict, null);
	});

	test('conflict times are shown in the viewer zone', () => {
		const times = { started_at: '2026-10-14T12:00:00Z', ends_by: '2026-10-14T12:09:00Z' };
		assert.equal(
			localConflictMessage(CONFLICT_MSG, times, 'Pacific/Auckland'),
			'A deploy, rollback or remove of web on pi1 has been running since 01:00 NZDT. Try again after 01:09 NZDT.'
		);
		assert.equal(localConflictMessage(CONFLICT_MSG, times, 'UTC'), 'A deploy, rollback or remove of web on pi1 has been running since 12:00 UTC. Try again after 12:09 UTC.');
		assert.equal(localConflictMessage(CONFLICT_MSG, null, 'Pacific/Auckland'), CONFLICT_MSG);
		assert.equal(localConflictMessage(CONFLICT_MSG, { started_at: 'x', ends_by: 'y' }, 'Pacific/Auckland'), CONFLICT_MSG);
		assert.equal(localConflictMessage('The entry changed after this plan was made. Plan again.', times, 'Pacific/Auckland'), 'The entry changed after this plan was made. Plan again.');

		const html = formHtml(stateWith([], { message: CONFLICT_MSG, conflict: times, history: null }));
		const local = localConflictMessage(CONFLICT_MSG, times);
		assert.equal(html.includes(local), true);
		// Informational, not an error (spec-deploy-stuck §9 item 4).
		assert.match(html, /role="status"[^>]*>[\s\S]*?Try again after/);
		assert.equal(html.includes('role="alert"'), false);

		const still = formHtml(stateWith([], { message: STILL_RUNNING, history: null }));
		assert.equal(still.includes(STILL_RUNNING), true);
		assert.equal(still.includes('role="alert"'), false);
		// Other failures stay errors.
		const other = formHtml(stateWith([], { message: 'The entry changed after this plan was made. Plan again.', history: null }));
		assert.equal(other.includes('role="alert"'), true);
	});

	test('History read after an action answers before 100 s', () => {
		const start = 1_000_000;
		assert.equal(historyTimeoutMs(start, start), 10_000);
		assert.equal(historyTimeoutMs(start, start + 80_000), 10_000);
		assert.equal(historyTimeoutMs(start, start + 88_000), 7_000);
		assert.equal(historyTimeoutMs(start, start + 94_500), 1_000);
		assert.equal(historyTimeoutMs(start, start + 120_000), 1_000);
	});

	test('a running conflict gives the next try a fresh key; other failures keep it', () => {
		const conflict = { imageDeploy: { conflict: { started_at: '2026-10-14T12:00:00Z', ends_by: '2026-10-14T12:09:00Z' } } };
		assert.equal(hasRunningConflict(conflict), true);
		assert.equal(reuseIdempotencyKey('failure', conflict), false);
		assert.equal(reuseIdempotencyKey('failure', { imageDeploy: { conflict: null, message: 'conflict' } }), true);
		assert.equal(reuseIdempotencyKey('failure', undefined), true);
		assert.equal(reuseIdempotencyKey('error'), true);
		assert.equal(reuseIdempotencyKey('success'), false);
		const source = readFileSync(new URL('../src/routes/projects/[id]/CatalogDeployForm.svelte', import.meta.url), 'utf8');
		assert.equal((source.match(/reuseIdempotencyKey\(actionResult\.type, failureData\(actionResult\)\)/g) ?? []).length, 3);
		// Remove posts through ConfirmAction, so its key turns over on the result instead.
		const effect = source.slice(source.indexOf('rotatedFor === result.resultId'));
		assert.match(effect.slice(0, 300), /removeKey = freshKey\(\)/);
		assert.match(effect.slice(0, 300), /deployRequest = null/);
	});

	test('History shows a running row with its start and ends-by time', () => {
		const running = parseImageDeploy(runningWire());
		const done = parseImageDeploy(finishedWire());
		assert.ok(running && done);
		const html = formHtml(stateWith([running, done]));
		assert.match(html, /running/);
		assert.equal(html.includes(`ends by ${formatClock('2026-10-14T12:09:00Z')}`), true);
		assert.match(html, /since (?:<!--[^>]*-->)*<time datetime="2026-10-14T12:00:00Z"/);
		// The running row offers no digest and no rollback.
		assert.equal(html.includes('no digest'), false);
		// The live region for the finished outcome is present and empty on load.
		assert.match(html, /<p class="sr-only" role="status"><\/p>/);
	});

	test('History polls every 10 s while a row runs, until a refresh drops it or the grace past ends_by ends', () => {
		const endsBy = Date.parse('2026-10-14T12:09:00Z');
		const running = [runningWire(), finishedWire()];
		assert.equal(RUNNING_POLL_MS, 10_000);
		assert.equal(runningRow(running)?.id, RUN_ID);
		assert.equal(historyPollDelay(running, endsBy - 5 * 60_000), RUNNING_POLL_MS);
		// The last refresh lands just after ends_by, when the API drops the row.
		assert.equal(historyPollDelay(running, endsBy - 4_000), 5_000);
		// A skewed local clock: past ends_by the row is still listed, so
		// polling goes on until a refresh drops it or the grace ends.
		assert.equal(RUNNING_POLL_GRACE_MS, 120_000);
		assert.equal(historyPollDelay(running, endsBy), RUNNING_POLL_MS);
		assert.equal(historyPollDelay(running, endsBy + 1), RUNNING_POLL_MS);
		assert.equal(historyPollDelay(running, endsBy + RUNNING_POLL_GRACE_MS - 1), RUNNING_POLL_MS);
		assert.equal(historyPollDelay(running, endsBy + RUNNING_POLL_GRACE_MS), null);
		assert.equal(historyPollDelay([finishedWire()], endsBy + 1), null);
		assert.equal(historyPollDelay([finishedWire()], endsBy - 60_000), null);
		assert.equal(historyPollDelay([], endsBy - 60_000), null);
		// Only the first row counts: the API lists a running row first.
		assert.equal(historyPollDelay([finishedWire(), runningWire()], endsBy - 60_000), null);

		assert.equal(finishedOutcome(running, [finishedWire(RUN_ID, 'rolled_back'), finishedWire()]), 'rolled_back');
		assert.equal(finishedOutcome(running, running), null);
		assert.equal(finishedOutcome([finishedWire()], [finishedWire()]), null);
		// The row vanished (its deadline passed): nothing to announce.
		assert.equal(finishedOutcome(running, [finishedWire()]), null);
	});

	test('the form polls through the history form and clears its timer', () => {
		const source = readFileSync(new URL('../src/routes/projects/[id]/CatalogDeployForm.svelte', import.meta.url), 'utf8');
		const effect = source.slice(source.indexOf('historyPollDelay(history'), source.indexOf('clearTimeout(timer)') + 'clearTimeout(timer)'.length);
		assert.notEqual(effect, '');
		assert.match(effect, /historyForm\?\.requestSubmit\(\)/);
		assert.match(effect, /return \(\) => clearTimeout\(timer\)/);
		// Polling stops when the drawer is not active.
		const before = source.slice(0, source.indexOf('historyPollDelay(history'));
		assert.match(before.slice(before.lastIndexOf('$effect')), /if \(!browser \|\| !active\) return;/);
		assert.equal(source.includes('deployTimeoutMs'), false);
	});
});
