-- Hand-run rollback for 0028_registry_github_refresh.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0028 fully applied.
-- It never refuses. Registry rows stay with their access token. Only the
-- sealed refresh tokens and the expiry times are dropped, so Connect GitHub
-- logins expire after 8 hours again (the behaviour before v1.43) until the
-- project reconnects. Roll the API back to v1.42 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0028 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0028_registry_github_refresh.sql';

BEGIN;

ALTER TABLE app.registries DROP CONSTRAINT IF EXISTS registries_refresh_pair_chk;
ALTER TABLE app.registries DROP COLUMN IF EXISTS refresh_expires_at;
ALTER TABLE app.registries DROP COLUMN IF EXISTS access_expires_at;
ALTER TABLE app.registries DROP COLUMN IF EXISTS refresh_ciphertext;
ALTER TABLE app.registries DROP COLUMN IF EXISTS refresh_nonce;

COMMIT;
