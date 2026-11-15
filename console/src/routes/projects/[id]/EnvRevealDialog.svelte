<script lang="ts">
	import { deserialize } from '$app/forms';
	import { tick, untrack } from 'svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import {
		REVEAL_CLOSED,
		REVEAL_ONCE_NOTE,
		revealAnswer,
		revealCanClose,
		revealClosed,
		revealFailedText,
		revealOpened,
		revealSettled,
		type RevealState
	} from '$lib/env-generate';
	import { buttonVariants } from '$lib/components/ui/button';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { envDomId } from './env-set-panel';

	let {
		setName,
		revealKey,
		action,
		onClose
	}: {
		setName: string;
		/** The key to show once, or null when the dialog is closed. */
		revealKey: string | null;
		/** revealEnvKey (api-v1 v1.27 item 3). */
		action: string;
		/** Called with the key when the dialog closes; the caller moves focus and reloads. */
		onClose: (key: string) => void;
	} = $props();

	const FETCH_TIMEOUT_MS = 45_000;

	// The value lives only here, and only while the dialog is open.
	let reveal = $state<RevealState>(REVEAL_CLOSED);
	// What the dialog shows: the open state, kept through the close animation and cleared when it ends.
	let frame = $state<RevealState>(REVEAL_CLOSED);
	let nextToken = 0;
	const open = $derived(reveal.status !== 'closed');
	const closable = $derived(revealCanClose(reveal));
	const inputId = $derived(envDomId(setName, 'reveal-value'));
	const liveText = $derived(frame.status === 'loading' ? 'Loading the value…' : frame.status === 'shown' ? `Value ready for ${frame.key}` : '');

	function setReveal(next: RevealState) {
		reveal = next;
		if (next.status !== 'closed') frame = next;
	}

	async function fetchValue(key: string, token: number) {
		const body = new FormData();
		body.set('set', setName);
		body.set('key', key);
		try {
			// The action result is read here and never applied to the page, so the value is not in `form` or page data.
			const response = await fetch(action, {
				method: 'POST',
				body,
				headers: { accept: 'application/json', 'x-sveltekit-action': 'true' },
				signal: AbortSignal.timeout(FETCH_TIMEOUT_MS)
			});
			const result = deserialize(await response.text());
			setReveal(revealSettled(reveal, key, token, revealAnswer(result, key)));
		} catch {
			setReveal(revealSettled(reveal, key, token, { ok: false, message: revealFailedText(key) }));
		}
	}

	$effect(() => {
		const key = revealKey;
		if (key == null) return;
		untrack(() => {
			nextToken += 1;
			const token = nextToken;
			setReveal(revealOpened(key, token));
			void fetchValue(key, token);
		});
	});

	// A shown value gets focus and is selected, ready to copy.
	$effect(() => {
		if (reveal.status !== 'shown') return;
		void tick().then(() => {
			const input = document.getElementById(inputId);
			if (input instanceof HTMLInputElement) {
				input.focus();
				input.select();
			}
		});
	});

	// Unmount (a reload that removes the set) drops the value too, even while loading.
	$effect(() => () => {
		reveal = REVEAL_CLOSED;
		frame = REVEAL_CLOSED;
	});

	function onOpenChange(next: boolean) {
		if (next || !revealCanClose(reveal)) return;
		const key = reveal.status === 'closed' ? null : reveal.key;
		reveal = revealClosed(reveal);
		if (key != null) onClose(key);
	}

	// The close animation has ended: the frame (and any value in it) goes too.
	function onOpenChangeComplete(next: boolean) {
		if (!next && reveal.status === 'closed') frame = REVEAL_CLOSED;
	}

	function onCloseAutoFocus(event: Event) {
		// The caller focuses the row once the dialog is gone. The frame goes now at the latest.
		event.preventDefault();
		if (reveal.status === 'closed') frame = REVEAL_CLOSED;
	}
</script>

<Dialog.Root {open} {onOpenChange} {onOpenChangeComplete}>
	<!-- Outside clicks never close it (this is the only copy); Escape and Close do, once the answer is in. -->
	<Dialog.Content
		class="sm:max-w-lg"
		showCloseButton={closable}
		interactOutsideBehavior="ignore"
		escapeKeydownBehavior={closable ? 'close' : 'ignore'}
		{onCloseAutoFocus}
	>
		{#if frame.status !== 'closed'}
			<Dialog.Header class="pr-8">
				<Dialog.Title class="break-all">Value of {frame.key}</Dialog.Title>
				<Dialog.Description>{REVEAL_ONCE_NOTE}</Dialog.Description>
			</Dialog.Header>
			<p class={frame.status === 'loading' ? 'text-muted-foreground text-sm' : 'sr-only'} role="status" aria-live="polite">{liveText}</p>
			{#if frame.status === 'shown'}
				<div class="grid gap-2">
					<Label for={inputId}>{frame.key}</Label>
					<div class="flex min-w-0 flex-wrap items-center gap-2">
						<Input id={inputId} readonly value={frame.value} class="min-w-0 flex-1 font-mono" autocomplete="off" spellcheck="false" />
						<CopyButton value={frame.value} label="Copy" />
					</div>
				</div>
			{:else if frame.status === 'failed'}
				<div class="max-w-full min-w-0 break-words">
					<Notice kind="error">{frame.message}</Notice>
				</div>
			{/if}
			<div class="flex flex-wrap items-center gap-2">
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })} disabled={!closable}>Close</Dialog.Close>
			</div>
		{/if}
	</Dialog.Content>
</Dialog.Root>
