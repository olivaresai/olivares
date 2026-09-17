#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Postgres initdb hook (runs ONCE, as the superuser, on first cluster init). It
# applies the canonical least-privilege role SQL with the olivares_app password
# from the environment. The official postgres image executes /docker-entrypoint-
# initdb.d/*.sh with PGUSER/POSTGRES_DB set, so psql connects locally as superuser.
#
# IT PROVISIONS TWO DISTINCT PRINCIPALS, and both are required for a FRESH Postgres
# install to be able to complete its first setup:
#
#   olivares_app    NOSUPERUSER NOBYPASSRLS — owns the database, serves all traffic,
#                   and is fully isolated by FORCE ROW LEVEL SECURITY.
#   olivares_admin  NOSUPERUSER BYPASSRLS, READ-ONLY — the cross-tenant System read
#                   pool (--admin-dsn). POST /v1/setup resolves the first
#                   organization through an authoritative cross-tenant read, which
#                   the RLS-scoped app role cannot perform: without this role the
#                   engine answers 501 cross_tenant_admin_pool_not_configured and
#                   /readyz answers 503 setup_blocked. Provisioning only the app
#                   role leaves a deployment that can never be set up.
#
# ⛔ INITIALIZATION-ONLY LIFECYCLE. `docker-entrypoint-initdb.d` runs ONLY when the
# data directory is empty. On a RETAINED pg-data volume this file is never executed
# again, so editing it repairs nothing there — deploy/compose/README.md documents the
# explicit one-command provisioning path for that case. Never delete the volume to
# "re-run initdb": that destroys the estate.
set -eu

: "${OLIVARES_DB_PASSWORD:?OLIVARES_DB_PASSWORD must be set for the olivares_app role}"
: "${OLIVARES_ADMIN_PASSWORD:?OLIVARES_ADMIN_PASSWORD must be set for the olivares_admin cross-tenant read role (see deploy/compose/README.md)}"

# The canonical SQL is mounted read-only at /sql by the compose override. The
# indirection exists so the SHIPPED hook — not a transcription of it — can also be
# executed against an owned local cluster when qualifying this bootstrap outside a
# container; the container default is unchanged.
sql_dir="${OLIVARES_INITDB_SQL_DIR:-/sql}"

run_sql() {
	_file="$1"
	shift
	[ -r "${sql_dir}/${_file}" ] || {
		echo "[olivares] FATAL: ${sql_dir}/${_file} is not readable — the compose override mounts the canonical SQL there" >&2
		exit 1
	}
	psql -v ON_ERROR_STOP=1 \
		--username "${POSTGRES_USER:-postgres}" \
		--dbname "${POSTGRES_DB:-postgres}" \
		"$@" \
		-f "${sql_dir}/${_file}"
}

echo "[olivares] provisioning least-privilege olivares_app role + olivares database…"
run_sql 01-app-role.sql -v app_password="${OLIVARES_DB_PASSWORD}"
echo "[olivares] provisioning NOSUPERUSER BYPASSRLS read-only olivares_admin role (cross-tenant System reads)…"
run_sql 02-admin-role.sql -v admin_password="${OLIVARES_ADMIN_PASSWORD}"
echo "[olivares] done — engine connects as olivares_app (NOSUPERUSER, NOBYPASSRLS) and reads cross-tenant as olivares_admin (NOSUPERUSER, BYPASSRLS, read-only)."
