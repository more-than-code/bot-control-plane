<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import type { SubmitFunction } from '@sveltejs/kit';
	import Globe from '@lucide/svelte/icons/globe';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { tick } from 'svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import FormDialog from '$lib/components/FormDialog.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import type { OrgDomain, Project } from '$lib/api-types';
	import { assignmentText, domainStatusBadge, type DomainPlan } from '$lib/edge-routes';
	import { actionUrlFrom } from '$lib/url';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Button } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import DomainAssignmentsDialog from './DomainAssignmentsDialog.svelte';

	/**
	 * Org Settings → Domains (api-v1 v1.35 item 1, org owners and admins). Add a domain, show its TXT record to verify
	 * it, assign it to projects, and delete it through a plan that lists the routes that stop.
	 */
	let {
		domains,
		omitted = 0,
		projects,
		message = null,
		addMessage = null,
		rowMessage = null,
		rowMessageId = null
	}: {
		domains: OrgDomain[];
		omitted?: number;
		projects: Project[];
		/** The list could not be read. */
		message?: string | null;
		/** The Add domain dialog's failure. */
		addMessage?: string | null;
		/** A Verify or Delete failure of the domain `rowMessageId`. */
		rowMessage?: string | null;
		rowMessageId?: string | null;
	} = $props();

	/** `verify:<id>` or `delete:<id>` while that post runs. */
	let pending = $state<string | null>(null);
	// The Delete plan lives here, not in the page data, so its dialog keeps focus and a refused Confirm shows inside it.
	let plan = $state<DomainPlan | null>(null);
	let planOpen = $state(false);
	let planMessage = $state<string | null>(null);
	let confirming = $state(false);
	const planDomain = $derived(plan ? (domains.find((domain) => domain.id === plan?.domainId) ?? null) : null);

	function errorId(domainId: string): string {
		return `domain-error-${domainId}`;
	}

	/** Verify and Delete: one post at a time per button; a refusal shows on the row and takes focus. */
	function rowEnhance(kind: 'verify' | 'delete', domainId: string): SubmitFunction {
		return ({ cancel }) => {
			if (pending != null) {
				cancel();
				return;
			}
			pending = `${kind}:${domainId}`;
			return async ({ result, update }) => {
				try {
					if (kind === 'delete' && result.type === 'success' && result.data?.domainPlan) {
						plan = result.data.domainPlan as DomainPlan;
						planMessage = null;
						planOpen = true;
						return;
					}
					await update();
				} finally {
					pending = null;
				}
				if (result.type === 'failure') {
					await tick();
					document.getElementById(errorId(domainId))?.focus();
				}
			};
		};
	}

	const confirmEnhance: SubmitFunction = ({ cancel }) => {
		if (confirming) {
			cancel();
			return;
		}
		confirming = true;
		return async ({ result, update }) => {
			try {
				if (result.type === 'failure') {
					const text = result.data?.message;
					planMessage = typeof text === 'string' ? text : 'The domain was not deleted.';
					return;
				}
				planOpen = false;
				await update();
			} finally {
				confirming = false;
			}
		};
	};

	function onPlanOpenChange(open: boolean) {
		if (open) return;
		planMessage = null;
	}

	/** Back to the domain's Delete button, or to Add domain once the domain is gone. */
	function onPlanCloseAutoFocus(event: Event) {
		const domainId = plan?.domainId;
		plan = null;
		const target = (domainId && document.getElementById(`delete-domain-${domainId}`)) || document.getElementById('add-domain');
		if (target) {
			event.preventDefault();
			target.focus();
		}
	}
</script>

