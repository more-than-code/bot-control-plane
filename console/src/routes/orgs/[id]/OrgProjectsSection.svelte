<script lang="ts">
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { Project } from '$lib/api-types';
	import type { Column } from '$lib/table';

	let {
		projects,
		omitted,
		truncated,
		message
	}: {
		/** The org's projects the caller can reach: every one for an org admin, their own for a member. */
		projects: Project[];
		omitted: number;
		truncated: boolean;
		message: string | null;
	} = $props();

	const columns: Column<Project>[] = [
		{ key: 'name', label: 'Name', sortable: true, searchable: true, value: (project) => project.name },
		{ key: 'slug', label: 'Slug', sortable: true, searchable: true, value: (project) => project.slug },
		{ key: 'role', label: 'Your role', value: (project) => project.role },
		{ key: 'created', label: 'Created', sortable: true, value: (project) => project.created_at }
	];
</script>

{#if message}
	<Notice kind="error">{message}</Notice>
{/if}
{#if omitted > 0}
	<p class="text-muted-foreground mb-2 text-sm">{omitted} more {omitted === 1 ? 'row' : 'rows'} left out.</p>
{/if}
{#if truncated}
	<p class="text-muted-foreground mb-2 text-sm">More items not shown</p>
{/if}
<h2 id="org-projects-heading" class="sr-only">Projects</h2>
<DataTable rows={projects} {columns} id="projects" labelledBy="org-projects-heading" empty="No projects yet." rowKey={(project) => project.id}>
	{#snippet row(project)}
		<tr>
			<td class="p-2 align-middle"><a class="text-primary underline-offset-4 hover:underline" href="/projects/{project.id}">{project.name}</a></td>
			<td class="p-2 align-middle">{project.slug}</td>
			<td class="p-2 align-middle">{project.role}{project.via === 'org' ? ' (org)' : ''}</td>
			<td class="p-2 align-middle"><LocalTime iso={project.created_at} /></td>
		</tr>
	{/snippet}
</DataTable>
