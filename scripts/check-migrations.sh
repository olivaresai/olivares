#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-migrations.sh — static online-safety linter for SQL migrations (OPS-3). A control plane that runs 24/7 cannot take a maintenance
# window for a schema change, so every migration must follow the expand-contract
# (parallel-change) model:
#
#   * EXPAND  (NNNN_expand_*.sql, or any file not marked contract): ADDITIVE only.
#     A new nullable column, a new table, a new index, a backfill. Old and new code
#     both run against an expanded schema, so it ships with zero downtime. A
#     destructive statement here is the classic outage: it breaks the still-running
#     old replicas mid-rollout.
#   * CONTRACT (NNNN_contract_*.sql): the destructive cleanup (DROP COLUMN, DROP
#     TABLE, SET NOT NULL, RENAME). Online-safe ONLY because it ships in a LATER
#     release than its expand — by then no running node depends on what it removes.
#
# This linter FAILS the build when an EXPAND migration contains a destructive
# statement, and warns when an index is created without CONCURRENTLY (which would
# hold a write lock on a populated table — use CREATE INDEX CONCURRENTLY + the
# Migration.NonTransactional flag). The Migration model (core/migrate) and the
# cluster-wide migration advisory lock (sqlstore) are the runtime half; this is the
# authoring guard. Mirrors the script+task shape of check-spdx.sh / check-boundary.sh.
#
# Usage:  scripts/check-migrations.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# scan_sql classifies a file by its name (…_contract_… ⇒ contract, else expand) and,
# for an expand, fails on any destructive statement. Returns 0 if clean, 1 if a
# violation was found; prints each violation. Content is passed as an argument so the
# same function lints both real files and the self-test samples.
scan_sql() {
  # The third argument is the REPOSITORY PATH, and it exists so an exemption can be by path
  # rather than by basename: two trees can hold a 007_*.up.sql, and an exemption that matched the
  # basename would silently cover both. The self-test callers pass no third argument, so it is
  # empty there and matches nothing.
  local name="$1" content="$2" path="${3:-}" phase="expand" bad=0
  case "${name}" in
    *contract*) phase="contract" ;;
    # ⛔ A `.down.sql` IS THE CONTRACT STEP, and calling it "expand" is a category error that
    # widening the census exposed. modules/ names its rollback `NNNN_contract_*.sql`;
    # cloud/control-plane ships up/down PAIRS — a convention this gate had never seen, because the
    # four-digit name pattern excluded that whole tree. Graded as expand, all eleven downs failed
    # for containing exactly what a down migration exists to contain.
    #
    # Recognising the convention is not loosening the rule: the destructive checks still run on
    # every `.up.sql`, which is the half that runs forward against live readers.
    *.down.sql) phase="contract" ;;
  esac

  # Strip SQL line comments and collapse to a single lower-case, whitespace-squeezed
  # line so multi-line DDL and odd spacing (DROP   COLUMN) lint uniformly and a
  # "-- drop column" note never trips the checks.
  local norm
  norm="$(printf '%s\n' "${content}" | sed 's/--.*$//' | tr 'A-Z\n\t' 'a-z  ' | tr -s ' ')"

  if [ "${phase}" = "expand" ]; then
    if printf '%s' "${norm}" | grep -Eq 'drop +table|drop +column|alter +table +[^;]*drop +| rename |set +not +null'; then
      # ⛔ ONE EXEMPTION, BY EXACT PATH AND EXACT STATEMENT, WITH ITS REASON — never a class.
      #
      # Widening a CHECK constraint cannot be done in place: the new one is added NOT VALID,
      # validated, and the old one dropped. That drop is destructive by this rule's letter and
      # additive in fact — every value the old constraint accepted, the new one accepts — so old
      # and new code both run against either schema.
      #
      # It is exempted here rather than in the migration because an applied migration is not
      # edited, and because an exemption that lives next to the RULE is one a reviewer of the rule
      # sees. The author already wrote the analysis by hand — measured on PostgreSQL 15.18 via
      # pg_locks, on a table that is empty in every deployment — precisely BECAUSE this gate used
      # to prune commercial/ and could not read it. Now it can, and this is the one file where
      # what it reads and what the rule means disagree.
      #
      # The match is the full path AND the constraint name. Any other drop in this file, or this
      # drop in another file, still fails.
      if [ "${path}" = "commercial/commerce/migrations/007_commerce_consumer.up.sql" ] \
         && printf '%s' "${norm}" | grep -q 'drop constraint grant_command_acks_consumer_check' \
         && printf '%s' "${norm}" | grep -q 'add constraint grant_command_acks_consumer_check_v2'; then
        echo "  ~ ${name}: EXEMPT — CHECK widening (add NOT VALID, validate, drop old); additive in behaviour, empty table"
      else
        echo "  ✗ ${name}: EXPAND migration contains a DESTRUCTIVE statement — move it to a separate NNNN_contract_*.sql in a LATER release"
        bad=1
      fi
    fi
    # ADD COLUMN ... NOT NULL without a DEFAULT rewrites/locks the table on Postgres.
    if printf '%s' "${norm}" | grep -Eq 'add +column [^;]*not +null' \
       && ! printf '%s' "${norm}" | grep -Eq 'add +column [^;]*not +null[^;]*default'; then
      echo "  ✗ ${name}: EXPAND adds a NOT NULL column without DEFAULT (table rewrite / blocks writes) — add a DEFAULT, or backfill then SET NOT NULL in a contract"
      bad=1
    fi
  fi

  # Hygiene warning (both phases): a plain CREATE INDEX locks writes on a populated
  # table. Not fatal (it is safe on a table created in the same migration), but worth
  # surfacing so authors reach for CONCURRENTLY + NonTransactional.
  if printf '%s' "${norm}" | grep -Eq 'create +(unique +)?index' \
     && ! printf '%s' "${norm}" | grep -Eq 'concurrently'; then
    echo "  ⚠ ${name}: CREATE INDEX without CONCURRENTLY — fine on a new table, but on a populated one use CREATE INDEX CONCURRENTLY + Migration.NonTransactional"
  fi
  return ${bad}
}

