# bot-api-server REST API v1 (phase 1 contract)

**Status:** Frozen for wave 1 (2026-10-13), v1.0.1 (clarifications at the end). Source of truth: umbrella `docs/contracts/api-v1.md`. Each repo gets a copy at `docs/contracts/api-v1.md`, and copies must not be edited. Report a contract problem in `NOTES.md` instead of changing it.
**Design:** umbrella `docs/design-bot-agent-deploy.md` (§2 permissions, §3 auth, §6 server).
**Consumers:** bot-console (BFF server routes), bot-mcp-server, the `deploybot login` CLI.

## Conventions

- Base path `/v1`. JSON in and out (UTF-8). All times are RFC3339 UTC strings. All ids are UUID strings.
- Envelope on every response: `{"code": "<string>", "msg": "<human text>", "payload": <object|null>}`.

| `code` | HTTP |
|---|---|
| `ok` | 200 (201 on create) |
| `bad_request` | 400 |
| `unauthorized` | 401 |
| `forbidden` | 403 |
| `not_found` | 404 |
| `conflict` | 409 |
| `unavailable` | 503 (host offline or asleep) |
| `timeout` | 504 (agent did not answer within 30 s) |
| `error` | 500 |
| `rate_limited` | 429 (v1.5) |
| `not_implemented` | 501 (v1.6) |
| `registry_unavailable` | 502 (v1.16: a container registry lookup failed; `payload.reason`) |

  The device token endpoint also uses `authorization_pending`, `slow_down`, `expired_token`, and `access_denied`, all with HTTP 400.
- **Auth:** `Authorization: Bearer <session token>` on every route except those marked public. A missing, expired, or unknown token, or a disabled user, gives `unauthorized`.
- **Permission failures:** `forbidden` when the user is a member but the role is too low. `not_found` when the user is not a member of the project that owns the resource, so the server does not reveal that it exists.
- **Lists:** `payload = {"items": [...], "next_cursor": "<opaque>|null"}`. Optional query `limit` (1–200, default 50) and `cursor`.
- `GET /health` (public, no `/v1`) → `{"status": "ok"}`, no envelope.
- **Roles:** `owner` > `admin` > `viewer`. "viewer+" means any member.

## Objects

```jsonc
User      {"id","email","name","avatar_url"}
Project   {"id","name","slug","role","created_at"}          // role = caller's role
Member    {"user_id","email","name","role","created_at"}
JoinToken {"id","host_name_hint","created_by","created_at","expires_at","used_at","used_by_host_id","revoked_at"}
Host      {"id","project_id","name","node_type","os","arch","always_on","agent_version",
           "status","last_seen_at","enrolled_at","revoked_at"}
           // status: "online" | "offline" | "asleep" (offline + always_on=false) | "revoked"
Service   {"id","host_id","name","source","kind","ports","systemd_unit","systemd_scope",
           "container_name","process_name","first_seen_at","last_seen_at","health"}
           // source: discovered|declared|owned ; kind: container|systemd|process
           // health: {"status":"up|down|unknown","last_checked_at","latency_ms"} (unknown when no check or stale > 2 min)
HealthCheck {"id","service_id","protocol","port","path","interval_sec",
             "expect_status_min","expect_status_max","enabled"}
             // protocol http|tcp; path required for http (starts with "/"), null for tcp
             // interval_sec 30..300 (default 60); expect 200..399 default
AuditEntry {"id","at","actor_user_id","via","project_id","host_id","service_id",
            "action","decision","task_id","result","error_code","detail"}
```

## Auth (public unless noted)

| Method | Path | Body / query | Payload |
|---|---|---|---|
| GET | `/v1/auth/google/start` | query `redirect_uri` (must be in server allowlist `BOT_API_OAUTH_REDIRECT_URIS`) | `{"authorize_url","state"}` |
| POST | `/v1/auth/google/callback` | `{"code","state","redirect_uri"}` | `{"token","expires_at","user":User}`. The session kind is `console`. |
| POST | `/v1/auth/signout` | — (session) | `null` (deletes this session) |
| GET | `/v1/me` | — (session) | `{"user":User,"identities":[{"provider","email"}],"projects":[Project]}` |
| POST | `/v1/auth/device/code` | `{"client_name"}` (1–100 chars) | `{"device_code","user_code","verification_uri","verification_uri_complete","expires_in":600,"interval":5}` |
| POST | `/v1/auth/device/token` | `{"device_code"}` | `{"token","expires_at"}` (session kind `mcp`, 30 days; **since v1.4** kind `mcp_access`, see oauth-v1 §9), or one of the four device codes |
| GET | `/v1/auth/device/{user_code}` | — (session) | `{"user_code","client_name","expires_at","status":"pending|approved|denied|expired"}` |
| POST | `/v1/auth/device/{user_code}/approve` | — (session) | `null` |
| POST | `/v1/auth/device/{user_code}/deny` | — (session) | `null` |

- `user_code`: 8 characters from `BCDFGHJKLMNPQRSTVWXZ`, shown as `XXXX-XXXX`. Input is case-insensitive and the hyphen is optional.
- `verification_uri` = `BOT_API_DEVICE_VERIFICATION_URI` (the console `/device` page). `verification_uri_complete` = that URI plus `?code=XXXX-XXXX`.
- Polling faster than `interval` returns `slow_down` and adds 5 s to that device code's interval.
- `state` and the PKCE verifier are kept server-side for 10 minutes and are single use. Only Google `email_verified=true` is accepted (`forbidden` otherwise).

## Projects and members

