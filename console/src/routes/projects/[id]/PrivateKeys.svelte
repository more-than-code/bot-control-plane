<script lang="ts">
	import { page } from '$app/state';
	import { actionUrlFrom } from '$lib/url';
	import Eye from '@lucide/svelte/icons/eye';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import { tick } from 'svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import { afterPrivateReveal, privateKeyNotes, privateKeysFolded, type PrivateKey } from '$lib/private-keys';
	import { envRowActions } from '$lib/env-generate';
	import EnvRegenerateDialog from './EnvRegenerateDialog.svelte';
	import EnvRevealDialog from './EnvRevealDialog.svelte';
	import FieldsPost from './FieldsPost.svelte';

	/**
	 * Secrets for this entry (api-v1 v1.33 item 6): the keys of its private set `@<entry>`, read-only, with their kind
	 * and what they still need. Never a value. It sits in the drawer's pinned readiness band, so it folds into a closed
	 * `<details>` once the form scrolls (`collapsed`) or when the list is long, and loads without a visible placeholder.
	 */
	let { entry, collapsed = false }: { entry: string; collapsed?: boolean } = $props();

	const action = $derived(actionUrlFrom(page.url, 'catalogPrivateKeys'));
	const revealAction = $derived(actionUrlFrom(page.url, 'revealPrivateKey'));
	const regenerateAction = $derived(actionUrlFrom(page.url, 'regeneratePrivateKey'));
	// Regenerate (api-v1 v1.52): a private key belongs to this entry only, so only this entry needs a redeploy.
	let regenKey = $state<string | null>(null);
	async function afterRegenerate(key: string, changed: boolean) {
		regenKey = null;
		if (changed) post?.send({ entry });
		await tick();
		document.getElementById(`${sectionId}-row-${key}`)?.focus();
	}
	const canAdmin = $derived(page.data.canAdmin === true);
	// Show once (api-v1 v1.33): the value lives only in the dialog while it is open, as for an env-set key.
	let revealKey = $state<string | null>(null);
	function showOnce(key: string) {
		revealKey = key;
	}
	async function afterReveal(key: string) {
		revealKey = null;
		// Shown (or refused): the row is an ordinary secret now; the reload confirms it.
		keys = afterPrivateReveal(keys, key);
		post?.send({ entry });
		await tick();
		document.getElementById(`${sectionId}-row-${key}`)?.focus();
	}
	const sectionId = $derived(`private-keys-${entry}`);
	let post = $state<FieldsPost | null>(null);
	let keys = $state<PrivateKey[]>([]);
	let message = $state<string | null>(null);
	let loading = $state(true);
	let detailsOpen = $state(false);
	const folded = $derived(privateKeysFolded(keys.length, collapsed));

	$effect(() => {
		if (collapsed) detailsOpen = false;
	});

	$effect(() => {
		if (post) {
			loading = true;
			post.send({ entry });
		}
	});

	function take(fields: Record<string, string>, data: Record<string, unknown>) {
		if (fields.entry !== entry) return;
		loading = false;
		keys = Array.isArray(data.privateKeys) ? (data.privateKeys as PrivateKey[]) : [];
		message = Array.isArray(data.privateKeys) ? null : typeof data.privateKeysMessage === 'string' ? data.privateKeysMessage : null;
	}
</script>

<FieldsPost bind:this={post} {action} onResult={take} />
{#if loading}
	<p class="sr-only" role="status">Loading secrets</p>
{:else if message}
	<p id={sectionId} class="text-muted-foreground mt-2 text-sm break-words" data-private-keys>Secrets for this entry could not be read: {message}</p>
{:else if keys.length > 0}
	{#if folded}
		<details id={sectionId} class="mt-2 min-w-0" data-private-keys bind:open={detailsOpen}>
			<summary class="text-muted-foreground hover:text-foreground w-fit cursor-pointer text-sm underline-offset-4 hover:underline">Secrets for this entry ({keys.length})</summary>
			<div class="mt-1">{@render list()}</div>
		</details>
	{:else}
		<section id={sectionId} class="mt-2 grid min-w-0 gap-1" aria-labelledby="{sectionId}-heading" data-private-keys>
			<h3 id="{sectionId}-heading" class="text-sm font-medium">Secrets for this entry</h3>
			{@render list()}
		</section>
	{/if}
{/if}

{#snippet list()}
	<ul class="grid min-w-0 gap-0.5 text-sm">
		{#each keys as row (row.key)}
			<li id="{sectionId}-row-{row.key}" tabindex="-1" class="flex min-w-0 flex-wrap items-center gap-x-2 break-words outline-none" data-private-key={row.key}>
				<span class="font-mono break-all">{row.key}</span>
				<span class="text-muted-foreground">{privateKeyNotes(row).join(', ')}</span>
				{#if row.reveal_pending && row.is_secret && canAdmin}
					<RowAction label="Reveal {row.key} once" icon={Eye} onclick={() => showOnce(row.key)} />
				{/if}
				{#if envRowActions(row).regenerate && canAdmin}
					<RowAction label="Regenerate {row.key}" icon={RotateCcw} onclick={() => (regenKey = row.key)} />
				{/if}
			</li>
		{/each}
	</ul>
{/snippet}

{#if canAdmin}
	<EnvRevealDialog setName="@{entry}" {revealKey} action={revealAction} onClose={afterReveal} />
	<EnvRegenerateDialog setName="@{entry}" {regenKey} usedBy={[entry]} action={regenerateAction} onClose={afterRegenerate} />
{/if}
