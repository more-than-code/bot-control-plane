// No colour literals outside the theme tokens: the console's rule (console/tests/components-reuse.test.ts,
// COLOUR and STYLE_COLOUR, same regexes) over every .svelte file in src/, and over src/app.css outside
// the light and dark token blocks. The only exception is src/app.html (its theme-color metas), which is
// not scanned.
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const SRC = fileURLToPath(new URL('../src/', import.meta.url));

const COLOUR = /(?<!\{)#[0-9a-fA-F]{3,8}\b|rgb\(|(?<![\w_])oklch\(|color-mix\(\s*in\s+oklch\b/;
const STYLE_COLOUR =
	/style\s*[:=]\s*(?:"[^"]*(?:\bcolor\b|background|fill|stroke|#[0-9a-fA-F]{3,8}\b|rgb\(|oklch\()[^"]*"|'[^']*(?:\bcolor\b|background|fill|stroke|#[0-9a-fA-F]{3,8}\b|rgb\(|oklch\()[^']*'|[A-Za-z-]+="#[0-9a-fA-F]{3,8}\b")/;

/** "file:line rule" for every line that matches. */
function scan(source: string, file: string): string[] {
	const found: string[] = [];
	source.split('\n').forEach((line, index) => {
		if (COLOUR.test(line)) found.push(`${file}:${index + 1} colour`);
		if (STYLE_COLOUR.test(line)) found.push(`${file}:${index + 1} style-colour`);
	});
	return found;
}

/** `css` with the block that starts with `opener` blanked out (newlines kept, so line numbers hold). */
function withoutBlock(css: string, opener: string): string {
	const start = css.indexOf(opener);
	assert.ok(start >= 0, `no block ${opener}`);
	let depth = 0;
	for (let i = start + opener.length - 1; i < css.length; i++) {
		if (css[i] === '{') depth++;
		else if (css[i] === '}' && --depth === 0) {
			return css.slice(0, start) + css.slice(start, i + 1).replace(/[^\n]/g, '') + css.slice(i + 1);
		}
	}
	throw new Error(`unclosed block ${opener}`);
}

test('svelte files in src/ have no colour literals or inline colour styles', () => {
	const files = readdirSync(SRC, { recursive: true })
		.map(String)
		.filter((name) => name.endsWith('.svelte'));
	assert.ok(files.length >= 10, 'found the site components');
	const violations = files.flatMap((name) => scan(readFileSync(join(SRC, name), 'utf8'), name));
	assert.deepEqual(violations, [], violations.join('\n'));
});

test('src/app.css has colour literals only in the light and dark token blocks', () => {
	const css = readFileSync(join(SRC, 'app.css'), 'utf8');
	const rest = withoutBlock(withoutBlock(css, ':root {'), '@media (prefers-color-scheme: dark) {');
	assert.ok(css.includes('oklch(') && !rest.includes('oklch('), 'the token blocks were found and blanked');
	const violations = scan(rest, 'app.css');
	assert.deepEqual(violations, [], violations.join('\n'));
});

test('the rules flag a fixture', () => {
	const fixture = [
		'{#each items as item}',
		'const ok = "#fff";',
		'fill="rgb(0, 0, 0)"',
		'oklch(0.2 0 0)',
		'<div style="left: 4px; top: 8px"></div>',
		'<span style="color: red"></span>'
	].join('\n');
	assert.deepEqual(scan(fixture, 'f'), ['f:2 colour', 'f:3 colour', 'f:4 colour', 'f:6 style-colour']);
});
