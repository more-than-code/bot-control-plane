import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { after, before, describe, test } from 'node:test';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { render } from 'svelte/server';
import {
	decodeBase64Utf8,
	decodeLocalText,
	encodeBase64Utf8,
	FILE_MAX_BASE64,
	FILE_FILL_TEXT,
	FILE_MAX_BYTES,
	fileFillBody,
	isFileExistsMessage,
	isFileRequestId,
	NO_PICK,
	parseFileFillRequest,
	pickOutcome,
	secretEditable,
	secretFlag,
	submitOnce,
	pastedText,
	postedContent,
	readLocalTextFile
} from '../src/lib/file-request.ts';
import { safeNext } from '../src/lib/server/session.ts';
import { actions, load } from '../src/routes/fill-file/[id]/+page.server.ts';
import FillFilePage from '../src/routes/fill-file/[id]/+page.svelte';
import { envelope, fakeEvent, installFetch, PROJECT, restoreEnv, TOKEN, useApi, USER } from './harness.ts';

// api-v1 v1.30 items 4–6, spec-file-request §3.3 and AC 2, 4, 5, 7.

const ID = 'FiLeReQuEsT0123456789abcdefghijklmnop-_ABCD';
const CONTENT = 'FILE-CONTENT-do-not-echo\nserver { listen 80; }\n';
const originalFetch = globalThis.fetch;

function page(overrides: Record<string, unknown> = {}): Record<string, unknown> {
	return {
		request_id: ID,
		project_id: PROJECT.id,
		project_name: 'Lab',
		name: 'nginx.conf',
		secret: true,
		state: 'pending',
		expires_at: '2026-11-16T10:15:00Z',
		created_at: '2026-11-16T10:00:00Z',
		reason: 'The proxy needs its config',
		...overrides
	};
}

function fillEvent(input: { id?: string; user?: boolean; form?: Record<string, string> } = {}) {
	const id = input.id ?? ID;
	return fakeEvent({
		url: `http://console.example/fill-file/${id}`,
		session: input.user === false ? undefined : TOKEN,
		user: input.user === false ? null : USER,
		projects: [PROJECT],
		params: { id },
		form: input.form
	}).event;
}

function data<T>(value: T): Exclude<T, void | undefined | null> {
	if (value == null) assert.fail('missing data');
	return value as Exclude<T, void | undefined | null>;
}

function htmlOf(loaded: unknown, form: unknown = null): string {
	return render(FillFilePage, { props: { data: loaded, form } as never }).body;
}

const SOURCE = readFileSync(new URL('../src/routes/fill-file/[id]/+page.svelte', import.meta.url), 'utf8');

