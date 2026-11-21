/**
 * The console's Referrer-Policy per path. `no-referrer` everywhere, except the pages whose Connect GitHub forms post
 * natively (so the browser follows the redirect to github.com). Under `no-referrer` a native form POST carries
 * `Origin: null`, which SvelteKit's CSRF check refuses in production. `same-origin` sends the real Origin to the
 * console and still no Referer to github.com or any other site.
 *
 * The header only sets the policy of a document the server sent. A client-side navigation keeps the first document's
 * policy, so the root layout also renders it as `<meta name="referrer">`, which the browser applies on every
 * navigation (a `no-referrer` page such as `/projects` → a project's Settings → Reconnect GitHub used to post
 * `Origin: null`).
 */
export type ReferrerPolicy = 'no-referrer' | 'same-origin';

export function referrerPolicyFor(pathname: string): ReferrerPolicy {
	return pathname.startsWith('/projects/') || pathname.startsWith('/connect/') ? 'same-origin' : 'no-referrer';
}
