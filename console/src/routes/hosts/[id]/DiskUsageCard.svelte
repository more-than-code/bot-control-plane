<script lang="ts">
	import { enhance } from '$app/forms';
	import { page } from '$app/state';
	import DataTable from '$lib/components/DataTable.svelte';
	import LocalTime from '$lib/components/LocalTime.svelte';
	import Notice from '$lib/components/Notice.svelte';
	import type { HostDisk } from '$lib/api-types';
	import {
		dockerSums,
		dockerTotalRows,
		mountsByUse,
		type DockerUsage,
		type DockerUsageContainer,
		type DockerUsageImage,
		type DockerUsageVolume
	} from '$lib/disk-usage';
	import { formatDecimalBytes, formatPercent } from '$lib/format';
	import type { Column } from '$lib/table';
	import { actionUrlFrom } from '$lib/url';
	import { Button } from '$lib/components/ui/button';
	import * as Card from '$lib/components/ui/card';
	import * as Tooltip from '$lib/components/ui/tooltip';

	/** What fills the host's disk (api-v1 v1.59): mount sizes for everyone, Docker usage for org owners and admins. */
	let {
		disks = [],
		canCheck = false,
		hostStatus = 'online',
		revoked = false,
		usage = null,
		message = null
	}: {
		disks?: HostDisk[];
		/** Org owner or admin of the host's org: the Check Docker usage button. The API decides. */
		canCheck?: boolean;
		/** The host's status: the button is disabled, with a tooltip, unless it is online. */
		hostStatus?: string;
		/** A revoked host gets no button. */
		revoked?: boolean;
		usage?: DockerUsage | null;
		message?: string | null;
	} = $props();

	let checking = $state(false);
	const offline = $derived(hostStatus !== 'online');
	const offlineNote = 'This host is offline; Docker usage needs it online.';

	const mounts = $derived(mountsByUse(disks));
	const totals = $derived(usage ? dockerTotalRows(usage) : []);
	const sums = $derived(usage ? dockerSums(usage) : null);

	type TotalRow = (typeof totals)[number];
	const mountColumns: Column<HostDisk>[] = [
		{ key: 'path', label: 'Mount' },
		{ key: 'used', label: 'Used', numeric: true },
		{ key: 'total', label: 'Size', numeric: true },
		{ key: 'used_percent', label: '%', numeric: true },
		{ key: 'at', label: 'As of', hideBelowSm: true }
	];
	const totalColumns: Column<TotalRow>[] = [
		{ key: 'label', label: 'Type' },
		{ key: 'count', label: 'Count', numeric: true },
		{ key: 'size', label: 'Size', numeric: true },
		{ key: 'reclaimable', label: 'Reclaimable', numeric: true }
	];
	const imageColumns: Column<DockerUsageImage>[] = [
		{ key: 'ref', label: 'Image' },
		{ key: 'size', label: 'Size', numeric: true },
		{ key: 'containers', label: 'Containers', numeric: true }
	];
	const volumeColumns: Column<DockerUsageVolume>[] = [
		{ key: 'name', label: 'Volume' },
		{ key: 'size', label: 'Size', numeric: true },
		{ key: 'in_use', label: 'In use' }
	];
	const containerColumns: Column<DockerUsageContainer>[] = [
		{ key: 'name', label: 'Container' },
		{ key: 'state', label: 'State' },
		{ key: 'size', label: 'Size', numeric: true }
	];
</script>

