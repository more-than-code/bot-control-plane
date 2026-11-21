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
	for (const path of ['/', '/projects', '/invite/abc', '/fill/abc', '/fill-file/abc', '/signin', '/about', '/orgs/x', '/hosts/x', '/connectx', '/projectsx/1', '/device', '/oauth/consent', '/auth/github/callback', '/auth/google/callback']) {
		assert.equal(referrerPolicyFor(path), 'no-referrer', path);
	}
});

// The header sets only the policy of the document the server sent; a SvelteKit client-side navigation keeps it.
// Signed in on `/projects` (no-referrer), opening a project and its Settings by link, then Reconnect GitHub, posted
// `Origin: null` and SvelteKit answered "Cross-site POST form submissions are forbidden" until a reload. The root
// layout now renders the route's policy as a meta tag, which the browser applies on every client navigation.
test('the root layout renders the route’s policy as <meta name="referrer"> on every navigation', async () => {
	const { render } = await import('svelte/server');
	const { createRawSnippet } = await import('svelte');
	const { default: RootLayout } = await import('../src/routes/+layout.svelte');
	const { setPageUrl, setRouteId } = await import('./stubs/app-state.ts');
	const { referrerPolicyFor: shared } = await import('../src/lib/referrer-policy.ts');
	assert.equal(shared, referrerPolicyFor, 'one rule for the header and the meta');
	const children = createRawSnippet(() => ({ render: () => '<p>child</p>' }));
	const data = { user: null, orgs: [], projects: [], theme: 'system' as const, timeZone: 'UTC', whatsNew: null };
	const cases: Array<[string, string, string]> = [
		['http://console.example/projects/0651af64-8c28-41c5-8ff6-be2231d517bc?tab=settings', '/projects/[id]', 'same-origin'],
		['http://console.example/connect/QG5nHqySbN4JqUc_Geex8Ex74J5W-XVnV_CVha0t5PQ', '/connect/[id]', 'same-origin'],
		['http://console.example/projects', '/projects', 'no-referrer'],
		['http://console.example/invite/abc', '/invite/[token]', 'no-referrer'],
		['http://console.example/device?code=ABCD-EFGH', '/device', 'no-referrer'],
		['http://console.example/oauth/consent?request=x', '/oauth/consent', 'no-referrer'],
		['http://console.example/auth/github/callback?code=x&state=y', '/auth/github/callback', 'no-referrer']
	];
	for (const [url, route, policy] of cases) {
		setPageUrl(url);
		setRouteId(route);
		const head = render(RootLayout, { props: { children, data } }).head;
		const metas = head.match(/<meta name="referrer"[^>]*>/g) ?? [];
		assert.equal(metas.length, 1, url);
		assert.match(metas[0] ?? '', new RegExp(`content="${policy}"`), url);
	}
});