| Method | Path | Role | Body | Payload |
|---|---|---|---|---|
| POST | `/v1/projects` | session | `{"name"}` (1–80) | Project (creator = owner), 201 |
| GET | `/v1/projects` | session | — | list of Project the caller belongs to |
| GET | `/v1/projects/{id}` | viewer+ | — | Project |
| PATCH | `/v1/projects/{id}` | owner | `{"name"}` | Project |
| DELETE | `/v1/projects/{id}` | owner | — | `null` (soft delete; its hosts' credentials are revoked) |
| GET | `/v1/projects/{id}/members` | viewer+ | — | list of Member |
| POST | `/v1/projects/{id}/members` | admin+ | `{"email","role":"admin|viewer"}` | Member, 201. `not_found` if no user has that email yet; `conflict` if already a member. |
| PATCH | `/v1/projects/{id}/members/{user_id}` | admin+ | `{"role":"admin|viewer"}` | Member. The owner's role cannot be changed here (`forbidden`). |
| DELETE | `/v1/projects/{id}/members/{user_id}` | admin+, or self | — | `null`. The owner cannot be removed. |
| POST | `/v1/projects/{id}/transfer-ownership` | owner | `{"user_id"}` (an existing member) | `null`. The old owner becomes admin. |

`slug` is derived from the name (lowercase, `[a-z0-9-]`, unique; a `-2`, `-3`, … suffix is added on collision) and does not change on rename.

## Join tokens and hosts

| Method | Path | Role | Body | Payload |
|---|---|---|---|---|
| POST | `/v1/projects/{id}/join-tokens` | admin+ | `{"host_name_hint"?: string, "expires_in_sec"?: 300..604800 (default 86400)}` | `JoinToken` + `"token"` (plaintext, **only here**) + `"install_command"`, 201 |
| GET | `/v1/projects/{id}/join-tokens` | admin+ | — | list of JoinToken (never the token) |
| DELETE | `/v1/projects/{id}/join-tokens/{token_id}` | admin+ | — | `null` (sets `revoked_at`) |
| GET | `/v1/projects/{id}/hosts` | viewer+ | — | list of Host |
| GET | `/v1/hosts/{id}` | viewer+ | — | Host |
| DELETE | `/v1/hosts/{id}/credential` | admin+ | — | `null` (revokes all credentials; the agent connection is closed with `forbidden`) |

`install_command` is exactly `sudo -u bot-agent bot-agent init --server '<BOT_API_AGENT_WS_URL>' --join-token '<token>'`.

## Services, health, metrics

| Method | Path | Role | Notes |
|---|---|---|---|
| GET | `/v1/hosts/{id}/services` | viewer+ | list of Service (not paginated; at most 500) |
| GET | `/v1/services/{id}` | viewer+ | Service + `"health_checks":[HealthCheck]` |
| PUT | `/v1/services/{id}/health-checks` | admin+ | Body `{"checks":[HealthCheck without id/service_id]}`. Replaces the whole set (max 10). Each `port` must be in `Service.ports`, otherwise `bad_request`. Pushed to the agent as `config_update`. |
| GET | `/v1/hosts/{id}/metrics` | viewer+ | Query `m` ∈ `cpu,mem,disk,diskio,net,system,temp`; `from`, `to` (RFC3339; default last 1 h; span ≤ 31 days); `step` (seconds, 10–3600; default chosen so there are ≤ 500 points). Payload `{"m","from","to","step","series":[{"name","unit","points":[["<t>", <number|null>], ...]}]}`. |
| GET | `/v1/hosts/{id}/processes` | viewer+ | Latest process sample (≤ 90 s old): `{"sampled_at","items":[{"pid","process_name","user","cmdline","service_id","cpu_usage","memory_rss","memory_usage","num_threads","started_at"}]}`. Query `sort=cpu|memory` (default cpu), `limit` ≤ 200. |
| GET | `/v1/services/{id}/health` | viewer+ | Query `from`, `to`. `{"items":[{"t","check_id","result","http_status","latency_ms"}]}`. Newest first, ≤ 1000. |

Series names per `m`:

| `m` | Series (`unit`) |
|---|---|
| `cpu` | `usage_percent` (%) = 100 − `usage_idle` |
| `mem` | `used_percent` (%), `used` (bytes), `total` (bytes) |
| `disk` | `used_percent` (%) for path `/` |
| `diskio` | `read_bps`, `write_bps` (bytes/s; counter deltas; negative deltas dropped) |
| `net` | `rx_bps`, `tx_bps` (bytes/s; all interfaces except `lo`; negative deltas dropped) |
| `system` | `load1`, `load5`, `load15` (`""`), `uptime` (s) |
| `temp` | `celsius` (°C) |

## Live calls (phase 1)

| Method | Path | Role | Body | Payload |
|---|---|---|---|---|
| POST | `/v1/hosts/{id}/snapshot` | viewer+ | — | Snapshot (agent protocol §Payloads) |
| POST | `/v1/services/{id}/probe` | viewer+ | `{"check_id"}`, or `{"protocol","port","path"}` (same validation as HealthCheck) | ProbeResult (agent protocol §Payloads) |

The server waits up to 30 s for the agent. An offline or asleep host gives `unavailable` immediately; no task is queued. Every live call is written to `audit_log` (`via` = `console` or `mcp`, taken from the session kind).

## Audit

| Method | Path | Role | Notes |
|---|---|---|---|
| GET | `/v1/projects/{id}/audit` | admin+ | list of AuditEntry, newest first |

## v1.0.1 clarifications (2026-10-13)

Raised by the wave 1 workers and decided by the orchestrator. They take precedence over anything above that conflicts.

1. **Audit ids:** `AuditEntry.id` is a decimal string (from `bigserial`), not a UUID. All other ids are UUIDs.
2. **Services list:** `GET /v1/hosts/{id}/services` uses the list envelope `{"items":[...],"next_cursor":null}`. It never paginates (≤ 500 items).
3. **Health history:** `GET /v1/services/{id}/health` returns `{"items":[...]}` with no cursor.
4. **One-sided windows:** `from` only → `to` = now. `to` only → `from` = `to` − 1 h. `from` ≥ `to` → `bad_request`.
5. **Live call outcomes:**
   - The agent answered with `succeeded` → `ok` with the payload. A probe whose `result` is not `success` is still `ok`: the probe ran.
   - The agent answered `failed` → `error` (HTTP 500), with the agent's payload if present.
   - The agent answered `timed_out` → `timeout` (504).
   - `task_reject`: `forbidden` → `forbidden`; `unknown_service` → `not_found`; `bad_spec` → `bad_request`; `wrong_host`/`expired` → `error`.
6. **Token formats:** session tokens, join tokens, device codes, and host credentials are 43-character unpadded base64url. OAuth `state` is 43-character base64url. `user_code` is as stated above.
7. **Probe/health-check `path`:** must start with `/`, must not start with `//`, ≤ 512 bytes, and contain no `@ \ ? #`, spaces, or control characters.
8. **Server-side redaction:** the server applies the agent-protocol redaction regex again to `cmdline` on ingest and on snapshot payloads (defense in depth).

## v1.0.2 amendments (2026-10-14)

1. **`install_command`** is now exactly `printf '%s' '<token>' | sudo -u bot-agent bot-agent init --server '<BOT_API_AGENT_WS_URL>' --join-token-stdin`. `printf` is a shell builtin, so the token never appears in any process argv.
2. Agent `task_reject` `busy` → `unavailable` (503).
3. Server-side redaction uses the agent-protocol v1.0.2 patterns.

## v1.1 shared hosts (2026-10-14, approved spec `docs/spec-shared-hosts.md`)

These take precedence over earlier sections. Permission matrix: spec §4.

1. **Host** gains `"relation": "owner" | "shared"` and `"share_level": "metrics" | "deploy" | null` (null when `relation` = owner). `project_id` stays the owning project.
2. **Service** gains `"project_id"` (the project the service belongs to; defaults to the host's owning project).
3. **`GET /v1/projects/{id}/hosts`** returns hosts owned by project `id` **and** hosts shared with it. Owned hosts come first, then shared ones, each sorted by name.
4. **Filtering:**
   - Owner-project members see everything.
   - A `metrics` share: host, status, and metrics are allowed; services, processes, snapshot, probe, and health history are `forbidden` (403).
   - A `deploy` share: services and processes are filtered to the sharing project's own services (processes whose `service_id` belongs to those services); probe, health history, and (admin+) health-check edits apply only to its own services (`forbidden` otherwise); snapshot is `forbidden`.
   - No relation: `not_found` (404).
   - A user in both the owner and a sharing project gets the owner relation.
5. **New endpoints:**
   - `GET /v1/hosts/{id}/shares` (owner admin+) → `{"items":[{"project_id","project_name","level","created_by","created_at"}]}` (not paginated).
   - `PUT /v1/hosts/{id}/shares/{project_id}` (owner admin+), body `{"level":"metrics"|"deploy"}`: creates or updates the share; 200 with the share object. The caller must also be a member (any role) of `project_id`, otherwise `not_found`. Sharing with the owning project itself → `bad_request`.
   - `DELETE /v1/hosts/{id}/shares/{project_id}` (owner admin+, or admin+ of `project_id`) → 200 `null`. `conflict` (409) if that project still has services on the host, unless the query has `reassign=owner`, which moves those services to the owning project in the same transaction.
6. **Audit actions:** `host.share`, `host.share_update`, `host.unshare` (with `detail` naming the project and level).
7. **Project deletion** cascades its shares. Services of a deleted sharing project on others' hosts move to the owner. Deleting the owning project revokes its hosts' credentials (unchanged), and their shares go too.


## v1.1.1 host-level visibility (2026-10-14, Joe: "processes per host should be visible to all members")

Supersedes v1.1 item 4 for processes and snapshot only.

1. **`GET /v1/hosts/{id}/processes`** returns the full, unfiltered latest sample to every user with any relation to the host: owner-project members (any role) and members of any sharing project (any share level, any role). No `service_id` filtering. No relation: `not_found` (404), unchanged.
2. **`POST /v1/hosts/{id}/snapshot`** is allowed for the same set of users.
3. Unchanged: services list, service get, probe, health history and health-check edits follow v1.1 item 4 (a `metrics` share gets 403; a `deploy` share is limited to its own project's services). `service_id` on a process row may name a service the caller cannot open; clients must treat a 403/404 on that service as normal.

## v1.2 service names (2026-10-14, Joe: "service name is meaningless")

1. **Service** gains `"display_name": string | null` (set by people; 1–64 printable characters, trimmed) and `"owner_user": string | null` (the account owning the listening socket, as reported by the agent).
2. **`PATCH /v1/services/{id}`**, body `{"display_name": "<name>" | null}`, sets or clears the name; returns the updated Service. Permission = the *edit* capability of spec-shared-hosts §4 (owner admin+, or a `deploy`-share admin for its own services). Audit action `service.rename` (detail: old → new). `display_name` is unique per host, case-insensitive; a duplicate is `conflict`.
3. The name survives rediscovery: services stay keyed by (host_id, discovered `name`), and `display_name` is never overwritten by the agent.
4. Suggested label for clients: `display_name` → else the discovered `name` unless it is `port-<n>` → else `port <n> · <owner_user>` (or `port <n>` when `owner_user` is null).

## v1.3 project services (2026-10-14, approved spec `docs/spec-project-services.md`)

These take precedence over earlier sections for the routes they name. v1.2 is implemented in the same release.

1. **`GET /v1/projects/{id}/services`**: the caller must be a member (any role) of project `id`; otherwise `not_found`.
   - **Rows:** every service on hosts owned by `id` (whatever the service's `project_id`), plus services with `project_id = id` on hosts shared with `id` at level `deploy`. Services with `project_id = id` on a host shared with `id` at level `metrics` are not returned.
   - **Row:** `Service` (with the v1.1 and v1.2 fields) plus `"host_name"`, `"host_status"`, and `"project_name"` (the name of the service's project).
   - **Query:**
     - `q`: case-insensitive substring over `display_name`, `name`, `kind`, `host_name`, `owner_user`, and ports as decimal text. Trimmed; empty means no filter; more than 100 characters → `bad_request`. `%`, `_` and `\` match literally.
     - `host_id`: a UUID of a host that the row rules reach for this project; otherwise `bad_request`.
     - `sort` ∈ `name` (by `lower(coalesce(display_name, name))`), `host` (host name), `kind`, `status` (`health.status`: up, down, unknown; then name), `last_seen` (`last_seen_at`). Default `name`.
     - `dir` ∈ `asc`, `desc`. Default `asc`; for `sort=last_seen` the default is `desc`.
     - `limit` 1–200, default 50.
     - `cursor` (opaque).
     - Any other value → `bad_request`.
   - **Ordering:** ties are broken by `id` ascending. `NULL`s sort last in both directions.
   - **Payload:** `{"items":[...], "next_cursor": "<opaque>"|null, "prev_cursor": "<opaque>"|null, "total": <int>}`. `total` counts every matching row and is recomputed per request.
   - **Cursors:** a cursor is bound to the `q`, `host_id`, `sort`, and `dir` it was issued for. Reusing it with different values → `bad_request` "cursor does not match query". Cursors are offset-based: a row inserted or deleted between pages may be skipped or repeated.
2. `GET /v1/hosts/{id}/services` is unchanged apart from the v1.2 fields on each row.
3. Every row returned by (1) is a service the caller may read with `GET /v1/services/{id}`; (1) never widens access.

## v1.4 OAuth for MCP (2026-10-14, approved spec `docs/spec-mcp-oauth.md`)

The full contract is `docs/contracts/oauth-v1.md` (frozen). Changes to this API:

1. **Audience rule:** on every `/v1/*` route, a token of session kind `mcp_access` or `mcp_refresh` → 401 `invalid_token` (oauth-v1 §8). `api_delegated` tokens act as their user; audit rows they cause carry `via='mcp'`.
2. **New console-only routes** (they need a `console` session; other kinds → 401):
   - `GET /v1/oauth/requests/{id}`, `POST /v1/oauth/requests/{id}/approve`, `POST /v1/oauth/requests/{id}/deny` (oauth-v1 §3.1);
   - `GET /v1/me/grants`, `DELETE /v1/me/grants/{id}` (oauth-v1 §11).
3. **Device flow:** `POST /v1/auth/device/token` issues an MCP-audience `mcp_access` token (oauth-v1 §9).
4. **Audit actions:** `oauth.client_register`, `oauth.authorize`, `oauth.token_revoke`, `oauth.refresh_reuse`.
5. **Cut-over:** legacy `mcp` sessions are revoked by migration 0005.
6. **v1.4.1:** the device-approval routes (`GET /v1/auth/device/{user_code}`, `/approve`, `/deny`) need a `console` session; other kinds → 401. Other v1.4.1 changes are in oauth-v1 "v1.4.1 amendments".

## v1.5 project invite links (2026-10-14, approved spec `docs/spec-project-invites.md`)

Types: `Invite {"id","role","created_by","created_at","expires_at","max_uses","uses","revoked_at"}`. An invite is **usable** when `revoked_at` is null, `expires_at` > now, and `uses` < `max_uses`.

| Method | Path | Who | Body | Result |
|---|---|---|---|---|
| POST | `/v1/projects/{id}/invites` | admin+ | `{"role":"admin"\|"viewer","expires_in_sec"?:3600..2592000 (default 604800),"max_uses"?:1..50 (default 1)}` | `Invite` + `"token"` + `"url"`, 201. Both appear **only here**. |
| GET | `/v1/projects/{id}/invites` | admin+ | — | list of `Invite`, newest first, never the token. Includes unusable invites. |
| DELETE | `/v1/projects/{id}/invites/{invite_id}` | admin+ | — | `null`; sets `revoked_at` (idempotent). An invite of another project → `not_found`. |
| POST | `/v1/invites/lookup` | any signed-in console user | `{"token"}` | `{"project_id","project_name","role","already_member":bool}` |
| POST | `/v1/invites/accept` | any signed-in console user | `{"token"}` | `{"project_id","role"}`, 200 |

1. **Token:** 43-character unpadded base64url (256 bits), stored as SHA-256 and compared by hash. `url` is exactly `<BOT_API_CONSOLE_ORIGIN>/invite/<token>`. The token is only ever sent in request bodies to the API.
2. **Validation:** `role` `owner` or any other value → `bad_request`. Values out of range → `bad_request`. Viewer → `forbidden`; non-member → `not_found` (general rule).
3. **Lookup and accept:** a token that is unknown, malformed, expired, revoked, or used up → `not_found` with one identical body for all of them. Lookup never changes state.
4. **Accept** runs in one transaction that locks the invite row (`SELECT … FOR UPDATE`):
   - if the caller is already a member → 200 with their current role; `uses` is unchanged and the role is never changed;
   - otherwise it inserts the membership with the invite's role and increments `uses`.
   Concurrent accepts never exceed `max_uses`.
5. **Session kinds:** lookup and accept need a `console` session; other kinds → 401.
6. **Rate limit:** lookup and accept together: 30/min per user and 60/min per source IP, in memory → new envelope error code `rate_limited` with HTTP 429 (added to the error table by this version).
7. **Audit:** `invite.create` (role, max_uses, expires_at), `invite.revoke`, `invite.accept` (invite id, user id). No audit row contains the token.
8. **Deletion:** deleting a project deletes its invites.
9. **Unchanged:** `POST /v1/projects/{id}/members` by email, and every other route.

## v1.6 project catalog, programs (2026-10-15, approved spec `docs/spec-project-catalog.md`, wave 1)

Additive. Container (`image`) entries, `secret_env` and the `docker run` form are wave 2: until then they are refused with the new envelope code **`not_implemented` (HTTP 501)**.

### Objects

```jsonc
CatalogEntry {"id","project_id","name","kind":"program","spec":ProgramSpec,"spec_hash","command_display",
              "created_by","created_at","updated_at","my_actions":["run","stop","restart","logs","status"]}
ProgramSpec  {"dir","exec","args":[string],"shell":bool,"run_as","env":{KEY:value},"port":int|null,
              "restart":"on-failure"|"no"}
CatalogGrant {"subject":"role:viewer"|"role:admin"|"user:<uuid>","actions":[Action]}   // Action ∈ run|stop|restart|logs
Approval     {"entry_id","entry_name","project_id","project_name","host_id","spec_hash","command_display",
              "dir","run_as","shell","status":"pending"|"approved","requested_by","requested_at",
              "approved_by","approved_at"}
ProgramStatus {"unit","active_state","sub_state","main_pid":int|null,"started_at":string|null,"ports":[int]}
ProgramLogs  {"unit","lines":[string],"truncated":bool}
HostPrograms {"enabled":bool,"run_as":[string],"allow_shell":bool,"policy_hash":string}   // from the agent's hello; null when the agent reports none
```

- `name`: `^[a-z0-9][a-z0-9-]{0,47}$`, unique per project, immutable (rename = delete + create).
- `my_actions` is what the caller may do on that entry. `status` is included whenever any action is.
- `command_display` is the argv re-joined with POSIX quoting (or the raw line in shell mode). Display only.
- `spec_hash` is lowercase hex SHA-256 of the canonical JSON of `{"kind","spec"}` (keys sorted, no whitespace).
- `Host` gains `"programs": HostPrograms|null`. `Service` gains `"catalog_entry": string|null` (the entry name when `source='owned'`).

### Writing a program entry

Create and replace take `{"name","kind":"program","command":string,"shell":bool,"dir","run_as","env"?,"port"?,"restart"?}` (`name` only on create).
1. **`command`** (1–4096 bytes, one line) is split into argv on the server with POSIX shell-word rules: single quotes, double quotes and backslash escapes are honoured; nothing else is interpreted.
2. With `shell=false`, the line is refused with `bad_request` and a message naming the feature if it contains, outside quotes: `|`, `&`, `;`, `<`, `>`, `$`, a backquote, `*`, `?`, `[`, `(`, `)`, `{`, `}`, `~` at word start, or a newline. A first word `cd` is refused ("use the working directory field").
3. Leading words of the form `KEY=value` (KEY matching the env key rule) are moved into `env`; a duplicate key is `bad_request`. The next word is `exec`; the rest are `args`.
4. With `shell=true`, nothing is split: `exec` = `/bin/sh`, `args` = `["-c", <line>]`.
5. **`exec`** is an absolute path or a bare name matching `^[A-Za-z0-9._+-]{1,128}$`; absolute paths match `^/[A-Za-z0-9._+/-]{1,255}$` with no `..` segment. `args`: ≤ 32, each ≤ 4096 bytes, no NUL or newline.
6. **`dir`**: absolute, ≤ 255 bytes, no `..` segment, no NUL, no newline.
7. **`run_as`**: `^[a-z_][a-z0-9_-]{0,31}$`, not `root`.
8. **`env`**: ≤ 64 keys; key `^[A-Z_][A-Z0-9_]{0,63}$`; value ≤ 4096 bytes, no NUL or newline. A key matching `(?i)(password|passwd|token|secret|credential|api[_-]?key|private[_-]?key)` is `bad_request` ("use secret_env", which is wave 2). `PATH` and `HOME` are reserved (`bad_request`).
9. `port` 1–65535 or null; `restart` default `on-failure`.
10. `secret_env` present, or `kind` other than `program` → `not_implemented`.

The API does not check host paths or accounts; the host policy and helper do (agent-protocol v1.3).

### Routes

| Method | Path | Who | Body | Result |
|---|---|---|---|---|
| GET | `/v1/projects/{id}/catalog` | viewer+ | — | `{"items":[CatalogEntry]}`; viewers see only entries where `my_actions` is non-empty |
| POST | `/v1/projects/{id}/catalog/parse` | owner | `{"command","shell"}` | `{"exec","args","env","command_display"}` or `bad_request` (rules 1–5; the preview for the form) |
| POST | `/v1/projects/{id}/catalog` | owner | create body | CatalogEntry, 201; `conflict` if the name exists |
| GET | `/v1/projects/{id}/catalog/{name}` | viewer+ with any action | — | CatalogEntry |
| PUT | `/v1/projects/{id}/catalog/{name}` | owner | replace body | CatalogEntry. A changed `spec_hash` voids every host approval of the entry. |
| DELETE | `/v1/projects/{id}/catalog/{name}` | owner | — | `null`. Running units are not stopped (stop first); approvals and grants are deleted. |
| GET | `/v1/projects/{id}/catalog/{name}/permissions` | owner | — | `{"grants":[CatalogGrant]}` |
| PUT | `/v1/projects/{id}/catalog/{name}/permissions` | owner | `{"grants":[CatalogGrant]}` | the stored grants. A `user:` subject must be a current member (else `bad_request`); ≤ 100 grants. |
| GET | `/v1/hosts/{id}/catalog-approvals` | admin+ of the host's owning project | — | `{"items":[Approval]}` (pending and approved; voided ones are gone) |
| POST | `/v1/hosts/{id}/catalog-approvals` | admin+ of the host's owning project | `{"entry_id","spec_hash"}` | Approval; `conflict` if `spec_hash` is no longer the entry's current hash |
| DELETE | `/v1/hosts/{id}/catalog-approvals/{entry_id}` | admin+ of the host's owning project | — | `null` |
| POST | `/v1/projects/{id}/catalog/{name}/run` · `/stop` · `/restart` | grant for that action | `{"host_id","idempotency_key"}` | ProgramStatus, or 202 PendingConfirmation (below) |
| POST | `/v1/projects/{id}/catalog/{name}/logs` | grant `logs` | `{"host_id","lines"?:1..1000 (default 200)}` | ProgramLogs |
| POST | `/v1/projects/{id}/catalog/{name}/status` | any grant | `{"host_id"}` | ProgramStatus |
| POST | `/v1/confirmations/{confirmation_id}` | the same user | `{}` | the result the original call would have returned |

**Default grants** of a new entry: `role:admin` → run, stop, restart, logs; `role:viewer` → logs. The owner always has every action and cannot be listed as a subject.

**Action checks, in order** (the first failure answers):
1. The caller is a member of project `{id}` (`not_found`) with a grant for the action (`forbidden`).
2. Host relation: the host is owned by `{id}`; or it is shared with `{id}` at level `deploy` **and** has an approval for (entry, host) with the entry's current `spec_hash`. Otherwise `not_found` if the host is not visible to the project at all, `forbidden` for a `metrics` share, and for a `deploy` share without a valid approval: `forbidden` with message "waiting for the host owner's approval" — this upserts a pending Approval (requested_by = caller) for the current `spec_hash`.
3. Host policy: `Host.programs` is non-null, `enabled`, lists `run_as`, and allows shell when `shell=true` — otherwise `forbidden` with the reason. Host offline → `unavailable`.
4. **Confirmation (D25):** if the caller's session kind is `api_delegated` (a call through MCP), run/stop/restart do nothing and answer **202** `{"confirmation_id","plan","expires_at"}`. `plan` is a sentence such as "will restart program tra-backend (pnpm run dev, as joe) on pi1". The id is 43-char base64url, stored hashed, single use, valid 5 min, bound to the user, route, entry `spec_hash` and body. `POST /v1/confirmations/{confirmation_id}` re-runs checks 1–3 and then acts; an unknown, used, expired or foreign id → `not_found`; a changed `spec_hash` → `conflict`. Console sessions skip this step. logs and status never need it.
5. **Idempotency:** run/stop/restart (and the confirm call) require `idempotency_key` (1–128 chars `[A-Za-z0-9._:-]`). The server stores (user, key) → request hash and response for 24 h. The same key with the same request returns the stored response without acting; with a different request → `conflict`.
6. The task goes to the agent (agent-protocol v1.3) and waits up to 30 s; outcomes map as in v1.0.1 item 5, plus `task_reject` `policy_denied` → `forbidden` and agent `error_code` `policy_denied` → `forbidden`.

**Audit:** `catalog.create`, `catalog.update` (old and new `spec_hash`), `catalog.delete`, `catalog.permissions`, `catalog.approve`, `catalog.approval_revoke`, `program.run`, `program.stop`, `program.restart` (host, entry, outcome; `via='mcp'` from delegated tokens), `program.confirmation_issued`. Reads (logs, status) are not audited.

**Owned services:** a `services` report entry with `owner` (agent-protocol v1.3) is stored with `source='owned'`, `name` = the entry name, `catalog_entry` = the entry name, and `project_id` = the project whose slug the agent reports. If no such project/entry exists, the item is stored as `discovered` with its unit name.

**Migration** 0007 adds the catalog, grants, approvals, confirmations and idempotency tables, `hosts.programs` (jsonb), and `services.catalog_entry`, with `deploybot_api` grants in the house style and a hand-run down script.

### v1.6.1 amendments (2026-10-15, from the wave 1 review)

1. **Confirm body:** `POST /v1/confirmations/{confirmation_id}` takes `{"idempotency_key"}` (rule 5 already required it; the table's `{}` was wrong).
2. **Environment that changes what runs is refused** (`bad_request`, "this variable changes what the program runs"): keys starting `LD_` or `DYLD_`, and `NODE_OPTIONS`, `NODE_PATH`, `BASH_ENV`, `ENV`, `IFS`, `PYTHONPATH`, `PYTHONSTARTUP`, `PERL5OPT`, `PERL5LIB`, `RUBYOPT`, `RUBYLIB`. This applies to hoisted `KEY=value` words too.
3. **Approvals show the environment:** `Approval` gains `"env":{KEY:value}` and `"exec","args"`, so the host owner sees everything that will run.
4. **Idempotency is reserved before acting:** the server inserts the (user, key, request hash) row first; a second request with the same key while the first is still running → `conflict` ("request in progress"); after the outcome the stored response is filled in. A failed dispatch stores its error response too, so a retry with the same key replays it.

## v1.7 service presence and views (2026-10-15, approved spec `docs/spec-services-view.md`; additive)

1. **`Service` gains `"gone_at"`**: RFC 3339 time or `null`. `null` means the service was in its host's latest services report (`hello.services` or a `services` frame).
2. **Ingest:** every services report from a host is that host's full set. In the same transaction as the upsert, the server sets `gone_at = <report time>` on that host's rows that are not in the report and still have `gone_at = null`, and sets `gone_at = null` on every reported row. Rows of other hosts are never touched. A host that sends no report changes nothing. `display_name`, health checks and ids are unaffected.
3. **`GET /v1/projects/{id}/services`** gains the query parameter `view`:
   - `all` (default): every row, as in v1.3.
   - `deploybot`: rows with `source` in (`owned`, `declared`), gone or not.
   - `deploybot_and_discovered`: the `deploybot` rows plus rows with `source = 'discovered'` and `gone_at IS NULL`.
   - Any other value → `bad_request`.
   - `q`, `host_id`, `sort`, `dir`, `limit`, `cursor` and `total` apply within the view. A cursor carries its view; using it with a different `view` → `bad_request`.
   - `sort=status`: rows with `gone_at` set come after all rows with `gone_at = null`, for both values of `dir`; within each group the v1.3 order applies.
4. Other routes are unchanged; `GET /v1/hosts/{id}/services` and `GET /v1/services/{id}` simply include `gone_at`.
5. Migration `0008_service_gone.sql` backfills: for each host, rows whose `last_seen_at` is earlier than that host's greatest `last_seen_at` get `gone_at = last_seen_at`.

## v1.8 catalog images (2026-10-15, approved spec `docs/spec-catalog-images.md` rev 3, wave 2a; additive)

`kind:"image"` entries are now accepted. `not_implemented` remains only for managed files (wave 2b). The `docker run` paste prefill is console-side; the API has no parse route for it.

### Objects

```jsonc
CatalogEntry   // kind "program" unchanged; for kind "image": "spec":ImageSpec, "command_display" = the image reference
ImageSpec {"kind":"service"|"job","image":string,"command":[string]|null,"entrypoint":[string]|null,
           "ports":[{"host":int,"container":int,"public":bool}],"env":{KEY:value},"env_sets":[string],
           "secret_env":{KEY:"<set>.<KEY>"},"volumes":[{"name":string,"target":string,"read_only":bool}],
           "restart":"no"|"on-failure"|"unless-stopped","user":string,"healthy_after_sec":int,
           "memory_mb":int|null,"timeout_sec":int}
ImageDeploy {"id","entry_id","host_id","digest","spec_hash","outcome","exit_code":int|null,
             "started_at","finished_at","by":"<user uuid>","via":"console"|"mcp"}
ImageStatus {"container":string|null,"state":"running"|"exited"|"created"|"paused"|"restarting"|"dead"|"missing",
             "exit_code":int|null,"restart_count":int,"started_at":string|null,"finished_at":string|null,
             "digest":string|null,"ports":[int]}
EnvSet      {"name","keys":[{"key","is_secret":bool,"value":string|null}],"updated_at","updated_by"}   // value only when !is_secret
Registry    {"host","username","updated_at","updated_by"}
Outcome     ∈ succeeded|unchanged|failed|timeout|rolled_back|rollback_failed|image_unavailable|forbidden_spec|conflict
```

- `HostPrograms` gains `"images":bool,"docker":bool` (from the agent's hello).
- `Service` gains `"container_name":string|null` and `"image_digest":string|null`; owned image services have `catalog_entry` set and `kind:"container"`.
- `CatalogGrant` actions gain **`deploy`** (deploy, redeploy, job run, rollback, remove). New image entries default to owner/admin `[run,stop,restart,logs,deploy]`, viewer `[logs]`. `my_actions` may include `deploy`.

### Writing an image entry

Create/replace take `{"name","kind":"image","spec":ImageSpec}` (`name` on create only). Validation (`bad_request` with the field named, never echoing a value):
1. `name` as v1.6, and additionally must not end with `-` or `-prev`.
2. `image`: a Docker reference (`[registry/]path[:tag][@sha256:<64 hex>]`), ≤ 255 bytes, lowercase path; a reference with neither tag nor digest is stored with `:latest` appended.
3. `command`, `entrypoint`: null or ≤ 32 strings, each ≤ 4096 bytes, no NUL/newline.
4. `ports`: ≤ 16; host/container 1–65535; host ports unique within the entry.
5. `env`: the v1.6 item 8 rules (including the secret-looking-key refusal) and the v1.6.1 denylist.
6. `env_sets`: ≤ 8 names of sets in the **same project** (`^[a-z0-9][a-z0-9-]{0,47}$`); unknown → `bad_request`.
7. `secret_env`: ≤ 64; key as env; value `<set>.<KEY>`, split at the first `.`, set in the same project and in `env_sets` or not (either is allowed), KEY present in that set; unknown → `bad_request`.
8. `volumes`: ≤ 8; `name` `^[a-z0-9][a-z0-9-]{0,47}$` (the host volume is `<project-slug>-<name>`); `target` absolute, ≤ 255 bytes, no `.`/`..` segment; targets unique.
9. `restart`: default `unless-stopped` for services; forced `no` for jobs.
10. `user`: empty, a name `^[a-z_][a-z0-9_-]{0,31}$`, a positive uid, or `name|uid:name|gid`; uid/gid 0, `root` in either position and leading-zero numbers are refused.
11. `healthy_after_sec` 5–600 (default 30, service only); `timeout_sec` 10–3600 (default 600, job only); `memory_mb` null or 16–65536.
12. Env precedence when resolving: `env_sets` in order < `secret_env` < `env`.

`spec_hash` for image entries = SHA-256 of canonical `{"kind","spec"}` **plus** `"env_keys": {<set>: sha256(sorted key names joined by "\n")}` for every referenced set, so a key added to or removed from a set changes the hash (and voids shared-host approvals). Values never enter the hash.

### Routes

| Method | Path | Who | Body | Result |
|---|---|---|---|---|
| POST | `/v1/projects/{id}/catalog/{name}/deploy` | grant `deploy` | `{"host_id","force"?:bool,"idempotency_key"}` | `{"deploy":ImageDeploy,"status":ImageStatus,"lines":[string]}` |
| POST | `/v1/projects/{id}/catalog/{name}/rollback` | grant `deploy` | `{"host_id","digest","idempotency_key"}` | same |
| POST | `/v1/projects/{id}/catalog/{name}/remove` | grant `deploy` | `{"host_id","idempotency_key"}` | `{"status":ImageStatus}` |
| GET | `/v1/projects/{id}/catalog/{name}/deploys?host_id=` | any grant | — | `{"items":[ImageDeploy]}` newest first, ≤ 50 |
| GET | `/v1/projects/{id}/env-sets` | admin+ | — | `{"items":[EnvSet]}` |
| GET / PUT / DELETE | `/v1/projects/{id}/env-sets/{set}` | admin+ | PUT `{"dotenv":string}` | EnvSet / EnvSet / `{}` |
| PATCH | `/v1/projects/{id}/env-sets/{set}/keys/{key}` | admin+ | `{"is_secret":bool}` | EnvSet |
| GET | `/v1/projects/{id}/registries` | admin+ | — | `{"items":[Registry]}` |
| PUT / DELETE | `/v1/projects/{id}/registries/{host}` | admin+ | PUT `{"username","token"}` | Registry / `{}` |

- run/stop/restart/logs/status (v1.6 routes) dispatch by kind; for images, status/run/stop/restart return `ImageStatus`, logs return `{"container","lines","truncated"}`.
- Deploy/rollback/remove follow the v1.6 check order (permission → host relation/approval → `HostPrograms.images && docker` else `bad_request` "this host does not run containers" → D25 confirmation for `api_delegated` → idempotency) and additionally: one in-flight deploy/rollback/remove per (host, entry) → `conflict`. `rollback.digest` must be one of the last 3 distinct `succeeded`/`unchanged` digests for (entry, host) → else `bad_request`. Outcomes are in the 200 body (`deploy.outcome`), not HTTP codes; only request-level failures use envelope errors.
- D25 `plan` for deploy/rollback: "will deploy <entry> (<image or digest>) on <host>"; it never contains env values.
- **dotenv grammar** (PUT): UTF-8, ≤ 256 KiB, ≤ 256 keys. Each line is blank, a `#` comment (after optional whitespace), or `[export ]KEY=value`. KEY as env rule and denylist (the secret-looking-key refusal does **not** apply: these are secrets). value is the rest of the line, optionally wrapped in one pair of matching `'` or `"` (removed); no escapes, no interpolation, no NUL; a value ≤ 4096 bytes. Duplicates or malformed lines → `bad_request` listing `"line N"` and the key if parsed, never a value. PUT replaces the set; every key becomes `is_secret:true`. *(v1.9.1, 2026-10-17, Joe: a key that already exists in the set keeps its `is_secret`; only keys new to the set start `is_secret:true`. Keys left out of the PUT are removed as before.)* *(v1.19: `mode` `create` and `merge` amend this; "replaces" holds for `replace`.)* `DELETE` of a set referenced by any entry → `conflict`.
- Registry `{host}`: a registry hostname[:port], lowercase; `username` ≤ 255, `token` ≤ 4096, no CR/LF/NUL. GET never returns tokens.
- Secret values and tokens are AES-256-GCM encrypted with `BOT_SECRETS_KEY` (key_version), a random 96-bit nonce per ciphertext, associated data `project_id|set|key` (or `project_id|registry|host`); they are decrypted only to build an `image_deploy`/`image_rollback` task for a host of the entry's project (or an approved shared host), and only the registry whose host equals the image reference's registry host is attached.
- No MCP-facing route returns env values; there are no MCP tools for env sets or registries.

**Audit:** `catalog.deploy`, `catalog.rollback`, `catalog.remove` (entry, host, digest, outcome, `via`; *v1.9.1, 2026-10-17: the row's `error_code` is the outcome when it is not `succeeded` or `unchanged`*), `env_set.replace`, `env_set.key_visibility`, `env_set.delete` *(v1.19 adds `env_set.merge`, item 3)*, `registry.put`, `registry.delete` — key names, set names and registry hosts only.

### v1.8.1 amendment (2026-10-16, Joe approved; from the wave 2a browser smoke)

`ImageStatus.state` gains **`unknown`**: the API did not receive the container's state in time (a deploy or rollback whose task ran past its budget, outcome `timeout`). With `unknown`, `container`, `exit_code`, `started_at`, `finished_at` and `digest` are null, `restart_count` is 0 and `ports` is `[]`. An empty string is never sent. Consumers show it as "unknown".

### v1.8.2 amendment (2026-10-16, Joe approved; from the dev console after the wave 2a cleanup)

`view=deploybot` (and so `deploybot_and_discovered`) no longer lists an **owned** row that is gone **and** whose `catalog_entry` no longer exists in the row's project: nothing can start it again. Such a row stays in the database and in `view=all`, with its `display_name` and health checks. If an entry with the same name is created and deployed, the next report clears `gone_at` and the row is listed again. Declared rows, owned rows with `gone_at = null`, and owned rows with `catalog_entry = null` are unchanged. `total` and cursors follow the narrower set. Before (v1.7): `deploybot` = `source` in (`owned`, `declared`), gone or not.

## v1.9 managed files (2026-10-16, approved spec `docs/spec-catalog-files.md` rev 1, wave 2b; additive)

The v1.8 line "`not_implemented` remains only for managed files" no longer applies: `files` is accepted in ImageSpec.

### Objects

```jsonc
ProjectFile  {"name","is_secret":bool,"size":int,"sha256":string|null,"updated_at","updated_by"}   // sha256 (hex of the content) only when !is_secret
ProjectFileContent {"name","content":string,"size":int,"sha256":string,"updated_at","updated_by"} // plain files only
ImageSpec    gains "files":[{"file":string,"target":string}]   // default []
```

- `HostPrograms` gains `"files":bool` (from the agent's hello, agent-protocol v1.5); absent = false.

### Project files

1. **Name:** `^[a-z0-9][a-z0-9._-]{0,63}$`, not `.` or `..`.
2. **Content:** valid UTF-8, 1–65536 bytes, no NUL. Secret content is stored as AES-256-GCM ciphertext with `key_version` (the env-set key, `BOT_SECRETS_KEY_FILE`); plain content as text. At most **64 files per project** (a new name beyond that → `bad_request`).
3. **Routes** (owner/admin of the project, **console sessions only**: an `api_delegated` token → `forbidden` before any decrypt; `mcp_access` / `mcp_refresh` tokens are already 401 `invalid_token` on every `/v1` route by the audience rule; a non-member → `not_found`):

| Method | Path | Body | Payload |
|---|---|---|---|
| GET | `/v1/projects/{id}/files` | — | `{"items":[ProjectFile]}` sorted by name, not paginated |
| GET | `/v1/projects/{id}/files/{name}` | — | `ProjectFileContent`; a secret file → `forbidden` |
| PUT | `/v1/projects/{id}/files/{name}` | `{"content":string,"secret":bool}` (`secret` omitted = `true`) | `ProjectFile`; creates or replaces content and flag |
| DELETE | `/v1/projects/{id}/files/{name}` | — | `null`; `conflict` listing the entry names while any image entry of the project references it |

   Errors never echo content. An unknown name on GET/DELETE → `not_found`.
4. **Audit:** `file.put` (`name`, `is_secret`, `size`), `file.delete` (`name`). Never content or `sha256` of a secret file.

### Writing an image entry: rule 13

13. `files`: ≤ 16 rows. `file` must name a file of the **same project** (unknown → `bad_request`), and each `file` appears at most once per entry (a slot holds one copy per name). `target`: absolute, ≤ 255 bytes, normalised (no empty, `.` or `..` segment, no trailing `/`), not `/`, not `/proc`, `/sys`, `/dev` or under them; unique among `files` targets and `volumes` targets; not under a volume target, and no volume target under it.

`spec_hash` covers `files` as written (names and targets), as part of `spec`. File content never enters `spec_hash`, so a content change does not void a shared-host approval.

### Deploy and rollback

- The API resolves each `spec.files[i].file` to its current content when it builds an `image_deploy` / `image_rollback` task (agent-protocol v1.5). *(v1.9.1, 2026-10-17: the API still sends the current content on every rollback. When the digest is the entry's kept `-prev` container, the agent restores that container with its own previous files and spec instead (agent-protocol v1.5.2).)* A file deleted since the entry was saved → `bad_request` "file <name> no longer exists", and no task is sent.
- The v1.4.1 frame-size check includes the resolved files: an oversized frame → `bad_request` "the resolved environment and files are too large for one task".
- `GET` of a catalog entry and task records show `files` as names and targets only. MCP `list_catalog` shows file names only (targets are dropped, as for volumes).

### Env-set values and redaction (Joe 2026-10-16, from the pi3 mirror)

1. **dotenv values are literal:** `$` and `\` are allowed and taken as-is (no escapes, no interpolation means nothing is interpreted). NUL is still refused, and the 4096-byte value limit stays. Inline `env` in an ImageSpec keeps the v1.6 item 8 rules.
2. **Image tasks send `secret_keys`** (agent-protocol v1.5), and the API's own redaction of image results and logs uses the same secret-only set: secret env-set values after precedence, the registry token, and secret-file items. Structured fields of `ImageStatus`, `ImageDeploy`, `outcome`, `digest` and `exit_code` are never redacted. Only free-text `lines` are. *(v1.9.1: the API's own exact-value pass uses the agent's marker `[REDACTED]`; a refused file delete is audited as `file.delete` (`name`, `outcome`:`"conflict"`); a completed one adds `outcome`:`"deleted"`.)*

### Migration

`0010_project_files.sql` (+ hand-run `.down.sql`): table `app.project_files` (`project_id` uuid FK → projects ON DELETE CASCADE, `name`, `is_secret`, `content_plain` text NULL, `nonce` bytea NULL, `content_cipher` bytea NULL, `key_version` int NULL (the env-set sealing scheme: a fresh 12-byte nonce per write, AAD `project_id|file|name`), `sha256` text, `size` int, `updated_by` uuid, `updated_at` timestamptz; PK `(project_id, name)`; CHECK exactly one of `content_plain` / `content_cipher` per `is_secret`). Grants to `deploybot_api` as for `app.env_values`.


## v1.10 health-check edits over MCP (2026-10-18, approved spec `docs/spec-health-check-editor.md` rev 1; additive)

1. **Body:** `PUT /v1/services/{id}/health-checks` takes `{"checks":[HealthCheck without id/service_id],"idempotency_key"?}`. `idempotency_key` (v1.6 rule 5 format) is **required** for an `api_delegated` session and ignored for a console session.
2. **Console sessions:** unchanged; the set is replaced at once and pushed as `config_update`.
3. **`api_delegated` sessions (D25):** after the existing checks (permission, share rules, body rules: max 10, port in `Service.ports`, protocol/path/interval/expect), nothing changes and the answer is **202** `{"confirmation_id","plan","expires_at"}`. `plan` lists protocol, port, path and interval only: "will set 2 health checks on <service label> (<host>): tcp 443, http 8080 /health every 60 s", or "will remove all health checks from <service label> (<host>)". When the set has a tcp and an http check on the same port, the plan adds "(tcp <port> is redundant with http <port>)"; the set is still applied as given.
4. **Binding:** the confirmation is bound to the user, the service, a hash of the request body, and a hash of the service's check set at issue time; 5-minute expiry; single use; the id has the same format as catalog confirmations.
5. **Confirm:** `POST /v1/confirmations/{confirmation_id}` (`{"idempotency_key"}`) re-runs permission and share checks; the check set changed since issue → `conflict`; unknown, used, expired or foreign id → `not_found`; otherwise it replaces the set, pushes `config_update`, and returns the same body as the console PUT (`{"checks":[HealthCheck]}` as today).
6. **Audit:** `health_checks.replace` as today (`via='mcp'` for delegated), plus `health_checks.confirmation_issued` (service, host, check count). No path query strings are added; paths are stored as given.
7. **Migration 0011** (+ hand-run `.down.sql`): `app.service_confirmations` (`token_hash` bytea PK, 32 bytes; `user_id` uuid; `project_id` uuid; `service_id` uuid FK → `app.services` ON DELETE CASCADE; `action` text CHECK = `'health_checks'`; `request_hash` bytea 32; `current_hash` bytea 32; `checks` jsonb NOT NULL (an array: the normalized request checks in request order, applied on confirm, never returned); `idempotency_key` text; `created_at`, `expires_at` timestamptz; `used_at` timestamptz NULL). Grants to `deploybot_api` as for `app.catalog_confirmations`. The confirm route looks an id up in `catalog_confirmations` first, then `service_confirmations`.
8. **MCP:** a new mutating tool `set_health_checks` (`service_id`, `checks`, `idempotency_key`, all required) issues the plan through this route; the existing `confirm` tool applies it with the same `idempotency_key`, as for the catalog tools. The tool checks `expect_status_min`/`max` within 200–399 like the API. A read-only tool `get_health_checks` (`service_id`) returns the service's current checks from `GET /v1/services/{id}` (Joe 2026-10-18), so a client reads the set before proposing a replacement.
9. **Amendment (Joe 2026-10-18, from the w17 worker notes):** item 7 gains the `checks` column; item 8 takes the caller's `idempotency_key` (rev 1 said the tool generates it, which left the plan unconfirmable) and the 200–399 expect bounds. From the w17 security review: in `plan` the service label and each HTTP path are Go-quoted (`on "swag" (pi3): tcp 443, http 8080 "/health" every 60 s`), and a disabled check ends with ` (disabled)`; confirm's compare, mark-used and replace run in one transaction holding the service row lock.

## v1.10.1 service rows of revoked hosts, and ownership that ends (2026-10-20, approved spec `docs/spec-service-rows.md` rev 1)

1. **Revoke marks rows gone:** every path that sets a host's `revoked_at` (`DELETE /v1/hosts/{id}/credential`, project soft delete) also sets `gone_at = <revoke time>` on that host's service rows whose `gone_at` is null, in the same transaction. Migration `0012_revoked_host_rows.sql` backfills `gone_at = hosts.revoked_at` for rows of hosts revoked earlier (no schema change; the down script is a no-op).
2. **Views:** `view=deploybot` and `view=deploybot_and_discovered` exclude rows whose host is revoked. `view=all` (default) still returns them, now with `gone_at` set. `total` and cursors follow the view.
3. **Ownership ends with the report:** when a services report places an item as `discovered` (no owner, or an owner whose catalog entry no longer exists) and the existing row for that `(host_id, name)` is `owned`, the row becomes `source = discovered`, `catalog_entry = null`, `project_id` = the host's project. Its `id`, `display_name` and health checks are kept. An owned report still upgrades a discovered row to owned; `declared` rows are unchanged by reports. Before: the row stayed `owned` with its old `catalog_entry`.

## v1.11 Docker health status (2026-10-18, approved spec `docs/spec-docker-health.md` rev 1; additive)

1. **Ingest:** accepts the agent-protocol v1.6 docker health sample only from a host whose `hello.programs.docker_health` is true, and stores it in `metrics.health_results` with `protocol='docker'`, `check_id`, `port`, `http_status` and `latency_ms` NULL. No migration.
2. **Config:** `config.docker_health: true` is sent (in `hello_ok` and `config_update`) only to a host that advertised `hello.programs.docker_health: true`.
3. **Precedence (Joe 2026-10-18, "Docker wins"):** `Service.health` derives from the latest docker row when one is at most 2 minutes old; otherwise from the latest non-docker row, as before (`unknown` when none or stale). Docker `success` → `up`, `unhealthy` → `down`.
4. **`Service.health`** gains `"source": "docker"|"check"|null` (the kind of row the status came from; `null` when there is no row). `latency_ms` is null for docker. The project services list sorts by the derived status as before.
5. **Health history** (`GET /v1/services/{id}/health`, MCP `service_health_history`) includes docker rows: `protocol:"docker"`, `check_id`, `port`, `http_status`, `latency_ms` null, `result` `success|unhealthy`.
6. **Console and MCP:** the health badge says the source; the health-check dialog notes that the checks apply only when Docker reports nothing; MCP passes `source` through.
7. **ImageSpec `healthcheck`** *(rev 2, approved 2026-10-20)*: optional `{"test":[argv…],"interval_sec","timeout_sec","retries","start_period_sec"}` on image entries. `test` 1–32 strings, each 1–1024 bytes, no NUL (Docker `CMD` form; no shell form). `interval_sec` 5–300 (default 30), `timeout_sec` 1–60 (default 5, must be < `interval_sec`), `retries` 1–10 (default 3), `start_period_sec` 0–600 (default 0). On a job → `bad_request` (`healthcheck applies to services`). Errors name the field and never echo the argv. Part of `spec_hash`. v1.12 plans show it like `command`.
8. **Gate** *(rev 2)*: deploy or rollback of an entry with `healthcheck` to a host whose `hello.programs.healthcheck` is not true → `bad_request` with message `this host's agent does not support healthcheck`, before confirmation and before any task. An entry without it sends no key.
9. **Deploy wait** *(rev 2)*: for a service with `healthcheck` the outcome is `succeeded` only after Docker reports `healthy` (and any configured checks pass); `unhealthy` or timeout → rollback, outcome as today. MCP `list_catalog` shows `healthcheck: true|false`, not the argv.

## v1.12 catalog writes over MCP (2026-10-20, approved spec `docs/spec-mcp-catalog-writes.md` rev 1)

1. **Delegated writes are planned (D25):** for an `api_delegated` session, `POST /v1/projects/{id}/catalog`, `PUT /v1/projects/{id}/catalog/{name}` and `DELETE /v1/projects/{id}/catalog/{name}` run every existing check (owner, image rules 1–13, name conflict, entry exists), change nothing, and answer **202** `{"confirmation_id","plan","expires_at"}`. Console sessions are unchanged.
2. **Image only:** a delegated create/replace with `kind` ≠ `image`, or an update/delete of a program entry → `forbidden` ("program entries are edited in the console").
3. **Idempotency:** `idempotency_key` (v1.6 rule 5) is required for delegated sessions — in the create/replace body, and as JSON body `{"idempotency_key"}` on DELETE; ignored for console sessions. Same key + same body → the same pending confirmation; same key + different body → `conflict`.
4. **Plan** (never env-set values, `secret_env` values or file contents): create lists image, kind, ports (loopback/public), env set names, inline `env` as `KEY="value"` (non-secret by rule 5), `secret_env` keys with their `<set>.<KEY>` source, volumes, files, command, **entrypoint** (when set), **restart** (when not the kind's default), user, memory; update lists only changed fields (inline env with old and new values) and the number of host approvals it voids; delete states the approvals and grants removed and that running containers are not stopped. Every caller- or user-controlled text in the plan (project name, image, argv elements, user, restart, names, targets, env values) is Go-quoted (`strconv.Quote`) when it is not a plain token, so no `;`, space, U+2028 or U+202E appears raw. *(Amended 2026-10-20 from the w22 reviews.)*
5. **Confirm:** `POST /v1/confirmations/{confirmation_id}` re-runs the checks and applies the stored normalized body exactly as a console write (same `spec_hash`, approval voiding, audit). Create requires the name still free, update/delete require the entry's current `spec_hash` to equal the plan's, else `conflict`; referenced env sets, files and volumes must still exist, else `bad_request`. Single use, 5 min, bound to user, project, route, entry name and request hash; unknown/used/expired/foreign → `not_found`.
6. **Console-only for delegated sessions:** `PUT /v1/projects/{id}/catalog/{name}/permissions`, host catalog approvals (`POST /v1/hosts/{id}/catalog-approvals`, `DELETE /v1/hosts/{id}/catalog-approvals/{entry_id}`; corrected 2026-10-20, the route table never had `PUT`) and the catalog env routes → `forbidden`.
7. **Migration 0013** (+ hand-run `.down.sql`): `app.catalog_write_confirmations` (`token_hash` bytea PK 32; `user_id`, `project_id` (FK ON DELETE CASCADE); `action` CHECK in (`create`,`update`,`delete`); `entry_name` text; `base_spec_hash` text NULL; `body` jsonb NULL, never returned; `request_hash` bytea 32; `idempotency_key` text; `created_at`, `expires_at`, `used_at`). Grants to `deploybot_api` as for `app.service_confirmations`. The confirm route also looks ids up here. A row is deleted when its write is applied, and expired rows are deleted when a new plan is stored; project delete deletes the project's rows. *(Amended 2026-10-20.)*
8. **Audit:** `catalog.create|update|delete` with `via='mcp'` whenever applied from a confirmation, whatever session confirms it; a delegated call refused by item 6 is audited with `decision=denied`, `via='mcp'`; new `catalog.write_confirmation_issued` (action, entry name).
9. **MCP tools:** `create_catalog_entry(project_id, name, spec, idempotency_key)`, `update_catalog_entry(project_id, name, spec, idempotency_key)` (replace), `delete_catalog_entry(project_id, name, idempotency_key)`; each returns the plan and confirmation id; `confirm` applies it.

## v1.12.1 console-only write routes for delegated sessions (2026-10-20, approved spec `docs/spec-delegated-routes.md` rev 1)

1. **Refused for `api_delegated`:** `POST /v1/projects`; `PATCH` and `DELETE /v1/projects/{id}`; `POST /v1/projects/{id}/members`; `PATCH` and `DELETE /v1/projects/{id}/members/{user_id}`; `POST /v1/projects/{id}/transfer-ownership`; `POST /v1/projects/{id}/join-tokens`; `DELETE /v1/projects/{id}/join-tokens/{token_id}`; `POST /v1/projects/{id}/invites`; `DELETE /v1/projects/{id}/invites/{invite_id}`; `DELETE /v1/hosts/{id}/credential`; `PUT` and `DELETE /v1/hosts/{id}/shares/{project_id}`; `PATCH /v1/services/{id}`. Each → `403 forbidden` (message `forbidden`), nothing written, before membership is checked (a non-member or unknown id gets the same answer).
2. **Audit:** one row per refusal: action `delegated.denied`, `decision=denied`, `via='mcp'`, `error_code=forbidden`, detail `{"route":"<METHOD pattern>"}`, `project_id` and `host_id` null.
3. **Default deny:** a mutating route added after v1.12.1 is refused for `api_delegated` in the same way unless this contract names it as allowed or as deciding in its handler.
4. **Unchanged:** console sessions; GET routes; `POST /v1/hosts/{id}/snapshot`, `POST /v1/services/{id}/probe`, catalog `logs`, `status` and `parse`, `POST /v1/auth/signout` and `POST /v1/confirmations/{confirmation_id}` stay allowed; the D25 routes and the v1.12 console-only routes keep their own behaviour and audit actions.

## v1.13 env-set editing, secret previews, admin catalog edit (2026-10-20, approved spec `docs/spec-env-editing.md` rev 1)

1. **Preview rule:** `preview(v)` is the first 4 runes of `v` followed by `…` when `v` has at least 16 runes, otherwise `••••`. It never exposes more than 4 runes.
2. **`GET /v1/projects/{id}/env-sets/{set}`:** each key is `{key, is_secret, value, preview}`. A secret key has `value: null` and `preview` = `preview(v)`; a plain key has `preview: null`.
3. **`PUT /v1/projects/{id}/env-sets/{set}/keys/{key}`** (new), body `{"value": string, "is_secret"?: bool}`: sets one key, creating the set if needed. `is_secret` applies only to a new key (default `true`); for an existing key it must be absent or equal to the stored flag, else `bad_request` with message `secret flag is fixed; delete and re-add the key`. Key and value rules are the dotenv rules (key grammar, no NUL, no line break, value ≤ 4096 bytes); errors never echo the value. Response: the key as item 2 shows it. Audit `env_set.key_put` (`set`, `key`, `created`), no value.
4. **`DELETE /v1/projects/{id}/env-sets/{set}/keys/{key}`** (new): removes one key; an unknown key is `not_found`. Audit `env_set.key_delete` (`set`, `key`).
5. **`PATCH /v1/projects/{id}/env-sets/{set}/keys/{key}`** is removed (`404 not_found`). A key's secret flag is fixed once created.
6. **`PUT /v1/projects/{id}/env-sets/{set}`** (dotenv) accepts optional `new_keys_secret` (bool, default `true`) for keys new to the set; existing keys keep their flag.
7. **Auth:** all env-set routes stay owner or admin, console session only. Items 3 and 4 are refused for `api_delegated` by v1.12.1 item 3 (`delegated.denied`). Adding or deleting a key changes the set's key list and voids host approvals of entries that use the set, as a whole-set replace does; a value-only change does not.
8. **`PUT /v1/projects/{id}/catalog/{name}`:** owner or admin (was owner), for console sessions and for the delegated D25 plan and its confirm. Create, delete and permissions stay owner-only.
9. **Redaction:** in log lines, status text (including the `ImageStatus`/`ImageDeploy` string fields) and error strings, an env-set secret value of at least 6 bytes (raw, base64 and JSON-escaped forms; the floor is unchanged from v1.4) is replaced by `preview(v)` instead of `[REDACTED]`. Audit rows keep their fixed phrases. Registry tokens and secret-file pieces keep `[REDACTED]`. Text already redacted by an older agent (`[REDACTED]`) is accepted as is.

## v1.14 container log limit (2026-10-21, approved spec `docs/spec-container-logging.md` rev 1; additive)

1. **ImageSpec `logging`** (optional; services and jobs): `{"max_size_mb": int 1–100, "max_files": int 1–10}`. When present, both keys are required; an unknown key, a missing key, a non-integer or an out-of-range value → `bad_request` naming the field (`logging.max_size_mb`, `logging.max_files`). `null` or absent means the agent default (10 MB × 3). It is part of `spec_hash`.
2. **Gate:** a deploy, rollback or job run of an entry whose spec has `logging` to a host whose `programs.logging` is not `true` → `bad_request` with message `this host's agent does not support log settings`, and no task is sent. Entries without `logging` are not gated. `HostPrograms` gains `logging` (bool, false when absent).
3. **Plans (v1.12 delegated writes):** create lists `logging max_size_mb=<n> max_files=<k>` when set; update lists it when it changes (old → new, or added/removed).
4. Catalog GET returns `logging` inside the spec as stored. No migration.
5. **Rollback note:** an older API's strict stored-spec parser cannot read an entry with `logging`; clear it before rolling the API back.

## v1.15 health failure policy (2026-10-23, approved spec `docs/spec-health-policy.md` rev 1; additive, one migration)

1. **ImageSpec `health_policy`** (optional; **services only**): `{"on_deploy_failure":"rollback"|"stop"|"keep","on_unhealthy":"none"|"restart"|"rollback","unhealthy_after_sec":int 15–600,"max_actions_per_hour":int 1–10}`.
   - **Keys are optional; defaults** `rollback`, `none`, `45`, `3`. Stored as written.
   - **Errors** → `bad_request` naming the field (`health_policy.on_unhealthy`, …): unknown key, bad enum, non-integer or out of range. A `job` with the key → `bad_request` (`health_policy applies to services`). `on_unhealthy` other than `none` without `healthcheck` → `bad_request` (`health_policy.on_unhealthy needs a healthcheck`).
   - **`spec_hash`:** `null` or absent means all defaults. It is part of `spec_hash` only when present and different from all-defaults, so an entry at the defaults keeps its hash.
2. **Gate:** a deploy, rollback or run of an entry whose `health_policy` differs from the defaults, to a host whose `programs.health_policy` is not `true` → `bad_request` `this host's agent does not support health policies`, and no task is sent. `HostPrograms` gains `health_policy` (bool, false when absent). Entries at the defaults are not gated, and their stored `health_policy` key (if any) is dropped from the task sent to an older agent.
3. **`auto_action` ingest** (agent-protocol v1.9 item 5).
   - **Validation:** the server checks the frame. `project` must be a project the host belongs to, `entry` an image entry in it, plus the enums and the `reason` length. An invalid frame is acknowledged and dropped, with one `warn` log line.
   - **Storage:** a valid frame writes:
     - an `audit_log` row: `action` `catalog.auto_<action>`, `via` `agent`, `actor_user_id` null, target = the entry, detail = host, container, reason, result;
     - an `image_deploys` row: `outcome` `auto_restarted` | `auto_rolled_back` | `auto_gave_up`, `via` `agent`, `by` null, `digest` from the frame (or the entry's last digest when null), `spec_hash` of the entry's last deploy to that host, and `started_at` = `finished_at` = `t`.
   - **Replay:** the frame `id` is stored, and a replayed `id` is acknowledged without a second row.
4. **Migration 0014.** `app.image_deploys.by_user` becomes nullable, with a CHECK that `by_user` is null only when `via = 'agent'`. `via` allows `agent`, and `outcome` allows the three `auto_*` values. A new unique column `auto_action_id uuid NULL` makes replays idempotent. Down migration: delete the `via='agent'` rows, then restore the old constraints.
5. **Shapes.** `ImageDeploy` gains `"via":"console"|"mcp"|"agent"` and `"by":"<user uuid>"|null`. `Outcome` gains `auto_restarted|auto_rolled_back|auto_gave_up`. Deploy history lists them like any other row.
6. **Plans (v1.12):** create lists `health_policy on_deploy_failure=… on_unhealthy=… unhealthy_after_sec=… max_actions_per_hour=…` when present and not all defaults; update lists the change.
7. **Rollback note:** an older API's strict stored-spec parser cannot read an entry with `health_policy`, and an older API stores a host as programs-disabled when its hello carries `health_policy`. Roll back the agents first, clear `health_policy` from every entry, then roll back the API and migration 0014.

### v1.15 rev 2 amendments (2026-10-23, Joe "yes" after the w31 reviews; these replace the matching rules above)
- **Item 1:** `on_deploy_failure: "keep"` combined with `on_unhealthy` other than `none` → `bad_request` (`health_policy.on_unhealthy cannot be combined with keep`). The unknown-key error names the key: `unknown field health_policy.<key>` when the key is at most 64 bytes of `[a-z_]`, else `invalid health_policy`.
- **Item 3, ingest authority.** A frame is stored only when **all** of these hold. Otherwise it is acknowledged, dropped, and logged once at `warn`:
  - the entry is an image `service`;
  - its stored effective `on_unhealthy` equals the action (`restart` ↔ `restart`, `rollback` ↔ `rollback`); `gave_up` and `auto_failed` need any value other than `none`;
  - `container` equals `<project>-<entry>`;
  - the entry has at least one `succeeded` or `unchanged` deploy row on this host.
- **Item 3, rate.** At most **20** stored frames per (entry, host) per rolling hour. Beyond that, frames are acknowledged and dropped, with one `warn` per hour. Rows use **server time** for `started_at` and `finished_at`. The frame's `t` goes into the audit detail only. The host flag check comes before any database read.
- **Item 3, outcome.** `result: "error"` is stored as outcome `auto_failed`, with a null digest, whatever the action.
- **Item 4, migration 0014:**
  - `outcome` also allows `auto_failed`;
  - CHECK `(via = 'agent') = (auto_action_id IS NOT NULL)`;
  - CHECK `outcome NOT IN (auto outcomes) OR via = 'agent'`;
  - agent rows have `in_flight = false`;
  - the down migration runs in one transaction.
- **Item 5, history.**
  - Deploy history returns the newest 50 non-automatic rows, plus at most the newest 20 automatic rows that are newer than the oldest returned row, merged by time.
  - Automatic rows are never rollback candidates.
  - Clients treat the newest `auto_rolled_back` as the current digest for display. They do not count automatic rows toward the "same image" rule.
  - MCP's short history lists non-automatic rows only.

### v1.15 rev 3 amendment (2026-10-23, agent-protocol v1.9 rev 3b)
- **Entry names:** an image entry `name` must not end with `-old` either. The rule is the same as for `-prev`, and it applies on create and replace (`bad_request`). The agent uses `<project>-<entry>-old` while it rolls back. Neither dev nor production has an entry name ending in `-old` (checked 2026-10-23).

## v1.16 registry tags and tag digests (2026-10-23, approved spec `docs/spec-registry-tags.md` rev 4; additive, no migration)

1. **`GET /v1/projects/{id}/image-tags?image=<ref>`**: owner or admin, console session only. Other session kinds, including MCP-delegated tokens, get 403. These routes are not under `/catalog/`, so they cannot shadow catalog entry names.
   - `image` is parsed like a catalog image reference. Its tag and digest are ignored.
   - The registry host must be either a host stored in **this** project's registries, or one of `docker.io`, `ghcr.io`, `quay.io`, `gcr.io`, `registry.gitlab.com`, `public.ecr.aws`, `mcr.microsoft.com`. Any other host → `bad_request`, msg `registry not allowed`. `docker.io` is fetched from `registry-1.docker.io`.
   - **Outbound:**
     - HTTPS on **port 443 only**. A stored host with another port gets no lookups; deploys are unaffected.
     - Every connection is checked after DNS resolution, on the IP actually dialled: the registry, token realms, redirects (at most 3) and `Link` next pages. Each must use HTTPS, port 443 and have no userinfo.
     - Refused addresses (an IPv6 address with a zone is always refused):
       - loopback, private, link-local, CGNAT, multicast, unspecified, `0.0.0.0/8` and broadcast;
       - the reserved and documentation ranges (`192.0.0.0/24`, `192.0.2.0/24`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`) and `168.63.129.16`;
       - IPv6 ULA, link-local, site-local, `100::/64`, NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`), IPv4-compatible (`::/96`), 6to4 (`2002::/16`), and IPv4-mapped forms of all of these.
     - Next pages must stay on the same host. A redirect to another host (including a subdomain) drops `Authorization`.
     - Limits: 5 s per request, 12 s per lookup, 32 KiB of response headers, 1 MiB per body, at most 5 pages and 500 tags read.
     - At most 32 lookups run at once across all users. Beyond that → `rate_limited`.
     - Proxy settings are ignored. `BOT_API_REGISTRY_ALLOW_PRIVATE=1` (dev only) lifts the address and port checks, not HTTPS.
   - **Auth:**
     - The Distribution v2 Bearer flow, with scope `repository:<path>:pull`.
     - The stored username and token are sent as Basic only to a realm whose host and port equal the registry's, or to `auth.docker.io` for `docker.io`. Otherwise the token is requested anonymously.
     - A plain `Basic` challenge from the registry host itself is answered with that host's stored credential.
   - **Response** in the standard envelope: `payload {"items":[{"tag":string}],"truncated":bool}`, at most 200 items.
     - Tags must match `^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`; others are dropped.
     - Order: semver-like tags descending, then `latest`, then the rest in reverse lexical order.
     - `truncated` is also true when the page or tag cap left tags unread.
2. **`GET /v1/projects/{id}/image-digest?image=<repo>:<tag>`**: same access, hosts, outbound rules, auth, errors and logging as item 1. It shares item 1's rate bucket.
   - `image` must have a tag and no digest, otherwise `bad_request` `invalid image`.
   - The API sends `HEAD /v2/<path>/manifests/<tag>` with Accept for the OCI index, the Docker manifest list v2, the OCI manifest and the Docker manifest v2, and reads `Docker-Content-Digest`. If that header is missing, it sends a GET (body ≤ 4 MiB) and uses the sha256 of the body.
   - **Response:** `payload {"digest":"sha256:<64 hex>"}`.
3. **Errors:**
   - `bad_request` (400): `invalid image` or `registry not allowed`.
   - `rate_limited` (429) with `Retry-After`: the per-user limit or the global concurrency cap.
   - A missing secrets key while a registry is stored → the same response the deploy routes give.
   - `registry_unavailable` (502) with `payload {"reason"}`:
     - `auth_failed`: a stored credential that was sent and refused.
     - `not_found`: a missing repository or tag. This includes a 401 or 403 after the token flow when no stored credential was sent.
     - `timeout`.
     - `unreachable`: DNS, connect, TLS, an address or port refusal, or a registry answering 429 or 5xx.
     - `bad_response`: a malformed answer or an invalid digest.
   - Registry bodies are never echoed.
4. **Cache, rate limit and logging:**
   - Results are cached 60 s per (project, host, path), and per (project, host, path, tag) for digests. At most 1024 entries; when full, a new result is not stored. Errors are not cached.
   - 20 uncached lookups per user per minute, shared by both routes.
   - Each lookup logs kind, host, path, outcome (`cache` for hits), duration, and `auth: stored|anonymous|cache` (whether the credential was sent; `cache` on a hit). Tokens are never logged. No audit row.
5. **Registry tokens** (amends the v1.8 sealing paragraph): besides building `image_deploy`/`image_rollback` tasks, a stored token is decrypted for items 1 and 2, and only for an image whose registry host equals the stored host in the same project.
6. **Unchanged:** no MCP tools for registries or tags. The stored image reference and the deploy path are unchanged; a picked `repo:tag@sha256:…` is stored as typed. An older API answers 404, and clients fall back to a plain input.

## v1.17 plain env-set keys made secret (2026-10-25, approved spec `docs/spec-env-make-secret.md` rev 1; additive, no migration)

1. **`POST /v1/projects/{id}/env-sets/{set}/secret`**, body `{"keys":[KEY,...]}`: marks plain keys secret. Owner or admin, console session only; refused for `api_delegated` (`delegated.denied`) like v1.13 items 3 and 4. No MCP tool.
   - `keys`: 1–200 entries, each matching the dotenv key grammar, no duplicates; otherwise `bad_request`. An unknown set or any key not in the set → `not_found` naming the key; nothing changes.
   - One transaction: each named plain key's stored value is sealed with the current secrets key and stored as secret (`value` cleared). Keys already secret are skipped. The set's `updated_at`/`updated_by` change when any key changed.
   - Response: the set as v1.13 item 2 shows it.
   - Audit: one `env_set.key_secret` row per changed key (`set`, `key`), never the value. Skipped keys get no row.
2. **Amends v1.13 item 5:** a secret key stays secret; a plain key becomes secret only through item 1. `PUT …/keys/{key}` still refuses any flag change (`secret flag is fixed; delete and re-add the key`).
3. **Approvals and hash:** unchanged. `spec_hash` covers key names only, so item 1 voids no host approvals (as a value-only change, v1.13 item 7). The next `image_deploy`/`image_rollback` lists the key in `secret_keys` (agent-protocol v1.5).
4. **Rollback note:** keys marked secret stay secret under an older API, which reads them normally.

## v1.18 input guards and services list health checks (2026-10-26, approved spec `docs/spec-api-input-guards.md` rev 1; no migration)

1. **Amends v1.11 item 7 (`healthcheck.test`):** `test` is the command and its arguments; Deploybot sends Docker `["CMD", test…]`. A `test` whose **first** item is exactly `CMD`, `CMD-SHELL` or `NONE` (case-sensitive) → `bad_request` with message `invalid healthcheck.test: give the command itself, without CMD, CMD-SHELL or NONE`. A later item with those words is allowed. Applies to `POST /v1/projects/{id}/catalog`, `PUT /v1/projects/{id}/catalog/{name}` and the catalog write confirm (`POST /v1/confirmations/{confirmation_id}`). Stored entries are not rewritten: an entry stored before v1.18 keeps deploying as stored, and its next save is refused until fixed. For a shell, use `["sh","-c","…"]`.
2. **Amends v1.13 item 3 (`PUT /v1/projects/{id}/env-sets/{set}/keys/{key}`):** for an **existing secret** key, `"value": ""` → `bad_request` with message `a secret value cannot be empty; delete the key instead`. Nothing is stored and no `env_set.key_put` audit row is written. An existing plain key and a new key (secret or plain) still accept `""`. The whole-set `PUT` (v1.13 item 6) in `replace` mode and v1.17 item 1 are unchanged (merge mode: v1.19 item 1).
3. **Amends v1.3 (`GET /v1/projects/{id}/services`):** optional query parameter `include=health_checks` adds `"health_checks": [HealthCheck]` to every row: the same objects and order as `GET /v1/services/{id}` (ordered by id), `[]` when the service has none. Any other `include` value → `bad_request` `invalid include`. Without `include`, rows are unchanged. `include` is not part of the cursor's query hash. Access is unchanged: every row is one the caller may read with `GET /v1/services/{id}` (v1.3 item 3). `GET /v1/hosts/{id}/services` is unchanged.
4. **Unchanged:** agent-protocol; MCP `list_services` does not send `include`, so its output is unchanged; `get_health_checks` still reads `GET /v1/services/{id}`.
5. **Rollback note:** an older API ignores the unknown `include` parameter (v1.3 refuses only bad values of the parameters it lists), so a v1.18 console against a v1.17 API gets rows without `health_checks` (each row shows that its checks could not be read). Ship the API before or with the console. Items 1 and 2 store nothing new.

## v1.19 env set paste modes (2026-10-26, approved spec `docs/spec-env-set-merge.md` rev 1; no migration)

1. **Amends v1.13 item 6 (`PUT /v1/projects/{id}/env-sets/{set}`, dotenv):** optional `"mode": "replace" | "create" | "merge"` (absent or `null` means `"replace"`; any other value → `bad_request`).
   - `replace`: unchanged (creates or replaces the set; keys left out are removed).
   - `create`: creates the set; if a set with that name exists → `conflict` with message `env set {set} already exists`, and nothing changes. The existence check and the insert are one transaction.
   - `merge`: the set must exist, else `not_found` with message `no env set {set}`. Each pasted key is upserted as `PUT …/keys/{key}` would: an existing key keeps `is_secret` and takes the new value; a new key is secret when `new_keys_secret` (default `true`). Keys not in the paste are kept. An empty value for an existing **secret** key → `bad_request` `a secret value cannot be empty; delete the key instead` (as v1.18 item 2). All pasted keys apply in one transaction; any error writes nothing.
   - Grammar and limits (v1.9.1 dotenv grammar, ≤ 256 KiB, ≤ 256 keys per paste), permissions (owner/admin, console sessions only; delegated/MCP refused) and the response (the EnvSet) are unchanged for every mode.
2. **Approvals and hash:** as today. A merge that adds a key name changes `spec_hash` of entries using the set and voids their host approvals (v1.13 item 7); a value-only merge does not.
3. **Audit:** `replace` and `create` keep `env_set.replace` (`set`, `keys`). `merge` writes `env_set.merge` with `set`, `keys` (the pasted names) and `created` (the names new to the set). Never values. A delegated (MCP) call is refused `forbidden` before the body is read, and audited `env_set.replace` (denied, `via` mcp) for every mode.
4. **Rollback note:** a v1.18 API refuses the unknown `mode` field (`bad_request "invalid json"`), so the console that sends `mode` ships after this API and rolls back first.

## v1.27 (renumbered from v1.20, spec-env-generate rev 4) generated env values, show-once reveal, env set creation over MCP, `${KEY}` references (2026-10-28, approved spec `docs/spec-env-generate.md` rev 1, rev 2 §3.7, rev 3; migration 0017)

*Status: items 1–3, 7, 8, 10 implemented on dev 2026-11-15 (merge 6d0613b, in v0.2.34); items 4–6 (phase B) implemented on dev 2026-11-16 (bot-control-plane merge 04b5e67), not yet released. Before a release with phase B, `POST …/env-sets` is 404, delegated env-set GETs are refused, and `needs_value` is always false.*

1. **Generate (amends v1.13 item 3, `PUT /v1/projects/{id}/env-sets/{set}/keys/{key}`):** the body may carry `"generate": {"kind": "password"|"hex"|"base64url", "length"?: int}` instead of `value` (`value` and `generate` together, an unknown kind, or `length` outside 16–64 or given for a kind other than `password` → `bad_request`). `password` = `length` (default 32) characters from `[A-Za-z0-9]`; `hex` = 32 random bytes as 64 lowercase hex; `base64url` = 32 random bytes, unpadded base64url (43 characters). Values come from a CSPRNG in the API and are sealed at once. A generated key is secret: on a new key it is created secret (any `is_secret: false` → `bad_request`); on any existing key → `bad_request` `{KEY} already has a value; edit it to change it` and nothing changes (rev 3, Joe 2026-10-28: no regenerate; the one exception, from phase B, is an item 4 `needs_value` placeholder, which `generate` fills). Audit `env_set.key_put` gains `generated: "<kind>"`. Console sessions only (delegated: v1.12.1 item 3, unchanged). Other messages: `send value or generate, not both`; `a generated value is secret`; `generate.kind must be "password", "hex" or "base64url"`; `generate.length must be 16 to 64`; `generate.length is only for kind password`. A body with neither keeps the v1.19 meaning (value `""`).
2. **Key flags (amends v1.13 item 2):** each env key object gains `reveal_pending` (bool) and `needs_value` (bool). A generated secret's `preview` is always `••••` (a stored `generated` flag, cleared by any typed value write), so no part of a generated value appears outside reveal. Redaction of agent output (deploy, rollback and logs results, the deploys list, audit reasons) likewise replaces a generated value, or a value that references one, with `••••` rather than a preview. Generation sets `reveal_pending: true`; any other value write (key PUT with `value`, set PUT in any mode, make secret) sets it false. `needs_value` is set only by item 4 placeholders and cleared by the key's first value write (value or generate).
3. **Reveal (new, amends api-v1:424 and design D31):** `POST /v1/projects/{id}/env-sets/{set}/keys/{key}/reveal`, owner or admin, **console session only** (refused for `api_delegated` by the route, `delegated.denied`). Atomically clears `reveal_pending` and returns `200 {"key","value"}` with `Cache-Control: no-store`; if the key was not pending → `conflict` `the value of {KEY} was already shown or was not generated; edit it to set a new value`; unknown set or key → `not_found`. At most one 200 per generated value. Audit `env_set.key_reveal` (`set`, `key`). This is the only route that returns a secret env value to a client.
4. **Create with key names only (new):** `POST /v1/projects/{id}/env-sets`, body `{"name", "keys": [{"key", "generate"?: <item 1 object>, "is_secret"?: bool}], "idempotency_key"}`; 1–64 keys, valid and distinct names; there is no value field (unknown fields → `bad_request "invalid json"`). Owner or admin. A key with `generate` is created as item 1 (always secret; `"is_secret": false` with `generate` → `bad_request` `a generated value is secret`); a key without is a **placeholder**: empty value, `needs_value: true`, secret unless the caller sends `"is_secret": false` (default true; spec rev 3 §3.4: the secret status comes from the caller, and secret is preferred because plain → secret is one way). An existing set name → `conflict` `env set {set} already exists`. Console session: applied at once, `201` with the EnvSet. **Delegated session (decides in its handler, v1.12.1 item 3):** `202 {"confirmation_id","plan","expires_at"}`; the plan lists each key as `KEY (generated <kind>[, <n> characters], secret)` or `KEY (empty; fill it in the console)`, never a value; `POST /v1/confirmations/{confirmation_id}` with the same `idempotency_key` creates the set (expiry 5 minutes, single use, same user; replay and key-mismatch rules as catalog writes, v1.12). Audit `env_set.create` (`set`, `keys`, `generated` names with kinds, `placeholders`, `via`) and `env_set.write_confirmation_issued` for a plan.
5. **Delegated read (amends api-v1:425):** `GET /v1/projects/{id}/env-sets` and `GET …/env-sets/{set}` answer an `api_delegated` session (owner or admin, as for the console reads; others → `forbidden`) with each key's `key`, `is_secret`, `reveal_pending`, `needs_value` only — no `value` (even for plain keys) and no `preview`. MCP tools `create_env_set` and `list_env_sets` use items 4 and 5; there is still no MCP route or tool that returns an env value or preview.
6. **Needs value at deploy or run:** image `deploy` and `rollback`, and program `run` and `restart` (console and delegated, at issue and at confirm; `stop` is not affected) are refused with `bad_request` `env {KEY} in set {set} has no value yet; set it in the console` when any key the entry resolves (its env sets and `secret_env` sources) has `needs_value`. No task is sent.
7. **`${KEY}` references (image entries; amends v1.9.1 "no interpolation" and api-v1:481):** after the rule-12 merge, every resolved value is scanned once: `${NAME}` (NAME matching the env key rule) is replaced by the final merged value of NAME for that entry; `$${` produces a literal `${`; any other `$` is literal; a referenced value is inserted as stored and is not scanned again. An unknown NAME → `bad_request` `env {KEY} references ${NAME}, which is not set for this entry`; an unterminated `${` → `bad_request` `env {KEY} has an unterminated ${`; a result over 4096 bytes → `bad_request` `env {KEY} is longer than 4096 bytes after references`. Never a value in a message. These messages hold only key names and fixed text, so they are returned and audited without the command-line redaction (which would otherwise rewrite `env SECRET_URL has …` as `env SECRET_URL=[REDACTED] …`). A key whose value references a secret key is treated as secret for that task (`secret_keys`, redaction). Errors apply to console deploys and rollbacks and to delegated ones at issue (no plan is stored) and at confirm. If the key holding the reference is secret, the unknown-name message does not echo NAME: `env {KEY} has a reference that is not set for this entry`. The 4096-byte check runs on the resolved value, so a catalog editor can learn a referenced secret's length class; accepted (2026-10-28). Plans, `list_catalog`, the stored spec and previews show the unresolved text. Program entries' env stays literal. *(Rev 2026-10-28, implementation: `${…}` whose inside is not a valid key name — e.g. `${lower}`, `${A:-b}`, `${}` — stays literal, as any other `$`. Precisely: when the text after `${` up to the next `}` is not a valid key name, those two characters `${` are literal and scanning continues right after them, so in `${A${B}` the inner `${B}` is resolved. A `${` with no `}` after it is an error even if the name would be invalid.)*
8. **Env hash (agent-protocol v1.14, renumbered from v1.10):** to an agent whose `hello.programs.env_hash` is true, `image_deploy` and `image_rollback` carry `env_hash` = lowercase hex HMAC-SHA256 over the resolved env (after item 7) as `KEY=value\n` lines in byte order of KEY, keyed by HKDF-SHA256(`BOT_SECRETS_KEY`, no salt, info `deploybot env hash v1`). Not stored by the API; omitted when no secrets key is configured. The host object's `programs` gains `env_hash` (bool) from the agent's hello.
9. **Unchanged:** permissions of all other env routes, the dotenv grammar and limits, the response shapes apart from items 2 and 5, and MCP's lack of any value-returning tool.
10. **Rollback note:** an API older than v1.27 refuses `generate` and the new routes and deploys `${…}` literally; replace references with literal values before rolling the API back. Migration 0017 down drops `reveal_pending`, `needs_value`, `generated` and `app.env_set_confirmations`.

## v1.21 last deployed time on catalog entries (2026-10-30, approved spec `docs/spec-catalog-last-deployed.md` rev 1; no migration required, an index may be added)

1. **CatalogEntry** gains `"last_deployed_at": RFC3339 | null`. It is read-only, set by the server, and returned by `GET /v1/projects/{id}/catalog`, `GET /v1/projects/{id}/catalog/{name}` and the replace response (`PUT …/catalog/{name}`). A create response, and the delegated catalog-write confirm response (create or update), carry `null`. On a replace, a failure to read the time does not fail the request; the field is then `null`. A write body that contains it is refused as an unknown field, like other server-set fields.
2. **Image entry:** the newest `started_at` of the entry's image deploys on any host and with any outcome, counting only deploys started `via` `console` or `mcp`. Rows written by the agent (automatic restart, rollback or give-up) never count. `null` when there is none.
3. **Program entry:** the newest time of an allowed `program.run` audit row for the entry's name in its project, `via` `console` or `mcp`, at or after the entry was created (so an entry re-created under an old name starts empty). An allowed run that then failed or found the host offline still counts. Denied runs, stop and restart don't count. `null` when there is none.
4. **Visibility and order are unchanged.** The list keeps its name order and viewer filtering. The value is computed per list request with a fixed number of queries, not one per entry.
5. **MCP `list_catalog`** returns `last_deployed_at` for each item.
6. **Unchanged:** all writes, `spec_hash`, deploy, run and rollback, and the agent protocol.

## v1.22 secrets for program entries (2026-11-13, approved spec `docs/spec-program-secrets.md` rev 1; additive, no migration)

1. **ProgramSpec** gains `"env_sets":[string]` and `"secret_env":{KEY:"<set>.<KEY>"}`, with the image rules 6, 7 and 12 (v1.8): at most 8 sets from the entry's project; `secret_env` at most 64 keys, each set and key existing; precedence `env_sets` in order < `secret_env` < `env`; the merged env is at most 64 keys. The write body takes the same two fields. Rule 10 no longer refuses `secret_env` for `kind: "program"`. Inline `env` keeps rule 8, including the secret-looking-key refusal ("use secret_env").
2. **Program `spec_hash`** adds `"env_keys":{<set>: sha256(sorted key names)}` for each referenced set, as images (v1.8 line on `spec_hash`). Env-set values never enter it; inline `env` values stay in it. Deleting a set referenced by a program entry is `conflict`; adding or deleting a key re-hashes program entries that reference the set and voids their host approvals (v1.13 rule, now for both kinds). A value-only change does neither.
3. **Run.** For an entry with non-empty `env_sets` or `secret_env`, `program_start` carries the resolved, merged `env` and `"secret_keys":[KEY]` (agent-protocol v1.11): the keys whose final value comes from an env-set key with `is_secret: true`; a key overridden by inline `env` is not secret. An entry without sets keeps the v1.6 frame exactly (no `secret_keys`). `program_restart` is unchanged and reuses the host's stored spec: a changed value takes effect at the next Run.
4. **Gates, before any task is sent:** host `programs.secrets` not true → `bad_request` "this host's agent does not support program secrets"; no server secrets key → `unavailable` (as images); resolved spec over 64 KiB → `bad_request` "the resolved environment is too large for one task".
5. **HostPrograms** gains `"secrets":bool` (from `hello.programs.secrets`; absent = false).
6. **Redaction.** Program results (`lines` of run, logs and status) are redacted with the entry's secret value forms (raw, base64, JSON-escaped; values of 6 bytes or more), shown as `preview(v)` (v1.13 item 9), on top of the keyword regex. Audit rows never hold values. `Approval` for a program entry shows inline `env` values and the `env_sets` and `secret_env` references, never a resolved secret.
7. **Unchanged:** program entries stay console-only over MCP (v1.12 item 2); `list_catalog` returns no program env; no route returns a value; `${KEY}` references stay image-only (v1.27 item 7, renumbered from v1.20).

## v1.23 the host's projects allowlist covers programs (2026-11-13, approved spec `docs/spec-projects-gate-programs.md` rev 1; additive, no migration)

1. **HostPrograms** gains `"projects":[string]|null` (from `hello.programs.projects`, agent-protocol v1.12; null when the agent did not send it). It is returned only to members of the host's owning project. Every project's view of the host gains `"project_listed":bool|null`: whether that project's slug is in the list (null when the list is unknown). A sharing project never sees the other slugs.
2. **Pre-check before any task.** When the list is known and does not contain the entry's project slug: a program Run, and an image deploy, rollback or restart, are `forbidden` "this host's allowlist does not include project <slug>". For a shared host the check comes after the approval check (v1.6 item 2). When the list is unknown the request goes on as before.
3. **Agent policy refusals.** A `task_reject` or failed `task_result` with `policy_denied`, for program or image tasks, maps to `forbidden` with the message "the host refused this under its policy (runner.toml)" instead of "forbidden". The agent's `reason` stays out of the message.
4. **Unchanged:** the host relation, share levels and approvals; all other refusal mappings.

### v1.22 and v1.23 amendments (2026-11-14, approved by Joe "Approve all three"; from the API slice reviews; still additive, no migration)

Before release; nothing of v1.22 or v1.23 has shipped.

1. **Plain set values are part of a program's approval (amends v1.22 items 2 and 6).** For a program entry, a set key "takes part" when it is a key of a set in `env_sets`, or the target of a `secret_env` reference. Program `spec_hash` adds `"env_values":{<set>: sha256(sorted "KEY=value" lines)}` over the taking-part keys of that set with `is_secret: false`. Secret values never enter it. A change to such a plain value re-hashes the program entries that reference the set and voids their host approvals; a change to a secret value still does neither. Image entries are unchanged. `Approval` for a program entry gains `"env_set_values":{<set>:{KEY:value}}`, the same plain taking-part keys with their values; secret keys are never listed there.
2. **Secret values may not change what runs (amends v1.22 item 1 and item 4).** For a program entry, a key whose final value comes from a set key with `is_secret: true` must not be one of the v1.6.1 item 2 keys, nor `GIT_SSH`, `GIT_SSH_COMMAND`, `GIT_ASKPASS`, `SSH_ASKPASS`, `JAVA_TOOL_OPTIONS`, `_JAVA_OPTIONS`, `JDK_JAVA_OPTIONS`, `PYTHONHOME`, `PYTHONINSPECT`, `GCONV_PATH`, `DOTNET_STARTUP_HOOKS`, `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, or `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY` in either case. A `secret_env` key in that list is `bad_request` "this variable changes what the program runs" on write; at Run, such a key coming from a secret set key is the same `bad_request` before any task. Inline `env` and plain set keys keep the v1.6.1 list (their values are approved).
3. **A plain secret-looking set key (amends v1.22 item 4).** A further Run gate, before any task and before the idempotency claim: a final key that matches the inline-env secret-looking rule (v1.22 item 1, "use secret_env") but whose value comes from a set key with `is_secret: false` → `bad_request` "<KEY> looks like a secret but is not marked secret in set <set>; mark it secret". It is audited as a denied `program.run`.
4. **`policy_denied` wording (amends v1.23 item 3).** The mapping applies to a `policy_denied` refusal of any task kind, not only program and image tasks.

## v1.24 organizations: hosts belong to an org, projects get host access (2026-11-14, approved spec `docs/spec-organizations.md` rev 1; migration 0015, phase 1 additive)

Supersedes the owning-project model of v1.1/v1.1.1 (shares) and the per-entry host approvals of v1.6 item 2. Agent protocol unchanged: no frame or task spec gains a key, and project slugs keep their meaning (globally unique, immutable, agent grammar `^[a-z0-9][a-z0-9-]{0,63}$`). Env generate (v1.27, renumbered from v1.20) moves its migration to 0017 (0016 is organizations phase 2, v1.26).

1. **Objects.**
   - `Org{id,name,slug,role,created_at}`. `role` is the caller's org role: `owner|admin|member`. There is exactly one owner. `slug` is unique, `^[a-z0-9][a-z0-9-]{0,63}$`, server-side only.
   - `OrgMember{user_id,email,name,role,added_at}`.
   - `Project` gains `org_id`, and `via:"member"|"org"`. `via:"org"` means the caller reaches the project only as an org owner or admin, and then `role` is `"admin"`.
   - `Host` gains `org_id` and `access_level:"metrics"|"deploy"|null`, the level of the project in whose list the host appears (null in org-scoped views). It also gains `access:[HostAccess]`, returned only to org admins and never to a delegated (MCP) session. Delegated sessions also get `programs.projects: null`. `project_id`, `relation` and `share_level` stay in phase 1 as deprecated mirrors:
     - `project_id` is the backfilled former owner, or for a new host the token's grant project when the grant is `deploy`, otherwise the org's oldest live project. It is returned only to org admins and members of that project; everyone else gets `null`;
     - `relation` is `"owner"` when `access_level` is `"deploy"`, `"shared"` when it is `"metrics"`, and empty (with `share_level` null) when `access_level` is null;
     - `share_level` equals `access_level`.
     They are removed in v1.26 (renumbered; v1.25 is shared networks).
   - `HostPrograms.projects`, the host's allowlist of project slugs (v1.23 item 1), is returned only to org admins and owners. Every other caller gets `projects: null`. Each project view keeps its own `project_listed`. This replaces v1.23's "members of the host's owning project": a project with access never sees the other projects' slugs.
   - `HostAccess{project_id,project_name,level,granted_by,granted_at}`. `granted_by` is a user id, or `null` for the former owner's row backfilled by the migration (a grant applied by a join token at enrolment records the token's creator); `granted_at` is always set.
2. **Permissions** (replaces the v1.1 matrix; roles are org roles or the caller's role in a project that has access):
   - see the host, status, metrics, processes and snapshot: org admin+, or any member of a project with access (any level);
   - services list, service read, logs, probe and health history: org admin+ (all services), or a member of a `deploy` project (that project's services only);
   - deploy, run, stop, restart, rollback, remove and health-check edits: org admin+ (all), or an admin+ of a `deploy` project (its own entries and services);
   - credential revoke, access grants and join tokens: org admin+ only;
   - anyone else: `not_found`.
   - Org members without a project role see no host.
   - Project capabilities (v1.0) also allow an org owner or admin of the project's org, as `admin`. `CapProjectWrite` (rename, delete, transfer) stays with the project owner, or the org owner.
3. **Routes** (all `delegatedDeny` unless noted):
   - `GET /v1/me` gains `orgs:[Org]`; its projects carry `org_id` and `via`.
   - `POST /v1/orgs` `{name}` → `Org`. The caller becomes owner. Any session.
   - `GET /v1/orgs/{id}` (member+).
   - `PATCH /v1/orgs/{id}` `{name}` (admin+).
   - `DELETE /v1/orgs/{id}` (owner): soft delete; it revokes every host's credential and soft-deletes every project, as v1.0 project delete.
   - `GET /v1/orgs/{id}/members` (member+).
   - `POST /v1/orgs/{id}/members` `{email,role:"admin"|"member"}`, existing users only, as v1.0 project members (admin+).
   - `PATCH` and `DELETE /v1/orgs/{id}/members/{user_id}` (admin+; self-removal for anyone; the owner cannot be removed).
   - `POST /v1/orgs/{id}/transfer-ownership` `{user_id}` (owner).
   - `GET /v1/orgs/{id}/projects` (member+: projects the caller can reach).
   - `POST /v1/orgs/{id}/projects` `{name}` (admin+) → `Project`. The caller becomes project owner. `POST /v1/projects` stays for one release: it creates the project in the caller's only org with role owner or admin, and is `bad_request` "choose an organization" when there are several, or none. A user who belongs to no org gets a personal org (owner, name "<name>'s org", slug `u-<12 hex of the user id>`) at sign-in (spec D-8), including existing users after the migration.
   - `GET /v1/orgs/{id}/hosts` (admin+): every host of the org, with `access`. Same paging as the project host list.
   - `POST /v1/orgs/{id}/join-tokens` (admin+) `{host_name_hint?, expires_in_sec?, grant?:{project_id, level}}`. The grant's project must be in the org. The response is as v1.0, plus `grant`.
   - `GET /v1/orgs/{id}/join-tokens` and `DELETE /v1/orgs/{id}/join-tokens/{token_id}` (admin+). List rows are the v1.0 `JoinToken` plus `grant`, which is `{project_id, level}` or `null` (never flattened); the POST response carries the same `grant`.
   - `POST /v1/projects/{id}/join-tokens` stays for one release: it needs org admin+ of the project's org, and creates an org token with `grant:{project_id:id, level:"deploy"}`. The project `GET` and `DELETE` routes list and revoke the tokens whose grant names that project.
   - `GET /v1/hosts/{id}/access` (org admin+) → `{items:[HostAccess]}`.
   - `PUT /v1/hosts/{id}/access/{project_id}` `{level}` (org admin+): an upsert. The project must be in the host's org, otherwise `not_found`.
   - `DELETE /v1/hosts/{id}/access/{project_id}`: org admin+, or admin+ of that project (leaving). It is `conflict` while the project still has services on the host, unless `?reassign=org`, which moves them to the org (`project_id` null, `catalog_entry` null, and `owned` becomes `discovered`; `declared` stays). Granting access on a revoked host, or a host of a deleted org, is `not_found`.
   - `/v1/hosts/{id}/shares` and `/v1/hosts/{id}/shares/{project_id}`, all methods: `410` `gone` "replaced by /access".
   - `DELETE /v1/hosts/{id}/credential`: org admin+.
   - `GET /v1/orgs/{id}/audit` (admin+): rows with that `org_id`.
4. **Catalog.**
   - Host approvals are removed. The `GET`, `POST` and `DELETE /v1/hosts/{id}/catalog-approvals*` routes are `410`.
   - A run or deploy on a host needs `access_level:"deploy"` for the entry's project. Without it: `forbidden` "this project has no deploy access to the host", where the caller can see the host; otherwise `not_found`.
   - The v1.23 allowlist pre-check is unchanged and comes right after the access check.
   - **Agents that report no `programs.projects`** (older than v0.2.8): program Run, Stop and Restart, and program entry placement, are allowed only for the host's single project with `deploy` access. Any other project, or every project when two or more have `deploy`, gets `forbidden` "this host's agent does not enforce a projects allowlist; upgrade it to 0.2.8". Image entries are not affected. (Spec D-10, Joe 2026-11-14.)
   - "Approved shared host" in v1.22 secret release means a host where the entry's project has `deploy` access.
   - Approval voiding (v1.22 amendment 1) no longer applies, but a changed `spec_hash` still deletes legacy `catalog_host_approvals` rows so a rollback to v0.2.29 sees no stale approvals (removed in phase 2). `spec_hash` is still computed. MCP plans (v1.12 item 4) no longer state the number of host approvals an update voids, nor the approvals a delete removes.
5. **Enrolment.**
   - Join tokens belong to an org.
   - Host names are unique per org, and `-2`-style suffixes are per org.
   - The token's `grant`, if any, becomes a `host_access` row in the enrolment transaction.
   - The `host.enroll` audit row carries `org_id`, plus the grant's project.
   - Wire frames are unchanged.
6. **Services.**
   - Discovered services with no Deploybot owner have `project_id: null` and are visible to org admins only.
   - An agent-reported owner `{project:<slug>, entry}` maps to that project only when the project is in the host's org and has `deploy` access. Otherwise the service is org-level.
   - `auto_action.project` must name such a project (amends v1.15).
   - An owned report never moves a service row away from another project, or from an org-level row. The row is left as it is and `service.owner_conflict` is audited. When ownership ends, the row keeps its `project_id`, so only that project can own it again. Known limit in phase 1: an org-level row (after `?reassign=org`, or a discovered row whose name matches an entry) is never claimed by a later owned report. It stays visible to org admins only.
7. **Project delete:**
   - it no longer revokes hosts;
   - it deletes the project's `host_access` rows;
   - it marks its services gone;
   - it revokes unused join tokens whose grant names the project;
   - it stops nothing on the host.
   `Settings` text: "Hosts stay in the organization."
8. **Audit.**
   - `AuditEntry` gains `org_id`. Every row with a host or project gets `org_id`, from the host or else the project. A denied request for a host the caller cannot see is written with no org, host or project id.
   - New actions: `org.create`, `org.rename`, `org.delete`, `org.transfer`, `org_member.add`, `org_member.update`, `org_member.remove`, `org_join_token.create`, `org_join_token.revoke`, `host.access_grant`, `host.access_update`, `host.access_revoke`, `service.owner_conflict`, and `host.migration_rename` (actor `system`, detail old and new name; written by migration 0015 and its down script for each host renamed under item 9).
   - The `host.share*` actions stay readable in history.
9. **Migration 0015** (phase 1, additive):
   - adds `orgs`, `org_members` and `host_access`;
   - adds `org_id` on `projects`, `hosts`, `join_tokens` and `audit_log`, plus `join_tokens.grant_project_id` and `grant_level`;
   - backfills: an explicit mapping on production (spec §4.1), and otherwise one org per project owner;
   - converts each host's former owning project into a `host_access` row at `deploy`, and same-org shares into `host_access` rows;
   - keeps `hosts.project_id`, `host_shares` and the approval tables, unused by the new API, so v0.2.29 still runs after a rollback;
   - relaxes `NOT NULL` on `services.project_id` (org-level discovered services), `hosts.project_id` (hosts enrolled with an org token that has no grant), `join_tokens.project_id` and `service_confirmations.project_id` (org tokens and org-level services), and adds `UNIQUE (org_id, name)` on hosts (`hosts_project_name_unique` stays). Before that constraint, a later host whose name another host of the same org already has (live hosts first, then by `enrolled_at`, then `id`) is renamed to the first 54 characters of its name plus `-` and the first 8 characters of its id; the down script does the same per project before `hosts_project_name_unique` applies again, and the before/after report lists every renamed host;
   - ships a rollback script, `0015_orgs.down.sql`, which is not applied by `--migrate`. Ops runs it before pointing `current` back at v0.2.29. It:
     - writes same-org `host_access` rows back into `host_shares` and removes same-org shares that no longer have access (cross-org shares stay);
     - keeps a live host's `project_id` when that project is live with `deploy` access, otherwise uses the live project with `deploy` access granted first, otherwise revokes the host and its credentials (never a metrics-only or arbitrary project);
     - gives every NULL `services.project_id` its host's `project_id`, and deletes unused project-less join tokens and project-less confirmations;
     - restores the `NOT NULL` constraints, notices the hosts it revoked and the shares it rewrote, and deletes its own `schema_migrations` row.
     Run it with `psql -v ON_ERROR_STOP=1`. Org-only audit rows lose their org on a round trip.
   - `--migrate` refuses to apply 0015 unless `BOT_API_ORG_MAPPING_FILE` is set: a mapping file, or `none` for one org per project owner. Cross-org shares the migration cannot convert are logged by host id and project slug, and kept in `host_shares` without access. Run order: stop the API, take a dump, `--migrate`, start the new release; never migrate under v0.2.29.
   - the `deploybot_api` grants follow the existing default privileges.
10. **MCP-visible change:**
    - `list_projects` shows the org;
    - new `list_orgs` (reads `/v1/me`);
    - `list_hosts` takes `project_id` or `org_id`.
    No write tool for orgs, members, tokens or access. Delegated sessions are refused on every route in item 3 except `GET /v1/me`, `GET /v1/orgs/{id}`, `GET /v1/orgs/{id}/projects` and `GET /v1/orgs/{id}/hosts`, which are `delegatedAllow` reads.

## v1.24.1 image entries round-trip (2026-11-14, approved spec `docs/spec-release-v0231.md` rev 1 §2.7; no migration)

1. **The other kind's wait field may be sent as 0 (amends v1.8 item 11).** An image entry stores both `healthy_after_sec` and `timeout_sec`, and GET returns both, with the field that does not apply to the entry's kind set to `0`. On create and replace (`POST`/`PUT …/catalog[/{name}]`), a `service` may now carry `"timeout_sec":0` and a `job` may carry `"healthy_after_sec":0`; the value is ignored and stored as `0`, as before. Any other value of the other kind's field is still `bad_request` with the same message (`invalid timeout_sec`, `invalid healthy_after_sec`). The kind's own field keeps its range and default. `spec_hash` is unchanged for every entry. So GET then PUT of an unedited image entry succeeds. `healthcheck.timeout_sec` is a different field and is unaffected.

## v1.25 shared networks (2026-11-14, approved spec `docs/spec-cross-project-network.md` rev 2; additive, gated; no migration)

1. **ImageSpec `networks: [name]`.**
   - Optional; 1–4 unique names matching `^[a-z0-9][a-z0-9-]{0,31}$`.
   - `null` and `[]` are stored as absent. A bad name, a duplicate or more than 4 is `bad_request` `invalid networks`.
   - Present only when set. It enters `spec_hash` only when present, so existing entries keep their hash.
   - A job may carry it too.
2. **`HostPrograms.shared_networks: [{"name","subnet","projects":[slug]}] | null`.**
   - It is `null` when the agent did not send it (agents < 0.2.9).
   - **Visibility:**
     - an org owner or admin in a session (not delegated) sees the full list;
     - every other caller who can see the host, delegated sessions included, sees only the networks whose `projects` lists the project in view, and `projects` is omitted.
     - with no single project in view (`GET /v1/hosts/{id}` for a caller with access through several projects or none, or a delegated `GET /v1/orgs/{id}/hosts`), that view is `[]`. `null` only ever means the agent did not send the key.
   - **Parsing the hello:** at most 8 networks, at most 32 slugs per network, valid names, slugs and CIDRs. A malformed value stores the programs as disabled, as for every other `programs` key. A network's `"projects": null` is read as `[]`.
3. **Deploy and rollback gates** (`POST …/catalog/{name}/deploy`, `…/rollback`, and the delegated confirm of those plans), when the entry's spec has `networks`. They run after the files, healthcheck, logging and health-policy gates, and before the rollback digest and secret-key checks.
   - Host `programs.shared_networks` is `null`: `bad_request` "this host's agent does not support shared networks; upgrade it to 0.2.9".
   - A name is not defined on the host, or does not list the entry's project: `bad_request` "shared network <name> is not open to this project on this host".
   - Run, stop and restart of an existing container are not gated.
4. **Task payload:** `spec.networks` is sent only when non-empty. Hosts that did not advertise `shared_networks` never receive it, because the gate above refuses first.
5. **Plan text:**
   - catalog create and update plans list `shared networks: <names>`, or `shared networks: <before> → none`;
   - deploy and rollback plans add `also joins shared network(s): <names>`.
6. **MCP:**
   - `create_catalog_entry` and `update_catalog_entry` accept `networks`, and `list_catalog` shows it;
   - the `list_hosts` summary names the shared networks and subnets the caller may see.

## v1.26 organizations phase 2: removals (2026-11-14, approved spec `docs/spec-organizations.md` rev 1, §4.1 and §5; migration 0016; breaking)

1. **Removed from `Host`:** `project_id`, `relation` and `share_level`, the deprecated mirrors of v1.24 item 1. Clients use `org_id` and `access_level`.
2. **Removed routes:** `/v1/hosts/{id}/shares*` and the catalog host-approval routes keep answering `410 gone` from a small stub; their real handlers and storage are deleted.
3. **Migration 0016** (forward only; applied by `--migrate`; no down script):
   - drops `app.host_shares`, the `host_shares_not_owner` trigger and function, and `app.catalog_host_approvals` with any other approvals table that v1.24 left unused;
   - drops `hosts.project_id` and the old host-name uniqueness constraint;
   - the v1.24 per-org name rule stays.

   The rollback after this migration is restoring the pre-release dump (spec §7).
4. **Unchanged:** every other route, field and error, agent-protocol, and oauth-v1.
5. **Release condition:** ships only after phase 1 (v0.2.30) has been live and checked (spec §7, D-9).

## v1.28 value requests: an agent asks, the human fills the value in the browser (2026-11-15, approved spec `docs/spec-env-request-value.md` rev 3; migration 0018; additive)

*Status: implemented on dev 2026-11-16 (bot-control-plane merge of e6aa328), not yet released; ships with v1.27 phase B (items 4–6). No agent-protocol change.*

*As implemented (2026-11-16): `via` for these rows is recorded in the audit detail (`audit_log.via` keeps console/mcp/agent/system); the read and the fill-page data also answer `not_found` when the creator no longer reaches the project; `reason` also refuses format and line/paragraph separator characters (Unicode Cf, Zl, Zp) with the same message; create and fill answer `unavailable` when the server has no secrets key; rows are deleted 7 days after expiry.*

1. **ValueRequest object:** `{"request_id","project_id","set","key","state","expires_at","created_at"}`. `state` is `pending`, `filled`, `declined` or `expired` (`expired` is computed from `expires_at` for a `pending` row). It never carries a value, a preview or the reason after creation.
2. **Create:** `POST /v1/projects/{id}/env-sets/{set}/value-requests`, body `{"key", "reason"?}`.
   - **Who:** a project owner or admin (org owners and admins included), console or `api_delegated` session. It **decides in its handler** (v1.12.1 item 3); it writes no env value.
   - **Checks, in order:**
     1. the set exists (`not_found`);
     2. `key` matches the env key rule (`bad_request`);
     3. `reason`, if given, is at most 200 characters with no control characters (`bad_request` `reason must be at most 200 characters of plain text`);
     4. the key is either absent from the set or a `needs_value` placeholder; a key with a value → `conflict` `{KEY} already has a value; edit it in the console`;
     5. the caller has fewer than 20 `pending`, unexpired requests, not counting one this request replaces (`conflict` `too many open value requests; fill or wait for the ones you have`).
   - **Effect:** stores the request only. A new key is created only when the request is filled, so an unfilled request leaves the set unchanged. The id is 43-character base64url from a CSPRNG, stored hashed (as confirmation ids). Valid for 15 minutes, single use.
   - **Replaces an open one (rev 3):** in the same transaction, a `pending`, unexpired request by the same user for the same project, set and key gets `expires_at` set to now, so its link answers `this request has expired; ask the agent for a new one`. At most one open request per user and key.
   - **Response:** `201 {"request_id","url","expires_at"}`. `url` is `<BOT_API_CONSOLE_ORIGIN>/fill/<request_id>`; it carries no user data and is not pre-authenticated.
   - **Audit:** `env_set.value_requested` (`set`, `key`, `via`, `request`). The reason is not audited.
   - **`request` in audit rows (rev 3):** the first 8 hex characters of the SHA-256 of the request id, never any part of the id itself (the id is the token in a live link).
3. **Read:** `GET /v1/value-requests/{request_id}` → `200` ValueRequest, only to the user who created it; anyone else, or an unknown id → `not_found`. Console or delegated session.
4. **Fill page data:** `GET /v1/value-requests/{request_id}/fill`, **console session only** (`api_delegated` → `forbidden` before any lookup), the requesting user only (else `not_found`) → `200` the ValueRequest plus `project_name` and `reason` (plain text). Used by the console fill page; never cached (`Cache-Control: no-store`).
5. **Fill:** `POST /v1/value-requests/{request_id}/fill`, console session only (`api_delegated` → `forbidden`), body `{"value"}` or `{"generate": <v1.27 item 1 object>}` (both or neither → `bad_request` `send value or generate, not both`).
   - **Checks:** the caller created the request (else `not_found`) and is still owner or admin of the project (else `not_found`); the request is `pending` (`filled` or `declined` → `conflict` `this request was already used`) and not expired (`conflict` `this request has expired; ask the agent for a new one`); the key is still absent or still a placeholder (`conflict` `{KEY} already has a value; edit it in the console`; the request stays `pending`); a typed value follows the env value rules (no empty value, size limit).
   - **Effect, in one transaction:** writes the key as **secret**, sealed on arrival; clears `needs_value`; a generated value follows v1.27 items 1–2 (`generated`, `reveal_pending`); the request becomes `filled` with `done_at`. Typed value → `200 {"state":"filled"}`. Generated → `200 {"state":"filled","reveal_pending":true}`; the page then uses the v1.27 item 3 reveal route.
   - **Audit:** `env_set.key_put` with `via='value_request'`, `request` (item 2's hash prefix), and `generated: "<kind>"` when generated. No value.
6. **Decline:** `POST /v1/value-requests/{request_id}/decline`, console session only, same user and state checks as item 5 → `200 {"state":"declined"}`. Audit `env_set.value_request_declined` (`set`, `key`, `request` as in item 2).
7. **Lifecycle:** deleting an env set, or its project, removes its requests (cascade). An expired request is never filled; expired rows may be deleted after 7 days.
8. **Migration 0018** (applied by `--migrate`): table `app.env_value_requests` (`id_hash`, `user_id`, `project_id`, `set_name`, `key`, `reason`, `state`, `created_at`, `expires_at`, `done_at`), foreign keys to users and projects `ON DELETE CASCADE`, an index for the per-user open count; **no value column**. `0018_env_value_requests.down.sql` drops the table (pending requests are lost; filled values stay in their sets).
9. **Unchanged:** every other env-set route and its delegated refusal; no route returns an env value or preview to a delegated session; v1.27 items 1–8.
10. **MCP consumers, no URL input request (amended 2026-11-23, bot-control-plane PR 10, DD 8hp8; MCP only, no API change):** `request_env_value`, `request_file` (v1.30 item 9) and `connect_registry` (v1.31 item 10) return the link at once to every client, as text and in the structured result with `elicitation` `not_supported`.
    - Before: a 2026-07-28 client declaring `elicitation.url` got a SEP-2322 URL input request instead, and its retry read the state.
    - Why: a client that showed the input request but got no answer from the person held the call until its own time limit and never received the link, and each retry replaced the earlier link.
    - A retry carrying a `requestState` from an older MCP server still reads the request and creates nothing.

## v1.29 automatic project settings: Connect GitHub, image access, readiness (2026-11-16, approved spec `docs/spec-settings-automatic.md` rev 1; migration 0019; additive)

*Status: implemented on dev 2026-11-16 (bot-control-plane commit bcc51bf), not yet released. No agent-protocol change: a GitHub token rides the existing `registry_auth` (agent-protocol v1.4).*

1. **Configuration:** `BOT_API_GITHUB_CLIENT_ID` and `BOT_API_GITHUB_CLIENT_SECRET_FILE` (a regular file, mode 0600, read like the secrets key). Without both, items 2–3 answer `unavailable` `GitHub is not configured` and the console hides Connect GitHub. A secrets key is required as for registry writes.
2. **Start:** `POST /v1/projects/{id}/registries/github/start`, console session only (`api_delegated` → `forbidden`), owner or admin (else `forbidden`); at most 5 outstanding states per user (the oldest is dropped) → `200 {"authorize_url"}`: GitHub's authorize URL with `client_id`, `scope=read:packages`, `redirect_uri=<BOT_API_CONSOLE_ORIGIN>/auth/github/callback`, a random `state` (32 bytes, base64url) and PKCE S256. The state is bound to the user and project, single use, valid 10 minutes, held in memory (capped, as Google sign-in states).
3. **Callback:** `POST /v1/auth/github/callback`, console session only, body `{"code","state"}`. Checks: the state exists, is unused, unexpired and belongs to the caller (else `bad_request` `this GitHub sign-in expired or was already used; start again`); the caller is still owner or admin of the state's project (else `forbidden`). The API exchanges the code (with the PKCE verifier), reads `GET https://api.github.com/user` for `login`, and requires `read:packages` in `X-OAuth-Scopes` (else `bad_request` `GitHub did not grant read:packages`; the token is revoked). Effect: upsert the project's `ghcr.io` registry with `username = login`, the token sealed as for item v1.8 registry PUT (`RegistryAAD`), `source = 'github'`, `connected_by = caller`. When the replaced row was `source = 'github'` with a different token, the old token is revoked at GitHub after the store write, best effort; if the store write fails, the new token is revoked. → `200 {"project_id","host":"ghcr.io","username":<login>,"source":"github"}`. A GitHub error answers `bad_gateway` `GitHub sign-in failed; try again` with no GitHub body echoed. Audit `registry.github_connected` (`host`, `login`, `via`).
4. **Disconnect:** the existing `DELETE /v1/projects/{id}/registries/{host}`; for a row with `source = 'github'` the API also revokes the token at GitHub (`DELETE /applications/{client_id}/token`, basic auth with the client id and secret), best effort after the row is deleted → `200 {"revoked": bool}` (`true` only on GitHub's 204). Audit `registry.github_disconnected` (`host`, `revoked`).
5. **Registry object** gains `source` (`manual|github`) and `connected_by` (`{"id","name"}` or null). The registries list response gains `"github_connect": bool` (true when item 1 is configured and the server has a secrets key), which the console uses to show or hide Connect GitHub. The token is never returned, logged or audited. A manual `PUT …/registries/ghcr.io` over a GitHub row replaces it (`source` becomes `manual`); the API does not revoke the GitHub token in that case.
6. **Image access:** `GET /v1/projects/{id}/image-access?image=<ref>`, owner or admin, console or delegated → `200 {"registry","access","login"}`; `access` is `public | private_ok | private_no_login | login_failed | not_found | unknown`, `login` is `none | manual | github`. Order: anonymous manifest HEAD (success → `public`); on 401/403 with a stored login, retry with it (success → `private_ok`, refused → `login_failed`); on 401/403/404 without one → `private_no_login` (an anonymous 404 is not trusted: registries answer 404 for private repositories); 404 with a credential → `not_found`; timeout, refused host or other errors → `unknown`. Same allowlist, private-address guard, 60 s cache and 20 lookups per user per minute as v1.16; an invalid ref → `bad_request` as v1.16.
7. **Readiness:** `GET /v1/projects/{id}/catalog/{name}/readiness?host_id=<uuid>` (`host_id` optional), console or delegated; owner or admin, or a member with deploy access to that host → `200 {"ready", "checks":[{"check","status","message","fix"}]}`. `status` is `ok | missing | warn | unknown`; `ready` is false when any check is `missing`. The `registry` check maps item 6: `public`/`private_ok` → `ok`; `private_no_login`/`login_failed` → `missing` with `connect_github` (ghcr.io) or `add_registry_login`; `not_found` → `missing` "tag T of R not found in the registry" (with " (no tag was given, so latest is used)" when the reference had no tag; "digest D of R not found in the registry" for a digest reference) with `none`; `unknown` → `warn`. Checks in order: `env_sets`, `env_values`, `env_refs`, `files`, `registry` (image entries), `host_access`, `host_online`, `host_images`, `host_policy`, `networks` (the last five only with `host_id`). `fix` is `{"action": "create_env_set"|"fill_value"|"add_file"|"connect_github"|"add_registry_login"|"grant_deploy_access"|"host_policy_snippet"|"none", ...names}`. `host_policy` is `unknown` unless the entry's latest deploy to that host ended `policy_denied` (read from the newest `catalog.deploy` audit row for the entry and host), then `missing`. A member who is not owner or admin needs `host_id`, the entry's `deploy` (image) or `run` (program) action, and deploy access to the host. Never a value, preview or token; contacts the registry (cached), never the host; advisory only (deploy refusals unchanged). A program entry skips `registry`, `host_images` and `networks`.
8. **Host-policy snippet:** `GET /v1/projects/{id}/catalog/{name}/host-policy-snippet?host_id=<uuid>`, image entries, owner or admin, console or delegated → `200 {"image_pattern","project","toml","command","note"}`: the entry's repository (no partial wildcard), the project slug, the `runner.toml` lines, `sudo ./install.sh install --runner --project <slug> --image '<pattern>'`, and a note that the host's root applies it. Text only; no host contact.
9. **Migration 0019:** `app.registries` gains `source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','github'))` and `connected_by uuid NULL REFERENCES app.users(id) ON DELETE SET NULL`. Down drops both (rows stay; a GitHub token then acts as a manual login).
10. **Unchanged:** registry PUT/GET rules, `registry_auth`, deploy and rollback refusals, v1.16 tag and digest routes.

## v1.30 file requests: an agent asks, the person adds a managed file in the browser (2026-11-16, approved spec `docs/spec-file-request.md` rev 1; migration 0020; additive)

*Status: implemented on dev 2026-11-16 (bot-control-plane commit 127f002, merge 568befa), not yet released. Mirrors v1.28; no agent-protocol change. As implemented: request reads (this item 3 and v1.28 item 3) check project reach (`CapRead`).*

1. **FileRequest object:** `{"request_id","project_id","name","secret","state","expires_at","created_at"}`; `state` is `pending | filled | declined | expired` (`expired` computed from `expires_at` for a pending row). It never carries content, and carries the reason only on the fill-page data.
2. **Create:** `POST /v1/projects/{id}/file-requests`, body `{"name", "secret"?, "reason"?}` (`secret` omitted = true). Owner or admin, console or `api_delegated` (decides in its handler). Checks in order: `name` matches the v1.9 file-name rule (`bad_request`); `reason` as v1.28 item 2 check 3 (same text and character rules); the file does not exist (`conflict` `file {name} already exists; replace it in the console`); the project has fewer than 64 files (`bad_request` as v1.9); a secrets key exists when `secret` is true (`unavailable`); the caller has fewer than 20 pending, unexpired file requests, not counting one this request replaces (`conflict` `too many open file requests; fill or wait for the ones you have`). Effect: stores the request only; a pending request by the same user for the same project and name gets `expires_at` = now in the same transaction. Id: 43-character base64url from a CSPRNG, stored as its SHA-256; 15 minutes; single use. Response `201 {"request_id","url","expires_at"}`, `url` = `<BOT_API_CONSOLE_ORIGIN>/fill-file/<request_id>`. Audit `file.requested` (`name`, `secret`, `via`, `request` = first 8 hex of SHA-256 of the id). The reason is not audited.
3. **Read:** `GET /v1/file-requests/{request_id}` → `200` FileRequest to its creator while the creator still reaches the project; anyone else or an unknown id → `not_found`. Console or delegated.
4. **Fill-page data:** `GET /v1/file-requests/{request_id}/fill`, console only (`api_delegated` → `forbidden` before lookup), the creator while still owner or admin (else `not_found`) → `200` FileRequest plus `project_name` and `reason`; `Cache-Control: no-store`.
5. **Fill:** `POST /v1/file-requests/{request_id}/fill`, console only, body `{"content", "secret"?}`. Checks: creator and still owner or admin (else `not_found`); `pending` (`filled`/`declined` → `conflict` `this request was already used`) and not expired (`conflict` `this request has expired; ask the agent for a new one`); the file still does not exist (`conflict` as item 2; the request stays pending); content per v1.9 (UTF-8, 1–65536 bytes, no NUL; `bad_request` with v1.9's texts); `secret` defaults to the request's flag, and `false` on a secret request → `bad_request` `a secret file cannot be made plain`. Effect in one transaction: write the file (sealed with the env-set key when secret) and mark the request `filled` with `done_at`. `200 {"state":"filled","secret":bool}`. Audit `file.put` with `via='file_request'` (in the detail), `name`, `secret`, `request`. No content.
6. **Decline:** `POST /v1/file-requests/{request_id}/decline`, console only, same user and state checks → `200 {"state":"declined"}`. Audit `file.request_declined` (`name`, `request`).
7. **Lifecycle:** deleting the project removes its requests (cascade); expired rows are deleted 7 days after expiry; a file created by other means leaves the request pending and its fill answers `conflict`.
8. **Migration 0020:** table `app.file_requests` (`id_hash`, `user_id`, `project_id`, `name`, `secret`, `reason`, `state`, `created_at`, `expires_at`, `done_at`), foreign keys to users and projects `ON DELETE CASCADE`, an index for the per-user open count and one on `expires_at`; no content column. `deploybot_api`: SELECT, INSERT, DELETE, and UPDATE on (`state`, `done_at`, `expires_at`). Down drops the table.
9. **MCP:** `request_file` and `get_file_request`, as v1.28's tools (the link as text for every client; amended 2026-11-23, see v1.28 item 10: no URL input request). `check_readiness`'s next step for `add_file` names `request_file`.
10. **Unchanged:** v1.9 file routes and rules, v1.29 readiness shape, every delegated refusal on file routes.

## v1.31 Connect GitHub started from chat (2026-11-16, approved spec `docs/spec-settings-automatic.md` rev 2 §3.6; migration 0021; additive)

*Status: implemented on dev 2026-11-16 (bot-control-plane 4ae09ed, merge 9b99f99); ships in v0.2.34. Builds on v1.29 items 1–4 and the v1.28 request pattern; no agent-protocol change.*

1. **ConnectRequest object:** `{"request_id","project_id","registry":"ghcr.io","state","expires_at","created_at"}`; `state` is `pending | connected | declined | expired` (`expired` computed from `expires_at` for a pending row). Never a token.
2. **Create:** `POST /v1/projects/{id}/registries/github/requests`, body `{"reason"?}`; owner or admin; console or `api_delegated` (decides in its handler). Checks in order: Connect GitHub configured and a secrets key present (else `unavailable` `GitHub is not configured`); `reason` as v1.28 item 2 check 3; fewer than 20 pending, unexpired connect requests for the caller, not counting one this request replaces (`conflict` `too many open connect requests; finish or wait for the ones you have`). Effect: stores the request; a pending one by the same user for the same project gets `expires_at` = now in the same transaction. Id: 43-character base64url from a CSPRNG, stored as its SHA-256; 15 minutes; single use. `201 {"request_id","url","expires_at"}`, `url` = `<BOT_API_CONSOLE_ORIGIN>/connect/<request_id>`. Audit `registry.github_connect_requested` (`request` = first 8 hex of SHA-256 of the id, `via`).
3. **Read:** `GET /v1/registry-connect-requests/{request_id}` → `200` ConnectRequest to its creator while the creator still reaches the project; else `not_found`. Console or delegated.
4. **Page data:** `GET /v1/registry-connect-requests/{request_id}/page`, console only (`api_delegated` → `forbidden` before lookup), the creator while still owner or admin (else `not_found`) → ConnectRequest plus `project_name`, `reason` and the current ghcr.io login if any (`{"source","username"}` or null); `Cache-Control: no-store`.
5. **Continue:** v1.29 item 2 `POST /v1/projects/{id}/registries/github/start` gains an optional body `{"connect_request_id"}`. With it, the API checks the request belongs to the caller and this project, is `pending` and unexpired (else `conflict` with the v1.28 used/expired texts, or `not_found`), and stores the request id in the OAuth state. Without it (no body, `{}` or a null id), v1.29 behaviour is unchanged; a body that is not valid JSON → `bad_request` `invalid json`.
6. **Callback:** v1.29 item 3; when the state carries a request id, the registry write and the request's move to `connected` (with `done_at`) happen in one transaction; if the request is no longer `pending` or has expired, the connect still applies (the person approved at GitHub) and the request is left as it is. The response gains `"connect_request_id"` when present, so the console returns to `/connect/<id>`; the `registry.github_connected` audit row then also carries `request`.
7. **Decline:** `POST /v1/registry-connect-requests/{request_id}/decline`, console only, the creator while still owner or admin (else `not_found`), `pending` and unexpired → `200 {"state":"declined"}`. Audit `registry.github_connect_declined` (`request`).
8. **Lifecycle:** deleting the project removes its requests; expired rows are deleted 7 days after expiry.
9. **Migration 0021:** table `app.registry_connect_requests` (`id_hash`, `user_id`, `project_id`, `reason`, `state`, `created_at`, `expires_at`, `done_at`), foreign keys `ON DELETE CASCADE`, indexes for the per-user open count and `expires_at`; no token column. `deploybot_api`: SELECT, INSERT, DELETE and UPDATE on (`state`, `done_at`, `expires_at`). Down drops the table.
10. **MCP:** `connect_registry` (`project_id`, `registry` = `"ghcr.io"`, `reason`?) and `get_registry_connect_request` (`request_id`), as v1.28's tools (the link as text for every client; amended 2026-11-23, see v1.28 item 10: no URL input request). Neither returns a token. `check_readiness`'s next step for `connect_github` names `connect_registry`.
11. **Unchanged:** v1.29 Settings flow, disconnect and revoke, `registry_auth`, and every token-secrecy rule.


## v1.32 Agent usability round 1 (2026-11-16, approved spec `docs/spec-agent-usability-1.md` rev 2 §3 B, D, E; additive)

*Status: implemented on dev 2026-11-16 (bot-control-plane merges of 86da615 and 3080f8d); not yet released. No migration; no agent-protocol change.*

1. **Confirm, own confirmation:** `POST /v1/confirmations/{confirmation_id}` (and every confirm path it reaches: catalog actions, catalog entry writes, env-set create, health checks) answers, when the confirmation belongs to the caller: expired → `conflict` `this confirmation expired; call the tool again for a new one`; already used → `conflict` `this confirmation was already used`. Another user's id, or no row, stays `not_found` `not found`. Before: all three answered `not_found`.
2. **Plain values in create-by-names (amends v1.27 item 4):** a key in `POST /v1/projects/{id}/env-sets` (the names-only create) may carry `"value"` only when `"is_secret": false` and no `generate`; otherwise `bad_request` `a value can be given only for a plain key`. The value follows the env value rules of `PUT /env-sets/{set}`, and `${KEY}` references are checked as there. The confirmation plan and the audit list key names only, never the value. A reminder, not a refusal (Joe 2026-11-16): for a plain key with a value whose name looks secret (the inline-env `LooksSecret` rule), the plan appends the sentence `KEY looks like a secret but will be stored as plain text, visible in the console; use a placeholder and request_env_value instead.`
3. **Readiness from a spec:** `POST /v1/projects/{id}/catalog/readiness`, body `{"spec": ImageSpec, "host_id"?}`, owner or admin, console or `api_delegated`. It runs the v1.29 item 7 checks on the given spec instead of a stored entry and returns the same Readiness object. An invalid spec answers `bad_request` with the catalog-entry create texts. It stores nothing and contacts no host.


## v1.33 Agent usability round 2 (2026-11-16, spec `docs/spec-agent-usability-1.md` rev 3, G, H, F2; migration 0022; additive)

*Status: approved 2026-11-16 ("Approve design"); implemented on dev 2026-11-16, with the review additions in item 6 and item 7. Not yet released. No agent-protocol change.*

1. **Update an env set (G1, G5):** `PATCH /v1/projects/{id}/env-sets/{set}`, owner or admin, console or `api_delegated`, body `{"set":{KEY:value},"add":[CreateKey],"remove":[KEY],"make_secret":[KEY]}` (at least one non-empty; a key in at most one list).
   - `set`: existing PLAIN keys only, values under the env value rules; a secret key → `bad_request` `the key is secret; change its value in the console or with request_env_value`.
   - `add`: new keys, each as a v1.27 item 4 / v1.32 item 2 create key (generated, placeholder or plain value).
   - `remove`: refused with `conflict` `KEY is used by entries a, b; update them first` when any entry of the project resolves the key (its `env_sets` include the set and it has no inline override, a `secret_env` source, or a `${KEY}` reference that resolves to it).
   - `make_secret`: plain → secret, one way (v1.17).
   - Answers `202 {"confirmation_id","expires_at","plan"}`; the plan names keys and actions only, with the v1.32 item 2 reminder for a plain value under a secret-looking name. Confirm (v1.32 item 1 texts) applies everything in one transaction. Audit `env_set.update` with key names per action.
2. **Delete an env set, delegated (G5):** `DELETE /v1/projects/{id}/env-sets/{set}` from `api_delegated` answers 202 with a plan (console keeps today's direct delete). Both refuse with `conflict` `env set S is used by entries a, b` while any entry names the set in `env_sets` or `secret_env`.
3. **Rotate a secret by request (G5):** v1.28 create accepts an existing key that has a value when it is secret; filling replaces the value (`reveal_pending` false). A plain key with a value is still refused (use item 1). The fill page says "Replace the value of KEY".
4. **Consumers (G3):** each set in `GET /env-sets` and `GET /env-sets/{set}` gains `"used_by":[entry name]` (entries naming the set in `env_sets` or `secret_env`), sorted.
5. **Split pairs (G4):** readiness gains check `env_pairs` (after `env_refs`): `warn` when `<P>_CLIENT_ID`/`<P>_CLIENT_SECRET`, `<P>_KEY_ID`/`<P>_SECRET` or `<P>_ID`/`<P>_SECRET` resolve from different sources (inline `env`, a set, or a private set), else `ok`; fix `none`. Advisory.
6. **Entry-private secrets (G2), migration 0022:**
   - A private set `@<entry>` belongs to one entry (`env_sets.entry_id`), is created on first use, and is deleted with the entry.
   - Image and program write bodies accept, in `secret_env`, `KEY: {"fill": true}` (a placeholder with `needs_value`) or `KEY: {"generate": {kind, length?}}` (v1.27 kinds) besides `"<set>.<KEY>"`; the API stores them in `@<entry>` and the entry's `secret_env` as `"@<entry>.KEY"`. A key removed from `secret_env` on update is removed from the private set.
   - v1.28 create accepts `{"entry": name}` instead of the set path: `POST /v1/projects/{id}/catalog/{name}/value-requests` body `{"key","reason"?}` for a key of `@<entry>`.
   - `GET /env-sets` hides private sets unless `?include=private`; `GET /catalog/{name}` gains `"private_keys":[{"key","is_secret","needs_value","reveal_pending"}]`, never values.
   - Plans, approvals and audit name `@<entry>.KEY` and never a value.
   - A private set is not reachable through `/env-sets/{set}` (`bad_request` `invalid name`). A generated private key is shown once with `POST /v1/projects/{id}/catalog/{name}/private-keys/{key}/reveal` (owner or admin, console session only; `api_delegated` → `forbidden`) → `200 {"key","value"}`, `no-store`, the same show-once rule as the env-key reveal; already shown or not generated → `conflict` `the value of KEY was already shown or was not generated; edit it to set a new value`. Audit `env_set.key_reveal` with `{"set":"@<entry>","key"}`.
   - Write bodies carry `idempotency_key` for items 1 and 2 (body `{"idempotency_key"}` on the delegated DELETE). A plan for item 1 or 2 is bound to the set's state at plan time; if any key changed before confirm → `conflict` `env set S changed since the plan; ask for a new one`. A rotation (item 3) adds `"replaced":true` to the `env_set.key_put` audit detail. The fill page data gains `"replace"` (bool) and `"entry"` (for a private set).
7. **Shared-network snippet (H3):** `GET /v1/projects/{id}/hosts/{host_id}/network-snippet?network=<name>&subnet=<cidr>` (owner or admin, console or delegated; the project needs `deploy` access to the host, or the caller is an owner or admin of the host's org, else `not_found`) → `200 {"snippet": "<runner.toml lines>"}`, the `shared_networks` and `shared_network_projects` lines that would open a network to this project, as v1.29's host-policy snippet. `subnet` may be left out when the host already defines the network; a different subnet → `bad_request` `network N already has subnet X on this host`. Nothing is changed on any host.
8. **Open requests (F2):** `GET /v1/me/requests`, console session only (`api_delegated` → `forbidden`) → `200 {"items":[{"type":"value"|"file"|"connect","handle","project_id","project_name","subject","expires_at","created_at"}]}`: the caller's pending, unexpired requests, newest first, at most 60. `handle` is the stored SHA-256 of the id (64 hex), never the id. `subject` is `set.KEY`, the file name, or `ghcr.io`. No `reason`, value or URL. The page-data, fill, save and decline routes of v1.28, v1.30 and v1.31 also accept `h-<handle>` in place of the id, for a console session of the request's own user only (anyone else → `not_found`, as for an id); the read routes for agents keep ids only.

## v1.34 Update awareness (2026-11-17, spec `docs/spec-update-awareness.md` rev 1; additive; no migration)

*Status: approved 2026-11-17 ("Approve both"); implemented on dev 2026-11-17 (update awareness). Not yet released. No agent-protocol change.*

1. **Latest agent version (API setting):** `BOT_API_LATEST_AGENT_VERSION`, optional, `X.Y.Z` (digits only) or empty. Any other value stops the API at start with `BOT_API_LATEST_AGENT_VERSION must be X.Y.Z or empty`.
2. **Host fields:** every `Host` returned by `GET /v1/projects/{id}/hosts`, `GET /v1/orgs/{id}/hosts` and `GET /v1/hosts/{id}` gains `"latest_agent_version": "X.Y.Z" | null` (`null` when the setting is empty) and `"update_available": bool` (true only when both the host's `agent_version` and the setting parse as `X.Y.Z` and the host's is lower by major, minor, patch; false otherwise). No route is added; old clients ignore the fields.
3. **MCP (not an API route, recorded here for consumers):** server `instructions` name the server version and up to 3 items of the newest `CHANGELOG.md` entry; new read-only tool `whats_new` `{"count"?: 1–10, default 3}` → `{"version","entries":[{"version","date","items"}]}`, no API call; `list_hosts` text adds ` (agent update available: X.Y.Z)` for a host with `update_available`.

## v1.35 Domains, per-host edge and routes, stage 1 (2026-11-17, spec `docs/spec-domains-edge-routes.md` rev 2; migration 0023; additive)

*Status: approved 2026-11-17 ("Approve both"); implemented on dev 2026-11-17, not released. Needs agent-protocol v1.15 (bot-agent v0.2.11) on a host for its edge. Item 7 records the implementation details that refine items 1–6.*

1. **Domains (org, console session only; `api_delegated` → `forbidden`; org owner or admin):**
   - `GET /v1/orgs/{id}/domains` → `{"items":[{"id","name","status":"pending"|"verified"|"lapsed","txt_name":"_deploybot.<name>","txt_value":"deploybot-verify=<token>","verified_at","last_check_at","assignments":[{"project_id","project_name","pattern"}]}]}`.
   - `POST /v1/orgs/{id}/domains {"name"}`: a lowercase registrable or sub domain (IDNA A-labels), not an IP, ≤ 253 chars → 201 with the item. A name another org has verified → `conflict` `this domain is verified by another organization`.
   - `POST …/domains/{domain_id}/verify`: looks up the TXT record now; found → `verified`; else `conflict` `TXT record not found yet: add <txt_name> = <txt_value>`.
   - `PUT …/domains/{domain_id}/assignments {"assignments":[{"project_id","pattern"}]}`: `pattern` is `@` (the name itself) or `*` (one label below it); projects must be in the org.
   - `DELETE …/domains/{domain_id}`: answers `202 {"confirmation_id","plan"}` naming every route that would stop; the confirm removes the domain, its assignments and those routes, and re-applies the affected edges.
   - Re-check: daily; 7 consecutive failures → `lapsed` (existing routes keep serving, new routes under it are refused). A later success → `verified`.
   - Audit `domain.add`, `domain.verify`, `domain.assign`, `domain.delete`, `domain.lapsed`.
2. **Edge (host, console session only; owner or admin of the host's org):**
   - `GET /v1/hosts/{id}/edge` → `{"available":bool,"enabled":bool,"status":"off"|"pending"|"applied"|"error","detail","config_hash","applied_at","routes":n}`. `available` is the host's hello `edge.enabled`.
   - `PUT /v1/hosts/{id}/edge {"enabled":bool}`: enabling refuses `bad_request` `the host's runner.toml does not allow the edge; its root adds edge = true and restarts bot-agent` when not available, and `conflict` `port 443 is used by <entry> (project <name>)` (or `by a process outside Deploybot`) when discovery shows another listener on 80 or 443. Enabling sends `edge_apply`; disabling sends `edge_stop`.
   - `HostPrograms.edge` `{"enabled","image"}` appears in host reads (org admins).
3. **Routes (project owner or admin; console or `api_delegated`):**
   - `GET /v1/projects/{id}/routes` and `GET /v1/projects/{id}/catalog/{name}/routes` → `{"items":[{"hostname","path_prefix","port","client_ip_from":null|"cloudflare","no_store_paths":[…],"host_id","state":"live"|"pending"|"error"}]}`.
   - `PUT /v1/projects/{id}/catalog/{name}/routes {"routes":[…],"idempotency_key"}` replaces the entry's routes; always `202 {"confirmation_id","plan","expires_at"}`; confirm via `POST /v1/confirmations/{id}`.
   - Route rules: `hostname` lowercase, under a `verified` (not `lapsed`) domain assigned to the project (`@` exact, `*` one label); `path_prefix` `/` (default) or `/seg[/seg…]`; `port` is a container port of an image entry that the entry publishes to a host port (refused `port P is not published; add a ports line for it (127.0.0.1 is enough)`), or the port a program entry listens on; at most 16 routes per entry; `client_ip_from` `cloudflare` or absent; `no_store_paths` up to 8 path prefixes.
   - Conflicts: hostname + path prefix unique across the instance (`conflict` `https://H/P is already routed`).
   - Plan text: `routes for E: add https://H/P → port N on HOST; remove https://H2/; …` (every hostname that goes live or stops).
   - The entry's routes go live on the host(s) where the entry is deployed and whose edge is enabled; otherwise `pending`. A route change never changes the entry's `spec_hash`.
4. **Readiness:** check `routes` (entries with routes): `ok`, or `warn` naming the first problem (domain lapsed, port not published, edge off on the entry's host, edge not available). Fix `none`.
5. **Edge config** is generated by the API from live routes per host (Caddy JSON; TLS per hostname, `encode zstd gzip`, `reverse_proxy 127.0.0.1:<host port>` with WebSockets, `X-Forwarded-*`; HTTP → HTTPS; `client_ip_from: cloudflare` trusts `CF-Connecting-IP` only from Cloudflare's ranges shipped with the release; `no_store_paths` sets `Cache-Control: private, no-store`). It is re-sent on route, domain, assignment, edge-switch and relevant deploy changes, and in full on every hello. Optional org setting `acme_email` (`PATCH /v1/orgs/{id}` field; see item 7).
6. **MCP:** tools `set_routes` (plan + confirm) and `list_routes`; `list_hosts` shows the edge state; `check_readiness` knows `routes`. Domains and the edge switch are console only.
7. **As implemented (refines items 1–6):**
   - **Domains:** all domain reads and writes are console only (`api_delegated` → `forbidden`). `DELETE …/domains/{id}` takes `{"idempotency_key"}`; the 202 carries `expires_at`; the confirm must reuse the key and answers `{"name","deleted":true,"routes_removed":n}`. The plan lists one route per line and is bound to that set: if the routes under the domain changed, the confirm answers `conflict` `routes changed since the plan; ask for a new one`. Only routes of the org's own live projects are touched, and a hostname still covered by another verified or lapsed domain assigned to the same project stays. A second add of the same name in one org is `conflict` `this domain is already added`. Verify answers `unavailable` `the DNS lookup failed; try again` on a resolver error; the re-check job (hourly, each domain at most daily) counts only a not-found as a failure. Domain names with an all-numeric last label are refused.
   - **ACME email:** `PATCH /v1/orgs/{id}` accepts `acme_email` (`""` clears; bad value `invalid acme_email`); org owners and admins read it on `GET /v1/orgs/{id}`. Console field deferred. Dev and test hosts may set `BOT_API_EDGE_TLS_ISSUER=internal` (Caddy's internal CA instead of ACME); any other value stops the API at start.
   - **Edge:** `detail` and `config_hash` are `""` when empty. Enabling also refuses a revoked host (`conflict`). The occupant check looks at 443, then 80. Agent failure codes become sentences in `detail` (with the agent's detail appended). When a 0.2.11+ agent's hello carries `programs.edge.enabled=false`, the API sends `edge_stop` and shows `off` with `the host's runner.toml no longer allows the edge`; it never sends anything to an older agent. Host reads (project, org and single host) carry `"edge":{"enabled","status"}`. `edge_status` is defined but not sent in stage 1. Task budget 180 s per call.
   - **Routes:** items also carry `entry`; PUT accepts and ignores `host_id` and `state`; the confirm answers `{"entry","routes":[…]}`. The plan has one address per line, and `(pending: no host running E has the edge on)` when it applies nowhere yet. A program route needs the program's declared `port` (`program E declares no port; add one to route it`). On each host a route is live only when its upstream port (≥ 1024) is among that entry's own live listeners there; otherwise it is `pending` on that host and left out of its config. `path_prefix` and `no_store_paths` are lowercased, with no `.` or `..` segment. Further refusals: `<host> is not under a verified domain assigned to this project` (bad_request), `domain D has lapsed; verify it again before adding routes under it`, `is listed twice`, `at most 16 routes`.
   - **Deletes:** deleting a project removes its routes and domain assignments (freeing the hostnames); deleting an org removes its domains.
   - **Reserved names:** project slug `edge`, and entry `edge` in a project with slug `deploybot`, are refused at create.
   - **Config shape** (generated; the agent accepts nothing else, agent-protocol v1.15 item 7): top level `admin` `{"listen":"unix//run/edge/admin.sock"}` and `apps`; one server listening on `:443` only; host and path matchers; for `client_ip_from: cloudflare` a `remote_ip` (Cloudflare ranges) plus `header` `{"Cf-Connecting-Ip":["*"]}` route that sets `X-Forwarded-For` from `CF-Connecting-IP`, and otherwise forwarding headers from the client are deleted; `encode` zstd and gzip; `reverse_proxy` to `127.0.0.1:<port>` with `"stream_close_delay":"1h"`, so open WebSockets stay up to an hour after a config load instead of closing (spec-domains-edge-routes rev 3 §11, amended in place 2026-11-17 before release); HTTP→HTTPS by Caddy's automatic HTTPS.

## v1.36 Small fixes (2026-11-18, approved spec `docs/spec-release-v0239.md` rev 1; additive; no migration)

1. **`update_available` on a revoked host** (amends v1.34): false for a host whose `revoked_at` is set, whatever its `agent_version`; `latest_agent_version` is still sent.
2. **Edge enable refusal text** (amends v1.35): the `conflict` message is now `port 443 is used by <entry> (project <name>); stop it, wait until the host no longer reports that port (up to a minute), then try again` (or `port 443 is used by a process outside Deploybot; stop it, …`; 80 likewise). The code stays `conflict`. A stopped Deploybot-owned container keeps its service row; the refusal ends at the first host report that no longer lists the port.
3. **Expired confirmations** (restores v1.32 item 1): plan tables keep an expired confirmation for 7 days before pruning it, so a `confirm` with an expired id answers `conflict` `this confirmation expired; call the tool again for a new one`, not `not_found`. An id with no row stays `not_found` `not found`.
4. **Make secret refuses an empty value** (amends v1.17 and v1.33 `make_secret`): making a key secret whose plain value is empty answers `bad_request` `a secret value cannot be empty; give <KEY> a value first`, and nothing changes. A `needs_value` placeholder is not refused; it becomes a secret placeholder as before.

## v1.37 Notifications and the header (2026-11-19, approved spec `docs/spec-header-org-project-bell.md` rev 1, option B; migration 0024; additive)

1. **`GET /v1/me/notifications`** (session callers only; a delegated session answers `forbidden`). Answers `{"items":[Notification],"unread":<int>}`, newest first, at most 20 items. `Notification` is `{"key":"<string>","kind":"release"|"host_update","title":"<string>","detail":"<string>","href":"<path>","at":"<RFC3339>","read":<bool>}`. Items are computed on each call, never stored:
   - **release:** one per `CHANGELOG.md` release dated within the last 90 days (at most 5), from the server's own build. `key` `release:<version>` (for example `release:0.2.40`), `title` `Deploybot v<version> is out`, `detail` the release's first bullet, `href` `/whats-new#v<version>`, `at` the release date at 00:00 UTC.
   - **host_update:** one per non-revoked host whose `update_available` is true (v1.34, v1.36), only in orgs where the caller is `owner` or `admin`. `key` `host_update:<host id>:<latest version>`, `title` `Update available on <host name>`, `detail` `bot-agent <latest> (this host: <agent_version>)`, `href` `/hosts/<host id>`, `at` the time the API first listed the host behind that latest version (amended 2026-11-23, bot-control-plane PR 11, migration 0035; before: the host's `last_seen_at`, or `enrolled_at` if it never connected, so every check-in moved the notice back to the top of the bell). Project members with deploy access do not get host items.
   - `read` is true when the caller has a read mark for that `key`. `unread` counts the items with `read` false.
   - **Migration 0035 (2026-11-23, amends this item):** `app.host_update_notices (host_id, version, noticed_at)`, primary key `(host_id, version)`, `ON DELETE CASCADE` from `app.hosts`. Listing notifications writes a row at the current time for each host behind `BOT_API_LATEST_AGENT_VERSION` that has none for that version and deletes the host's rows for other versions; an existing row keeps its time, which is the `host_update` item's `at`. A newer latest version starts a new notice. A host already behind when 0035 is first applied is dated by the first list after the upgrade. The down script drops the table.
2. **`POST /v1/me/notifications/read`** (session callers only; delegated → `forbidden`). Body `{"keys":["<key>",…]}` (1–50 keys, each 1–200 characters of valid UTF-8 with no control characters (U+0000–U+001F, U+007F); otherwise `bad_request` `invalid keys`) or `{"all":true}` (marks every item the GET would return now). Answers `200` with the usual envelope (`{"code":"ok","msg":"ok","payload":null}`). Marking an unknown key is allowed and stores the mark. A body with both or neither field (`{"all":false}` alone counts as neither) → `bad_request` `send keys or all`. Not audited.
3. **Storage:** migration `0024_notification_reads.sql` creates `app.notification_reads (user_id uuid REFERENCES app.users ON DELETE CASCADE, key text, read_at timestamptz, PRIMARY KEY (user_id, key))`, with `0024_notification_reads.down.sql`. When marks are written, the caller's marks older than 90 days are pruned unless their key is still among the caller's current items (so a long-standing item never turns unread again). A caller keeps at most 500 marks: beyond that the oldest are dropped (by `read_at`), never a key among the caller's current items. The down script never refuses: read marks are harmless to lose.
4. **`GET /v1/me`:** each project gains `org_name` (the name of the project's org), also when the caller is not a member of that org. Additive; `org_id` and `via` are unchanged.
5. MCP has no tool for these routes; `whats_new` and `list_hosts` already carry the same information.

## v1.38 Host names: a revoked host's name is free, and hosts can be renamed (2026-11-20, approved spec `docs/spec-host-names.md` rev 1; migration 0025)

1. **Host names** are unique per org **among hosts that are not revoked** (before: among all hosts of the org, revoked ones included; v1.24). Enrolment appends `-2`, `-3`, … only when a live host in the org already has the name. A revoked host keeps its name and still lists with status `revoked`, so an org can list two hosts with one name when one of them is revoked. Migration `0025_host_names_live.sql` replaces the constraint `hosts_org_name_unique` with the partial unique index `hosts_org_name_live_uidx ON app.hosts (org_id, name) WHERE revoked_at IS NULL`. Its down script refuses, changing nothing, while any `(org_id, name)` pair is held by more than one host.
2. **`PATCH /v1/hosts/{id}`** with the strict body `{"name": "<name>"}` (no other field; an unknown field, a non-string `name` or trailing data is `bad_request` `invalid json`), for owners and admins of the host's org. Others get `forbidden`; delegated (MCP) sessions get `forbidden`. The host is found by id as for `GET /v1/hosts/{id}` (`not_found` when the caller cannot see it).
   - `name` must match `^[a-z0-9-]{1,63}$`, otherwise `bad_request` `name must be 1–63 characters: lowercase letters, digits and -`.
   - A live host in the org already named `name` → `conflict` `a host named <name> already exists in this organization`.
   - A revoked host → `conflict` `this host is revoked`.
   - The same name as a live host has now → `200` with the host, no change, no audit.
   - Checks run in this order: authorization, body and name rule (a missing or null `name` gets the name-rule message), revoked host, live duplicate, same name.
   - Otherwise `200` with the `Host` object, as `GET /v1/hosts/{id}` answers it. The agent is not told and needs nothing: it never uses the name after enrolment. Stored confirmation plans and earlier audit rows keep the old name as text.
3. **Audit:** `host.rename` with detail `{"from": "<old>", "to": "<new>"}` on a successful rename. A request refused for its name, a duplicate, a revoked host or an unchanged name writes no row. An authorization denial is audited as on every mutating route (decision `denied`, action `host.rename`).

## v1.39 Connect GitHub failures, the fill page hint, and path-named env keys (2026-11-20, approved spec `docs/spec-v0241-small-fixes.md` rev 1: A, B1, C1; no migration)

1. **Connect GitHub callback** (v1.29 item 3, v1.31 item 6):
   - Every failure logs one server line `github sign-in failed` with a `step` (`state`, `scope`, `exchange`, `user`, `forbidden`, `store`). It never logs a token, code, state or GitHub body. Responses and messages are unchanged.
   - The callback body gains an optional `connect_request_id`, which the console sends from its connect cookie. On any failure, the API records the failure against that request when the request belongs to the caller and is still `pending`. Otherwise the field is ignored. A malformed id is ignored, not refused.
2. **ConnectRequest** (v1.31 item 1) gains `last_failure`.
   - Shape: `null`, or `{"reason": "expired_sign_in"|"scope_missing"|"github_error"|"forbidden"|"save_failed", "at": "<RFC3339>"}`.
   - Steps map to reasons: `state` → `expired_sign_in`; `scope` → `scope_missing`; `exchange` and `user` → `github_error`; `forbidden` → `forbidden`; seal and store failures → `save_failed`.
   - It is kept in the API's memory until the request expires, so a restart clears it. The request stays `pending`, and a later success moves it to `connected` with `last_failure` `null`.
   - It appears in `GET /v1/registry-connect-requests/{id}`, the page data, and MCP `get_registry_connect_request`.
3. **Fill page** (v1.28 item 5): no API change. Env values still refuse line breaks (v1.6 item 8). The console stops a value with a line break before posting (typed, pasted or dropped), and points to a file request (`request_file`) for key files. A short hint under the field says it is one line only.
4. **Secret-looking plain env keys** (v1.6 item 8, v1.8 item 5, programs and images):
   - A key whose name ends in `_FILE`, `_PATH` or `_DIR` (case-insensitive) is not refused, because its value is a path.
   - Any other key matching the secret-name rule is refused with `bad_request` `<KEY> looks like a secret: put it in secret_env or in a secret env-set key`. That message replaces `use secret_env`.
   - The same exemption applies wherever the secret-name rule is used for plain values (amended 2026-11-20, Joe "Apply everywhere"). The env-set plan reminder (v1.32 item 2, update plan) gives no warning for such a key. The program run gate for plain env-set keys ("looks like a secret but is not marked secret") does not refuse such a key. One rule serves all three: catalog `LooksSecret` minus the path suffixes.

## v1.40 Remove a revoked host from the lists (archive) (2026-11-20, approved spec `docs/spec-host-remove.md` rev 1; migration 0026)

1. **`Host`** gains `archived_at`: `null`, or the RFC3339 time the host was removed from the lists. Additive. An archived host's `status` stays `revoked`.
2. **`POST /v1/hosts/{id}/archive`** (no body), for owners and admins of the host's org. Others get `forbidden`; a caller who cannot see the host gets `not_found`; delegated (MCP) sessions get `forbidden`.
   - A live (not revoked) host → `conflict` `revoke this host first`.
   - An already archived host → `200` with the host, no change, no audit.
   - Otherwise it sets `archived_at` to now and answers `200` with the `Host`, as `GET /v1/hosts/{id}` answers it.
   - Audit: `host.archive` on success. An authorization denial is audited as on every mutating route (decision `denied`, action `host.archive`).
3. **Lists:** `GET /v1/orgs/{id}/hosts` and `GET /v1/projects/{id}/hosts` omit archived hosts. `GET /v1/projects/{id}/services` omits the rows of archived hosts in every `view`. Reads by id are unchanged: `GET /v1/hosts/{id}`, its metrics, processes and services. The host's `host_access` rows are kept.
4. **Storage:** migration `0026_host_archive.sql` adds `app.hosts.archived_at timestamptz` with `CHECK (archived_at IS NULL OR revoked_at IS NOT NULL)`. Its down script drops both and never refuses; archived hosts then list again as revoked.

## v1.41 Problems in the bell (2026-11-21, approved spec `docs/spec-bell-problems.md` rev 1; migration 0027)

1. **`Problem`:** `{"id", "kind", "level", "title", "detail", "href", "opened_at", "updated_at", "resolved_at", "host_id", "project_id", "entry_id"}`.
   - `kind`: `host_offline` | `registry_login` | `deploy`. `level`: `needs_action` | `heads_up`.
   - `title` and `detail` are at most 300 characters and never contain a token, secret, env value, log line or email address. `href` is a console path.
   - `resolved_at` is `null` while the problem is open. One open problem per kind and subject; a problem that returns within 30 minutes of resolving reopens the same row.
   - **`host_offline`:** an `always_on`, live, not archived host whose `last_seen_at` is more than 10 minutes old. Resolved when the host reports again. `needs_action`. Not opened in the first 5 minutes after an API start.
   - **`registry_login`:** an image-access lookup with a stored registry login answered `login_failed`. Subject: the project and the registry host (a registry login has no id of its own). Resolved by a `private_ok` lookup with that login, a replaced login or a deleted login, or when the project no longer has a catalog image on that registry. Once an hour the API looks up the project's catalog images on that registry with the stored login, by name, until one answers other than `public` (at most one non-public answer per login per hour). `needs_action`.
   - **`deploy`:** the newest finished deploy row for an entry and host has outcome `failed`, `timeout`, `image_unavailable`, `forbidden_spec`, `conflict`, `rolled_back`, `rollback_failed`, `auto_rolled_back`, `auto_gave_up` or `auto_failed`. `needs_action` for `rollback_failed`, `auto_gave_up` and `auto_failed`, else `heads_up`. Resolved by the next `succeeded` or `unchanged` row for that entry and host, by a successful remove of the entry from that host, when the entry is deleted, or when the host is revoked or archived or the project loses `deploy` access to it (checked by the minute sweep).
2. **Audience** (computed per request, never stored per user): `host_offline` for owners and admins of the host's org; `registry_login` for owners and admins of the project and of its org; `deploy` for users holding the entry's `deploy` action, and owners and admins of the project and of its org.
3. **Notifications** (v1.37): items gain kind `problem`, with `level` and `state` (`open` | `resolved`) on problem items only; other kinds unchanged. Key `problem:<id>`. Open problems come first (`needs_action` before `heads_up`) and are never cut by the 20-item limit; problems resolved in the last 7 days fill the rest. A resolved problem is never unread.
4. **New routes:** `GET /v1/orgs/{id}/problems` (org owners and admins) and `GET /v1/projects/{id}/problems` (the problems the caller would see in the bell for that project). Both list open problems; `?resolved=1` adds problems resolved in the last 24 hours. Delegated (MCP) sessions are allowed. Response `{"items": [Problem]}`.
5. **MCP:** new tool `list_problems` (`org_id` or `project_id`, exactly one).
6. **Storage:** migration `0027_problems.sql` creates `app.problems` with a partial unique index on `(kind, subject) WHERE resolved_at IS NULL`. Resolved rows older than 30 days are deleted. Its down script drops the table and never refuses. `BOT_API_PROBLEMS=off` turns the detectors off (the routes then answer empty lists).

## v1.42 Running operations in deploy history, clear conflict messages (2026-11-21, approved spec `docs/spec-deploy-stuck.md` rev 1; no migration)

1. **`GET /v1/projects/{id}/catalog/{name}/deploys`** lists a deploy, rollback or remove that is still running for that entry and host first, with outcome `in_flight`, `finished_at` `null`, and a new field `ends_by` (RFC3339: the time after which the operation's lock clears at the latest). Finished rows are unchanged and carry `ends_by` `null`; the list holds the running row, if any, then the finished rows as before (v1.15 rev 2 item 5: up to 50 deploy rows plus up to 20 automatic rows). `in_flight` joins the Outcome values in responses only.
2. **Conflict messages** (code stays `conflict`, status 409):
   - Another deploy, rollback or remove of the entry is running on that host: `msg` "A deploy, rollback or remove of <entry> on <host> has been running since <HH:MM> UTC. Try again after <HH:MM> UTC.", and the body carries `started_at` and `ends_by` (RFC3339).
   - A confirmation whose plan no longer matches the entry: "The entry changed after this plan was made. Plan again."
   - An idempotency key reused with a different request body: "This request key was already used for a different request."
   - A deploy, rollback or remove that loses a race with one that has just started, when the other's times cannot be read: "Another deploy, rollback or remove of <entry> on <host> just started. Try again shortly."
   - The running-operation, just-started and changed-plan conflicts are not stored as the answer for the request's idempotency key: nothing started, so a retry with the same key after the operation ended runs normally.
3. **Wait for the agent:** for deploy and rollback the API waits for the task result for the task budget plus 30 s. The budget sent to the agent (`timeout_sec`) is unchanged. A result within that time is recorded with its outcome; `timeout` is recorded only after it.

## v1.43 Connect GitHub logins are refreshed (2026-11-21, approved spec `docs/spec-github-token-refresh.md` rev 1; migration 0028)

1. **Refresh:** when GitHub issues an expiring user token (8 h access token plus a refresh token) on Connect GitHub, the server keeps the sealed refresh token and both expiry times with the project's `ghcr.io` login and refreshes the access token before use (less than 30 minutes left; less than 75 minutes before a deploy or rollback task frame is built) and at least hourly (less than 90 minutes left). Registry rows and every response keep their shape: no token, refresh token or expiry is ever returned. A GitHub answer without expiry (a long-lived token) and manual logins behave as before.
2. **Rejected refresh:** when GitHub rejects the refresh token, the server stops refreshing that login (it is dead until someone reconnects) and audits `registry.github_refresh_rejected` with detail `{"host"}` once. The next lookup answers `login_failed` as before.
3. **Problem text:** the `registry_login` problem detail for a login with `source` `github` is "GitHub sign-in expired or was revoked. Reconnect GitHub in Settings." A manual login keeps "Token expired or revoked. Reconnect it in Settings."
4. **Setting:** `BOT_API_GITHUB_REFRESH` (optional) is empty or `on` (refresh) or `off` (no refresh at all; stored material is kept). Any other value stops the API at start with `BOT_API_GITHUB_REFRESH must be empty, on or off`.
5. **Disconnect race:** `DELETE /v1/projects/{id}/registries/{host}` for a `github` login whose login changed while the request waited (the row is no longer `github`, or holds another GitHub account) answers `409 conflict` "The registry login changed; reload and try again" and changes nothing (no delete, no revoke, no audit).

## v1.44 Host config files (2026-11-22, approved spec `docs/spec-host-config-files.md` rev 1a; migration 0029; additive; implemented in bot-control-plane work-hostfiles-api 2026-11-22, pi3 live check passed; not yet released)
1. **Who:** every route below is org owner or admin of the org that owns the host, console sessions only; delegated and MCP tokens get `forbidden`. Exception: the list route is readable by any member who can see the host, without content.
2. **`GET /v1/orgs/{org}/hosts/{host}/files`** → `{"items":[{"name","path","unit","secret","applied_version","latest_version","status","last_apply"}]}`; `status` is `in_sync | different | missing | wrong_owner | unknown`; `last_apply` `{version, outcome, stage, reason, at}` or null.
3. **`GET …/files/{name}`** (admin) → plain: `{"content", "versions":[{"version","size","sha256","by","at","applied"}]}`; secret: `{"keys":[{"name","set":bool}], "versions":[{"version","size","by","at","applied"}]}`. Never a secret value, preview or hash.
4. **`GET …/files/{name}/versions/{v}`** (admin) → `{"content"}` for a plain file; `forbidden` for a secret file.
5. **`PUT …/files/{name}`** (admin) → `{"version"}`. Plain `{"content"}` 1–65536 bytes UTF-8, saved exactly. Secret `{"base_version", "set":{"KEY":"value"}, "remove":["KEY"]}`; keys `^[A-Z_][A-Z0-9_]{0,63}$`, values one line, no NUL, ≤ 4096 bytes, ≤ 128 keys; a stale `base_version` is `conflict`. The newest 50 versions per file are kept (never the applied one).
6. **`POST …/files/{name}/apply {"version", "settle_sec"?, "force"?}` (`settle_sec` is sent to the agent only when given)** (admin) → waits up to 610 s (amendment A4; was 430); `200 {"outcome","stage","reason","took_ms"}`; `worker_busy` answers `409` code `worker_busy`; an apply already running for that file or another file with the same unit on the host is `conflict` (amendment A1); offline host `host_offline`; an agent without `host_files` `unsupported_agent`.
7. **Audit:** `host_file.save {"host","name","version","secret","size"}`, `host_file.apply {"host","name","version","outcome","stage","reason"}`.
8. **Problems:** kind `host_file` for org owners and admins: the last apply was `rollback_failed`, or the status is `different`, `missing` or `wrong_owner` for 30 minutes after an apply.
9. **Migration 0029:** `app.host_file_versions`, `app.host_file_applies`, `app.hosts.host_files_status`; the down script drops them and never refuses.

## v1.45 delete stale image versions (2026-11-22, spec `docs/spec-image-version-delete.md` rev 1 with §13 amendment A; migration 0030; additive; implemented on bot-control-plane `dev` (d27c6fb, 833099d, 77231bc); in the v0.2.45 release commit 5b986f2 on `main`, which has no tag on origin yet)
1. **Connect GitHub with delete access (opt-in).**
   - `POST /v1/projects/{id}/registries/github/start` accepts an optional `"delete_packages": true`. GitHub is then also asked for `delete:packages`.
   - If GitHub leaves that scope out, the connect fails with `bad_request` "GitHub did not grant delete:packages", and the stored login stays as it was.
   - A plain connect records whatever GitHub granted. A token refresh keeps the scopes.
   - The registry object gains **`delete_packages`** (bool): true only for a `github`-source login whose last connect was granted `delete:packages`; false for a manual row.
2. **`GET /v1/projects/{id}/image-versions?image=<ref>`**: project owner or admin, console sessions only (MCP-delegated: 403). Checks, in order:
   - GitHub must be configured (`unavailable` "GitHub is not configured").
   - The image must be on `ghcr.io` (`bad_request` "image versions are only available for ghcr.io images").
   - The project must have a Connect GitHub login (`bad_request` "connect GitHub in Settings to list image versions").
   - The per-user lookup limit applies (`rate_limited`).
   - The GitHub token is never echoed.
3. **Response** `payload {"items":[...],"truncated":bool,"can_delete":bool,"can_delete_reason":string|null}`.
   - Each item is `{"version_id","digest","tags","created_at","updated_at","in_use":[holder],"stale":bool,"release":bool}`.
     - `stale`: nothing holds the version and it was not updated for 14 days.
     - `release`: a tag looks like a release (`release-*`, `v1.2`, `1.2.3`).
   - `can_delete` is false, with a reason, when the caller is not an org owner or admin, or the login lacks `delete:packages`.
4. **Holders.** A holder is `{"project_id","project_name","entry","host_id","host_name","why"}`, ordered running first.
   - Holders come from the image entries of the same repository in every project of the org that the caller reaches; an org owner or admin reaches them all.
   - `why` is one of:
     - `running`;
     - `rollback_target`: one of the rollback route's newest distinct succeeded or unchanged digests (v1.6);
     - `recent_deploy`: among the newest 5 deploy rows per entry and host, whatever their outcome, an in-flight row included.
5. **`DELETE /v1/projects/{id}/image-versions/{version_id}?image=<ref>`** with body `{"digest","confirm"}`: same start checks as item 2, plus:
   - The caller must be an org owner or admin; otherwise `forbidden` "only org owners and admins can delete image versions".
   - The login must have `delete:packages`; otherwise `forbidden` with the reason.
   - An unknown `version_id` (not a positive integer, or not found at GitHub) → `not_found`.
   - The version's current digest must equal `digest`; otherwise `conflict` "this version changed; reload".
   - A held version → `conflict` "in use: <entry> on <host> (<why>)" with `payload {"in_use":[at most 10 holders],"count"}`.
   - `confirm` must be the first 12 hex characters of the digest (case-insensitive); otherwise `bad_request`.
   - On success: `200 {"deleted":true,"version_id","digest","tags"}`. The project's cached tags and digests for that repository are dropped.
   - GitHub failures (list and delete) → `registry_unavailable` with `payload {"reason"}`, one of `not_found`, `delete_refused`, `timeout` or `unreachable`; no GitHub body is echoed.
6. **Audit:**
   - `registry.image_version_deleted {"host","package","version_id","digest","tags","via"}`;
   - `registry.github_connected` gains `delete_packages`.
7. **`image-tags` order (amendment A):** for a `ghcr.io` image whose project has a Connect GitHub login, tags with a package-version `updated_at` come first, newest first (a tie keeps the v1.16 order). The rest follow in the v1.16 order. Other images keep the v1.16 order.
8. **Migration 0030:**
   - Adds `app.registries.delete_packages boolean NOT NULL DEFAULT false`, false for every existing row, and grants `deploybot_api` UPDATE on that column.
   - The down script drops the column.

## v1.46 image tags for agents (2026-11-23, spec `docs/spec-mcp-deploy-image.md` rev 1, option A; additive, no migration; implemented on bot-control-plane `dev` 414c61e (PR 5, on top of the v0.2.45 release commit); not yet released)
1. **MCP-delegated sessions may call `GET /v1/projects/{id}/image-tags` and `GET /v1/projects/{id}/image-digest`** (v1.16 items 1–2).
   - Before: console sessions only; any other session kind got 403.
   - After: console sessions and MCP-delegated tokens. Everything else is unchanged: the owner-or-admin rule, the allowed hosts, the outbound rules, the limits, the shared rate bucket, the cache and the errors.
   - The image-version routes (v1.45) stay console only.
2. **`image-tags` response, two additive fields.**
   - **`items[].updated_at`** (RFC 3339, UTC). It is present when GitHub's package versions gave the tag a time: a `ghcr.io` image whose project uses a Connect GitHub login (v1.45 amendment A). A tag on more than one package version takes the latest `updated_at`. Otherwise it is absent.
   - **`order`**: `"time"` when at least one item carries `updated_at`, meaning the list is newest first by push time; otherwise `"name"`, meaning the v1.16 version-name order.
   - New shape: `payload {"items":[{"tag":string,"updated_at"?:string}],"truncated":bool,"order":"time"|"name"}`.
   - Clients ignore unknown fields. A client talking to an API before v1.46 sees no `order`, and treats the list as `"name"`.
3. **Consumer:** the MCP tool `list_image_tags` (bot-control-plane `mcp/docs/design.md`).
   - Input: `project_id`, exactly one of `entry` (an image entry) or `image`, and `limit` (1–50, default 20).
   - Output: `tag`, `image` (`<repository>:<tag>`), `updated_at` and, with `entry`, `current`. It also returns `repository`, `order` and `truncated`.
   - Deploys still go through `set_entry_image`, `confirm` and `deploy_entry`.

## v1.47 why a deploy failed (2026-11-23, spec `docs/spec-deploy-reason.md` rev 1 in bot-control-plane; migration 0031_deploy_reason; with agent-protocol v1.19; implemented on bot-control-plane `dev` 48e1d4c/2451123, merged 949dc69)
1. **Stored with each finished deploy or rollback** (migration 0031, columns on `app.image_deploys`):
   - `reason` (text, at most 1024 bytes);
   - `final_status` (`{"state","exit_code","restart_count"}` from the agent's status; null when the state is unknown);
   - `log_tail` (the agent's redacted lines, the newest at most 50 lines and 16 KiB, after the API's own exact-value redaction).
2. **`ImageDeploy`** gains `"reason": string|null` and `"final_status": {...}|null`.
   - `reason` is null on `succeeded`, `unchanged` and a running row.
   - A row without a stored reason (older rows, agent auto actions, a stale row closed as `timeout`) answers a reason worked out from its outcome.
   - Both appear in the deploy and rollback answers and in `GET …/deploys`. The list never carries lines.
3. **Reason rules.**
   - When the agent sent a `reason` (agent-protocol v1.19: one printable line of at most 512 bytes, exact-value redacted), it is the cause.
   - Otherwise the API works one out: a job's non-zero `exit_code` → "the job exited with code N"; else from `final_status`: restarting or `restart_count > 0` → "the container kept restarting (N restarts)"; exited or dead → "the container exited with code N" / "the container stopped"; missing or created → "the container did not start"; running → "the container did not pass its health checks in time".
   - `rolled_back` appends "; rolled back; the previous version is running again" and `rollback_failed` "; the rollback failed too; the service may be down".
   - Other outcomes have fixed sentences (`image_unavailable`, `forbidden_spec`, `conflict`, `timeout`).
4. **Outcome `refused`** (new, in responses and storage).
   - When the host refuses the task (`unavailable`, `forbidden`, `bad_request`, `not_found` from the agent session), the in-flight row is finished as `refused` with no digest and `reason` = "<phrase>: <message>" (the fixed phrase when the message held a secret), instead of being deleted. The request still answers the same envelope error.
   - `refused` does not count for `last_deployed_at`, rollback targets, recent image-version holds (v1.45) or deploy problems (v1.41). An automatic agent action never copies the spec hash of a `refused` row.
   - A frame too large to send is still not a deploy (row deleted).
   - A host that drops while a sent task is waiting did not refuse it and the deploy may have run; v1.50 item 8 keeps that row `in_flight` for a late result or the sweep.
   - The single-deploy route shows a running row past its deadline as `timeout`, like the list.
5. **`GET /v1/projects/{id}/catalog/{name}/deploys/{deploy_id}?host_id=`** (console or delegated; the same permission as the list: any catalog action on the entry) → `{"deploy": ImageDeploy, "lines": [string]|null}`.
   - `lines` is the stored tail (`[]` when none), re-redacted with the entry's current secret forms; null for a caller who holds neither `logs` nor `deploy` on the entry.
   - A running row has outcome `in_flight` and `ends_by` (v1.42).
   - An unknown id, or one of another entry or host → `not_found`; a malformed id → `bad_request`.
6. **Migration 0031:** adds the three columns and allows outcome `refused`. Its down script deletes `refused` rows and drops the three columns; roll the API back to v1.46 first.
7. **MCP:**
   - New read tool `deploy_history` (`project_id`, `entry`, `host_id`, optional `deploy_id`, optional `limit` 1–20, default 10): the newest rows with `id`, outcome, digest, times, via, `reason` and `final_status`, and `detail`, the stored log tail of `deploy_id` or of the newest finished row (`lines` null without the logs or deploy permission). It reads stored data and works while the host is offline.
   - Deploy and rollback results (`confirm`) keep `deploy.id` and `deploy.reason`; when the outcome is not `succeeded` or `unchanged`, the text points to `deploy_history`. "still running on the host" points to it too.
   - `deploy_entry`'s attached history carries `reason` for rows that did not succeed and shows at most the newest `refused` row, which does not take one of the three rows kept for rollback choices. `list_problems` names `deploy_history` as the fix for a deploy problem.
8. **Console:** accepts `refused` (badge in the revoked tone) and shows `reason` under a deploy result and under each history row. An older console drops a `refused` row as unreadable.

## v1.48 agent upgrade from the console or MCP (renumbered from v1.47 on 2026-11-23 because of the clash above; migration **0032**) (2026-11-23, approved spec `docs/spec-agent-self-upgrade.md` rev 1; migration 0032; additive; with agent-protocol v1.20; implemented on bot-control-plane `dev`, merged 93fef50; released in v0.2.45)
1. **`POST /v1/hosts/{id}/agent-upgrade {"version"}`**: org owner or admin of the host's org. Console sessions and MCP-delegated tokens are allowed; the MCP server asks for `confirm` first.
   - Refusals, in order:
     - not found, or no access → `not_found` / `forbidden`;
     - a revoked host → `conflict`;
     - `version` differs from `latest_agent_version`, or the host is not older → `bad_request`;
     - the host's hello has no `programs.agent_upgrade: true` → `unsupported_agent` (409), msg "this host does not allow upgrades from Deploybot (runner.toml agent_upgrade)";
     - the host is offline → `host_offline` (503);
     - an upgrade for this host is `started` or `running` → `conflict`.
   - It inserts a row (`started`) and sends task `agent_upgrade` (agent-protocol v1.20).
     - **Task succeeded (accepted):** the row becomes `running`, and the route answers **`202 {"upgrade_id","state":"running"}`**.
     - **Task failed:** the row becomes `failed` with `reason` = the error code, and the route answers 409 `conflict` with `payload {"reason"}`.
     - **Transport failure:** the row becomes `failed`, reason `unavailable`, and the route answers 503.
2. **Settling a running row:**
   - **Implementation notes (2026-11-23):**
     - Only a result whose `version` equals the row's `to` and whose `at` is no earlier than 30 s before `started_at` settles the row. Hello repeats `last_upgrade`, so an older attempt must not settle a new row.
     - A row may settle from `started` as well as `running`.
     - On a task timeout or a lost connection after the task was sent, the route answers 503 `unavailable` but **leaves the row open**. The helper may already have started the upgrade, so a later result settles the row, or the 15-minute sweep marks it `unknown`. Only an answer from the agent (a failure or reject) marks the row `failed`.
   - From hello `last_upgrade` or the frame `agent_upgrade_result` whose `version` equals the row's `to`: `succeeded`, `rolled_back` or `failed`, with `reason`.
   - A hello with `agent_version == to` and no result also means `succeeded`.
   - After 15 minutes with neither, the row becomes `unknown`.
   - `finished_at` is set when the row settles.
3. **`GET /v1/hosts/{id}/agent-upgrades`**: anyone who can see the host. Returns `payload {"items":[{"id","from","to","state","reason","started_by":{"id","name"},"started_at","finished_at"}]}`, newest first, at most 20.
4. **Host object:**
   - `agent_upgrade_allowed` (bool): the hello flag, false when revoked or never seen;
   - `agent_upgrade`: the newest row or `null`.
   - Both appear wherever `update_available` does (v1.34).
5. **Audit:**
   - `host.agent_upgrade {"host","from","to"}` when an upgrade is started;
   - `host.agent_upgrade_result {"host","to","state","reason"}` when it settles.
6. **Problems:** kind `agent_upgrade`, needs_action, for org owners and admins. Its `href` is `/hosts/<id>#agent-upgrade`; the fragment lets a client tell it from `host_offline`, since bell items carry no kind (amended before release, 2026-11-23, spec-release-v0245 §8 question 2).
   - It opens when a row settles `rolled_back`, `failed` from a host result, or `unknown`.
   - It does **not** open for a synchronous refusal from the agent: `busy`, `not_newer`, `bad_spec`, `forbidden`, `policy_denied` or `helper_unavailable`. The admin already saw the 409, and the row still records it.
   - It closes on the next `succeeded` for that host, or when the host reports an `agent_version` at or above `latest_agent_version`, or equal to the newest row's `to`. An `unknown` row with that `to` then becomes `succeeded`.
   - A host result whose `at` is more than 5 minutes ahead of the API clock is ignored.
7. **Migration 0032:**
   - `app.host_agent_upgrades` (id, host_id, from_version, to_version, state, reason, started_by, started_at, finished_at), with an index on (host_id, started_at desc);
   - the problems kind check gains `agent_upgrade`;
   - narrow grants to `deploybot_api`.
   - The down script deletes `agent_upgrade` problems, restores the previous kind check and drops the table.
8. **MCP:** tool `upgrade_agent {host_id}`, which goes through `confirm`. `list_hosts` passes `agent_upgrade_allowed` and `agent_upgrade` through.
9. **Console:**
   - The "Update available" badge offers **Upgrade** when `agent_upgrade_allowed` is true. Otherwise the tooltip says how to opt in.
   - Upgrade opens a confirm dialog, then shows a running state and the outcome.
   - The host Overview lists the history.

## v1.49 host resource alerts (2026-11-23, approved spec `docs/spec-host-resource-alerts.md` rev 1; migration 0033; additive; implemented on bot-control-plane `dev` 2026-11-23, not yet released; ships with v1.50)
1. **Problem kind `host_resource`.**
   - Audience: org owners and admins of the host's org.
   - Subjects: `<host_id>:mem`, `<host_id>:disk:<path>`, `<host_id>:cpu`, `<host_id>:load`.
   - Levels: `needs_action` for mem and disk, `heads_up` for cpu and load.
   - The href is `/hosts/<id>?range=1h`.
   - Title and detail carry only the host name, the mount path and numbers.
2. **Sweep** (inside the 1-minute problems sweep, after the 5-minute grace; off with `BOT_API_PROBLEMS=off`).
   - Watched hosts are always-on, not revoked, online, with the newest sample under 2 minutes old and alerts enabled.
   - The sweep runs one grouped query per measurement over 15 minutes.
   - Default thresholds:

     | Check | Opens | Resolves |
     |---|---|---|
     | mem | avg `available` < max(15 % of total, 96 MiB) over 10 min | ≥ max(20 %, 128 MiB) over 5 min |
     | disk (each mount) | `used_percent` ≥ 90 for 10 min, latest included | < 85 over 5 min |
     | cpu | avg busy (100 − idle) ≥ 90 % over 15 min | < 75 % over 5 min |
     | load | avg load5 / `n_cpus` ≥ 2.0 over 15 min | < 1.2 over 5 min |

   - A mount no longer reported, a host no longer watched, or alerts switched off resolves the open problem with "No longer checked".
3. **`GET /v1/hosts/{id}/alert-settings`** (anyone who can see the host) and **`PUT …/alert-settings`** (org owner or admin; console sessions and MCP-delegated tokens).
   - Body: `{"alerts_enabled":bool, "mem_min_available_percent":5–50|null, "disk_max_used_percent":50–99|null, "cpu_max_busy_percent":50–100|null}`. Null means the default.
   - With an override, the resolve point is the override plus 5 points for memory, or minus 5 for disk and cpu.
   - Out-of-range values → `bad_request`.
   - Audit `host.alert_settings {"host", changed fields}`.
4. **Metrics query:**
   - `m=mem` adds `available` (average bytes per step).
   - `m=disk` accepts `&path=<mount>`, default `/`. An unknown path returns an empty series.
   - `GET /v1/hosts/{id}/disks` lists the mounts seen in the last 24 h: `{"items":[{"path","fstype"}]}`.
5. **Migration 0033:**
   - `app.host_alert_settings` (host_id pk, fk cascade; the four columns with checks; updated_at; updated_by);
   - the problems kind check gains `host_resource`;
   - narrow grants.
   - The down script deletes `host_resource` problems, restores the previous kind check and drops the table.
6. **MCP:**
   - `list_problems` describes `host_resource`;
   - new tools `get_alert_settings {host_id}` and `set_alert_settings {host_id, …}`, the latter through `confirm`;
   - `query_metrics` accepts `path` for disk.
7. **Implementation notes (2026-11-23):**
   - `PUT …/alert-settings`: `alerts_enabled` is required; the body holds only the four fields (unknown fields, an `idempotency_key` or a fractional number → `bad_request`). A revoked host → `conflict`. A PUT that changes nothing writes and audits nothing. MCP-delegated tokens call it directly (the MCP server keeps the confirm step, as for v1.48).
   - Memory floors: the 96 MiB open and 128 MiB resolve floors apply only to the default. With `mem_min_available_percent` set, the percent alone decides.
   - For an open problem the resolve test runs first, so 5 minutes of recovery resolve it even while the longer open window still holds the bad minutes.
   - Coverage: a window opens or resolves only with at least half the samples the metrics interval (10 s) gives it: 15 of 30 for 5 minutes, 30 of 60 for 10, 45 of 90 for 15.
   - Disk: at most 50 mounts per host are judged, in path order. A mount the host stops reporting resolves with "No longer reported: the host no longer reports this mount."; other cases keep "No longer checked: …".
   - Disk subject: `<host_id>:disk:<path>` when the path is printable and the subject stays within 300 characters; otherwise `<host_id>:disk:#<first 16 hex digits of sha256(path)>`. Titles show the mount with control characters replaced and cut to 64 characters with "…".
   - Disk href: `/hosts/<id>?range=1h&disk=<url-escaped path>` (for a hashed subject, the path from the host's mounts of the last 24 h; without a match, no `disk`).
   - Metrics `path`: more than 1024 characters or a control character → `bad_request` "invalid path"; ignored for other `m`.
   - `GET /v1/hosts/{id}/disks`: anyone who can see the host, MCP-delegated too; at most 200 mounts, sorted by path.

## v1.50 Deploybot on containers (2026-11-23, approved spec `docs/spec-deploybot-on-containers.md` rev 1; additive; migration 0034_deploy_task; ships with v1.49; implemented on bot-control-plane `dev` 58c52a6, merged 8e432ad)
1. **ImageSpec `network`** (agent-protocol v1.21 item 3): optional, absent or `"host"`; any other value is `bad_request`. Stored and returned with the entry (REST and MCP `create_catalog_entry`, `update_catalog_entry`, `get_catalog_entry`). With `"host"`, `networks` must be empty (`bad_request`). A deploy, rollback or run of such an entry on a host whose hello lacks `programs.host_network: true` is refused `409 unsupported_agent` ("this host does not allow host networking (runner.toml host_network_projects)") before any task is sent; the agent's own `policy_denied` still applies per project.
2. **Secrets as env values:** each of `BOT_SECRETS_KEY`, `BOT_API_MCP_CLIENT_SECRET`, `BOT_API_GITHUB_CLIENT_SECRET` and `BOT_MCP_CLIENT_SECRET` may be given as an env value instead of its `*_FILE`. Setting both forms is a start-up error naming the variable. The same length and format checks apply; the files keep their 0600 rule. Values are never logged.
3. **Late deploy results:**
   - A task result for an image deploy, rollback or run whose `image_deploys` row is still `in_flight`, or was settled `timeout`, settles it with the reported outcome (and v1.47 reason, final status and log tail), whichever API process receives it, including after an API restart. A row already settled otherwise is left alone. The API answers every such result with `task_result_ack {"task_id"}`.
   - The settle is audited like an on-time result, with `"late": true`.
   - A result for an unknown task id is acked and dropped (logged with the id only).
4. **`deploybot-server health <url>`:** exits 0 when a GET answers 2xx within 3 s, else 1. Only `http://127.0.0.1:<port>/…` and `http://localhost:<port>/…` are accepted (else exit 2). For the image's Docker `healthcheck`, since the base image has no curl.
5. **Images** (not a contract surface, recorded here for consumers): `ghcr.io/more-than-code/deploybot-server` (static binary, distroless nonroot) and `ghcr.io/more-than-code/deploybot-console` (node 24 slim, `NODE_OPTIONS=--max-old-space-size=160` in its `CMD`), public, built reproducibly from the release tag and deployed by digest.
6. **`env_hash` on every image task (clarification, 2026-11-23):** to an agent whose hello has `programs.env_hash: true`, the API sends `env_hash` on every `image_deploy` and `image_rollback`, also for an entry with no env set (the hash of its empty env), so a changed host env file (agent-protocol v1.21 item 6) is never `unchanged`.
7. **Ack on a store error (clarification, 2026-11-23):** the API withholds `task_result_ack` only when settling the result hits a store error, so the agent keeps its file and resends after the next hello. Every other result, including unknown task ids and rows already settled, is acked.
8. **A dropped connection keeps the row (amends v1.47, 2026-11-23):** when the agent connection drops while the API waits for an image deploy or rollback result, and the task frame was already written to the agent, the `image_deploys` row stays `in_flight` (the caller still gets `unavailable`); a later result settles it (item 3), else a stateless sweep (every minute, any API process) settles `in_flight` rows whose `in_flight_until` has passed (task budget + 30 s result margin + 120 s grace, the v1.42 lock deadline) as `timeout`, audited with `"swept": true`. When the frame was never written, the row is recorded `refused` as in v1.47 (the host never ran it), so it is never left in flight. Late `image_start` (run) results have no row and are acked and dropped. Migration **0034_deploy_task** stores the task id and action on the row (`task_id uuid`, `task_action` deploy|rollback, partial unique index), so any API process can settle it.
9. **Review changes (2026-11-23, before release):**
   - `env_hash` (item 6) is sent when the API has a secrets key (`BOT_SECRETS_KEY[_FILE]`), as since v1.27; production always has one. Without a key, a changed host env file is not detected.
   - The ack (item 7) for an image deploy or rollback, on time or late, is sent only after the row is written; on a store error the row stays `in_flight` (swept later) and no ack is sent.
   - A `timeout` row is re-settled only by a different outcome; a repeated `timeout` result is acked and dropped without audit. A result settles only the same host's task (the live waiter checks the delivering connection too).
   - A late result's log tail is redacted against the entry found by its id; when its secrets cannot be resolved, the tail is stored withheld. A late `refused` is audited as a denial with its error code.
   - When the live waiter finds its row already settled by a late result, it answers from the stored row.
   - `deploybot-server health` dials 127.0.0.1 for `localhost`. The server image has no baked HEALTHCHECK; each entry sets its own (e.g. `["/usr/local/bin/deploybot-server","health","http://127.0.0.1:8190/health"]`), so another process on a default port cannot mask a broken container.
   - Readiness checks host networking for `network: "host"` entries; MCP `list_catalog` shows `network`; delegated deploy, rollback and run plans say "runs on the host network".

## v1.51 host policy from the console (2026-11-23, spec `docs/spec-console-host-policy.md` rev 2 in bot-control-plane, as implemented; with agent-protocol v1.22; no migration; implemented on bot-control-plane `dev` 70de9e7, merged 408a811)
Principle (DD `DEF-20261123-design-principle-privileged-steps-are-do-exut`): no MCP tool and no delegated session changes a host's policy. MCP hands the person a console link instead (item 6).
1. **Hello fields read** (agent-protocol v1.22): `programs.console_policy` (bool, the host's root opted in with `console_policy = true`) and `programs.image_patterns` (the runner.toml `images`, sent only when `console_policy` is true). A host is **managed** when `console_policy` is true and the hello carries a `policy_hash`.
2. **`GET /v1/orgs/{id}/hosts/{host_id}/policy`** (org owners and admins; console sessions only, a delegated session → 403) → `{"managed", "policy_hash", "edge", "images", "projects", "apply"}`.
   - `edge` and `projects` come from hello; `images` only on a managed host (else `[]`).
   - `apply` is the last apply this API process started, kept in memory (no migration), or `null`: `{"state","started_at","ends_by","started_by":{"id","name"},"change","finished_at"}`.
   - `state` is `running`; `applied` (a hello carried the new hash); `rolled_back` (after 150 s the host is back on `base_hash`); or `unknown`. A settled apply is shown for 24 h.
3. **`POST /v1/orgs/{id}/hosts/{host_id}/policy`** `{"base_hash","edge","images","projects"}` (org owners and admins; console only, `delegatedDeny`).
   - It validates with the agent's rules (strict keys, valid patterns and slugs, no repeats, at most 64 images and 32 projects).
   - Refusals: the host not online → `host_offline`; not managed → `409 unsupported_agent` ("this host's root has not let the console change its policy: add console_policy = true to /etc/bot-agent/runner.toml once and restart bot-agent (bot-agent 0.2.15 or later)"); `base_hash` differs from the host's → `conflict`; an apply already running → `conflict` with `started_at` and `ends_by`; nothing to change → `bad_request` `nothing to change`.
   - It sends task `host_policy_apply` (60 s budget) and answers **`202`** with the apply (`state` `running`).
   - Task refusals map to the same codes: `policy_denied` or `forbidden` → `unsupported_agent`; `conflict` → `conflict` (the policy changed since the page loaded); `busy` → `conflict` ("the host is busy with another change; try again in a minute"); `bad_spec` → `bad_request` ("the host refused this policy"); `helper_unavailable` → `unavailable`. No answer → `unavailable` ("the host did not answer; the change may still apply, so reload in a minute").
4. **Audit:** `host.policy_apply` (the change: edge before and after, patterns and slugs added and removed; never the file) and `host.policy_result` (`{"host","outcome"}`, via `system`, once when the apply settles). No problem kind.
5. **Readiness (`check_readiness`):** on a managed host the image check is exact, since the host's patterns are known. A `host_policy_snippet` fix carries `image_pattern`. New fix actions `allow_edge`, `turn_on_edge` and `verify_domain` (with `org_id`).
6. **MCP deep links:** with `BOT_MCP_CONSOLE_ORIGIN` set on the MCP server, fixes whose step is a person's carry `fix.console_url`, and `host_policy_snippet`, `image_access`, `list_hosts`, `get_catalog_entry` and `list_problems` carry the page to open. The Policy tab link stages a change (`?tab=policy&policy.edge=on`, `&policy.add_image=<pattern>`, `&policy.add_project=<slug>`) and never applies it. Unset: no links, results as before. Tool descriptions tell the agent to post the `console_url` and not describe clicks.
7. **Console:** host page tab **Policy** (`?tab=policy`, org owners and admins): Edge switch, Images and Projects lists, a diff of the change, Apply with a confirm, then the result, re-read every 5 s while it runs. A host that is not managed shows the snippet and the one line the root adds.

## v1.52 assign one name of a domain (2026-11-23, spec `docs/spec-domain-assign-names.md` rev 1 in bot-control-plane; migration 0036; no agent-protocol change; implemented on bot-control-plane `dev`, merged b4335ce (PR 13))
1. **Assignment `pattern` is relative to the domain name**, like a DNS zone file. Four shapes:
   - `@`: the domain itself (unchanged);
   - `*`: any one label below the domain (unchanged);
   - a relative name of one or more labels, such as `marketingos` or `api.dev`: exactly `marketingos.<domain>`;
   - `*.` and a relative name, such as `*.dev`: any one label below `dev.<domain>`.
   - Labels are lowercase LDH (IDNA A-labels included), each at most 63 characters, and the name written out is at most 253 characters. `PUT …/domains/{id}/assignments` refuses anything else as `bad_request` with the shape message, which names all four forms. The same project and pattern twice in one body is still refused. At most 64 assignments per domain, unchanged.
2. **A route is allowed** when a `verified` domain assigned to the project covers its hostname under one of its patterns. `*` matches exactly one label; `*.dev` exactly one label below `dev.<domain>`; a relative name only itself. No new DNS record is needed.
3. **Domain delete** plans and removes the routes on the domain and one label below it, as before, and also the routes deeper below it that no more specific domain of the org holds (whatever that domain's status or assignments). It still leaves a route that another verified or lapsed domain assigned to the same project covers.
4. **The Assign link.** The API sends ids; the MCP server builds `console_url` (v1.51 item 6).
   - `PUT …/catalog/{name}/routes` refusing "<hostname> is not under a verified domain assigned to this project" carries a payload when the hostname is under a `verified` domain of the project's org (the most specific one): `{"hostname","domain","domain_id","org_id","pattern","name"}`. `pattern` is the narrowest pattern that assigns exactly that hostname, and `name` is it written out. The message is unchanged. A hostname under no verified domain of the org has no payload.
   - The readiness `routes` warn for the same case has fix `{"action":"assign_domain","org_id","domain_id","pattern","name"}` instead of v1.51's `verify_domain`, which stays for a hostname under no verified domain and for a lapsed one.
   - MCP has no assign tool (the API refuses delegated sessions on domain routes). The link is `<console>/orgs/<org>?tab=settings&domains.assign=<domain>&domains.project=<project>&domains.pattern=<pattern>`; `set_routes` appends one sentence to the refusal naming the domain and the name, with `Open: <link>`, and `check_readiness` puts it in the fix's `console_url`. Without a console origin the sentences stay and the link is left out.
5. **Console:** the Assign dialog's pattern is a text field with a preview of the full name; a pasted full name, a trailing dot and upper case are tidied to a pattern. Assignment rows write the pattern out. The deep link opens that domain's Assign dialog once, with the row added for the admin to check and save.
6. **Migration 0036** only relaxes `domain_assignments_pattern_chk`; no row changes. Its down script deletes the assignments that are not `@` or `*` and restores the old check. Roll the API back to v1.51 first.

## v1.53 why ghcr.io refused the GitHub connection (2026-11-23, spec `docs/spec-registry-refusal-reason.md` rev 1 in bot-control-plane; no migration; implemented on bot-control-plane `dev`, merged c56ece1 (PR 14))
1. **Diagnosis.** When an image-access lookup ends `login_failed` with the GitHub connection (`login` `github`, registry `ghcr.io`), the API asks the GitHub REST API with the same token ghcr.io refused (no second token read or refresh):
   - `GET /user`: a 401, or a token without `read:packages` in `X-OAuth-Scopes`, is `github_sign_in`;
   - when the image's owner (first path segment) is the connected login (case-insensitive), no refusal;
   - otherwise `GET /orgs/{owner}/packages/container/{name}`: a 403 whose body names "OAuth App access restrictions" is `org_restricted`; a 404 is `no_package_access`; anything else, no refusal.
   - No GitHub body is returned, logged or stored. A failed call means no refusal. The answer is cached with the access answer (60 s). Manual logins are never diagnosed. No new scope.
2. **`ImageAccess.refusal`** (optional; only with `access` `login_failed` and `login` `github`, and only when GitHub told): `{"reason": "github_sign_in"|"org_restricted"|"no_package_access", "owner"?, "account"?, "url"?}`. `owner` is the image's owner, `account` the connected GitHub login, `url` (`org_restricted` only) `https://github.com/settings/connections/applications/<client_id>`, where an org owner clicks Grant and a member clicks Request.
3. **Readiness:** new fix action `github_org_access` with `registry`, `owner` and `url`, for `org_restricted`; the `registry` check message names the organization and says reconnecting will not help. `no_package_access` keeps `connect_github` with a message naming the account and the package owner; `github_sign_in` says the sign-in expired, was revoked or lost `read:packages`.
4. **Problems:** the `registry_login` detail (bell, `list_problems`) says the same, with the GitHub URL for `org_restricted`, within the 300-character limit. `github_sign_in` and an unknown reason keep the v1.43 detail.
5. **Consumers:** MCP `image_access` passes `refusal` (allowlisted fields; `url` must be GitHub's application page) and, for `org_restricted`, `github_url` instead of a console link, telling the agent to send that link and not to ask for a reconnect; `check_readiness` keeps `owner` and `url` on the fix, with no `console_url`; `list_problems` says to send the GitHub link instead of `connect_registry`. The console shows which organization has not granted the app, with Grant on GitHub.

## v1.54 regenerate a secret env value (2026-11-23, spec `docs/spec-env-regenerate.md` rev 1 in bot-control-plane; additive, no migration, no agent-protocol change; implemented on bot-control-plane `dev`, merged 1513661 (PR 15))
1. **Routes:** `POST /v1/projects/{id}/env-sets/{set}/keys/{key}/regenerate` and `POST /v1/projects/{id}/catalog/{name}/private-keys/{key}/regenerate` (the entry's private set `@<name>`). Project owner or admin. **Console sessions only** (`delegatedDeny`).
   - Body `{"generate": <v1.27 item 1 object>}`; no body or no `generate` → `bad_request` `send generate`; otherwise the v1.27 generate messages.
2. **Checks:** the key must exist (`not_found`) and be secret (`bad_request` `{KEY} is not secret; edit it to change it`). Any secret key qualifies, generated or typed (a rotation), including a `needs_value` placeholder, which it fills.
3. **Effect:** a new CSPRNG value is sealed and stored with `generated` and `reveal_pending` true, in one transaction with the set re-hash, like a key PUT. Running copies keep the old value until their next deploy or rollback; nothing is sent to an agent.
4. **Answer:** the value is not returned. `200 {"key","is_secret":true,"preview":"••••","reveal_pending":true,"used_by":[entry names]}`; `used_by` is the set's consumers (v1.33 item 4), empty when that read fails. The v1.27 reveal route then shows the value once.
5. **Audit:** `env_set.key_regenerate` with `set`, `key`, `kind` and `used_by`; never a value.
6. **Console:** a Regenerate row action (`RotateCcw`) on every secret key with a value, for owners and admins, in Env sets and in an entry's private keys; the dialog regenerates, then reveals the value once. Deep link `/projects/<id>?tab=settings&env.set=<set>&env.key=<KEY>&env.do=reveal|regenerate#env-sets` focuses the row's Reveal once or Regenerate button and never opens the dialog or acts.
7. **MCP:** no regenerate tool. `list_env_sets` carries `console_url` on each secret key with a value: the Reveal once link while `reveal_pending`, else the Regenerate link; a private set's key links to its entry. Empty without `BOT_MCP_CONSOLE_ORIGIN`.

## v1.55 a refused deploy names the field and keeps its plan (2026-11-23, spec `docs/spec-refused-deploy.md` rev 1 in bot-control-plane; with agent-protocol v1.23; no migration; implemented on bot-control-plane `dev`, merged f96cfa9 (PR 17))
1. **The field in the answer.** The API keeps a `task_reject` `field` (agent-protocol v1.23) only with `error_code` `bad_spec` and only when it has the v1.23 shape. A deploy or rollback refused with a field answers `400 bad_request` with
   `the host's agent refused the task as malformed (bad_spec): field "<field>" did not pass the host's checks (the agent names the field, never its value).`
   then a fixed hint for `spec.secret_env`, `env`, `registry_auth`, `files`, `spec.files` and `spec.volumes`, then `Check that the agent is up to date (upgrade_agent), then compare that field with the ImageSpec rules`.
   - The field is quoted so key=value redaction leaves it alone. Without a field the v1.47 sentence stays.
   - The refused row's `reason` (v1.47) is the same text, so `deploy_history` and the console's reason line show it too.
2. **A refused confirm keeps its plan.** When `POST /v1/confirmations/{id}` performs an image deploy or rollback and the host refuses it (a `task_reject`, or the host unavailable before the frame was written; not when the frame may have reached the host), the confirmation's `used_at` is cleared and its `expires_at` becomes at least now + 5 minutes.
   - The answer adds `. Nothing ran on the host, so this plan is kept: after fixing the cause outside the entry (for example upgrade_agent, the host's runner.toml or an env set), confirm it again with the same confirmation_id and idempotency_key until HH:MM UTC. A change to the entry itself needs a new plan`.
   - This holds even when the plan's own 5 minutes ran out while the host was asked. It happens only when the refused row was written; if that write failed, the row stays in flight for the sweep (v1.50 item 8) and the plan stays used.
   - Failures before the frame is built (a running operation, a bad `${KEY}` reference) still use up the plan.
   - The idempotency key, owner and plan checks are unchanged; a changed entry still answers the v1.42 "Plan again" conflict. Console actions have no plan and get no such sentence. The stored reason and the audit row do not carry it.
3. **MCP:** `confirm`'s description says a host-refused deploy or rollback keeps the plan. Messages pass through unchanged.

## v1.56 regenerate only a generated value (2026-11-24, Defect Drainer DEF-20261123-regenerate-is-offered-on-hand-entered-se-c0hz and -1fw3, approved by Joe "Yes, file both in DD, then work here"; amends v1.54; additive field, no migration; implemented on bot-control-plane `dev`)

1. **`generated` on env keys.** Every env key object carries `"generated": bool`: on `GET` env sets and the env set list (the delegated names-only reads MCP uses too), a key's `PUT` answer, `private_keys` on `GET /catalog/{name}`, and the regenerate answer. Only a secret can be generated; a plain key is always false. It is true while the stored value is one the API generated (v1.27). A typed write clears it (migration 0017). An older API sends no field, which a client reads as false.
2. **Regenerate refuses a hand-entered value.** On both regenerate routes (v1.54 item 1), a secret key whose value was entered by hand (not generated, and not an empty `needs_value` placeholder, which Regenerate may still fill) answers `400 bad_request` with `<KEY> was entered by hand, so it cannot be regenerated; edit it to paste a new value`.
   - Nothing is written and no `env_set.key_regenerate` is audited.
   - A random value would destroy a provider-issued secret (an API key). Change such a value with the key PUT (Edit).
   - Order of checks: a missing set or key is `not_found`; a plain key is the v1.54 "not secret" answer; a hand-entered one is this answer (`store.ErrNotGenerated`).
3. **Console** (amends v1.54 item 6):
   - **Where Regenerate shows:** only on a secret key with `generated` true. A hand-entered secret keeps Edit and Delete.
   - **The confirm text** leads with the consequence: "Nothing changes in running services until you redeploy <used_by>.", then warns: "If this value was also set somewhere else (a database user, a first-start setting), change it there too, or the app will fail after the redeploy."
   - **The line under the new value:** "Redeploy <used_by> now to use the new value; until then they keep the old one."
4. **MCP** (amends v1.54 item 7):
   - `list_env_sets` keys carry `generated`.
   - The Regenerate `console_url` is given only for a generated value; a hand-entered secret gets no link.
   - The tool description says nothing redeploys, and that a value also set elsewhere must be changed there too.


## v1.57 release notices dated by when this server first served them (2026-11-24, approved spec `docs/spec-release-first-served.md` rev 1 (umbrella), Joe "Approve"; Defect Drainer DEF-20261124-bell-release-rows-sink-below-today-s-ite-0kny; amends v1.37 item 1; additive field; migration 0038; implemented on bot-control-plane `dev`)

1. **Release `at`** on `GET /v1/me/notifications`: when this server first served the release, if it has a record; otherwise the release date at 00:00 UTC, as before. The 90-day window and the at-most-5 rule still use the CHANGELOG date. Sorting is unchanged (by `at`, newest first), so a recorded release sorts by its real time.
2. **`date_only`** (release items only, `bool`): `true` when `at` is only the CHANGELOG date at 00:00 UTC, `false` when it is a recorded time. Other kinds carry no `date_only`. A client that receives no field (an older server) treats the release as dated.
3. **Recording:** at startup the API records its own version (the newest entry of its embedded CHANGELOG) with the current time, unless the version already has a record; the first time is kept across restarts, redeploys and replicas. A version that is not `X.Y.Z` (1 to 4 digits each) is not recorded. A failed write logs one warning and the API starts anyway; a failed read dates every release by its CHANGELOG date.
4. **Storage:** migration `0038_release_first_served.sql` creates `app.release_first_served (version text PRIMARY KEY, first_served_at timestamptz NOT NULL)` with a check on the `X.Y.Z` shape, and grants `deploybot_api` SELECT and INSERT only. `0038_release_first_served.down.sql` drops the table (hand-run).
5. **Console:** a release row with `date_only` false shows its age ("1 h ago") and the full local time on hover; with `date_only` true it shows its date ("24 Nov", in UTC) and "Released 24 Nov 2026" on hover.
6. MCP and `whats_new` are unchanged: they keep CHANGELOG dates.
