-- SPDX-FileCopyrightText: 2026 Olivares.AI
-- SPDX-License-Identifier: AGPL-3.0-only
-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
--
-- Provision the Olivares CROSS-TENANT ADMINISTRATIVE READ role (`--admin-dsn`).
--
-- WHY A FRESH POSTGRES INSTALL NEEDS THIS AT BOOTSTRAP TIME, not "optionally later":
-- `POST /v1/setup` resolves the first organization through `Store.System` +
-- `SystemScope.ListOrgs` (core/api/handlers_auth.go). Under FORCE ROW LEVEL SECURITY
-- the NOBYPASSRLS application role — even as the table owner — is filtered to the
-- bound tenant, so that read is REFUSED with `ErrEnumerationNotAuthoritative` rather
-- than reported as an empty estate (core/internal/store/sqlstore/system.go). The
-- engine then answers `501 cross_tenant_admin_pool_not_configured` and `GET /readyz`
-- answers `503 status=setup_blocked`. So a Postgres deployment without this role
-- cannot complete its FIRST SETUP at all. It is not only a first-setup dependency:
-- the org list, multi-tenant checkpoint coverage and DR backup use the same read.
--
-- THIS FILE IS THE COMPANION OF `01-app-role.sql`, NOT A REPLACEMENT. Run that one
-- first: it creates `olivares_app` (NOSUPERUSER NOBYPASSRLS) and the `olivares`
-- database it owns. This file adds a SECOND, DISTINCT principal and changes nothing
-- about the first one. The privileges below are exactly the ones the product's own
-- `olivares db init --admin-role` renders (core/internal/store/sqlstore/dbsetup.go,
-- `RenderProvisionSQL`): CONNECT, schema USAGE, SELECT on existing tables and a
-- default-privilege row so the app role's FUTURE tables are selectable too.
--
-- BY HAND — run as a Postgres superuser, from a maintenance DB such as `postgres`:
--   psql "postgres://postgres@db:5432/postgres" -v ON_ERROR_STOP=1 \
--        -v admin_password="$OLIVARES_ADMIN_PASSWORD" -f 02-admin-role.sql
--
-- Then add the second pool to the control plane's own command line:
--   olivares serve --engine postgres \
--     --dsn       "postgres://olivares_app:$OLIVARES_DB_PASSWORD@db:5432/olivares?sslmode=verify-full" \
--     --admin-dsn "postgres://olivares_admin:$OLIVARES_ADMIN_PASSWORD@db:5432/olivares?sslmode=verify-full"
--
-- EQUIVALENT ONE-LINER (the binary does the same thing and verifies it by
-- reconnecting as the role; see deploy/postgres/README.md):
--   olivares db init --superuser-dsn "postgres://postgres@db:5432/postgres" \
--     --app-role olivares_app --app-password-file /run/secrets/app_password \
--     --admin-role olivares_admin --admin-password-file /run/secrets/admin_password
--
-- WHAT THIS FILE DELIBERATELY NEVER DOES:
--   * no `DROP ROLE` — a bootstrap script must not be able to delete a principal that
--     may already own grants, sessions or an audit trail;
--   * no `ALTER ROLE` on a role that already exists — an existing principal's
--     authority is VERIFIED and, if wrong, REFUSED out loud; never silently changed;
--   * no membership between `olivares_app` and `olivares_admin` in either direction —
--     membership would hand the application role a way to SET ROLE into BYPASSRLS and
--     make the FORCE-RLS backstop inert, which is the whole reason the roles are split;
--   * no write privilege for the admin role, and no `--allow-privileged-db-role`. This
--     file grants only SELECT — and since GRANT cannot take a privilege away, it also
--     REFUSES when a role that already exists can reach write authority on schema public
--     — a table or column privilege, CREATE on the schema, ownership of a relation, or
--     pg_write_all_data — directly or through PUBLIC or a group it can SET ROLE into. Sequences, functions,
--     other schemas and other databases are outside that check;
--   * nothing to the application role. `01-app-role.sql` owns that principal.
--
-- The credential is read from the psql variable `admin_password` and quoted
-- SERVER-SIDE with `format('%L')`, the same way the binary's provisioner does it, so
-- the password is never concatenated into SQL text by a shell.

