<script lang="ts">
	import { enhance } from '$app/forms';
	import Plus from '@lucide/svelte/icons/plus';
	import { tick } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { DomainPattern, OrgDomain, Project } from '$lib/api-types';
	import { ASSIGNMENTS_MAX, assignmentText } from '$lib/edge-routes';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	/**
	 * Which projects of the org may route under a domain (api-v1 v1.35 item 1): one row per project and pattern, `@` for
	 * the domain itself and `*` for one label below it. Save replaces the whole list. A refusal shows here; it is only
	 * the one from a save made since the dialog opened.
	 */
	let { domain, projects, action }: { domain: OrgDomain; projects: Project[]; action: string } = $props();

	type Row = { project_id: string; pattern: DomainPattern };

	const triggerId = $derived(`assign-${domain.id}`);
	let open = $state(false);
	let rows = $state<Row[]>([]);
	let message = $state<string | null>(null);
	let saving = $state(false);

	function saved(): Row[] {
		return domain.assignments.map((item) => ({ project_id: item.project_id, pattern: item.pattern }));
	}

	// Opening and closing both go back to the saved list and drop an old message.
	function onOpenChange(next: boolean) {
		rows = saved();
		message = null;
		if (!next) saving = false;
	}

	function projectLabel(id: string): string {
		return projects.find((project) => project.id === id)?.name ?? (domain.assignments.find((item) => item.project_id === id)?.project_name || 'Choose a project');
	}

	async function removeRow(index: number) {
		rows = rows.filter((_, item) => item !== index);
		await tick();
		const next = document.getElementById(`${triggerId}-project-${index}`) ?? document.getElementById(`${triggerId}-add`);
		next?.focus();
	}
</script>

<Dialog.Root bind:open {onOpenChange}>
	<Dialog.Trigger id={triggerId} class={buttonVariants({ variant: 'outline', size: 'sm' })} aria-label="Assign {domain.name}">Assign</Dialog.Trigger>
	<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-y-auto sm:max-w-xl">
		<Dialog.Header class="pr-8">
			<Dialog.Title class="break-all">Assign {domain.name}</Dialog.Title>
			<Dialog.Description>
				A project may add public addresses only under the names assigned to it. "{domain.name}" is the name itself; "*.{domain.name}" is any name one label below it.
			</Dialog.Description>
		</Dialog.Header>
		<form
			method="POST"
			{action}
			class="grid min-w-0 gap-3"
			use:enhance={({ cancel }) => {
				if (saving) {
					cancel();
					return;
				}
				saving = true;
				message = null;
				return async ({ result, update }) => {
					saving = false;
					if (result.type === 'failure') {
						const text = result.data?.message;
						message = typeof text === 'string' ? text : 'The assignments were not saved.';
						return;
					}
					open = false;
					await update();
				};
			}}
		>
			<Input type="hidden" name="domain_id" value={domain.id} />
			{#if message}
				<div class="max-w-full min-w-0 break-words" data-assignments-error><Notice kind="error">{message}</Notice></div>
			{/if}
			{#if rows.length === 0}
				<p class="text-muted-foreground text-sm">No project is assigned. Saving with no rows removes every assignment.</p>
			{/if}
			{#each rows as row, index (index)}
				<div class="grid min-w-0 gap-2 rounded-md border p-2 sm:grid-cols-[1fr_1fr_auto] sm:items-end">
					<div class="grid gap-1.5">
						<Label for="{triggerId}-project-{index}">Project</Label>
						<Select.Root type="single" name="project_id" bind:value={rows[index].project_id}>
							<Select.Trigger id="{triggerId}-project-{index}" class="w-full">{projectLabel(row.project_id)}</Select.Trigger>
							<Select.Content>
								{#each projects as project (project.id)}
									<Select.Item value={project.id} label={project.name}>{project.name}</Select.Item>
								{/each}
							</Select.Content>
						</Select.Root>
					</div>
					<div class="grid gap-1.5">
						<Label for="{triggerId}-pattern-{index}">Names</Label>
						<Select.Root type="single" name="pattern" bind:value={rows[index].pattern}>
							<Select.Trigger id="{triggerId}-pattern-{index}" class="w-full font-mono">{assignmentText(domain.name, row.pattern)}</Select.Trigger>
							<Select.Content>
								<Select.Item value="@" label={domain.name}>{domain.name}</Select.Item>
								<Select.Item value="*" label="*.{domain.name}">*.{domain.name}</Select.Item>
							</Select.Content>
						</Select.Root>
					</div>
					<Button type="button" variant="outline" aria-label="Remove row {index + 1}" onclick={() => removeRow(index)}>Remove</Button>
				</div>
			{/each}
			<div>
				<Button
					id="{triggerId}-add"
					type="button"
					variant="outline"
					disabled={rows.length >= ASSIGNMENTS_MAX || projects.length === 0}
					onclick={() => (rows = [...rows, { project_id: projects[0]?.id ?? '', pattern: '@' }])}
				>
					<Plus class="size-4" />Add project
				</Button>
			</div>
			<div class="flex flex-wrap gap-2">
				<Button type="submit" disabled={saving} aria-busy={saving}>{saving ? 'Saving…' : 'Save assignments'}</Button>
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
