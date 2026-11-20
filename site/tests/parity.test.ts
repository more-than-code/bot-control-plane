// The site's copies of console files stay equal to the console's (umbrella docs/spec-site-pages.md
// §3.1, §6 question 3). Until the cutover (§3.4 step 5) the console still serves the site from its
// own copies; after it, the console keeps only the tokens and the brand files it uses itself.
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const SITE = fileURLToPath(new URL('..', import.meta.url));
const CONSOLE = join(SITE, '..', 'console');
const read = (dir: string, path: string) => readFileSync(join(dir, path), 'utf8');

/** The body of the first block that starts with `opener` (e.g. ':root {'), braces matched. */
function block(css: string, opener: string): string {
	const start = css.indexOf(opener);
	assert.ok(start >= 0, `no block ${opener}`);
	let depth = 0;
	for (let i = start + opener.length - 1; i < css.length; i++) {
		if (css[i] === '{') depth++;
		else if (css[i] === '}' && --depth === 0) return css.slice(start + opener.length, i);
	}
	throw new Error(`unclosed block ${opener}`);
}

/** Declarations of a flat block as name → value, whitespace normalised. */
function declarations(body: string): Map<string, string> {
	const map = new Map<string, string>();
	for (const part of body.split(';')) {
		const decl = part.replace(/\/\*[\s\S]*?\*\//g, '').trim();
		if (!decl) continue;
		const colon = decl.indexOf(':');
		assert.ok(colon > 0, `not a declaration: ${decl}`);
		const name = decl.slice(0, colon).trim();
		assert.equal(map.has(name), false, `duplicate ${name}`);
		map.set(name, decl.slice(colon + 1).replace(/\s+/g, ' ').trim());
	}
	return map;
}

const consoleCss = read(CONSOLE, 'src/app.css');
const siteCss = read(SITE, 'src/app.css');

describe('theme token parity with console/src/app.css', () => {
	test('light tokens (:root) are equal', () => {
		const expected = declarations(block(consoleCss, ':root {'));
		assert.ok(expected.size >= 35, 'parsed the console tokens');
		assert.deepEqual(declarations(block(siteCss, ':root {')), expected);
	});

	test('dark tokens are equal: the console .dark block is the site prefers-color-scheme block', () => {
		const expected = declarations(block(consoleCss, '.dark {'));
		assert.ok(expected.size >= 35, 'parsed the console dark tokens');
		const media = block(siteCss, '@media (prefers-color-scheme: dark) {');
		assert.deepEqual(declarations(block(media, ':root {')), expected);
	});

	test('the @theme inline mapping and the base layer are equal', () => {
		assert.deepEqual(declarations(block(siteCss, '@theme inline {')), declarations(block(consoleCss, '@theme inline {')));
		const norm = (s: string) => s.replace(/\s+/g, ' ').trim();
		assert.equal(norm(block(siteCss, '@layer base {')), norm(block(consoleCss, '@layer base {')));
	});

	test('dark mode is the system media query only: no .dark class block, no class variant', () => {
		assert.ok(siteCss.includes('@custom-variant dark (@media (prefers-color-scheme: dark));'));
		assert.equal(siteCss.includes('.dark'), false);
	});
});

describe('static copies', () => {
	// The brand files the console keeps after the cutover (it uses them itself): lockups, favicons,
	// icons and the apple-touch-icon. Each must exist in both apps and be byte-equal.
	const KEPT_BRAND = [
		'lockup-light.svg',
		'lockup-dark.svg',
		'favicon.svg',
		'favicon-16.png',
		'favicon-32.png',
		'icon-192.png',
		'icon-512.png',
		'apple-touch-icon.png'
	];

	test('the brand files the console keeps are byte-equal', () => {
		for (const name of KEPT_BRAND) {
			const site = readFileSync(join(SITE, 'static/brand', name));
			assert.ok(site.equals(readFileSync(join(CONSOLE, 'static/brand', name))), name);
		}
	});

	// Remove at console cleanup (spec §3.4 step 5): the console then drops these site-only files.
	test('favicon.ico, llms.txt, robots.txt and sitemap.xml equal the console copies', () => {
		for (const name of ['favicon.ico', 'llms.txt', 'robots.txt', 'sitemap.xml']) {
			const console = join(CONSOLE, 'static', name);
			if (!existsSync(console)) continue; // after the cutover the console drops the site-only files
			assert.ok(readFileSync(join(SITE, 'static', name)).equals(readFileSync(console)), name);
		}
	});

	test('the manifest equals the console copy except start_url', () => {
		const site = JSON.parse(read(SITE, 'static/manifest.webmanifest'));
		const console = JSON.parse(read(CONSOLE, 'static/manifest.webmanifest'));
		assert.equal(site.start_url, '/');
		assert.deepEqual({ ...site, start_url: console.start_url }, console);
	});
});