# --- self-test: prove the checker logic works even when no real migrations exist yet.
self_test() {
  local fails=0
  # A clean expand must pass.
  if ! scan_sql "0001_expand_add_email.sql" "ALTER TABLE users ADD COLUMN email TEXT;" >/dev/null; then
    echo "self-test FAIL: a clean expand was rejected"; fails=1
  fi
  # A destructive expand must fail.
  if scan_sql "0002_expand_oops.sql" "ALTER TABLE users DROP COLUMN email;" >/dev/null; then
    echo "self-test FAIL: a destructive expand was accepted"; fails=1
  fi
  # The same destructive statement in a contract is allowed.
  if ! scan_sql "0003_contract_drop_email.sql" "ALTER TABLE users DROP COLUMN email;" >/dev/null; then
    echo "self-test FAIL: a destructive contract was rejected"; fails=1
  fi
  # NOT NULL without default in an expand must fail.
  if scan_sql "0004_expand_notnull.sql" "ALTER TABLE users ADD COLUMN tier TEXT NOT NULL;" >/dev/null; then
    echo "self-test FAIL: a NOT NULL-without-default expand was accepted"; fails=1
  fi
  # A .down.sql is the CONTRACT step of an up/down pair — the convention cloud/control-plane uses
  # and this gate had never seen, because the four-digit pattern excluded that tree entirely.
  if ! scan_sql "001_schema.down.sql" "DROP TABLE tenants;" "cloud/control-plane/migrations/001_schema.down.sql" >/dev/null; then
    echo "self-test FAIL: a .down.sql was graded as an expand"; fails=1
  fi
  # ...and its .up.sql half is still held to the expand rule. Recognising the convention must not
  # become "this tree is exempt".
  if scan_sql "001_schema.up.sql" "ALTER TABLE tenants DROP COLUMN email;" "cloud/control-plane/migrations/001_schema.up.sql" >/dev/null; then
    echo "self-test FAIL: a destructive .up.sql in an up/down tree was accepted"; fails=1
  fi
  # THE EXEMPTION IS BY EXACT PATH. The same statement anywhere else still fails — otherwise one
  # reviewed decision becomes a class, which is how every allowlist in this repository has rotted.
  local widen="ALTER TABLE commerce.grant_command_acks ADD CONSTRAINT grant_command_acks_consumer_check_v2 CHECK (consumer IN ('a')) NOT VALID; ALTER TABLE commerce.grant_command_acks DROP CONSTRAINT grant_command_acks_consumer_check;"
  if ! scan_sql "007_commerce_consumer.up.sql" "${widen}" "commercial/commerce/migrations/007_commerce_consumer.up.sql" >/dev/null; then
    echo "self-test FAIL: the reviewed CHECK widening was rejected at its own path"; fails=1
  fi
  if scan_sql "007_commerce_consumer.up.sql" "${widen}" "some/other/migrations/007_commerce_consumer.up.sql" >/dev/null; then
    echo "self-test FAIL: the exemption applied to a path it does not name"; fails=1
  fi
  # NOT NULL WITH a default in an expand is fine.
  if ! scan_sql "0005_expand_notnull_default.sql" "ALTER TABLE users ADD COLUMN tier TEXT NOT NULL DEFAULT 'free';" >/dev/null; then
    echo "self-test FAIL: a NOT NULL-with-default expand was rejected"; fails=1
  fi
  if [ "${fails}" -ne 0 ]; then
    echo "check-migrations.sh: SELF-TEST FAILED (the linter itself is broken)"; exit 2
  fi
}

