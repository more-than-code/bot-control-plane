// Guard over the built output (umbrella docs/spec-site-pages.md §3.1, §3.2): run after `pnpm build`
// with `pnpm guard`. It checks build/, the exact files Wrangler uploads.
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const BUILD = fileURLToPath(new URL('../build', import.meta.url));
const BINARY = /\.(png|webp|ico)$/i;

/** URLs a built file may contain besides our own hosts (all present on the site before the move). */
const ALLOWED_OTHER = [
	'http://www.w3.org/2000/svg', // SVG namespace in the brand SVGs and inline diagrams
	'http://www.sitemaps.org/schemas/sitemap/0.9', // sitemap namespace
	'https://www.privacy.org.nz' // the Privacy Policy's regulator link
];
/** Our hosts, https only. api2 and console2 are retired and refused below. */
const OUR_HOSTS = new Set(['deploybot.dev', 'console.deploybot.dev', 'mcp.deploybot.dev']);
const GITHUB_PREFIX = 'https://github.com/more-than-code/';

type Built = { rel: string; text: string };
let built: Built[] = [];

function walk(dir: string): string[] {
	return readdirSync(dir).flatMap((name) => {
		const path = join(dir, name);
		return statSync(path).isDirectory() ? walk(path) : [path];
	});
}

const file = (rel: string) => readFileSync(join(BUILD, rel), 'utf8');

before(() => {
	assert.ok(existsSync(BUILD), 'build/ is missing: run pnpm build first');
	built = walk(BUILD).map((path) => ({
		rel: relative(BUILD, path),
		text: BINARY.test(path) ? '' : readFileSync(path, 'utf8')
	}));
});

describe('built site guard', () => {
	test('the expected pages and files exist', () => {
		for (const rel of ['index.html', 'privacy.html', 'terms.html', '404.html', 'llms.txt', 'robots.txt', 'sitemap.xml']) {
			assert.ok(existsSync(join(BUILD, rel)), rel);
		}
		for (const rel of ['manifest.webmanifest', 'favicon.ico', '_redirects', '_headers', 'brand/og-image.png']) {
			assert.ok(existsSync(join(BUILD, rel)), rel);
		}
		assert.equal(existsSync(join(BUILD, 'about.html')), false, '/about is a redirect, not a page');
	});

	test('no client JavaScript: no .js file, no <script>, no __data.json or version.json', () => {
		const hits = built
			.filter(
				(f) =>
					/\.m?js$/.test(f.rel) ||
					/(^|\/)(__data\.json|version\.json)$/.test(f.rel) ||
					/<script\b/i.test(f.text) ||
					/javascript:/i.test(f.text) ||
					/modulepreload/.test(f.text)
			)
			.map((f) => f.rel);
		assert.deepEqual(hits, []);
	});

	test('no noindex and no robots meta in any page', () => {
		for (const f of built.filter((f) => f.rel.endsWith('.html'))) {
			assert.doesNotMatch(f.text, /<meta[^>]*name="robots"/i, f.rel);
			assert.equal(f.text.includes('noindex'), false, f.rel);
		}
	});

	test('every URL is ours, the public repos, or a documented link', () => {
		const hits: string[] = [];
		// _headers holds host patterns (https://:project.pages.dev/*), checked exactly below.
		for (const f of built.filter((f) => f.rel !== '_headers')) {
			for (const raw of f.text.match(/https?:\/\/[^\s"'<>)`\\]+/g) ?? []) {
				const url = raw.replace(/[.,;:]+$/, '');
				if (ALLOWED_OTHER.includes(url)) continue;
				if (url.startsWith(GITHUB_PREFIX)) continue;
				if (url.startsWith('https://') && OUR_HOSTS.has(new URL(url).hostname)) continue;
				hits.push(`${f.rel}: ${url}`);
			}
		}
		assert.deepEqual(hits, []);
	});

	test('no api2 or console2, no dev-branch links', () => {
		for (const f of built) {
			assert.doesNotMatch(f.text, /\b(api2|console2)\b/, f.rel);
			assert.doesNotMatch(f.text, /\/(tree|blob)\/dev\b/, f.rel);
		}
	});

	test('sign-in links go to the console by absolute URL', () => {
		for (const rel of ['index.html', 'privacy.html', 'terms.html', '404.html']) {
			const html = file(rel);
			assert.ok(html.includes('href="https://console.deploybot.dev/signin"'), rel);
			assert.equal(html.includes('href="/signin"'), false, rel);
		}
	});

	test('the home page is About with its title and canonical; the 404 page says "Page not found"', () => {
		const index = file('index.html');
		assert.ok(index.includes('<title>Deploybot: Let AI run your servers, without the keys</title>'));
		assert.ok(index.includes('<link rel="canonical" href="https://deploybot.dev/"/>'));
		const notFound = file('404.html');
		assert.ok(notFound.includes('<title>Page not found · Deploybot</title>'));
		assert.match(notFound, /<h1\b[^>]*>Page not found<\/h1>/);
		assert.equal(notFound.includes('rel="canonical"'), false);
		// Absolute asset URLs: Pages serves 404.html for /a/b/c, so a relative ./_app path would break.
		for (const href of notFound.match(/(?:href|src)="([^"]+)"/g) ?? []) {
			assert.doesNotMatch(href, /="\.\.?\//, href);
		}
	});

	test('sitemap.xml has exactly the three apex URLs; robots.txt names it', () => {
		const locs = [...file('sitemap.xml').matchAll(/<loc>([^<]*)<\/loc>/g)].map((m) => m[1]);
		assert.deepEqual(locs, ['https://deploybot.dev/', 'https://deploybot.dev/privacy', 'https://deploybot.dev/terms']);
		assert.ok(file('robots.txt').split('\n').includes('Sitemap: https://deploybot.dev/sitemap.xml'));
	});

	test('llms.txt starts with "# Deploybot"; the manifest starts on /', () => {
		assert.ok(file('llms.txt').startsWith('# Deploybot\n'));
		assert.equal(JSON.parse(file('manifest.webmanifest')).start_url, '/');
	});

	test('_redirects is exactly the spec §3.2 list', () => {
		assert.equal(
			file('_redirects'),
			[
				'/about           /                                         301',
				'/signin          https://console.deploybot.dev/signin      301',
				'/signin/*        https://console.deploybot.dev/signin/:splat   301',
				'/projects        https://console.deploybot.dev/projects    301',
				'/projects/*      https://console.deploybot.dev/projects/:splat 301',
				''
			].join('\n')
		);
	});

	test('_headers: security headers on every path, llms.txt as text, noindex on /404 and the pages.dev hosts only', () => {
		assert.equal(
			file('_headers'),
			[
				'/*',
				'  X-Content-Type-Options: nosniff',
				'  X-Frame-Options: DENY',
				'  Referrer-Policy: no-referrer',
				'',
				'/llms.txt',
				'  Content-Type: text/plain; charset=utf-8',
				'',
				'/404',
				'  X-Robots-Tag: noindex',
				'',
				'https://:project.pages.dev/*',
				'  X-Robots-Tag: noindex',
				'',
				'https://:version.:project.pages.dev/*',
				'  X-Robots-Tag: noindex',
				''
			].join('\n')
		);
		// /404 answers 200 when requested directly, so it gets noindex; unknown paths answer 404 with no tag.
		assert.equal(file('_headers').match(/X-Robots-Tag/g)?.length, 3);
		assert.equal(file('_headers').includes('deploybot.dev'), false, 'no noindex rule can match the apex');
	});
});
