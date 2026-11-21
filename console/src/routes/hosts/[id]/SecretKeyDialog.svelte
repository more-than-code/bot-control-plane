<script lang="ts">
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Plus from '@lucide/svelte/icons/plus';
	import { onMount } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import { secretKeyProblem, secretValueProblem } from '$lib/host-files';
	import { PASSWORD_MANAGER_IGNORE, secretInput, supportsTextMask } from '$lib/secret-input';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	/**
	 * Stage one key's value of a secret host file, or a new key (`key` null). Nothing is posted here: the value
	 * goes to the page's staged changes (memory only) and is sent by Save. The masked input has no bound value,
	 * so the value never appears in the markup, and it is gone once the dialog closes.
	 */
	let {
		key = null,
		onStage
	}: {
		/** The key to set; null adds a key. */
		key?: string | null;
		onStage: (key: string, value: string) => void;
	} = $props();

	let open = $state(false);
	let message = $state<string | null>(null);
	let canMask = $state(false);
	const valueInput = $derived(secretInput(true, canMask));
	const ids = $props.id();

	onMount(() => {
		canMask = supportsTextMask();
	});

	function submit(event: SubmitEvent) {
		event.preventDefault();
		const data = new FormData(event.currentTarget as HTMLFormElement);
		const name = key ?? String(data.get('key') ?? '').trim();
		const value = String(data.get('value') ?? '');
		const problem = secretKeyProblem(name) ?? secretValueProblem(value);
		if (problem) {
			message = problem;
			return;
		}
		message = null;
		onStage(name, value);
		open = false;
	}

	const title = $derived(key ? `Set value for ${key}` : 'Add key');
</script>

<Dialog.Root bind:open onOpenChangeComplete={(isOpen) => (isOpen ? null : (message = null))}>
	{#if key}
		<RowAction label="Set value {key}" icon={KeyRound}>
			{#snippet trigger(props)}
				<Dialog.Trigger {...props}>
					<KeyRound class="size-4" />
				</Dialog.Trigger>
			{/snippet}
		</RowAction>
	{:else}
		<Dialog.Trigger class={buttonVariants({ variant: 'outline' })}><Plus class="size-4" />Add key</Dialog.Trigger>
	{/if}
	<Dialog.Content class="sm:max-w-md">
		<Dialog.Header>
			<Dialog.Title>{title}</Dialog.Title>
			<Dialog.Description>The value is staged until you Save; it is stored encrypted and never shown again.</Dialog.Description>
		</Dialog.Header>
		<form class="grid gap-3" autocomplete="off" onsubmit={submit}>
			{#if message}
				<Notice kind="error">{message}</Notice>
			{/if}
			{#if !key}
				<Label class="grid gap-1.5">
					<span>Key</span>
					<Input
						type="text"
						name="key"
						maxlength={64}
						autocomplete="off"
						autocapitalize="characters"
						spellcheck={false}
						required
						class="font-mono"
						aria-describedby="{ids}-key-hint"
					/>
				</Label>
				<p id="{ids}-key-hint" class="text-muted-foreground text-sm">Upper-case letters, digits and underscores, for example API_TOKEN.</p>
			{/if}
			<Label class="grid gap-1.5">
				<span>Value</span>
				<Input
					name="value"
					type={valueInput.type}
					autocomplete={valueInput.autocomplete}
					spellcheck={false}
					required
					class="font-mono {valueInput.maskClass}"
					{...PASSWORD_MANAGER_IGNORE}
				/>
			</Label>
			<div class="flex flex-wrap items-center gap-2">
				<Button type="submit">Stage</Button>
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</form>
	</Dialog.Content>
</Dialog.Root>