self_test

echo "==> scanning SQL migrations for expand-contract online-safety"
# Migration files follow NNNN_<phase>_name.sql. Scan the whole tree (engines embed
# their .sql under module dirs); exclude vendored / generated trees. commercial/ is the
# Cloudflare D1 of the license Worker — a separate single-writer DB whose schema follows
# wrangler's NNNN_*.sql naming but NOT the engine's Postgres expand-contract rules
# (CONCURRENTLY is not even SQLite syntax), so it is out of scope for this linter.
# THE EXIT STATUS OF `find` IS LOST HERE, and that is not pedantry: `mapfile < <(cmd)` always
# returns 0, so a PARTIAL enumeration — an unreadable directory, a permission error mid-walk — is
# invisible and its result is graded as if complete. Measured by an adversarial contrast with a
# directory in mode 000 containing a migration. Captured to a file so the status is readable.
_mig_list="$(mktemp)" || { echo "check-migrations: COULD NOT LOOK — mktemp failed" >&2; exit 2; }
trap 'rm -f "$_mig_list"' EXIT
# ⛔ THE CENSUS IS `git ls-files`, NOT `find .`, AND THAT CHANGED WHAT IT GRADES BY THIRTY FILES.
#
# A raw `find .` walks whatever happens to be lying in the tree, so the verdict depended on the
# junk present: measured 2026-08-14, the same script graded 55 files in a clean worktree and 163
# in the shared clone — 96 of them from `.claude/worktrees/wf_*` and 12 from `.export-tmp/tmp.*`,
# a directory `lint:export` CREATES earlier in the same hook run. A gate whose subject changes
# with the contents of /tmp is not measuring the repository.
#
# And it missed thirty real ones, in two independent ways:
#
#   ·  the name pattern was FOUR digits, so cloud/control-plane/migrations/001_… through 011_…
#      (22 files) never matched — tenant provisioning, billing_events, suspension_log
#   ·  `-path './commercial/*'` pruned the whole tree with a written reason that names D1 and
#      SQLite. That is true of commercial/license-worker/migrations (9 files) and NOT of
#      commercial/commerce/migrations (8 files), which is PostgreSQL: CREATE SCHEMA, TIMESTAMPTZ,
#      BIGSERIAL, plpgsql. Orders, ledger and saga were out of scope by accident of path.
#
# Tracked files only, three digits or more, and the D1 tree pruned BY NAME rather than by its
# parent. `git ls-files` also removes the partial-walk hazard the paragraph above describes: it
# either lists the index or fails, and its status is checked.
# ⛔ `--others --exclude-standard`, AND THE COST OF LEAVING IT OUT IS ALREADY IN THIS TREE.
# Reading the census from the index fixed the scratch-tree problem and opened a new one: a
# migration its author has written but not `git add`-ed yet is INVISIBLE, so `task lint` says clean
# about a file it never opened — which is, word for word, the failure this gate was rewritten to
# stop, moved one step earlier. It is not hypothetical: a51bb004e had to plant AND add its fixture
# because "the linter cannot see the file at all", and that commit adapted the TEST to the blind
# spot rather than closing it.
#
# `--others` restores the author's uncommitted file; `--exclude-standard` is what keeps the scratch
# trees out, and it is load-bearing rather than belt-and-braces. Measured 2026-08-15 in the shared
# hub clone: tracked-only 94, tracked+others+exclude-standard 94 — the fix costs nothing today —
# and tracked+others WITHOUT exclude-standard 106. The `_expected` control below deliberately stays
# tracked-only: it is the CONTROL, and a control that moves with the thing it checks is not one.
git ls-files -z --cached --others --exclude-standard -- \
  '*/migrations/*.sql' 'migrations/*.sql' > "$_mig_list.z" || {
  echo "check-migrations: UNVERIFIED — git ls-files exited $?; the migration set is UNKNOWN," >&2
  echo "  not empty. A partial enumeration graded as complete is the failure this says no to." >&2
  exit 2
}
tr '\0' '\n' < "$_mig_list.z" \
  | grep -E '/[0-9]{3,}_[^/]*\.sql$' \
  | grep -v '^commercial/license-worker/migrations/' \
  | grep -v '/testdata/' > "$_mig_list" || true
rm -f "$_mig_list.z"

