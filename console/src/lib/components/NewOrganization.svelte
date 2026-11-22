<script lang="ts">
	import { enhance } from '$app/forms';
	import Plus from '@lucide/svelte/icons/plus';
	import { untrack } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import { focusFallbackAfterClose, nextFormDialog, startSubmit, submitResultStep, type DialogOutcome } from '$lib/dialog-open';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	/** New organization (spec-new-org §2): any signed-in user may create one and becomes its owner. */
	let {
		action,
		failed = false,
		message = null,
		showTrigger = true,
		openRequest = 0,
		fallbackFocusId = null,
		ownResult = false
	}: {
		action: string;
		failed?: boolean;
		message?: string | null;
		/** False: no button of its own (the header's Org ▾ menu opens it). */
		showTrigger?: boolean;
		/** A new value (a counter) opens the dialog. */
		openRequest?: number;
		/** Where focus goes when the dialog closes and its own trigger is hidden. */
		fallbackFocusId?: string | null;
		/**
		 * True when a failure should stay in this dialog instead of becoming the page's `form` (the header posts to
		 * `/projects`, and the `/projects` page shares its `form` with New project). A success still follows the redirect.
		 */
		ownResult?: boolean;
	} = $props();

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
	// A second submit while the first is pending, or while a success redirect navigates, is ignored.
	let saving = $state(false);

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
		<Dialog.Trigger class={buttonVariants({ variant: 'outline' })}>
			<Plus class="size-4" />
			New organization
		</Dialog.Trigger>
	{/if}
	<Dialog.Content>
		<Dialog.Header>
			<Dialog.Title>New organization</Dialog.Title>
			<Dialog.Description id="org-name-help">You will be its owner. Add projects, members and hosts from its page.</Dialog.Description>
		</Dialog.Header>
		<form
			method="POST"
			{action}
			class="grid gap-3"
			use:enhance={({ cancel }) => {
				const start = startSubmit(saving);
				saving = start.saving;
				if (start.cancel) {
					cancel();
					return;
				}
				return async ({ result, update }) => {
					const data = result.type === 'failure' ? (result.data as { message?: unknown } | undefined) : undefined;
					const step = submitResultStep({
						type: result.type,
						ownResult,
						message: data?.message,
						fallback: 'The organization could not be created.'
					});
					saving = step.saving;
					ownMessage = step.ownMessage;
					if (!step.update) return;
					await update();
					if (step.settle) settle(step.settle);
				};
			}}
		>
			{#if shownMessage}
				<Notice kind="error">{shownMessage}</Notice>
			{/if}
			<Label class="grid gap-1.5" for="org-name">
				Name
				<Input id="org-name" name="name" required maxlength={80} autocomplete="off" aria-describedby="org-name-help" />
			</Label>
			<div class="flex flex-wrap items-center gap-2">
				<Button type="submit" disabled={saving}>Create</Button>
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
