// Node test loader: resolves $lib and extensionless imports and compiles .svelte files for
// `svelte/server` render. A trimmed copy of console/tests/loader.mjs (the site has no $app or $env).
import { existsSync, readFileSync, statSync } from 'node:fs';
import { dirname, extname, resolve as resolvePath } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { compile, compileModule } from 'svelte/compiler';

const root = resolvePath(fileURLToPath(new URL('.', import.meta.url)), '..');

function fileUrl(abs) {
	return pathToFileURL(abs).href;
}

function withExtension(abs) {
	if (extname(abs) && existsSync(abs) && statSync(abs).isFile()) return fileUrl(abs);
	for (const suffix of ['.ts', '.js', '.svelte']) {
		const candidate = abs + suffix;
		if (existsSync(candidate) && statSync(candidate).isFile()) return fileUrl(candidate);
	}
	return null;
}

export async function resolve(specifier, context, nextResolve) {
	if (specifier.startsWith('$lib/')) {
		const url = withExtension(resolvePath(root, 'src/lib', specifier.slice('$lib/'.length)));
		if (!url) throw new Error(`Cannot resolve ${specifier}`);
		return { url, shortCircuit: true };
	}
	if ((specifier.startsWith('./') || specifier.startsWith('../')) && context.parentURL?.startsWith('file:')) {
		const url = withExtension(resolvePath(dirname(fileURLToPath(context.parentURL)), specifier));
		if (url) return { url, shortCircuit: true };
	}
	return nextResolve(specifier, { ...context, conditions: [...(context.conditions ?? []), 'svelte'] });
}

export async function load(url, context, nextLoad) {
	// The layout's app.css has no effect on server render.
	if (url.startsWith('file:') && url.endsWith('.css')) {
		return { format: 'module', source: '', shortCircuit: true };
	}
	if (url.startsWith('file:') && url.endsWith('.svelte')) {
		const filename = fileURLToPath(url);
		const compiled = compile(readFileSync(filename, 'utf8'), { filename, generate: 'server', css: 'external' });
		return { format: 'module', source: compiled.js.code, shortCircuit: true };
	}
	return nextLoad(url, context);
}
