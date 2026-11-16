import assert from 'node:assert/strict';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import { GITHUB_NOTES } from '../src/lib/settings-auto.ts';
import { CONNECT_TEXT, connectedText, connectView, isConnectRequestId, parseConnectRequest, replaceNote } from '../src/lib/connect-request.ts';
import { safeNext } from '../src/lib/server/session.ts';
import { GET as githubCallback } from '../src/routes/auth/github/callback/+server.ts';
import { actions, load } from '../src/routes/connect/[id]/+page.server.ts';
import ConnectPage from '../src/routes/connect/[id]/+page.svelte';
import { actions as projectActions } from '../src/routes/projects/[id]/+page.server.ts';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

// api-v1 v1.31 items 4–7, spec-settings-automatic §3.6.

const ID = 'CoNnEcTrEqUeSt0123456789-_abcdefghijklmnopq';
const STATE = 'S'.repeat(43);
const LEAK = 'gho_PLANTEDtokenVALUE0000';
const AUTHORIZE = `https://github.com/login/oauth/authorize?client_id=c&state=${STATE}`;
const originalFetch = globalThis.fetch;

function page(overrides: Record<string, unknown> = {}): Record<string, unknown> {
	return {
		request_id: ID,
		project_id: PROJECT.id,
		registry: 'ghcr.io',
		project_name: 'Lab',
		state: 'pending',
		expires_at: '2026-11-16T10:15:00Z',
		created_at: '2026-11-16T10:00:00Z',
		reason: 'Pull ghcr.io/acme/api',
		current_login: null,
		...overrides
	};
}

function connectEvent(input: { id?: string; user?: boolean; form?: Record<string, string>; query?: string } = {}) {
	const id = input.id ?? ID;
	return fakeEvent({
		url: `http://console.example/connect/${id}${input.query ?? ''}`,
		session: input.user === false ? undefined : TOKEN,
		user: input.user === false ? null : USER,
		projects: [PROJECT],
		params: { id },
		form: input.form
	});
}

function data<T>(value: T): Exclude<T, void | undefined | null> {
	if (value == null) assert.fail('missing data');
	return value as Exclude<T, void | undefined | null>;
}

function htmlOf(loaded: unknown, form: unknown = null): string {
	return render(ConnectPage, { props: { data: loaded, form } as never }).body;
}

function redirectOf(err: unknown): { status: number; location: string } {
	assert.equal(isRedirect(err), true, String(err));
	const r = err as { status: number; location: string };
	return { status: r.status, location: r.location };
}

