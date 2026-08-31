#!/usr/bin/env sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Consistent logical backup of the Olivares control-plane Postgres store, wrapped
# into a LEDGER-CONTINUITY-SAFE DR bundle (docs/DR-RUNBOOK.md).
#
# pg_dump's default single transaction is a consistent snapshot, so audit_events
# and audit_heads are captured at one instant (the chain's tail-truncation
# detection survives the dump). `olivares dr backup` then wraps that dump with
# the signing keys (encrypted under your KEK) and a manifest of the per-tenant
# chain tips, so the restore is provably complete and the ledger is verifiable.
#
# RPO with this method == the interval you run it at. For near-zero RPO use PITR
# (see pitr-setup.md) and pair it with `dr backup --pitr-ref`.
#
# Requires pg_dump AND the olivares binary on PATH, and READ access to the
# engine's data dir (the signing keys live there).
#
# Usage:
#   OLIVARES_DSN=postgres://olivares_app:***@host:5432/olivares \
#   OLIVARES_ADMIN_DSN=postgres://olivares_admin:***@host:5432/olivares \
#   OLIVARES_DATA_DIR=/var/lib/olivares \
#   OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
#   ./pg-dump.sh /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle
set -eu

OUT="${1:?usage: pg-dump.sh <out.drbundle>}"
: "${OLIVARES_DSN:?set OLIVARES_DSN (the olivares_app role DSN)}"
: "${OLIVARES_ADMIN_DSN:?set OLIVARES_ADMIN_DSN (the NOSUPERUSER BYPASSRLS backup/admin role DSN)}"
: "${OLIVARES_DATA_DIR:=/var/lib/olivares}"
: "${OLIVARES_DR_PASSPHRASE_FILE:?set OLIVARES_DR_PASSPHRASE_FILE (the backup KEK passphrase)}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "pg_dump (custom format, single consistent snapshot)…"
# pg_dump disables row_security and fails on FORCE-RLS tables through the app
# role. The read-only BYPASSRLS role sees every tenant and has no write grants.
pg_dump --format=custom --no-owner --no-privileges \
  --file="$TMP/dump.pgcustom" --dbname="$OLIVARES_ADMIN_DSN"

echo "wrapping into a DR bundle (signing keys + chain-tip manifest + dump)…"
olivares dr backup \
  --engine=postgres \
  --dsn="$OLIVARES_DSN" \
  --admin-dsn="$OLIVARES_ADMIN_DSN" \
  --data-dir="$OLIVARES_DATA_DIR" \
  --snapshot-file="$TMP/dump.pgcustom" \
  --out="$OUT" \
  --passphrase-file="$OLIVARES_DR_PASSPHRASE_FILE"

echo "DR bundle written: $OUT"
echo "REMEMBER: mirror it OFFSITE and keep the KEK passphrase SEPARATE (3-2-1, docs/DR-RUNBOOK.md)."
