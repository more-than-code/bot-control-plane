<script lang="ts">
	import BellRingIcon from '@lucide/svelte/icons/bell-ring';
	import BotIcon from '@lucide/svelte/icons/bot';
	import CircleCheckIcon from '@lucide/svelte/icons/circle-check';
	import CircleXIcon from '@lucide/svelte/icons/circle-x';
	import EyeIcon from '@lucide/svelte/icons/eye';
	import NetworkIcon from '@lucide/svelte/icons/network';
	import RocketIcon from '@lucide/svelte/icons/rocket';
	import SlidersHorizontalIcon from '@lucide/svelte/icons/sliders-horizontal';
	import ShieldIcon from '@lucide/svelte/icons/shield';
	import UsersIcon from '@lucide/svelte/icons/users';
	import { GITHUB_URL, INSTALL_GUIDE_URL, REPO_URL, SIGN_IN_URL } from './links';
	import ComponentsDiagram from './ComponentsDiagram.svelte';
	import PitchDiagram from './PitchDiagram.svelte';

	const focus = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring';
	const primaryButton = `bg-primary text-primary-foreground hover:bg-primary/80 inline-flex h-10 items-center rounded-lg px-4 text-sm font-medium ${focus}`;
	const outlineButton = `bg-background hover:bg-muted inline-flex h-10 items-center rounded-lg border px-4 text-sm font-medium ${focus}`;
	const linkClass = `text-primary underline-offset-4 hover:underline ${focus}`;

	// See it in action (spec-about-page §14, rev 7): one illustrative chat thread, with a console capture beside it.
	// It follows Deploybot's real dev replies with generic names. The thread's names (my-website, my-raspberry-pi)
	// must match the Catalog capture and its alt text below.
	type Line = { text: string; tool?: boolean };
	const thread: { from: 'user' | 'agent'; lines: Line[] }[] = [
		{ from: 'user', lines: [{ text: 'Deploy my-website to my-raspberry-pi.' }] },
		{
			from: 'agent',
			lines: [
				{ text: "Deploybot's plan:" },
				{ text: 'will deploy my-website (docker.io/library/nginx:alpine) on my-raspberry-pi', tool: true },
				{ text: 'Deploy it?' }
			]
		},
		{ from: 'user', lines: [{ text: 'Yes.' }] },
		{
			from: 'agent',
			lines: [
				{ text: 'Deployed and healthy.' },
				{ text: 'succeeded sha256:785835ea65db', tool: true },
				{ text: '"GET / HTTP/1.1" 200', tool: true }
			]
		},
		{ from: 'user', lines: [{ text: 'Open a root shell on my-raspberry-pi.' }] },
		{
			from: 'agent',
			lines: [{ text: "Deploybot refused: that isn't in your catalog." }, { text: 'not_found: not found', tool: true }]
		}
	];
	/** Splits a line so each hyphenated word (a name like my-raspberry-pi) can sit in a no-wrap span: odd parts. */
	const wordsWithHyphens = (text: string) => text.split(/(\S*-\S*)/);

	// Spec §3.4: the three ways, cell text exact. The highlighted approach is Deploybot.
	const aspects = [
		{ key: 'can', label: 'What the AI can do' },
		{ key: 'safeguard', label: 'Safeguard' },
		{ key: 'ports', label: 'Ports and keys' },
		{ key: 'unattended', label: 'When nobody is watching' },
		{ key: 'best', label: 'Best for' }
	] as const;
	const approaches = [
		{
			name: 'AI in a terminal (SSH)',
			can: 'Any shell command',
			safeguard: 'Approving each command',
			ports: 'SSH open, keys on a laptop',
			unattended: 'Nothing runs',
			best: 'Debugging one box',
			highlight: false
		},
		{
			name: 'AI with your cloud tokens',
			can: 'Anything the token allows',
			safeguard: 'Hoping the agent behaves',
			ports: 'Long-lived token in a file',
			unattended: 'Nothing runs',
			best: 'Throwaway projects',
			highlight: false
		},
		{
			name: 'Deploybot',
			can: 'Only your catalog',
			safeguard: 'Limited by design, acts as you, confirm step',
			ports: 'Hosts dial out, no keys for the AI',
			unattended: 'Health checks; a failed deploy rolls back',
			best: 'Running production, teams',
			highlight: true
		}
	];

	// What you get: the feature names (Bot Product, 2026-11-17), the same groups and names as llms.txt Features.
	// Each item is a feature name with an optional short note.
	const features = [
		{
			title: 'Safety',
			icon: ShieldIcon,
			body: 'Secrets never pass through the AI: when an agent needs a password, token or file, it asks, and you fill it in on a one-time console page.',
			items: [
				{ text: 'Catalog', note: 'only approved images and programs run' },
				{ text: 'Plan and confirm', note: 'every change waits for your yes' },
				{ text: 'Acts as you', note: 'your role, no more' },
				{ text: 'Host allowlist', note: "each host's own, root-owned" },
				{ text: 'Secret requests', note: 'one-time page' },
				{ text: 'Audit log' }
			]
		},
		{
			title: 'Deploys',
			icon: RocketIcon,
			body: 'Deploys wait for health, and you can go back in one step.',
			items: [
				{ text: 'Health-gated deploys', note: 'Docker or HTTP/TCP checks' },
				{ text: 'Automatic rollback', note: 'when a deploy is unhealthy' },
				{ text: 'Digest rollback', note: 'to an earlier image digest' },
				{ text: 'Health policy', note: 'per service' },
				{ text: 'Run/stop/restart' },
				{ text: 'Persistent programs', note: 'systemd units that survive reboot' }
			]
		},
		{
			title: 'Configuration',
			icon: SlidersHorizontalIcon,
			body: 'Give each entry what it needs to run; secret values are encrypted at rest.',
			items: [
				{ text: 'Env sets', note: 'for containers and programs' },
				{ text: 'Entry secrets', note: 'generate, show once' },
				{ text: 'Managed files', note: 'for containers' },
				{ text: 'Registry logins', note: 'for private images' },
				{ text: 'Readiness check', note: 'before deploy' }
			]
		},
		{
			title: 'Networking',
			icon: NetworkIcon,
			body: 'Let projects on one host reach each other when the host allows it.',
			items: [{ text: 'Shared networks', note: "between projects on one host, offered by the host's admin" }]
		},
		{
			title: 'Visibility',
			icon: EyeIcon,
			body: 'See what runs on each host and how it is doing.',
			items: [
				{ text: 'Host metrics', note: 'with history' },
				{ text: 'Processes and services', note: 'running and discovered' },
				{ text: 'Health history', note: 'per service' },
				{ text: 'Live logs', note: 'redacted, capped' }
			]
		},
		{
			title: 'Teams',
			icon: UsersIcon,
			body: 'An organization owns the hosts; its projects get the access they need.',
			items: [
				{ text: 'Organizations', note: 'own the hosts; org admins manage them' },
				{ text: 'Project roles', note: 'owner, admin and viewer' },
				{ text: 'Per-entry rights', note: 'run, stop, restart or deploy' },
				{ text: 'Host access per project', note: 'metrics or deploy' },
				{ text: 'Invite links', note: 'expire, single-use by default' },
				{ text: 'Join tokens', note: 'one-time, for adding hosts' }
			]
		},
		{
			title: 'For AI agents',
			icon: BotIcon,
			body: 'Connect any MCP client; it signs in as you.',
			items: [
				{ text: 'MCP server with OAuth' },
				{ text: 'deploybot login', note: 'for clients without a browser' },
				{ text: 'llms.txt', note: 'a guide written for agents' },
				{ text: 'whats_new', note: 'recent changes, as an MCP tool' }
			]
		},
		{
			title: 'Upkeep',
			icon: BellRingIcon,
			body: 'See what changed and which hosts need an update.',
			items: [
				{ text: "What's new page", note: 'in the account menu' },
				{ text: 'Agent update notice', note: 'when a newer bot-agent release exists' }
			]
		}
	];

	const components = [
		{
			name: 'bot-agent',
			href: REPO_URL('bot-agent'),
			role: 'Runs on each host, dials out, deploys and reports',
			stack: 'Go, one static binary; Linux (amd64/arm64) with systemd; Docker only for container entries',
			licence: 'Apache-2.0'
		},
		{
			name: 'bot-control-plane/api',
			href: REPO_URL('bot-control-plane', 'api'),
			role: 'Google sign-in, projects and roles, catalog, audit log, OAuth for MCP clients',
			stack: 'Go, PostgreSQL with TimescaleDB',
			licence: 'AGPL-3.0-only'
		},
		{
			name: 'bot-control-plane/mcp',
			href: REPO_URL('bot-control-plane', 'mcp'),
			role: 'Standard MCP endpoint for AI agents',
			stack: 'Go, MCP over Streamable HTTP',
			licence: 'AGPL-3.0-only'
		},
		{
			name: 'bot-control-plane/console',
			href: REPO_URL('bot-control-plane', 'console'),
			role: 'The web console',
			stack: 'SvelteKit (Node.js)',
			licence: 'AGPL-3.0-only'
		}
	];

	const steps = [
		{ title: 'Enrol a host', body: 'Install the agent and join the host to your organization.' },
		{ title: 'Add a catalog entry', body: 'Describe the program or container you want to run.' },
		{ title: 'Deploy and watch', body: 'Deploy it to your hosts and view its health and logs.' }
	];

	const startSteps = [
		{ title: 'Sign in' },
		{ title: 'Install the agent on a host', href: INSTALL_GUIDE_URL },
		{ title: 'Deploy your first catalog entry' }
	];
