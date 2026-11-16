<script lang="ts">
	import { page } from '$app/state';
	import Globe from '@lucide/svelte/icons/globe';
	import Plus from '@lucide/svelte/icons/plus';
	import { tick } from 'svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { toastRoutesSaved } from '$lib/success-toast';
	import Notice from '$lib/components/Notice.svelte';
	import Textarea from '$lib/components/Textarea.svelte';
	import type { CatalogEntry, EdgeRoute } from '$lib/api-types';
	import {
		blankRouteDraft,
		defaultRoutePort,
		NO_STORE_MAX,
		routeDraftOf,
		routePorts,
		routesFromDrafts,
		routeStateBadge,
		routeUrl,
		ROUTES_MAX,
		type EdgePlan,
		type RouteDraft
	} from '$lib/edge-routes';
	import { actionUrlFrom } from '$lib/url';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import * as Dialog from '$lib/components/ui/dialog';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import * as Select from '$lib/components/ui/select';
	import FieldsPost from './FieldsPost.svelte';

	/**
	 * Public addresses of one entry (api-v1 v1.35 item 3): its routes `hostname[/path] → port`, with their state. Edit
	 * replaces the whole list as a plan the user confirms; nothing changes before Confirm. The API decides which
	 * hostnames are allowed (assigned, verified domains), whether an image's port is published, and conflicts; its
	 * refusal is shown as is. Owners and admins only (the routes API is theirs).
	 */
	let {
		entry,
		collapsed = false,
		onSaved
	}: {
		entry: CatalogEntry;
		collapsed?: boolean;
		/** After a confirmed change: the drawer re-runs its readiness (the `routes` check). */
		onSaved?: () => void;
	} = $props();

	const canAdmin = $derived(page.data.canAdmin === true);
	const loadAction = $derived(actionUrlFrom(page.url, 'catalogRoutes'));
	const saveAction = $derived(actionUrlFrom(page.url, 'catalogRoutesSave'));
	const confirmAction = $derived(actionUrlFrom(page.url, 'catalogRoutesConfirm'));
	const sectionId = $derived(`entry-routes-${entry.name}`);
	const ports = $derived(routePorts(entry));

	let loadPost = $state<FieldsPost | null>(null);
	let savePost = $state<FieldsPost | null>(null);
	let confirmPost = $state<FieldsPost | null>(null);

	let routes = $state<EdgeRoute[]>([]);
	let omitted = $state(0);
	let loadMessage = $state<string | null>(null);
	let loading = $state(true);
	// Set by a confirmed change; the toast fires once the dialog has finished closing.
	let savedPending = false;

	let open = $state(false);
	let drafts = $state<RouteDraft[]>([]);
	let editMessage = $state<string | null>(null);
	let plan = $state<EdgePlan | null>(null);
	// The plan is bound to the key of the PUT that issued it; Confirm sends the same key (api-v1 v1.35, v1.33 item 1).
	let reviewKey = $state('');
	let busy = $state(false);

	function load() {
		loading = true;
		loadMessage = null;
		loadPost?.send({ entry: entry.name });
	}

	$effect(() => {
		if (loadPost && canAdmin) load();
	});

	// Opening starts from the saved routes and drops any earlier plan or message.
	let wasOpen = false;
	$effect(() => {
		const now = open;
		if (now && !wasOpen) {
			drafts = routes.map(routeDraftOf);
			plan = null;
			editMessage = null;
		}
		wasOpen = now;
	});

	function takeRoutes(fields: Record<string, string>, data: Record<string, unknown>) {
		if (fields.entry !== entry.name) return;
		loading = false;
		if (Array.isArray(data.routes)) {
			routes = data.routes as EdgeRoute[];
			omitted = typeof data.routesOmitted === 'number' ? data.routesOmitted : 0;
			loadMessage = null;
		} else {
			loadMessage = typeof data.routesMessage === 'string' ? data.routesMessage : 'Public addresses could not be read.';
		}
	}

	function review(event: SubmitEvent) {
		event.preventDefault();
		if (busy) return;
		const built = routesFromDrafts(drafts);
		if (!built.ok) {
			editMessage = built.message;
			return;
		}
		editMessage = null;
		busy = true;
		// Each review is a new request with a new key.
		reviewKey = crypto.randomUUID();
		savePost?.send({ entry: entry.name, routes: JSON.stringify(built.routes), idempotency_key: reviewKey });
	}

	function takePlan(fields: Record<string, string>, data: Record<string, unknown>) {
		busy = false;
		if (fields.entry !== entry.name) return;
		const next = data.routesPlan as EdgePlan | undefined;
		if (next && typeof next.confirmation_id === 'string' && typeof next.plan === 'string') {
			plan = next;
			editMessage = null;
		} else {
			plan = null;
			editMessage = typeof data.routesMessage === 'string' ? data.routesMessage : 'The change could not be planned.';
		}
	}

	function confirmPlan() {
		if (busy || !plan) return;
		busy = true;
		confirmPost?.send({ entry: entry.name, confirmation_id: plan.confirmation_id, idempotency_key: reviewKey });
	}

	function takeConfirm(fields: Record<string, string>, data: Record<string, unknown>, ok: boolean) {
		busy = false;
		if (fields.entry !== entry.name) return;
		if (ok && data.routesConfirmed === true) {
			plan = null;
			savedPending = true;
			open = false;
			load();
			onSaved?.();
			return;
		}
		// An expired or used plan cannot be confirmed again: go back to editing.
		plan = null;
		editMessage = typeof data.routesMessage === 'string' ? data.routesMessage : 'The change was not applied.';
	}

	function edited() {
		// A plan names what was reviewed; any edit needs a new one.
		plan = null;
	}

	function addRow() {
		drafts = [...drafts, blankRouteDraft(defaultRoutePort(entry))];
		edited();
	}

	async function removeRow(index: number) {
		drafts = drafts.filter((_, item) => item !== index);
		edited();
		await tick();
		(document.getElementById(`${sectionId}-host-${index}`) ?? document.getElementById(`${sectionId}-add`))?.focus();
	}

	// After bits-ui has closed the dialog, so the toast is not announced inside it.
	function onOpenChangeComplete(isOpen: boolean) {
		if (isOpen || !savedPending) return;
		savedPending = false;
		toastRoutesSaved();
	}