-- --------------------------------------------------------------------------------
-- 0 · Preconditions. Refuse EARLY and by name; a half-applied bootstrap is worse
--     than one that never started.
-- --------------------------------------------------------------------------------
\if :{?admin_password}
\else
DO $guard$
BEGIN
	RAISE EXCEPTION 'olivares: 02-admin-role.sql needs -v admin_password=… . The compose initdb hook passes OLIVARES_ADMIN_PASSWORD; by hand use: psql … -v admin_password="$OLIVARES_ADMIN_PASSWORD" -f 02-admin-role.sql';
END
$guard$;
\endif

SELECT length(trim(:'admin_password')) = 0 AS olivares_admin_password_empty \gset
\if :olivares_admin_password_empty
DO $guard$
BEGIN
	RAISE EXCEPTION 'olivares: -v admin_password is empty. A LOGIN role with no password is not a credential; set OLIVARES_ADMIN_PASSWORD (deploy/compose/README.md) or pass -v admin_password=… by hand.';
END
$guard$;
\endif

DO $guard$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'olivares_app') THEN
		RAISE EXCEPTION 'olivares: role olivares_app does not exist. Run deploy/postgres/01-app-role.sql FIRST: this file adds the cross-tenant read role beside the application role, it does not create it.';
	END IF;
	-- The application role's authority is a PRECONDITION here, never an output. If it
	-- is not NOSUPERUSER NOBYPASSRLS the FORCE-RLS tenant backstop is already inert and
	-- adding a second pool would paper over that, so this refuses instead.
	IF EXISTS (
		SELECT 1 FROM pg_catalog.pg_roles
		WHERE rolname = 'olivares_app' AND (rolsuper OR rolbypassrls)
	) THEN
		RAISE EXCEPTION 'olivares: role olivares_app is SUPERUSER or BYPASSRLS, so FORCE ROW LEVEL SECURITY does not isolate tenants on it. Fix the application role (deploy/postgres/01-app-role.sql) before provisioning the administrative read role; this file will not change it.';
	END IF;
	IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database WHERE datname = 'olivares') THEN
		RAISE EXCEPTION 'olivares: database olivares does not exist. Run deploy/postgres/01-app-role.sql FIRST.';
	END IF;
END
$guard$;

-- --------------------------------------------------------------------------------
-- 1 · The role itself: NOSUPERUSER (least privilege) + BYPASSRLS (the one capability
--     it exists for). Created ONLY if absent — see "never does" above.
-- --------------------------------------------------------------------------------
SELECT pg_catalog.format(
	'CREATE ROLE olivares_admin LOGIN PASSWORD %L NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION',
	:'admin_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'olivares_admin')\gexec

-- The posture the engine's boot guard will demand (openAdminPool: BYPASSRLS required,
-- SUPERUSER refused). Verified for a role this run created AND for one that already
-- existed, because the second case is the one where silence would be dangerous.
DO $guard$
BEGIN
	IF NOT EXISTS (
		SELECT 1 FROM pg_catalog.pg_roles
		WHERE rolname = 'olivares_admin'
		  AND rolcanlogin AND rolbypassrls
		  AND NOT rolsuper AND NOT rolcreaterole AND NOT rolcreatedb AND NOT rolreplication
	) THEN
		RAISE EXCEPTION 'olivares: role olivares_admin already exists with a posture this bootstrap will not adopt. It requires LOGIN NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION — deliberately stricter than the engine minimum, which only demands BYPASSRLS and refuses SUPERUSER. This file does not ALTER an existing principal: inspect it (\\du olivares_admin) and correct it deliberately.';
	END IF;
	-- Membership in EITHER direction, direct or inherited. pg_has_role follows the
	-- whole graph, so an indirect path through a group role is caught too.
	IF pg_catalog.pg_has_role('olivares_app', 'olivares_admin', 'MEMBER') THEN
		RAISE EXCEPTION 'olivares: olivares_app is a member of olivares_admin (directly or through a group). That lets the application role SET ROLE into BYPASSRLS and defeats the FORCE-RLS tenant backstop. Revoke the membership; the two pools are deliberately separate principals.';
	END IF;
	IF pg_catalog.pg_has_role('olivares_admin', 'olivares_app', 'MEMBER') THEN
		RAISE EXCEPTION 'olivares: olivares_admin is a member of olivares_app (directly or through a group). The administrative pool is read-only by design; membership in the table-owning role would give it write authority. Revoke the membership.';
	END IF;
