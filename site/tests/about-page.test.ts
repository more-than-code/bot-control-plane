// The About page (/ on the site) structure (umbrella docs/spec-about-page.md rev 1, approved by Joe 2026-10-29; See it in action per §14, rev 7).
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { render } from 'svelte/server';
import AboutPage from '../src/routes/+page.svelte';
import { GITHUB_URL, INSTALL_GUIDE_URL, SIGN_IN_URL } from '../src/lib/site/links.ts';

const root = fileURLToPath(new URL('..', import.meta.url));
const body = render(AboutPage).body.replace(/<!--[\s\S]*?-->/g, '');

function text(html: string): string {
	return html
		.replace(/<[^>]+>/g, '')
		.replaceAll('&amp;', '&')
		.replaceAll('&quot;', '"')
		.replaceAll('&#39;', "'")
		.replace(/\s+/g, ' ')
		.trim();
}

/** The markup of the section labelled by `id`, up to the next section. */
function section(id: string): string {
	const start = body.indexOf(`aria-labelledby="${id}"`);
	assert.ok(start >= 0, id);
	const end = body.indexOf('<section', start);
	return body.slice(body.indexOf('>', start) + 1, end < 0 ? undefined : end);
}

function anchors(html: string): string[] {
	return [...html.matchAll(/<a\b[^>]*>/g)].map((m) => m[0]);
}

