-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- Provision the Olivares control-plane application role for Postgres.
--
-- WHY THIS MATTERS (docs/SECURITY-HARDENING.md): the access-graph is the most sensitive asset
-- and a cross-tenant leak is "a customer breach". Tenant isolation on Postgres is
-- enforced by FORCE ROW LEVEL SECURITY. Postgres SILENTLY bypasses ALL row-level
-- security for a SUPERUSER or a role with BYPASSRLS. If the control plane connects
-- as such a role, the FORCE-RLS backstop is INERT and only the application-layer
-- `tenant_id = ?` predicate separates tenants. The engine therefore REFUSES to
-- start against a superuser/BYPASSRLS role unless `--allow-privileged-db-role` is
-- passed (single-tenant/dev only). Use this script to create the correct role.
--
-- Run as a Postgres superuser ONCE, before first boot:
--   psql "postgres://postgres@db:5432/postgres" -v ON_ERROR_STOP=1 \
--        -v app_password="$OLIVARES_DB_PASSWORD" -f 01-app-role.sql
--
-- Then point the control plane DSN at this role:
--   olivares serve --engine postgres \
--     --dsn "postgres://olivares_app:$OLIVARES_DB_PASSWORD@db:5432/olivares?sslmode=verify-full"
--
-- Notes:
--   * `olivares_app` is the exact role name the engine's append-only REVOKE targets
--     (dialect/postgres.go defaultAppRole). Keep this name.
--   * The role OWNS the database so it can run the (idempotent) schema migrations.
--     FORCE ROW LEVEL SECURITY applies the tenant policy EVEN TO THE TABLE OWNER,
--     so an owning-but-NOBYPASSRLS role is still fully isolated — verified in CI.
--   * Use TLS (`sslmode=verify-full`) and a strong password / SCRAM. Never reuse the
--     `postgres` superuser as the application DSN.

-- The application role: login, NOT a superuser, NOT BYPASSRLS, no createrole.
DROP ROLE IF EXISTS olivares_app;
CREATE ROLE olivares_app LOGIN PASSWORD :'app_password'
  NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;

-- The role owns the application database (so it can apply schema migrations).
-- Create the database owned by it. (Run from a maintenance DB such as `postgres`.)
SELECT 'CREATE DATABASE olivares OWNER olivares_app'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'olivares')\gexec

-- Least privilege on the public schema of the new database is configured by the
-- engine's migrations (it owns its tables). Nothing else is granted here.

-- ---------------------------------------------------------------------------
-- OPTIONAL: the cross-tenant admin role (for `--admin-dsn`).
--
-- Genuinely cross-tenant System reads (the org list, and the per-tenant audit
-- checkpoint cadence covering EVERY tenant) must see all tenants' rows. Under
-- FORCE ROW LEVEL SECURITY the `olivares_app` role (even as owner) is filtered to
-- the bound tenant, so those reads return an empty set on it. Provision this
-- dedicated role — BYPASSRLS so it bypasses the tenant policy, but NOSUPERUSER so
-- it is least-privilege (read across tenants, not alter the system) — and point
-- `--admin-dsn` at it:
--   olivares serve --engine postgres \
--     --dsn       "postgres://olivares_app:$OLIVARES_DB_PASSWORD@db/olivares?sslmode=verify-full" \
--     --admin-dsn "postgres://olivares_admin:$OLIVARES_ADMIN_PASSWORD@db/olivares?sslmode=verify-full"
--
-- The engine validates this role at boot: it MUST be able to bypass RLS, and a
-- SUPERUSER is refused (use NOSUPERUSER BYPASSRLS). Omit this role entirely for a
-- single-tenant deployment — the engine then logs that cross-tenant reads are
-- RLS-limited. Pass -v admin_password="$OLIVARES_ADMIN_PASSWORD" to enable it.
--
-- DROP ROLE IF EXISTS olivares_admin;
-- CREATE ROLE olivares_admin LOGIN PASSWORD :'admin_password'
--   NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;
-- GRANT CONNECT ON DATABASE olivares TO olivares_admin;
-- \connect olivares
-- GRANT USAGE ON SCHEMA public TO olivares_admin;
-- -- Read-only on existing and future app-owned tables (no INSERT/UPDATE/DELETE):
-- GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_admin;
-- ALTER DEFAULT PRIVILEGES FOR ROLE olivares_app IN SCHEMA public
--   GRANT SELECT ON TABLES TO olivares_admin;
