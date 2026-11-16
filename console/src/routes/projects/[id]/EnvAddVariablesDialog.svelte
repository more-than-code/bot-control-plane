<script lang="ts">
	import { enhance } from '$app/forms';
	import { tick, untrack } from 'svelte';
	import Notice from '$lib/components/Notice.svelte';
	import Textarea from '$lib/components/Textarea.svelte';
	import { restoreFocusTo } from '$lib/dialog-focus';
	import { nextFormDialog } from '$lib/dialog-open';
	import { updateInPlace } from '$lib/form-redirect';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import * as Tabs from '$lib/components/ui/tabs';
	import ValueSourceFields from '$lib/components/ValueSourceFields.svelte';
	import { dotenvWarnings, envValueWarnings } from '$lib/env-lint';
	import { looksSecretLine, secretLookingKeys } from '$lib/env-secret-name';
	import {
		addErrorText,
		addIsSecret,
		addKeyError,
		addKeyRefused,
		addKeyTyped,
		placeholderRule,
		addTabFailed,
		addValueTyped,
		closeAddVariables,
		initialAddVariables,
		pasteTabFailed,
		pasteTyped,
		setAddSecret,
		setAddSource,
		settleAdd,
		settleMerge,
		switchAddTab,
		type AddTab
	} from './env-add-variable';
	import { envDomId, failureMessage } from './env-set-panel';
	import { PASSWORD_MANAGER_IGNORE, secretInput, supportsTextMask } from '$lib/secret-input';

	// A masked secret is a CSS-masked text input where the browser can, so no "save password?" prompt ($lib/secret-input).
	let canMask = $state(false);
	$effect(() => {
		canMask = supportsTextMask();
	});

	let {
		setName,
		existingKeys,
		secretPlaceholders = [],
		plainPlaceholders = [],
		addAction,
		mergeAction,
		mergeError = null,
		onAdded,
		onGenerated = () => {},
		onMerged
	}: {
		setName: string;
		/** Keys already in the set: One variable refuses them instead of overwriting. Placeholders are left out, so Add fills them. */
		existingKeys: readonly string[];
		/** Secret placeholders (needs_value): adding one keeps it secret, typed or generated (api-v1 v1.27 item 4). */
		secretPlaceholders?: readonly string[];
		/** Plain placeholders: adding one keeps it plain, and Generate is off (a generated value is secret). */
		plainPlaceholders?: readonly string[];
		/** putEnvKey, sent with op=add. */
		addAction: string;
		/** mergeEnvSet (api-v1 v1.19 merge). */
		mergeAction: string;
		/** This set's mergeEnvSet failure from the page's form result (a post without JS), else null. */
		mergeError?: string | null;
		/** After a successful Add variable, before the reload: the panel sets "Added {KEY}.". */
		onAdded: (key: string) => void;
		/** After a generated value is saved and the page reloaded: the panel opens the show-once dialog. */
		onGenerated?: (key: string) => void;
		/** After a successful Add variables, before the reload: the panel sets "Saved variables in {set}.". */
		onMerged: () => void;
	} = $props();

	const id = (part: string) => envDomId(setName, part);
	const triggerId = $derived(id('add-variables'));
	const serverFailed = $derived(mergeError != null && mergeError !== '');

	let open = $state(untrack(() => serverFailed));
	let sawFailure = $state(untrack(() => serverFailed));
	let view = $state(untrack(() => (serverFailed ? pasteTabFailed(initialAddVariables(), mergeError ?? '') : initialAddVariables())));
	// Set when a success closes the dialog: focus moves to the status line after the reload, not on close.
	let closingAfterSave = false;

	const keyError = $derived(view.draft.errorField === 'key');
	const generating = $derived(view.draft.source === 'generate');
	const placeholder = $derived(placeholderRule(view.draft.key, secretPlaceholders, plainPlaceholders));
	const secretShown = $derived(placeholder.secret ?? addIsSecret(view.draft));
	const valueField = $derived(secretInput(secretShown, canMask));
	// Every key already in the set, placeholders included: the paste suggestion names new keys only.
	const allKeys = $derived([...existingKeys, ...secretPlaceholders, ...plainPlaceholders]);
	/** Typing a key: the name suggestion, and back to Type a value when the key is a plain placeholder. */
	function keyTyped() {
		let draft = addKeyTyped(view.draft);
		if (!placeholderRule(draft.key, secretPlaceholders, plainPlaceholders).canGenerate) draft = setAddSource(draft, 'value');
		view = { ...view, draft };
	}
	// Paste lint (spec-env-generate §3.6): warnings only, naming the key, never the value.
	const valueWarnings = $derived(generating ? [] : envValueWarnings(view.draft.key, view.draft.value));
	const pasteWarnings = $derived(dotenvWarnings(view.paste));
	// Secret suggestion by name (spec rev 3 §3.4): a visible hint, never a silent rule.
	const pasteLooksSecret = $derived(looksSecretLine(secretLookingKeys(view.paste, allKeys)));

	// A new form-result failure opens the dialog on Paste several. Closing it stays closed while it is current.
	$effect(() => {
		const failed = serverFailed;
		const next = nextFormDialog(untrack(() => ({ open, sawFailure })), failed ? 'failure' : 'idle');
		if (next.open && !untrack(() => open)) view = pasteTabFailed(untrack(() => view), mergeError ?? '');
		open = next.open;
		sawFailure = next.sawFailure;
	});

	function focusById(target: string) {
		document.getElementById(target)?.focus();
	}

	// Opening from the trigger, and closing with Cancel, Escape or the X, start from One variable with both inputs empty.
	function onOpenChange() {
		view = closeAddVariables(view);
	}

	function onCloseAutoFocus(event: Event) {
		if (closingAfterSave) {
			event.preventDefault();
			closingAfterSave = false;
			return;
		}
		restoreFocusTo(triggerId)(event);
	}

	function closeAfterSave() {
		if (!open) return;
		closingAfterSave = true;
		open = false;
		view = closeAddVariables(view);
	}

	function submitOne({ formData, cancel }: { formData: FormData; cancel: () => void }) {
		const key = String(formData.get('key') ?? '');
		const invalid = addKeyError(key, existingKeys);
		if (invalid) {
			cancel();
			view = { ...view, draft: addKeyRefused(view.draft, invalid) };
			void tick().then(() => focusById(id('add-key')));
			return;
		}
		const generated = formData.get('generate_kind') != null;
		return async ({ update, result }: { update: () => Promise<void>; result: { type: string; data?: unknown; location?: string } }) => {
			const outcome = await settleAdd(open, result.type, {
				added: () => onAdded(key),
				close: closeAfterSave,
				failed: async () => {
					// Stay open on this tab. The server never echoes the value; a secret one is asked for again.
					view = addTabFailed(view, failureMessage(result), secretShown);
					await tick();
					focusById(id(keyError ? 'add-key' : 'add-error'));
				},
				update: () => updateInPlace(result, update),
				tick,
				focusStatus: () => focusById(id('status'))
			});
			// The value is shown once, in the panel's reveal dialog, after the reload.
			if (outcome === 'added' && generated) onGenerated(key);
		};
	}

	function submitPaste() {
		return async ({ update, result }: { update: () => Promise<void>; result: { type: string; data?: unknown; location?: string } }) => {
			await settleMerge(open, result.type, {
				clearPaste: () => {
					view = { ...view, paste: '' };
				},
				merged: onMerged,
				close: closeAfterSave,
				failed: async () => {
					view = pasteTabFailed(view, failureMessage(result));
					await tick();
					focusById(id('merge-error'));
				},
				update: () => updateInPlace(result, update),
				tick,
				focusStatus: () => focusById(id('status'))
			});
		};
	}
