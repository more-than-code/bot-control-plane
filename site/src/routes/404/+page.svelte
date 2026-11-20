<script lang="ts">
	// Emitted as build/404.html. Cloudflare Pages serves it with status 404 for every unknown
	// path and keeps the URL (spec-site-pages §3.2). No robots meta: a 404 is never indexed.
	import SiteError from '$lib/site/SiteError.svelte';
</script>

<SiteError status={404} />
