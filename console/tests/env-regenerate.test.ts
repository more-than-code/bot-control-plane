import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure } from '@sveltejs/kit';
import {
	envKeyLink,
	redeployNote,
	regenerateAnswer,
	regenerateFailedText,
	regenerateMessage
} from '../src/lib/env-generate.ts';
import { privateKeyRegeneratePath } from '../src/lib/private-keys.ts';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

// Regenerate a secret key and show the new value once (api-v1 v1.54).

const NEW_VALUE = 'Regen3ratedValueNeverInPageData0123456789';
const base = { session: TOKEN, user: USER, projects: [PROJECT], params: { id: PROJECT.id } };
const ACTION_HEADERS = { 'x-sveltekit-action': 'true', accept: 'application/json' };

function source(path: string): string {
	return readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8');
}

describe('regenerate text', () => {
	test('the confirm names the entries that keep the old value', () => {
		assert.match(regenerateMessage('PW', ['web', 'api', 'worker']), /Nothing changes in running services until you redeploy web, api and worker\. If this value was also set somewhere else/);
		assert.match(regenerateMessage('PW', ['web']), /until you redeploy web\. /);
		assert.match(regenerateMessage('PW', ['web']), /change it there too, or the app will fail after the redeploy\.$/);
		assert.match(regenerateMessage('PW', []), /No catalog entry uses it\.$/);
		assert.match(regenerateMessage('PW', undefined), /Nothing changes in running services until you redeploy the entries that use it\. If this value/);
		assert.match(regenerateMessage('PW', []), /^Make a new value for PW\? The current value is replaced now and cannot be shown again\./);
		assert.equal(redeployNote(['web', 'api']), 'Redeploy web and api now to use the new value; until then they keep the old one.');
		assert.equal(redeployNote([]), '');
	});

	test('the answer reader takes a value only for the asked key', () => {
		const data = { scope: 'settings', regenerated: { key: 'PW', value: NEW_VALUE, used_by: ['web', 7] } };
		assert.deepEqual(regenerateAnswer({ type: 'success', data }, 'PW'), { ok: true, value: NEW_VALUE, usedBy: ['web'] });
		assert.deepEqual(regenerateAnswer({ type: 'success', data }, 'OTHER'), { ok: false, message: regenerateFailedText('OTHER') });
		assert.deepEqual(regenerateAnswer({ type: 'failure', data: { regenerateError: 'PW is not secret; edit it to change it' } }, 'PW'), {
			ok: false,
			message: 'PW is not secret; edit it to change it'
		});
		assert.deepEqual(regenerateAnswer({ type: 'error' }, 'PW'), { ok: false, message: regenerateFailedText('PW') });
	});

	test('the deep link reads env.set, env.key and env.do; anything else is ignored', () => {
		const q = (s: string) => new URLSearchParams(s);
		assert.deepEqual(envKeyLink(q('tab=settings&env.set=mos-dev-secrets&env.key=MOS_SEED_PASSWORD&env.do=regenerate')), {
			set: 'mos-dev-secrets',
			key: 'MOS_SEED_PASSWORD',
			action: 'regenerate'
		});
		assert.equal(envKeyLink(q('env.set=app&env.key=PW&env.do=reveal'))?.action, 'reveal');
		assert.equal(envKeyLink(q('env.set=app&env.key=PW&env.do=delete')), null);
		assert.equal(envKeyLink(q('env.set=@web&env.key=PW&env.do=reveal')), null);
		assert.equal(envKeyLink(q('env.set=app&env.key=pw&env.do=reveal')), null);
		assert.equal(envKeyLink(q('tab=settings')), null);
	});

	test('the link only focuses a button: the table never opens a dialog from the URL', () => {
		const table = source('routes/projects/[id]/EnvSetTable.svelte');
		assert.match(table, /envKeyLink\(page\.url\.searchParams\)/);
		const effect = table.slice(table.indexOf('envKeyLink(page.url'), table.indexOf('envKeyLink(page.url') + 700);
		assert.equal(/regenKey =|revealKey =|showOnce\(/.test(effect), false);
		assert.match(effect, /button\?\.focus\(\)/);
	});

	test('private keys get Regenerate too, and the path is the entry route', () => {
		assert.equal(privateKeyRegeneratePath(PROJECT.id, 'web', 'KEY'), `/v1/projects/${PROJECT.id}/catalog/web/private-keys/KEY/regenerate`);
		assert.equal(privateKeyRegeneratePath(PROJECT.id, 'Web', 'KEY'), null);
		const keys = source('routes/projects/[id]/PrivateKeys.svelte');
		assert.match(keys, /label="Regenerate \{row\.key\}" icon=\{RotateCcw\}/);
		assert.match(keys, /<EnvRegenerateDialog setName="@\{entry\}" \{regenKey\} usedBy=\{\[entry\]\} action=\{regenerateAction\}/);
	});
});

describe('regenerate actions', () => {
	before(() => useApi('https://api.example'));
	after(() => restoreEnv());
	const url = `http://console.example/projects/${PROJECT.id}?tab=settings&/regenerateEnvKey`;

	test('regenerate then reveal; the value is only in that action result', async () => {
		const calls = installFetch((call) =>
			call.url.pathname.endsWith('/regenerate')
				? envelope({ key: 'PW', is_secret: true, preview: '••••', reveal_pending: true, used_by: ['api', 'web', 'Bad Name'] })
				: envelope({ key: 'PW', value: NEW_VALUE })
		);
		const result = await projectActions.regenerateEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'PW', generate_kind: 'hex' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.deepEqual(
			calls.map((c) => [c.method, c.url.pathname]),
			[
				['POST', `/v1/projects/${PROJECT.id}/env-sets/app/keys/PW/regenerate`],
				['POST', `/v1/projects/${PROJECT.id}/env-sets/app/keys/PW/reveal`]
			]
		);
		assert.deepEqual(JSON.parse(calls[0]?.body ?? ''), { generate: { kind: 'hex' } });
		assert.deepEqual(result, { scope: 'settings', regenerated: { key: 'PW', value: NEW_VALUE, used_by: ['api', 'web'] } });
		assert.equal(Object.hasOwn(result as object, 'message'), false);
	});

	test('a password sends its length; private keys use the entry routes', async () => {
		const calls = installFetch((call) => (call.url.pathname.endsWith('/regenerate') ? envelope({ key: 'KEY', used_by: ['web'] }) : envelope({ key: 'KEY', value: NEW_VALUE })));
		const result = await projectActions.regeneratePrivateKey(
			fakeEvent({ ...base, url, form: { set: '@web', key: 'KEY', generate_kind: 'password' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.deepEqual(JSON.parse(calls[0]?.body ?? ''), { generate: { kind: 'password', length: 32 } });
		assert.equal(calls[0]?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/web/private-keys/KEY/regenerate`);
		assert.equal(calls[1]?.url.pathname, `/v1/projects/${PROJECT.id}/catalog/web/private-keys/KEY/reveal`);
		assert.deepEqual(result, { scope: 'catalog', regenerated: { key: 'KEY', value: NEW_VALUE, used_by: ['web'] } });
	});

	test('refusals: no JavaScript, a bad kind or key, an API answer, a lost reveal', async () => {
		const calls = installFetch(() => envelope({ key: 'PW', value: NEW_VALUE }));
		for (const [form, headers] of [
			[{ set: 'app', key: 'PW', generate_kind: 'hex' }, {}],
			[{ set: 'app', key: 'PW', generate_kind: 'rot13' }, ACTION_HEADERS],
			[{ set: 'app', key: 'bad key', generate_kind: 'hex' }, ACTION_HEADERS]
		] as const) {
			const out = await projectActions.regenerateEnvKey(fakeEvent({ ...base, url, form, requestHeaders: headers }).event as never);
			assert.equal(isActionFailure(out) && out.status, 400);
		}
		assert.equal(calls.length, 0);

		const refused = 'PW is not secret; edit it to change it';
		installFetch(() => envelope(null, 400, 'bad_request', refused));
		const failed = await projectActions.regenerateEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'PW', generate_kind: 'hex' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.equal(isActionFailure(failed) && (failed.data as unknown as { regenerateError: string }).regenerateError, refused);

		// The regenerate worked but the reveal answer is lost: the key stays pending, so the text points at Reveal once.
		installFetch((call) => (call.url.pathname.endsWith('/regenerate') ? envelope({ key: 'PW', used_by: [] }) : envelope(null, 503, 'unavailable', 'down')));
		const lost = await projectActions.regenerateEnvKey(
			fakeEvent({ ...base, url, form: { set: 'app', key: 'PW', generate_kind: 'hex' }, requestHeaders: ACTION_HEADERS }).event as never
		);
		assert.equal(isActionFailure(lost) && (lost.data as unknown as { regenerateError: string }).regenerateError, regenerateFailedText('PW'));
	});
});
