-- Connect GitHub logins are refreshed (api-v1 v1.43, spec
-- docs/spec-github-token-refresh.md rev 1). Applied only by `bot-api-server
-- --migrate` (SET ROLE deploybot_owner). The matching rollback is
-- 0028_registry_github_refresh.down.sql. --migrate does not apply
-- *.down.sql.
--
-- GitHub OAuth Apps issue user tokens that expire after 8 hours, with a
-- refresh token that expires after 6 months without use. A github-source
-- row that got a refresh token keeps it sealed in refresh_nonce and
-- refresh_ciphertext (same key as the access token, AAD
-- registry-refresh|project|host, distinct from the access token AAD), and
-- both expiry times. access_expires_at is when the stored access token
-- expires, refresh_expires_at when the refresh token does. All four are
-- null for a manual row and for a long-lived GitHub token. After GitHub
-- rejects a refresh the refresh columns are null again and
-- access_expires_at stays. A refresh rewrites nonce, ciphertext and
-- key_version (already granted) and these four columns, never updated_at.
-- The API never returns any of them.
--
-- deploybot_api gains UPDATE on the four new columns, next to its other
-- column grants. Existing rows keep nulls: they expire as before until the
-- project reconnects GitHub once.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.registries
    ADD COLUMN refresh_nonce bytea NULL,
    ADD COLUMN refresh_ciphertext bytea NULL,
    ADD COLUMN access_expires_at timestamptz NULL,
    ADD COLUMN refresh_expires_at timestamptz NULL,
    ADD CONSTRAINT registries_refresh_pair_chk CHECK ((refresh_nonce IS NULL) = (refresh_ciphertext IS NULL));

GRANT UPDATE (refresh_nonce, refresh_ciphertext, access_expires_at, refresh_expires_at) ON TABLE app.registries TO deploybot_api;
