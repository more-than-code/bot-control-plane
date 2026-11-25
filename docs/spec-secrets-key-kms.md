# The secrets encryption key in AWS KMS, with disaster recovery

Status: rev 1, **draft, waiting for Joe's approval** (Tier 3: how the server
starts, how stored secrets are protected, a migration). api-v1 **v1.61**
(config surface, like v1.50 item 2 and v1.58), migration **0041**. No
agent-protocol change, no console change, no MCP change.

Source: the computerless plan (`plans/plan-computerless.md` in the project
folder, sections 2, 3 (S1), 5 and 8). Joe, 2026-11-25: "set up for the signing
key, encrypt key, and the SSH key first". This spec is the encrypt key only.
AWS conventions follow bot-agent PR #15 §13 (KMS signing key): one key per
purpose, an alias, least-privilege IAM with conditions, every step doable in a
phone browser, sessions never hold AWS credentials.

## 1. The problem, in Joe's words

> "If deploy bot server is crapped, the decrypt will be not working, right?
> Since the key is gone." "If deploy bot cannot decrypt, then the new
> deployment won't be working since the secrets stored cannot be decrypted."

Both are right today. The secrets key (S1) is 32 random bytes in
`BOT_SECRETS_KEY_FILE`, a 0600 file on the Deploybot server host (or
`BOT_SECRETS_KEY` in containers). `seal.Box` uses it (AES-256-GCM,
`key_version` 1) for env-set secrets, secret files, entry-private secrets,
registry tokens, GitHub tokens and host secret files in Postgres.

- **Server lost, file lost:** a database backup restores everything except
  those sealed values. They are unrecoverable. Every deploy of an entry that
  uses a secret env, a secret file or a private registry is refused
  (`500 internal error`, `decrypt failed` in the log) until each person
  re-enters every secret and reconnects every registry and GitHub login.
- **File copied with a backup:** the opposite risk. Anyone with a host backup
  and a database dump reads every user's secrets.
- **Wrong key at start** (for example a restore with a different file): the
  server starts and fails only at the first deploy that decrypts. Nothing
  checks the key against the database.

## 2. Goal

1. The plain key is no longer a file on the host. The host keeps only the key
   **wrapped** (encrypted) by an AWS KMS key; the server unwraps it once at
   start and keeps it in memory.
2. A new server can be rebuilt from a database backup plus KMS access, with no
   old host and no file from it.
