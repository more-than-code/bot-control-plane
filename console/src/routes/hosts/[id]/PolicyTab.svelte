<script lang="ts">
	import { browser } from '$app/environment';
	import { invalidate } from '$app/navigation';
	import { page } from '$app/state';
	import { untrack } from 'svelte';
	import Plus from '@lucide/svelte/icons/plus';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import Undo2 from '@lucide/svelte/icons/undo-2';
	import X from '@lucide/svelte/icons/x';
	import CommandBlock from '$lib/components/CommandBlock.svelte';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';
	import {
		applyBadge,
		applyStateText,
		changeIsEmpty,
		changeLines,
		CONSOLE_POLICY_LINE,
		imagePatternProblem,
		POLICY_POLL_MS,
		policyDiff,
		projectProblem,
		stagedFromUrl,
		type HostPolicyTab,
		type PolicyApply,
		type PolicyValues
	} from '$lib/host-policy';
	import { actionUrlFrom } from '$lib/url';

	/**
	 * Policy (api-v1 v1.49): the part of runner.toml org owners and admins may change on a host whose root set
	 * console_policy = true. A deep link stages a change (policy.edge, policy.add_image, policy.add_project); nothing
	 * applies until the person confirms Apply. The host checks the new file, restarts its agent and puts the old file
	 * back if the agent does not reconnect.
	 */
	let {
		hostName,
		tab,
		formMessage = null,
		formApply = null
	}: { hostName: string; tab: HostPolicyTab; formMessage?: string | null; formApply?: PolicyApply | null } = $props();

	const POLICY_ERROR_ID = 'policy-error';
	const policy = $derived(tab.policy);
	const base: PolicyValues = $derived(policy ? { edge: policy.edge, images: policy.images, projects: policy.projects } : { edge: false, images: [], projects: [] });

	// The staged values start from the host's values plus what the link asks for, and start again whenever the
	// host reports a new policy.
	const initial = untrack(() => stagedFromUrl(page.url.searchParams, base));
	let staged: PolicyValues = $state(initial.values);
	let dropped = $state(initial.dropped);
	let stagedFor: string | null = untrack(() => policy?.policy_hash ?? null);
	$effect(() => {
		const key = policy?.policy_hash ?? null;
		if (key === stagedFor) return;
		stagedFor = key;
		const from = untrack(() => stagedFromUrl(page.url.searchParams, base));
		staged = from.values;
		dropped = from.dropped;
	});

	const apply = $derived(policy?.apply ?? formApply);
	const running = $derived(apply?.state === 'running');
	const change = $derived(policyDiff(base, staged));
	const lines = $derived(changeLines(change));
	const empty = $derived(changeIsEmpty(change));

	let newImage = $state('');
	let imageProblem: string | null = $state(null);
	let newProject = $state('');
	let projectError: string | null = $state(null);

	function addImage() {
		const value = newImage.trim();
		imageProblem = imagePatternProblem(value, staged.images);
		if (imageProblem) return;
		staged.images = [...staged.images, value];
		newImage = '';
	}

	function addProject() {
		const value = newProject.trim();
		projectError = projectProblem(value, staged.projects);
		if (projectError) return;
		staged.projects = [...staged.projects, value];
		newProject = '';
	}

	function reset() {
		staged = { edge: base.edge, images: [...base.images], projects: [...base.projects] };
		dropped = 0;
	}

	const hidden = $derived([
		{ name: 'base_hash', value: policy?.policy_hash ?? '' },
		{ name: 'edge', value: staged.edge ? '1' : '0' },
		...staged.images.map((value) => ({ name: 'image', value })),
		...staged.projects.map((value) => ({ name: 'project', value }))
	]);

	// While an apply runs, re-read the policy every few seconds (visible tab only).
	$effect(() => {
		if (!browser || !running) return;
		let timer: ReturnType<typeof setInterval> | undefined;
		const start = () => {
			if (!timer) timer = setInterval(() => void invalidate('host:policy'), POLICY_POLL_MS);
		};
		const stop = () => {
			if (timer) clearInterval(timer);
			timer = undefined;
		};
		const onVisibility = () => (document.visibilityState === 'hidden' ? stop() : start());
		if (document.visibilityState !== 'hidden') start();
		document.addEventListener('visibilitychange', onVisibility);
		return () => {
			stop();
			document.removeEventListener('visibilitychange', onVisibility);
		};
	});
