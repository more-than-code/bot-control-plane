import adapter from '@sveltejs/adapter-node';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter(),
		// A release build with SOURCE_DATE_EPOCH (the container image, docs/containers.md)
		// gets a fixed app version name, so the same tag builds the same files. Without it
		// SvelteKit uses the build time, as before.
		...(process.env.SOURCE_DATE_EPOCH ? { version: { name: process.env.SOURCE_DATE_EPOCH } } : {})
	}
};

export default config;
