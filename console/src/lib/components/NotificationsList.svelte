<script lang="ts">
	import ServerCog from '@lucide/svelte/icons/server-cog';
	import Sparkles from '@lucide/svelte/icons/sparkles';
	import type { Notification } from '$lib/api-types';
	import { formatAgo, formatWhen } from '$lib/format';
	import { NOTIFICATIONS_ERROR } from '$lib/notifications';
	import { timeZoneSource } from '$lib/time-zone';
	import { cn } from '$lib/utils';

	/** The bell popover's rows (spec-header-org-project-bell §3.2): newest first as the API sends them. */
	let {
		items,
		failed = false,
		onOpen
	}: {
		/** Null until a list has loaded. */
		items: Notification[] | null;
		/** The latest read failed: the error line shows above the last list that loaded, if any. */
		failed?: boolean;
		/** A row was clicked: mark it read, then go to its page. */
		onOpen?: (item: Notification) => void;
	} = $props();

	const zones = timeZoneSource();
</script>

{#if failed}
	<p data-load-error class="text-muted-foreground px-1 py-2 text-sm">{NOTIFICATIONS_ERROR}</p>
{/if}
{#if items == null}
	{#if !failed}<p class="text-muted-foreground px-1 py-2 text-sm">Loading…</p>{/if}
{:else if items.length === 0}
	<p class="text-muted-foreground px-1 py-2 text-sm">Nothing new.</p>
{:else}
	<ul class="-mx-1 flex max-h-96 flex-col overflow-y-auto">
		{#each items as item (item.key)}
			{@const Icon = item.kind === 'host_update' ? ServerCog : Sparkles}
			<li>
				<a
					href={item.href}
					data-notification={item.key}
					data-read={item.read ? 'true' : 'false'}
					class={cn(
						'hover:bg-muted focus-visible:outline-ring flex min-w-0 items-start gap-2 rounded-md px-2 py-2 focus-visible:outline-2',
						item.read ? 'text-muted-foreground' : 'text-foreground'
					)}
					onclick={(event) => {
						if (!onOpen || event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
						event.preventDefault();
						onOpen(item);
					}}
				>
					<span aria-hidden="true" class={cn('mt-1.5 size-2 shrink-0 rounded-full', item.read ? '' : 'bg-primary')}></span>
					<Icon class="mt-0.5 size-4 shrink-0" />
					<span class="flex min-w-0 flex-col">
						<span class={cn('truncate text-sm', item.read ? '' : 'font-medium')} title={item.title}>
							{#if !item.read}<span class="sr-only">Unread:</span>{/if}
							{item.title}
						</span>
						<span class="text-muted-foreground truncate text-xs" title={item.detail || undefined}>
							{#if item.detail}{item.detail} <span aria-hidden="true">·</span>{/if}
							<time datetime={item.at} title={formatWhen(item.at, zones?.current)}>{formatAgo(item.at)}</time>
						</span>
					</span>
				</a>
			</li>
		{/each}
	</ul>
{/if}
