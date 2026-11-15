import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import { FILL_TEXT, fillBody, fillView, isValueRequestId, parseFillRequest, withoutValue } from '../src/lib/fill-request.ts';
import { safeNext } from '../src/lib/server/session.ts';
import { actions, load } from '../src/routes/fill/[id]/+page.server.ts';
import FillPage from '../src/routes/fill/[id]/+page.svelte';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

// api-v1 v1.28 items 4–6, spec-env-request-value §3.3 and AC 8.

const ID = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde';
const SECRET = 'typed-secret-value-0123456789';
const originalFetch = globalThis.fetch;

function page(overrides: Record<string, unknown> = {}): Record<string, unknown> {
	return {
		request_id: ID,
		project_id: PROJECT.id,
		project_name: 'Lab',
		set: 'services',
		key: 'STRIPE_API_KEY',
		state: 'pending',
		expires_at: '2026-11-15T10:15:00Z',
		created_at: '2026-11-15T10:00:00Z',
		reason: 'Needed for checkout',
		...overrides
	};
}

function fillEvent(input: { id?: string; user?: boolean; form?: Record<string, string>; headers?: Record<string, string> } = {}) {
	const id = input.id ?? ID;
	return fakeEvent({
		url: `http://console.example/fill/${id}`,
		session: input.user === false ? undefined : TOKEN,
		user: input.user === false ? null : USER,
		projects: [PROJECT],
		params: { id },
		form: input.form,
		requestHeaders: input.headers
	}).event;
}

function data<T>(value: T): Exclude<T, void | undefined | null> {
	if (value == null) assert.fail('missing data');
	return value as Exclude<T, void | undefined | null>;
}

function htmlOf(loaded: unknown, form: unknown = null): string {
	return render(FillPage, { props: { data: loaded, form } as never }).body;
}

