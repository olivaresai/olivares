#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-d1-migrations.sh — prove the D1 collision gate by MUTATION, in BOTH directions.
#
# ⚠ THE COUNTERFACTUAL IS THE POINT, and it was asked for by name: fabricate the real 0006 clash
# and SEE IT RED before believing anything. A check that has not seen its own case has proved
# nothing. The fixtures below are the two real migrations reduced to the part that collides —
# `dodo_cohort_fragments` with incompatible primary keys, both under `IF NOT EXISTS`.
#
# The second-most important case is the one that would have prevented a wrong instruction: after
# RENUMBERING, the prefix clash is gone and the TABLE is still created twice. The gate must stay
# red. The first advice given on this incident was "decide the order and renumber the second one",
# and it was withdrawn precisely because renumbering converts a loud failure into a mute one.
set -u -o pipefail

# This battery builds throwaway git repositories, and an inherited GIT_DIR OUTRANKS `-C`: from a
# LINKED worktree git exports it into every hook, so without this the scenarios below would drive
# the LIVE repository instead. Measured 2026-08-06 on another member of this class — it left a
# real branch pointing at a fixture commit. `lint:git-env` derives the class from the pairing of
# `mktemp -d` with a git call and refuses a push without the sanitiser; it caught this file, which
# is the second time today it caught a new script nobody would have added to a hand-written list.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
export LC_ALL=C

HERE=$(cd -- "$(dirname -- "$0")" && pwd)
SUT="$HERE/check-d1-migrations.sh"
[ -r "$SUT" ] || { echo "test-d1-migrations: cannot read $SUT" >&2; exit 2; }

# Every scenario used to call bare `mktemp -d` and none removed its directory: 34 entries per
# run, multiplied by every concurrent pre-push. Keep one owned root and remove it on every normal
# exit; scripts/with-clean-tmp.sh is the enclosing abnormal-exit backstop in the Taskfile.
RUN_TMP="$(mktemp -d "${TMPDIR:-/tmp}/d1-migrations.XXXXXX")" || exit 2
cleanup() { chmod -R u+rwX "$RUN_TMP" 2>/dev/null; rm -rf -- "$RUN_TMP"; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

PASS=0; FAIL=0; START=$SECONDS

mkdir_d() { mktemp -d "$RUN_TMP/case.XXXXXX"; }
put() { mkdir -p "$(dirname -- "$1/$2")"; printf '%s\n' "$3" > "$1/$2"; }
# run() drives the RULES THAT NEED NO HISTORY (1-3) and says so to the subject. Since 2026-08-08 an
# unresolvable base is no longer a silent skip — it is COULD NOT LOOK — so a case about duplicate
# prefixes run in a throwaway directory with no origin/main would otherwise die on the base and
# measure nothing. Setting the documented override keeps each case about what it is about, AND
# exercises the "disabled, and said out loud" path on every single one of them.
run() { # run <dir> <want-rc> <label>
	local d="$1" want="$2" label="$3" out rc
	out=$(OLIVARES_D1_SKIP_RENAME_CHECK=1 bash "$SUT" --dir "$d" 2>&1); rc=$?
	if [ "$rc" -eq "$want" ]; then
		PASS=$((PASS + 1)); printf 'ok   %-60s rc=%d\n' "$label" "$rc"
	else
		FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d want rc=%d\n' "$label" "$rc" "$want"
		printf '     %s\n' "$out"
	fi
}

FRAGMENTS_A='CREATE TABLE IF NOT EXISTS dodo_cohort_fragments (
  webhook_id       TEXT PRIMARY KEY,
  business_id      TEXT NOT NULL,
  billing_period   TEXT NOT NULL
);'
FRAGMENTS_B='CREATE TABLE IF NOT EXISTS dodo_cohort_fragments (
  business_id      TEXT NOT NULL,
  subscription_id  TEXT NOT NULL,
  event_timestamp  TEXT NOT NULL,
  kind             TEXT NOT NULL,
  PRIMARY KEY (business_id, subscription_id, event_timestamp, kind)
);'

