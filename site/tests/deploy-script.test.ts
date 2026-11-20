// scripts/deploy.sh with a stub pnpm on PATH and a fake HOME: what it runs, and what it refuses.
// No real pnpm, wrangler or network call; the stub only records its arguments.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const SCRIPT = fileURLToPath(new URL('../scripts/deploy.sh', import.meta.url));
const FAKE_TOKEN = 'fake-token-for-test-0123456789';

type Sandbox = { repo: string; site: string; home: string; bin: string; log: string };

/** origin/dev in the throwaway repo: none, at HEAD, or one commit behind HEAD. */
type Origin = 'none' | 'head' | 'behind';

/** Temp dirs this file made; removed after the tests. */
const made: string[] = [];
after(() => {
	for (const dir of made) rmSync(dir, { recursive: true, force: true });
});

/**
 * A throwaway git repo holding site/scripts/deploy.sh, a fake HOME and a stub pnpm. `origin` is a
 * local bare repo, so the origin/dev rule runs offline.
 */
function sandbox({ clean, token = true, mode = 0o600, origin = 'none' }: { clean: boolean; token?: boolean; mode?: number; origin?: Origin }): Sandbox {
	const dir = mkdtempSync(join(tmpdir(), 'site-deploy-test-'));
	made.push(dir);
	const repo = join(dir, 'repo');
	const site = join(repo, 'site');
	mkdirSync(join(site, 'scripts'), { recursive: true });
	cpSync(SCRIPT, join(site, 'scripts', 'deploy.sh'));
	// --no-verify: the test repos skip any commit and push hooks the machine's git config adds.
	const git = (cwd: string, ...args: string[]) =>
		assert.equal(spawnSync('git', ['-c', 'user.name=t', '-c', 'user.email=t@example.invalid', ...args], { cwd }).status, 0, args.join(' '));
	git(repo, 'init', '-q');
	git(repo, 'add', '.');
	git(repo, 'commit', '-q', '--no-verify', '-m', 'test');
	if (origin !== 'none') {
		git(dir, 'init', '-q', '--bare', 'origin.git');
		git(repo, 'remote', 'add', 'origin', join(dir, 'origin.git'));
		git(repo, 'push', '-q', '--no-verify', 'origin', 'HEAD:refs/heads/dev');
		if (origin === 'behind') git(repo, 'commit', '-q', '--no-verify', '--allow-empty', '-m', 'not pushed');
	}
	if (!clean) writeFileSync(join(site, 'dirty.txt'), 'x');
	const home = join(dir, 'home');
	mkdirSync(join(home, '.config', 'deploybot'), { recursive: true });
	if (token) {
		const file = join(home, '.config', 'deploybot', 'cloudflare-pages-token');
		writeFileSync(file, `${FAKE_TOKEN}\n`);
		chmodSync(file, mode);
	}
	const bin = join(dir, 'bin');
	mkdirSync(bin);
	const log = join(dir, 'pnpm.log');
	// Records each call, and for wrangler whether the token reached its environment (never the value).
	// With DIRTY_ON_GUARD=1, "pnpm guard" leaves an untracked file, as a gate that writes into the tree would.
	writeFileSync(
		join(bin, 'pnpm'),
		`#!/bin/sh\necho "$*" >>"${log}"\nif [ "$1" = guard ] && [ "\${DIRTY_ON_GUARD:-}" = 1 ]; then echo x >"${site}/gate-output.txt"; fi\nif [ "$1" = exec ]; then [ "$CLOUDFLARE_API_TOKEN" = "${FAKE_TOKEN}" ] && echo "token-ok account=$CLOUDFLARE_ACCOUNT_ID" >>"${log}"; fi\nexit 0\n`
	);
	chmodSync(join(bin, 'pnpm'), 0o755);
	writeFileSync(log, '');
	return { repo, site, home, bin, log };
}

const headOf = (box: Sandbox) => spawnSync('git', ['rev-parse', 'HEAD'], { cwd: box.site, encoding: 'utf8' }).stdout.trim();