3. A running server never needs KMS, so a KMS outage cannot block deploys
   (Joe's second point).
4. A break-glass copy of the plain key in a password manager covers loss of
   the AWS account or the KMS key.
5. The server refuses to start with a key that does not match the database.
6. Nothing stored is re-encrypted: the 32 bytes stay the same.
7. Self-hosters keep `BOT_SECRETS_KEY_FILE` and `BOT_SECRETS_KEY` unchanged.

Out of scope: server config secrets in Secrets Manager (S2–S5), backup
encryption (S15), the SSH key, rotating the data key itself. Each is its own
spec.

## 3. Design

### 3.1 Envelope

- **KMS key:** symmetric, `SYMMETRIC_DEFAULT`, Encrypt and decrypt, single
  region, alias `deploybot-secrets-key`. Automatic yearly rotation on (KMS
  keeps old versions; old wrapped blobs still decrypt). Deletion waiting
  period 30 days, the maximum.
- **Wrapped key:** `kms:Encrypt` of the existing 32 bytes with encryption
  context `{"app": "deploybot", "purpose": "secrets-key"}`, stored as the
  standard base64 of the `CiphertextBlob` (about 250 characters). It is not a
  secret on its own: it is useless without `kms:Decrypt` on this key with this
  context.
- **Start:** one `kms:Decrypt` with the explicit `KeyId` (so a blob for another
  key is refused) and the same context. The result must be exactly 32 bytes.
  The key then goes to `seal.New` as today. The plain key is never written to
  disk or logged.

### 3.2 Configuration (api-v1 v1.61 item 1)

Two new variables, set together:

| Variable | Value |
|---|---|
| `BOT_SECRETS_KEY_KMS` | the KMS key ARN (`arn:aws:kms:<region>:<account>:key/<id>`); the region comes from the ARN |
| `BOT_SECRETS_KEY_WRAPPED` | the base64 wrapped key |

Rules, checked by `config.FromEnv`:

- At most one source of the secrets key: `BOT_SECRETS_KEY`,
  `BOT_SECRETS_KEY_FILE`, or the KMS pair. Two sources is a start-up error
  naming them (as v1.50 item 2).
- One of the pair without the other, an ARN that is not a KMS key ARN, or a
  wrapped value that is not base64 of 1–6144 bytes is a start-up error naming
  the variable.
- None of the three: the key is not configured, as today (secret writes and
  deploys that need it answer `unavailable`).

AWS credentials come from the AWS SDK's default chain, so the same binary
works with an access key now and IAM Roles Anywhere later (§4) with no code
change. On the Deploybot host the recommended form is
`AWS_SHARED_CREDENTIALS_FILE` pointing to a root-owned 0600 file readable by
the server's user, written through the host files flow (api-v1 v1.44) as a
secret file. `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` in the unit's env
file also work.

### 3.3 Start-up when KMS is slow or down

- Up to 5 attempts, each with a 10 s timeout, 2, 4, 8 and 16 s apart (about
  80 s in all). An `AccessDenied`, `InvalidCiphertext`, `IncorrectKey`,
  disabled-key or pending-deletion answer is not retried: it will not fix
  itself.
- Still no key: the process **exits non-zero** with one line naming the cause
  class only (`secrets key: KMS unreachable`, `secrets key: KMS refused
  (access denied)`, `secrets key: wrapped key does not match the KMS key`).
  No ARN, blob or AWS request id is logged; the AWS error text is not echoed.
  systemd (`Restart=on-failure`) or Docker (`restart: unless-stopped`) keeps
  retrying.
- Why fail closed instead of starting without the key: starting without it
  looks healthy while every secret deploy fails, and a later background
  unwrap would make `Secrets` change under running requests. A failed start is
  visible (the console is down, the host-offline problem fires) and the
  break-glass path (§6.3) gets the server up in minutes.

### 3.4 Key check (api-v1 v1.61 item 2, migration 0041)

New one-row table `app.secrets_key`:

| Column | |
|---|---|
| `id` | `smallint` primary key, check `id = 1` |
| `key_version` | `smallint`, 1 |
| `check_value` | `text`, 64 lowercase hex: HMAC-SHA256 keyed by HKDF-SHA256(key, no salt, info `deploybot secrets key check v1`) over the bytes `deploybot` |
| `kms_key_arn` | `text` null |
| `wrapped_key` | `text` null, the base64 wrapped key |
| `recorded_at` | `timestamptz` |

At each start with a key configured, after the database opens:

- **No row:** insert it (the first start after this release adopts whatever
  key the server has, so a running production server keeps working).
- **Row with the same check value:** update `kms_key_arn`, `wrapped_key` and
  `recorded_at` when they changed (a switch from file to KMS, or a re-wrap
  under a new KMS key). A file-configured server leaves them as they were.
- **Row with a different check value:** refuse to start with
  `secrets key does not match this database (BOT_SECRETS_KEY*)`. This catches
  a restore with the wrong key before the first deploy fails.

With no key configured the row is neither read nor written.

The check value reveals nothing usable about the key (a keyed hash under a
derived key, as the v1.14 env hash). Because every database dump now carries
the wrapped key and the KMS ARN, **a backup alone is enough to recover**, given
`kms:Decrypt` on that key (§6). An attacker with only the dump gets a blob
that needs this AWS account.

Grants: `deploybot_api` gets `SELECT, INSERT` and `UPDATE (kms_key_arn,
wrapped_key, recorded_at)`. A test runs the store code as `deploybot_api`
(the AGENTS.md rule; `ON CONFLICT DO UPDATE` needs the UPDATE grant). The
down migration drops the table; nothing else depends on it.

### 3.5 Two server subcommands

Both run on the host as root or the server user, read the same variables, and
print no key.

- `deploybot-server secrets-key wrap --kms-key <arn>`: reads the current key
  from `BOT_SECRETS_KEY_FILE` or `BOT_SECRETS_KEY`, calls `kms:Encrypt` with the
  context, unwraps the result again to prove it round-trips, and prints only
  the two lines to paste: `BOT_SECRETS_KEY_KMS=…` and
  `BOT_SECRETS_KEY_WRAPPED=…`. Used once for the migration (§5).
- `deploybot-server secrets-key check [--database-url <url>]`: unwraps (or
  reads) the key, compares it with `app.secrets_key` and opens one sealed row
  of each kind present (env value, file, registry token) with the real
  associated data. Prints `ok` and counts, or which step failed. Exit 0 or 1.
  Used before a switch, for the restore drill (§6.4) and in runbooks.

### 3.6 Dependency

`github.com/aws/aws-sdk-go-v2/config` and `.../service/kms`, pinned to the
current release, from the Go module proxy. They bring `smithy-go` and the
SDK's own internal modules, all from the `aws` GitHub organization. A
hand-written SigV4 client was considered and rejected: it is security code
we would own, and it would not get the credential chain (Roles Anywhere,
`credential_process`) for free. Only the `api` binary path imports it; `mcp`
does not.

## 4. How the Deploybot host authenticates to AWS

**Phase 1 (this spec):** one IAM user, `deploybot-server-<host>`, no console
password, one access key. Its only policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "kms:Decrypt",
    "Resource": "arn:aws:kms:REGION:ACCOUNT_ID:key/KEY_ID",
    "Condition": {
      "StringEquals": {
        "kms:EncryptionContext:app": "deploybot",
        "kms:EncryptionContext:purpose": "secrets-key"
      },
      "IpAddress": {"aws:SourceIp": "HOST_PUBLIC_IP/32"}
    }
  }]
}
```

- The `aws:SourceIp` condition is used only if the host's public address is
  stable. A home connection with a changing address leaves it out (decision
  D3); otherwise an address change would stop the next start.
- What this buys, honestly: root on the running host can still read the key
  from memory or use the access key; that does not change. What changes: a
  disk image, a host backup or a database dump no longer contains the plain
  key; the access key alone cannot encrypt, list or change anything; every
  `Decrypt` shows in CloudTrail event history; and Joe can cut access from
  the phone (deactivate the access key, or disable the KMS key).
- `kms:Encrypt` for the one-off `wrap` (§5) is a second, temporary statement,
  removed right after.

**Phase 2 (later, own spec):** IAM Roles Anywhere with the certificate's
private key in the host's TPM where it has one, through `credential_process`.
No code change in the server (default credential chain).

Cloud sessions and threads never get credentials for this account (PR #15
§13, A1).

## 5. Migrating the existing key (no re-encryption)

The 32 bytes do not change, so no stored row changes and there is nothing to
roll back in the data.

1. Joe (phone): create the KMS key and the IAM user (§7 steps 1–3).
2. Joe or the Ops session: put the access key on the host as a secret host
   file (host files flow) and point `AWS_SHARED_CREDENTIALS_FILE` at it.
3. **Break-glass copy first.** Joe copies the current key (the file's one line)
   into the password manager as a secure note "Deploybot secrets key (plain,
   break-glass)". This is the only time the plain key is read by a person. It
   is read over Joe's own SSH session; no Claude session reads or prints it.
4. On the host: `deploybot-server secrets-key wrap --kms-key <arn>` (the IAM
   user has the temporary Encrypt statement). It prints the two lines.
5. Joe removes the temporary Encrypt statement (§7 step 5).
6. Before touching the server's env, run the check with only the KMS pair
   (the server keeps running on the file meanwhile):
   `env -u BOT_SECRETS_KEY_FILE BOT_SECRETS_KEY_KMS=… BOT_SECRETS_KEY_WRAPPED=… deploybot-server secrets-key check`.
   The key file stays on disk.
7. In the server's env, replace `BOT_SECRETS_KEY_FILE` with the two lines
   (two sources is a start-up error), restart the server. The start
   log says `secrets key: KMS` and the check row is updated with the ARN and
   wrapped key.
8. After a week with no problem: shred the key file on the host
   (`shred -u`), and check host backups made before that date: they still
   hold the plain key and should be deleted or kept as securely as the
   break-glass copy.

The same wrapped value also goes into the password manager next to the plain
copy (it is not secret, but it is the fastest way to rebuild, §6.2).

## 6. Disaster recovery (core)

### 6.1 What is needed to read the sealed secrets

Any **one** of:

- (A) a database backup (it carries the wrapped key and the ARN in
  `app.secrets_key`) **plus** `kms:Decrypt` on the key, or
- (B) a database backup **plus** the break-glass plain key from the password
  manager.

Neither needs the old host.

### 6.2 Rebuild after the server is lost

1. Restore the newest database backup into the new Postgres
   (`--migrate` brings it to the current schema).
2. Read `kms_key_arn` and `wrapped_key` from `app.secrets_key` in the restored
   database (or take them from the password manager).
3. Give the new host AWS credentials: in IAM, create a new access key for
   `deploybot-server-<host>` (or a new user for the new host), deactivate the
   old one, update the `aws:SourceIp` condition if used.
4. Set `BOT_SECRETS_KEY_KMS` and `BOT_SECRETS_KEY_WRAPPED`, run
   `deploybot-server secrets-key check`, start the server. The key check
   (§3.4) confirms it is the right key before anything is deployed.

If AWS is unavailable (account locked, key deleted): use path (B), with the
plain key as `BOT_SECRETS_KEY_FILE`; the key check confirms it; wrap it again
under a new KMS key when AWS is back.

Lost for good only if **both** the KMS key (or the AWS account) **and** the
password manager copy are gone. That is the residual risk, and why the
password manager account needs its own recovery codes kept apart (plan
section 6).

### 6.3 Break-glass

The password manager holds: the plain key (base64 line), the wrapped value
and the ARN, and the date of the last check. Bitwarden Premium is the
recommendation (decision D1; Joe has none yet). Until a password manager
exists, migration step 3 waits: switching to KMS without a second copy would
make the AWS account the single point of failure.

### 6.4 Tested restore

A recovery that was never tried is a guess. The drill, every 3 months and
after any change to the key or the IAM setup:

1. Restore the newest backup into a scratch database.
2. Run `deploybot-server secrets-key check --database-url <scratch>` with the
   KMS pair and a credential allowed to decrypt.
3. Run it again with the break-glass key as `BOT_SECRETS_KEY` instead.
4. Both print `ok`; record the date in the password manager note.

Who runs it: until backups move to the automated pipeline (plan section 8,
S15, its own spec), Joe or the Ops session on a machine that has the
credentials. When that pipeline lands, a GitHub Actions workflow in a
protected environment (Joe's approval tap from the GitHub app) runs steps 1
and 2 through OIDC with a role that may only `kms:Decrypt` with this context,
as PR #15 does for signing. Step 3 stays a person's step, since the plain key
never goes into CI. A cloud session never runs it with real credentials.

## 7. AWS console steps for Joe, from a phone browser

Use the same region as the bot-agent signing key.

1. **KMS → Customer managed keys → Create key:** Symmetric, Encrypt and
   decrypt, single region. Alias `deploybot-secrets-key`. Key administrators:
   yourself only. Key users: none (the IAM user gets rights from its own
   policy). After creation: Key rotation → automatic, yearly. Copy the key
   ARN.
2. **IAM → Users → Create user** `deploybot-server-<host>`, no console access.
   Add the inline policy from §4 (replace `REGION`, `ACCOUNT_ID`, `KEY_ID`,
   and `HOST_PUBLIC_IP` or delete the `IpAddress` block).
3. **For the migration only**, add a second inline policy
   `deploybot-wrap-once`:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [{
       "Effect": "Allow",
       "Action": "kms:Encrypt",
       "Resource": "arn:aws:kms:REGION:ACCOUNT_ID:key/KEY_ID",
       "Condition": {"StringEquals": {
         "kms:EncryptionContext:app": "deploybot",
         "kms:EncryptionContext:purpose": "secrets-key"
       }}
     }]
   }
   ```

   Then **Security credentials → Create access key** (type "Application
   running outside AWS"). Paste the two values straight into the host file in
   the Deploybot console (Config files, secret), not into a chat.
4. In the password manager: the break-glass note (§6.3).
5. After the `wrap` step: delete `deploybot-wrap-once`.
6. Optional: **CloudTrail → Event history**, filter Event source
   `kms.amazonaws.com`, to see each server start's `Decrypt`.

Emergency switches, also from the phone: IAM user → deactivate access key
(the next start fails; a running server keeps working); KMS key → Disable
(same); never "Schedule deletion" unless the break-glass copy is checked.

## 8. Failure modes and what happens to deploys

The server unwraps once at start and keeps the key in memory. After that it
makes no KMS call, so **a KMS outage or revoked credentials never affect a
running server.**

| Failure | Running server | Next start | Deploys | Recovery |
|---|---|---|---|---|
| KMS outage, server already running | unaffected | — | all work | none needed |
| KMS outage at start | — | retries ~80 s, then exits; systemd keeps retrying | none until it starts (console down too) | wait, or break-glass: plain key as `BOT_SECRETS_KEY_FILE` via host files, restart; remove it again after |
| IAM access key revoked or deactivated | unaffected | exits: `KMS refused (access denied)`, no retry | none after a restart | new access key (§7 step 3, without the wrap policy) |
| KMS key disabled | unaffected | exits as above | none after a restart | re-enable the key |
| KMS key deleted, or AWS account lost | unaffected while it runs | exits | none after a restart | break-glass plain key; wrap under a new KMS key later |
| Server and host lost | — | — | none until rebuilt | §6.2 from the newest backup |
| Wrong key at start (wrong file, wrong blob) | — | exits: key check mismatch | none | the right key; nothing in the database was touched |
| Key lost and no break-glass copy | — | — | secret deploys refused for good | people re-enter secrets and logins (today's only outcome, avoided by §6.3) |

"Deploys" means deploys of entries that need the key (secret env, secret
files, private registry or GitHub login). Deploys that need no secret, runs,
stops, metrics and the console keep their current behaviour.

## 9. Rollback

- **Config rollback** (any time): set `BOT_SECRETS_KEY_FILE` back (the kept
  file, or the break-glass copy written through host files), remove the two
  KMS variables, restart. Same key, so every row opens. The check row keeps
  the old ARN and wrapped key, which is harmless.
- **Binary rollback** to a release before v1.61: first do the config rollback.
  An older server ignores the KMS variables and would start *without* a key
  (`unavailable` on secret deploys), not fail.
- **Migration rollback:** `0041_secrets_key.down.sql` drops `app.secrets_key`.
  Older servers never read it.
- **AWS rollback:** disable, then schedule deletion of the KMS key (30 days)
  and delete the IAM user, only after the config rollback and with the
  break-glass copy checked.

## 10. Contract diff (api-v1 v1.61)

Before: the secrets key is `BOT_SECRETS_KEY_FILE` or `BOT_SECRETS_KEY`
(v1.50 item 2); a wrong key is found at the first decrypt.

After (additive):

1. `BOT_SECRETS_KEY_KMS` and `BOT_SECRETS_KEY_WRAPPED`, a third source of the
   secrets key; at most one source; start-up behaviour and messages as §3.2
   and §3.3.
2. The key check (§3.4): a server with a key records it in
   `app.secrets_key` (migration 0041) and refuses to start with a key that
   does not match.
3. `deploybot-server secrets-key wrap|check` (§3.5).

No HTTP route, payload, MCP tool or agent frame changes. Consumers: the
server's own configuration (Ops, `docs/containers.md`, `api/README.md`,
`api/docs/design.md` Config table), updated in the code PR.

## 11. Acceptance criteria and tests

Unit (`api/internal/config`, a new `api/internal/secretskey` package):

1. Two sources set → start-up error naming them; half a KMS pair, a bad ARN,
   bad base64 → error naming the variable; no value, ARN or blob in any error.
2. Unwrap through a fake KMS client: success gives the 32 bytes; a 31- or
   33-byte plaintext is refused; the request carries the explicit `KeyId` and
   the encryption context.
3. Retry: a timeout is retried up to 5 times with the stated waits (fake
   clock); `AccessDenied`, `InvalidCiphertext`, `IncorrectKey`, disabled and
   pending-deletion are not retried; the final error is the fixed class line.
4. Check value: a fixed key gives a fixed vector, written out by hand as in
   `TestEnvHash`.
5. `wrap` round-trips through the fake and prints only the two lines; `check`
   reports a mismatch and a sealed row that does not open.

Database (`api/internal/db`, with the test database):

6. First start inserts the row; same key updates only the KMS columns; a
   different key is refused and changes nothing; no key touches nothing.
7. The store code runs as `deploybot_api` (grants).
8. Migration 0041 up, down, up; the down-and-forget helpers updated if the
   table is touched by a later migration (none now).

Not tested here: a real AWS call. The code PR's verification is a manual
`secrets-key check` against the real key by Joe or Ops after deploy.

## 12. Decisions for Joe

| # | Decision | Recommendation |
|---|---|---|
| D1 | Password manager for the break-glass copy | Bitwarden Premium; the migration's step 3 waits for it |
| D2 | Start-up when KMS is down: fail closed (exit, retry) or start without the key | Fail closed (§3.3) |
| D3 | Pin the IAM user to the host's public IP | Only if the address is fixed; Joe to say |
| D4 | Record the wrapped key and key check in the database (migration 0041) | Yes: it is what makes a backup alone recoverable, and catches a wrong key at start |
| D5 | Restore drill cadence | Every 3 months and after each key change; automated with the backup pipeline later |