# ------------------------------------------------------------------------ control
d=$(mkdir_d)
put "$d" 0001_init.sql 'CREATE TABLE IF NOT EXISTS licenses (id TEXT PRIMARY KEY);'
put "$d" 0002_index.sql 'CREATE INDEX IF NOT EXISTS idx_licenses_id ON licenses (id);'
run "$d" 0 "an ordinary directory is clean"

# IF NOT EXISTS on a table only ONE file creates is ordinary and must stay clean. Measured on main
# before narrowing the rule: all four tables in the real directory use it, so a universal rule
# would open with four findings on a correct tree.
d=$(mkdir_d)
put "$d" 0001_init.sql "$FRAGMENTS_A"
run "$d" 0 "IF NOT EXISTS with a single creator is not a finding"

# ------------------------------------------------------------- THE COUNTERFACTUAL
d=$(mkdir_d)
put "$d" 0006_dodo_fulfillment.sql "$FRAGMENTS_A"
put "$d" 0006_dodo_cohort_barrier.sql "$FRAGMENTS_B"
run "$d" 1 "THE REAL CLASH: two 0006 creating dodo_cohort_fragments"

# THE ONE THAT WOULD HAVE STOPPED A WRONG INSTRUCTION. Renumbering removes the prefix clash and
# leaves the table created twice: the loud failure becomes a mute one, and the gate must still be
# red. If this case ever goes green, the gate has started endorsing the withdrawn advice.
d=$(mkdir_d)
put "$d" 0006_dodo_fulfillment.sql "$FRAGMENTS_A"
put "$d" 0013_dodo_cohort_barrier.sql "$FRAGMENTS_B"
run "$d" 1 "RENUMBERING does not fix it: the table is still created twice"

# The prefix clash alone, with no shared object, is still worth a finding: readdir order is
# unspecified, so which one runs first is not decided by anything a human wrote.
d=$(mkdir_d)
put "$d" 0006_a.sql 'CREATE TABLE IF NOT EXISTS alpha (id TEXT PRIMARY KEY);'
put "$d" 0006_b.sql 'CREATE TABLE IF NOT EXISTS beta (id TEXT PRIMARY KEY);'
run "$d" 1 "a DUPLICATE PREFIX alone is a finding (readdir order is unspecified)"

# ...and the mirror: different prefixes, different tables, nothing to report. Without this the two
# cases above would also pass a gate that simply always failed.
d=$(mkdir_d)
put "$d" 0006_a.sql 'CREATE TABLE IF NOT EXISTS alpha (id TEXT PRIMARY KEY);'
put "$d" 0007_b.sql 'CREATE TABLE IF NOT EXISTS beta (id TEXT PRIMARY KEY);'
run "$d" 0 "different prefixes creating different tables is clean"

# Newlines between CREATE TABLE and the name are the normal formatting in the real files. A
# line-oriented matcher misses every one of them and reports a clean tree.
d=$(mkdir_d)
put "$d" 0006_a.sql 'CREATE TABLE IF NOT EXISTS
  gamma (
  id TEXT PRIMARY KEY
);'
put "$d" 0007_b.sql 'CREATE TABLE
  gamma (
  id TEXT NOT NULL
);'
run "$d" 1 "a table name on the NEXT LINE is still the same table"

# Case must not decide identity: SQL is case-insensitive for keywords and these files mix.
d=$(mkdir_d)
put "$d" 0006_a.sql 'create table if not exists delta (id TEXT PRIMARY KEY);'
put "$d" 0007_b.sql 'CREATE TABLE Delta (id TEXT NOT NULL);'
run "$d" 1 "case does not hide a duplicate table"

# A prefix-sharing name must not be read as the same table.
d=$(mkdir_d)
put "$d" 0006_a.sql 'CREATE TABLE IF NOT EXISTS grants (id TEXT PRIMARY KEY);'
put "$d" 0007_b.sql 'CREATE TABLE IF NOT EXISTS grants_history (id TEXT PRIMARY KEY);'
run "$d" 0 "grants_history is not grants"

