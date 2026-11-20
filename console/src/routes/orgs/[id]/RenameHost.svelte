<script lang="ts">
	import { enhance } from '$app/forms';
	import Pencil from '@lucide/svelte/icons/pencil';
	import { onDestroy, tick, untrack } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import { formDialogOutcome, nextFormDialog, type DialogOutcome } from '$lib/dialog-open';
	import { HOST_NAME_HINT, HOST_NAME_MESSAGE, validHostName, type HostRename } from '$lib/host-name';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	// Rename a host (api-v1 v1.38), from the org Hosts table. Org owners and admins only, live hosts only.
	let {
		action,
		hostId,
		name,
		failed,
		message = null,
		onRenamed,
		onClosed
	}: {
		action: string;
		hostId: string;
		/** The host's current name. */
		name: string;
		/** This host's rename just failed: the dialog opens again with the message. */
		failed: boolean;
		message?: string | null;
		/** A rename succeeded; `dialogOpen` says whether this dialog was still open (the section holds the toast). */
		onRenamed: (rename: HostRename, dialogOpen: boolean) => void;
		/** The dialog finished closing, or this row unmounted. */
		onClosed: () => void;
	} = $props();

	let open = $state(untrack(() => failed));
	let sawFailure = $state(untrack(() => failed));
	let draft = $state(untrack(() => name));
	/** The rule check before posting; the server message wins once a post has failed. */
	let localMessage = $state<string | null>(null);
	/** Closing hides a server message until a new failure arrives, so reopening shows a clean dialog. */
	let serverMessageHidden = $state(false);
	let input = $state<HTMLInputElement | null>(null);
	const hintId = $props.id();
	const messageId = `${hintId}-message`;

	$effect(() => {
		const failedNow = failed;
		const serverName = name;
		const before = untrack(() => ({ open, sawFailure }));
		const next = nextFormDialog(before, failedNow ? 'failure' : 'idle');
		open = next.open;
		sawFailure = next.sawFailure;
		// A new failure shows its message again.
		if (failedNow && !before.sawFailure) serverMessageHidden = false;
		// A closed dialog follows the server name, including after a successful rename reloads it.
		if (!next.open) draft = serverName;
	});

	function settle(outcome: DialogOutcome) {
		const next = nextFormDialog({ open, sawFailure }, outcome);
		open = next.open;
		sawFailure = next.sawFailure;
	}

	function closeComplete(isOpen: boolean) {
		if (isOpen) return;
		// Closed without a success (Cancel, Escape) or after one: either way the next open starts clean.
		draft = name;
		localMessage = null;
		serverMessageHidden = true;
		onClosed();
	}

	onDestroy(() => onClosed());

	const shownMessage = $derived(localMessage ?? (failed && !serverMessageHidden ? message : null));
</script>

<Dialog.Root bind:open onOpenChangeComplete={closeComplete}>
	<RowAction label="Rename {name}" icon={Pencil}>
		{#snippet trigger(props)}
			<Dialog.Trigger {...props}>
				<Pencil class="size-4" />
			</Dialog.Trigger>
		{/snippet}
	</RowAction>
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>Rename {name}</Dialog.Title>
			<Dialog.Description>The agent keeps running and needs no change. Earlier audit entries keep the old name.</Dialog.Description>
		</Dialog.Header>
		<form
			method="POST"
			{action}
			class="grid gap-3"
			use:enhance={({ cancel }) => {
				const to = draft;
				if (!validHostName(to)) {
					localMessage = HOST_NAME_MESSAGE;
					cancel();
					void tick().then(() => input?.focus());
					return;
				}
				localMessage = null;
				const from = name;
				return async ({ result, update }) => {
					const outcome = formDialogOutcome(result.type);
					// Reported before the reload, which may unmount this row.
					if (outcome === 'success') onRenamed({ hostId, from, to }, open);
					await update();
					settle(outcome);
				};
			}}
		>
			{#if shownMessage}
				<div id={messageId}><Notice kind="error">{shownMessage}</Notice></div>
			{/if}
			<Input type="hidden" name="host_id" value={hostId} />
			<Label class="grid gap-1.5">
				<span>Name</span>
				<Input
					type="text"
					name="name"
					maxlength={63}
					autocomplete="off"
					autocapitalize="none"
					autocorrect="off"
					spellcheck={false}
					required
					aria-invalid={shownMessage ? 'true' : undefined}
					aria-describedby={shownMessage ? `${messageId} ${hintId}` : hintId}
					bind:ref={input}
					bind:value={draft}
				/>
			</Label>
			<p id={hintId} class="text-muted-foreground text-sm">{HOST_NAME_HINT}</p>
			<div class="flex flex-wrap items-center gap-2">
				<Button type="submit">Rename</Button>
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
