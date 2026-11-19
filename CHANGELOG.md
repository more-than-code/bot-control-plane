# Changelog

## v0.2.40 — 2026-11-19
- The console header shows your organization and project on every page, and switches project from any page.
- A bell in the header collects new releases and, for organization owners and admins, hosts whose agent has an update. What you mark read stays read on every device.
- The "What's new" dot moved from the account menu to the bell.

## v0.2.39 — 2026-11-18
- The landing page shows the console's Catalog & services tab again, and the site and llms.txt describe public addresses and routes.
- Turning the edge on while another program holds port 80 or 443 now says what to do next.
- An expired confirmation says so and asks you to run the tool again, instead of "not found".
- Making a key secret while its value is empty is refused, in the console and over MCP, so no empty secret is created.
- Revoked hosts no longer show "Update available".
- Smaller console fixes: a neutral message when an entry name is taken, an accessible name on the env-set Delete button, clearer Edge card text, and the tag list shows how many tags match your filter and says when a long list is not complete.

## v0.2.38 — 2026-11-17
- Public addresses: verify your own domain with a DNS TXT record, assign it to projects, and give an entry an HTTPS address, served by a per-host edge with automatic certificates.
- Routes go through plan and confirm, in the console and over MCP (`set_routes`, `list_routes`), with two options: client IPs from Cloudflare, and no-store paths.
- An org admin turns the edge on per host; it is refused while another program holds port 80 or 443, and the message names it.
- Open WebSockets stay up when routes change.
- The site and llms.txt describe the current features, with a components diagram.
- Works with bot-agent v0.2.11 (needed only for the edge).

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