# -------------------------------------------- 4 · a PUBLISHED migration was RENAMED
# The residual of the checks above, brought with its number: they see the clash when two branches
# CONVERGE, and this path leaves them clean while still breaking in silence. Rename an applied
# migration and delete the old one, and ONE file creates the table -- correctly clean by checks 2
# and 3 -- but the ledger does not know the new name, runs it, and CREATE TABLE IF NOT EXISTS does
# nothing over the existing table. Five files are in that class today, and it grows every deploy.
#
# THE DISTINCTION THIS PAIR EXISTS TO PROVE, and it decides whether the rule is safe: renumbering
# a migration that lives only in a BRANCH is an ADD against main, not a rename. PR #598 is exactly
# that, and the merge order this repository adjudicated renumbers it before landing. If this gate
# flagged that, the gate and the plan could not both be right.
mkrepo_pub() { # a repo with a published migration directory and a branch on top
	local d; d=$(mkdir_d) || return 1
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	git -C "$d" config user.email t@example.invalid
	git -C "$d" config user.name t
	git -C "$d" config commit.gpgsign false
	mkdir -p "$d/migrations"
	printf 'CREATE TABLE IF NOT EXISTS licenses (id TEXT PRIMARY KEY);\n' > "$d/migrations/0005_published.sql"
	git -C "$d" add -A >/dev/null 2>&1
	git -C "$d" commit -q -m published --no-verify >/dev/null 2>&1
	git -C "$d" branch -q -f published-base
	printf '%s' "$d"
}
runbase() { # runbase <repo> <want-rc> <label>
	local d="$1" want="$2" label="$3" out rc
	out=$(cd "$d" && bash "$SUT" --dir migrations --base published-base 2>&1); rc=$?
	if [ "$rc" -eq "$want" ]; then
		PASS=$((PASS + 1)); printf 'ok   %-60s rc=%d\n' "$label" "$rc"
	else
		FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d want rc=%d\n' "$label" "$rc" "$want"
		printf '     %s\n' "$out"
	fi
}

d=$(mkrepo_pub)
git -C "$d" mv migrations/0005_published.sql migrations/0013_published.sql >/dev/null 2>&1
git -C "$d" commit -q -m 'rename an applied migration' --no-verify >/dev/null 2>&1
runbase "$d" 1 "RENAMING a PUBLISHED migration is caught"

d=$(mkrepo_pub)
printf 'CREATE TABLE IF NOT EXISTS grants (id TEXT PRIMARY KEY);\n' > "$d/migrations/0006_branch_only.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m 'add' --no-verify >/dev/null 2>&1
git -C "$d" mv migrations/0006_branch_only.sql migrations/0014_branch_only.sql >/dev/null 2>&1
git -C "$d" commit -q -m 'renumber a branch-only migration' --no-verify >/dev/null 2>&1
runbase "$d" 0 "RENUMBERING a BRANCH-ONLY migration is an ADD, not a rename"

# ------------------------------------------------- 5 · new migration BELOW the published max
# THIS RULE EXISTS BECAUSE THE GATE ANSWERED CLEAN ON A REAL ONE. Integrating #539 against a main
# holding 0001-0005 plus 0014 and 0015, its `0006_crl_attestations.sql` passed every earlier check:
# the prefix is unique (1), no table is created twice (2, 3), and it is an ADD and not a rename (4).
# wrangler runs the unapplied set in sorted order, so 0006 runs AFTER 0014 and 0015 are already in.
#
# The gap in the published sequence is NOT the finding — this repository has one and it is fine.
# The finding is the DIRECTION of the add. Both directions are pinned below, because a rule tested
# on one side only is half a rule, and the inverted comparison is a mutant that compiles.
mkrepo_gap() { # published 0005 AND 0015, i.e. a real gap, with a branch on top
	local d; d=$(mkdir_d) || return 1
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	git -C "$d" config user.email t@example.invalid
	git -C "$d" config user.name t
	git -C "$d" config commit.gpgsign false
	mkdir -p "$d/migrations"
	printf 'CREATE TABLE IF NOT EXISTS licenses (id TEXT PRIMARY KEY);\n' > "$d/migrations/0005_published.sql"
	printf 'ALTER TABLE licenses ADD COLUMN tier TEXT;\n' > "$d/migrations/0015_published_later.sql"
	git -C "$d" add -A >/dev/null 2>&1
	git -C "$d" commit -q -m published --no-verify >/dev/null 2>&1
	git -C "$d" branch -q -f published-base
	printf '%s' "$d"
}