</script>

<section class="mt-6 grid min-w-0 gap-4" aria-labelledby="policy-heading" data-policy-tab>
	<h2 id="policy-heading" class="text-lg font-semibold tracking-tight">Host policy</h2>
	<p class="text-muted-foreground text-sm">
		The host's /etc/bot-agent/runner.toml decides what Deploybot may do here. Org owners and admins can change three of its settings: the edge, the images the host may run and the projects that may deploy to it.
	</p>
	{#if formMessage}
		<div id={POLICY_ERROR_ID} tabindex="-1" class="max-w-full min-w-0 rounded-md break-words focus-visible:ring-3 focus-visible:ring-ring">
			<Notice kind="error">{formMessage}</Notice>
		</div>
	{/if}
	{#if tab.message}
		<Notice kind="error">{tab.message}</Notice>
	{:else if policy && (!policy.managed || !policy.policy_hash)}
		<div class="grid min-w-0 gap-2" data-policy-unmanaged>
			<p class="text-sm">
				{hostName} does not let the console change its policy yet. Its root adds this line to /etc/bot-agent/runner.toml once and restarts bot-agent (version 0.2.15 or later):
			</p>
			<CommandBlock command={CONSOLE_POLICY_LINE} label="Copy runner.toml line" ariaLabel="runner.toml line" />
			<p class="text-muted-foreground text-sm">Every other line of the file stays the root's own; the console never changes it.</p>
		</div>
	{:else if policy}
		{#if apply}
			{@const badge = applyBadge(apply.state)}
			<div class="grid min-w-0 gap-1 rounded-md border p-3" data-policy-apply>
				<div class="flex flex-wrap items-center gap-2">
					<span class="text-sm font-medium">Last change</span>
					<StatusBadge status={badge.status} text={badge.text} />
					<span class="text-muted-foreground text-sm">by {apply.started_by.name}, <LocalTime iso={apply.started_at} /></span>
				</div>
				<p class="text-sm">{applyStateText(apply.state)}</p>
				{#if changeLines(apply.change).length > 0}
					<ul class="text-muted-foreground list-disc pl-5 text-sm">
						{#each changeLines(apply.change) as line (line)}<li>{line}</li>{/each}
					</ul>
				{/if}
			</div>
		{/if}
		{#if dropped > 0}
			<Notice kind="info">The link asked for {dropped} {dropped === 1 ? 'change that is' : 'changes that are'} not valid; {dropped === 1 ? 'it was' : 'they were'} left out.</Notice>
		{/if}

		<fieldset class="grid min-w-0 gap-4" disabled={running}>
			<div class="flex flex-wrap items-center gap-3">
				<Switch id="policy-edge" bind:checked={staged.edge} disabled={running} aria-describedby="policy-edge-help" />
				<Label for="policy-edge">Allow the edge</Label>
				<span id="policy-edge-help" class="text-muted-foreground text-sm">Lets Deploybot run its Caddy on ports 80 and 443. Turn the edge itself on in the Edge section below.</span>
			</div>

			<div class="grid min-w-0 gap-2">
				<h3 class="text-sm font-medium">Images this host may run</h3>
				{#if staged.images.length === 0}
					<p class="text-muted-foreground text-sm">None yet: the host runs no catalog image.</p>
				{:else}
					<ul class="grid gap-1" data-policy-images>
						{#each staged.images as image (image)}
							<li class="flex min-w-0 items-center gap-2">
								<span class="min-w-0 font-mono text-sm break-all">{image}</span>
								{#if !base.images.includes(image)}<span class="text-muted-foreground text-xs">new</span>{/if}
								<RowAction label="Remove {image}" icon={X} destructive disabled={running} onclick={() => (staged.images = staged.images.filter((x) => x !== image))} />
							</li>
						{/each}
					</ul>
				{/if}
				<form class="flex min-w-0 flex-wrap items-start gap-2" onsubmit={(e) => { e.preventDefault(); addImage(); }}>
					<div class="grid min-w-0 flex-1 gap-1">
						<Label for="policy-add-image" class="sr-only">Image pattern</Label>
						<Input id="policy-add-image" bind:value={newImage} placeholder="ghcr.io/acme/*" autocomplete="off" aria-invalid={imageProblem ? 'true' : undefined} aria-describedby={imageProblem ? 'policy-image-problem' : undefined} />
						{#if imageProblem}<p id="policy-image-problem" class="text-destructive text-sm">{imageProblem}</p>{/if}
					</div>
					<Button type="submit" variant="outline"><Plus class="size-4" />Add image</Button>
				</form>
			</div>

			<div class="grid min-w-0 gap-2">
				<h3 class="text-sm font-medium">Projects that may deploy here</h3>
				{#if staged.projects.length === 0}
					<p class="text-muted-foreground text-sm">No list: every project with deploy access may deploy.</p>
				{:else}
					<ul class="grid gap-1" data-policy-projects>
						{#each staged.projects as project (project)}
							<li class="flex min-w-0 items-center gap-2">
								<span class="min-w-0 font-mono text-sm break-all">{project}</span>
								{#if !base.projects.includes(project)}<span class="text-muted-foreground text-xs">new</span>{/if}
								<RowAction label="Remove {project}" icon={X} destructive disabled={running} onclick={() => (staged.projects = staged.projects.filter((x) => x !== project))} />
							</li>
						{/each}
					</ul>
				{/if}
				<form class="flex min-w-0 flex-wrap items-start gap-2" onsubmit={(e) => { e.preventDefault(); addProject(); }}>
					<div class="grid min-w-0 flex-1 gap-1">
						<Label for="policy-add-project" class="sr-only">Project slug</Label>
						<Input id="policy-add-project" bind:value={newProject} placeholder="project-slug" autocomplete="off" aria-invalid={projectError ? 'true' : undefined} aria-describedby={projectError ? 'policy-project-problem' : undefined} />
						{#if projectError}<p id="policy-project-problem" class="text-destructive text-sm">{projectError}</p>{/if}
					</div>
					<Button type="submit" variant="outline"><Plus class="size-4" />Add project</Button>
				</form>
			</div>
		</fieldset>

		<div class="grid min-w-0 gap-2 rounded-md border p-3" data-policy-diff>
			<h3 class="text-sm font-medium">Change</h3>
			{#if empty}
				<p class="text-muted-foreground text-sm">Nothing changed yet.</p>
			{:else}
				<ul class="list-disc pl-5 text-sm">
					{#each lines as line (line)}<li>{line}</li>{/each}
				</ul>
			{/if}
			<div class="flex flex-wrap gap-2">
				<ConfirmAction
					id="policy-apply"
					action={actionUrlFrom(page.url, 'applyPolicy')}
					label="Apply"
					confirmLabel="Apply and restart agent"
					icon={ShieldCheck}
					destructive={false}
					disabled={empty || running}
					closeOnFailure
					failureFocusId={POLICY_ERROR_ID}
					{hidden}
					message="Change the policy of {hostName}? {lines.join('. ')}. The host checks the new runner.toml, restarts bot-agent (its services keep running) and puts the old file back if the agent does not reconnect within 90 seconds."
				/>
				<Button variant="ghost" disabled={empty || running} onclick={reset}><Undo2 class="size-4" />Discard</Button>
			</div>
		</div>
	{/if}
</section>
