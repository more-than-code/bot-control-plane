<script lang="ts">
	import { page } from '$app/state';
	import DataTable from '$lib/components/DataTable.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import PageHeader from '$lib/components/PageHeader.svelte';
	import type { Project } from '$lib/api-types';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import NewProject from '$lib/components/NewProject.svelte';

	let { data, form } = $props();

	const columns: Column<Project>[] = [
		{ key: 'name', label: 'Name' },
		{ key: 'slug', label: 'Slug' },
		{ key: 'org', label: 'Organization' },
		{ key: 'role', label: 'Role' },
		{ key: 'created', label: 'Created' }
	];
</script>

<svelte:head>
	<title>Projects · Deploybot</title>
</svelte:head>

<PageHeader title="Projects" headingId="projects-heading">
	{#snippet actions()}
		<NewProject action={actionUrlFrom(page.url, 'create')} failed={Boolean(form?.message)} message={form?.message ?? null} orgs={data.orgs} />
	{/snippet}
</PageHeader>
{#if data.message}
	<Notice kind="error">{data.message}</Notice>
{/if}


{#if data.omitted > 0}
	<p class="text-muted-foreground mb-2 text-sm">{data.omitted} more {data.omitted === 1 ? 'row' : 'rows'} left out.</p>
{/if}
{#if data.truncated}
	<p class="text-muted-foreground mb-2 text-sm">More items not shown</p>
{/if}
<DataTable
	rows={data.projects}
	{columns}
	id="projects"
	labelledBy="projects-heading"
	empty="No projects yet."
	rowKey={(project) => project.id}
>
	{#snippet row(project)}
		<tr>
			<td class="p-2 align-middle"><a class="text-primary underline-offset-4 hover:underline" href="/projects/{project.id}">{project.name}</a></td>
			<td class="p-2 align-middle">{project.slug}</td>
			<td class="p-2 align-middle">
				{#if project.org_id && data.orgNames[project.org_id]}
					<a class="text-primary underline-offset-4 hover:underline" href="/orgs/{project.org_id}">{data.orgNames[project.org_id]}</a>
				{:else}
					n/a
				{/if}
			</td>
			<td class="p-2 align-middle">{project.role}{project.via === 'org' ? ' (org)' : ''}</td>
			<td class="p-2 align-middle"><LocalTime iso={project.created_at} /></td>
		</tr>
	{/snippet}
</DataTable>