# ⛔ THE CENSUS IS CHECKED AGAINST THE INDEX, because "how many did you grade" is the question this
# gate got wrong for its whole life. Every tracked migration that is not the D1 worker's must be
# in the list; a pattern that stops matching a tree, or a prune that grows a directory, turns this
# red instead of quietly grading a smaller set and printing a confident number.
#
# ⛔ AND THE CONTROL DOES NOT REPEAT THE NUMBERING PATTERN. It used to, and that made it VACUOUS for
# half of what its own paragraph promises: filtering both sides with `[0-9]{3,}` means a file the
# pattern stops matching disappears from the expectation at the same moment it disappears from the
# list, so the comparison balances and the gate grades a smaller set with a confident number — the
# exact defect that produced this script, reproduced inside its own safeguard. Measured 2026-08-15
# with a two-digit migration planted in a graded directory: exit 0, "3 migration(s) online-safe",
# the planted file never mentioned. A prune that grew was caught; a pattern that stopped matching
# was not.
#
# The expectation is therefore EVERY tracked .sql under a migrations/ directory that is not
# explicitly excluded, unfiltered by shape. Measured the same day across the tree: 96 such files,
# 0 of which fail the numbering pattern — so the strict form costs nothing today and refuses the
# first file that is ever named outside the convention, which is when the question actually matters.
_expected="$(git ls-files -- '*/migrations/*.sql' 'migrations/*.sql' \
  | grep -v '^commercial/license-worker/migrations/' \
  | grep -v '/testdata/' | sort)" || {
  echo "check-migrations: UNVERIFIED — the index could not be read for the census control" >&2
  exit 2
}
_missing="$(comm -23 <(printf '%s\n' "${_expected}") <(sort "$_mig_list"))"
if [ -n "${_missing}" ]; then
  echo "check-migrations: the census MISSED tracked migrations — the pattern or the prune is wrong:" >&2
  printf '  %s\n' ${_missing} >&2
  exit 2
fi
mapfile -t files < <(sort "$_mig_list")

rc=0
if [ "${#files[@]}" -eq 0 ]; then
  # THIS BRANCH USED TO PRINT "(no NNNN_*.sql migration files yet — the framework is in place;
  # this guard activates as soon as one lands)" AND EXIT 0. That sentence was true when it was
  # written and is FALSE NOW: measured 2026-08-08, this same `find` returns TWELVE files under
  # modules/eventing/migrations/. So the only way to reach this branch today is that the
  # enumeration stopped working — a moved directory, a widened prune, a rename — and the gate
  # would answer with a sentence claiming none exist yet. A verdict that was honest on the day
  # it was written becomes a false claim the moment the world moves under it; that is the same
  # class as trusting a dated note as live state, in the shape of a gate's own output.
  #
  # Zero units is COULD NOT LOOK. It is not "nothing to do" once there IS something to do.
  echo "check-migrations: ZERO migration files matched; expand-contract safety is UNVERIFIED." >&2
  echo "  On 2026-08-08 this same enumeration found 12 under modules/eventing/migrations/, so" >&2
  echo "  an empty result means the search stopped working, not that no migrations exist." >&2
  echo "  Check the find at the top of this file: a moved directory, a renamed path, or a prune" >&2
  echo "  that grew. (commercial/ is pruned DELIBERATELY — it is Cloudflare D1, not Postgres —" >&2
  echo "  and has its own gate, task lint:d1-migrations.)" >&2
  exit 2
else
  # The migration body is read into a variable FIRST, and the read is checked. As a
  # command-substitution ARGUMENT — `scan_sql "$(basename "$f")" "$(cat "$f")"` — cat's
  # exit status is discarded by bash no matter what `set -e` says, so an unreadable
  # migration was linted as the EMPTY STRING: it passed every destructive-statement
  # check and the gate printed "migrations are expand-contract online-safe". Measured
  # 2026-08-01 with a migration holding `ALTER TABLE users DROP COLUMN email;` and mode
  # 000: readable it failed and named the file, unreadable it passed with exit 0.
  for f in "${files[@]}"; do
    body="$(cat "${f}")" || {
      echo "check-migrations: could not read ${f}; the migration is UNVERIFIED." >&2
      exit 2
    }
    if [ -s "${f}" ] && [ -z "${body}" ]; then
      echo "check-migrations: read nothing from the non-empty file ${f}; UNVERIFIED." >&2
      exit 2
    fi
    if ! scan_sql "$(basename "${f}")" "${body}" "${f#./}"; then
      rc=1
    fi
  done
fi

if [ "${rc}" -ne 0 ]; then
  echo "✗ migration online-safety check FAILED"
  exit 1
fi
echo "✓ ${#files[@]} migration(s) are expand-contract online-safe"
