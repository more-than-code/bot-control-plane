/** See `sonner-document.ts`: the stand-in document goes once svelte-sonner has loaded. */
delete (globalThis as { document?: unknown }).document;
export {};
