# Regenerate a secret env value (api-v1 v1.52)

Rev 1, 2026-11-23. DD `DEF-20261123-no-way-to-regenerate-or-reveal-again-a-g-ltzh`; follows the privileged-steps principle (`DEF-20261123-design-principle-privileged-steps-are-do-exut`, api-v1 v1.51 deep links). Additive, no migration, no agent-protocol change.

## Problem

A generated secret is shown once (v1.27 item 3). If that view is missed there is no way to get a new generated value: generate refuses an existing key (v1.27 rev 3), MCP cannot write secret keys, and the workaround (a new key name, repoint `secret_env`, redeploy) leaves an orphaned key.

## API

1. `POST /v1/projects/{id}/env-sets/{set}/keys/{key}/regenerate` and `POST /v1/projects/{id}/catalog/{name}/private-keys/{key}/regenerate` (the entry's private set `@<name>`). Body `{"generate": <v1.27 item 1 object>}`; no body or no `generate` → `bad_request` `send generate`; the v1.27 generate messages otherwise. Project owner or admin. **Console sessions only** (`delegatedDeny`).
2. The key must exist (`not_found`) and be secret (`bad_request` `{KEY} is not secret; edit it to change it`). Any secret key qualifies, generated or typed (rotate), including a `needs_value` placeholder, which it fills. A new value from the CSPRNG is sealed and stored with `generated` and `reveal_pending` true, in one transaction with the set re-hash, as a key PUT.
3. The value is not returned. `200 {"key","is_secret":true,"preview":"••••","reveal_pending":true,"used_by":[entry names]}`; `used_by` is the set's consumers (v1.33 item 4), empty when the read fails. The v1.27 reveal route then shows the value once.
4. Audit `env_set.key_regenerate` with `set`, `key`, `kind` and `used_by`; never a value.
5. Running copies keep the old value until their next deploy or rollback; nothing is sent to an agent.

Store: `EnvKeyPut.Regenerate` makes `PutEnvKey` accept an existing secret key only (missing set or key `ErrNotFound`, nothing created; plain `ErrSecretFixed`) and a private set name.

## Console

- Env sets rows and an entry's private keys ("Secrets for this entry") get a **Regenerate** row action (`RotateCcw`) on every secret key with a value, for owners and admins.
- The Regenerate dialog: a kind select (Password 32 by default), the text "Make a new value for KEY? The current value is replaced now and cannot be shown again. web and api keep the old value until redeployed.", Regenerate and Cancel. On Regenerate the `regenerateEnvKey` / `regeneratePrivateKey` action calls regenerate then reveal; the value is shown once in the same dialog with Copy and "Redeploy web and api to use the new value." The value lives only in the dialog, as the reveal dialog. A lost reveal answer leaves the key pending, so the row offers Reveal once.
- Deep link: `/projects/<id>?tab=settings&env.set=<set>&env.key=<KEY>&env.do=reveal|regenerate#env-sets` opens that set and focuses the row's Reveal once or Regenerate button. It never opens the dialog or acts.

## MCP

- No regenerate tool. `list_env_sets` gains `console_url` on each secret key with a value: the Reveal once link while `reveal_pending`, else the Regenerate link; a private set's key links to its entry (`?tab=catalog&catalog.q=<entry>`). Empty without `BOT_MCP_CONSOLE_ORIGIN`. The summary counts values not shown yet.
