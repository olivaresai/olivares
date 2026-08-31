-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: Apache-2.0
--
-- Least-privilege read-only role for the pgcontent connector (the documented,
-- differentiating posture: the connector's role has SELECT and NOTHING else).
-- Used by testdata/docker-compose.e2e.yml to provision the read-only role the
-- wire-proof E2E connects as (PGCONTENT_E2E_RO_DSN).
--
-- This is also the reference an operator copies for production: create a dedicated
-- role, grant it USAGE on the schema and SELECT on exactly the tables the connector
-- ingests, and NEVER grant INSERT/UPDATE/DELETE/DDL. Combined with the connector's
-- default_transaction_read_only session and its SELECT-only query builders, a write
-- is impossible on three independent layers.

CREATE ROLE olivares_ro LOGIN PASSWORD 'ro_password';

-- Read access to the schema and its current + future tables. Do NOT grant any
-- write privilege; the connector never needs one.
GRANT USAGE ON SCHEMA public TO olivares_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO olivares_ro;

-- Belt-and-braces: pin the role itself to read-only transactions by default, so even
-- an accidental write attempt on this role fails before it reaches a table.
ALTER ROLE olivares_ro SET default_transaction_read_only = on;
