#!/usr/bin/env sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Restore an Olivares control-plane DR bundle into a Postgres target and PROVE
# ledger continuity (docs/DR-RUNBOOK.md). `olivares dr restore`:
#   1. decrypts the signing keys from the bundle into the data dir (under your KEK),
#   2. runs pg_restore of the bundled dump into the target database,
#   3. re-verifies every tenant chain + per-event signatures + checkpoints + tips,
#      and EXITS NON-ZERO unless the restored ledger is continuity-safe.
#
# The target database should be EMPTY (a fresh DR target with the olivares_app role
# provisioned via 01-app-role.sql). Requires pg_restore AND the olivares binary
# on PATH.
#
# For a PITR recovery, recover Postgres from the basebackup + WAL FIRST (see
# pitr-setup.md), then run this with the `--pitr-ref` companion bundle — it skips
# pg_restore and only installs the keys + verifies the recovered store.
#
# Usage:
#   OLIVARES_DSN=postgres://olivares_app:***@host:5432/olivares \
#   OLIVARES_DATA_DIR=/var/lib/olivares \
#   OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
#   ./pg-restore.sh /backups/olivares-dr-20260609T000000Z.drbundle
set -eu

IN="${1:?usage: pg-restore.sh <in.drbundle>}"
: "${OLIVARES_DSN:?set OLIVARES_DSN (the target olivares_app role DSN)}"
: "${OLIVARES_DATA_DIR:=/var/lib/olivares}"
: "${OLIVARES_DR_PASSPHRASE_FILE:?set OLIVARES_DR_PASSPHRASE_FILE (the backup KEK passphrase)}"

# OLIVARES_ADMIN_DSN — the NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql).
#
# WHY IT IS HERE NOW, and why the script does not simply demand it. The verifier gained a
# check this script cannot satisfy as the application role: tenants present in the restored
# store but ABSENT from the manifest — a foreign bundle, or a restore into an unclean store.
# Enumerating tenants under FORCE ROW LEVEL SECURITY as olivares_app is not authoritative, so
# the check cannot run, and a verifier that cannot look must not certify. Without this DSN the
# restore therefore reports NOT-OK with that reason, which is correct and is NOT what an
# operator following this runbook expects to see.
#
# So: pass it when you have it and get the full verification; run without it and be told, in
# advance and in one sentence, which check you are giving up. What this script must never do
# is print "restore verified" for a run whose foreign-bundle check never happened.
# POSIX sh, not bash: this file is `#!/usr/bin/env sh`, so there are no arrays. `set --`
# rebuilds the positional parameters, which is the portable way to carry an OPTIONAL
# argument. IN was already captured above, so overwriting $1 here is safe. (Written with a
# bash array first; `bash -n` accepted it and `sh -n` did not — the shebang decides.)
if [ -n "${OLIVARES_ADMIN_DSN:-}" ]; then
  set -- --admin-dsn="$OLIVARES_ADMIN_DSN"
else
  set --
  echo "NOTE: OLIVARES_ADMIN_DSN is not set, so the extra-tenant check (foreign bundle / unclean" >&2
  echo "      target) CANNOT RUN and this restore will be reported NOT-OK for that reason alone." >&2
  echo "      Set it to the NOSUPERUSER BYPASSRLS role to get a complete verification." >&2
fi

echo "restoring + verifying ledger continuity from $IN…"
olivares dr restore \
  --engine=postgres \
  --dsn="$OLIVARES_DSN" \
  --data-dir="$OLIVARES_DATA_DIR" \
  "$@" \
  --in="$IN" \
  --passphrase-file="$OLIVARES_DR_PASSPHRASE_FILE"

echo "restore verified: ledger continuity and key custody intact."
echo "Start the engine and confirm with: GET /v1/audit/verify  (or  olivares audit verify --tenant <id>)."
