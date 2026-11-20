<script lang="ts">
	import { goto } from '$app/navigation';
	import Bell from '@lucide/svelte/icons/bell';
	import { onMount, untrack } from 'svelte';
	import NotificationsList from '$lib/components/NotificationsList.svelte';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as Popover from '$lib/components/ui/popover';
	import type { NotificationList } from '$lib/api-types';
	import { bellLabel } from '$lib/notifications';
	import {
		bellNeedAction,
		bellUnread,
		browserPoll,
		canMarkAll,
		createBell,
		initialBellState,
		MARK_ERROR,
		type BellState
	} from '$lib/notifications-bell';
	import { cn } from '$lib/utils';

	/**
	 * The header bell (spec-header-org-project-bell §3.2). It reads the console route after the page has loaded, again
	 * whenever the popover opens, and every 2 minutes while the tab is visible and the popover is closed
	 * (spec-bell-problems §3.3), so no page load waits on it. Opening marks nothing read. The dot is red while an unread open problem needs action. The
	 * behaviour is `$lib/notifications-bell.ts`; this component only holds its state.
	 */
	let {
		initial = null
	}: {
		/** A first value before the read (server render in tests). The layout passes none. */
		initial?: { list: NotificationList | null; failed: boolean } | null;
	} = $props();

	let bell = $state<BellState>(untrack(() => initialBellState(initial)));
	const controller = createBell(
		{ fetch: (input, init) => fetch(input, init), goto: (href) => goto(href), update: (next) => (bell = next) },
		untrack(() => bell)
	);
	let open = $state(false);
	let titleEl = $state<HTMLElement | null>(null);

	/** Mark all read disables itself when nothing is left unread; keep focus in the popover on the title then. */
	async function markAll(button: HTMLElement): Promise<void> {
		await controller.markAll();
		if (button.matches(':disabled') && (document.activeElement === button || document.activeElement === document.body)) titleEl?.focus();
	}

	const TITLE_ID = 'notifications-title';
	const unread = $derived(bellUnread(bell));
	const needAction = $derived(bellNeedAction(bell));

	onMount(() => {
		void controller.start();
		return controller.poll(browserPoll(() => open));
	});
</script>

<Popover.Root bind:open onOpenChange={(next) => void controller.openChanged(next)}>
	<Popover.Trigger
		class={cn(buttonVariants({ variant: 'ghost', size: 'icon' }), 'relative shrink-0 max-sm:size-11')}
		aria-label={bellLabel(unread, needAction)}
	>
		<Bell class="size-4" />
		{#if unread}
			<span
				aria-hidden="true"
				data-bell-dot={needAction > 0 ? 'needs-action' : 'unread'}
				class={cn('absolute top-1.5 right-1.5 size-2 rounded-full', needAction > 0 ? 'bg-destructive' : 'bg-primary')}
			></span>
		{/if}
	</Popover.Trigger>
	<Popover.Content align="end" class="w-80 max-w-[calc(100vw-2rem)]" aria-labelledby={TITLE_ID}>
		<div class="flex items-center justify-between gap-2">
			<Popover.Title id={TITLE_ID} tabindex={-1} bind:ref={titleEl}>Notifications</Popover.Title>
			{#if (bell.list?.items.length ?? 0) > 0}
				<Button
					variant="link"
					size="sm"
					class="h-auto px-0"
					disabled={!canMarkAll(bell)}
					onclick={(event) => void markAll(event.currentTarget)}>Mark all read</Button
				>
			{/if}
		</div>
		{#if bell.markFailed}
			<p data-mark-error class="text-destructive px-1 text-sm">{MARK_ERROR}</p>
		{/if}
		<NotificationsList
			items={bell.list?.items ?? null}
			failed={bell.loadFailed}
			onOpen={(item) => void controller.openItem(item, () => (open = false))}
		/>
		<a class="text-primary border-t pt-2 text-sm underline-offset-4 hover:underline" href="/whats-new" onclick={() => (open = false)}
			>See all changes</a
		>
	</Popover.Content>
</Popover.Root>
