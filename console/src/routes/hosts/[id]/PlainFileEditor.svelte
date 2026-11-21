<script lang="ts">
	import { enhance } from '$app/forms';
	import { beforeNavigate, invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import Link from '@lucide/svelte/icons/link';
	import Save from '@lucide/svelte/icons/save';
	import { untrack } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import Textarea from '$lib/components/Textarea.svelte';
	import { hasManagedUrl, hostFileHref, insertManagedUrl, plainSaveAllowed, unsavedLeave, versionText } from '$lib/host-files';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { actionUrlFrom } from '$lib/url';
	import FileDiff from './FileDiff.svelte';

	/**
	 * A plain host file's editor: the latest saved text, its diff against the applied version, Save and
	 * "Insert Deploybot link". The text is sent as typed (CRLF becomes LF). A failed Save keeps it
	 * (`failedContent`), and so does a newer version saved meanwhile while the text has unsaved changes.
	 */
	let {
		hostId,
		orgId,
		name,
		content,
		latest,
		applied,
		appliedContent,
		failedContent = null,
		onDraft
	}: {
		hostId: string;
		orgId: string;
		name: string;
		/** The latest saved version's text ("" when none is saved). */
		content: string;
		latest: number | null;
		applied: number | null;
		/** The applied version's text, the diff base; null when nothing is applied. */
		appliedContent: string | null;
		/** The text of a Save that just failed. */
		failedContent?: string | null;
		/** The editor text differs from the latest saved version. */
		onDraft?: (unsaved: boolean) => void;
	} = $props();

	const DIFF_DEBOUNCE_MS = 250;

	/** The file this editor was opened for; the page remounts the editor for another file. */
	const owner = untrack(() => name);
	let draft = $state(untrack(() => failedContent ?? content));
	/** The saved text the draft was last in step with. */
	let base = untrack(() => content);
	let seen = untrack(() => `${name}:${latest}`);
	let saving = $state(false);
	/** A newer version arrived while the text had unsaved changes: its number. */
	let changedTo = $state<number | null>(null);
	/** A navigation away was stopped once because of unsaved text. */
	let warned = $state(false);
	/** The text the diff shows, a moment behind the editor. */
	let diffText = $state(untrack(() => draft));

	// A new saved version (after Save, or another admin's) replaces the editor text unless it has unsaved changes.
	$effect(() => {
		const key = `${name}:${latest}`;
		if (key === seen) return;
		seen = key;
		const previous = base;
		base = content;
		if (untrack(() => draft) === previous || untrack(() => draft) === content) {
			draft = content;
			changedTo = null;
		} else {
			changedTo = latest;
		}
	});

	const unsaved = $derived(draft !== content);

	$effect(() => {
		onDraft?.(unsaved);
		if (!unsaved) warned = false;
	});

	$effect(() => {
		const text = draft;
		const timer = setTimeout(() => (diffText = text), DIFF_DEBOUNCE_MS);
		return () => clearTimeout(timer);
	});

	// Leaving with unsaved text: a page unload gets the browser's own prompt; a link is stopped once with a note.
	beforeNavigate((navigation) => {
		const decision = unsavedLeave({
			unsaved,
			saving,
			type: navigation.type,
			to: navigation.to?.url ?? null,
			here: page.url,
			name: owner,
			warned
		});
		if (decision === 'go') return;
		navigation.cancel();
		if (decision === 'warn') warned = true;
	});

	const linkPresent = $derived(hasManagedUrl(draft));
	const linkHintId = $props.id();

	function insertLink() {
		draft = insertManagedUrl(draft, `${page.url.origin}${hostFileHref(hostId, name)}`);
	}
</script>

<form
	method="POST"
	action={actionUrlFrom(page.url, 'saveFile')}
	class="grid gap-3"
	use:enhance={({ cancel }) => {
		// aria-disabled keeps focus on the button, so the submit is stopped here.
		if (saving || !plainSaveAllowed(owner, name, draft, content)) {
			cancel();
			return;
		}
		saving = true;
		return async ({ result, update }) => {
			try {
				await update({ reset: false });
				// The file may have changed meanwhile: read it again; the editor keeps the typed text.
				if (result.type === 'failure') await invalidateAll();
			} finally {
				saving = false;
			}
		};
	}}
>
	<Input type="hidden" name="org" value={orgId} />
	<Input type="hidden" name="name" value={name} />
	<Label for="host-file-content">Content (latest saved: {versionText(latest)})</Label>
	{#if changedTo != null && unsaved}
		<Notice kind="info">The file changed since you opened it (now v{changedTo}). Your text is kept; Save makes it the next version.</Notice>
	{/if}
	{#if warned}
		<Notice kind="info">You have unsaved changes. Save them, or follow the link again to leave without saving.</Notice>
	{/if}
	<Textarea
		id="host-file-content"
		name="content"
		class="min-h-72 font-mono text-xs"
		spellcheck={false}
		autocomplete="off"
		bind:value={draft}
	/>
	<div class="flex flex-wrap items-center gap-2">
		<Button type="submit" aria-disabled={saving || !unsaved ? 'true' : undefined} class={saving || !unsaved ? 'opacity-50' : undefined}
			><Save class="size-4" />Save</Button
		>
		<Button
			type="button"
			variant="outline"
			disabled={linkPresent}
			aria-describedby={linkPresent ? linkHintId : undefined}
			onclick={insertLink}><Link class="size-4" />Insert Deploybot link</Button
		>
	</div>
	{#if linkPresent}
		<p id={linkHintId} class="text-muted-foreground text-sm">The file already has a managed_url line.</p>
	{/if}
</form>

<section class="mt-4" aria-labelledby="host-file-diff-heading">
	<h4 id="host-file-diff-heading" class="mb-2 text-sm font-medium">
		{applied == null ? 'Changes (nothing applied yet; every line is new)' : `Changes against the applied version (v${applied})`}
	</h4>
	{#if applied != null && appliedContent == null}
		<p class="text-muted-foreground text-sm">The applied version could not be read.</p>
	{:else}
		<FileDiff before={appliedContent ?? ''} after={diffText} label="Editor text against the applied version" />
	{/if}
</section>