describe('file request fill page', () => {
	before(() => useApi('https://api.test'));
	after(() => {
		globalThis.fetch = originalFetch;
		restoreEnv();
	});

	test('ids, shapes and bodies fail closed', () => {
		assert.equal(ID.length, 43);
		assert.equal(isFileRequestId(ID), true);
		for (const bad of ['', 'short', `${ID}x`, `${ID.slice(0, 42)}/`]) assert.equal(isFileRequestId(bad), false, bad);
		assert.equal(parseFileFillRequest(page(), ID)?.name, 'nginx.conf');
		assert.equal(parseFileFillRequest(page(), ID)?.secret, true);
		assert.equal(parseFileFillRequest(page({ request_id: 'other' }), ID), null);
		assert.equal(parseFileFillRequest(page({ state: 'done' }), ID), null);
		assert.equal(parseFileFillRequest(page({ name: 'Upper.conf' }), ID), null);
		assert.equal(parseFileFillRequest(page({ secret: 'yes' }), ID), null);
		assert.equal(parseFileFillRequest(page({ reason: 7 }), ID), null);
		assert.equal(parseFileFillRequest([], ID), null);
		assert.deepEqual(fileFillBody(CONTENT, '1'), { ok: true, body: { content: CONTENT, secret: true } });
		assert.deepEqual(fileFillBody(CONTENT, '0'), { ok: true, body: { content: CONTENT, secret: false } });
		assert.deepEqual(fileFillBody(CONTENT, ''), { ok: true, body: { content: CONTENT } });
		assert.deepEqual(fileFillBody('', '1'), { ok: false, message: FILE_FILL_TEXT.noContent });
		assert.deepEqual(fileFillBody('a\0b', '1'), { ok: false, message: FILE_FILL_TEXT.notText });
		assert.deepEqual(fileFillBody('é'.repeat(FILE_MAX_BYTES / 2 + 1), '1'), { ok: false, message: FILE_FILL_TEXT.notText });
		assert.equal(fileFillBody('a'.repeat(FILE_MAX_BYTES), '1').ok, true);
	});

	test('a picked file is read as UTF-8 text, or refused and nothing read in', async () => {
		const enc = new TextEncoder();
		assert.equal(decodeLocalText(enc.encode(CONTENT)), CONTENT);
		assert.equal(decodeLocalText(enc.encode('héllo ✓')), 'héllo ✓');
		// A byte-order mark is kept: the file is stored as it is on disk.
		assert.equal(decodeLocalText(new Uint8Array([0xef, 0xbb, 0xbf, 0x61])), '﻿a');
		// Too large, binary (NUL), and not UTF-8.
		assert.equal(decodeLocalText(new Uint8Array(FILE_MAX_BYTES + 1).fill(0x61)), null);
		assert.equal(decodeLocalText(new Uint8Array([0x61, 0x00, 0x62])), null);
		assert.equal(decodeLocalText(new Uint8Array([0xff, 0xfe, 0x61])), null);
		assert.equal(decodeLocalText(new Uint8Array(FILE_MAX_BYTES).fill(0x61))?.length, FILE_MAX_BYTES);
		assert.equal(await readLocalTextFile(new Blob([CONTENT])), CONTENT);
		assert.equal(await readLocalTextFile(new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x00, 0x01])])), null);
		// The size is checked before any byte is read.
		let read = false;
		const big = { size: FILE_MAX_BYTES + 1, arrayBuffer: async () => ((read = true), new ArrayBuffer(0)) } as unknown as Blob;
		assert.equal(await readLocalTextFile(big), null);
		assert.equal(read, false);
		// The page state after a pick: a good file is picked exactly; a refused one reads nothing in,
		// drops an earlier pick (back to paste mode) and shows the error.
		assert.equal(FILE_FILL_TEXT.notText, 'This file is too large or not text');
		assert.deepEqual(pickOutcome('app.conf', 'a\r\nb'), { picked: { name: 'app.conf', text: 'a\r\nb' }, error: null });
		assert.deepEqual(pickOutcome('photo.png', null), { picked: null, error: FILE_FILL_TEXT.notText });
		// Clear file is paste mode with no error.
		assert.deepEqual(NO_PICK, { picked: null, error: null });
	});

	test('a picked file round-trips byte-exact through content_b64; typed text gets LF', async () => {
		const lf = 'a: 1\nb: 2\n';
		const crlf = 'a: 1\r\nb: 2\r\n';
		const unicode = '\ufeffhéllo ✓\r\nzwei\n';
		for (const text of [lf, crlf, unicode]) {
			assert.equal(decodeBase64Utf8(encodeBase64Utf8(text)), text);
			assert.equal(postedContent(encodeBase64Utf8(text), ''), text);
		}
		// A picked file of exactly 64 KiB fits; one byte more does not.
		const full = 'x'.repeat(FILE_MAX_BYTES);
		assert.equal(encodeBase64Utf8(full).length, FILE_MAX_BASE64);
		assert.equal(decodeBase64Utf8(encodeBase64Utf8(full)), full);
		assert.equal(decodeBase64Utf8(encodeBase64Utf8(`${full}x`)), null);
		// Malformed, too long, NUL and not UTF-8 are refused.
		for (const bad of ['not base64!', 'abc', 'ab=c', 'YQ', 'A'.repeat(FILE_MAX_BASE64 + 4), btoa('a\0b'), btoa('\xff\xfe')]) {
			assert.equal(decodeBase64Utf8(bad), null, bad.slice(0, 20));
		}
		// Typed or pasted text: CRLF becomes LF; a lone LF stays.
		assert.equal(pastedText('one\r\ntwo\r\n'), 'one\ntwo\n');
		assert.equal(postedContent('', 'one\r\ntwo\nthree'), 'one\ntwo\nthree');

		// Through the save action: the API gets the exact bytes of an LF and a CRLF file, and LF for paste.
		const bodies: string[] = [];
		installFetch((call) => {
			bodies.push(JSON.parse(call.body ?? '').content);
			return envelope({ state: 'filled', secret: true });
		});
		await actions.save(fillEvent({ form: { content_b64: encodeBase64Utf8(lf), content: '', secret: '1' } }) as never);
		await actions.save(fillEvent({ form: { content_b64: encodeBase64Utf8(crlf), secret: '1' } }) as never);
		await actions.save(fillEvent({ form: { content: 'line one\r\nline two\r\n', secret: '1' } }) as never);
		assert.deepEqual(bodies, [lf, crlf, 'line one\nline two\n']);

		// A malformed or too large content_b64 is refused before the API.
		for (const bad of ['%%%', encodeBase64Utf8('y'.repeat(FILE_MAX_BYTES + 1)), 'A'.repeat(FILE_MAX_BASE64 + 4)]) {
			const calls = installFetch(() => assert.fail('refused content_b64 is not sent'));
			const refused = await actions.save(fillEvent({ form: { content_b64: bad, secret: '1' } }) as never);
			assert.ok(isActionFailure(refused));
			assert.equal((refused.data as unknown as { message: string }).message, FILE_FILL_TEXT.notText);
			assert.equal(calls.length, 0);
		}
	});

	test('signed out goes to sign-in and back to the same page', async () => {
		const calls = installFetch(() => assert.fail('no API call when signed out'));
		await assert.rejects(
			async () => {
				await load(fillEvent({ user: false }) as never);
			},
			(err: unknown) => isRedirect(err) && err.status === 303 && err.location === `/signin?next=${encodeURIComponent(`/fill-file/${ID}`)}`
		);
		assert.equal(safeNext(`/fill-file/${ID}`), `/fill-file/${ID}`);
		assert.equal(calls.length, 0);
	});

	test('a pending secret request shows its facts, the note as text, and the form', async () => {
		const reason = '<a href="https://evil.test">click</a> <script>x</script>';
		const calls = installFetch((call) => {
			assert.equal(call.method, 'GET');
			assert.equal(call.url.pathname, `/v1/file-requests/${ID}/fill`);
			assert.equal(call.authorization, `Bearer ${TOKEN}`);
			return envelope(page({ reason }));
		});
		const loaded = data(await load(fillEvent() as never));
		assert.equal(loaded.view, 'form');
		assert.equal(calls.length, 1);
		const html = htmlOf(loaded);
		assert.match(html, /Add a file/);
		assert.match(html, /Lab/);
		assert.match(html, /nginx\.conf/);
		assert.match(html, /Secret \(stored encrypted\)/);
		assert.match(html, /Note from the agent/);
		assert.equal(html.includes('<a href="https://evil.test"'), false);
		assert.equal(html.includes('<script>x'), false);
		assert.match(html, /&lt;a href="https:\/\/evil.test">click&lt;\/a>/);
		assert.match(html, /<textarea[^>]*name="content"/);
		// The file picker has no name: the picked file itself is never posted.
		const picker = html.match(/<input[^>]*type="file"[^>]*>/)?.[0] ?? '';
		assert.ok(picker, 'file input');
		assert.equal(/\bname=/.test(picker), false);
		// A secret request: the checkbox is checked and disabled, and the form posts secret=1.
		const box = html.match(/<button[^>]*id="fill-secret"[^>]*>/)?.[0] ?? '';
		assert.match(box, /aria-checked="true"|data-state="checked"/);
		assert.match(box, /\sdisabled=""/);
		assert.match(html, /<input[^>]*type="hidden"[^>]*name="secret"[^>]*value="1"|<input[^>]*name="secret"[^>]*type="hidden"[^>]*value="1"|<input[^>]*value="1"[^>]*name="secret"/);
		assert.match(html, /cannot be made plain/);
		// Decline submits its own empty form.
		assert.match(html, /<button[^>]*form="fill-decline"[^>]*>/);
		const declineForm = html.match(/<form[^>]*id="fill-decline"[^>]*>([\s\S]*?)<\/form>/);
		assert.ok(declineForm, 'decline form');
		assert.match(declineForm[0], /action="\?\/decline"/);
		assert.equal(declineForm[1].includes('<input') || declineForm[1].includes('<textarea'), false);
	});

	test('a plain request leaves the checkbox open, and the form posts secret=0', async () => {
		installFetch(() => envelope(page({ secret: false })));
		const html = htmlOf(data(await load(fillEvent() as never)));
		assert.match(html, />Plain</);
		const box = html.match(/<button[^>]*id="fill-secret"[^>]*>/)?.[0] ?? '';
		assert.match(box, /aria-checked="false"|data-state="unchecked"/);
		assert.equal(/\sdisabled=""/.test(box), false);
		assert.equal(html.includes('cannot be made plain'), false);
		const hidden = html.match(/<input[^>]*name="secret"[^>]*>/)?.[0] ?? '';
		assert.match(hidden, /value="0"/);
	});

	test('the secret flag: a secret request stays secret; a plain one only becomes secret', () => {
		assert.equal(secretFlag(true, false), true);
		assert.equal(secretFlag(true, true), true);
		assert.equal(secretFlag(false, false), false);
		assert.equal(secretFlag(false, true), true);
		assert.equal(secretEditable(true), false);
		assert.equal(secretEditable(false), true);
	});

	test('used, expired, other user and a bad link show a message and no form', async () => {
		const cases: Array<[Response, string]> = [
			[envelope(page({ state: 'filled' })), FILE_FILL_TEXT.used],
			[envelope(page({ state: 'declined' })), FILE_FILL_TEXT.used],
			[envelope(page({ state: 'expired' })), FILE_FILL_TEXT.expired],
			[envelope(null, 404, 'not_found', 'not found'), FILE_FILL_TEXT.missing]
		];
		for (const [response, text] of cases) {
			installFetch(() => response.clone());
			const html = htmlOf(data(await load(fillEvent() as never)));
			assert.equal(html.includes('<form'), false, text);
			assert.match(html, new RegExp(text.slice(0, 30).replace(/[.?]/g, '\\$&')), text);
		}
		const calls = installFetch(() => assert.fail('no API call for a bad id'));
		const bad = data(await load(fillEvent({ id: 'not-an-id' }) as never));
		assert.equal(bad.view, 'invalid');
		assert.equal(htmlOf(bad).includes('<form'), false);
		assert.equal(calls.length, 0);
	});

	test('save posts the content once and never returns it', async () => {
		const calls = installFetch((call) => {
			assert.equal(call.method, 'POST');
			assert.equal(call.url.pathname, `/v1/file-requests/${ID}/fill`);
			return envelope({ state: 'filled', secret: true });
		});
		const result = await actions.save(fillEvent({ form: { content: CONTENT, secret: '1' } }) as never);
		assert.equal(calls.length, 1);
		assert.deepEqual(JSON.parse(calls[0].body ?? ''), { content: CONTENT, secret: true });
		assert.deepEqual(result, { saved: true, secret: true });
		assert.equal(JSON.stringify(result).includes('FILE-CONTENT'), false);
		installFetch(() => envelope(page({ state: 'filled' })));
		const html = htmlOf(data(await load(fillEvent() as never)), result);
		assert.match(html, /Saved\. You can close this tab\./);
		assert.equal(html.includes('already used'), false);
		assert.equal(html.includes('<form'), false);
		assert.equal(html.includes('FILE-CONTENT'), false);
		// A plain fill made secret posts secret true; a plain one false.
		const plain = installFetch(() => envelope({ state: 'filled', secret: false }));
		await actions.save(fillEvent({ form: { content: CONTENT, secret: '0' } }) as never);
		assert.equal(JSON.parse(plain[0].body ?? '').secret, false);
	});

	test('save failures keep the API text, never echo the content, and keep the form unless closed', async () => {
		const cases: Array<[Response, string, boolean]> = [
			[envelope(null, 409, 'conflict', 'this request was already used'), 'this request was already used', true],
			[envelope(null, 409, 'conflict', 'this request has expired; ask the agent for a new one'), 'this request has expired; ask the agent for a new one', true],
			[envelope(null, 409, 'conflict', 'file nginx.conf already exists; replace it in the console'), 'file nginx.conf already exists; replace it in the console', false],
			[envelope(null, 404, 'not_found', 'not found'), FILE_FILL_TEXT.missing, true],
			[envelope(null, 400, 'bad_request', 'a secret file cannot be made plain'), 'a secret file cannot be made plain', false],
			[envelope(null, 400, 'bad_request', 'invalid content'), 'invalid content', false]
		];
		for (const [response, text, closed] of cases) {
			installFetch(() => response.clone());
			const result = await actions.save(fillEvent({ form: { content: CONTENT, secret: '1' } }) as never);
			assert.ok(isActionFailure(result));
			const failed = result.data as unknown as { message: string; closed: boolean };
			assert.equal(failed.message, text);
			assert.equal(failed.closed, closed, text);
			assert.equal(JSON.stringify(result).includes('FILE-CONTENT'), false);
		}
		for (const [form, message] of [
			[{ content: '', secret: '1' }, FILE_FILL_TEXT.noContent],
			[{ content: 'a\0b', secret: '1' }, FILE_FILL_TEXT.notText],
			[{ content: 'a'.repeat(FILE_MAX_BYTES + 1), secret: '1' }, FILE_FILL_TEXT.notText]
		] as const) {
			const calls = installFetch(() => assert.fail('refused content is not sent'));
			const refused = await actions.save(fillEvent({ form }) as never);
			assert.ok(isActionFailure(refused));
			assert.equal((refused.data as unknown as { message: string }).message, message);
			assert.equal(calls.length, 0);
		}
		// A kept form: the textarea stays, marked invalid and described by the error.
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(fillEvent() as never)), { message: 'a secret file cannot be made plain', closed: false });
		const area = html.match(/<textarea[^>]*>/)?.[0] ?? '';
		assert.match(area, /name="content"/);
		assert.match(area, /aria-invalid="true"/);
		assert.match(area, /aria-describedby="fill-error"/);
		assert.match(html, /<div id="fill-error">/);
		// Before any save the textarea is not marked.
		const fresh = htmlOf(data(await load(fillEvent() as never)));
		assert.equal(/<textarea[^>]*aria-invalid/.test(fresh), false);
		const closedHtml = htmlOf(data(await load(fillEvent() as never)), { message: 'this request was already used', closed: true });
		assert.equal(closedHtml.includes('<form'), false);
		assert.equal(closedHtml.includes('Note from the agent'), false);
		assert.match(closedHtml, /id="fill-result"[^>]*tabindex="-1"/);
	});

	test('decline posts no content and shows the declined notice', async () => {
		const calls = installFetch((call) => {
			if (call.method === 'GET') return envelope(page({ state: 'declined' }));
			assert.equal(call.url.pathname, `/v1/file-requests/${ID}/decline`);
			return envelope({ state: 'declined' });
		});
		// Even a decline post carrying a content field sends none to the API.
		const result = await actions.decline(fillEvent({ form: { content: CONTENT } }) as never);
		assert.deepEqual(result, { declined: true });
		const posts = calls.filter((call) => call.method === 'POST');
		assert.equal(posts.length, 1);
		assert.equal(posts[0].body, '{}');
		const html = htmlOf(data(await load(fillEvent() as never)), result);
		assert.match(html, /Declined\./);
		assert.equal(html.includes('<form'), false);
	});

	test('a load failure shows only its message', async () => {
		installFetch(() => envelope(null, 503, 'unavailable', 'API unavailable'));
		const loaded = data(await load(fillEvent() as never));
		assert.equal(loaded.view, 'error');
		const html = htmlOf(loaded);
		assert.equal(html.includes('<form'), false);
		assert.ok(loaded.message && html.includes(loaded.message.slice(0, 20)), 'message shown');
		installFetch(() => envelope({ unexpected: true }));
		const odd = data(await load(fillEvent() as never));
		assert.equal(odd.view, 'error');
		assert.equal(odd.message, FILE_FILL_TEXT.unexpected);
	});

	test('a file that exists by now hides the form and keeps Decline, which still posts no content', async () => {
		const msg = 'file nginx.conf already exists; replace it in the console';
		assert.equal(isFileExistsMessage(msg), true);
		assert.equal(isFileExistsMessage('this request was already used'), false);
		installFetch(() => envelope(null, 409, 'conflict', msg));
		const result = await actions.save(fillEvent({ form: { content: CONTENT, secret: '1' } }) as never);
		assert.ok(isActionFailure(result));
		assert.deepEqual(result.data, { message: msg, closed: false, exists: true });
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(fillEvent() as never)), result.data);
		assert.match(html, /already exists; replace it in the console/);
		assert.equal(html.includes('action="?/save"'), false);
		assert.equal(html.includes('<textarea'), false);
		assert.match(html, /<button[^>]*form="fill-decline"[^>]*>/);
		const declineForm = html.match(/<form[^>]*id="fill-decline"[^>]*>([\s\S]*?)<\/form>/);
		assert.ok(declineForm, 'decline form');
		assert.equal(declineForm[1].includes('<input') || declineForm[1].includes('<textarea'), false);
	});

	test('a save or decline posts once: a second submit while pending is cancelled', async () => {
		let pending = false;
		const states: boolean[] = [];
		const handler = submitOnce(
			() => pending,
			(next) => {
				pending = next;
				states.push(next);
			}
		);
		let cancelled = 0;
		const input = () => ({ cancel: () => (cancelled += 1) }) as never;
		const after = handler(input());
		assert.equal(typeof after, 'function');
		assert.equal(pending, true);
		// A second click while the first post is in flight never posts.
		assert.equal(handler(input()), undefined);
		assert.equal(cancelled, 1);
		let updated = 0;
		await (after as (arg: never) => Promise<void>)({ update: async () => void (updated += 1) } as never);
		assert.equal(updated, 1);
		assert.equal(pending, false);
		// A failed update still re-enables the buttons.
		const again = handler(input());
		await assert.rejects((again as (arg: never) => Promise<void>)({ update: async () => Promise.reject(new Error('x')) } as never));
		assert.equal(pending, false);
		assert.deepEqual(states, [true, false, true, false]);
		// Both buttons render disabled-able and use the guarded enhance.
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(fillEvent() as never)));
		assert.match(html, />Save</);
		assert.equal(/<button[^>]*type="submit"[^>]*disabled/.test(html), false);
	});

	test('accessibility: the checkbox is named by its label and described by the note; the pick is announced', async () => {
		installFetch(() => envelope(page()));
		const html = htmlOf(data(await load(fillEvent() as never)));
		const box = html.match(/<button[^>]*id="fill-secret"[^>]*>/)?.[0] ?? '';
		assert.equal(box.includes('aria-label='), false);
		assert.match(box, /aria-describedby="fill-secret-note"/);
		assert.match(html, /<p id="fill-secret-note"[^>]*>The agent asked for a secret file/);
		// Empty in paste mode (only hydration comments), so it announces a later pick.
		assert.match(html, /<p role="status"[^>]*>(<!--[^>]*-->)*<\/p>/);
		installFetch(() => envelope(page({ secret: false })));
		const plain = htmlOf(data(await load(fillEvent() as never)));
		assert.equal((plain.match(/<button[^>]*id="fill-secret"[^>]*>/)?.[0] ?? '').includes('aria-describedby'), false);
	});

	test('the page renders no raw HTML', () => {
		assert.equal(SOURCE.includes('{@html'), false);
	});
});
