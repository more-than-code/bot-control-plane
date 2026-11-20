// The site pages: route tree, layout, 404, titles, About content and the import allowlist. The site
// parts of console/tests/public-site.test.ts and seo-indexing.test.ts, adapted to the static site
// (umbrella docs/spec-site-pages.md): no hooks, no session, no noindex to strip.
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { basename, join } from 'node:path';
import { describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { createRawSnippet } from 'svelte';
import { render } from 'svelte/server';
import SiteLayout from '../src/routes/+layout.svelte';
import AboutPage from '../src/routes/+page.svelte';
import PrivacyPage from '../src/routes/privacy/+page.svelte';
import TermsPage from '../src/routes/terms/+page.svelte';
import NotFoundPage from '../src/routes/404/+page.svelte';
import SiteError from '../src/lib/site/SiteError.svelte';
import { LAST_UPDATED } from '../src/lib/legal/last-updated.ts';
import { SIGN_IN_URL, SITE_URL, SUPPORT_EMAIL } from '../src/lib/site/links.ts';

const root = fileURLToPath(new URL('..', import.meta.url));
const read = (path: string) => readFileSync(join(root, path), 'utf8');

function filesUnder(dir: string): string[] {
	return readdirSync(join(root, dir)).flatMap((name) => {
		const rel = `${dir}/${name}`;
		return statSync(join(root, rel)).isDirectory() ? filesUnder(rel) : [rel];
	});
}

/** Every source file the pages are built from. */
const PUBLIC_SOURCES = [...filesUnder('src/routes'), ...filesUnder('src/lib/site'), ...filesUnder('src/lib/legal')];

function title(head: string): string | undefined {
	return head.match(/<title>([^<]*)<\/title>/)?.[1];
}

function hrefs(html: string): string[] {
	return [...html.matchAll(/<a\b[^>]*\bhref="([^"]*)"/g)].map((m) => m[1] ?? '');
}

/** Code that can import: `<script>` bodies of .svelte files, whole .ts files. */
function importableCode(file: string, source: string): string {
	if (!file.endsWith('.svelte')) return source;
	return [...source.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((m) => m[1] ?? '').join('\n');
}

/** Visible text: tags and Svelte hydration comments removed, entities decoded, spaces collapsed. */
function text(html: string): string {
	return html
		.replace(/<!--[\s\S]*?-->/g, '')
		.replace(/<[^>]+>/g, '')
		.replaceAll('&amp;', '&')
		.replaceAll('&quot;', '"')
		.replaceAll('&#39;', "'")
		.replace(/\s+/g, ' ')
		.trim();
}

const child = () => createRawSnippet(() => ({ render: () => '<p>child</p>' }));

describe('site pages', () => {
	test('the route tree: /, /privacy, /terms and /404, one layout, no endpoints or server loads', () => {
		assert.deepEqual(filesUnder('src/routes').sort(), [
			'src/routes/+layout.svelte',
			'src/routes/+layout.ts',
			'src/routes/+page.svelte',
			'src/routes/404/+page.svelte',
			'src/routes/privacy/+page.svelte',
			'src/routes/terms/+page.svelte'
		]);
	});

	test('every page is prerendered with no client JavaScript and no trailing slash', () => {
		const layout = read('src/routes/+layout.ts');
		assert.match(layout, /^export const prerender = true;$/m);
		assert.match(layout, /^export const csr = false;$/m);
		assert.match(layout, /^export const trailingSlash = 'never';$/m);
		const config = read('svelte.config.js');
		assert.ok(config.includes("from '@sveltejs/adapter-static'"));
		assert.ok(config.includes('strict: true'));
		assert.ok(config.includes('relative: false'), '404.html must load its CSS at any depth');
	});

	test('app.html has no robots meta and no script', () => {
		const html = read('src/app.html');
		assert.equal(html.includes('robots'), false);
		assert.equal(/<script\b/.test(html), false);
	});

	test('the 404 page: "Page not found", one h1, a link home, its title, no canonical and no robots meta', () => {
		const { head, body } = render(NotFoundPage);
		assert.equal(title(head), 'Page not found · Deploybot');
		assert.equal(head.includes('rel="canonical"'), false);
		assert.equal(head.includes('robots'), false);
		assert.equal(body.match(/<h1\b/g)?.length, 1);
		assert.match(body, /<h1\b[^>]*>Page not found<\/h1>/);
		assert.deepEqual(hrefs(body), ['/']);
	});

	test('the site error content for another status: generic text with the status', () => {
		const { head, body } = render(SiteError, { props: { status: 500 } });
		assert.equal(title(head), 'Something went wrong · Deploybot');
		assert.ok(text(body).includes('error 500'));
		assert.equal(text(body).includes('Page not found'), false);
	});

	test('the 404 renders inside the site layout (header with Sign in, footer)', () => {
		const children = createRawSnippet(() => ({ render: () => render(NotFoundPage).body }));
		const { body } = render(SiteLayout, { props: { children } });
		for (const tag of ['<header', '<main', '<footer']) assert.ok(body.includes(tag), tag);
		assert.ok(body.includes('Page not found'));
	});

	test('page titles are exact', () => {
		assert.equal(title(render(AboutPage).head), 'Deploybot: Let AI run your servers, without the keys');
		assert.equal(title(render(PrivacyPage).head), 'Privacy Policy · Deploybot');
		assert.equal(title(render(TermsPage).head), 'Terms of Service · Deploybot');
	});

	test('the About hero carries the pitch; How it works has the short summary; the panels live only in What you get', () => {
		const PITCH =
			'Let any AI agent that speaks MCP run your own servers, without handing it SSH keys or a cloud token. It can only deploy what your catalog allows, as you, after a confirm step.';
		const body = render(AboutPage).body.replace(/<!--[^]*?-->/g, '').replace(/\s+/g, ' ');
		const hero = body.slice(body.indexOf('id="hero-title"'), body.indexOf('aria-labelledby="flow-title"'));
		assert.ok(hero.includes(PITCH), 'pitch in the hero');
		assert.match(hero, /^id="hero-title"[^>]*>\s*Let AI run your servers, without the keys\.\s*<\/h1>/);
		const how = body.slice(body.indexOf('id="how-title"'));
		assert.ok(how.includes('Three steps from a new host to a running service.'));
		for (const panel of ['Safety', 'Deploys', 'Configuration', 'Networking', 'Visibility', 'Teams', 'For AI agents', 'Upkeep']) {
			assert.equal(body.match(new RegExp(`>${panel}<`, 'g'))?.length, 1, panel);
		}
	});

	test('the pitch diagrams (landscape and portrait) sit in the flow section with accessible names and generic agents', () => {
		const body = render(AboutPage).body.replace(/<!--[^]*?-->/g, '');
		const hero = body.slice(body.indexOf('id="flow-title"'), body.indexOf('aria-labelledby="visual-title"'));
		assert.match(hero, /<svg[^>]*role="img"[^>]*aria-labelledby="pitch-title" aria-describedby="pitch-desc"/);
		assert.match(hero, /<svg[^>]*role="img"[^>]*aria-labelledby="pitch-portrait-title" aria-describedby="pitch-portrait-desc"/);
		for (const id of ['pitch-title', 'pitch-portrait-title']) {
			assert.ok(hero.includes(`<title id="${id}">How Deploybot lets AI agents run your servers</title>`), id);
		}
		assert.ok(hero.includes('id="pitch-desc"') && hero.includes('id="pitch-portrait-desc"'));
		for (const word of ['Claude', 'Codex', 'Grok']) assert.equal(hero.includes(word), false, `no ${word} in the hero`);
		for (const id of ['pitch-title', 'pitch-portrait-title']) {
			const svg = hero.slice(hero.indexOf(`aria-labelledby="${id}"`)).split('</svg>')[0] ?? '';
			assert.ok((svg.match(/<g class="line[^"]*" transform="translate\(/g)?.length ?? 0) >= 9, id);
		}
		assert.equal(hero.match(/hosted or self-hosted/gi)?.length, 4, 'subtitle in both diagrams and both descs');
		assert.equal(hero.includes('Hosts dial out, so no open ports'), false);
		// Dark mode comes from app.css (the dark variant is the system media query), not from page markup.
		assert.equal(body.includes('prefers-color-scheme'), false);
	});

	test('the About page ends with Open source: bot-agent and the bot-control-plane parts on main, SPDX licences, self-hosting needs', () => {
		const body = render(AboutPage).body.replace(/<!--[^]*?-->/g, '').replace(/\s+/g, ' ');
		assert.ok(body.indexOf('id="source-title"') > body.indexOf('id="how-title"'), 'after How it works');
		const source = body.slice(body.indexOf('id="source-title"'));
		for (const path of [
			'bot-agent/tree/main',
			'bot-control-plane/tree/main/api',
			'bot-control-plane/tree/main/mcp',
			'bot-control-plane/tree/main/console'
		]) {
			assert.ok(source.includes(`href="https://github.com/more-than-code/${path}"`), path);
		}
		assert.doesNotMatch(source, /bot-api-server|bot-mcp-server|github\.com\/more-than-code\/bot-console/);
		assert.equal(source.match(/AGPL-3\.0-only/g)?.length, 3);
		assert.ok(source.includes('Apache-2.0'));
		assert.ok(source.includes('PostgreSQL 18 with TimescaleDB, a Google OAuth client, a TLS reverse proxy'));
		assert.ok(source.includes('secrets key file'));
		assert.doesNotMatch(source, /\bv\d+\.\d+|us1|caddy/i);
	});

	test('each page has one h1 and the legal pages carry LAST_UPDATED', () => {
		for (const page of [AboutPage, PrivacyPage, TermsPage]) {
			assert.equal(render(page).body.match(/<h1\b/g)?.length, 1);
		}
		assert.match(LAST_UPDATED, /^\d{1,2} [A-Z][a-z]+ \d{4}$/);
		for (const page of [PrivacyPage, TermsPage]) {
			assert.ok(text(render(page).body).includes(`Last updated: ${LAST_UPDATED}`));
		}
	});

	test('the site layout has header, main and footer with the sign-in and footer links', () => {
		const { body } = render(SiteLayout, { props: { children: child() } });
		for (const tag of ['<header', '<main', '<footer']) assert.ok(body.includes(tag), tag);
		assert.ok(body.includes('<p>child</p>'));
		const links = hrefs(body);
		for (const href of ['/', SIGN_IN_URL, '/privacy', '/terms', `mailto:${SUPPORT_EMAIL}`]) {
			assert.ok(links.includes(href), href);
		}
		assert.ok(body.includes('/brand/lockup-light.svg') && body.includes('/brand/lockup-dark.svg'));
	});

	test('Sign in and Get started go to the console by absolute URL, from the one constant', () => {
		// Spec site-pages §3.1: the site and the console are separate hosts, so no relative /signin.
		assert.equal(SIGN_IN_URL, 'https://console.deploybot.dev/signin');
		assert.equal(SITE_URL, 'https://deploybot.dev');
		assert.equal(SUPPORT_EMAIL, 'support@deploybot.dev');
		const layout = render(SiteLayout, { props: { children: child() } }).body;
		const pages = [AboutPage, PrivacyPage, TermsPage].map((page) => render(page).body);
		assert.equal(hrefs(layout).filter((href) => href === SIGN_IN_URL).length, 1);
		assert.equal(hrefs(pages[0] ?? '').filter((href) => href === SIGN_IN_URL).length, 2);
		for (const html of [layout, ...pages]) {
			assert.equal(hrefs(html).includes('/signin'), false, 'no relative /signin');
			assert.equal(html.includes('data-sveltekit-'), false, 'no client router attributes');
		}
		for (const file of PUBLIC_SOURCES.filter((f) => f !== 'src/lib/site/links.ts')) {
			assert.equal(read(file).includes('console.deploybot.dev/signin'), false, file);
		}
	});

	test('no source file names the temporary hosts api2 or console2', () => {
		const walk = (dir: string): string[] =>
			readdirSync(dir).flatMap((name) => {
				const path = join(dir, name);
				return statSync(path).isDirectory() ? walk(path) : [path];
			});
		const files = [...walk(join(root, 'src')), ...walk(join(root, 'static')).filter((f) => !/\.(png|webp|ico)$/.test(f))];
		assert.ok(files.length > 15, 'the walk found files');
		for (const file of files) {
			assert.equal(/\b(api2|console2)\.deploybot\.dev/.test(readFileSync(file, 'utf8')), false, file);
		}
	});

	test('pages import only site and legal modules, and use no {@html}', () => {
		const allowed = (spec: string) =>
			spec === 'svelte' ||
			spec === '../app.css' ||
			/^@lucide\/svelte\/icons\/[a-z0-9-]+$/.test(spec) ||
			spec.startsWith('$lib/site/') ||
			spec.startsWith('$lib/legal/') ||
			spec.startsWith('./');
		let scanned = 0;
		for (const file of PUBLIC_SOURCES) {
			const source = read(file);
			assert.equal(source.includes('{@html'), false, `${file} uses {@html}`);
			const imports = [...importableCode(file, source).matchAll(/(?:\bfrom|\bimport)\s*\(?\s*['"]([^'"]+)['"]/g)].map(
				(m) => m[1] ?? ''
			);
			scanned += imports.length;
			for (const spec of imports) {
				assert.ok(allowed(spec), `${file} imports ${spec}: not on the site allowlist`);
				if (spec !== '../app.css') assert.equal(spec.includes('..'), false, `${file} imports ${spec}`);
			}
			if (basename(file) === '+layout.svelte') assert.ok(imports.includes('../app.css'), 'the layout loads app.css');
		}
		assert.ok(scanned >= 8, 'the scan sees the imports in the script blocks');
	});
});
