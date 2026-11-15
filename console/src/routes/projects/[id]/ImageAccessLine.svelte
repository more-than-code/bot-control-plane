<script lang="ts">
	import Plug from '@lucide/svelte/icons/plug';
	import { Button } from '$lib/components/ui/button';
	import { accessLineText, type AccessLineState } from '$lib/settings-auto';

	/**
	 * The one line under the Image field (api-v1 v1.29 item 6). A polite status, so a new answer is read out, and the
	 * field's `aria-describedby` names `id`. Connect GitHub is a button when the API offers it and the person is an
	 * admin (`onConnect` opens GitHub in a new tab, so the unsaved edit stays); otherwise a link to Settings.
	 */
	let {
		id,
		state,
		settingsHref,
		canConnect = false,
		onConnect
	}: {
		id: string;
		state: AccessLineState;
		settingsHref: string;
		canConnect?: boolean;
		onConnect?: () => void;
	} = $props();

	const line = $derived(accessLineText(state));
</script>

<p {id} role="status" class="text-muted-foreground flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm break-words" data-image-access>
	<span class="min-w-0">{line.text}</span>
	{#if line.connect && canConnect && onConnect}
		<Button type="button" variant="outline" size="sm" onclick={onConnect}>
			<Plug class="size-4" />
			Connect GitHub<span class="sr-only">{' (opens in a new tab)'}</span>
		</Button>
	{:else if line.connect || line.settings}
		<!-- A new tab keeps the unsaved edit in this drawer. -->
		<a class="text-foreground underline underline-offset-3" href={settingsHref} rel="noopener noreferrer" target="_blank"
			>Settings → Registries<span class="sr-only">{' (opens in a new tab)'}</span></a
		>
	{/if}
</p>
