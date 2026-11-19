<script lang="ts">
	import { enhance } from '$app/forms';
	import { clearOpenStorage, visitStorage } from '$lib/catalog-open';
	import { page } from '$app/state';
	import { onMount, untrack } from 'svelte';
	import AppWindow from '@lucide/svelte/icons/app-window';
	import Inbox from '@lucide/svelte/icons/inbox';
	import LogOut from '@lucide/svelte/icons/log-out';
	import Sparkles from '@lucide/svelte/icons/sparkles';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
	import { Toaster } from '$lib/components/ui/sonner';
	import HeaderNav from '$lib/components/HeaderNav.svelte';
	import NotificationsBell from '$lib/components/NotificationsBell.svelte';
	import ThemeSwitch from '$lib/components/ThemeSwitch.svelte';
	import { avatarInitial } from '$lib/avatar';
	import { browserTimeZone, provideTimeZone, rememberTimeZone } from '$lib/time-zone';
	import { cn } from '$lib/utils';
	import { SITE_URL } from '$lib/site/links';
	import '../app.css';

	let { children, data } = $props();

	// A broken or blocked avatar image falls back to the initial.
	let avatarFailed = $state(false);

	// The server and the first client render share this value. After mount the
	// browser replaces it when the viewer is in a different zone.
	let timeZone = $state(untrack(() => data.timeZone));
	provideTimeZone({
		get current() {
			return timeZone;
		}
	});

	onMount(() => {
		const local = browserTimeZone();
		if (!local || local === timeZone) return;
		timeZone = local;
		rememberTimeZone(local);
	});

	// The public site pages ((site) group) bring their own layout, not the app shell.
	const sitePage = $derived(page.route.id?.startsWith('/(site)') ?? false);
	// Same link style as the sign-in line and the public site footer.
	const footerLink =
		'rounded-sm underline underline-offset-4 hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring';
	// The host page names its org in its own data; the header's Org ▾ follows it there.
	const pageOrgId = $derived(
		page.route.id === '/hosts/[id]' ? ((page.data as { org?: { id?: string } | null }).org?.id ?? null) : null
	);
</script>

{#if sitePage}
	{@render children()}
{:else}
	<a
		class="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-background focus:px-3 focus:py-2"
		href="#content">Skip to content</a
	>
	<div class="mx-auto w-full min-w-0 max-w-6xl px-4">
		<header class="flex flex-col gap-y-2 py-3 sm:flex-row sm:items-center sm:gap-x-3">
			<div class="flex w-full min-w-0 flex-wrap items-center gap-x-3 gap-y-2 sm:contents">
				<!-- Below sm: logo, project menu and bell stay on one row; only the nav shrinks. -->
				<div data-header-main class="flex w-full min-w-0 flex-nowrap items-center gap-x-3 sm:contents">
					<a class="shrink-0" href="/projects" aria-label="Deploybot">
						<img src="/brand/lockup-light.svg" alt="" class="h-6 w-auto dark:hidden" />
						<img src="/brand/lockup-dark.svg" alt="" class="hidden h-6 w-auto dark:block" />
					</a>
					{#if data.user}
						<HeaderNav orgs={data.orgs ?? []} projects={data.projects ?? []} {pageOrgId} />
						<div class="ml-auto shrink-0 sm:order-1">
							<NotificationsBell />
						</div>
					{/if}
				</div>
				{#if data.user}
					<div class="flex min-w-0 max-w-full items-center gap-2 sm:order-2">
						<DropdownMenu.Root>
							<DropdownMenu.Trigger
								class={cn(
									buttonVariants({ variant: 'ghost', size: 'sm' }),
									'max-w-full min-w-0 shrink overflow-hidden'
								)}
							>
								{#if data.user.avatarUrl && !avatarFailed}
									<img
										src={data.user.avatarUrl}
										alt=""
										class="size-6 shrink-0 rounded-full"
										referrerpolicy="no-referrer"
										onerror={() => (avatarFailed = true)}
									/>
								{:else}
									<span
										aria-hidden="true"
										class="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium text-muted-foreground"
										>{avatarInitial(data.user.name)}</span
									>
								{/if}
								<span class="min-w-0 truncate">{data.user.name || 'Account'}</span>
							</DropdownMenu.Trigger>
							<!-- The content would otherwise take the trigger's width (a short name), wrapping and clipping the items. -->
							<DropdownMenu.Content align="end" class="w-auto min-w-56 max-w-[calc(100vw-2rem)]">
								<DropdownMenu.Item>
									{#snippet child({ props })}
										<a href="/account/apps" {...props}><AppWindow class="size-4" />Connected apps</a>
									{/snippet}
								</DropdownMenu.Item>
								<DropdownMenu.Item>
									{#snippet child({ props })}
										<a href="/requests" {...props}><Inbox class="size-4" />Open requests</a>
									{/snippet}
								</DropdownMenu.Item>
								{#if data.whatsNew}
									<DropdownMenu.Item>
										{#snippet child({ props })}
											<a href="/whats-new" {...props}
												><Sparkles class="size-4" /><span class="min-w-0 truncate">What's new</span>
												{#if data.whatsNew?.version}<span data-whats-new-meta class="text-muted-foreground ml-auto shrink-0 pl-2 font-mono text-xs">v{data.whatsNew.version}</span>{/if}</a
											>
										{/snippet}
									</DropdownMenu.Item>
								{/if}
							</DropdownMenu.Content>
						</DropdownMenu.Root>
						<!-- Signing out also forgets which catalog entries were open in this tab. -->
						<form class="shrink-0" method="POST" action="/signout" use:enhance onsubmit={() => clearOpenStorage(visitStorage())}>
							<Button type="submit" variant="outline" size="sm"><LogOut class="size-4" />Sign out</Button>
						</form>
					</div>
				{/if}
			</div>
			<!-- Signed out there is no bell, so the theme switch takes the right edge itself. -->
			<div class={cn('sm:order-1', !data.user && 'sm:ml-auto')}>
				<ThemeSwitch value={data.theme} />
			</div>
		</header>
		<main id="content" class="min-w-0 pb-10">
			{@render children()}
		</main>
		<footer class="text-muted-foreground border-t py-4 text-sm">
			<nav class="flex flex-wrap gap-x-4 gap-y-1" aria-label="Legal">
				<a class={footerLink} href="{SITE_URL}/privacy">Privacy</a>
				<a class={footerLink} href="{SITE_URL}/terms">Terms</a>
			</nav>
		</footer>
	</div>
	<!-- Success confirmations only. Errors stay inline next to what failed. -->
	<Toaster position="bottom-center" />
{/if}
