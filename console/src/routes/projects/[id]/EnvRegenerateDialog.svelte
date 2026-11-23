<script lang="ts">
	import { deserialize } from '$app/forms';
	import { tick, untrack } from 'svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import {
		GENERATE_KINDS,
		GENERATE_LABELS,
		REVEAL_ONCE_NOTE,
		isGenerateKind,
		redeployNote,
		regenerateAnswer,
		regenerateFailedText,
		regenerateMessage,
		type GenerateKind
	} from '$lib/env-generate';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import { envDomId } from './env-set-panel';

	/**
	 * Regenerate a secret key (api-v1 v1.54): pick a kind, confirm, then the new value is shown once here. The value
	 * lives only in this dialog's state while it is open, as in EnvRevealDialog. The fetch is the action's only reader.
	 */
	let {
		setName,
		regenKey,
		usedBy,
		action,
		onClose
	}: {
		setName: string;
		/** The key to regenerate, or null when the dialog is closed. */
		regenKey: string | null;
		/** The set's used_by, or undefined when unknown. */
		usedBy: readonly string[] | undefined;
		/** regenerateEnvKey or regeneratePrivateKey. */
		action: string;
		/** Called with the key and whether a new value may exist; the caller moves focus and reloads. */
		onClose: (key: string, changed: boolean) => void;
	} = $props();

	const FETCH_TIMEOUT_MS = 45_000;

	type Phase =
		| { status: 'closed' }
		| { status: 'confirm'; key: string }
		| { status: 'loading'; key: string; token: number }
		| { status: 'shown'; key: string; value: string; usedBy: string[] }
		| { status: 'failed'; key: string; message: string };

	const CLOSED: Phase = { status: 'closed' };
	let phase = $state<Phase>(CLOSED);
	// What the dialog shows, kept through the close animation and cleared when it ends.
	let frame = $state<Phase>(CLOSED);
	let kind = $state<GenerateKind>('password');
	let nextToken = 0;
	const open = $derived(phase.status !== 'closed');
	const closable = $derived(phase.status !== 'loading');
	const id = (part: string) => envDomId(setName, `regen-${part}`);

	function setPhase(next: Phase) {
		phase = next;
		if (next.status !== 'closed') frame = next;
	}

	$effect(() => {
		const key = regenKey;
		if (key == null) return;
		untrack(() => {
			kind = 'password';
			setPhase({ status: 'confirm', key });
		});
	});

	async function regenerate() {
		if (phase.status !== 'confirm') return;
		const key = phase.key;
		nextToken += 1;
		const token = nextToken;
		setPhase({ status: 'loading', key, token });
		const body = new FormData();
		body.set('set', setName);
		body.set('key', key);
		body.set('generate_kind', kind);
		let next: Phase;
		try {
			// The action result is read here and never applied to the page, so the value is not in `form` or page data.
			const response = await fetch(action, {
				method: 'POST',
				body,
				headers: { accept: 'application/json', 'x-sveltekit-action': 'true' },
				signal: AbortSignal.timeout(FETCH_TIMEOUT_MS)
			});
			const answer = regenerateAnswer(deserialize(await response.text()), key);
			next = answer.ok ? { status: 'shown', key, value: answer.value, usedBy: answer.usedBy } : { status: 'failed', key, message: answer.message };
		} catch {
			next = { status: 'failed', key, message: regenerateFailedText(key) };
		}
		// An answer for an older request, or one after the dialog closed, is dropped.
		const current = phase as Phase;
		if (current.status === 'loading' && current.token === token) setPhase(next);
	}

	// A shown value gets focus and is selected, ready to copy.
	$effect(() => {
		if (phase.status !== 'shown') return;
		void tick().then(() => {
			const input = document.getElementById(id('value'));
			if (input instanceof HTMLInputElement) {
				input.focus();
				input.select();
			}
		});
	});

	// Unmount drops the value too.
	$effect(() => () => {
		phase = CLOSED;
		frame = CLOSED;
	});

	function onOpenChange(next: boolean) {
		if (next || !closable || phase.status === 'closed') return;
		const key = phase.key;
		const changed = phase.status !== 'confirm';
		phase = CLOSED;
		onClose(key, changed);
	}

	function onOpenChangeComplete(next: boolean) {
		if (!next && phase.status === 'closed') frame = CLOSED;
	}

	function onCloseAutoFocus(event: Event) {
		// The caller focuses the row once the dialog is gone.
		event.preventDefault();
		if (phase.status === 'closed') frame = CLOSED;
	}
</script>

<Dialog.Root {open} {onOpenChange} {onOpenChangeComplete}>
	<Dialog.Content
		class="sm:max-w-lg"
		showCloseButton={closable}
		interactOutsideBehavior="ignore"
		escapeKeydownBehavior={closable ? 'close' : 'ignore'}
		{onCloseAutoFocus}
	>
		{#if frame.status !== 'closed'}
			<Dialog.Header class="pr-8">
				<Dialog.Title class="break-all">Regenerate {frame.key}</Dialog.Title>
				<Dialog.Description class="break-words">
					{frame.status === 'shown' ? REVEAL_ONCE_NOTE : regenerateMessage(frame.key, usedBy)}
				</Dialog.Description>
			</Dialog.Header>
			{#if frame.status === 'confirm' || frame.status === 'loading'}
				<div class="grid gap-1.5">
					<Label for={id('kind')}>Kind</Label>
					<Select.Root
						type="single"
						value={kind}
						disabled={frame.status === 'loading'}
						onValueChange={(next) => {
							if (isGenerateKind(next)) kind = next;
						}}
					>
						<Select.Trigger id={id('kind')} class="w-full sm:w-64">
							<Select.Value>{GENERATE_LABELS[kind]}</Select.Value>
						</Select.Trigger>
						<Select.Content>
							{#each GENERATE_KINDS as item (item)}
								<Select.Item value={item} label={GENERATE_LABELS[item]}>{GENERATE_LABELS[item]}</Select.Item>
							{/each}
						</Select.Content>
					</Select.Root>
				</div>
				<p class={frame.status === 'loading' ? 'text-muted-foreground text-sm' : 'sr-only'} role="status" aria-live="polite">
					{frame.status === 'loading' ? 'Making a new value…' : ''}
				</p>
			{:else if frame.status === 'shown'}
				<div class="grid gap-2">
					<Label for={id('value')}>{frame.key}</Label>
					<div class="flex min-w-0 flex-wrap items-center gap-2">
						<Input id={id('value')} readonly value={frame.value} class="min-w-0 flex-1 font-mono" autocomplete="off" spellcheck="false" />
						<CopyButton value={frame.value} label="Copy" />
					</div>
					{#if redeployNote(frame.usedBy)}
						<p class="text-sm break-words" role="status">{redeployNote(frame.usedBy)}</p>
					{/if}
				</div>
			{:else if frame.status === 'failed'}
				<div class="max-w-full min-w-0 break-words">
					<Notice kind="error">{frame.message}</Notice>
				</div>
			{/if}
			<div class="flex flex-wrap items-center gap-2">
				{#if frame.status === 'confirm' || frame.status === 'loading'}
					<Button type="button" variant="destructive" disabled={frame.status === 'loading'} onclick={regenerate}>Regenerate</Button>
					<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })} disabled={!closable}>Cancel</Dialog.Close>
				{:else}
					<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Close</Dialog.Close>
				{/if}
			</div>
		{/if}
	</Dialog.Content>
</Dialog.Root>
