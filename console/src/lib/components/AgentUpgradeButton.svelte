<script lang="ts">
	import CircleArrowUp from '@lucide/svelte/icons/circle-arrow-up';
	import { onDestroy, untrack } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import RowAction from '$lib/components/RowAction.svelte';
	import * as AlertDialog from '$lib/components/ui/alert-dialog';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import type { AgentUpgrade } from '$lib/api-types';
	import { UPGRADE_POLL_MS, upgradeOutcomeText, upgradePollNext } from '$lib/agent-upgrade';

	// The Upgrade action of the "Update available" badge (api-v1 v1.48): a confirm dialog, then a running note
	// polled from /hosts/<id>/agent-upgrade until the host reports back, then the outcome in plain words.
	let {
		hostId,
		hostName,
		from,
		latest,
		running = false,
		compact = false
	}: {
		hostId: string;
		hostName: string;
		from: string | null;
		latest: string;
		/** An upgrade is already running (from the host read): start polling instead of offering the button. */
		running?: boolean;
		/** In a table row: an icon-only RowAction trigger. */
		compact?: boolean;
	} = $props();

	let open = $state(false);
	let busy = $state(false);
	// Set from the first render, so the server render already shows the running note.
	let polling = $state(untrack(() => running));
	let error = $state<string | null>(null);
	let outcome = $state<AgentUpgrade | null>(null);
	let timer: ReturnType<typeof setTimeout> | null = null;
	let pollStarted = 0;

	async function readNewest(): Promise<AgentUpgrade | null> {
		try {
			const res = await fetch(`/hosts/${hostId}/agent-upgrade`, { headers: { accept: 'application/json' } });
			if (!res.ok) return null;
			const body = (await res.json()) as { items?: AgentUpgrade[] };
			return body.items?.[0] ?? null;
		} catch {
			return null;
		}
	}

	async function poll() {
		const row = await readNewest();
		if (upgradePollNext(row, pollStarted, Date.now())) {
			timer = setTimeout(poll, UPGRADE_POLL_MS);
			return;
		}
		timer = null;
		polling = false;
		outcome = row;
		await invalidateAll();
	}

	function startPolling() {
		if (timer) return;
		polling = true;
		pollStarted = Date.now();
		timer = setTimeout(poll, UPGRADE_POLL_MS);
	}

	$effect(() => {
		if (running) startPolling();
	});

	onDestroy(() => {
		if (timer) clearTimeout(timer);
	});

	async function confirm() {
		busy = true;
		error = null;
		outcome = null;
		try {
			const res = await fetch(`/hosts/${hostId}/agent-upgrade`, {
				method: 'POST',
				headers: { 'content-type': 'application/json', accept: 'application/json' },
				body: JSON.stringify({ version: latest })
			});
			const body = (await res.json().catch(() => ({}))) as { message?: string };
			// 503: the host did not answer, but it may have started; the row stays open, so follow it.
			if (res.status === 503) {
				open = false;
				startPolling();
				return;
			}
			if (res.status !== 202) {
				error = body.message ?? 'The upgrade could not start.';
				return;
			}
			open = false;
			startPolling();
		} catch {
			error = 'The console could not reach the server.';
		} finally {
			busy = false;
		}
	}

	const message = $derived(
		`${hostName} downloads bot-agent ${latest} from GitHub, checks its signature, and restarts its agent${from ? ` (now ${from})` : ''}. ` +
			'If the new agent does not connect, the host puts the old one back by itself. Containers and services keep running.'
	);
</script>

{#if polling}
	<span class="text-muted-foreground text-sm" role="status">Upgrading to {latest}…</span>
{:else}
	<AlertDialog.Root bind:open>
		{#if compact}
			<RowAction label={`Upgrade bot-agent on ${hostName}`} icon={CircleArrowUp}>
				{#snippet trigger(props)}
					<AlertDialog.Trigger {...props}>
						<CircleArrowUp class="size-4" />
					</AlertDialog.Trigger>
				{/snippet}
			</RowAction>
		{:else}
			<AlertDialog.Trigger class={buttonVariants({ variant: 'outline', size: 'sm' })}>
				<CircleArrowUp class="size-4" />
				Upgrade
			</AlertDialog.Trigger>
		{/if}
		<AlertDialog.Content>
			<AlertDialog.Header>
				<AlertDialog.Title>Upgrade bot-agent on {hostName}?</AlertDialog.Title>
				<AlertDialog.Description>{message}</AlertDialog.Description>
			</AlertDialog.Header>
			{#if error}
				<p class="text-destructive text-sm" role="alert">{error}</p>
			{/if}
			<AlertDialog.Footer>
				<AlertDialog.Cancel type="button" disabled={busy}>Cancel</AlertDialog.Cancel>
				<Button type="button" disabled={busy} onclick={confirm}>{busy ? 'Starting…' : `Upgrade to ${latest}`}</Button>
			</AlertDialog.Footer>
		</AlertDialog.Content>
	</AlertDialog.Root>
{/if}
{#if outcome && !polling}
	<span class={outcome.state === 'succeeded' ? 'text-status-up text-sm' : 'text-destructive text-sm'} role="status">{upgradeOutcomeText(outcome)}</span>
{/if}
