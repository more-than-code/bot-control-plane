import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// Not the console's 5180 or the API/MCP ports. Local preview does not apply _redirects or _headers.
export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: { host: '127.0.0.1', port: 5190, strictPort: true },
	preview: { host: '127.0.0.1', port: 5190, strictPort: true }
});
