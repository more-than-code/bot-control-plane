/**
 * svelte-sonner keeps toasts only in a browser (`typeof document !== 'undefined'` when it loads). A test that reads
 * its real toast list imports this, then `svelte-sonner`, then `sonner-document-done.ts`: the library loads with a
 * document, and every later module (bits-ui reads `window` when it sees one) loads without it.
 */
(globalThis as { document?: unknown }).document = {};
export {};
