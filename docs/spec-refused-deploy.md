# A refused deploy says which field failed, and keeps its plan

Status: rev 1, as implemented (2026-11-23). api-v1 v1.55 with agent-protocol
v1.23 (bot-agent). No migration. Covers DD items
DEF-20261123-agent-bad-spec-refusals-don-t-name-the-f-yars and
DEF-20261123-a-refused-deploy-confirm-uses-up-the-pla-fsm5. Joe said go ahead
on both on 2026-11-23.

## 1. agent-protocol v1.23: `task_reject` names the field

- `task_reject` gains an optional `"field": string`, sent only with
  `error_code` `bad_spec` on an image task. It is a key name, never a value:
  a task key (`project`, `entry`, `spec`, `env`, `registry_auth`,
  `spec_hash`, `digest`, `files`, `secret_keys`, `env_hash`, `lines`,
  `service`) or `spec.<key>` for an ImageSpec rule (`spec.secret_env`,
  `spec.health_policy.on_unhealthy`, `spec.healthcheck.timeout_sec`).
- Shape `^[a-z][a-z0-9_]{0,31}(\.[a-z][a-z0-9_]{0,31}){0,2}$`, at most 64
  bytes. The agent builds it from key names it defines; an unknown key, a map
  key (an env key name) or a body that is not JSON gives no field.
- No hello gate: the API reads agent frames by field and has always ignored
  unknown keys, so an older API keeps its fixed sentence.

## 2. api-v1 v1.55

1. **The field in the answer.** The API keeps the field only when it has the
   shape above, and only with `bad_spec`. A deploy or rollback refused with a
   field answers `400 bad_request` with
   `the host's agent refused the task as malformed (bad_spec): field "<field>" did not pass the host's checks (the agent names the field, never its value).`
   then a fixed hint for `spec.secret_env`, `env`, `registry_auth`, `files`,
   `spec.files` and `spec.volumes`, then
   `Check that the agent is up to date (upgrade_agent), then compare that field with the ImageSpec rules`.
   The field is quoted so key=value redaction leaves it alone. Without a field
   the v1.47 sentence stays. The refused history row's `reason` is the same
   text, so MCP `deploy_history` and the console's reason line show it too.
2. **A refused confirm keeps its plan.** When `POST /v1/confirmations/{id}`
   performs an image deploy or rollback and the host refuses it (a
   `task_reject`, or the host unavailable before the frame was written; not
   when the frame may have reached the host), the confirmation's `used_at`
   is cleared and its `expires_at` becomes at least now + 5 minutes
   (`ConfirmationTTL`). The answer adds
   `. Nothing ran on the host, so this plan is kept: after fixing the cause outside the entry (for example upgrade_agent, the host's runner.toml or an env set), confirm it again with the same confirmation_id and idempotency_key until HH:MM UTC. A change to the entry itself needs a new plan`.
   This holds even when the plan's own 5 minutes ran out while the host was
   asked. It happens only when the refused row was written; if that write
   failed, the row stays in flight for the sweep and the plan stays used.
   Failures before the frame is built (a running operation, a bad `${KEY}`
   reference) still use up the plan, as before.
   The idempotency key, owner and plan checks are unchanged; a changed entry
   still answers the v1.42 "Plan again" conflict. Console actions have no
   plan and get no such sentence. The stored reason and the audit row do not
   carry it.
3. **MCP:** `confirm`'s description says a host-refused deploy or rollback
   keeps the plan. Messages pass through unchanged.
