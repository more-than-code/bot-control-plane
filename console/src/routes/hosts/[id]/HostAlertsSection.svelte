<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import Notice from '$lib/components/Notice.svelte';
	import type { HostAlertSettings } from '$lib/api-types';
	import { ALERT_FIELDS, ALERT_LINK_KEYS, alertsStagedFromUrl, alertValueText, hasAlertLink } from '$lib/host-alerts';
	import { toastAlertsSaved } from '$lib/success-toast';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';

	/**
	 * Resource alerts (api-v1 v1.49 item 3): org owners and admins edit; anyone else who sees the host reads them.
	 * An empty number means the default. The API checks the ranges and the role again.
	 * An MCP deep link (api-v1 v1.60) stages a change through `alerts.*`; nothing is saved until the person taps Save.
	 */
	let {
		settings,
		canEdit,
		loadMessage = null,
		actionMessage = null
	}: { settings: HostAlertSettings | null; canEdit: boolean; loadMessage?: string | null; actionMessage?: string | null } = $props();

	const ERROR_ID = 'alerts-error';
	let saving = $state(false);
	const linked = $derived(hasAlertLink(page.url.searchParams));
	// The stored values plus what a deep link stages; only for someone who can save them.
	const staged = $derived(settings && canEdit && linked ? alertsStagedFromUrl(page.url.searchParams, settings) : null);
	const shown = $derived(staged?.values ?? settings);
	// The switch starts from the shown value and follows it only when that value changes (a save, another
	// admin, a new link), so the 30 s Overview refresh, which hands in a new object, keeps an unsaved switch as it is.
	// svelte-ignore state_referenced_locally
	let enabled = $state(shown?.alerts_enabled ?? true);
	// svelte-ignore state_referenced_locally
	let shownEnabled = shown?.alerts_enabled ?? true;
	$effect(() => {
		const next = shown?.alerts_enabled ?? true;
		if (next !== shownEnabled) {
			shownEnabled = next;
			enabled = next;
		}
	});
</script>

<section id="alerts" class="mt-8 grid min-w-0 gap-3" aria-labelledby="alerts-heading">
	<h2 id="alerts-heading" class="text-lg font-semibold tracking-tight">Alerts</h2>
	<p id="alerts-help" class="text-muted-foreground text-sm">
		Org owners and admins get a notice when this host runs low on memory or disk, or stays busy. Leave a field empty to use the default.
	</p>
	{#if actionMessage}
		<div id={ERROR_ID}>
			<Notice kind="error">{actionMessage}</Notice>
		</div>
	{/if}
	{#if loadMessage}
		<Notice kind="error">{loadMessage}</Notice>
	{:else if settings && canEdit && shown}
		{#if staged && staged.changes.length > 0}
			<div data-alerts-staged>
				<Notice kind="info">A link filled in this change: {staged.changes.join('; ')}. Nothing is saved until you tap Save.</Notice>
			</div>
		{/if}
		{#if staged && staged.dropped > 0}
			<Notice kind="error">{staged.dropped === 1 ? '1 value in the link was not valid and was left out.' : `${staged.dropped} values in the link were not valid and were left out.`}</Notice>
		{/if}
		<form
			method="POST"
			action={actionUrlFrom(page.url, 'saveAlerts', ALERT_LINK_KEYS)}
			class="grid max-w-md min-w-0 gap-3"
			aria-describedby={actionMessage ? `alerts-help ${ERROR_ID}` : 'alerts-help'}
			use:enhance={({ cancel }) => {
				if (saving) {
					cancel();
					return;
				}
				saving = true;
				return async ({ result, update }) => {
					try {
						await update({ reset: false });
					} finally {
						saving = false;
					}
					if (result.type === 'redirect' || result.type === 'success') toastAlertsSaved();
				};
			}}
		>
			<div class="flex items-center gap-2">
				<Switch id="alerts-enabled" name="alerts_enabled" value="1" bind:checked={enabled} aria-describedby="alerts-help" />
				<Label for="alerts-enabled">Alerts</Label>
				{#if staged?.changed.includes('alerts_enabled')}<span class="text-xs font-medium" data-alerts-changed>from the link</span>{/if}
			</div>
			{#each ALERT_FIELDS as field (field.name)}
				<div class="grid gap-1">
					<Label for="alerts-{field.name}">
						{field.label} (%)
						{#if staged?.changed.includes(field.name)}<span class="text-xs font-medium" data-alerts-changed>from the link</span>{/if}
					</Label>
					<Input
						id="alerts-{field.name}"
						name={field.name}
						type="number"
						inputmode="numeric"
						min={field.min}
						max={field.max}
						step="1"
						placeholder="Default ({field.fallback})"
						value={shown[field.name] ?? ''}
						aria-describedby="alerts-{field.name}-help"
						aria-invalid={actionMessage && (actionMessage.startsWith(field.label) || actionMessage.startsWith(field.name)) ? 'true' : undefined}
					/>
					<span id="alerts-{field.name}-help" class="text-muted-foreground text-xs">{field.help}; {field.min}–{field.max}</span>
				</div>
			{/each}
			<div>
				<Button type="submit" size="sm" disabled={saving} aria-busy={saving}>{saving ? 'Saving…' : 'Save'}</Button>
			</div>
		</form>
	{:else if settings}
		{#if linked}
			<p class="text-muted-foreground text-sm" data-alerts-link-readonly>This link proposes an alert change; an org owner or admin can apply it.</p>
		{/if}
		<dl class="text-sm grid grid-cols-[auto_1fr] gap-x-3 gap-y-1" data-alerts-readonly>
			<dt class="text-muted-foreground">Alerts</dt>
			<dd>{settings.alerts_enabled ? 'On' : 'Off'}</dd>
			{#each ALERT_FIELDS as field (field.name)}
				<dt class="text-muted-foreground">{field.label}</dt>
				<dd>{alertValueText(settings, field)}</dd>
			{/each}
		</dl>
	{/if}
</section>
