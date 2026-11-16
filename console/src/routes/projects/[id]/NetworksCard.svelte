<script lang="ts">
	import DataTable from '$lib/components/DataTable.svelte';
	import * as Card from '$lib/components/ui/card';
	import type { CatalogEntry, Host } from '$lib/api-types';
	import { imageHostBlockReason, projectNetworkRows, type ProjectNetworkRow } from '$lib/catalog';
	import { networkSnippetHosts } from '$lib/settings-auto';
	import type { Column } from '$lib/table';
	import NetworkSnippetDialog from './NetworkSnippetDialog.svelte';

	/**
	 * Settings → Networks (spec-agent-usability-1 H2, H3): the shared networks open to this project on its hosts and
	 * which of its entries join each. Read-only, from the loaded hosts and catalog. With none open, an empty state and
	 * the dialog with the runner.toml lines to ask the host's root for one.
	 */
	let {
		hosts,
		hostsIncomplete = false,
		projectSlug,
		catalog,
		snippetAction
	}: {
		hosts: Host[];
		/** The host list was truncated or could not be read: a host left out may open more networks. */
		hostsIncomplete?: boolean;
		projectSlug: string;
		catalog: CatalogEntry[];
		/** `?/networkSnippet`. */
		snippetAction: string;
	} = $props();

	const rows = $derived(projectNetworkRows(hosts, projectSlug, catalog));
	// The snippet is for a host this project deploys containers to.
	// Only hosts this project deploys to (`access_level` `deploy`): the API answers not_found for any other.
	const snippetHosts = $derived(networkSnippetHosts(hosts, (host) => imageHostBlockReason(host) == null));

	const columns: Column<ProjectNetworkRow>[] = [
		{ key: 'host', label: 'Host', value: (row) => row.host },
		{ key: 'name', label: 'Network', value: (row) => row.name },
		{ key: 'subnet', label: 'Subnet', value: (row) => row.subnet },
		{ key: 'entries', label: 'Entries', value: (row) => row.entries.join(', ') }
	];
</script>

<Card.Root id="networks" class="mt-4">
	<Card.Header class="max-sm:has-data-[slot=card-action]:grid-cols-1">
		<Card.Title id="networks-heading">Networks</Card.Title>
		<Card.Description>Shared networks the hosts' root opened to this project. Entries on the same network reach each other by container name.</Card.Description>
		{#if snippetHosts.length > 0}
			<Card.Action class="max-sm:col-start-1 max-sm:row-span-1 max-sm:row-start-3 max-sm:justify-self-start">
				<NetworkSnippetDialog action={snippetAction} hosts={snippetHosts} />
			</Card.Action>
		{/if}
	</Card.Header>
	<Card.Content class="grid gap-3">
		{#if rows.length === 0 && hostsIncomplete}
			<p class="text-muted-foreground text-sm" data-networks-unknown>Hosts could not be read, so networks are unknown.</p>
		{:else if rows.length === 0}
			<p class="text-muted-foreground text-sm" data-networks-empty>No shared network is open to this project. Ask the host's root to add one.</p>
		{:else}
			<DataTable {rows} {columns} id="networks" labelledBy="networks-heading" empty="No shared networks." rowKey={(row) => `${row.host}-${row.name}`}>
				{#snippet row(item)}
					<tr>
						<td class="p-2 align-middle break-words">{item.host}</td>
						<td class="p-2 align-middle font-mono break-all">{item.name}</td>
						<td class="p-2 align-middle font-mono">{item.subnet}</td>
						<td class="p-2 align-middle break-words">
							{#if item.entries.length > 0}{item.entries.join(', ')}{:else}<span class="text-muted-foreground">None</span>{/if}
						</td>
					</tr>
				{/snippet}
			</DataTable>
		{/if}
		{#if hostsIncomplete && rows.length > 0}
			<p class="text-muted-foreground text-sm">Not every host could be read, so another host may open more networks.</p>
		{/if}
	</Card.Content>
</Card.Root>
