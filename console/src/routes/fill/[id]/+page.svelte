<script lang="ts">
	import { enhance } from '$app/forms';
	import { tick } from 'svelte';
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import ValueSourceFields from '$lib/components/ValueSourceFields.svelte';
	import type { GenerateKind } from '$lib/env-generate';
	import { FILL_TEXT, fillSubject, lineBreakCheck, type FillSource } from '$lib/fill-request';
	import EnvRevealDialog from '../../projects/[id]/EnvRevealDialog.svelte';
	import { PASSWORD_MANAGER_IGNORE, secretInput, supportsTextMask } from '$lib/secret-input';

	// A masked secret is a CSS-masked text input where the browser can, so no "save password?" prompt ($lib/secret-input).
	let canMask = $state(false);
	$effect(() => {
		canMask = supportsTextMask();
	});

	let { data, form } = $props();

	let source = $state<FillSource>('value');
	let kind = $state<GenerateKind>('password');
	let shown = $state(false);
	// Pasted or dropped text with line breaks: stopped before it reaches the field, so nothing broken is posted (api-v1 v1.39 item 3).
	let lineBreak = $state(false);
	const valueInput = $derived(secretInput(!shown, canMask));
	// The key whose generated value the show-once dialog shows, or null.
	let revealKey = $state<string | null>(null);
	let revealDone = $state(false);

	const request = $derived(data.request);
	// A private set (`@<entry>`, api-v1 v1.33 item 6) is named by its entry.
	const subject = $derived(request ? fillSubject(request.set, request.entry) : null);
	// A rotation (api-v1 v1.33 item 3): the key already has a secret value that this save replaces.
	const replacing = $derived(request?.replace === true && data.view === 'form');
	const saved = $derived(form != null && 'saved' in form && form.saved === true);
	const generated = $derived(saved && form != null && 'revealPending' in form && form.revealPending === true);
	const declined = $derived(form != null && 'declined' in form && form.declined === true);
	const message = $derived(form != null && 'message' in form && typeof form.message === 'string' ? form.message : data.message);
	// A save or decline the API refused for good (used, expired, not found): the form goes, the message stays.
	const closed = $derived(form != null && 'closed' in form && form.closed === true);
	// After a save or decline the reload says the request is used; the outcome notice replaces that.
	const problem = $derived(data.view === 'form' || data.view === 'error' || saved || declined || closed ? null : FILL_TEXT[data.view]);
	const showForm = $derived(data.view === 'form' && request != null && !saved && !declined && !closed);

	// A generated value opens the show-once dialog once, right after the save.
	$effect(() => {
		if (generated && request && !revealDone && revealKey == null) revealKey = request.key;
	});

	/** Move focus to the outcome, so a screen reader hears it and the next Tab starts there. */
	async function focusResult() {
		await tick();
		document.getElementById('fill-result')?.focus();
	}

	// A typed save, a decline or a refusal: focus the outcome. A generated save waits for the dialog.
	$effect(() => {
		if ((saved && !generated) || declined || closed) void focusResult();
	});

	/** A one-line input drops line breaks from inserted text; stop such a paste or drop and say why instead. */
	function onValueInsert(event: Event, text: string | undefined) {
		lineBreak = lineBreakCheck(text).block;
		if (lineBreak) event.preventDefault();
	}

	/** Switching the value source remounts the field, so a stale line-break error is dropped. */
	function onSource(next: FillSource) {
		source = next;
		lineBreak = false;
	}

	function afterReveal() {
		revealKey = null;
		revealDone = true;
		void focusResult();
	}
</script>

<svelte:head>
	<title>Enter a value · Deploybot</title>
</svelte:head>

<PageHeader
	title={replacing && request ? `Replace the value of ${request.key}` : 'Enter a value'}
	subtitle={replacing ? 'An AI agent asked you to replace a secret value. Only you can do it.' : 'An AI agent asked for an env value. Only you can fill it.'}
