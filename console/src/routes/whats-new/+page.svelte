<script lang="ts">
	import PageHeader from '$lib/components/PageHeader.svelte';

	let { data } = $props();
</script>

<svelte:head>
	<title>What's new · Deploybot</title>
</svelte:head>

<PageHeader title="What's new" subtitle="Changes in each Deploybot release, newest first." />
<div class="flex flex-col gap-6">
	{#each data.entries as entry (entry.version)}
		<section id="v{entry.version}" class="scroll-mt-4" aria-labelledby="release-{entry.version}">
			<h2 id="release-{entry.version}" class="text-base font-semibold">
				v{entry.version} <span class="text-muted-foreground text-sm font-normal"><span aria-hidden="true">·</span> <time datetime={entry.date}>{entry.date}</time></span>
			</h2>
			<ul class="mt-2 list-disc space-y-1 pl-5">
				{#each entry.items as item, i (i)}
					<li class="break-words">
						{#each item as part, j (j)}{#if part.code}<code class="bg-muted rounded px-1 py-0.5 font-mono text-sm break-all">{part.text}</code>{:else}{part.text}{/if}{/each}
					</li>
				{/each}
			</ul>
		</section>
	{:else}
		<p class="text-muted-foreground">No release notes yet.</p>
	{/each}
</div>
