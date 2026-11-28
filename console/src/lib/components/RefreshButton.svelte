<script lang="ts">
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { invalidateAll } from '$app/navigation';
	import { Button } from '$lib/components/ui/button';
	import { singleRefresh } from '$lib/page-refresh';

	/** Re-reads the page's data in place; open forms and dialogs keep what was typed. */
	let pending = $state(false);
	const refresh = singleRefresh(() => invalidateAll(), (value) => (pending = value));
</script>

<Button variant="outline" onclick={refresh} disabled={pending} aria-busy={pending}>
	<RefreshCw class={pending ? 'size-4 animate-spin' : 'size-4'} />
	{pending ? 'Refreshing…' : 'Refresh'}
</Button>
