<script lang="ts">
	import { enhance } from '$app/forms';
	import { tick } from 'svelte';
	import Plug from '@lucide/svelte/icons/plug';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { Button } from '$lib/components/ui/button';
	import { CONNECT_TEXT, connectedText, replaceNote } from '$lib/connect-request';

	let { data, form } = $props();

	const request = $derived(data.request);
	const declined = $derived(form != null && 'declined' in form && form.declined === true);
	const message = $derived(form != null && 'message' in form && typeof form.message === 'string' ? form.message : data.message);
	// A Continue or Decline the API refused for good (used, expired, not found, not configured): the buttons go.
	const closed = $derived(form != null && 'closed' in form && form.closed === true);
	const outcome = $derived.by(() => {
		if (declined || closed) return null;
		if (data.connectedEnded) return { kind: 'info' as const, text: CONNECT_TEXT.connectedEnded };
		switch (data.view) {
			case 'connected':
				return { kind: 'success' as const, text: connectedText(request?.current_login ?? null) };
			case 'declined':
			case 'expired':
			case 'missing':
			case 'invalid':
				return { kind: 'info' as const, text: CONNECT_TEXT[data.view] };
			default:
				return null;
		}
	});
	const showButtons = $derived(data.view === 'form' && request != null && !declined && !closed);
	const replaces = $derived(request ? replaceNote(request.current_login) : null);

	// A decline, a refusal or a failed Continue: move focus to the outcome, so a screen reader hears it.
	const failedMessage = $derived(form != null && 'message' in form && typeof form.message === 'string');
	$effect(() => {
		if (declined || closed || failedMessage) {
			void tick().then(() => document.getElementById('connect-result')?.focus());
		}
	});
</script>

<svelte:head>
	<title>Connect GitHub · Deploybot</title>
</svelte:head>

<PageHeader title="Connect GitHub" subtitle="An AI agent asked to connect GitHub for a project. Only you can approve it." />
<div id="connect-result" tabindex="-1" class="grid gap-2 outline-none">
	{#if message}
		<Notice kind="error">{message}</Notice>
	{/if}
	{#if data.note && showButtons}
		<Notice kind={data.note.kind}>{data.note.text}</Notice>
	{/if}
	{#if outcome}
		<Notice kind={outcome.kind}>{outcome.text}</Notice>
	{/if}
	{#if declined}
		<Notice kind="info">{CONNECT_TEXT.declinedNow}</Notice>
	{/if}
</div>

{#if request && data.view === 'form' && !closed && !declined}
	<dl class="mt-4 grid max-w-xl grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
		<dt class="text-muted-foreground">Project</dt>
		<dd class="break-words">{request.project_name}</dd>
		<dt class="text-muted-foreground">Registry</dt>
		<dd class="font-mono break-all">ghcr.io</dd>
		<dt class="text-muted-foreground">Expires</dt>
		<dd><LocalTime iso={request.expires_at} /></dd>
	</dl>
	{#if replaces}
		<p class="mt-3 max-w-xl text-sm break-words">{replaces}</p>
	{/if}
	{#if request.reason !== ''}
		<section class="mt-4 max-w-xl" aria-labelledby="connect-reason-label">
			<h2 id="connect-reason-label" class="text-sm font-medium">Note from the agent</h2>
			<!-- Plain text: Svelte escapes it, and it is never a link. -->
			<p class="text-muted-foreground mt-1 text-sm break-words whitespace-pre-wrap">{request.reason}</p>
		</section>
	{/if}
{/if}

{#if showButtons}
	<div class="mt-6 grid max-w-xl gap-4">
		<p class="text-muted-foreground text-sm">{CONNECT_TEXT.safety}</p>
		<div class="flex flex-wrap gap-2">
			<!-- A plain form post: the action answers with a redirect to github.com, which an enhanced form cannot follow. -->
			<form method="POST" action="?/continue">
				<Button type="submit">
					<Plug class="size-4" />
					Continue to GitHub
				</Button>
			</form>
			<form method="POST" action="?/decline" use:enhance>
				<Button type="submit" variant="outline">Decline</Button>
			</form>
		</div>
	</div>
{/if}
