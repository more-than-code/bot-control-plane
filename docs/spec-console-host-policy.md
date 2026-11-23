# Privileged steps go through the console: host policy from the console, deep links from MCP

Status: rev 2, as implemented (2026-11-23). Joe chose the root opt-in line on 2026-11-23. Covers DD items
DEF-20261123-design-principle-privileged-steps-are-do-exut (items 1 and 2),
DEF-20261123-let-org-owners-admins-change-a-host-s-ru-ul0m and
DEF-20261123-mcp-results-should-include-console-deep-6rqh. Item 3 of the
principle (connect_registry, request_env_value, request_file always return the
link at once) is in the connect_registry thread and is not repeated here.

## 1. Principle

The agent never performs a privileged step. For each one, MCP returns a deep
link to the exact console page, already pointed at the change. The person taps
it, reviews, and confirms in the console. Privileged steps are: a host's
runner.toml, registry logins and Connect GitHub, secret values and files,
host access grants, domains.

New rule in code: no MCP tool and no delegated (MCP) session may change a
host's policy. The new API routes are console-only, like host config files.

## 2. Host policy from the console

### 2.1 Opt-in by the host's root (the open question)

Recommendation: **yes, the host's root opts in once.** runner.toml is root-owned
so that the server can never widen a host on its own. Without an opt-in, a
compromised API or a stolen org-admin session could open images or projects
on every host. With it, the root decides once, at install or with one edit.

- New runner.toml key `console_policy = true|false` (missing = false). An older
  agent refuses it as `unknown_key`, like every new key.
- No installer flag in this rev: the root adds the line by hand once
  (`packaging/runner.toml.example` shows it). `bot-agent policy check` prints
  `console_policy=<bool>` as its last line.
- Only three keys are console-editable: `edge`, `images`, `projects`.
  Never: `enabled`, `run_as`, `dirs`, `exec_allow`, `allow_shell`,
  `max_programs`, `max_containers`, `allow_public_ports`, shared networks,
  `host_files`, `agent_upgrade`, its proxy, or `console_policy` itself.
- Optional ceiling for later (not in this rev): `console_images_within = [...]`
  so the console can only add image patterns inside root-set prefixes.

Hosts that are not opted in keep today's path: the console and MCP show the
snippet (`host_policy_snippet`) and say the root must add it once.

Bootstrap cost for bot-ops-vm: it runs agent 0.2.14, which predates server
upgrades, so it needs one SSH session to install the new agent (0.2.15 or
later) and add `console_policy = true`. After that, edge and images are console-only work.

### 2.2 Agent (agent-protocol v1.21, additive, gated)

1. Hello: `programs.console_policy` (bool) and `programs.image_patterns`
   (the runner.toml `images`, sent only when `console_policy` is true; at most
   64). The existing `programs.policy_hash`, `edge` and `projects` carry the
   rest. There is no `last_policy_apply` and no result frame: the server reads
   the outcome from the next hello's `policy_hash`.
2. Task `host_policy_apply`, spec `{"base_hash", "edge", "images",
   "projects"}` (strict: no other key, valid patterns and slugs, no repeats,
   at most 64 images and 32 projects). Refused `forbidden` when the host is
   not managed.
3. Helper verb `host_policy_apply` (root):
   - re-read runner.toml; `console_policy` must be true, else `policy_denied`;
   - its SHA-256 must equal `base_hash`, else `conflict`;
   - rewrite only the three keys, line by line, keeping comments and order
     (append a key that is missing); no change is `bad_spec`;
   - load the result with the same policy loader: it must be valid and keep
     `console_policy = true`, else `bad_spec`;
   - no other helper lock held, no upgrade running and the unit not active,
     else `busy`;
   - stage the new file under `/var/lib/bot-agent-runner/policy/`, start the
     transient unit `bot-agent-policy` (`policy-run <base_hash> <new_hash>`)
     and answer `accepted` with `new_hash`. The helper's sandbox cannot write
     `/etc`; the unit does.
