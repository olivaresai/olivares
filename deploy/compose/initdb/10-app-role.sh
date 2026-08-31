#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Postgres initdb hook (runs ONCE, as the superuser, on first cluster init). It
# applies the canonical least-privilege role SQL with the olivares_app password
# from the environment. The official postgres image executes /docker-entrypoint-
# initdb.d/*.sh with PGUSER/POSTGRES_DB set, so psql connects locally as superuser.
set -eu

: "${OLIVARES_DB_PASSWORD:?OLIVARES_DB_PASSWORD must be set for the olivares_app role}"

echo "[olivares] provisioning least-privilege olivares_app role + olivares database…"
psql -v ON_ERROR_STOP=1 \
  --username "${POSTGRES_USER:-postgres}" \
  --dbname "${POSTGRES_DB:-postgres}" \
  -v app_password="${OLIVARES_DB_PASSWORD}" \
  -f /sql/01-app-role.sql
echo "[olivares] done — engine will connect as olivares_app (NOSUPERUSER, NOBYPASSRLS)."
