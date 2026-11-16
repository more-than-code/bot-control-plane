<script lang="ts">
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import { OPEN_REQUEST_TEXT, openRequestHref, openRequestTypeLabel, type OpenRequest } from '$lib/open-requests';
	import type { Column } from '$lib/table';

	let { data } = $props();

	const columns: Column<OpenRequest>[] = [
		{ key: 'type', label: 'Type', value: (row) => openRequestTypeLabel(row.type) },
		{ key: 'project', label: 'Project', value: (row) => row.project_name },
		{ key: 'subject', label: 'Subject', value: (row) => row.subject },
		{ key: 'expires', label: 'Expires', value: (row) => row.expires_at }
	];
</script>

<svelte:head>
	<title>Open requests · Deploybot</title>
</svelte:head>

<PageHeader title="Open requests" subtitle="Values, files and GitHub connections an AI agent asked you for. Each link works for 15 minutes." />
{#if data.message}
	<Notice kind="error">{data.message}</Notice>
{/if}
{#if data.requests.length === 0 && !data.message}
	<Notice kind="info">{OPEN_REQUEST_TEXT.empty}</Notice>
{:else if data.requests.length > 0}
	<h2 id="requests-heading" class="sr-only">Open requests</h2>
	<DataTable rows={data.requests} {columns} id="requests" labelledBy="requests-heading" empty="No open requests." rowKey={(row) => `${row.type}-${row.handle}`}>
		{#snippet row(request)}
			<tr>
				<td class="p-2 align-middle">
					<a class="text-primary underline-offset-4 hover:underline" href={openRequestHref(request)}
						>{openRequestTypeLabel(request.type)}<span class="sr-only">{` ${request.subject} in ${request.project_name}`}</span></a
					>
				</td>
				<td class="p-2 align-middle break-words">{request.project_name}</td>
				<td class="p-2 align-middle font-mono break-all">{request.subject}</td>
				<td class="p-2 align-middle"><LocalTime iso={request.expires_at} /></td>
			</tr>
		{/snippet}
	</DataTable>
{/if}
