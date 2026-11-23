<script lang="ts">
	import CircleArrowUp from '@lucide/svelte/icons/circle-arrow-up';
	import Cpu from '@lucide/svelte/icons/cpu';
	import FileWarning from '@lucide/svelte/icons/file-warning';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import ServerCog from '@lucide/svelte/icons/server-cog';
	import ServerOff from '@lucide/svelte/icons/server-off';
	import Sparkles from '@lucide/svelte/icons/sparkles';
	import type { Notification } from '$lib/api-types';
	import { formatAgo, formatWhen } from '$lib/format';
	import { needsAction, NOTIFICATIONS_ERROR, problemAge, problemKindOf } from '$lib/notifications';
	import { timeZoneSource } from '$lib/time-zone';
	import { cn } from '$lib/utils';

	/**
	 * The bell popover's rows (spec-header-org-project-bell §3.2) in the API's order: open problems first, then the
	 * rest newest first. A problem row (api-v1 v1.41) has an icon by what broke (host from its href, registry login,
	 * deploy, a host config file since v1.44), its age ("open 25 min", "resolved 3 h ago"), and is muted once resolved.
	 */
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

	const PROBLEM_ICONS = {
		host_offline: ServerOff,
		registry_login: KeyRound,
		deploy: RotateCcw,
		host_file: FileWarning,
		agent_upgrade: CircleArrowUp,
		host_resource: Cpu
	};

	function iconOf(item: Notification) {
		const problem = problemKindOf(item);
		if (problem) return PROBLEM_ICONS[problem];
		return item.kind === 'host_update' ? ServerCog : Sparkles;
	}
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
			{@const Icon = iconOf(item)}
			{@const age = problemAge(item)}
			{@const urgent = needsAction(item)}
			<li>
				<a
					href={item.href}
					data-notification={item.key}
					data-read={item.read ? 'true' : 'false'}
					data-state={item.state}
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
					<span
						aria-hidden="true"
						class={cn('mt-1.5 size-2 shrink-0 rounded-full', item.read ? '' : urgent ? 'bg-destructive' : 'bg-primary')}
					></span>
					<Icon class={cn('mt-0.5 size-4 shrink-0', urgent && 'text-destructive')} />
					<span class="flex min-w-0 flex-col">
						<span class={cn('truncate text-sm', item.read ? '' : 'font-medium')} title={item.title}>
							{#if !item.read}<span class="sr-only">Unread:</span>{/if}
							{item.title}
						</span>
						<span class="text-muted-foreground truncate text-xs" title={item.detail || undefined}>
							{#if urgent}<span class="sr-only">Needs action:</span>{/if}
							{#if item.detail}{item.detail} <span aria-hidden="true">·</span>{/if}
							<time datetime={item.at} title={formatWhen(item.at, zones?.current)}>{age ?? formatAgo(item.at)}</time>
						</span>
					</span>
				</a>
			</li>
		{/each}
	</ul>
{/if}