</script>

{#snippet warnings(warningsId: string, items: string[])}
	{#if items.length > 0}
		<ul id={warningsId} class="text-muted-foreground grid gap-1 text-sm break-words">
			{#each items as item, index (index)}
				<li>{item}</li>
			{/each}
		</ul>
	{/if}
{/snippet}

<Dialog.Root bind:open {onOpenChange}>
	<Dialog.Trigger id={triggerId} class={buttonVariants({ variant: 'outline', size: 'sm' })} aria-label="Add variables to {setName}">
		Add variables
	</Dialog.Trigger>
	<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden sm:max-w-xl" {onCloseAutoFocus}>
		<Dialog.Header class="shrink-0 pr-8">
			<Dialog.Title class="break-all">Add variables to {setName}</Dialog.Title>
			<Dialog.Description>Secret values are encrypted and shown only as a short preview.</Dialog.Description>
		</Dialog.Header>
		<div class="grid min-h-0 gap-3 overflow-y-auto">
			<Tabs.Root bind:value={() => view.tab, (tab) => (view = switchAddTab(view, tab as AddTab))}>
				<Tabs.List aria-label="How to add" class="group-data-[orientation=horizontal]/tabs:h-10">
					<Tabs.Trigger value="one" id={id('tab-one')}>One variable</Tabs.Trigger>
					<Tabs.Trigger value="paste" id={id('tab-paste')}>Paste several</Tabs.Trigger>
				</Tabs.List>
				<Tabs.Content value="one">
					<form id={id('add-form')} method="POST" action={addAction} class="grid gap-3" use:enhance={submitOne}>
						<Input type="hidden" name="set" value={setName} />
						<Input type="hidden" name="op" value="add" />
						<Input type="hidden" name="is_secret" value={secretShown ? '1' : '0'} />
						{#if generating}
							<Input type="hidden" name="generate_kind" value={view.draft.kind} />
						{/if}
						<Label class="grid gap-1.5" for={id('add-key')}>
							Key
							<Input
								id={id('add-key')}
								name="key"
								bind:value={view.draft.key}
								oninput={keyTyped}
								required
								maxlength={64}
								autocomplete="off"
								spellcheck="false"
								aria-invalid={keyError ? true : undefined}
								aria-describedby={keyError ? id('add-error') : undefined}
							/>
						</Label>
						<ValueSourceFields
							idOf={(part) => id(`add-${part}`)}
							source={view.draft.source}
							kind={view.draft.kind}
							onSource={(next) => (view = { ...view, draft: setAddSource(view.draft, next) })}
							onKind={(next) => (view = { ...view, draft: { ...view.draft, kind: next } })}
							generateDisabled={!placeholder.canGenerate}
							generateDescribedBy={placeholder.note ? id('add-placeholder') : undefined}
						/>
						{#if !generating}
							<Label class="grid gap-1.5" for={id('add-value')}>
								<span class="sr-only">Value</span>
								<Input
									id={id('add-value')}
									name="value"
									type={valueField.type}
									class={valueField.maskClass}
									bind:value={view.draft.value}
									oninput={() => (view = { ...view, draft: addValueTyped(view.draft) })}
									autocomplete={valueField.autocomplete}
									spellcheck="false"
									{...PASSWORD_MANAGER_IGNORE}
									aria-describedby={[view.draft.error ? id('add-error') : null, valueWarnings.length > 0 ? id('add-warnings') : null].filter(Boolean).join(' ') || undefined}
								/>
							</Label>
							{@render warnings(id('add-warnings'), valueWarnings)}
						{/if}
						<Label class="flex items-center gap-2">
							<Checkbox
								id={id('add-secret')}
								checked={secretShown}
								disabled={generating || placeholder.secret !== null}
								aria-describedby={placeholder.note ? id('add-placeholder') : undefined}
								onCheckedChange={(value) => (view = { ...view, draft: setAddSecret(view.draft, value === true) })}
							/>
							Secret
						</Label>
						{#if placeholder.note}
							<p id={id('add-placeholder')} class="text-muted-foreground text-sm break-words">{placeholder.note}</p>
						{/if}
						{#if view.draft.error}
							<div id={id('add-error')} tabindex="-1" class="max-w-full min-w-0 break-words focus-visible:ring-3 focus-visible:ring-ring">
								<Notice kind="error">{addErrorText(view.draft)}</Notice>
							</div>
						{/if}
					</form>
				</Tabs.Content>
				<Tabs.Content value="paste">
					<form id={id('merge-form')} method="POST" action={mergeAction} class="grid gap-3" use:enhance={submitPaste}>
						<Input type="hidden" name="set" value={setName} />
						<p class="text-muted-foreground text-sm">
							Paste the variables, one KEY=value per line, like a .env file. Existing keys keep the secret flag they already have; other keys in the set are kept.
						</p>
						<Label class="grid gap-1.5" for={id('merge-dotenv')}>
							Variables (one KEY=value per line)
							<Textarea
								id={id('merge-dotenv')}
								name="dotenv"
								bind:value={view.paste}
								oninput={() => (view = pasteTyped(view, allKeys))}
								rows={6}
								spellcheck="false"
								autocomplete="off"
								aria-invalid={view.pasteError ? true : undefined}
								aria-describedby={[view.pasteError ? id('merge-error') : null, pasteWarnings.length > 0 ? id('merge-warnings') : null].filter(Boolean).join(' ') || undefined}
							/>
						</Label>
						{@render warnings(id('merge-warnings'), pasteWarnings)}
						<Input type="hidden" name="new_keys_secret" value={view.pasteSecret ? '1' : '0'} />
						<Label class="flex items-center gap-2">
							<Checkbox
								id={id('merge-new-secret')}
								checked={view.pasteSecret}
								aria-describedby={pasteLooksSecret ? id('merge-looks-secret') : undefined}
								onCheckedChange={(value) => (view = { ...view, pasteSecret: value === true, pasteSecretTouched: true })}
							/>
							New keys are secret
						</Label>
						{#if pasteLooksSecret}
							<p id={id('merge-looks-secret')} class="text-muted-foreground text-sm break-words">{pasteLooksSecret}</p>
						{/if}
						{#if view.pasteError}
							<div id={id('merge-error')} tabindex="-1" class="max-w-full min-w-0 break-words focus-visible:ring-3 focus-visible:ring-ring">
								<Notice kind="error">{view.pasteError}</Notice>
							</div>
						{/if}
					</form>
				</Tabs.Content>
			</Tabs.Root>
			<div class="flex flex-wrap items-center gap-2">
				{#if view.tab === 'one'}
					<Button type="submit" form={id('add-form')}>Add variable</Button>
				{:else}
					<Button type="submit" form={id('merge-form')}>Add variables</Button>
				{/if}
				<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
			</div>
		</div>
	</Dialog.Content>
</Dialog.Root>