/>
<div id="fill-result" tabindex="-1" class="grid gap-2 outline-none">
	{#if message}
		<Notice kind="error">{message}</Notice>
	{/if}
	{#if problem}
		<Notice kind="info">{problem}</Notice>
	{/if}
	{#if saved}
		<Notice kind="success">{FILL_TEXT.saved}</Notice>
	{:else if declined}
		<Notice kind="info">{FILL_TEXT.declined}</Notice>
	{/if}
</div>

{#if request && data.view === 'form' && !closed}
	<dl class="mt-4 grid max-w-xl grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
		<dt class="text-muted-foreground">Project</dt>
		<dd class="break-words">{request.project_name}</dd>
		<dt class="text-muted-foreground">{subject?.label}</dt>
		<dd class="font-mono break-all" data-fill-subject>{subject?.name}</dd>
		<dt class="text-muted-foreground">Key</dt>
		<dd class="font-mono break-all">{request.key}</dd>
		<dt class="text-muted-foreground">Expires</dt>
		<dd><LocalTime iso={request.expires_at} /></dd>
	</dl>
	{#if request.reason !== ''}
		<section class="mt-4 max-w-xl" aria-labelledby="fill-reason-label">
			<h2 id="fill-reason-label" class="text-sm font-medium">Note from the agent</h2>
			<!-- Plain text: Svelte escapes it, and it is never a link. -->
			<p class="text-muted-foreground mt-1 text-sm break-words whitespace-pre-wrap">{request.reason}</p>
		</section>
	{/if}
{/if}

{#if showForm && request}
	<form method="POST" action="?/save" class="mt-6 grid max-w-xl gap-4" use:enhance>
		<p class="text-muted-foreground text-sm">{FILL_TEXT.safety}</p>
		{#if replacing}
			<p class="text-sm font-medium" data-replace-note>{FILL_TEXT.replaceNote}</p>
		{/if}
		<Input type="hidden" name="source" value={source} />
		<ValueSourceFields
			idOf={(part) => `fill-${part}`}
			{source}
			{kind}
			{onSource}
			onKind={(next) => (kind = next)}
			typeLabel="Type or paste"
		/>
		{#if source === 'generate'}
			<Input type="hidden" name="kind" value={kind} />
		{:else}
			<div class="grid gap-1.5">
				<Label for="fill-value">New value for {request.key}</Label>
				<div class="flex min-w-0 items-center gap-2">
					<Input
						id="fill-value"
						name="value"
						type={valueInput.type}
						autocomplete={valueInput.autocomplete}
						spellcheck="false"
						required
						class="min-w-0 flex-1 font-mono {valueInput.maskClass}"
						aria-describedby={lineBreak ? 'fill-value-hint fill-value-error' : 'fill-value-hint'}
						aria-invalid={lineBreak || undefined}
						onpaste={(event) => onValueInsert(event, event.clipboardData?.getData('text'))}
						ondrop={(event) => onValueInsert(event, event.dataTransfer?.getData('text'))}
						oninput={() => (lineBreak = false)}
						{...PASSWORD_MANAGER_IGNORE}
					/>
					<Button
						type="button"
						variant="outline"
						size="icon"
						aria-label="Show value"
						aria-pressed={shown}
						onclick={() => (shown = !shown)}
					>
						{#if shown}<EyeOff />{:else}<Eye />{/if}
					</Button>
				</div>
				<p id="fill-value-hint" class="text-muted-foreground text-sm">{FILL_TEXT.lineBreakHint}</p>
				{#if lineBreak}
					<div id="fill-value-error"><Notice kind="error">{FILL_TEXT.lineBreak}</Notice></div>
				{/if}
			</div>
		{/if}
		<div class="flex flex-wrap gap-2">
			<Button type="submit">{replacing ? 'Replace value' : 'Save'}</Button>
			<!-- Submits the empty decline form below, so a typed value is never posted with a decline. -->
			<Button type="submit" variant="outline" form="fill-decline">Decline</Button>
		</div>
	</form>
	<form id="fill-decline" method="POST" action="?/decline" use:enhance></form>
{/if}

{#if request && generated}
	<EnvRevealDialog setName={request.set} {revealKey} action="?/reveal" onClose={afterReveal} />
{/if}
