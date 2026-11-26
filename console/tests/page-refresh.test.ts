// The page Refresh button (singleRefresh) and the pages that show it.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import { singleRefresh } from '../src/lib/page-refresh.ts';

test('a click while a refresh runs does nothing, and pending follows the run', async () => {
	let runs = 0;
	let release: () => void = () => {};
	const seen: boolean[] = [];
	const refresh = singleRefresh(
		() => {
			runs += 1;
			return new Promise<void>((resolve) => (release = resolve));
		},
		(pending) => seen.push(pending)
	);
	const first = refresh();
	await refresh();
	assert.equal(runs, 1);
	release();
	await first;
	assert.deepEqual(seen, [true, false]);
	const second = refresh();
	assert.equal(runs, 2);
	release();
	await second;
});

test('a failed refresh does not throw and allows the next one', async () => {
	let runs = 0;
	const seen: boolean[] = [];
	const refresh = singleRefresh(
		async () => {
			runs += 1;
			throw new Error('load failed');
		},
		(pending) => seen.push(pending)
	);
	await refresh();
	await refresh();
	assert.equal(runs, 2);
	assert.deepEqual(seen, [true, false, true, false]);
});

test('the project, host and org pages show the Refresh button in their header', () => {
	for (const file of ['projects/[id]', 'hosts/[id]', 'orgs/[id]']) {
		const source = readFileSync(new URL(`../src/routes/${file}/+page.svelte`, import.meta.url), 'utf8');
		assert.match(source, /import RefreshButton from '\$lib\/components\/RefreshButton.svelte'/, file);
		assert.match(source, /<RefreshButton \/>/, file);
	}
});

test('a rename draft survives a reload of the same name and follows a changed name', async () => {
	const { createDraftFollower } = await import('../src/lib/draft-field.ts');
	const follow = createDraftFollower('daikou');
	// Refresh loads the same name again: what was typed stays.
	assert.equal(follow('daikou', 'daikou-prod'), 'daikou-prod');
	assert.equal(follow('daikou', 'daikou-prod'), 'daikou-prod');
	// The name changed (a save, someone else's rename): the field shows it.
	assert.equal(follow('daikou-2', 'daikou-prod'), 'daikou-2');
	assert.equal(follow('daikou-2', 'typed again'), 'typed again');
});

test('the project and org rename fields keep their draft across Refresh', () => {
	for (const file of ['projects/[id]/SettingsSection.svelte', 'orgs/[id]/OrgSettingsSection.svelte']) {
		const source = readFileSync(new URL(`../src/routes/${file}`, import.meta.url), 'utf8');
		assert.match(source, /name="name" bind:value=\{renameDraft\}/, file);
		assert.doesNotMatch(source, /name="name" value=\{(project|org)\.name\}/, file);
		assert.match(source, /createDraftFollower\(/, file);
	}
});
