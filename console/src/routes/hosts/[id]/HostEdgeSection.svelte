<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import Power from '@lucide/svelte/icons/power';
	import { tick } from 'svelte';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import type { HostEdge } from '$lib/api-types';
	import { EDGE_POLICY_LINE, edgeStatusBadge } from '$lib/edge-routes';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';

	/**
	 * The host's edge (api-v1 v1.35 item 2): one Deploybot-managed Caddy on 80/443 that serves the routes of entries
	 * deployed here. Org owners and admins turn it on or off; the host's root must first allow it in runner.toml. A
	 * refusal (the runner.toml line, another process on 80 or 443) is the API's own text.
	 */
	let {
		hostName,
		edge,
		loadMessage = null,
		actionMessage = null,
		policyHref = null
	}: { hostName: string; edge: HostEdge | null; loadMessage?: string | null; actionMessage?: string | null; policyHref?: string | null } = $props();

	const badge = $derived(edge ? edgeStatusBadge(edge.status) : null);
	const EDGE_ERROR_ID = 'edge-error';
	let turningOn = $state(false);
</script>

<section id="edge" class="mt-8 grid min-w-0 gap-3" aria-labelledby="edge-heading">
	<h2 id="edge-heading" class="text-lg font-semibold tracking-tight">Edge</h2>
	<p class="text-muted-foreground text-sm">
		The edge serves this host's public addresses on ports 80 and 443 and gets their certificates by itself. Certificates come from Let's Encrypt, so ports 80 and 443 must reach this host from the internet, directly or through a proxy such as Cloudflare.
	</p>
	{#if actionMessage}
		<!-- A refused Turn off closes its confirm and focuses this (failureFocusId). -->
		<div id={EDGE_ERROR_ID} tabindex="-1" class="max-w-full min-w-0 rounded-md break-words focus-visible:ring-3 focus-visible:ring-ring" data-edge-error>
			<Notice kind="error">{actionMessage}</Notice>
		</div>
	{/if}
	{#if loadMessage}
		<Notice kind="error">{loadMessage}</Notice>
	{:else if edge}
		<div class="flex min-w-0 flex-wrap items-center gap-2">
			<span class="text-sm font-medium">{edge.enabled ? 'On' : 'Off'}</span>
			{#if badge}<StatusBadge status={badge.status} text={badge.text} />{/if}
			<span class="text-muted-foreground text-sm">{edge.routes} {edge.routes === 1 ? 'route' : 'routes'}</span>
		</div>
		{#if edge.detail}
			<p class="text-sm break-words" data-edge-detail>{edge.detail}</p>
		{/if}
		<dl class="text-muted-foreground grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
			<dt>Last applied</dt>
			<dd><LocalTime iso={edge.applied_at} /></dd>
			{#if edge.config_hash}
				<dt>Config</dt>
				<dd class="font-mono break-all">{edge.config_hash.slice(0, 16)}</dd>
			{/if}
		</dl>
		{#if !edge.available}
			<div class="grid min-w-0 gap-2" data-edge-unavailable>
				<p class="text-sm">
					This host does not allow the edge yet. Its root adds this line to /etc/bot-agent/runner.toml and restarts bot-agent (version 0.2.11 or later):
				</p>
				<CommandBlock command={EDGE_POLICY_LINE} label="Copy runner.toml line" ariaLabel="runner.toml line" />
				{#if policyHref}
					<p class="text-sm" data-edge-policy-link>
						If the host lets the console change its policy, <a class="underline underline-offset-4" href={policyHref}>allow the edge on the Policy tab</a> instead.
					</p>
				{/if}
			</div>
		{/if}
		<div class="flex flex-wrap gap-2">
			{#if edge.enabled}
				<ConfirmAction
					id="edge-off"
					action={actionUrlFrom(page.url, 'setEdge')}
					label="Turn off edge"
					confirmLabel="Turn off edge"
					icon={Power}
					destructive
					closeOnFailure
					failureFocusId={EDGE_ERROR_ID}
					hidden={[{ name: 'enabled', value: '0' }]}
					message="Turn off the edge on {hostName}? Every public address on this host stops answering until it is turned on again. Certificates are kept."
				/>
			{:else}
				<form
					method="POST"
					action={actionUrlFrom(page.url, 'setEdge')}
					use:enhance={({ cancel }) => {
						if (turningOn) {
							cancel();
							return;
						}
						turningOn = true;
						return async ({ update }) => {
							try {
								await update();
							} finally {
								turningOn = false;
							}
							await tick();
							document.getElementById(EDGE_ERROR_ID)?.focus();
						};
					}}
				>
					<Input type="hidden" name="enabled" value="1" />
					<Button type="submit" variant="outline" size="sm" disabled={!edge.available || turningOn} aria-busy={turningOn}>
						<Power class="size-4" />{turningOn ? 'Turning on…' : 'Turn on edge'}
					</Button>
				</form>
			{/if}
		</div>
	{/if}
</section>
