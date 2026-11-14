<script lang="ts">
	import { enhance } from '$app/forms';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import type { AccessLevel, HostPrograms } from '$lib/api-types';
	import {
		ACCESS_HELP,
		ACCESS_LEVEL_TEXT,
		grantableProjects,
		missingFromAllowlist,
		type AccessProject
	} from '$lib/host-access';
	import type { Column } from '$lib/table';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	type AccessRow = { project_id: string; project_name: string; level: AccessLevel; granted_at?: string | null };

	let {
		hostId,
		hostName,
		rows,
		projects,
		programs = null,
		setAction,
		removeAction,
		tableId = 'access',
		message = null,
		conflictProjectId = null
	}: {
		hostId: string;
		hostName: string;
		rows: AccessRow[];
		/** The org's projects: names for the grant form and slugs for the runner.toml note. */
		projects: AccessProject[];
		/** The host's reported policy: a deploy project missing from its allowlist gets a note. */
		programs?: Pick<HostPrograms, 'projects'> | null;
		setAction: string;
		removeAction: string;
		/** DataTable id (URL state namespace). */
		tableId?: string;
		/** This host's last access failure. */
		message?: string | null;
		/** A remove that came back 409: offer to move that project's services to the organization. */
		conflictProjectId?: string | null;
	} = $props();

	const showGranted = $derived(rows.some((row) => row.granted_at !== undefined));
	const columns = $derived<Column<AccessRow>[]>([
		{ key: 'project', label: 'Project', sortable: true, value: (row) => row.project_name },
		{ key: 'level', label: 'Level', sortable: true, value: (row) => row.level },
		...(showGranted ? [{ key: 'granted', label: 'Granted', value: (row: AccessRow) => row.granted_at ?? null }] : []),
		{ key: 'actions', label: 'Actions', srLabel: true }
	]);
	const slugOf = $derived(new Map(projects.map((project) => [project.id, project.slug])));
	const choices = $derived(grantableProjects(projects, rows));
	const conflict = $derived(conflictProjectId ? (rows.find((row) => row.project_id === conflictProjectId) ?? null) : null);

	let grantProject = $state('');
	let grantLevel = $state<AccessLevel>('deploy');
	const grantSlug = $derived(choices.find((project) => project.id === grantProject)?.slug ?? null);
	const conflictNoticeId = $derived(`access-conflict-${hostId}`);
	const headingId = $derived(`${tableId}-heading`);
</script>

<!-- The second gate (api-v1 v1.24, spec D-4): the host's root-owned runner.toml must list the project too. -->
{#snippet runnerNote(slug: string)}
	This host also needs <code>{slug}</code> in its runner.toml <code>projects</code>.
{/snippet}

<div class="grid gap-3">
	<p class="text-muted-foreground text-sm">{ACCESS_HELP}</p>
	{#if message && !conflict}
		<Notice kind="error">{message}</Notice>
	{/if}
	{#if conflict}
		<div id={conflictNoticeId} tabindex="-1" class="grid gap-2 rounded-md focus-visible:ring-3 focus-visible:ring-ring">
			<Notice kind="error">
				{conflict.project_name} still has services on {hostName}. Remove its access and move those services to the organization?
			</Notice>
			<div>
				<ConfirmAction
					id="reassign-access-{hostId}-{conflict.project_id}"
					action={removeAction}
					label="Remove and move services"
					confirmLabel="Remove and move services"
					message="Remove {conflict.project_name}'s access to {hostName}? Its services there become organization services and keep running."
					hidden={[
						{ name: 'host_id', value: hostId },
						{ name: 'project_id', value: conflict.project_id },
						{ name: 'reassign', value: '1' }
					]}
				/>
			</div>
		</div>
	{/if}
	<h3 id={headingId} class="sr-only">Project access to {hostName}</h3>
	<DataTable {rows} {columns} id={tableId} labelledBy={headingId} empty="No project has access to this host." rowKey={(row) => row.project_id}>
		{#snippet row(item)}
			{@const slug = slugOf.get(item.project_id) ?? null}
			<tr>
				<td class="p-2 align-middle">{item.project_name}</td>
				<td class="p-2 align-middle whitespace-normal">
					<form method="POST" action={setAction} class="flex flex-wrap items-center gap-2" use:enhance>
						<Input type="hidden" name="host_id" value={hostId} />
						<Input type="hidden" name="project_id" value={item.project_id} />
						<Label class="sr-only" for="access-level-{hostId}-{item.project_id}">Level for {item.project_name}</Label>
						<Select.Root type="single" name="level" value={item.level}>
							<Select.Trigger id="access-level-{hostId}-{item.project_id}" class="w-28">
								<Select.Value />
							</Select.Trigger>
							<Select.Content>
								<Select.Item value="metrics">metrics</Select.Item>
								<Select.Item value="deploy">deploy</Select.Item>
							</Select.Content>
						</Select.Root>
						<RowAction type="submit" label="Change level {item.project_name}" icon={KeyRound} />
					</form>
					{#if missingFromAllowlist(programs, item.level, slug)}
						<p class="text-muted-foreground mt-1 text-sm">{@render runnerNote(slug ?? '')}</p>
					{/if}
				</td>
				{#if showGranted}
					<td class="p-2 align-middle"><LocalTime iso={item.granted_at ?? null} /></td>
				{/if}
				<td class="p-2 align-middle">
					<ConfirmAction
						id="remove-access-{hostId}-{item.project_id}"
						action={removeAction}
						label="Remove access"
						confirmLabel="Remove access"
						message="Remove {item.project_name}'s access to {hostName}?"
						hidden={[
							{ name: 'host_id', value: hostId },
							{ name: 'project_id', value: item.project_id }
						]}
						rowLabel="Remove access {item.project_name}"
						closeOnFailure
						failureFocusId={conflictNoticeId}
					/>
				</td>
			</tr>
		{/snippet}
	</DataTable>
	{#if choices.length > 0}
		<form method="POST" action={setAction} class="grid gap-3 sm:max-w-md" use:enhance>
			<Input type="hidden" name="host_id" value={hostId} />
			<Label class="grid gap-1.5" for="grant-project-{hostId}">
				Project
				<Select.Root type="single" name="project_id" bind:value={grantProject}>
					<Select.Trigger id="grant-project-{hostId}" class="w-full">
						{choices.find((project) => project.id === grantProject)?.name ?? 'Choose a project'}
					</Select.Trigger>
					<Select.Content>
						{#each choices as project (project.id)}
							<Select.Item value={project.id} label={project.name}>{project.name}</Select.Item>
						{/each}
					</Select.Content>
				</Select.Root>
			</Label>
			<Label class="grid gap-1.5" for="grant-level-{hostId}">
				Level
				<Select.Root type="single" name="level" bind:value={grantLevel}>
					<Select.Trigger id="grant-level-{hostId}" class="w-full">
						<Select.Value />
					</Select.Trigger>
					<Select.Content>
						<Select.Item value="metrics">metrics</Select.Item>
						<Select.Item value="deploy">deploy</Select.Item>
					</Select.Content>
				</Select.Root>
			</Label>
			<p class="text-muted-foreground text-sm">{ACCESS_LEVEL_TEXT[grantLevel]}</p>
			{#if grantSlug && grantLevel === 'deploy'}
				<p class="text-sm">{@render runnerNote(grantSlug)}</p>
			{/if}
			<div>
				<Button type="submit" disabled={!choices.some((project) => project.id === grantProject)}>Grant access</Button>
			</div>
		</form>
	{:else if projects.length > 0}
		<p class="text-muted-foreground text-sm">Every project in the organization has access to this host.</p>
	{/if}
</div>
