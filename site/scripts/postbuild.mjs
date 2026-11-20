// After `vite build`: make build/ plain HTML, CSS and assets (spec-site-pages §3.1, as in DD).
// With csr = false no page loads JavaScript, but SvelteKit still emits its client bundles,
// __data.json and version.json. They are build output only, so they are deleted here, with any
// directory left empty. tests/guard.build.ts checks that no .js file and no <script> remain.
import { readdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const build = fileURLToPath(new URL('../build', import.meta.url));

/** @param {string} dir */
function prune(dir) {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) {
			prune(path);
			if (readdirSync(path).length === 0) rmSync(path, { recursive: true });
		} else if (entry.name.endsWith('.js') || entry.name === '__data.json' || entry.name === 'version.json') {
			rmSync(path);
		}
	}
}

prune(build);
console.log('site postbuild: pruned client bundles');
