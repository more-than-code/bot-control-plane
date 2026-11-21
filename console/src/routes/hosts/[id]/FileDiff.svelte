<script lang="ts">
	import { diffChanged, diffHunks, lineDiff } from '$lib/host-files';

	// A line diff with -, + and space prefixes; long unchanged runs fold into one "… N unchanged lines" row.
	let { before, after, label }: { before: string; after: string; label: string } = $props();

	const lines = $derived(lineDiff(before, after));
	const items = $derived(diffHunks(lines));
	const changed = $derived(diffChanged(lines));
</script>

{#if !changed}
	<p class="text-muted-foreground text-sm">No difference.</p>
{:else}
	<pre
		class="bg-muted max-h-96 overflow-auto rounded-lg border p-2 font-mono text-xs leading-5"
		aria-label={label}
		data-file-diff>{#each items as item, index (index)}{#if item.op === 'skip'}<span class="text-muted-foreground block italic">… {item.count} unchanged {item.count === 1 ? 'line' : 'lines'}</span>{:else}<span
				class="block whitespace-pre {item.op === '+' ? 'text-status-up' : item.op === '-' ? 'text-destructive' : 'text-muted-foreground'}"
				data-op={item.op === ' ' ? 'same' : item.op === '+' ? 'add' : 'remove'}>{item.op} {item.text}</span>{/if}{/each}</pre>
{/if}