END
$guard$;

-- --------------------------------------------------------------------------------
-- 2 · The already-designed read privileges. Nothing more: no CREATE on the schema and
--     nothing beyond SELECT on the relations in it (sequences are out of scope).
-- --------------------------------------------------------------------------------
GRANT CONNECT ON DATABASE olivares TO olivares_admin;

\connect olivares

-- BEFORE granting anything here: a role that can ALREADY write is not made read-only by
-- adding SELECT to it. GRANT never takes a privilege away, so a pre-existing
-- olivares_admin keeps whatever it had and this file used to report read-only anyway.
-- Reachability, not just direct grants: a privilege counts if it was granted to the role,
-- to PUBLIC, or to any role olivares_admin is a member of (pg_has_role follows the whole
-- graph, and MEMBER holds even for a NOINHERIT role, which can still SET ROLE).
-- Scope: relations in schema public of THIS database. Sequences are not inspected.
DO $guard$
DECLARE
	v record;
BEGIN
	-- pg_write_all_data is the one that no ACL inspection can find: membership confers
	-- INSERT/UPDATE/DELETE on every table in the cluster and leaves no entry on any of
	-- them (measured: relacl stays NULL while has_table_privilege says true).
	IF pg_catalog.pg_has_role('olivares_admin', 'pg_write_all_data', 'MEMBER') THEN
		RAISE EXCEPTION 'olivares: olivares_admin is a member of pg_write_all_data, which confers INSERT/UPDATE/DELETE on every table without leaving a privilege entry on any of them. Revoke the membership deliberately, then re-run; this file does not REVOKE.';
	END IF;

	-- Three ways the role can reach a write on a relation here. Each is keyed on
	-- pg_has_role(..., 'MEMBER'), which is the SET ROLE-reachable closure: it holds for an
	-- inherited grant and for a NOINHERIT group the role can still SET ROLE into.
	SELECT * INTO v FROM (
		-- 1 · table-level privileges, in relacl.
		SELECT 'holds ' || acl.privilege_type || ' on' AS what,
		       c.relname AS rel, COALESCE(g.rolname, 'PUBLIC') AS via
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(
			COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) acl
		LEFT JOIN pg_catalog.pg_roles g ON g.oid = acl.grantee
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND acl.privilege_type <> 'SELECT'
		  AND CASE WHEN acl.grantee = 0 THEN true
		           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
		UNION ALL
		-- 2 · COLUMN-level privileges live in pg_attribute.attacl, and a table-level
		--     has_table_privilege('INSERT') reports FALSE while the insert succeeds.
		SELECT 'holds column-level ' || acl.privilege_type || ' on',
		       c.relname || '.' || a.attname, COALESCE(g.rolname, 'PUBLIC')
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) acl
		LEFT JOIN pg_catalog.pg_roles g ON g.oid = acl.grantee
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND acl.privilege_type <> 'SELECT'
		  AND CASE WHEN acl.grantee = 0 THEN true
		           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
		UNION ALL
		-- 3 · OWNERSHIP is standing authority, not a privilege: an owner that revoked its
		--     own DML can GRANT it straight back, and can ALTER or DROP the relation.
		SELECT 'owns', c.relname, owner.rolname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		JOIN pg_catalog.pg_roles owner ON owner.oid = c.relowner
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND pg_catalog.pg_has_role('olivares_admin', c.relowner, 'MEMBER')
	) reachable ORDER BY rel, what LIMIT 1;
	IF FOUND THEN
		RAISE EXCEPTION 'olivares: olivares_admin % public.% (through %), so it is not a read-only pool. GRANT SELECT cannot take that back and this file does not REVOKE or change ownership: correct it deliberately, then re-run. Inspect with \\dp public.% and \\d public.%', v.what, v.rel, v.via, v.rel, v.rel;
	END IF;

	-- The same question for the tables the app has NOT created yet. A default privilege
	-- reaches them whether it names olivares_admin, PUBLIC or a role it belongs to, and
	-- whether it was set IN SCHEMA public or globally (defaclnamespace = 0).
	SELECT COALESCE(ns.nspname, 'every schema') AS scope, acl.privilege_type AS priv,
	       COALESCE(g.rolname, 'PUBLIC') AS grantee
	INTO v
	FROM pg_catalog.pg_default_acl d
	JOIN pg_catalog.pg_roles grantor ON grantor.oid = d.defaclrole
	LEFT JOIN pg_catalog.pg_namespace ns ON ns.oid = d.defaclnamespace
	CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) acl
	LEFT JOIN pg_catalog.pg_roles g ON g.oid = acl.grantee
	WHERE grantor.rolname = 'olivares_app'
	  AND d.defaclobjtype = 'r'
	  AND (d.defaclnamespace = 0 OR ns.nspname = 'public')
	  AND acl.privilege_type <> 'SELECT'
	  AND CASE WHEN acl.grantee = 0 THEN true
	           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
	LIMIT 1;
	IF FOUND THEN
		RAISE EXCEPTION 'olivares: olivares_app has a default privilege that would give olivares_admin % on every table it creates in % (granted to %). The pool would stop being read-only at the next migration. Remove it (ALTER DEFAULT PRIVILEGES ... REVOKE) and re-run; this file does not REVOKE. Inspect with \\ddp', v.priv, v.scope, v.grantee;
	END IF;

	-- CREATE on the schema is write authority of its own: whoever holds it makes relations
	-- here and owns them. It is asked of every role the admin can reach, not of the admin
	-- alone, because has_schema_privilege answers only for privileges a role INHERITS —
	-- a membership granted WITH INHERIT FALSE, SET TRUE returns false there and is still
	-- one SET ROLE away from CREATE TABLE. Reaching itself is included, which is also what
	-- moves the direct case ahead of the grants below.
	SELECT r.rolname AS via INTO v
	FROM pg_catalog.pg_roles r
	WHERE pg_catalog.pg_has_role('olivares_admin', r.oid, 'MEMBER')
	  AND pg_catalog.has_schema_privilege(r.oid, 'public', 'CREATE')
	ORDER BY (r.rolname <> 'olivares_admin'), r.rolname
	LIMIT 1;
	IF FOUND THEN
		RAISE EXCEPTION 'olivares: CREATE on schema public is reachable by olivares_admin through role %, so it can create relations there and own them. A read pool must not be able to create. Revoke it deliberately, then re-run; this file does not REVOKE. Inspect with \\dn+ public and \\du %', v.via, v.via;
	END IF;
