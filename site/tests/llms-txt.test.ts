import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

// Joe 2026-10-26 (spec-release-v0218 §2.5): https://deploybot.dev/llms.txt, served from static/.
const LLMS = readFileSync(new URL('../static/llms.txt', import.meta.url), 'utf8');

test('llms.txt starts with the Deploybot heading and the pitch', () => {
	assert.ok(LLMS.startsWith('# Deploybot\n\n> Deploybot lets any AI agent that speaks MCP run your own servers'));
	assert.ok(LLMS.includes('https://mcp.deploybot.dev/mcp'));
});

test('llms.txt names no retired hosts and claims no macOS support', () => {
	assert.doesNotMatch(LLMS, /api2\.|console2\.|macos|mac mini/i);
});

test('llms.txt links point at main, the latest release', () => {
	assert.doesNotMatch(LLMS, /\/blob\/dev\//);
	assert.match(LLMS, /bot-agent\/blob\/main\/docs\/install\.md/);
});

test('llms.txt links the server parts in bot-control-plane, not the retired repos', () => {
	for (const path of ['README.md', 'api/README.md', 'mcp/README.md', 'console/README.md', 'docs/contracts/api-v1.md']) {
		assert.ok(LLMS.includes(`https://github.com/more-than-code/bot-control-plane/blob/main/${path}`), path);
	}
	assert.doesNotMatch(LLMS, /more-than-code\/(bot-api-server|bot-mcp-server|bot-console)\b/);
});

test('llms.txt sends secrets through one-time console pages, never chat', () => {
	assert.ok(!LLMS.includes('Secrets belong in env sets entered in the web console'));
	const rule =
		'Never ask the user to paste a secret into chat. Use `request_env_value` or `request_file`; the user fills it on a one-time console page, and you poll `get_env_value_request` / `get_file_request`.';
	const safety = LLMS.slice(LLMS.indexOf('## How the safety model works'), LLMS.indexOf('## Instructions for AI agents'));
	const instructions = LLMS.slice(LLMS.indexOf('## Instructions for AI agents'), LLMS.indexOf('## Connect an MCP client'));
	assert.ok(safety.includes(rule), 'safety model');
	assert.ok(instructions.includes(rule), 'instructions');
	assert.ok(instructions.includes('Call `whats_new` when a tool you expect is missing or behaves differently.'));
});

test('llms.txt lists the released MCP tools, with env-set and route edits under plan and confirm', () => {
	const tools = LLMS.slice(LLMS.indexOf('## MCP tools'), LLMS.indexOf('## Add a host'));
	for (const name of [
		'list_orgs', 'check_readiness', 'list_env_sets', 'create_env_set', 'update_env_set', 'delete_env_set',
		'request_env_value', 'get_env_value_request', 'request_file', 'get_file_request', 'connect_registry',
		'get_registry_connect_request', 'image_access', 'host_policy_snippet', 'shared_network_snippet', 'whats_new',
		'list_routes', 'set_routes'
	]) {
		assert.ok(tools.includes(`\`${name}\``), name);
	}
	const change = tools.split('\n').find((line) => line.startsWith('- Change, with plan and confirm:')) ?? '';
	for (const name of ['create_env_set', 'update_env_set', 'delete_env_set', 'set_routes']) {
		assert.ok(change.includes(`\`${name}\``), name);
	}
});

test('llms.txt explains public addresses: domains and the edge switch are console-only, routes go through set_routes', () => {
	const part = LLMS.slice(LLMS.indexOf('## Public addresses'), LLMS.indexOf('## Add a host'));
	assert.ok(part.startsWith('## Public addresses\n'));
	assert.ok(part.includes('there is no MCP tool for domains or for turning the edge on'));
	assert.ok(part.includes('`set_routes`') && part.includes('`list_routes`'));
	assert.ok(part.includes('client IPs from Cloudflare') && part.includes('no-store paths'));
	assert.ok(part.includes('pending') && part.includes('live'));
	assert.ok(LLMS.includes('catalog, env-set and route edits'));
});

test('llms.txt explains the components in text', () => {
	assert.ok(
		LLMS.includes(
			'## Components\n\nConsole and MCP gateway both go through the API; only the API writes PostgreSQL, where secrets are encrypted. bot-agent runs unprivileged on each host and dials out to the API, so hosts open no ports. A root runner on the host checks the root-owned /etc/bot-agent/runner.toml allowlist before it runs Docker containers or systemd units.\n'
		)
	);
});

test('robots.txt allows all crawlers and names the apex sitemap', () => {
	// Umbrella docs/spec-seo-site.md §3.4.
	assert.equal(
		readFileSync(new URL('../static/robots.txt', import.meta.url), 'utf8'),
		'User-agent: *\nAllow: /\nSitemap: https://deploybot.dev/sitemap.xml\n'
	);
});
