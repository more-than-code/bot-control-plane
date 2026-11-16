<script lang="ts">
	import { enhance } from '$app/forms';
	import { tick } from 'svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import Textarea from '$lib/components/Textarea.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import {
		encodeBase64Utf8,
		FILE_FILL_TEXT,
		NO_PICK,
		pickOutcome,
		readLocalTextFile,
		secretEditable,
		secretFlag,
		submitOnce,
		type PickState
	} from '$lib/file-request';

	let { data, form } = $props();

	// Typed or pasted content lives only in this textarea and the one POST to ?/save. It stays after a
	// failed save (it is the person's own file) and is cleared once the form goes.
	let content = $state('');
	// A picked file: its exact text, shown read-only and posted as base64 (content_b64), never as the
	// textarea's value, which a browser sends with CRLF line breaks. Null `picked` is paste mode.
	let pick = $state<PickState>(NO_PICK);
	let pickInput = $state<HTMLInputElement | null>(null);
	// The person may make a plain request secret, never the other way (api-v1 v1.30 item 5).
	let ticked = $state(false);
	// A save or decline in flight: both buttons are disabled and a second submit is cancelled.
	let submitting = $state(false);
	const once = submitOnce(
		() => submitting,
		(pending) => (submitting = pending)
	);

	const request = $derived(data.request);
	const secretFile = $derived(secretFlag(request?.secret === true, ticked));
	const saved = $derived(form != null && 'saved' in form && form.saved === true);
	const declined = $derived(form != null && 'declined' in form && form.declined === true);
	const saveError = $derived(form != null && 'message' in form && typeof form.message === 'string' ? form.message : null);
	const message = $derived(saveError ?? data.message);
	// A save or decline the API refused for good (used, expired, not found): the form goes.
	const closed = $derived(form != null && 'closed' in form && form.closed === true);
	// The file exists by now: the request stays pending, so only Decline is left.
	const exists = $derived(form != null && 'exists' in form && form.exists === true);
	// After a save or decline the reload says the request is used; the outcome notice replaces that.
	const problem = $derived(data.view === 'form' || data.view === 'error' || saved || declined || closed ? null : FILE_FILL_TEXT[data.view]);
	const canDecline = $derived(data.view === 'form' && request != null && !saved && !declined && !closed);
	const showForm = $derived(canDecline && !exists);

	$effect(() => {
		if (!showForm) {
			content = '';
			pick = NO_PICK;
		}
	});

	/** Move focus to the outcome, so a screen reader hears it and the next Tab starts there. */
	async function focusResult() {
		await tick();
		document.getElementById('fill-result')?.focus();
	}

	$effect(() => {
		if (saved || declined || closed || exists) void focusResult();
	});

	/** Read a picked file as UTF-8 text. A refused file reads nothing in and returns to paste mode. */
	async function pickFile(event: Event) {
		const input = event.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		pick = pickOutcome(file.name, await readLocalTextFile(file));
		// The file input has no name, so the picked file itself is never posted.
		if (pick.picked == null) input.value = '';
	}

	/** Drop the picked file, return to paste mode and put focus back on the picker. */
	function clearPicked() {
		pick = NO_PICK;
		if (pickInput) {
			pickInput.value = '';
			pickInput.focus();
		}
	}
</script>

<svelte:head>
	<title>Add a file · Deploybot</title>
</svelte:head>

<PageHeader title="Add a file" subtitle="An AI agent asked for a project file. Only you can add it." />
<div id="fill-result" tabindex="-1" class="grid gap-2 outline-none">
	{#if message}
		<div id="fill-error">
			<Notice kind="error">{message}</Notice>
		</div>
	{/if}
	{#if problem}
		<Notice kind="info">{problem}</Notice>
	{/if}
	{#if saved}
		<Notice kind="success">{FILE_FILL_TEXT.saved}</Notice>
	{:else if declined}
		<Notice kind="info">{FILE_FILL_TEXT.declined}</Notice>
	{/if}
</div>

{#if request && data.view === 'form' && !closed}
	<dl class="mt-4 grid max-w-xl grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
		<dt class="text-muted-foreground">Project</dt>
		<dd class="break-words">{request.project_name}</dd>
		<dt class="text-muted-foreground">File</dt>
		<dd class="font-mono break-all">{request.name}</dd>
		<dt class="text-muted-foreground">Kind</dt>
		<dd>{request.secret ? 'Secret (stored encrypted)' : 'Plain'}</dd>
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
	<form method="POST" action="?/save" class="mt-6 grid max-w-xl gap-4" use:enhance={once}>
		<p class="text-muted-foreground text-sm">{FILE_FILL_TEXT.safety}</p>
		<div class="grid gap-1.5">
			<Label for="fill-content">Content of {request.name}</Label>
			{#if pick.picked}
				<!-- The picked file, read-only. It is posted byte-exact as content_b64, not as this value. -->
				<Textarea id="fill-content" value={pick.picked.text} readonly rows={10} spellcheck="false" class="font-mono" />
				<Input type="hidden" name="content_b64" value={encodeBase64Utf8(pick.picked.text)} />
			{:else}
				<Textarea
					id="fill-content"
					name="content"
					bind:value={content}
					rows={10}
					spellcheck="false"
					autocomplete="off"
					class="font-mono"
					aria-invalid={saveError ? true : undefined}
					aria-describedby={saveError ? 'fill-error' : undefined}
				/>
			{/if}
		</div>
		<div class="grid gap-1.5">
			<Label for="fill-pick">Or choose a text file (up to 64 KiB)</Label>
			<Input
				id="fill-pick"
				type="file"
				bind:ref={pickInput}
				aria-invalid={pick.error ? true : undefined}
				aria-describedby={pick.error ? 'fill-pick-error' : undefined}
				onchange={pickFile}
			/>
			<!-- Announces the picked file; empty (and hidden) in paste mode. -->
			<p role="status" class="text-muted-foreground text-sm break-all empty:hidden">{#if pick.picked}Using {pick.picked.name}, stored exactly as it is on disk.{/if}</p>
			{#if pick.picked}
				<div>
					<Button type="button" variant="outline" size="sm" onclick={clearPicked}>Clear file</Button>
				</div>
			{/if}
			{#if pick.error}
				<div id="fill-pick-error">
					<Notice kind="error">{pick.error}</Notice>
				</div>
			{/if}
		</div>
		<Input type="hidden" name="secret" value={secretFile ? '1' : '0'} />
		<Label class="flex items-center gap-2">
			<Checkbox
				id="fill-secret"
				checked={secretFile}
				disabled={!secretEditable(request.secret)}
				aria-describedby={request.secret ? 'fill-secret-note' : undefined}
				onCheckedChange={(value) => (ticked = value === true)}
			/>
			Secret
		</Label>
		{#if request.secret}
			<p id="fill-secret-note" class="text-muted-foreground text-sm">The agent asked for a secret file. A secret file cannot be made plain.</p>
		{/if}
		<div class="flex flex-wrap gap-2">
			<Button type="submit" disabled={submitting}>Save</Button>
			<!-- Submits the empty decline form below, so content is never posted with a decline. -->
			<Button type="submit" variant="outline" form="fill-decline" disabled={submitting}>Decline</Button>
		</div>
	</form>
{:else if canDecline && exists}
	<!-- The file exists by now and the request is still pending: the person can still close it. -->
	<div class="mt-6">
		<Button type="submit" variant="outline" form="fill-decline" disabled={submitting}>Decline</Button>
	</div>
{/if}
{#if canDecline}
	<form id="fill-decline" method="POST" action="?/decline" use:enhance={once}></form>
{/if}
