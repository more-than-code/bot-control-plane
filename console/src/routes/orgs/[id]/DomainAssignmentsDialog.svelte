<script lang="ts">
	import { enhance } from '$app/forms';
	import { replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import Plus from '@lucide/svelte/icons/plus';
	import { onMount, tick } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { DomainPattern, OrgDomain, Project } from '$lib/api-types';
	import { ASSIGNMENTS_MAX, assignLinkRow, assignmentText, cleanDomainPattern, withoutAssignLink } from '$lib/edge-routes';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	/**
	 * Which projects of the org may route under a domain (api-v1 v1.35 item 1): one row per project and pattern, `@` for
	 * the domain itself, `*` for one label below it, and since v1.52 a name below it (`app`, `api.dev`) or a narrower
	 * wildcard (`*.dev`). Save replaces the whole list. A refusal shows here; it is only the one from a save made since
	 * the dialog opened. An Assign link (`domains.assign`, `domains.project`, `domains.pattern`) opens it once with
	 * that row added, for an org admin to check and save.
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

	onMount(() => {
		const linked = assignLinkRow(page.url.searchParams, domain, projects.map((project) => project.id));
		if (!linked && page.url.searchParams.get('domains.assign') !== domain.id) return;
		// A macrotask, so the router has started when this runs during hydration.
		const timer = setTimeout(() => replaceState(withoutAssignLink(new URL(location.href)), page.state));
		if (!linked) return () => clearTimeout(timer);
		const current = saved();
		const present = current.some((row) => row.project_id === linked.project_id && row.pattern === linked.pattern);
		rows = present ? current : [...current, linked];
		message = null;
		open = true;
		return () => clearTimeout(timer);
	});

	/** The full name a typed pattern assigns, or null while it is not a pattern yet. */
	function preview(pattern: string): string | null {
		const clean = cleanDomainPattern(domain.name, pattern);
		return clean === null ? null : assignmentText(domain.name, clean);
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
				A project may add public addresses only under the names assigned to it. Names are relative to {domain.name}: @ is the name itself, * is any name one label below it, a name such as app assigns only app.{domain.name}, and *.dev is any name one label below dev.{domain.name}.
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
			<Input type="hidden" name="domain_name" value={domain.name} />
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
						<Input
							id="{triggerId}-pattern-{index}"
							name="pattern"
							class="font-mono"
							autocomplete="off"
							autocapitalize="none"
							spellcheck={false}
							maxlength={253}
							aria-describedby="{triggerId}-pattern-{index}-hint"
							bind:value={rows[index].pattern}
						/>
					</div>
					<Button type="button" variant="outline" aria-label="Remove row {index + 1}" onclick={() => removeRow(index)}>Remove</Button>
					<p id="{triggerId}-pattern-{index}-hint" class="text-muted-foreground min-w-0 text-xs break-all sm:col-span-3">
						{#if preview(row.pattern)}Assigns <span class="font-mono">{preview(row.pattern)}</span>{:else}Enter @, *, a name such as app, or *.dev{/if}
					</p>
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