<Card.Root class="mb-4" id="domains">
	<Card.Header>
		<Card.Title>Domains</Card.Title>
		<Card.Description>
			Verify a domain once with a DNS TXT record, then assign it to projects. A project adds public addresses under its domains in an entry's Edit drawer; they go live on hosts
			whose edge is on.
		</Card.Description>
		<Card.Action>
			<FormDialog id="add-domain" label="Add domain" title="Add a domain" description="Use the xn-- (punycode) form for non-ASCII names. You will get a TXT record to add at your DNS provider." failed={addMessage != null}>
				{#snippet children(settle, cancel)}
					<form
						method="POST"
						action={actionUrlFrom(page.url, 'addDomain')}
						class="grid gap-3"
						use:enhance={() =>
							async ({ result, update }) => {
								settle(result.type);
								await update();
							}}
					>
						{#if addMessage}
							<div class="max-w-full min-w-0 break-words"><Notice kind="error">{addMessage}</Notice></div>
						{/if}
						<Label class="grid gap-1.5" for="add-domain-name">
							Domain
							<Input id="add-domain-name" name="name" required maxlength={253} autocomplete="off" spellcheck="false" placeholder="example.com" />
						</Label>
						<div class="flex flex-wrap gap-2">
							<Button type="submit">Add domain</Button>
							{@render cancel()}
						</div>
					</form>
				{/snippet}
			</FormDialog>
		</Card.Action>
	</Card.Header>
	<Card.Content class="grid gap-4">
		{#if message}
			<Notice kind="error">{message}</Notice>
		{:else if domains.length === 0}
			<p class="text-muted-foreground text-sm">No domains yet.</p>
		{/if}
		{#if omitted > 0}
			<p class="text-muted-foreground text-sm">{omitted} {omitted === 1 ? 'domain' : 'domains'} could not be read.</p>
		{/if}
		<ul class="grid gap-4">
			{#each domains as domain (domain.id)}
				{@const badge = domainStatusBadge(domain.status)}
				<li class="grid min-w-0 gap-3 rounded-lg border p-3" id="domain-{domain.id}" data-domain={domain.name}>
					<div class="flex min-w-0 flex-wrap items-center gap-2">
						<Globe class="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
						<span class="font-mono break-all">{domain.name}</span>
						<StatusBadge status={badge.status} text={badge.text} />
					</div>
					{#if rowMessageId === domain.id && rowMessage}
						<div id={errorId(domain.id)} tabindex="-1" class="max-w-full min-w-0 rounded-md break-words focus-visible:ring-3 focus-visible:ring-ring">
							<Notice kind="error">{rowMessage}</Notice>
						</div>
					{/if}
					{#if domain.status === 'lapsed'}
						<div class="max-w-full min-w-0 break-words">
							<Notice kind="info">
								The TXT record was not found for 7 days. Routes under this domain keep serving, but no new route is accepted. Add the record again and verify.
							</Notice>
						</div>
					{/if}
					{#if domain.status !== 'verified'}
						<div class="grid min-w-0 gap-2 text-sm">
							<p>Add this TXT record at your DNS provider, then verify.</p>
							<div class="grid min-w-0 gap-1">
								<span class="text-muted-foreground">Name</span>
								<div class="flex min-w-0 flex-wrap items-center gap-2">
									<code class="bg-muted rounded px-1.5 py-0.5 font-mono break-all">{domain.txt_name}</code>
									<CopyButton value={domain.txt_name} label="Copy name" />
								</div>
							</div>
							<div class="grid min-w-0 gap-1">
								<span class="text-muted-foreground">Value</span>
								<div class="flex min-w-0 flex-wrap items-center gap-2">
									<code class="bg-muted rounded px-1.5 py-0.5 font-mono break-all">{domain.txt_value}</code>
									<CopyButton value={domain.txt_value} label="Copy value" />
								</div>
							</div>
						</div>
					{/if}
					<dl class="text-muted-foreground grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
						<dt>Verified</dt>
						<dd><LocalTime iso={domain.verified_at} /></dd>
						<dt>Last check</dt>
						<dd><LocalTime iso={domain.last_check_at} /></dd>
					</dl>
					<div class="grid min-w-0 gap-1 text-sm">
						<span class="font-medium">Assigned to</span>
						{#if domain.assignments.length === 0}
							<p class="text-muted-foreground">No project yet.</p>
						{:else}
							<ul class="grid gap-0.5">
								{#each domain.assignments as assignment (`${assignment.project_id} ${assignment.pattern}`)}
									<li class="break-words">
										<span class="font-mono break-all">{assignmentText(domain.name, assignment.pattern)}</span>
										<span class="text-muted-foreground">→ {assignment.project_name || 'a project'}</span>
									</li>
								{/each}
							</ul>
						{/if}
					</div>
					<div class="flex flex-wrap gap-2">
						{#if domain.status !== 'verified'}
							<form method="POST" action={actionUrlFrom(page.url, 'verifyDomain')} use:enhance={rowEnhance('verify', domain.id)}>
								<Input type="hidden" name="domain_id" value={domain.id} />
								<Button type="submit" variant="outline" size="sm" aria-label="Verify {domain.name}" disabled={pending === `verify:${domain.id}`} aria-busy={pending === `verify:${domain.id}`}>
									{pending === `verify:${domain.id}` ? 'Verifying…' : 'Verify'}
								</Button>
							</form>
						{/if}
						<DomainAssignmentsDialog {domain} {projects} action={actionUrlFrom(page.url, 'setDomainAssignments')} />
						<form method="POST" action={actionUrlFrom(page.url, 'deleteDomain')} use:enhance={rowEnhance('delete', domain.id)}>
							<Input type="hidden" name="domain_id" value={domain.id} />
							<Button
								id="delete-domain-{domain.id}"
								type="submit"
								variant="outline"
								size="sm"
								aria-label="Delete {domain.name}"
								disabled={pending === `delete:${domain.id}`}
								aria-busy={pending === `delete:${domain.id}`}
							>
								<Trash2 class="size-4" />{pending === `delete:${domain.id}` ? 'Checking…' : 'Delete'}
							</Button>
						</form>
					</div>
				</li>
			{/each}
		</ul>
	</Card.Content>
</Card.Root>

<!-- Delete plan (202): what stops if the domain goes. Nothing is removed until Confirm delete. -->
<AlertDialog.Root bind:open={planOpen} onOpenChange={onPlanOpenChange}>
	<AlertDialog.Content onCloseAutoFocus={onPlanCloseAutoFocus}>
		<AlertDialog.Header>
			<AlertDialog.Title class="break-all">Delete {planDomain?.name ?? 'domain'}?</AlertDialog.Title>
			<AlertDialog.Description class="break-words whitespace-pre-line" data-domain-plan>{plan?.plan ?? ''}</AlertDialog.Description>
		</AlertDialog.Header>
		{#if planMessage}
			<div class="max-w-full min-w-0 break-words" data-domain-plan-error><Notice kind="error">{planMessage}</Notice></div>
		{/if}
		{#if plan}
			<form method="POST" action={actionUrlFrom(page.url, 'confirmDomainDelete')} use:enhance={confirmEnhance}>
				<Input type="hidden" name="domain_id" value={plan.domainId} />
				<Input type="hidden" name="confirmation_id" value={plan.confirmation_id} />
				<Input type="hidden" name="idempotency_key" value={plan.idempotency_key} />
				<AlertDialog.Footer>
					<AlertDialog.Cancel type="button">Cancel</AlertDialog.Cancel>
					<Button type="submit" variant="destructive" disabled={confirming} aria-busy={confirming}><Trash2 class="size-4" />Confirm delete</Button>
				</AlertDialog.Footer>
			</form>
		{/if}
	</AlertDialog.Content>
</AlertDialog.Root>
