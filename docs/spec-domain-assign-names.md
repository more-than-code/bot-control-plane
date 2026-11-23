# Spec: assign one name of a domain, and the Assign link in MCP

Rev 1 (2026-11-23). Joe asked that an org be able to give a project a single hostname (or a narrower wildcard) instead of only the apex or `*.<domain>`, and that domain assignment reach MCP. api-v1 **v1.52** (renumbered from v1.49 when v1.49–v1.51 landed first), migration **0036**. No agent-protocol change.

## 1. Problem

An assignment's `pattern` was `@` (the domain itself) or `*` (any one label below it). To give the Marketing OS project only `marketingos.deploybot.dev`, the owner had to assign `*.deploybot.dev`, which lets that project route any first-level name under the domain, including `console.`, `api.` and `mcp.`. DNS limits the practical risk today, but the grant is wider than intended.

The second half of the ask, "expose domain assignment over MCP with plan + confirm", is answered differently: Joe's design principle (DD `DEF-20261123-design-principle-privileged-steps-are-do-exut`) is that the MCP must not perform privileged steps. Assigning a domain stays console only, and MCP hands the person a deep link to the exact action instead.

## 2. Patterns (api-v1 v1.52)

1. **`pattern` is relative to the domain name**, like a DNS zone file. Four shapes:
   - `@` — the domain itself (unchanged).
   - `*` — any one label below the domain (unchanged).
   - a relative name, one or more labels: `marketingos`, `api.dev` — exactly `marketingos.<domain>`.
   - `*.` and a relative name: `*.dev` — any one label below `dev.<domain>`.
   Labels are lowercase LDH (IDNA A-labels included), each ≤ 63 characters, and the name written out is ≤ 253 characters. `PUT …/domains/{id}/assignments` refuses anything else as `bad_request` with the shape message, which now names all four forms. The same project and pattern twice in one body is still refused. At most 64 assignments per domain, unchanged.
2. **A route is allowed** when a `verified` domain assigned to the project covers its hostname under one of its patterns. `*` still matches exactly one label; `*.dev` matches exactly one label below `dev.<domain>`; a relative name matches only itself. No new DNS record is needed, because the parent domain is already verified.
3. **A domain delete** plans and removes the routes on the domain and one label below it, as before, and since v1.52 also the routes deeper below it that no more specific domain of the org holds (whatever that domain's status or assignments). It still leaves a route that another verified or lapsed domain assigned to the same project covers, under any of the four patterns.
4. **Migration 0036** only relaxes `domain_assignments_pattern_chk`; no row changes. Its down script deletes the assignments that are not `@` or `*` and restores the old check. Routes one label below a domain keep serving until that domain is deleted. Routes two or more labels below every domain of their org are listed and removed, since the older API cannot stop them on a domain delete; hosts drop them from the edge when they reconnect. Roll the API back to v1.51 first.

## 3. The Assign link (api-v1 v1.52)

Links follow v1.51's convention: the API sends ids, and the MCP server builds `console_url` from `BOT_MCP_CONSOLE_ORIGIN`.

1. **`PUT …/catalog/{name}/routes`** refusing `"<hostname> is not under a verified domain assigned to this project"` now carries a payload when that hostname is under a `verified` domain of the project's org (the most specific one): `{"hostname","domain","domain_id","org_id","pattern","name"}`. `pattern` is the narrowest pattern that assigns exactly that hostname, and `name` is it written out. The message is unchanged, so an older client reads the same refusal. A hostname under no verified domain of the org has no payload.
2. **Readiness** `routes` warn for the same case has fix `{"action":"assign_domain","org_id","domain_id","pattern","name"}` instead of v1.51's `verify_domain`, which stays for a hostname under no verified domain and for a lapsed one.
3. **MCP.** No assign tool: the API refuses delegated sessions on the domain routes, and this is a privileged step. The link is `<console>/orgs/<org>?tab=settings&domains.assign=<domain>&domains.project=<project>&domains.pattern=<pattern>`, built only from checked UUIDs and a checked pattern. `set_routes` appends one sentence to that refusal naming the domain and the name, with `Open: <link>`. `check_readiness` puts the same link in the fix's `console_url`. Without a console origin the sentences stay and the link is left out.

## 4. Console

1. **Assign dialog** (Organization settings → Domains → Assign, org owners and admins): the pattern select becomes a text field, one per row, holding the relative pattern. A hint under the row previews the full name it assigns ("Assigns marketingos.deploybot.dev") or says what to enter. A typed full name, a trailing dot and upper case are tidied back to a pattern before the save, so pasting `marketingos.deploybot.dev` works. The API stays the authority.
2. **Assignment rows** on the domain card write the pattern out (`marketingos.deploybot.dev`, `*.dev.deploybot.dev`).
3. **The deep link** `?tab=settings&domains.assign=<domain>&domains.project=<project>&domains.pattern=<pattern>` opens that domain's Assign dialog with the row added to the saved list, for the admin to check and save. The three keys are dropped from the URL on arrival, so the dialog opens once. A link for an unknown project, a pattern the domain cannot take, or another domain opens nothing.

## 5. Out of scope

- A pattern with a wildcard anywhere but the first label (`a.*.b`), and multi-label wildcards (`**`).
- Verifying a subdomain separately: the parent domain's TXT record still carries the whole domain.
- A console list of which project holds which name across domains.
- The console's own readiness view: like v1.51's console-step fixes, `assign_domain` is shown through MCP's `console_url`; the console Settings readiness row keeps fix `none`.
- `domain_id` and `org_id` in the refusal and the fix reach project owners and admins who may not be org members; ids are not secrets, and the Assign dialog itself is for org owners and admins only.
