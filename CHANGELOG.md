# Changelog

## v0.2.45 — 2026-11-23
- Delete stale image versions from the image picker: an org owner or admin can remove a ghcr.io version after allowing it in the GitHub connection. A version that is running, is a rollback target or is among the last 5 deploys is kept, and the message says why.
- The image picker lists ghcr.io tags newest first.
- New MCP tools `get_catalog_entry` and `set_entry_image`: read one entry in full, and move an image entry to a new version without rewriting the rest of its spec.
- Create an organization from the console, in the header's Org menu or on the projects page.
- The project and organization Hosts tabs keep host status and "Update available" current while open.
- New MCP tool `list_image_tags` lists an image's registry tags, newest first, so an agent can pick a version to deploy.
- A failed deploy now says why: Deploybot keeps the reason, the final container status and the end of its log, and shows them in the deploy history. This needs bot-agent 0.2.15 on the host.
- Upgrade a host's agent from the console or MCP. The host owner turns this on in `runner.toml`. The agent checks the release's signature, installs it, and goes back to the previous version if the new one does not come up. This needs bot-agent 0.2.15 on the host.

## v0.2.44 — 2026-11-22
- Edit, version and apply a host's config files from the console. The host owner chooses which files in `runner.toml`, and only organization owners and admins can change them.
- Each apply is checked by the host's own command, restarts the file's service, is confirmed healthy, and puts the previous file back if anything fails.
- Secret config files keep their values encrypted and never show them: you set or remove keys, then save and apply.
- A new "needs action" problem now shows as a notice on any open console page, and you can turn on desktop notifications for them in the bell menu.
- Reconnect after moving between pages works again.

## v0.2.43 — 2026-11-21
- Connect GitHub logins now stay signed in: Deploybot renews GitHub's 8-hour access token before it expires, including for projects that rarely deploy.
- If GitHub ends the sign-in (the app was revoked, access was removed, or it was unused for 6 months), the bell says so and asks you to reconnect GitHub in Settings.
- A GitHub login connected before this release still expires as before: reconnect GitHub once in the project's Settings.

## v0.2.42 — 2026-11-21
- The bell now shows problems: a host that stopped reporting, a registry login that was refused, and a deploy that failed or rolled back. Each says what to do, and resolves on its own when fixed.
- New MCP tool `list_problems` lists the open and recent problems of a project or organization.
- A deploy, rollback or remove that is still running shows in the entry's history with the time it ends by, and the dialog follows it to the end.
- Trying to deploy while another operation runs now says what is running and when to try again.
- The console no longer gives up on a long deploy: it answers within 80 seconds and shows the outcome when it arrives.

## v0.2.41 — 2026-11-21
- Rename a host from the organization's Hosts table. A revoked host no longer keeps its name, so a re-enrolled machine can take it back.
- Remove a revoked host from every host list. Its history and audit records stay.
- Connecting GitHub now says why an attempt failed, in the console and over MCP.
- The page for filling in a requested value says when the value has a line break.
- Keys ending in `_FILE`, `_PATH` or `_DIR` are no longer treated as secrets because of their name, and the Secret box is no longer ticked for you; a hint says when a key looks secret.

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