<Card.Root class="mb-4" data-disk-usage>
	<Card.Header>
		<Card.Title>Disk usage</Card.Title>
		<Card.Description>Each mount's newest sample from the last 24 hours.</Card.Description>
	</Card.Header>
	<Card.Content class="grid min-w-0 gap-3">
		<span id="disk-usage-mounts" class="sr-only">Mounts</span>
		<DataTable rows={mounts} columns={mountColumns} id="disk-usage-mounts" labelledBy="disk-usage-mounts" empty="No disk data" rowKey={(disk) => disk.path}>
			{#snippet row(disk)}
				<tr>
					<td class="max-w-64 truncate p-2 align-middle font-mono" title={disk.path}>{disk.path}</td>
					<td class="p-2 text-right align-middle">{formatDecimalBytes(disk.used)}</td>
					<td class="p-2 text-right align-middle">{formatDecimalBytes(disk.total)}</td>
					<td class="p-2 text-right align-middle">{formatPercent(disk.used_percent)}</td>
					<td class="p-2 align-middle max-sm:hidden"><LocalTime iso={disk.at ?? null} /></td>
				</tr>
			{/snippet}
		</DataTable>

		{#if canCheck && !revoked && offline}
			<Tooltip.Provider>
				<Tooltip.Root>
					<Tooltip.Trigger>
						{#snippet child({ props })}
							<!-- aria-disabled, not disabled: it stays focusable, so the tooltip shows on keyboard focus too. -->
							<Button
								{...props}
								type="button"
								variant="outline"
								class="w-max opacity-50"
								aria-disabled="true"
								aria-describedby="docker-usage-offline"
								data-docker-offline>Check Docker usage</Button
							>
						{/snippet}
					</Tooltip.Trigger>
					<Tooltip.Content>{offlineNote}</Tooltip.Content>
				</Tooltip.Root>
			</Tooltip.Provider>
			<span id="docker-usage-offline" class="sr-only">{offlineNote}</span>
		{:else if canCheck && !revoked}
			<form
				method="POST"
				action={actionUrlFrom(page.url, 'dockerUsage')}
				use:enhance={() => {
					checking = true;
					return async ({ update }) => {
						await update();
						checking = false;
					};
				}}
			>
				<Button type="submit" variant="outline" disabled={checking}>{checking ? 'Checking Docker usage…' : 'Check Docker usage'}</Button>
			</form>
		{/if}

		{#if message}
			<Notice kind="error">{message}</Notice>
		{/if}

		{#if usage && sums}
			<p class="text-sm">
				Docker uses {formatDecimalBytes(sums.size)}, of which {formatDecimalBytes(sums.reclaimable)} can be reclaimed. Checked <LocalTime iso={usage.at} />.
			</p>
			<span id="docker-usage-totals" class="sr-only">Docker totals</span>
			<DataTable rows={totals} columns={totalColumns} id="docker-usage-totals" labelledBy="docker-usage-totals" empty="No Docker data." rowKey={(t) => t.key}>
				{#snippet row(t)}
					<tr>
						<td class="p-2 align-middle">{t.label}</td>
						<td class="p-2 text-right font-mono align-middle">{t.count}</td>
						<td class="p-2 text-right align-middle">{formatDecimalBytes(t.size)}</td>
						<td class="p-2 text-right align-middle">{formatDecimalBytes(t.reclaimable)}</td>
					</tr>
				{/snippet}
			</DataTable>
			<span id="docker-usage-images" class="sr-only">Largest images</span>
			<DataTable rows={usage.images} columns={imageColumns} id="docker-usage-images" labelledBy="docker-usage-images" empty="No images." rowKey={(im) => `${im.ref} ${im.id}`}>
				{#snippet row(im)}
					<tr>
						<td class="max-w-64 truncate p-2 align-middle font-mono" title={im.ref}>{im.ref} <span class="text-muted-foreground">{im.id}</span></td>
						<td class="p-2 text-right align-middle">{formatDecimalBytes(im.size)}</td>
						<td class="p-2 text-right font-mono align-middle">{im.containers}</td>
					</tr>
				{/snippet}
			</DataTable>
			<span id="docker-usage-volumes" class="sr-only">Largest volumes</span>
			<DataTable rows={usage.volumes} columns={volumeColumns} id="docker-usage-volumes" labelledBy="docker-usage-volumes" empty="No volumes." rowKey={(v) => v.name}>
				{#snippet row(v)}
					<tr>
						<td class="max-w-64 truncate p-2 align-middle font-mono" title={v.name}>{v.name}</td>
						<td class="p-2 text-right align-middle">{formatDecimalBytes(v.size)}</td>
						<td class="p-2 align-middle">{v.in_use ? 'yes' : 'no'}</td>
					</tr>
				{/snippet}
			</DataTable>
			<span id="docker-usage-containers" class="sr-only">Largest containers</span>
			<DataTable rows={usage.containers} columns={containerColumns} id="docker-usage-containers" labelledBy="docker-usage-containers" empty="No containers." rowKey={(c) => c.name}>
				{#snippet row(c)}
					<tr>
						<td class="max-w-64 truncate p-2 align-middle font-mono" title={c.name}>{c.name}</td>
						<td class="p-2 align-middle">{c.state}</td>
						<td class="p-2 text-right align-middle">{formatDecimalBytes(c.size)}</td>
					</tr>
				{/snippet}
			</DataTable>
			<p class="text-muted-foreground text-sm" data-reclaim-note>
				Reclaim on the host with <code>docker image prune</code>, <code>docker builder prune</code> and <code>docker container prune</code>.
				Check a volume before removing it: it may hold data.
			</p>
		{/if}
	</Card.Content>
</Card.Root>
