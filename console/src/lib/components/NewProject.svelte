<script lang="ts">
	import { enhance } from '$app/forms';
	import Plus from '@lucide/svelte/icons/plus';
	import { untrack } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import { focusFallbackAfterClose, formDialogOutcome, nextFormDialog, type DialogOutcome } from '$lib/dialog-open';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';

	let {
		action,
		failed,
		message = null,
		orgs = [],
		showTrigger = true,
		openRequest = 0,
		fallbackFocusId = null,
		ownResult = false
	}: {
		action: string;
		failed: boolean;
		message?: string | null;
		/**
		 * Organizations the caller may create a project in (owner or admin). One is posted as is; several
		 * get a choice. None posts no org, and the API picks the caller's only org or asks for one.
		 */
		orgs?: Array<{ id: string; name: string }>;
		/** False: no button of its own (the header's Project ▾ menu opens it). */
		showTrigger?: boolean;
		/** A new value (a counter) opens the dialog. */
		openRequest?: number;
		/** Where focus goes when the dialog closes and its own trigger is hidden. */
		fallbackFocusId?: string | null;
		/**
		 * True when the action is on another route (the header posts to `/projects`): a failure stays in this dialog
		 * instead of becoming the current page's `form`. A success still follows the redirect to the new project.
		 */
		ownResult?: boolean;
	} = $props();

	let orgId = $state(untrack(() => (orgs.length === 1 ? orgs[0].id : '')));
	const orgName = $derived(orgs.find((org) => org.id === orgId)?.name ?? null);

	let open = $state(untrack(() => failed));
	let sawFailure = $state(untrack(() => failed));

	$effect(() => {
		const next = nextFormDialog(untrack(() => ({ open, sawFailure })), failed ? 'failure' : 'idle');
		open = next.open;
		sawFailure = next.sawFailure;
	});

	// The failure message of an `ownResult` post.
	let ownMessage = $state<string | null>(null);
	const shownMessage = $derived(ownResult ? ownMessage : failed ? message : null);

	// The header menu opens this by changing `openRequest`; closing returns focus to that menu's trigger.
	let requestSeen = untrack(() => openRequest);
	$effect(() => {
		const request = openRequest;
		if (request === requestSeen) return;
		requestSeen = request;
		untrack(() => {
			ownMessage = null;
			open = true;
		});
	});
	let openBefore = untrack(() => open);
	$effect(() => {
		const now = open;
		if (openBefore && !now) focusFallbackAfterClose(untrack(() => fallbackFocusId));
		openBefore = now;
	});

	function settle(outcome: DialogOutcome) {
		const next = nextFormDialog({ open, sawFailure }, outcome);
		open = next.open;
		sawFailure = next.sawFailure;
	}
</script>

<Dialog.Root bind:open>
	{#if showTrigger}
		<Dialog.Trigger class={buttonVariants()}>
			<Plus class="size-4" />
			New project
		</Dialog.Trigger>
	{/if}
	<Dialog.Content>
		<Dialog.Header>
			<Dialog.Title>New project</Dialog.Title>
			<Dialog.Description>You become its owner.{orgs.length === 1 && orgName ? ` It belongs to ${orgName}.` : ''}</Dialog.Description>
		</Dialog.Header>
		<form
			method="POST"
			{action}
			class="grid gap-3"
			use:enhance={() => {
				return async ({ result, update }) => {
					if (ownResult && result.type === 'failure') {
						const data = result.data as { message?: unknown } | undefined;
						ownMessage = typeof data?.message === 'string' ? data.message : 'The project could not be created.';
						return;
					}
					ownMessage = null;
					await update();
					settle(formDialogOutcome(result.type));
				};
			}}
		>
			{#if shownMessage}
				<Notice kind="error">{shownMessage}</Notice>
			{/if}
			{#if orgs.length === 1}
				<Input type="hidden" name="org_id" value={orgId} />
			{:else if orgs.length > 1}
				<Label class="grid gap-1.5" for="project-org">
					Organization
					<Select.Root type="single" name="org_id" bind:value={orgId} required>
						<Select.Trigger id="project-org" class="w-full">{orgName ?? 'Choose an organization'}</Select.Trigger>
						<Select.Content>
							{#each orgs as org (org.id)}
								<Select.Item value={org.id} label={org.name}>{org.name}</Select.Item>
							{/each}
						</Select.Content>
					</Select.Root>
				</Label>
			{/if}
			<Label class="grid gap-1.5" for="project-name">
				Project name
				<Input id="project-name" name="name" required maxlength={80} autocomplete="off" />
			</Label>
			<div class="flex flex-wrap items-center gap-2">
				<Button type="submit">Create project</Button>
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
