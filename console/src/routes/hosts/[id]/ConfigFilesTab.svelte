<script lang="ts">
	import { browser } from '$app/environment';
	import { enhance } from '$app/forms';
	import { afterNavigate, invalidate, invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import type { SubmitFunction } from '@sveltejs/kit';
	import Eye from '@lucide/svelte/icons/eye';
	import FileDiffIcon from '@lucide/svelte/icons/file-diff';
	import FileText from '@lucide/svelte/icons/file-text';
	import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
	import Upload from '@lucide/svelte/icons/upload';
	import { onDestroy, tick, untrack } from 'svelte';
	import ConfirmAction from '$lib/components/ConfirmAction.svelte';
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import RowAction from '$lib/components/RowAction.svelte';
	import StatusBadge from '$lib/components/StatusBadge.svelte';
	import { Badge } from '$lib/components/ui/badge';
	import { Button, buttonVariants } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { formatBytes } from '$lib/format';
	import {
		APPLY_POLL_MS,
		APPLY_REREAD_MS,
		applyLabel,
		applyOutcomeMessage,
		applyProgressText,
		hostFileHref,
		hostFileOutcomeBadge,
		hostFileQuery,
		hostFilesForm,
		hostFileStatusBadge,
		latestVersion,
		stepWatches,
		versionText,
		WORKER_BUSY_TEXT,
		type ApplyWatch,
		type HostFile,
		type HostFileRun,
		type HostFileRunDone,
		type HostFilesTab,
		type HostFileVersion
	} from '$lib/host-files';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import FileDiff from './FileDiff.svelte';
	import PlainFileEditor from './PlainFileEditor.svelte';
	import SecretFileEditor from './SecretFileEditor.svelte';

	/**
	 * Host config files (api-v1 v1.44, spec-host-config-files §2.8). Everyone who sees the host gets the list;
	 * org owners and admins open a file: a plain file's editor, diff and versions, or a secret file's key names
	 * (never a value), and Apply. Apply answers within about 25 s; a longer one comes back `running` and the tab
	 * polls `/hosts/<id>/file-applies` every 3 s while visible until it ends.
	 */
	let { hostId, files, form }: { hostId: string; files: HostFilesTab; form: unknown } = $props();

	const FILES_ERROR_ID = 'host-files-error';
	const result = $derived(hostFilesForm(form));
	const selected = $derived(files.selected);
	const detail = $derived(selected?.detail ?? null);
	const file = $derived(selected?.file ?? null);
	const latest = $derived(file ? latestVersion(file.latest_version, detail?.versions ?? []) : null);
	const applied = $derived(detail?.versions.find((item) => item.applied)?.version ?? file?.applied_version ?? null);
	const pageMessage = $derived(result.message && !result.field && !result.apply?.busy ? result.message : null);

	/** The apply request this page is sending (until the action answers). */
	let pending = $state<{ name: string; version: number } | null>(null);
	/** Applies still running after the action answered, polled until they end. */
	let watches = $state<ApplyWatch[]>([]);
	/** The last apply outcome to show, per file. */
	let outcomes = $state<Record<string, HostFileRunDone>>({});
	let unsaved = $state(false);
	/** The Apply anyway confirm is posting: it stays mounted until its answer comes back. */
	let forcing = $state(false);

	function watchFor(run: HostFileRun) {
		if (watches.some((watch) => watch.name === run.name)) return;
		const item = files.items.find((entry) => entry.name === run.name);
		const since = Date.parse(run.startedAt);
		watches = [...watches, { name: run.name, version: run.version, since: Number.isFinite(since) ? since : Date.now(), lastApplyAt: item?.last_apply?.at ?? null }];
	}

	let rereadTimer: ReturnType<typeof setTimeout> | undefined;
	function settled(name: string, done: HostFileRunDone) {
		outcomes = { ...outcomes, [name]: done };
		// One more read a moment later: the host's status frame can land after the apply answer.
		clearTimeout(rereadTimer);
		if (browser) rereadTimer = setTimeout(() => void invalidate('host:files'), APPLY_REREAD_MS);
	}
	onDestroy(() => clearTimeout(rereadTimer));

	// A reload while an apply runs: the load lists it, so the progress note and the blocked Apply come back.
	// A run with an unknown result is watched too: the list's last apply settles it.
	$effect(() => {
		for (const run of files.runs) {
			if (!run.done) untrack(() => watchFor(run));
		}
	});

	// Each action answer: an outcome, a busy host, or a run still going.
	let lastForm: unknown = untrack(() => form);
	$effect(() => {
		const current = form;
		if (current === lastForm) return;
		lastForm = current;
		const answer = hostFilesForm(current);
		untrack(() => {
			if (answer.running && !answer.running.done) watchFor(answer.running);
			else if (answer.apply) settled(answer.apply.name, { apply: answer.apply, message: answer.message });
		});
	});

	// The poll: every 3 s while the tab is visible and an apply is watched.
	$effect(() => {
		if (!browser || watches.length === 0 || !files.orgId) return;
		const org = files.orgId;
		let stopped = false;
		const poll = async () => {
			if (document.visibilityState === 'hidden') return;
			let runs: HostFileRun[] | null = null;
			try {
				const response = await fetch(`/hosts/${hostId}/file-applies?org=${org}`, { headers: { accept: 'application/json' } });
				if (response.ok) runs = ((await response.json()) as { runs: HostFileRun[] }).runs;
			} catch {
				runs = null;
			}
			if (stopped) return;
			const step = stepWatches(untrack(() => watches), runs, untrack(() => files.items), Date.now());
			watches = step.watches;
			for (const outcome of step.outcomes) settled(outcome.name, outcome.done);
			// An ended run, or one the console lost (restart): read the list for its last apply.
			if (step.outcomes.length > 0 || step.lost) await invalidate('host:files');
		};
		const timer = setInterval(() => void poll(), APPLY_POLL_MS);
		return () => {
			stopped = true;
			clearInterval(timer);
		};
	});

	function running(name: string): boolean {
		return (
			pending?.name === name ||
			watches.some((watch) => watch.name === name) ||
			files.runs.some((run) => run.name === name && !run.done && !run.unknown)
		);
	}
	const busyHere = $derived(file ? running(file.name) : false);
	/** The outcome to show: one this page saw end, else the action answer the page holds (also on the first render). */
	const shownOutcome = $derived.by((): HostFileRunDone | null => {
		if (!file) return null;
		if (outcomes[file.name]) return outcomes[file.name];
		return result.apply?.name === file.name ? { apply: result.apply, message: result.message } : null;
	});
	/** The host answered worker_busy for this file: the version Apply anyway would send. */
	const busyVersion = $derived(shownOutcome?.apply?.busy ? shownOutcome.apply.version : null);

	function applySubmit(name: string, version: number): SubmitFunction {
		return ({ cancel }) => {
			// The buttons stay focusable (aria-disabled) while an apply runs, so a second click stops here.
			if (running(name)) {
				cancel();
				return;
			}
			pending = { name, version };
			const { [name]: _cleared, ...rest } = outcomes;
			outcomes = rest;
			return async ({ result: answer, update }) => {
				try {
					await update({ reset: false });
					if (answer.type === 'failure') await invalidateAll();
				} finally {
					pending = null;
				}
			};
		};
	}

	// View, Diff and Open keep the scroll position (data-sveltekit-noscroll); focus moves to what they opened.
	afterNavigate((navigation) => {
		if (navigation.type !== 'link' && navigation.type !== 'goto') return;
		const params = navigation.to?.url.searchParams;
		if (!params || params.get('tab') !== 'files' || !params.get('files.name')) return;
		const target = params.get('files.v') ? 'host-file-view-heading' : 'host-file-heading';
		void tick().then(() => {
			const element = document.getElementById(target);
			element?.focus({ preventScroll: true });
			element?.scrollIntoView({ block: 'nearest' });
		});
	});

	const listColumns = $derived<Column<HostFile>[]>([
		{ key: 'name', label: 'Name', sortable: true, searchable: true, value: (row) => row.name },
		{ key: 'path', label: 'Path', sortable: true, searchable: true, value: (row) => row.path },
		{ key: 'unit', label: 'Unit', sortable: true, searchable: true, hideBelowSm: true, value: (row) => row.unit },
		{ key: 'applied', label: 'Applied', numeric: true, sortable: true, value: (row) => row.applied_version },
		{ key: 'status', label: 'On host', sortable: true, value: (row) => hostFileStatusBadge(row.status).text },
		{ key: 'last', label: 'Last apply', sortable: true, hideBelowSm: true, value: (row) => row.last_apply?.at ?? null },
		...(files.canEdit ? [{ key: 'actions', label: 'Actions', srLabel: true }] : [])
	]);

	const versionColumns: Column<HostFileVersion>[] = [
		{ key: 'version', label: 'Version', numeric: true, sortable: true, firstDir: 'desc', value: (row) => row.version },
		{ key: 'size', label: 'Size', numeric: true, sortable: true, value: (row) => row.size },
		{ key: 'at', label: 'Saved', sortable: true, value: (row) => row.at },
		{ key: 'by', label: 'By', hideBelowSm: true, value: (row) => row.by },
		{ key: 'actions', label: 'Actions', srLabel: true }
	];
</script>

{#snippet linkAction(href: string, label: string, Icon: typeof Eye)}
	<RowAction {label} icon={Icon}>
		{#snippet trigger(props)}
			<a {...props} {href} data-sveltekit-noscroll><Icon class="size-4" /></a>
		{/snippet}
	</RowAction>
{/snippet}

<h2 id="host-files-heading" class="mb-1 text-base font-medium">Config files</h2>
<p class="text-muted-foreground mb-3 text-sm">
	Files this host's runner.toml lets Deploybot manage. Apply checks the file, restarts its unit and confirms it is healthy; a
	failed health check puts the previous file back.
</p>

{#if !files.available}
	<Notice kind="info">Config files are not available for this host: it does not belong to an organization.</Notice>
{:else if files.message}
	<Notice kind="error">{files.message}</Notice>
{:else if files.items.length === 0}
	<p class="text-muted-foreground text-sm">
		This host declares no config files. Its runner.toml has no host_files lines, or its bot-agent is older than the release
		that supports them.
	</p>
{:else}
	<DataTable
		rows={files.items}
		columns={listColumns}
		id="files"
		labelledBy="host-files-heading"
		empty="No matching files."
		rowKey={(row) => row.name}
		defaultSort="name"
		searchLabel="Search name, path, or unit"
	>
		{#snippet row(row)}
			{@const badge = hostFileStatusBadge(row.status)}
			<tr data-file={row.name}>
				<td class="p-2 align-middle font-mono">{row.name}{#if row.secret}<Badge variant="outline" class="ml-2">secret</Badge>{/if}</td>
				<td class="p-2 align-middle font-mono text-xs">{row.path}</td>
				<td class="p-2 align-middle font-mono text-xs max-sm:hidden">{row.unit}</td>
				<td class="p-2 text-right align-middle font-mono">{versionText(row.applied_version)}</td>
				<td class="p-2 align-middle"><StatusBadge status={badge.status} text={badge.text} /></td>
				<td class="p-2 align-middle max-sm:hidden">
					{#if row.last_apply}
						{@const outcome = hostFileOutcomeBadge(row.last_apply.outcome)}
						<span class="inline-flex flex-wrap items-center gap-1.5">
							<StatusBadge status={outcome.status} text={outcome.text} />
							<span class="text-muted-foreground text-xs">v{row.last_apply.version}, <LocalTime iso={row.last_apply.at} /></span>
						</span>
					{:else}
						<span class="text-muted-foreground">none</span>
					{/if}
				</td>
				{#if files.canEdit}
					<td class="p-2 text-right align-middle">{@render linkAction(hostFileHref(hostId, row.name), `Open ${row.name}`, FileText)}</td>
				{/if}
			</tr>
		{/snippet}
	</DataTable>
	{#if files.notFound}
		<Notice kind="info">This host has no config file by that name.</Notice>
	{/if}
{/if}

{#if file && selected}
	<section class="mt-6 grid gap-4" aria-labelledby="host-file-heading" data-host-file={file.name} data-secret={file.secret ? 'true' : 'false'}>
		<div>
			<h3 id="host-file-heading" tabindex="-1" class="text-base font-medium outline-none"><span class="font-mono">{file.name}</span></h3>
			<p class="text-muted-foreground text-sm">
				<span class="font-mono">{file.path}</span>, unit <span class="font-mono">{file.unit}</span>. Applied {versionText(applied)}, latest
				{versionText(latest)}.
			</p>
		</div>
		<div id={FILES_ERROR_ID} tabindex="-1" class="outline-none">
			{#if selected.message}
				<Notice kind="error">{selected.message}</Notice>
			{/if}
			{#if pageMessage}
				<Notice kind="error">{pageMessage}</Notice>
			{/if}
			{#if result.saved && result.saved.name === file.name && !pageMessage}
				<Notice kind="success">Saved v{result.saved.version}. Apply it to put it on the host.</Notice>
			{/if}
		</div>

		<!-- One live region for the apply: its progress, then its outcome. -->
		<div aria-live="polite" aria-atomic="true" data-apply-status>
			{#if busyHere}
				<Notice kind="info">{applyProgressText(file.unit)}</Notice>
			{:else if shownOutcome?.apply?.busy}
				<Notice kind="info">{WORKER_BUSY_TEXT}</Notice>
			{:else if shownOutcome?.apply?.result}
				{@const outcome = applyOutcomeMessage(shownOutcome.apply.version, shownOutcome.apply.result)}
				<Notice kind={outcome.kind}>
					<span data-apply-outcome={shownOutcome.apply.result.outcome}
						>{#each outcome.parts as part, index (index)}{#if part.mono}<code class="font-mono">{part.text}</code>{:else}{part.text}{/if}{/each}</span
					>
				</Notice>
			{:else if shownOutcome?.message}
				<Notice kind="error">{shownOutcome.message}</Notice>
			{/if}
		</div>
		<!-- Outside the live region's branches, so it does not vanish while the note changes. -->
		{#if busyVersion != null && (!busyHere || forcing)}
			<div>
				<ConfirmAction
					closeOnFailure
					failureFocusId={FILES_ERROR_ID}
					id="apply-anyway-{file.name}"
					action={actionUrlFrom(page.url, 'applyFile')}
					label="Apply anyway"
					confirmLabel="Apply now"
					message="Apply v{busyVersion} of {file.name} now? The program on this host is running a job; restarting {file.unit} interrupts it."
					hidden={[
						{ name: 'org', value: files.orgId ?? '' },
						{ name: 'name', value: file.name },
						{ name: 'version', value: String(busyVersion) },
						{ name: 'force', value: '1' }
					]}
					icon={Upload}
					destructive
					onSubmitting={(active) => {
						forcing = active;
						if (active) {
							pending = { name: file.name, version: busyVersion };
							const { [file.name]: _cleared, ...rest } = outcomes;
							outcomes = rest;
						} else pending = null;
					}}
				/>
			</div>
		{/if}

		<!-- One editor per file: opening another file starts a fresh one, so a draft or staged keys never carry over. -->
		{#key file.name}
		{#if detail && !detail.secret}
			<PlainFileEditor
				{hostId}
				orgId={files.orgId ?? ''}
				name={file.name}
				content={detail.content}
				{latest}
				{applied}
				appliedContent={selected.appliedContent}
				failedContent={result.content}
				onDraft={(value) => (unsaved = value)}
			/>
		{:else if detail && detail.secret}
			<SecretFileEditor
				orgId={files.orgId ?? ''}
				name={file.name}
				keys={detail.keys}
				{latest}
				message={result.field === 'secret' ? result.message : null}
				onStaged={(value) => (unsaved = value)}
			/>
		{/if}
		{/key}

		{#if detail && latest != null}
			<div class="flex flex-wrap items-center gap-2">
				<form method="POST" action={actionUrlFrom(page.url, 'applyFile')} use:enhance={applySubmit(file.name, latest)}>
					<Input type="hidden" name="org" value={files.orgId ?? ''} />
					<Input type="hidden" name="name" value={file.name} />
					<Input type="hidden" name="version" value={String(latest)} />
					<Button type="submit" aria-disabled={busyHere ? 'true' : undefined} class={busyHere ? 'opacity-50' : undefined}
						><Upload class="size-4" />{applyLabel(latest, latest)}</Button
					>
				</form>
				{#if unsaved}
					<span class="text-muted-foreground text-sm">Unsaved changes: Apply sends the latest saved version (v{latest}). Save first to apply them.</span>
				{/if}
			</div>
		{/if}

		{#if selected.view && detail && !detail.secret}
			{@const view = selected.view}
			<section aria-labelledby="host-file-view-heading" class="grid gap-2">
				<div class="flex flex-wrap items-center justify-between gap-2">
					<h4 id="host-file-view-heading" tabindex="-1" class="text-sm font-medium outline-none">
						{view.mode === 'diff' ? `v${view.version} against the applied version (${versionText(applied)})` : `v${view.version}`}
					</h4>
					<a class={buttonVariants({ variant: 'outline', size: 'sm' })} href={hostFileHref(hostId, file.name)} data-sveltekit-noscroll>Close</a>
				</div>
				{#if view.mode === 'diff'}
					<FileDiff before={selected.appliedContent ?? ''} after={view.content} label="v{view.version} against the applied version" />
				{:else}
					<pre class="bg-muted max-h-96 overflow-auto rounded-lg border p-2 font-mono text-xs leading-5" data-file-view>{view.content}</pre>
				{/if}
			</section>
		{/if}

		{#if detail && detail.versions.length > 0}
			<h4 id="host-file-versions-heading" class="text-sm font-medium">Versions</h4>
			<DataTable
				rows={detail.versions}
				columns={versionColumns}
				id="fileversions"
				labelledBy="host-file-versions-heading"
				empty="No versions."
				rowKey={(row) => row.version}
				defaultSort="version"
				defaultDir="desc"
			>
				{#snippet row(row)}
					<tr data-version={row.version}>
						<td class="p-2 text-right align-middle font-mono">
							<span class="inline-flex items-center gap-1.5">
								v{row.version}
								{#if row.applied}<Badge variant="outline">applied</Badge>{/if}
								{#if row.version === latest}<Badge variant="outline">latest</Badge>{/if}
							</span>
						</td>
						<td class="p-2 text-right align-middle font-mono">{formatBytes(row.size)}</td>
						<td class="p-2 align-middle"><LocalTime iso={row.at} /></td>
						<td class="p-2 align-middle font-mono text-xs max-sm:hidden" title={row.by}>{row.by.slice(0, 8)}</td>
						<td class="p-2 text-right align-middle">
							<span class="inline-flex items-center gap-1">
								{#if !detail.secret}
									{@render linkAction(hostFileQuery(hostId, file.name, row.version, 'view'), `View v${row.version}`, Eye)}
									{@render linkAction(hostFileQuery(hostId, file.name, row.version, 'diff'), `Diff v${row.version}`, FileDiffIcon)}
								{/if}
								<form method="POST" action={actionUrlFrom(page.url, 'applyFile')} use:enhance={applySubmit(file.name, row.version)}>
									<Input type="hidden" name="org" value={files.orgId ?? ''} />
									<Input type="hidden" name="name" value={file.name} />
									<Input type="hidden" name="version" value={String(row.version)} />
									<RowAction
										type="submit"
										label={applyLabel(row.version, latest)}
										icon={latest != null && row.version < latest ? RotateCcw : Upload}
										blockedReason={busyHere ? 'An apply of this file is running.' : null}
									/>
								</form>
							</span>
						</td>
					</tr>
				{/snippet}
			</DataTable>
		{/if}
	</section>
{/if}
