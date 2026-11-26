# Spec: restart or roll back on unhealthy with the image's own HEALTHCHECK

Status: rev 1, approved by Joe on 2026-11-26 ("Approve as written"); amendment 1a (rollout order, §6) waits for Joe's OK. Tier 3 (api-v1 input rule, agent-protocol rule, user-visible editor flow).
Contracts: **api-v1 v1.63** and **agent-protocol v1.28** (next free numbers on 2026-11-26: api-v1 v1.61 is claimed by open PR #37, v1.62 merged in #44; agent-protocol v1.27 is in bot-agent v0.2.19).
Defect: Defect Drainer `DEF-20261126-on-unhealthy-restart-refused-when-an-ent-srsy`.
Repos: bot-control-plane (API, MCP, console) and bot-agent (validation, deploy check, hello gate). One PR per repo, paired.

## 1. Problem

`health_policy.on_unhealthy` = `restart` or `rollback` is refused unless the entry sets `spec.healthcheck`:

- API: `bad_request` `health_policy.on_unhealthy needs a healthcheck` (`api/internal/catalog/health_policy.go`, api-v1 v1.15 item 1).
- MCP: the same check before the call (`mcp/internal/tools/catalog_write.go`).
- Console: "Restart and Roll back need a Docker health check command" (`console/src/lib/image-spec.ts`).
- Agent: `bad_spec` `spec.health_policy.on_unhealthy` (agent-protocol v1.9 rev 2 item 1: "so an image-defined HEALTHCHECK alone never triggers actions").

Since agent-protocol v1.26 (bot-agent v0.2.19), an entry with a host env file may run only a healthcheck root pinned in `runner.toml`, and a pin has no quotes or shell. A check like the console image's `node -e "fetch(...)"` cannot be pinned, so the entry must drop `spec.healthcheck` and use the image's HEALTHCHECK, and then loses automatic restart. Real case: us1 `console`, now `on_unhealthy = none`.

The supervisor itself does not need `spec.healthcheck`: it acts on Docker's health from `container_list` (v1.9 item 4), and Docker reports health for an image HEALTHCHECK the same way. Only the validation rules stand in the way.

The one real risk the old rule covered: an entry with neither its own healthcheck nor an image HEALTHCHECK. Docker health is then always `null`, and the policy silently never fires.

## 2. Decisions

| # | Question | Choice | Alternative not taken |
|---|---|---|---|
| D1 | Allow `on_unhealthy` without `spec.healthcheck`? | **Yes.** The trigger is Docker health, whichever check Docker runs (the entry's or the image's). | Keep the rule and fix the console only (see D5). |
| D2 | Neither the entry nor the image has a check | **The agent fails the deploy** with a clear reason, and `on_deploy_failure` applies as for any failed health wait. The API cannot know at save time (it never inspects images). | Save-time warning only, or let it deploy and never fire (silent). A warning nobody reads leaves the hung-service case unguarded; a failed deploy says exactly what to fix. |
| D3 | How the agent tells "no check at all" | After `container_deploy`, `container_list` `health` is `null` for the new container. Docker sets `starting` at start whenever any HEALTHCHECK (entry or image) is configured, so `null` means none. No new helper verb, no image inspect. | A helper image-inspect call (root helper change, larger blast radius). |
| D4 | Older agents (≤ v0.2.19) still refuse this spec | **Gate:** hello `programs.image_healthcheck_policy: true`. For a spec with `on_unhealthy` ≠ `none` and no `healthcheck`, deploy and rollback-by-pull to a host without the flag answer `409 unsupported_agent` ("upgrade this host's agent"), like v1.50 `host_network`. Saving the entry is not gated. | No gate: the old agent's `bad_spec` (v1.55 shows the field). Works, but reads as a spec error rather than "upgrade the agent". |
| D5 | Console-only stopgap | **Not in this spec.** Optional follow-up: ship `/app/healthcheck.js` in the console image so the console's check is pinnable (`/usr/local/bin/node /app/healthcheck.js`). Only fixes one image. | — |
| D6 | Deploy wait with an image HEALTHCHECK | **Unchanged.** Without `spec.healthcheck` the deploy wait stays the v1.5 rule (running, no restarts); it does not wait for the image check to turn `healthy`. The supervisor covers it after `unhealthy_after_sec`. | Waiting on any Docker health: a behaviour change for every entry with an image HEALTHCHECK, out of scope. |

## 3. Contract diff

### api-v1 v1.63 (amends v1.15 item 1 and rev 2)

Before: `on_unhealthy` other than `none` without `healthcheck` → `bad_request` (`health_policy.on_unhealthy needs a healthcheck`).

After:

1. That rule is removed. `on_unhealthy` other than `none` is accepted with or without `healthcheck`; it acts on Docker's health check: the entry's `healthcheck` when set, otherwise the image's own HEALTHCHECK. The `keep` rule is unchanged.
2. Deploy and rollback (by pull) of a service whose spec has `on_unhealthy` ≠ `none` and no `healthcheck`, to a host whose hello lacks `programs.image_healthcheck_policy`, → `409 unsupported_agent` with message `this host's agent must be upgraded to use on_unhealthy with the image's own health check`. Nothing is sent to the agent.
3. A deploy that fails because neither the entry nor the image has a health check is stored like any failed deploy, with the agent's reason (v1.47).
4. No change to `spec_hash`, stored JSON, plans or auto_action authority (v1.15 rev 2 `autoAllowed`).

MCP: `create_catalog_entry` / `update_catalog_entry` drop the local check and their descriptions say "on_unhealthy uses the entry's healthcheck, or the image's own HEALTHCHECK when the entry has none; a deploy fails if neither exists".

Console: the editor no longer blocks Restart / Roll back without a command. Under the policy select, when the Command field is empty: "Uses the image's own HEALTHCHECK. If the image has none, the deploy fails." The `DOCKER_IMAGE_DEFAULT` text stays.

### agent-protocol v1.28 (amends v1.9 rev 2 item 1)

Before: `on_unhealthy` other than `none` requires `spec.healthcheck` → `bad_spec` otherwise.

After:

1. That rule is removed (the `keep` rule stays). Labels at create (v1.9 item 3) and the supervisor (item 4) are unchanged: they already act on Docker's health, whichever check produced it.
2. **No-check failure.** In `image_deploy` and in `image_rollback` by pull, for a service with `on_unhealthy` ≠ `none` and no `spec.healthcheck`: after `container_deploy`, the first `container_list` that lists the new container `running` with `health` `null` fails the health wait (the agent polls until it is listed or the wait ends; a listed container that is not running is left to the normal wait, which names its state, because Docker reports health only for a running container) with reason `the container has no Docker health check, so on_unhealthy can never act: set a healthcheck on the entry or use an image with a HEALTHCHECK`. The failed wait is handled by `on_deploy_failure` exactly as any other (v1.9 item 2, v1.16). Any non-null `health` (`starting`, `healthy`, `unhealthy`) passes this check, and the v1.5 wait continues unchanged.
3. **Gate:** `hello.programs` gains `"image_healthcheck_policy": true`.
4. No new helper verb or privilege; `runner.toml` unchanged.

## 4. Changes

bot-control-plane:
- `api/internal/catalog/health_policy.go`: drop the healthcheck rule; `decodeHealthPolicy` no longer needs `hc` (keep the signature change minimal).
- `api/internal/agentws`: read `programs.image_healthcheck_policy` from hello next to the other program flags.
- The deploy and rollback handlers: the v1.63 item 2 gate, beside the `host_network` gate.
- `mcp/internal/tools/catalog_write.go` and descriptions; `console/src/lib/image-spec.ts` and the editor hint.
- `docs/contract-status.md`: one sentence for v1.63.

bot-agent:
- `internal/agent/images/spec.go`: drop the rule.
- `internal/agent/run/image.go`: the no-check failure in the health wait.
- `internal/agent/protocol`: the hello flag.
- `docs/contracts/agent-protocol-v1.md`: v1.28 section (agent copy; frozen copies at release).

## 5. Tests

- API catalog and handler tests: the two "needs a healthcheck" cases become accepted; `keep` still refused; gate returns `409 unsupported_agent` for an agent without the flag and passes with it; an entry with its own healthcheck is not gated.
- MCP: the local check gone; descriptions.
- Console: `image-spec` validation accepts restart without a command; hint shown.
- Agent: `Validate` accepts the spec; deploy with `health` `null` fails with the reason and rolls back per `on_deploy_failure`; `starting` or `healthy` passes; entry with its own healthcheck unchanged; hello carries the flag. `-race` on `./internal/agent/run/...`.

## 6. Rollout and rollback

1. Merge both PRs into dev; release server v0.2.51 and bot-agent v0.2.20.
2. **Deploy the server first, then `upgrade_agent` on the hosts.** (Amendment 1a, found while implementing: the API reads hello `programs` with unknown fields refused, and the agent always sends the new key, so an API before v1.63 stores a v0.2.20 agent's programs as disabled. On such a host the old API then refuses deploys and `upgrade_agent`, drops Docker health samples, and leaves `auto_action` frames unacknowledged until the API is upgraded. This is the same constraint `programs.agent_upgrade` (v1.20) had. The gate still keeps an old agent safe on a new API.)
3. **Never run an API older than v1.63 while any v0.2.20 agent is connected.** Downgrading agents is a host-side step for the Ops session, since `upgrade_agent` only offers the latest version.
3. On Joe's yes: set us1 `console` back to `on_unhealthy = restart` and redeploy.

Rollback: revert the agents first, on the hosts (1a), then the API. An entry saved with restart and no healthcheck then fails the old API's validation on its next save and the old agent's on its next deploy (`bad_spec`), which is the behaviour before this change; running containers keep their labels and the supervisor keeps acting.

## 7. Acceptance

- us1 `console` with no `spec.healthcheck` and `on_unhealthy = restart` saves, deploys, and the supervisor restarts it after it stays `unhealthy` for `unhealthy_after_sec`.
- An entry on an image with no HEALTHCHECK and no `spec.healthcheck` and `on_unhealthy = restart` fails its deploy with the reason in §3 and keeps the previous container (default `on_deploy_failure = rollback`).
- A host on bot-agent ≤ v0.2.19 gets `409 unsupported_agent` for such a deploy.

## 8. Notes from implementation (rev 1, amendment 1a)

- The flag is also kept off every Host answer, like `programs.host_network` (v1.50); the 409 itself tells a deployer whether the agent has it.
- Out of scope, noted for a follow-up: readiness (`check_readiness`) does not report this gate yet; an automatic rollback of an entry with a host env file can bring back `-prev` with env from before a file rotation (already true for a pinned healthcheck before this change).