</script>

<section class="py-10 sm:py-16" aria-labelledby="hero-title">
	<h1 id="hero-title" class="text-4xl font-semibold tracking-tight sm:text-5xl">
		Let AI run your servers, without the keys.
	</h1>
	<p class="text-muted-foreground mt-4 max-w-2xl text-lg">
		Let any AI agent that speaks MCP run your own servers, without handing it SSH keys or a cloud token. It can
		only deploy what your catalog allows, as you, after a confirm step.
	</p>
	<div class="mt-8 flex flex-wrap gap-3">
		<a class={primaryButton} href={SIGN_IN_URL} data-sveltekit-reload>Get started</a>
		<a class={outlineButton} href={GITHUB_URL} rel="noopener noreferrer" target="_blank"
			>View on GitHub<span class="sr-only">{' (opens in a new tab)'}</span></a
		>
	</div>
	<p class="text-muted-foreground mt-3 text-sm">Agent for Linux amd64 and arm64 · Open source</p>
</section>

<section class="py-8" aria-labelledby="flow-title">
	<h2 id="flow-title" class="text-2xl font-semibold tracking-tight">From your AI agent to your servers</h2>
	<div class="mt-4">
		<PitchDiagram />
	</div>
</section>

<section class="py-8" aria-labelledby="visual-title">
	<h2 id="visual-title" class="text-2xl font-semibold tracking-tight">See it in action</h2>
	<!-- The chat alone (Joe 2026-11-17: the stale Catalog capture is dropped until a retake). -->
	<figure class="mt-4 max-w-2xl min-w-0">
		<div class="bg-card overflow-hidden rounded-lg border">
			<div class="bg-muted flex items-center gap-1.5 border-b px-3 py-1.5" aria-hidden="true">
				<span class="bg-muted-foreground/40 size-2 rounded-full"></span>
				<span class="bg-muted-foreground/40 size-2 rounded-full"></span>
				<span class="bg-muted-foreground/40 size-2 rounded-full"></span>
				<span class="text-muted-foreground ml-2 text-xs">AI agent</span>
			</div>
			<ol class="flex flex-col gap-2 p-3 text-sm sm:p-4" role="list">
				{#each thread as message (message)}
					<li
						class={[
							'max-w-[85%] space-y-1.5 rounded-lg px-3 py-2',
							message.from === 'user' ? 'bg-primary text-primary-foreground self-end' : 'bg-muted self-start'
						]}
					>
						<span class="sr-only">{message.from === 'user' ? 'You: ' : 'AI agent: '}</span>
						{#each message.lines as line (line)}
							<p class={[line.tool && 'text-muted-foreground border-l-2 pl-2 font-mono text-xs']}>
								{#each wordsWithHyphens(line.text) as part, i}{#if i % 2}<span class="whitespace-nowrap"
											>{part}</span
										>{:else}{part}{/if}{/each}
							</p>
						{/each}
					</li>
				{/each}
			</ol>
		</div>
		<figcaption class="text-muted-foreground mt-2 text-sm">
			Your AI agent asks, you confirm, Deploybot does it. Anything outside your catalog is refused.
		</figcaption>
	</figure>
</section>

<section class="py-8" aria-labelledby="compare-title">
	<h2 id="compare-title" class="text-2xl font-semibold tracking-tight">Three ways to let AI run your servers</h2>
	<!-- From sm up: the comparison table. On phones: one card per approach (the table is hidden there). -->
	<table class="mt-4 w-full text-left text-sm max-sm:hidden" aria-labelledby="compare-title">
		<thead>
			<tr class="border-b">
				<th scope="col" class="py-2 pr-4"><span class="sr-only">Aspect</span></th>
				{#each approaches as approach (approach.name)}
					<th
						scope="col"
						class={[
							'px-3 py-2 align-bottom font-medium',
							approach.highlight && 'bg-primary/5 text-primary border-primary rounded-t-lg border-t-2'
						]}>{approach.name}</th
					>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#each aspects as aspect (aspect.key)}
				<tr class="border-b align-top">
					<th scope="row" class="py-2 pr-4 font-medium">{aspect.label}</th>
					{#each approaches as approach (approach.name)}
						<td class={['px-3 py-2', approach.highlight ? 'bg-primary/5' : 'text-muted-foreground']}
							>{approach[aspect.key]}</td
						>
					{/each}
				</tr>
			{/each}
		</tbody>
	</table>
	<ul class="mt-4 grid gap-4 sm:hidden">
		{#each approaches as approach (approach.name)}
			<li
				class={[
					'rounded-xl border p-4',
					approach.highlight && 'bg-primary/5 border-primary border-t-2'
				]}
			>
				<h3 class={['font-semibold', approach.highlight && 'text-primary']}>{approach.name}</h3>
				<dl class="mt-2 space-y-2 text-sm">
					{#each aspects as aspect (aspect.key)}
						<div>
							<dt class="font-medium">{aspect.label}</dt>
							<dd class="text-muted-foreground">{approach[aspect.key]}</dd>
						</div>
					{/each}
				</dl>
			</li>
		{/each}
	</ul>
</section>

<section class="py-8" aria-labelledby="features-title">
	<h2 id="features-title" class="text-2xl font-semibold tracking-tight">What you get</h2>
	<div class="mt-4 grid gap-4 md:grid-cols-2">
		{#each features as feature (feature.title)}
			<div class="rounded-xl border p-5">
				<div class="flex items-center gap-2">
					<feature.icon class="text-primary size-5 shrink-0" aria-hidden="true" />
					<h3 class="font-semibold">{feature.title}</h3>
				</div>
				<p class="text-muted-foreground mt-2 text-sm">{feature.body}</p>
				<ul class="mt-3 list-disc space-y-1 pl-5 text-sm marker:text-primary">
					{#each feature.items as item (item.text)}
						<li>
							<span class="font-medium">{item.text}</span>{#if item.note}<span class="text-muted-foreground"
									>{` (${item.note})`}</span
								>{/if}
						</li>
					{/each}
				</ul>
			</div>
		{/each}
	</div>
</section>

<section class="py-8" aria-labelledby="ai-title">
	<h2 id="ai-title" class="text-2xl font-semibold tracking-tight">What the AI can and can't do</h2>
	<div class="mt-4 grid gap-4 md:grid-cols-2">
		<div class="rounded-xl border p-5">
			<div class="flex items-center gap-2">
				<CircleCheckIcon class="text-primary size-5 shrink-0" aria-hidden="true" />
				<h3 class="font-semibold">Can</h3>
			</div>
			<p class="text-muted-foreground mt-2 text-sm">
				Deploy, roll back, run, stop and restart catalog entries; read logs, status and metrics; add or change image catalog
				entries and their health checks; edit env sets and ask you for secret values or files; check readiness. Every change shows a plan first and waits for your confirm, within your role and
				per-entry rights.
			</p>
		</div>
		<div class="rounded-xl border p-5">
			<div class="flex items-center gap-2">
				<CircleXIcon class="text-destructive size-5 shrink-0" aria-hidden="true" />
				<h3 class="font-semibold">Can't</h3>
			</div>
			<p class="text-muted-foreground mt-2 text-sm">
				Open a shell, see secret values, edit hosts, or act beyond your role.
			</p>
		</div>
	</div>
	<p class="mt-4 text-sm">
		<a class={linkClass} href="/llms.txt" data-sveltekit-reload>Read the full list for AI agents</a>
	</p>
</section>

<section class="py-8" aria-labelledby="how-title">
	<h2 id="how-title" class="text-2xl font-semibold tracking-tight">How it works</h2>
	<p class="text-muted-foreground mt-2">Three steps from a new host to a running service.</p>
	<ol class="mt-4 grid gap-4 sm:grid-cols-3">
		{#each steps as step, index (step.title)}
			<li class="rounded-xl border p-5">
				<p class="text-muted-foreground text-sm font-medium">Step {index + 1}</p>
				<h3 class="mt-1 font-semibold">{step.title}</h3>
				<p class="text-muted-foreground mt-2 text-sm">{step.body}</p>
			</li>
		{/each}
	</ol>
</section>

<section class="py-8" aria-labelledby="source-title">
	<h2 id="source-title" class="text-2xl font-semibold tracking-tight">Open source</h2>
	<p class="text-muted-foreground mt-2">Use Deploybot hosted, or run all of it yourself from these two repos.</p>
	<figure class="mt-4">
		<ComponentsDiagram />
		<figcaption class="text-muted-foreground mt-2 text-sm">
			The console and the MCP gateway both go through the API; hosts dial out, and a root runner checks each host's
			allowlist.
		</figcaption>
	</figure>
	<table class="mt-4 w-full text-left text-sm max-sm:block">
		<thead class="max-sm:sr-only">
			<tr class="border-b">
				<th scope="col" class="py-2 pr-4 font-medium">Component</th>
				<th scope="col" class="py-2 pr-4 font-medium">What it does</th>
				<th scope="col" class="py-2 pr-4 font-medium">Built with</th>
				<th scope="col" class="py-2 font-medium">Licence</th>
			</tr>
		</thead>
		<tbody class="max-sm:grid max-sm:gap-4">
			{#each components as component (component.name)}
				<tr class="border-b align-top max-sm:block max-sm:rounded-xl max-sm:border max-sm:p-4">
					<th scope="row" class="py-2 pr-4 font-medium whitespace-nowrap max-sm:block max-sm:p-0">
						<a
							class={linkClass}
							href={component.href}
							rel="noopener noreferrer"
							target="_blank">{component.name}<span class="sr-only">{' (opens in a new tab)'}</span></a
						>
					</th>
					<td class="py-2 pr-4 max-sm:block max-sm:p-0 max-sm:pt-2">{component.role}</td>
					<td class="text-muted-foreground py-2 pr-4 max-sm:block max-sm:p-0 max-sm:pt-1">
						<span class="font-medium sm:hidden">Built with:{' '}</span>{component.stack}
					</td>
					<td class="text-muted-foreground py-2 whitespace-nowrap max-sm:block max-sm:p-0 max-sm:pt-1">
						<span class="font-medium sm:hidden">Licence:{' '}</span>{component.licence}
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
	<p class="text-muted-foreground mt-4 text-sm">
		To self-host you need a Linux server, PostgreSQL 18 with TimescaleDB, a Google OAuth client, a TLS reverse proxy
		(agents connect over wss://) and a secrets key file for encrypted values. Each README has the steps.
	</p>
	<p class="text-muted-foreground mt-2 text-sm">
		The Deploybot name and logo are not licensed under any code licence; forks must use a different name.
	</p>
</section>

<section class="py-8" aria-labelledby="start-title">
	<h2 id="start-title" class="text-2xl font-semibold tracking-tight">Get started</h2>
	<ol class="mt-4 grid gap-4 sm:grid-cols-3">
		{#each startSteps as step, index (step.title)}
			<li class="rounded-xl border p-5">
				<p class="text-muted-foreground text-sm font-medium">Step {index + 1}</p>
				<p class="mt-1 font-semibold">
					{#if step.href}
						<a class={linkClass} href={step.href} rel="noopener noreferrer" target="_blank"
							>{step.title}<span class="sr-only">{' (opens in a new tab)'}</span></a
						>
					{:else}
						{step.title}
					{/if}
				</p>
			</li>
		{/each}
	</ol>
	<a class={`${primaryButton} mt-6`} href={SIGN_IN_URL} data-sveltekit-reload>Get started</a>
</section>