</script>

{#if canAdmin}
	<FieldsPost bind:this={loadPost} action={loadAction} onResult={takeRoutes} />
	<FieldsPost bind:this={savePost} action={saveAction} onResult={takePlan} />
	<FieldsPost bind:this={confirmPost} action={confirmAction} onResult={takeConfirm} />
	<section id={sectionId} class="mt-2 grid min-w-0 gap-1" aria-labelledby="{sectionId}-heading" data-entry-routes>
		<div class="flex min-w-0 flex-wrap items-center gap-2">
			<h3 id="{sectionId}-heading" class="text-sm font-medium">Public addresses{collapsed && routes.length > 0 ? ` (${routes.length})` : ''}</h3>
			<Dialog.Root bind:open {onOpenChangeComplete}>
				<Dialog.Trigger class={buttonVariants({ variant: 'outline', size: 'sm' })} disabled={loading || loadMessage != null}>
					<Globe class="size-4" />Edit public addresses
				</Dialog.Trigger>
				<Dialog.Content class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-y-auto sm:max-w-2xl">
					<Dialog.Header class="pr-8">
						<Dialog.Title class="break-all">Public addresses of {entry.name}</Dialog.Title>
						<Dialog.Description>
							Each address sends https://host/path to a port of this entry, on hosts where it is deployed and whose edge is on. The host name must be under a verified domain
							assigned to this project.
						</Dialog.Description>
					</Dialog.Header>
					<form class="grid min-w-0 gap-3" onsubmit={review}>
						{#if ports != null && ports.length === 0}
							<p class="text-muted-foreground text-sm" data-routes-no-ports>
								This container publishes no port. Add a ports line for the port to route (127.0.0.1 is enough) and save the entry first.
							</p>
						{/if}
						{#if drafts.length === 0}
							<p class="text-muted-foreground text-sm">No public address. Saving with none removes every address of this entry.</p>
						{/if}
						{#each drafts as draft, index (index)}
							<fieldset class="grid min-w-0 gap-2 rounded-md border p-2" data-route-row={index}>
								<legend class="px-1 text-sm font-medium">Address {index + 1}</legend>
								<div class="grid min-w-0 gap-2 sm:grid-cols-[2fr_1fr_1fr]">
									<Label class="grid gap-1.5" for="{sectionId}-host-{index}">
										Host name
										<Input
											id="{sectionId}-host-{index}"
											bind:value={drafts[index].hostname}
											oninput={edited}
											maxlength={253}
											autocomplete="off"
											spellcheck="false"
											placeholder="app.example.com"
										/>
									</Label>
									<Label class="grid gap-1.5" for="{sectionId}-path-{index}">
										Path
										<Input id="{sectionId}-path-{index}" bind:value={drafts[index].path_prefix} oninput={edited} maxlength={256} autocomplete="off" spellcheck="false" placeholder="/" />
									</Label>
									{#if ports != null}
										<div class="grid gap-1.5">
											<Label for="{sectionId}-port-{index}">Port</Label>
											<Select.Root type="single" bind:value={drafts[index].port} onValueChange={edited}>
												<Select.Trigger id="{sectionId}-port-{index}" class="w-full font-mono">{draft.port === '' ? 'Choose' : draft.port}</Select.Trigger>
												<Select.Content>
													{#each ports as port (port)}
														<Select.Item value={String(port)} label={String(port)}>{port}</Select.Item>
													{/each}
												</Select.Content>
											</Select.Root>
										</div>
									{:else}
										<Label class="grid gap-1.5" for="{sectionId}-port-{index}">
											Port
											<Input id="{sectionId}-port-{index}" bind:value={drafts[index].port} oninput={edited} inputmode="numeric" maxlength={5} placeholder="8080" />
										</Label>
									{/if}
								</div>
								<Label class="flex items-center gap-2" for="{sectionId}-cf-{index}">
									<Checkbox
										id="{sectionId}-cf-{index}"
										checked={draft.cloudflare}
										onCheckedChange={(value) => {
											drafts[index].cloudflare = value === true;
											edited();
										}}
									/>
									Behind Cloudflare (take the client address from CF-Connecting-IP, only from Cloudflare's ranges)
								</Label>
								<Label class="grid gap-1.5" for="{sectionId}-nostore-{index}">
									No-store paths (one per line, at most {NO_STORE_MAX})
									<Textarea
										id="{sectionId}-nostore-{index}"
										bind:value={drafts[index].no_store}
										oninput={edited}
										rows={2}
										class="min-h-12 font-mono"
										spellcheck="false"
										placeholder="/evidence/"
									/>
								</Label>
								<div>
									<Button type="button" variant="outline" size="sm" aria-label="Remove address {index + 1}" onclick={() => removeRow(index)}>Remove</Button>
								</div>
							</fieldset>
						{/each}
						<div>
							<Button id="{sectionId}-add" type="button" variant="outline" disabled={drafts.length >= ROUTES_MAX || (ports != null && ports.length === 0)} onclick={addRow}>
								<Plus class="size-4" />Add address
							</Button>
						</div>
						{#if editMessage}
							<div class="max-w-full min-w-0 break-words" data-routes-error><Notice kind="error">{editMessage}</Notice></div>
						{/if}
						{#if plan}
							<div class="max-w-full min-w-0 break-words whitespace-pre-line" data-routes-plan><Notice kind="info">{plan.plan}</Notice></div>
							{#if plan.expires_at}
								<p class="text-muted-foreground text-sm" data-routes-plan-expiry>Confirm before <LocalTime iso={plan.expires_at} />.</p>
							{/if}
							<div class="flex flex-wrap gap-2">
								<Button type="button" disabled={busy} aria-busy={busy} onclick={confirmPlan}>Confirm</Button>
								<Button type="button" variant="outline" disabled={busy} onclick={edited}>Back to edit</Button>
							</div>
						{:else}
							<div class="flex flex-wrap gap-2">
								<Button type="submit" variant="outline" aria-busy={busy} aria-disabled={busy}>{busy ? 'Checking…' : 'Review changes'}</Button>
								<Dialog.Close type="button" class={buttonVariants({ variant: 'outline' })}>Cancel</Dialog.Close>
							</div>
						{/if}
					</form>
				</Dialog.Content>
			</Dialog.Root>
		</div>
		{#if loading}
			<p class="sr-only" role="status">Loading public addresses</p>
		{:else if loadMessage}
			<div class="flex min-w-0 flex-wrap items-center gap-2" data-routes-load-error>
				<p class="text-muted-foreground text-sm break-words">Public addresses could not be read: {loadMessage}</p>
				<Button type="button" variant="outline" size="sm" onclick={load}>Retry</Button>
			</div>
		{:else if routes.length === 0}
			<p class="text-muted-foreground text-sm">None.</p>
		{:else if !collapsed}
			<ul class="grid min-w-0 gap-0.5 text-sm">
				{#each routes as route (routeUrl(route))}
					{@const badge = routeStateBadge(route.state)}
					<li class="flex min-w-0 flex-wrap items-center gap-x-2 break-words" data-route={routeUrl(route)}>
						<span class="font-mono break-all">{routeUrl(route)}</span>
						<span class="text-muted-foreground">→ port {route.port}</span>
						<StatusBadge status={badge.status} text={badge.text} />
						{#if route.client_ip_from === 'cloudflare'}<span class="text-muted-foreground">behind Cloudflare</span>{/if}
						{#if route.no_store_paths.length > 0}<span class="text-muted-foreground">no-store {route.no_store_paths.join(', ')}</span>{/if}
					</li>
				{/each}
			</ul>
			{#if routes.some((route) => route.state === 'pending')}
				<p class="text-muted-foreground text-sm">Pending: the entry is not deployed on a host with the edge on, or it is not listening on that port yet.</p>
			{/if}
		{/if}
		{#if omitted > 0}
			<p class="text-muted-foreground text-sm">{omitted} {omitted === 1 ? 'address' : 'addresses'} could not be read.</p>
		{/if}
	</section>
{/if}
