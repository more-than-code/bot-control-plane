# Spec: assign one name of a domain, and the Assign link in MCP

Rev 1 (2026-11-23). Joe asked that an org be able to give a project a single hostname (or a narrower wildcard) instead of only the apex or `*.<domain>`, and that domain assignment reach MCP. api-v1 **v1.49**, migration **0033**. No agent-protocol change.

## 1. Problem

An assignment's `pattern` was `@` (the domain itself) or `*` (any one label below it). To give the Marketing OS project only `marketingos.deploybot.dev`, the owner had to assign `*.deploybot.dev`, which lets that project route any first-level name under the domain, including `console.`, `api.` and `mcp.`. DNS limits the practical risk today, but the grant is wider than intended.

The second half of the ask, "expose domain assignment over MCP with plan + confirm", is answered differently: Joe's design principle (DD `DEF-20261123-design-principle-privileged-steps-are-do-exut`) is that the MCP must not perform privileged steps. Assigning a domain stays console only, and MCP hands the person a deep link to the exact action instead.

## 2. Patterns (api-v1 v1.49)

1. **`pattern` is relative to the domain name**, like a DNS zone file. Four shapes:
   - `@` — the domain itself (unchanged).
   - `*` — any one label below the domain (unchanged).
   - a relative name, one or more labels: `marketingos`, `api.dev` — exactly `marketingos.<domain>`.
   - `*.` and a relative name: `*.dev` — any one label below `dev.<domain>`.
   Labels are lowercase LDH (IDNA A-labels included), each ≤ 63 characters, and the name written out is ≤ 253 characters. `PUT …/domains/{id}/assignments` refuses anything else as `bad_request` with the shape message, which now names all four forms. The same project and pattern twice in one body is still refused. At most 64 assignments per domain, unchanged.
2. **A route is allowed** when a `verified` domain assigned to the project covers its hostname under one of its patterns. `*` still matches exactly one label; `*.dev` matches exactly one label below `dev.<domain>`; a relative name matches only itself. No new DNS record is needed, because the parent domain is already verified.
3. **A domain delete** plans and removes the routes under the domain at any depth, and still leaves a route that another verified or lapsed domain assigned to the same project covers, under any of the four patterns.
4. **Migration 0033** only relaxes `domain_assignments_pattern_chk`; no row changes. Its down script deletes the assignments that are not `@` or `*` (routes under them keep serving until their domain is deleted, but no new route is accepted) and restores the old check. Roll the API back to v1.48 first.

## 3. The Assign link (api-v1 v1.49)

1. **`PUT …/catalog/{name}/routes`** refusing `"<hostname> is not under a verified domain assigned to this project"` now carries a payload when that hostname is under a `verified` domain of the project's org (the most specific one): `{"hostname","domain","domain_id","pattern","name","assign_url"}`. `pattern` is the narrowest pattern that assigns exactly that hostname, `name` is it written out, and `assign_url` is `<BOT_API_CONSOLE_ORIGIN>/orgs/<org>?tab=settings&domains.assign=<domain_id>&domains.project=<project>&domains.pattern=<pattern>` (left out when the console origin is unset). The message is unchanged, so an older client reads the same refusal. A hostname under no verified domain of the org has no payload.
2. **Readiness** `routes` warn for the same case gains fix `{"action":"assign_domain","name","url"}` with the same link. Any other `routes` warning keeps fix `none`.
3. **MCP.** No assign tool: the API refuses delegated sessions on the domain routes, and this is a privileged step. `set_routes` appends one sentence to that refusal naming the domain and the link, and tells the agent to show it to the person. `check_readiness` turns the fix into "ask an org owner or admin to open <url> and save the row that assigns <name> to this project (assigning a domain is console only)". The MCP checks the link itself (scheme `http`/`https`, no userinfo, the project and pattern in the query matching the payload) and drops a link that does not fit, so a refusal can never point the agent elsewhere.

## 4. Console

1. **Assign dialog** (Organization settings → Domains → Assign, org owners and admins): the pattern select becomes a text field, one per row, holding the relative pattern. A hint under the row previews the full name it assigns ("Assigns marketingos.deploybot.dev") or says what to enter. A typed full name, a trailing dot and upper case are tidied back to a pattern before the save, so pasting `marketingos.deploybot.dev` works. The API stays the authority.
2. **Assignment rows** on the domain card write the pattern out (`marketingos.deploybot.dev`, `*.dev.deploybot.dev`).
3. **The deep link** `?tab=settings&domains.assign=<domain>&domains.project=<project>&domains.pattern=<pattern>` opens that domain's Assign dialog with the row added to the saved list, for the admin to check and save. The three keys are dropped from the URL on arrival, so the dialog opens once. A link for an unknown project, a pattern the domain cannot take, or another domain opens nothing.

## 5. Out of scope

- A pattern with a wildcard anywhere but the first label (`a.*.b`), and multi-label wildcards (`**`).
- Verifying a subdomain separately: the parent domain's TXT record still carries the whole domain.
- A console list of which project holds which name across domains.
