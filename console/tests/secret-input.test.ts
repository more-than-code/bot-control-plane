import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { PASSWORD_MANAGER_IGNORE, secretInput, supportsTextMask, TEXT_MASK_CLASS } from '../src/lib/secret-input.ts';

const root = fileURLToPath(new URL('..', import.meta.url));

function svelteFiles(dir: string): string[] {
	return readdirSync(join(root, dir)).flatMap((name) => {
		const rel = `${dir}/${name}`;
		if (statSync(join(root, rel)).isDirectory()) return rel.includes('/components/ui') ? [] : svelteFiles(rel);
		return rel.endsWith('.svelte') ? [rel] : [];
	});
}

test('a masked secret is CSS-masked text where the browser can, so no save-password prompt', () => {
	assert.deepEqual(secretInput(true, true), { type: 'text', autocomplete: 'off', maskClass: TEXT_MASK_CLASS });
});

test('without CSS masking a secret stays type=password, never clear text', () => {
	assert.deepEqual(secretInput(true, false), { type: 'password', autocomplete: 'new-password', maskClass: '' });
});

test('a shown or plain value is unmasked text', () => {
	for (const canMask of [true, false]) {
		assert.deepEqual(secretInput(false, canMask), { type: 'text', autocomplete: 'off', maskClass: '' });
	}
});

test('server rendering cannot mask, so the first paint is type=password', () => {
	// node has no CSS global: the helper must say no rather than throw.
	assert.equal(supportsTextMask(), false);
});

test('password managers are told to skip secret fields', () => {
	assert.equal(PASSWORD_MANAGER_IGNORE['data-1p-ignore'], '');
	assert.equal(PASSWORD_MANAGER_IGNORE['data-lpignore'], 'true');
	assert.equal(PASSWORD_MANAGER_IGNORE['data-bwignore'], '');
});

test('no console input hard-codes a password type; secret fields go through secretInput', () => {
	const offenders: string[] = [];
	for (const file of [...svelteFiles('src/routes'), ...svelteFiles('src/lib/components')]) {
		const text = readFileSync(join(root, file), 'utf8');
		if (/type=("password"|\{[^}]*'password'[^}]*\})/.test(text)) offenders.push(file);
	}
	assert.deepEqual(offenders, []);
});
