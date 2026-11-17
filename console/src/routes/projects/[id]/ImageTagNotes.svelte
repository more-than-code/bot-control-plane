<script lang="ts">
	import { TAGS_CUT_NOTE, imageNoteIds } from '$lib/image-tags';

	let {
		fieldId,
		message = null,
		authFailed = false,
		settingsHref,
		pinLine = null,
		cut = false
	}: {
		fieldId: string;
		/** Why no tags are listed. Never blocks saving. */
		message?: string | null;
		/** Adds the link to Settings → Registries. */
		authFailed?: boolean;
		settingsHref: string;
		/** "Pinned to sha256:…" or the pin failure line. */
		pinLine?: string | null;
		/** The API cut the tag list (`truncated`): adds the cut note. */
		cut?: boolean;
	} = $props();

	const ids = $derived(imageNoteIds(fieldId));
</script>

{#if message}
	<p id={ids.tags} class="text-muted-foreground text-sm break-words">
		{message}
		{#if authFailed}
			<!-- A new tab keeps the unsaved edit in this dialog. -->
			<a class="text-foreground underline underline-offset-3" href={settingsHref} rel="noopener noreferrer" target="_blank">Settings → Registries<span class="sr-only">{' (opens in a new tab)'}</span></a>
		{/if}
	</p>
{/if}
{#if cut}
	<p class="text-muted-foreground text-sm break-words" data-tags-cut>{TAGS_CUT_NOTE}</p>
{/if}
{#if pinLine}
	<p id={ids.pin} class="text-muted-foreground text-sm break-all">{pinLine}</p>
{/if}