END
$guard$;

GRANT USAGE ON SCHEMA public TO olivares_admin;

-- Existing tables (a no-op on a fresh cluster, and the part that matters when this
-- file is applied by hand to a RETAINED volume whose tables already exist).
GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_admin;

-- FUTURE tables, attached to the role that ACTUALLY CREATES THEM. In this posture
-- olivares_app owns the database and runs the migrations, so it is the grantor whose
-- default privileges apply. Set BEFORE the engine's first migration, which is what
-- makes every table the first boot creates selectable without a later manual GRANT.
ALTER DEFAULT PRIVILEGES FOR ROLE olivares_app IN SCHEMA public
	GRANT SELECT ON TABLES TO olivares_admin;

-- --------------------------------------------------------------------------------
-- 3 · Prove the post-condition rather than assume it. An ACL that did not land is
--     indistinguishable from one that did until a cross-tenant read fails at 2 a.m.
-- --------------------------------------------------------------------------------
DO $guard$
BEGIN
	IF NOT pg_catalog.has_database_privilege('olivares_admin', 'olivares', 'CONNECT') THEN
		RAISE EXCEPTION 'olivares: olivares_admin cannot CONNECT to database olivares after the grant above.';
	END IF;
	IF NOT pg_catalog.has_schema_privilege('olivares_admin', 'public', 'USAGE') THEN
		RAISE EXCEPTION 'olivares: olivares_admin has no USAGE on schema public after the grant above.';
	END IF;
	-- Read-only means read-only: the engine schema must not be writable by this role.
	IF EXISTS (
		SELECT 1 FROM pg_catalog.pg_roles r
		WHERE pg_catalog.pg_has_role('olivares_admin', r.oid, 'MEMBER')
		  AND pg_catalog.has_schema_privilege(r.oid, 'public', 'CREATE')
	) THEN
		RAISE EXCEPTION 'olivares: CREATE on schema public is reachable by olivares_admin, directly or through a role it can SET ROLE into. The administrative pool is a read role; re-run to see which role is named.';
	END IF;
	-- The default-privilege row is the MECHANISM for the tables first boot creates, so
	-- its presence is checked, and checked to be SELECT and nothing else.
	IF NOT EXISTS (
		SELECT 1
		FROM pg_catalog.pg_default_acl d
		JOIN pg_catalog.pg_roles grantor ON grantor.oid = d.defaclrole
		JOIN pg_catalog.pg_namespace ns ON ns.oid = d.defaclnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) acl
		JOIN pg_catalog.pg_roles grantee ON grantee.oid = acl.grantee
		WHERE grantor.rolname = 'olivares_app' AND ns.nspname = 'public'
		  AND d.defaclobjtype = 'r' AND grantee.rolname = 'olivares_admin'
		  AND acl.privilege_type = 'SELECT'
	) THEN
		RAISE EXCEPTION 'olivares: no default SELECT privilege for olivares_admin on olivares_app''s future tables in schema public. The engine''s first migration would then create tables the administrative pool cannot read.';
	END IF;
	-- Read-only is asserted of the state this file LEAVES, not only of the one it found.
	-- Same reachability as the guard before the grants: direct, PUBLIC, or through a role
	-- olivares_admin belongs to; existing relations and olivares_app's future tables.
	IF pg_catalog.pg_has_role('olivares_admin', 'pg_write_all_data', 'MEMBER') OR EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(
			COALESCE(c.relacl, pg_catalog.acldefault('r', c.relowner))) acl
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND acl.privilege_type <> 'SELECT'
		  AND CASE WHEN acl.grantee = 0 THEN true
		           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
	) OR EXISTS (
		SELECT 1
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) acl
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND acl.privilege_type <> 'SELECT'
		  AND CASE WHEN acl.grantee = 0 THEN true
		           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
	) OR EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
		WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND pg_catalog.pg_has_role('olivares_admin', c.relowner, 'MEMBER')
	) OR EXISTS (
		SELECT 1
		FROM pg_catalog.pg_default_acl d
		JOIN pg_catalog.pg_roles grantor ON grantor.oid = d.defaclrole
		LEFT JOIN pg_catalog.pg_namespace ns ON ns.oid = d.defaclnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) acl
		WHERE grantor.rolname = 'olivares_app' AND d.defaclobjtype = 'r'
		  AND (d.defaclnamespace = 0 OR ns.nspname = 'public')
		  AND acl.privilege_type <> 'SELECT'
		  AND CASE WHEN acl.grantee = 0 THEN true
		           ELSE pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER') END
	) THEN
		RAISE EXCEPTION 'olivares: after the grants above olivares_admin can still reach write authority in schema public — a table or column privilege, ownership, or pg_write_all_data. The administrative pool must be read-only; re-run to see which one is named.';
	END IF;
	RAISE NOTICE 'olivares_admin: NOSUPERUSER BYPASSRLS pool ready — CONNECT + USAGE on schema public + SELECT on its relations and on olivares_app''s future tables. In schema public it reaches no table or column privilege other than SELECT, cannot CREATE there, owns nothing, and is not in pg_write_all_data — counting grants made to it, to PUBLIC, or to any role it can SET ROLE into. NOT inspected: sequences, functions, other schemas, other databases.';
END
$guard$;
