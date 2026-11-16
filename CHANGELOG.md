# Changelog

## v0.2.37 — 2026-11-17
- A "What's new" page in the console, opened from the account menu; a dot on the menu marks a release you have not seen yet.
- A new MCP tool `whats_new` lists recent changes, and the MCP server tells AI clients its version and newest changes when they connect.
- Hosts show "Update available" when a newer bot-agent release exists, in the console and in `list_hosts`.
- Works with bot-agent v0.2.10.

## v0.2.36 — 2026-11-16
- Edit or delete an env set over MCP through a confirm plan (`update_env_set`, `delete_env_set`); a plan is refused if the set changed since.
- Replace a secret through a value request; the audit log marks the value as replaced.
- Env sets list the entries that use them (`used_by`), and the readiness check flags a client ID and its secret that come from different places.
- Entry-private secrets: fill or generate a secret for one entry only, with a show-once reveal.
- Shared networks in the console: choose networks for an image entry and ask a host's administrator for one it does not offer yet.
- An Open requests page lists the values, files and GitHub connections an AI agent asked you for.
- A compact readiness panel, and secret fields no longer trigger the browser's save-password prompt.
- An expired or already used confirmation now answers `conflict` with a clear message.
