# Spec: why a deploy failed (deploy reason and log tail)

Rev 1 (2026-11-23). Joe asked that an agent deploying over MCP learns why a deploy failed instead of deploying blind, and chose to include the agent's exact reason (option "control plane and agent", 2026-11-23). api-v1 **v1.47**, migration **0031**, agent-protocol **v1.19** (bot-agent).

## 1. Problem

A deploy row kept only `outcome`, `digest`, `exit_code` and times. The agent sent the last 50 redacted log lines and the final container status with every deploy result; the confirm answer showed them once, and the API dropped them. A confirm that ran past the MCP client's 50 s ("still running on the host") never showed them at all. A deploy the host refused (offline, policy, unsupported) deleted its row, so history did not show it. `entry_logs` reads live logs and fails when the host is offline; a rolled-back container's logs are not the running one's.

## 2. API (api-v1 v1.47)

1. **Stored with each finished deploy or rollback** (migration 0031, columns on `app.image_deploys`): `reason` (text ≤ 1024 bytes), `final_status` (`{"state","exit_code","restart_count"}` from the agent's status; null when the state is unknown), `log_tail` (the agent's redacted lines, the newest ≤ 50 lines and ≤ 16 KiB, after the API's own exact-value redaction).
2. **`ImageDeploy`** gains `"reason": string|null` and `"final_status": {...}|null`. `reason` is null on `succeeded`, `unchanged` and a running row. A row without a stored reason (older rows, agent auto actions, a stale row closed as `timeout`) answers a reason worked out from its outcome. Both appear in the deploy and rollback answers and in `GET …/deploys`. The list never carries lines.
3. **Reason rules.** When the agent sent a `reason` (agent-protocol v1.19; one printable line ≤ 512 bytes, exact-value redacted) it is the cause; otherwise the API works one out: a job's non-zero `exit_code` → "the job exited with code N"; else from `final_status`: restarting or `restart_count > 0` → "the container kept restarting (N restarts)", exited or dead → "the container exited with code N" / "the container stopped", missing or created → "the container did not start", running → "the container did not pass its health checks in time". `rolled_back` appends "; rolled back; the previous version is running again" and `rollback_failed` "; the rollback failed too; the service may be down". Other outcomes have fixed sentences (`image_unavailable`, `forbidden_spec`, `conflict`, `timeout`).
4. **Outcome `refused`** (new, responses and storage): when the host refuses the task (`unavailable`, `forbidden`, `bad_request`, `not_found` from the agent session), the in-flight row is finished as `refused` with no digest and `reason` = "<phrase>: <message>" (the fixed phrase when the message held a secret), instead of being deleted. The request still answers the same envelope error. `refused` does not count for `last_deployed_at`, rollback targets or deploy problems. A frame too large to send is still not a deploy (row deleted). A host that drops while a sent task is waiting (the socket closes, the agent reconnects, the API shuts down) did not refuse it and the deploy may have run, so that row is deleted as before, not kept as `refused`. An automatic agent action never copies the spec hash of a `refused` row. The single-deploy route shows a running row past its deadline as `timeout`, like the list.
5. **`GET /v1/projects/{id}/catalog/{name}/deploys/{deploy_id}?host_id=`** (console or delegated; same permission as the list: any catalog action on the entry) → `{"deploy": ImageDeploy, "lines": [string]|null}`. `lines` is the stored tail (`[]` when none), re-redacted with the entry's current secret forms; null for a caller who holds neither `logs` nor `deploy` on the entry. A running row has outcome `in_flight` and `ends_by`. Unknown id, or one of another entry or host → `not_found`; a malformed id → `bad_request`.
6. Rollback note: migration 0031's down script deletes `refused` rows and drops the three columns; roll the API back to v1.46 first.

## 3. Agent (agent-protocol v1.19, bot-agent)

`image_deploy` and `image_rollback` results gain an optional `reason` (one line, ≤ 512 bytes, only when the outcome is not a success). It is built only from fixed words, refusal codes, container states and the failing health check's protocol, port, path and HTTP status, never from helper, Docker, registry or container text. Examples: "health check GET :8080/health answered 503 (expected 200-299)", "health check tcp :5432: connection refused (nothing listens on that port)", "the container kept restarting (3 restarts, last exit code 1)", "the Docker health check reported unhealthy", "the image could not be pulled: the tag is missing or the registry refused the pull". An older API ignores the key.

## 4. MCP

1. **`deploy_history`** (read; `project_id`, `entry`, `host_id`, optional `deploy_id`, optional `limit` 1–20, default 10): the newest rows with `id`, outcome, digest, times, via, `reason`, `final_status`; and `detail`, the stored log tail of `deploy_id` or of the newest finished row (`lines` null without the logs or deploy permission). The text lists one line per row ("rolled_back sha256:efef… <time> via mcp container running: <reason> [<id>]") and the last 20 tail lines. Reads stored data and works while the host is offline.
2. **Deploy and rollback results** (`confirm`) keep `deploy.id` and `deploy.reason`; the text adds `reason: …` and, when the outcome is not `succeeded` or `unchanged`, "Run deploy_history for this entry and host to read why it failed and the stored log tail."
3. "still running on the host" now points to `deploy_history`. `deploy_entry`'s attached history carries `reason` for rows that did not succeed and shows at most the newest `refused` row, which does not take one of the three rows kept for rollback choices, and its description says to read `deploy_history` before deploying again. `list_problems` names `deploy_history` as the fix for a deploy problem.

## 5. Console

Accepts `refused` (badge in the revoked tone) and shows `reason` under a deploy result and under each history row. An older console drops a `refused` row as unreadable.

## 6. Out of scope

- The registry's own pull error text: the root helper only returns fixed refusal codes by design, so `image_unavailable` stays one sentence.
- Program (systemd) runs; reasons for agent auto actions beyond the outcome sentence.
