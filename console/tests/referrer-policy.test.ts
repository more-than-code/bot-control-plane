import assert from 'node:assert/strict';
import { test } from 'node:test';
import { referrerPolicyFor } from '../src/hooks.server.ts';

// A native (non-enhanced) form POST from a page served with `no-referrer` carries
// `Origin: null`, which SvelteKit's production CSRF check refuses ("Cross-site POST
// form submissions are forbidden"). The Connect GitHub forms must post natively so
// the browser follows the redirect to github.com, so their pages use `same-origin`:
// the console still gets the real Origin, and github.com still gets no Referer.
test('pages with a native POST to github.com use same-origin', () => {
	for (const path of ['/projects/0651af64-8c28-41c5-8ff6-be2231d517bc', '/connect/QG5nHqySbN4JqUc_Geex8Ex74J5W-XVnV_CVha0t5PQ']) {
		assert.equal(referrerPolicyFor(path), 'same-origin', path);
	}
});

test('every other path keeps no-referrer', () => {
	for (const path of ['/', '/projects', '/invite/abc', '/fill/abc', '/fill-file/abc', '/signin', '/about', '/orgs/x', '/hosts/x', '/connectx', '/projectsx/1']) {
		assert.equal(referrerPolicyFor(path), 'no-referrer', path);
	}
});