/** Runs the script as `sh scripts/deploy.sh` from site/, or as `sh site/scripts/deploy.sh` from the repo root. */
function run(box: Sandbox, env: Record<string, string>, from: 'site' | 'root' = 'site') {
	const result = spawnSync('sh', [from === 'site' ? 'scripts/deploy.sh' : 'site/scripts/deploy.sh'], {
		cwd: from === 'site' ? box.site : box.repo,
		env: { PATH: `${box.bin}:/usr/bin:/bin`, HOME: box.home, ...env },
		encoding: 'utf8'
	});
	const output = `${result.stdout}${result.stderr}`;
	assert.equal(output.includes(FAKE_TOKEN), false, 'the token is never printed');
	return { status: result.status, output, calls: readFileSync(box.log, 'utf8').trim().split('\n').filter(Boolean) };
}

const GATES = ['install --frozen-lockfile', 'check', 'test', 'build', 'guard'];

describe('scripts/deploy.sh', () => {
	test('DRY_RUN=1 runs every gate and never calls wrangler', () => {
		const box = sandbox({ clean: true, token: false });
		const { status, output, calls } = run(box, { DRY_RUN: '1' });
		assert.equal(status, 0, output);
		assert.deepEqual(calls, GATES);
		assert.match(output, /dry run, not uploading/);
	});

	test('DRY_RUN=1 with ALLOW_DIRTY_FOR_TEST=1 runs on a dirty tree, still without wrangler', () => {
		const box = sandbox({ clean: false, token: false });
		const { status, calls } = run(box, { DRY_RUN: '1', ALLOW_DIRTY_FOR_TEST: '1' });
		assert.equal(status, 0);
		assert.deepEqual(calls, GATES);
	});

	test('a dirty tree is refused before any step, even with ALLOW_DIRTY_FOR_TEST=1 when DRY_RUN is unset', () => {
		const envs: Record<string, string>[] = [{}, { DRY_RUN: '1' }, { ALLOW_DIRTY_FOR_TEST: '1' }, { ALLOW_DIRTY_FOR_TEST: '1', CLOUDFLARE_ACCOUNT_ID: 'acc' }];
		for (const env of envs) {
			const box = sandbox({ clean: false });
			const { status, output, calls } = run(box, env);
			assert.equal(status, 1, JSON.stringify(env));
			assert.match(output, /uncommitted changes/);
			assert.deepEqual(calls, []);
		}
	});

	test('a real deploy runs the gates, then wrangler with the pinned arguments and the token in its environment', () => {
		const box = sandbox({ clean: true, origin: 'head' });
		const head = headOf(box);
		const { status, output, calls } = run(box, { CLOUDFLARE_ACCOUNT_ID: 'acc123', BRANCH: 'main' });
		assert.equal(status, 0, output);
		assert.deepEqual(calls, [
			...GATES,
			`exec wrangler pages deploy build --project-name deploybot-site --branch main --commit-hash ${head} --commit-dirty=false`,
			'token-ok account=acc123'
		]);
		assert.match(output, new RegExp(`uploading build/ to deploybot-site, branch main, commit ${head} \\(git branch \\S+\\)`));
	});

	test('BRANCH=main refuses a commit that is not on origin/dev, before any step, in a dry run too', () => {
		const envs: Record<string, string>[] = [{ CLOUDFLARE_ACCOUNT_ID: 'a', BRANCH: 'main' }, { DRY_RUN: '1', BRANCH: 'main' }];
		for (const env of envs) {
			const box = sandbox({ clean: true, origin: 'behind' });
			const { status, output, calls } = run(box, env);
			assert.equal(status, 1, output);
			assert.match(output, /only a commit already on origin\/dev/);
			assert.deepEqual(calls, []);
		}
		const noOrigin = sandbox({ clean: true });
		const { status, output, calls } = run(noOrigin, { DRY_RUN: '1', BRANCH: 'main' });
		assert.equal(status, 1, output);
		assert.match(output, /could not fetch dev from origin/);
		assert.deepEqual(calls, []);
	});

	test('the origin/dev rule applies only to BRANCH=main: a preview deploys a commit not on origin/dev', () => {
		const box = sandbox({ clean: true, origin: 'behind' });
		const { status, output, calls } = run(box, { CLOUDFLARE_ACCOUNT_ID: 'a' });
		assert.equal(status, 0, output);
		assert.match(calls[GATES.length] ?? '', / --branch preview /);
	});

	test('a tree made dirty by the gates is refused after them, before the upload', () => {
		const envs: Record<string, string>[] = [{ CLOUDFLARE_ACCOUNT_ID: 'a' }, { DRY_RUN: '1' }];
		for (const env of envs) {
			const box = sandbox({ clean: true });
			const { status, output, calls } = run(box, { ...env, DIRTY_ON_GUARD: '1' });
			assert.equal(status, 1, output);
			assert.match(output, /uncommitted changes/);
			assert.deepEqual(calls, GATES, 'every gate ran, wrangler did not');
		}
	});

	test('DRY_RUN and ALLOW_DIRTY_FOR_TEST accept only 0 or 1, checked before any step', () => {
		const envs: Record<string, string>[] = [
			{ DRY_RUN: 'yes' },
			{ DRY_RUN: '' },
			{ DRY_RUN: '2' },
			{ DRY_RUN: 'true' },
			{ DRY_RUN: '1', ALLOW_DIRTY_FOR_TEST: 'true' },
			{ ALLOW_DIRTY_FOR_TEST: ' 1' }
		];
		for (const env of envs) {
			const box = sandbox({ clean: true });
			const { status, output, calls } = run(box, { CLOUDFLARE_ACCOUNT_ID: 'a', ...env });
			assert.equal(status, 1, `${JSON.stringify(env)}: ${output}`);
			assert.match(output, /(DRY_RUN|ALLOW_DIRTY_FOR_TEST) must be 0 or 1/);
			assert.deepEqual(calls, []);
		}
		const box = sandbox({ clean: true, token: false });
		const { status, output } = run(box, { DRY_RUN: '0', ALLOW_DIRTY_FOR_TEST: '0', CLOUDFLARE_ACCOUNT_ID: 'a' });
		assert.equal(status, 1);
		assert.match(output, /missing .*cloudflare-pages-token/, 'DRY_RUN=0 is a real deploy');
	});

	test('runs from the repo root as sh site/scripts/deploy.sh', () => {
		const box = sandbox({ clean: true, token: false });
		const { status, output, calls } = run(box, { DRY_RUN: '1' }, 'root');
		assert.equal(status, 0, output);
		assert.deepEqual(calls, GATES);
		assert.match(output, new RegExp(`dry run, not uploading commit ${headOf(box)}`));
	});

	test('the branch defaults to preview, and the account id can come from its file', () => {
		const box = sandbox({ clean: true });
		writeFileSync(join(box.home, '.config', 'deploybot', 'cloudflare-account-id'), 'acc-file\n');
		const { status, calls } = run(box, {});
		assert.equal(status, 0);
		assert.match(calls[GATES.length] ?? '', / --branch preview /);
		assert.equal(calls.at(-1), 'token-ok account=acc-file');
	});

	test('missing token, wrong mode, missing account id or a bad branch fail before any step', () => {
		const cases: [Sandbox, Record<string, string>, RegExp][] = [
			[sandbox({ clean: true, token: false }), { CLOUDFLARE_ACCOUNT_ID: 'a' }, /missing .*cloudflare-pages-token/],
			[sandbox({ clean: true, mode: 0o644 }), { CLOUDFLARE_ACCOUNT_ID: 'a' }, /must be mode 600 \(it is rw-r--r--\)/],
			[sandbox({ clean: true }), {}, /CLOUDFLARE_ACCOUNT_ID/],
			[sandbox({ clean: true }), { CLOUDFLARE_ACCOUNT_ID: 'a', BRANCH: 'Main;x' }, /BRANCH must be/]
		];
		for (const [box, env, message] of cases) {
			const { status, output, calls } = run(box, env);
			assert.equal(status, 1, output);
			assert.match(output, message);
			assert.deepEqual(calls, []);
		}
	});
});