describe('connect request page', () => {
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('ids, shapes and texts fail closed', () => {
		assert.equal(ID.length, 43);
		assert.equal(isConnectRequestId(ID), true);
		for (const bad of ['', 'short', `${ID}x`, `${ID.slice(0, 42)}/`]) assert.equal(isConnectRequestId(bad), false, bad);
		assert.equal(parseConnectRequest(page(), ID)?.project_name, 'Lab');
		assert.equal(parseConnectRequest(page({ request_id: 'other' }), ID), null);
		assert.equal(parseConnectRequest(page({ registry: 'docker.io' }), ID), null);
		assert.equal(parseConnectRequest(page({ state: 'done' }), ID), null);
		assert.equal(parseConnectRequest(page({ current_login: { source: 'other', username: 'x' } }), ID), null);
		assert.equal(parseConnectRequest(page({ current_login: undefined }), ID), null);
		assert.equal(parseConnectRequest([], ID), null);
		// A planted token-shaped field is dropped.
		assert.equal(JSON.stringify(parseConnectRequest(page({ token: LEAK, current_login: { source: 'github', username: 'octo', token: LEAK } }), ID)).includes(LEAK), false);
		assert.equal(connectView('pending'), 'form');
		assert.equal(connectView('connected'), 'connected');
		assert.equal(connectView('expired'), 'expired');
		assert.equal(replaceNote(null), null);
		assert.equal(replaceNote({ source: 'github', username: 'octo' }), 'This replaces the current ghcr.io login (connected as octo).');
		assert.equal(replaceNote({ source: 'manual', username: 'bot' }), 'This replaces the current ghcr.io login (a hand-typed login).');
		assert.equal(connectedText({ source: 'github', username: 'octo' }), 'Connected to GitHub as octo. You can close this tab.');
		assert.equal(connectedText(null), 'Connected to GitHub. You can close this tab.');
	});

	test('signed out goes to sign-in and back to the same page', async () => {
		const calls = installFetch(() => assert.fail('no API call when signed out'));
		await assert.rejects(
			async () => {
				await load(connectEvent({ user: false }).event as never);
			},
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/signin?next=${encodeURIComponent(`/connect/${ID}`)}`
		);
		assert.equal(safeNext(`/connect/${ID}`), `/connect/${ID}`);
		assert.equal(calls.length, 0);
	});

	test('a pending request shows the project, the note as text, the current login and two buttons', async () => {
		const reason = '<a href="https://evil.test">click</a> <script>x</script>';
		const calls = installFetch((call) => {
			assert.equal(call.method, 'GET');
			assert.equal(call.url.pathname, `/v1/registry-connect-requests/${ID}/page`);
			assert.equal(call.authorization, `Bearer ${TOKEN}`);
			return envelope(page({ reason, current_login: { source: 'github', username: 'octo' } }));
		});
		const loaded = data(await load(connectEvent().event as never));
		assert.equal(loaded.view, 'form');
		assert.equal(calls.length, 1);
		const html = htmlOf(loaded);
		assert.match(html, /Connect GitHub/);
		assert.match(html, /Lab/);
		assert.match(html, /ghcr\.io/);
		assert.match(html, /This replaces the current ghcr\.io login \(connected as octo\)\./);
		assert.match(html, /Note from the agent/);
		assert.equal(html.includes('<a href="https://evil.test"'), false);
		assert.equal(html.includes('<script>x'), false);
		assert.match(html, /&lt;a href="https:\/\/evil.test">click&lt;\/a>/);
		const cont = html.match(/<form[^>]*action="\?\/continue"[^>]*>([\s\S]*?)<\/form>/);
		assert.ok(cont, 'continue form');
		assert.match(cont[1], /Continue to GitHub/);
		assert.equal(cont[1].includes('<input'), false);
		const decline = html.match(/<form[^>]*action="\?\/decline"[^>]*>([\s\S]*?)<\/form>/);
		assert.ok(decline, 'decline form');
		assert.match(decline[1], />Decline</);
		// It never redirects on its own.
		assert.equal(/http-equiv="refresh"|location\.|window\.open/i.test(html), false);
		assert.equal(html.includes('github.com/login'), false);

		installFetch(() => envelope(page({ current_login: { source: 'manual', username: 'bot' } })));
		assert.match(htmlOf(data(await load(connectEvent().event as never))), /\(a hand-typed login\)/);
	});

	test('a GitHub round trip that did not connect shows its note and keeps the buttons', async () => {
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(connectEvent({ query: '?github=denied' }).event as never)));
		assert.match(html, /GitHub sign-in was cancelled/);
		assert.match(html, /Continue to GitHub/);
		installFetch(() => envelope(page()));
		const odd = data(await load(connectEvent({ query: '?github=%3Cb%3E' }).event as never));
		assert.equal(odd.note, null);
	});

	test('connected, declined, expired, other user, bad link and errors show a message and no buttons', async () => {
		const cases: Array<[Response, string]> = [
			[envelope(page({ state: 'connected', current_login: { source: 'github', username: 'octo' } })), 'Connected to GitHub as octo. You can close this tab.'],
			[envelope(page({ state: 'declined' })), CONNECT_TEXT.declined],
			[envelope(page({ state: 'expired' })), CONNECT_TEXT.expired],
			[envelope(null, 404, 'not_found', 'not found'), CONNECT_TEXT.missing],
			[envelope(page({ registry: 'x' })), CONNECT_TEXT.unexpected]
		];
		for (const [response, text] of cases) {
			installFetch(() => response.clone());
			const html = htmlOf(data(await load(connectEvent().event as never)));
			assert.equal(html.includes('<form'), false, text);
			assert.match(html, new RegExp(text.slice(0, 30).replace(/[.?()]/g, '\\$&')), text);
		}
		const calls = installFetch(() => assert.fail('no API call for a bad id'));
		const bad = data(await load(connectEvent({ id: 'not-an-id' }).event as never));
		assert.equal(bad.view, 'invalid');
		assert.match(htmlOf(bad), /This link is not valid/);
		assert.equal(calls.length, 0);
	});

	test('Continue starts Connect GitHub with the request id and goes to github.com', async () => {
		const calls = installFetch((call) => {
			if (call.method === 'GET') return envelope(page());
			assert.equal(call.url.pathname, `/v1/projects/${PROJECT.id}/registries/github/start`);
			return envelope({ authorize_url: AUTHORIZE });
		});
		const { event, sets } = connectEvent({ form: {} });
		const err = await (actions.continue(event as never) as Promise<unknown>).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		assert.deepEqual(redirectOf(err), { status: 303, location: AUTHORIZE });
		assert.equal(calls.length, 2);
		assert.deepEqual(JSON.parse(calls[1].body ?? ''), { connect_request_id: ID });
		assert.equal(sets.some((op) => op.op === 'set' && op.name === 'bot_github_connect' && op.value === ID), true);
		assert.equal(sets.some((op) => op.op === 'set' && op.name === 'bot_github_return' && op.value === PROJECT.id), true);
	});

	test('Continue refusals: not configured, used, expired, missing, and a non-GitHub URL', async () => {
		const cases: Array<[Response, string, boolean]> = [
			[envelope(null, 503, 'unavailable', 'GitHub is not configured'), CONNECT_TEXT.off, true],
			// A transient unavailable (the sign-in store is full) keeps the buttons.
			[envelope(null, 503, 'unavailable', 'unavailable'), 'unavailable', false],
			[envelope(null, 409, 'conflict', 'this request was already used'), 'this request was already used', true],
			[envelope(null, 409, 'conflict', 'this request has expired; ask the agent for a new one'), 'this request has expired; ask the agent for a new one', true],
			[envelope(null, 404, 'not_found', 'not found'), CONNECT_TEXT.missing, true],
			[envelope({ authorize_url: 'https://evil.test/login/oauth/authorize' }), CONNECT_TEXT.unexpected, false]
		];
		for (const [response, text, closed] of cases) {
			installFetch((call) => (call.method === 'GET' ? envelope(page()) : response.clone()));
			const { event, sets } = connectEvent({ form: {} });
			const result = await actions.continue(event as never);
			assert.ok(isActionFailure(result), text);
			const body = result.data as unknown as { message: string; closed: boolean };
			assert.equal(body.message, text);
			assert.equal(body.closed, closed, text);
			assert.equal(sets.length, 0, 'no cookie on a refusal');
		}
		const calls = installFetch(() => assert.fail('no API call for a bad id'));
		assert.ok(isActionFailure(await actions.continue(connectEvent({ id: 'bad', form: {} }).event as never)));
		assert.equal(calls.length, 0);
	});

	test('Decline posts its own empty form and closes the page', async () => {
		const calls = installFetch((call) => {
			assert.equal(call.method, 'POST');
			assert.equal(call.url.pathname, `/v1/registry-connect-requests/${ID}/decline`);
			return envelope({ state: 'declined' });
		});
		const result = await actions.decline(connectEvent({ form: {} }).event as never);
		assert.deepEqual(result, { declined: true });
		assert.equal(calls.length, 1);
		installFetch(() => envelope(page({ state: 'declined' })));
		const html = htmlOf(data(await load(connectEvent().event as never)), result);
		assert.match(html, /Declined\. The agent will see that you declined\./);
		assert.equal(html.includes('<form'), false);

		installFetch(() => envelope(null, 409, 'conflict', 'this request was already used'));
		const refused = await actions.decline(connectEvent({ form: {} }).event as never);
		assert.ok(isActionFailure(refused));
		installFetch(() => envelope(page()));
		const after = htmlOf(data(await load(connectEvent().event as never)), refused.data);
		assert.match(after, /this request was already used/);
		assert.equal(after.includes('<form'), false);
	});

	async function callback(url: string, reply: () => Response, cookies: Record<string, string>) {
		installFetch(reply);
		const { event, jar, sets } = fakeEvent({ url, user: USER, session: TOKEN });
		for (const [name, value] of Object.entries(cookies)) jar.set(name, value);
		const err = await (githubCallback(event as never) as Promise<Response>).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		return { err, sets };
	}

	test('the GitHub callback returns to /connect/<id> for a request, and to Settings without one', async () => {
		const ok = () => envelope({ project_id: PROJECT.id, host: 'ghcr.io', username: 'octo', source: 'github', connect_request_id: ID });
		const both = { bot_github_return: PROJECT.id, bot_github_connect: ID };
		let res = await callback(`http://console.example/auth/github/callback?code=abc&state=${STATE}`, ok, both);
		assert.deepEqual(redirectOf(res.err), { status: 303, location: `/connect/${ID}?github=connected` });
		assert.equal(res.sets.some((op) => op.op === 'delete' && op.name === 'bot_github_connect'), true);
		// The API answer wins even without the cookie.
		res = await callback(`http://console.example/auth/github/callback?code=abc&state=${STATE}`, ok, { bot_github_return: PROJECT.id });
		assert.equal(redirectOf(res.err).location, `/connect/${ID}?github=connected`);
		// A failure goes back to the connect page with a short code.
		res = await callback(`http://console.example/auth/github/callback?error=access_denied&state=${STATE}`, () => assert.fail('api called'), both);
		assert.equal(redirectOf(res.err).location, `/connect/${ID}?github=denied`);
		res = await callback(
			`http://console.example/auth/github/callback?code=abc&state=${STATE}`,
			() => envelope(null, 400, 'bad_request', 'this GitHub sign-in expired or was already used; start again'),
			both
		);
		assert.equal(redirectOf(res.err).location, `/connect/${ID}?github=expired`);
		const failures: Array<[Response, string]> = [
			[envelope(null, 400, 'bad_request', 'GitHub did not grant read:packages'), 'scope'],
			[envelope(null, 403, 'forbidden', 'forbidden'), 'forbidden'],
			[envelope(null, 502, 'bad_gateway', 'GitHub sign-in failed; try again'), 'failed'],
			[envelope(null, 503, 'unavailable', 'GitHub is not configured'), 'off']
		];
		for (const [reply, code] of failures) {
			res = await callback(`http://console.example/auth/github/callback?code=abc&state=${STATE}`, () => reply.clone(), both);
			assert.equal(redirectOf(res.err).location, `/connect/${ID}?github=${code}`, code);
		}
		res = await callback(`http://console.example/auth/github/callback?code=abc&state=short`, () => assert.fail('api called'), both);
		assert.equal(redirectOf(res.err).location, `/connect/${ID}?github=expired`);
		// No request: v1.29 unchanged.
		res = await callback(
			`http://console.example/auth/github/callback?code=abc&state=${STATE}`,
			() => envelope({ project_id: PROJECT.id, host: 'ghcr.io', username: 'octo', source: 'github' }),
			{ bot_github_return: PROJECT.id }
		);
		assert.equal(redirectOf(res.err).location, `/projects/${PROJECT.id}?tab=settings&github=connected#registries`);
		// A malformed connect cookie is ignored.
		res = await callback(`http://console.example/auth/github/callback?error=access_denied`, () => assert.fail('api called'), {
			bot_github_return: PROJECT.id,
			bot_github_connect: '../evil'
		});
		assert.equal(redirectOf(res.err).location, `/projects/${PROJECT.id}?tab=settings&github=denied#registries`);
	});

	test('a Settings Connect GitHub drops a leftover connect cookie, so it returns to Settings', async () => {
		const calls = installFetch(() => envelope({ authorize_url: AUTHORIZE }));
		const { event, jar, sets } = fakeEvent({
			url: `http://console.example/projects/${PROJECT.id}?tab=settings&/connectGitHub`,
			form: {},
			params: { id: PROJECT.id },
			user: USER,
			session: TOKEN
		});
		jar.set('bot_github_connect', ID);
		const err = await (projectActions.connectGitHub(event as never) as Promise<unknown>).then(
			() => assert.fail('no redirect'),
			(e: unknown) => e
		);
		assert.equal(redirectOf(err).location, AUTHORIZE);
		assert.equal(calls[0].body, '{}');
		assert.equal(sets.some((op) => op.op === 'delete' && op.name === 'bot_github_connect'), true);
	});

	test('a transient Continue failure keeps the buttons under its message', async () => {
		installFetch((call) => (call.method === 'GET' ? envelope(page()) : envelope(null, 503, 'unavailable', 'unavailable')));
		const failed = await actions.continue(connectEvent({ form: {} }).event as never);
		assert.ok(isActionFailure(failed));
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(connectEvent().event as never)), failed.data);
		assert.match(html, /unavailable/);
		assert.match(html, /Continue to GitHub/);
		assert.match(html, /id="connect-result"/);
	});

	test('connected after the request ended: the login is saved, no buttons', async () => {
		for (const state of ['expired', 'declined']) {
			installFetch(() => envelope(page({ state, current_login: { source: 'github', username: 'octo' } })));
			const loaded = data(await load(connectEvent({ query: '?github=connected' }).event as never));
			assert.equal(loaded.connectedEnded, true, state);
			const html = htmlOf(loaded);
			assert.match(html, /GitHub was connected, but this request had already ended\. The login is saved\./, state);
			assert.equal(html.includes(CONNECT_TEXT[state as 'expired' | 'declined']), false, state);
			assert.equal(html.includes('<form'), false, state);
			// Without the code the page reads as the request's own state.
			installFetch(() => envelope(page({ state })));
			const plain = data(await load(connectEvent().event as never));
			assert.equal(plain.connectedEnded, false, state);
			assert.match(htmlOf(plain), new RegExp(CONNECT_TEXT[state as 'expired' | 'declined'].slice(0, 25).replace(/[.]/g, '\\.')), state);
		}
		installFetch(() => envelope(page({ state: 'connected', current_login: { source: 'github', username: 'octo' } })));
		const html = htmlOf(data(await load(connectEvent({ query: '?github=connected' }).event as never)));
		assert.match(html, /Connected to GitHub as octo\. You can close this tab\./);
		assert.equal(html.includes('already ended'), false);
	});

	test('every ?github= return code reads right on the page', async () => {
		for (const code of Object.keys(GITHUB_NOTES) as Array<keyof typeof GITHUB_NOTES>) {
			// A pending request: a failure code shows its note above the buttons; connected shows nothing extra.
			installFetch(() => envelope(page()));
			const loaded = data(await load(connectEvent({ query: `?github=${code}` }).event as never));
			const html = htmlOf(loaded);
			assert.match(html, /Continue to GitHub/, code);
			if (code === 'connected') {
				assert.equal(loaded.note, null, code);
				assert.equal(loaded.connectedEnded, false, code);
			} else {
				assert.deepEqual(loaded.note, GITHUB_NOTES[code], code);
				assert.ok(html.includes(GITHUB_NOTES[code].text), code);
			}
			// An ended request: only connected says the login is saved; failure codes add no note.
			installFetch(() => envelope(page({ state: 'expired' })));
			const ended = data(await load(connectEvent({ query: `?github=${code}` }).event as never));
			assert.equal(ended.note, null, code);
			assert.equal(ended.connectedEnded, code === 'connected', code);
		}
		// The codes the callback sends to a connect page are all in the list.
		for (const code of ['connected', 'expired', 'scope', 'failed', 'forbidden', 'denied', 'off']) assert.ok(Object.hasOwn(GITHUB_NOTES, code), code);
	});
});