4. `bot-agent-runner policy-run` (the unit, RuntimeMaxSec 300): wait 3 s so
   the agent can send the task result, check runner.toml still has
   `base_hash` and the staged file `new_hash`, keep `runner.toml.prev`
   (0600), write atomically, restart bot-agent and wait up to 90 s for
   `/var/lib/bot-agent/last_hello` newer than the restart. On failure put the
   previous file back and restart again (`rolled_back`, `not_reconnected`).
   Writes `result.json` for the root and the journal.

### 2.3 API (api-v1 v1.49)

- `GET /v1/orgs/{id}/hosts/{host_id}/policy`:
  `{managed, policy_hash, edge, images, projects, apply}`. `apply` is the
  last apply this API process started (kept in memory, no migration):
  `{state, started_at, ends_by, started_by, change, finished_at}`, `state`
  `running`, `applied` (a hello carried `new_hash`), `rolled_back` (after
  150 s the host is back on `base_hash`) or `unknown`. Org owners and admins,
  console only (delegated sessions 403).
- `POST /v1/orgs/{id}/hosts/{host_id}/policy` `{base_hash, edge, images,
  projects}`: validates with the agent's rules; refuses `host_offline`,
  `unsupported_agent` (not managed), `conflict` (base hash changed, or an
  apply is running, with `started_at` and `ends_by`), `bad_request`
  (nothing to change); sends the task (60 s) and answers `202` with the
  apply. A task refusal maps to the same codes; no answer is `unavailable`.
- Audit `host.policy_apply` (the change: edge before and after, patterns and
  slugs added and removed, never the file) and `host.policy_result` (the
  settled state). No problem kind in this rev.
- `check_readiness`: on a managed host the image check is exact (the
  host's patterns are known); fixes `host_policy_snippet` carry
  `image_pattern`, and new fix actions `allow_edge`, `turn_on_edge` and
  `verify_domain` (with `org_id`).

### 2.4 Console

Host page, new **Policy** tab (`?tab=policy`), org owners and admins only:
Edge switch, Images list (add, remove), Projects list (add, remove), a diff of
the change, Apply with a confirm, then the result (applied, rolled back or
unknown), re-read every 5 s while it runs. The Edge section's "edge not
allowed" note links to `?tab=policy&policy.edge=on`. A host that is not opted
in shows the snippet and the one line the root adds.

Deep-link params stage a change but never apply it:
`?tab=policy&policy.edge=on`, `&policy.add_image=<pattern>` (repeatable),
`&policy.add_project=<slug>`. The person sees the diff and taps Apply.

## 3. Deep links in MCP results

MCP gets an optional `BOT_MCP_CONSOLE_ORIGIN` (unset: no links, results as
today). One package builds every link, so URLs live in one place.

- `check_readiness`: every fix whose action is a person's gets
  `fix.console_url`:
  - `create_env_set`, `fill_value`, `add_file` → project Settings at that set
    or file (`/projects/<id>?tab=settings&…`);
  - `connect_github`, `add_registry_login` → project Settings, Registries;
  - `grant_deploy_access` → `/hosts/<id>#access` (org admins);
  - `host_policy_snippet` → `/hosts/<id>?tab=policy&policy.add_image=<p>`
    (opted-in host) or the host page with the snippet (not opted in);
  - edge off → `…&policy.edge=on`; domain not verified or not assigned →
    org `?tab=domains`.
- Tool results that name an object the person may need to act on carry its
  page: `host_policy_snippet` (the Policy tab with the pattern staged),
  `image_access` (project Registries when there is no login or it failed),
  `list_hosts`, `get_catalog_entry` and `list_problems` (each item's link).
  Later: `list_env_sets`, `list_routes` and deploy failures that need a
  person.
- Tool descriptions tell the agent: when a step needs the person, post the
  `console_url` and do not describe clicks.

## 4. Out of scope

connect_registry link timing (other thread); MCP plan + confirm for policy
changes (excluded by the principle); the image ceiling key; editing other
runner.toml keys.

## 5. Order of work

1. bot-agent: key, hello, task, helper verb, policy-run, tests.
2. API: routes, task, audit, readiness, tests.
3. Console: Policy tab, deep-link staging, Edge card link, tests.
4. MCP: console origin, link package, readiness fixes and tool results, tests.
5. Contracts: api-v1 v1.49 and agent-protocol v1.21 sections for the umbrella.