describe('About page', () => {
	test('one h1, then an h2 per section in the spec order', () => {
		assert.equal(body.match(/<h1\b/g)?.length, 1);
		const sections = [...body.matchAll(/<section\b[^>]*aria-labelledby="([^"]+)"/g)].map((m) => m[1]);
		assert.deepEqual(sections, [
			'hero-title',
			'flow-title',
			'visual-title',
			'compare-title',
			'features-title',
			'ai-title',
			'how-title',
			'source-title',
			'start-title'
		]);
		for (const id of sections.slice(1)) assert.match(body, new RegExp(`<h2 id="${id}"`), id);
		assert.ok(text(section('compare-title')).startsWith('Three ways to let AI run your servers'));
		assert.ok(text(section('ai-title')).startsWith("What the AI can and can't do"));
		assert.ok(text(section('start-title')).startsWith('Get started'));
	});

	test('the comparison table cells match the spec exactly', () => {
		const table = section('compare-title').match(/<table[\s\S]*?<\/table>/)?.[0] ?? '';
		const rows = [...table.matchAll(/<tr\b[\s\S]*?<\/tr>/g)].map((row) =>
			[...row[0].matchAll(/<(th|td)\b[^>]*>([\s\S]*?)<\/\1>/g)].map((cell) => text(cell[2] ?? ''))
		);
		assert.deepEqual(rows, [
			['Aspect', 'AI in a terminal (SSH)', 'AI with your cloud tokens', 'Deploybot'],
			['What the AI can do', 'Any shell command', 'Anything the token allows', 'Only your catalog'],
			['Safeguard', 'Approving each command', 'Hoping the agent behaves', 'Limited by design, acts as you, confirm step'],
			[
				'Ports and keys',
				'SSH open, keys on a laptop',
				'Long-lived token in a file',
				'Hosts dial out, no keys for the AI'
			],
			[
				'When nobody is watching',
				'Nothing runs',
				'Nothing runs',
				'Health checks; a failed deploy rolls back'
			],
			['Best for', 'Debugging one box', 'Throwaway projects', 'Running production, teams']
		]);
		assert.equal(table.match(/<th scope="col"/g)?.length, 4);
		assert.equal(table.match(/<th scope="row"/g)?.length, 5);
		assert.match(table, /^<table[^>]*aria-labelledby="compare-title"/);
		assert.match(table, /^<table[^>]*class="[^"]*\bmax-sm:hidden\b/);
	});

	test('on phones the comparison is one card per approach with the same text as the table', () => {
		const compare = section('compare-title');
		const table = compare.match(/<table[\s\S]*?<\/table>/)?.[0] ?? '';
		const tableRows = [...table.matchAll(/<tr\b[\s\S]*?<\/tr>/g)].map((row) =>
			[...row[0].matchAll(/<(th|td)\b[^>]*>([\s\S]*?)<\/\1>/g)].map((cell) => text(cell[2] ?? ''))
		);
		const [header = [], ...aspectRows] = tableRows;
		const list = compare.match(/<ul\b[^>]*>[\s\S]*?<\/ul>/)?.[0] ?? '';
		assert.match(list, /^<ul[^>]*class="[^"]*\bsm:hidden\b/);
		const cards = [...list.matchAll(/<li\b[\s\S]*?<\/li>/g)].map((m) => m[0]);
		assert.equal(cards.length, 3);
		cards.forEach((card, index) => {
			const column = index + 1;
			assert.equal(text(card.match(/<h3\b[^>]*>([\s\S]*?)<\/h3>/)?.[1] ?? ''), header[column]);
			const terms = [...card.matchAll(/<dt\b[^>]*>([\s\S]*?)<\/dt>/g)].map((m) => text(m[1] ?? ''));
			const values = [...card.matchAll(/<dd\b[^>]*>([\s\S]*?)<\/dd>/g)].map((m) => text(m[1] ?? ''));
			assert.deepEqual(terms, aspectRows.map((row) => row[0]));
			assert.deepEqual(values, aspectRows.map((row) => row[column]));
		});
	});

	test('no product names, production names or versions on the page', () => {
		const page = text(body);
		const names = ['Claude', 'Codex', 'Grok', 'Cursor', 'Hatoba', 'ChatGPT', 'Copilot', 'Kubernetes', 'AWS', 'Azure'];
		names.push('Hetzner', 'DigitalOcean', 'Terraform', 'Ansible', 'Portainer', 'Coolify', 'Vercel', 'Gemini', 'OpenAI');
		for (const name of names) {
			assert.doesNotMatch(page, new RegExp(`\\b${name}\\b`, 'i'), name);
		}
		assert.doesNotMatch(body, /\bus1\b|mohiguide|\bapi2\b|console2|\bv\d+\.\d+\.\d+/i);
	});

	test('every image has alt, width and height, and lives under static/brand below 200 KB', () => {
		const images = [...body.matchAll(/<img\b[^>]*>/g)].map((m) => m[0]);
		for (const img of images) {
			assert.match(img, /\balt="[^"]+"/, img);
			assert.match(img, /\bwidth="\d+"/, img);
			assert.match(img, /\bheight="\d+"/, img);
			const src = img.match(/\bsrc="([^"]+)"/)?.[1] ?? '';
			assert.ok(src.startsWith('/brand/'), src);
			assert.ok(statSync(join(root, 'static', src)).size < 200 * 1024, src);
		}
	});

	test('both Get started links go to the console sign-in in the same tab; View on GitHub opens the repo in a new tab', () => {
		assert.equal(SIGN_IN_URL, 'https://console.deploybot.dev/signin');
		for (const id of ['hero-title', 'start-title']) {
			const start = anchors(section(id)).filter((a) => a.includes(`href="${SIGN_IN_URL}"`));
			assert.equal(start.length, 1, id);
			assert.doesNotMatch(start[0] ?? '', /target=/, id);
		}
		assert.equal(GITHUB_URL, 'https://github.com/more-than-code/deploybot');
		const hero = section('hero-title');
		const github = anchors(hero).find((a) => a.includes(`href="${GITHUB_URL}"`)) ?? '';
		assert.match(github, /target="_blank"/);
		assert.match(github, /rel="noopener noreferrer"/);
		assert.ok(text(hero).includes('View on GitHub (opens in a new tab)'));
		assert.ok(text(hero).includes('Agent for Linux amd64 and arm64 · Open source'));
	});

	test('See it in action is one illustrative chat thread with generic names', () => {
		const visual = section('visual-title');
		assert.ok(text(visual).startsWith('See it in action'));
		const chat = visual.match(/<figure\b[\s\S]*?<\/figure>/)?.[0] ?? '';
		// Each line is its own <p>; join them with a space.
		const messages = [...chat.matchAll(/<li\b[\s\S]*?<\/li>/g)].map((m) => text(m[0].replaceAll('</p>', ' </p>')));
		assert.deepEqual(messages, [
			'You: Deploy my-website to my-raspberry-pi.',
			"AI agent: Deploybot's plan: will deploy my-website (docker.io/library/nginx:alpine) on my-raspberry-pi Deploy it?",
			'You: Yes.',
			'AI agent: Deployed and healthy. succeeded sha256:785835ea65db "GET / HTTP/1.1" 200',
			'You: Open a root shell on my-raspberry-pi.',
			"AI agent: Deploybot refused: that isn't in your catalog. not_found: not found"
		]);
		// Names never break at their hyphens.
		for (const token of ['my-website', 'my-raspberry-pi.']) {
			assert.ok(chat.includes(`<span class="whitespace-nowrap">${token}</span>`), token);
		}
		// No hyphenated word is left outside a no-wrap span.
		const outside = text(chat.replace(/<span class="whitespace-nowrap">[^<]*<\/span>/g, ' '));
		assert.doesNotMatch(outside, /\S-\S/);
		const captions = [...visual.matchAll(/<figcaption\b[^>]*>([\s\S]*?)<\/figcaption>/g)].map((m) => text(m[1] ?? ''));
		assert.deepEqual(captions, [
			'Your AI agent asks, you confirm, Deploybot does it. Anything outside your catalog is refused.',
			'In the console: your catalog, the only things your AI agent can deploy, and where each one runs.'
		]);
		for (const word of ['pi3-2', 'deploy_entry', 'confirmation_id']) {
			assert.equal(text(visual).includes(word), false, word);
		}
		assert.doesNotMatch(visual, /\b(role="tab|tablist|aria-selected)/);
	});

	test('beside the chat at 50/50: one name-free Catalog & services capture, a dark and a light image, frame aligned with the chat from lg, lazy', () => {
		// Retaken 2026-11-17 (Joe "both"): the merged Catalog & services tab of a demo project with generic names.
		const visual = section('visual-title');
		const tokens = (tag: string) => (tag.match(/\bclass="([^"]*)"/)?.[1] ?? '').split(' ');
		const grid = tokens(visual.match(/<div\b[^>]*>/)?.[0] ?? '');
		for (const name of ['grid', 'gap-6', 'lg:grid-cols-2', 'lg:grid-rows-[1fr_auto]', 'lg:gap-y-2']) {
			assert.ok(grid.includes(name), name);
		}
		const figures = [...visual.matchAll(/<figure\b[\s\S]*?<\/figure>/g)].map((m) => m[0]);
		assert.equal(figures.length, 2);
		// From lg both figures share the section's two rows (subgrid): equal frames, captions on one line.
		for (const figure of figures) {
			const classes = tokens(figure.match(/^<figure\b[^>]*>/)?.[0] ?? '');
			for (const name of ['lg:row-span-2', 'lg:grid', 'lg:grid-rows-subgrid']) assert.ok(classes.includes(name), name);
		}
		const shot = figures[1] ?? '';
		// The image is absolute in a relative frame from lg, so only the chat sets the row height.
		assert.ok(tokens(shot.match(/<div\b[^>]*>/)?.[0] ?? '').includes('lg:relative'));
		const images = [...visual.matchAll(/<img\b[^>]*>/g)].map((m) => m[0]);
		assert.equal(images.length, 2);
		assert.deepEqual(
			images.map((img) => img.match(/\bsrc="([^"]+)"/)?.[1]),
			['/brand/about-catalog-dark.webp', '/brand/about-catalog-light.webp']
		);
		const [dark = '', light = ''] = images;
		assert.match(dark, /class="[^"]*\bhidden dark:block\b/);
		assert.doesNotMatch(dark, /\bdark:hidden\b/);
		assert.match(light, /class="[^"]*\bblock dark:hidden\b/);
		assert.doesNotMatch(light, /\bdark:block\b/);
		for (const img of images) {
			assert.ok(shot.includes(img), 'both images sit in the capture figure');
			assert.match(
				img,
				/\balt="The Deploybot console's Catalog &amp; services tab: eight entries such as my-website \(nginx\), my-cache \(redis\) and home-assistant\. my-website is expanded to show its copy running on my-raspberry-pi on port 8088; my-cache shows one running copy; the rest are not deployed\."/,
				img
			);
			assert.match(img, /\bwidth="784"/, img);
			assert.match(img, /\bheight="678"/, img);
			assert.match(img, /\bloading="lazy"/, img);
			assert.match(img, /\bdecoding="async"/, img);
			// Natural size below lg; cropping (object-cover) and a forced height only from lg.
			const classes = (img.match(/\bclass="([^"]*)"/)?.[1] ?? '').split(' ');
			assert.ok(classes.includes('h-auto') && classes.includes('w-full'), img);
			for (const name of classes.filter((c) => /object-|h-full/.test(c))) {
				assert.ok(name.startsWith('lg:'), name);
			}
			for (const name of ['lg:absolute', 'lg:object-cover', 'lg:object-left-top']) assert.ok(classes.includes(name), name);
			const src = img.match(/\bsrc="([^"]+)"/)?.[1] ?? '';
			assert.ok(statSync(join(root, 'static', src)).size < 200 * 1024, src);
		}
	});

	test('no dev or production project and host names anywhere on the page, alt texts included', () => {
		const alts = [...body.matchAll(/\balt="([^"]*)"/g)].map((m) => m[1] ?? '').join(' ');
		const page = `${body} ${alts}`;
		for (const name of ['mohiguide', 'mo-services', 'mo-webadmin', 'joe-and-his-friends', 'grok-bot-box', 'pi3-2', 'home-lab', 'us1']) {
			assert.equal(page.toLowerCase().includes(name), false, name);
		}
		assert.doesNotMatch(page, /\bpi[1-4]\b/i);
		// The dev project "Home Lab"; matched by case, since the diagram's generic "home lab" label is fine.
		assert.equal(page.includes('Home Lab'), false, 'Home Lab');
	});

	test('static/brand holds only the known brand assets and About captures, so a screenshot cannot slip in unnoticed', () => {
		assert.deepEqual(readdirSync(join(root, 'static/brand')).sort(), [
			'about-catalog-dark.webp',
			'about-catalog-light.webp',
			'apple-touch-icon.png',
			'favicon-16.png',
			'favicon-32.png',
			'favicon.svg',
			'icon-192.png',
			'icon-512.png',
			'lockup-dark.svg',
			'lockup-light.svg',
			'og-image.png'
		]);
	});

	test('What you get has the eight feature groups by name, the same as llms.txt Features', () => {
		// Bot Product 2026-11-17: one set of feature names on the site and in llms.txt. No routes or domains yet.
		const groups: [string, string[]][] = [
			['Safety', ['Catalog', 'Plan and confirm', 'Acts as you', 'Host allowlist', 'Secret requests (one-time page)', 'Audit log']],
			['Deploys', ['Health-gated deploys', 'Automatic rollback', 'Digest rollback', 'Health policy', 'Run/stop/restart', 'Persistent programs']],
			['Configuration', ['Env sets', 'Entry secrets (generate, show once)', 'Managed files', 'Registry logins', 'Readiness check']],
			['Networking', ['Public addresses', 'Route options', 'Shared networks']],
			['Visibility', ['Host metrics', 'Processes and services', 'Health history', 'Live logs (redacted, capped']],
			['Teams', ['Organizations', 'Project roles', 'Per-entry rights', 'Host access per project', 'Invite links', 'Join tokens']],
			['For AI agents', ['MCP server with OAuth', 'deploybot login', 'llms.txt', 'whats_new']],
			['Upkeep', ["What's new page", 'Agent update notice']]
		];
		const html = section('features-title');
		const titles = [...html.matchAll(/<h3\b[^>]*>([\s\S]*?)<\/h3>/g)].map((m) => text(m[1] ?? ''));
		assert.deepEqual(titles, groups.map(([title]) => title));
		const llms = readFileSync(join(root, 'static/llms.txt'), 'utf8');
		const llmsFeatures = llms.slice(llms.indexOf('## Features'), llms.indexOf('## Components'));
		const cards = html.split(/<h3\b/).slice(1).map(text);
		groups.forEach(([title, names], i) => {
			const line = llmsFeatures.split('\n').find((l) => l.startsWith(`- ${title}: `)) ?? '';
			for (const name of names) {
				assert.ok(cards[i]?.includes(name), `${title} card: ${name}`);
				assert.ok(line.includes(name), `llms.txt ${title}: ${name}`);
			}
		});
		const features = text(html);
		assert.ok(
			features.includes(
				'Secrets never pass through the AI: when an agent needs a password, token or file, it asks, and you fill it in on a one-time console page.'
			)
		);
		// Released in v0.2.38: public addresses on your own domain, served by a per-host edge.
		assert.ok(
			features.includes(
				'Give an entry an HTTPS address on your own domain, and let projects on one host reach each other when the host allows it.'
			)
		);
	});

	test('the AI section links to /llms.txt and lists what the AI can and cannot do', () => {
		const ai = section('ai-title');
		assert.ok(anchors(ai).some((a) => a.includes('href="/llms.txt"')));
		assert.doesNotMatch(body, /data-sveltekit-/, 'no client router on the static site');
		assert.ok(
			text(ai).includes(
				'Deploy, roll back, run, stop and restart catalog entries; read logs, status and metrics; add or change image catalog entries and their health checks; edit env sets and ask you for secret values or files; check readiness. Every change shows a plan first and waits for your confirm, within your role and per-entry rights.'
			)
		);
		assert.ok(text(ai).includes('Open a shell, see secret values, edit hosts, or act beyond your role.'));
	});

	test('the components diagram sits only in Open source, before the table, with accessible names and no colour literals', () => {
		const source = section('source-title');
		for (const id of ['components-title', 'components-portrait-title']) {
			assert.ok(source.includes(`<title id="${id}">Deploybot components</title>`), id);
		}
		assert.match(source, /<svg[^>]*role="img"[^>]*aria-labelledby="components-title" aria-describedby="components-desc"/);
		assert.ok(source.indexOf('components-title') < source.indexOf('<table'), 'diagram before the components table');
		assert.equal(body.match(/id="components-title"/g)?.length, 1, 'one components diagram on the page');
		const svelte = readFileSync(join(root, 'src/lib/site/ComponentsDiagram.svelte'), 'utf8');
		assert.doesNotMatch(svelte, /#[0-9a-f]{3,8}\b|rgb\(|oklch\(|style=/i);
		for (const label of ['Console', 'MCP gateway', 'API', 'PostgreSQL', 'Secrets encrypted', 'bot-agent', 'Root runner', 'Checks host allowlist', 'Dials out, no open ports']) {
			assert.ok(svelte.includes(`>${label}<`), label);
		}
	});

	test('Open source says the name and logo are not under the code licence, with no trademark claim', () => {
		const source = text(section('source-title'));
		assert.ok(
			source.includes(
				'The Deploybot name and logo are not licensed under any code licence; forks must use a different name.'
			)
		);
		assert.doesNotMatch(body, /™|&trade;|registered trademark/i);
	});

	test('every external link except the console sign-in opens in a new tab, safely, and says so', () => {
		const external = [...body.matchAll(/<a\b([^>]*)>([\s\S]*?)<\/a>/g)].filter(
			(m) => /\bhref="http/.test(m[1] ?? '') && !(m[1] ?? '').includes(`href="${SIGN_IN_URL}"`)
		);
		assert.ok(external.length >= 6, 'GitHub, install guide and the four components');
		for (const [, attrs = '', inner = ''] of external) {
			assert.match(attrs, /\btarget="_blank"/, attrs);
			assert.match(attrs, /\brel="noopener noreferrer"/, attrs);
			assert.match(inner, /<span class="sr-only">\s*\(opens in a new tab\)<\/span>/, attrs);
		}
		const start = section('start-title');
		assert.ok(anchors(start).some((a) => a.includes(`href="${INSTALL_GUIDE_URL}"`)));
		assert.equal(INSTALL_GUIDE_URL, 'https://github.com/more-than-code/bot-agent/blob/main/docs/install.md');
	});

	test('the diagram is flow only: no panels in either SVG or its desc', () => {
		const diagram = readFileSync(join(root, 'src/lib/site/PitchDiagram.svelte'), 'utf8');
		for (const word of ['SECURITY', 'RESILIENCE', 'VISIBILITY', 'TEAMS']) {
			assert.equal(diagram.includes(word), false, word);
		}
		for (const word of ['Security:', 'Resilience:', 'Visibility:', 'Teams:']) {
			assert.equal(diagram.includes(word), false, word);
		}
		assert.ok(diagram.includes('Anything outside the catalog is refused'));
	});
});
