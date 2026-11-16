-- Hand-run rollback for 0023_domains_edge_routes.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0023 fully applied.
-- It refuses while any route or domain exists: the first two statements
-- add a CHECK (false) that fails on a table with rows, and nothing changes.
-- Remove the routes (entry routes or domain delete) and the domains first,
-- after rolling the API back to v1.34 (spec-domains-edge-routes 8). Edges
-- on hosts are not stopped by this script: disable them first. Take a dump
-- first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0023 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0023_domains_edge_routes.sql';

BEGIN;

ALTER TABLE app.routes ADD CONSTRAINT routes_none_chk CHECK (false);

ALTER TABLE app.org_domains ADD CONSTRAINT org_domains_none_chk CHECK (false);

DROP TABLE IF EXISTS app.edge_confirmations;

DROP TABLE IF EXISTS app.host_edges;

DROP TABLE IF EXISTS app.routes;

DROP TABLE IF EXISTS app.domain_assignments;

DROP TABLE IF EXISTS app.org_domains;

ALTER TABLE app.orgs DROP CONSTRAINT IF EXISTS orgs_acme_email_chk;

ALTER TABLE app.orgs DROP COLUMN IF EXISTS acme_email;

COMMIT;