describe('value request fill page', () => {
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('ids, shapes and bodies fail closed', () => {
		assert.equal(ID.length, 43);
		assert.equal(isValueRequestId(ID), true);
		for (const bad of ['', 'short', `${ID}x`, `${ID.slice(0, 42)}/`, `${ID.slice(0, 42)}.`]) assert.equal(isValueRequestId(bad), false, bad);
		assert.equal(parseFillRequest(page(), ID)?.key, 'STRIPE_API_KEY');
		assert.equal(parseFillRequest(page({ request_id: 'other' }), ID), null);
		assert.equal(parseFillRequest(page({ state: 'done' }), ID), null);
		assert.equal(parseFillRequest(page({ key: 'lower' }), ID), null);
		assert.equal(parseFillRequest(page({ reason: 7 }), ID), null);
		assert.equal(parseFillRequest([], ID), null);
		assert.equal(fillView('pending'), 'form');
		assert.equal(fillView('filled'), 'used');
		assert.equal(fillView('declined'), 'used');
		assert.equal(fillView('expired'), 'expired');
		assert.deepEqual(fillBody('value', SECRET, ''), { ok: true, body: { value: SECRET } });
		assert.deepEqual(fillBody('value', '', ''), { ok: false, message: FILL_TEXT.noValue });
		assert.deepEqual(fillBody('generate', '', 'password'), { ok: true, body: { generate: { kind: 'password', length: 32 } } });
		assert.deepEqual(fillBody('generate', SECRET, 'hex'), { ok: true, body: { generate: { kind: 'hex' } } });
		assert.equal(fillBody('generate', '', 'nope').ok, false);
		assert.equal(withoutValue(`bad ${SECRET} here`, SECRET), 'bad [redacted] here');
	});

	test('signed out goes to sign-in and back to the same page', async () => {
		const calls = installFetch(() => assert.fail('no API call when signed out'));
		await assert.rejects(
			async () => {
				await load(fillEvent({ user: false }) as never);
			},
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/signin?next=${encodeURIComponent(`/fill/${ID}`)}`
		);
		assert.equal(safeNext(`/fill/${ID}`), `/fill/${ID}`);
		assert.equal(calls.length, 0);
	});

	test('a pending request shows its facts, the note as text, and a write-only field', async () => {
		const reason = '<a href="https://evil.test">click</a> <script>x</script>';
		const calls = installFetch((call) => {
			assert.equal(call.method, 'GET');
			assert.equal(call.url.pathname, `/v1/value-requests/${ID}/fill`);
			assert.equal(call.authorization, `Bearer ${TOKEN}`);
			return envelope(page({ reason }));
		});
		const loaded = data(await load(fillEvent() as never));
		assert.equal(loaded.view, 'form');
		assert.equal(calls.length, 1);
		const html = htmlOf(loaded);
		assert.match(html, /Enter a value/);
		assert.match(html, /Lab/);
		assert.match(html, /services/);
		assert.match(html, /STRIPE_API_KEY/);
		assert.match(html, /Note from the agent/);
		// The reason is escaped text: no link, no script.
		assert.equal(html.includes('<a href="https://evil.test"'), false);
		assert.equal(html.includes('<script>x'), false);
		assert.match(html, /&lt;a href="https:\/\/evil.test">click&lt;\/a>/);
		const input = html.match(/<input[^>]*id="fill-value"[^>]*>/)?.[0] ?? '';
		assert.match(input, /type="password"/);
		assert.match(input, /autocomplete="new-password"/);
		assert.match(input, /name="value"/);
		assert.match(html, /New value for STRIPE_API_KEY/);
		const toggle = html.match(/<button[^>]*aria-label="Show value"[^>]*>/)?.[0] ?? '';
		assert.match(toggle, /aria-pressed="false"/);
		assert.equal(html.includes('aria-label="Hide value"'), false);
		assert.match(html, />Save</);
		assert.match(html, />Decline</);
		// Decline submits its own empty form: a typed value is never posted to ?/decline.
		assert.equal(html.includes('formaction'), false);
		assert.match(html, /<button[^>]*form="fill-decline"[^>]*>/);
		const declineForm = html.match(/<form[^>]*id="fill-decline"[^>]*>([\s\S]*?)<\/form>/);
		assert.ok(declineForm, 'decline form');
		assert.match(declineForm[0], /action="\?\/decline"/);
		assert.equal(declineForm[1].includes('<input'), false);
		assert.match(html, /Generate/);
	});

	test('used, expired, other user and a bad link show a message and no form', async () => {
		const cases: Array<[Response, string]> = [
			[envelope(page({ state: 'filled' })), FILL_TEXT.used],
			[envelope(page({ state: 'declined' })), FILL_TEXT.used],
			[envelope(page({ state: 'expired' })), FILL_TEXT.expired],
			[envelope(null, 404, 'not_found', 'not found'), FILL_TEXT.missing]
		];
		for (const [response, text] of cases) {
			installFetch(() => response.clone());
			const loaded = data(await load(fillEvent() as never));
			const html = htmlOf(loaded);
			assert.equal(html.includes('<form'), false, text);
			assert.match(html, new RegExp(text.slice(0, 30).replace(/[.?]/g, '\\$&')), text);
		}
		const calls = installFetch(() => assert.fail('no API call for a bad id'));
		const bad = data(await load(fillEvent({ id: 'not-an-id' }) as never));
		assert.equal(bad.view, 'invalid');
		assert.equal(htmlOf(bad).includes('<form'), false);
		assert.equal(calls.length, 0);
	});

	test('save posts the value once and never returns it', async () => {
		const calls = installFetch((call) => {
			assert.equal(call.method, 'POST');
			assert.equal(call.url.pathname, `/v1/value-requests/${ID}/fill`);
			return envelope({ state: 'filled' });
		});
		const result = await actions.save(fillEvent({ form: { source: 'value', value: SECRET, kind: 'password' } }) as never);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { value: SECRET });
		assert.deepEqual(result, { saved: true, revealPending: false });
		assert.equal(JSON.stringify(result).includes(SECRET), false);
		// use:enhance reloads the page data: the request is now filled, and only the saved notice shows.
		installFetch(() => envelope(page({ state: 'filled' })));
		const html = htmlOf(data(await load(fillEvent() as never)), result);
		assert.match(html, /Saved\. You can close this tab\./);
		assert.equal(html.includes('already used'), false);
		assert.equal(html.includes('<form'), false);
	});

	test('save failures keep the API text and never echo the value', async () => {
		const cases: Array<[Response, string]> = [
			[envelope(null, 409, 'conflict', 'this request was already used'), 'this request was already used'],
			[envelope(null, 409, 'conflict', 'this request has expired; ask the agent for a new one'), 'this request has expired; ask the agent for a new one'],
			[envelope(null, 404, 'not_found', 'not found'), FILL_TEXT.missing],
			[envelope(null, 400, 'bad_request', `bad ${SECRET}`), 'bad [redacted]']
		];
		for (const [response, text] of cases) {
			installFetch(() => response.clone());
			const result = await actions.save(fillEvent({ form: { source: 'value', value: SECRET } }) as never);
			assert.ok(isActionFailure(result));
			assert.equal((result.data as unknown as { message: string }).message, text);
			assert.equal(JSON.stringify(result).includes(SECRET), false);
		}
		const calls = installFetch(() => assert.fail('an empty value is not sent'));
		const empty = await actions.save(fillEvent({ form: { source: 'value', value: '' } }) as never);
		assert.ok(isActionFailure(empty));
		assert.equal(calls.length, 0);
	});

	test('generate fills, then the show-once dialog reveals through this page only', async () => {
		const calls = installFetch((call) => {
			if (call.url.pathname.endsWith('/fill') && call.method === 'POST') return envelope({ state: 'filled', reveal_pending: true });
			if (call.method === 'GET') return envelope(page({ state: 'filled' }));
			assert.equal(call.url.pathname, `/v1/projects/${PROJECT.id}/env-sets/services/keys/STRIPE_API_KEY/reveal`);
			return envelope({ key: 'STRIPE_API_KEY', value: 'generated-value' });
		});
		const saved = await actions.save(fillEvent({ form: { source: 'generate', kind: 'hex' } }) as never);
		assert.deepEqual(saved, { saved: true, revealPending: true });
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { generate: { kind: 'hex' } });

		// A form post without JavaScript is refused before the API.
		const before = calls.length;
		const plain = await actions.reveal(fillEvent({ form: { key: 'STRIPE_API_KEY' } }) as never);
		assert.ok(isActionFailure(plain));
		assert.equal(calls.length, before);

		const headers = { 'x-sveltekit-action': 'true', accept: 'application/json' };
		const shown = await actions.reveal(fillEvent({ form: { key: 'STRIPE_API_KEY' }, headers }) as never);
		assert.deepEqual(shown, { revealed: { key: 'STRIPE_API_KEY', value: 'generated-value' } });
		// The key must be the request's own key.
		const other = await actions.reveal(fillEvent({ form: { key: 'OTHER' }, headers }) as never);
		assert.ok(isActionFailure(other));
	});

	test('decline posts and shows the declined notice', async () => {
		const calls = installFetch((call) => {
			if (call.method === 'GET') return envelope(page({ state: 'declined' }));
			assert.equal(call.url.pathname, `/v1/value-requests/${ID}/decline`);
			return envelope({ state: 'declined' });
		});
		const result = await actions.decline(fillEvent({ form: {} }) as never);
		assert.deepEqual(result, { declined: true });
		const html = htmlOf(data(await load(fillEvent() as never)), result);
		assert.match(html, /Declined\./);
		assert.equal(html.includes('already used'), false);
		assert.equal(html.includes('<form'), false);
		assert.equal(calls.filter((call) => call.method === 'POST').length, 1);
	});

	test('a save or decline the API refused for good hides the form and shows only the message', async () => {
		installFetch(() => envelope(null, 409, 'conflict', 'this request has expired; ask the agent for a new one'));
		const refused = await actions.save(fillEvent({ form: { source: 'value', value: SECRET } }) as never);
		assert.ok(isActionFailure(refused));
		assert.equal((refused.data as unknown as { closed: boolean }).closed, true);
		installFetch(() => envelope(null, 404, 'not_found', 'not found'));
		const gone = await actions.decline(fillEvent({ form: {} }) as never);
		assert.ok(isActionFailure(gone));
		assert.equal((gone.data as unknown as { closed: boolean }).closed, true);
		// A bad value keeps the form for another try.
		installFetch(() => envelope(null, 400, 'bad_request', 'value is too long'));
		const retry = await actions.save(fillEvent({ form: { source: 'value', value: SECRET } }) as never);
		assert.ok(isActionFailure(retry));
		assert.equal((retry.data as unknown as { closed: boolean }).closed, false);

		installFetch(() => envelope(page()));
		const loaded = data(await load(fillEvent() as never));
		const html = htmlOf(loaded, { message: 'this request has expired; ask the agent for a new one', closed: true });
		assert.match(html, /this request has expired; ask the agent for a new one/);
		assert.equal(html.includes('<form'), false);
		assert.equal(html.includes('Note from the agent'), false);
		assert.match(html, /id="fill-result"[^>]*tabindex="-1"/);
	});

	test('a load failure shows only its message', async () => {
		installFetch(() => envelope(null, 503, 'unavailable', 'API unavailable'));
		const loaded = data(await load(fillEvent() as never));
		assert.equal(loaded.view, 'error');
		const html = htmlOf(loaded);
		assert.equal(html.includes('<form'), false);
		assert.equal(html.includes(FILL_TEXT.missing.slice(0, 30)), false);
		assert.ok(loaded.message && html.includes(loaded.message.slice(0, 20)), 'message shown');
		installFetch(() => envelope({ unexpected: true }));
		const odd = data(await load(fillEvent() as never));
		assert.equal(odd.view, 'error');
		assert.equal(odd.message, FILL_TEXT.unexpected);
	});

	test('the Generate branch posts the kind', () => {
		const source = readFileSync(new URL('../src/routes/fill/[id]/+page.svelte', import.meta.url), 'utf8');
		const branch = source.slice(source.indexOf("{#if source === 'generate'}"), source.indexOf('{:else}', source.indexOf("{#if source === 'generate'}")));
		assert.match(branch, /<Input type="hidden" name="kind" value=\{kind\} \/>/);
		assert.match(source, /<Input type="hidden" name="source" value=\{source\} \/>/);
	});

	test('the page uses enhance and reuses the reveal dialog', () => {
		const source = readFileSync(new URL('../src/routes/fill/[id]/+page.svelte', import.meta.url), 'utf8');
		assert.match(source, /use:enhance/);
		assert.match(source, /EnvRevealDialog/);
		assert.equal(source.includes('{@html'), false);
	});
});