d=$(mkrepo_gap)
printf 'CREATE TABLE IF NOT EXISTS crl_attestations (id TEXT PRIMARY KEY);\n' > "$d/migrations/0006_below.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
runbase "$d" 1 "a NEW migration numbered BELOW the published max is caught"

d=$(mkrepo_gap)
printf 'CREATE TABLE IF NOT EXISTS crl_attestations (id TEXT PRIMARY KEY);\n' > "$d/migrations/0016_above.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
runbase "$d" 0 "a NEW migration ABOVE the published max is fine (no false positive)"

d=$(mkrepo_gap)
printf 'CREATE TABLE IF NOT EXISTS a (id TEXT);\n' > "$d/migrations/0016_a.sql"
printf 'CREATE TABLE IF NOT EXISTS b (id TEXT);\n' > "$d/migrations/0007_b.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
runbase "$d" 1 "one good add does not excuse a below-max sibling in the same change"

# ------------------------------------------- the base itself: three ways this used to fail OPEN
# An adversarial contrast measured all three ending in exit 0, and the CI one was not hypothetical:
# actions/checkout without `ref:` leaves no origin/main, so checks 4 and 5 were skipped on every
# run while the gate printed CLEAN. Each is pinned here in the direction that matters.
d=$(mkrepo_gap)
git -C "$d" branch -q -D published-base 2>/dev/null
printf 'CREATE TABLE IF NOT EXISTS solo (id TEXT);\n' > "$d/migrations/0016_ok.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
out=$(cd "$d" && bash "$SUT" --dir migrations --base no/such/ref 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-60s rc=2\n' "an UNRESOLVABLE base over a clean tree is COULD NOT LOOK"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d want rc=2\n' "an UNRESOLVABLE base over a clean tree is COULD NOT LOOK" "$rc"
	printf '     %s\n' "$out"
fi

# ...but a REAL finding from rules 1-3 still outranks it: reporting a duplicate prefix as "could not
# look" would lose a collision that is true regardless of history.
d=$(mkrepo_gap)
printf 'CREATE TABLE IF NOT EXISTS dup (id TEXT);\n' > "$d/migrations/0016_a.sql"
printf 'CREATE TABLE IF NOT EXISTS dup (id TEXT);\n' > "$d/migrations/0016_b.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
out=$(cd "$d" && bash "$SUT" --dir migrations --base no/such/ref 2>&1); rc=$?
if [ "$rc" -eq 1 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-60s rc=1\n' "a rules-1-3 finding outranks an unreadable base"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d want rc=1\n' "a rules-1-3 finding outranks an unreadable base" "$rc"
	printf '     %s\n' "$out"
fi

# the env override is honoured, and the verdict SAYS it was.
d=$(mkrepo_gap)
printf 'CREATE TABLE IF NOT EXISTS x (id TEXT);\n' > "$d/migrations/0006_below.sql"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -q -m add --no-verify >/dev/null 2>&1
out=$(cd "$d" && OLIVARES_D1_SKIP_RENAME_CHECK=1 bash "$SUT" --dir migrations --base published-base 2>&1); rc=$?
case "$rc:$out" in
0:*DISABLED*) PASS=$((PASS + 1)); printf 'ok   %-60s rc=0\n' "the env override is honoured AND announced" ;;
*) FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d, announced=%s\n' "the env override is honoured AND announced" "$rc" "$(printf '%s' "$out" | grep -c DISABLED)" ;;
esac

# ---------------------------------------------------------------- could-not-look
d=$(mkdir_d)
run "$d" 2 "an EMPTY directory is COULD NOT LOOK, never clean"

out=$(bash "$SUT" --dir /nonexistent/nope 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then
	PASS=$((PASS + 1)); printf 'ok   %-60s rc=2\n' "a MISSING directory is COULD NOT LOOK"
else
	FAIL=$((FAIL + 1)); printf 'FAIL %-60s got rc=%d want rc=2\n' "a MISSING directory is COULD NOT LOOK" "$rc"
fi

printf '\ncheck-d1-migrations: %d passed, %d failed, %ds wall\n' "$PASS" "$FAIL" "$((SECONDS - START))"
[ "$FAIL" -eq 0 ] || exit 1
