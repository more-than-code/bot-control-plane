<script lang="ts">
	import type { EnvSetKey } from '$lib/api-types';
	import Notice from '$lib/components/Notice.svelte';
	import { Input } from '$lib/components/ui/input';
	import { editValueField } from './env-set-panel';
	import { PASSWORD_MANAGER_IGNORE, secretInput, supportsTextMask } from '$lib/secret-input';

	// A masked secret is a CSS-masked text input where the browser can, so no "save password?" prompt ($lib/secret-input).
	let canMask = $state(false);
	$effect(() => {
		canMask = supportsTextMask();
	});
	const secretField = $derived(secretInput(true, canMask));

	// The inline value input of a row in edit state. It belongs to the row's form in the row's edit cell.
	// Escape is handled by the row, so it cancels from anywhere in it.
	let {
		item,
		inputId,
		errorId,
		formId,
		error,
		draft = $bindable(''),
		onInput
	}: {
		item: EnvSetKey;
		inputId: string;
		errorId: string;
		formId: string;
		/** Full error text, empty when there is none. */
		error: string;
		draft?: string;
		/** Typing clears the row error. */
		onInput: () => void;
	} = $props();

	const placeholder = $derived(editValueField(item).placeholder);
</script>

<div class="grid gap-1">
	{#if item.is_secret}
		<!-- Uncontrolled and empty: the stored secret never reaches the browser (only a generated value, once, through the reveal dialog). -->
		<Input
			id={inputId}
			form={formId}
			name="value"
			type={secretField.type}
			class="min-w-40 sm:min-w-48 {secretField.maskClass}"
			{placeholder}
			aria-label="New value for {item.key}"
			aria-invalid={error ? true : undefined}
			aria-describedby={error ? errorId : undefined}
			autocomplete={secretField.autocomplete}
			spellcheck="false"
			{...PASSWORD_MANAGER_IGNORE}
			oninput={onInput}
		/>
	{:else}
		<Input
			id={inputId}
			form={formId}
			name="value"
			type="text"
			class="min-w-40 sm:min-w-48"
			bind:value={draft}
			aria-label="Value for {item.key}"
			aria-invalid={error ? true : undefined}
			aria-describedby={error ? errorId : undefined}
			autocomplete="off"
			spellcheck="false"
			oninput={onInput}
		/>
	{/if}
	{#if error}
		<!-- Notice renders the shared alert role. -->
		<div id={errorId} class="max-w-full min-w-0 break-words">
			<Notice kind="error">{error}</Notice>
		</div>
	{/if}
</div>
