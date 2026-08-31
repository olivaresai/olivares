#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-d1-migrations.sh — two migrations that create the SAME TABLE do not conflict in git, and
# `CREATE TABLE IF NOT EXISTS` turns the collision into a silent no-op. This refuses that shape.
#
# WHY THIS EXISTS, and it cost most of 2026-08-08. Three unmerged branches carried a `0006`, and
# two of them (#591 and the hub's) created `dodo_cohort_fragments` with INCOMPATIBLE primary keys
# — `webhook_id` against `(business_id, subscription_id, event_timestamp, kind)`. Every instrument
# we had said there was nothing wrong:
#
#   git merge-tree  reported SEVEN conflicts and the two 0006 files were NOT among them: to git
#                   they are different files, so they land side by side.
#   tsc             silent. The schemas differ; the TypeScript does not.
#   the gate        `scripts/check-migrations.sh` prunes `./commercial/*` DELIBERATELY and with a
#                   written reason (`:104-110`): that tree is Cloudflare D1, whose SQLite schema
#                   does not follow the engine's Postgres expand-contract rules. That pruning is
#                   CORRECT and stays — the first version of this request asked to remove it, and
#                   removing it would apply Postgres rules to SQLite. It was withdrawn after
#                   reading the code, before being made. This file is the narrow check instead.
#
# THE FAILURE MODE IS THE QUIET ONE. `wrangler` records applied migrations by FULL FILENAME
# (`name TEXT UNIQUE`), so two files sharing the `NNNN_` prefix BOTH apply and BOTH get recorded;
# the tiebreak between equal prefixes is `readdir` order, which is not sorted. Reproduced against
# node:sqlite over the merged tree: one order fails loudly on "no such column: billing_period",
# and the other applies everything with NO error, because the second `CREATE TABLE IF NOT EXISTS`
# is a no-op over the rival's schema — then breaks at the first real webhook, in production, with
# the migration ledger insisting it was applied.
#
# AND RENUMBERING DOES NOT FIX IT. The ledger stores the name, so renaming an applied migration is
# not a rename: it is a new, unapplied migration. Renumbering is only ever correct when the two
# files share NO OBJECT — measured across the four open PRs, exactly one object collision existed.
#
# WHAT IT DELIBERATELY DOES NOT DO. It does not parse SQL. Checks 1-3 answer questions about the
# migration directory AS IT STANDS, which is the state a merge produces, and they cannot see two
# branches before they converge. That is the honest limit: they catch the collision at the moment
# it becomes real, not before. Check 4 closes the one residual of that limit — a rename of an
# already-published migration, which leaves 1-3 correctly clean and still breaks in silence.
#
# Exit 0 = CLEAN. Exit 1 = a collision, each named. Exit 2 = COULD NOT LOOK. Never a silent green.
set -u
set -o pipefail
export LC_ALL=C

DIR="${OLIVARES_D1_MIGRATIONS_DIR:-commercial/license-worker/migrations}"

say() { printf '%s\n' "$*"; }
cannot_look() {
	say "check-d1-migrations: COULD NOT LOOK — $1" >&2
	say "check-d1-migrations: this is not a clean verdict." >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--dir) DIR="${2:-}"; shift 2 || cannot_look "--dir given without a value" ;;
	--base) BASE_REF="${2:-}"; shift 2 || cannot_look "--base given without a value" ;;
	*) cannot_look "unknown argument: $1" ;;
	esac
done

command -v git >/dev/null 2>&1 || cannot_look "no git on PATH"
if ROOT="$(git rev-parse --show-toplevel 2>/dev/null)"; then cd "$ROOT" || cannot_look "cannot enter $ROOT"; fi
[ -d "$DIR" ] || cannot_look "$DIR is not a directory; this gate has nothing to grade"

FILES=""
for f in "$DIR"/*.sql; do
	[ -f "$f" ] || continue
	[ -r "$f" ] || cannot_look "$f exists and is unreadable"
	FILES="${FILES}${f}"$'\n'
done

N=$(printf '%s' "$FILES" | grep -c . || true)
# ZERO UNITS IS RED, NOT GREEN. A checker that graded nothing and printed CLEAN is the instrument
# this repository keeps finding: "clean" meaning "I did not look". If the directory is empty the
# glob did not match and something moved.
[ "$N" -gt 0 ] || cannot_look "no .sql files under $DIR; a gate that measured zero units must not report clean"

FINDINGS=0

# ---- 1 · two files sharing the NNNN_ prefix ------------------------------------------------
# The ledger keys on the full name, so these do not collide there — they collide in READDIR
# ORDER, which is unspecified. One order fails loudly, the other silently.
PREFIXES=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	b=$(basename -- "$f")
	p=$(printf '%s' "$b" | grep -oE '^[0-9]+' || true)
	[ -n "$p" ] || continue
	PREFIXES="${PREFIXES}${p} ${b}"$'\n'
done <<EOF
$FILES
EOF

for p in $(printf '%s' "$PREFIXES" | awk '{print $1}' | sort -u); do
	owners=$(printf '%s' "$PREFIXES" | awk -v k="$p" '$1==k {print $2}')
	c=$(printf '%s' "$owners" | grep -c . || true)
	if [ "$c" -gt 1 ]; then
		FINDINGS=$((FINDINGS + 1))
		say "check-d1-migrations: DUPLICATE PREFIX — ${p} is claimed by ${c} files:" >&2
		printf '%s\n' "$owners" | sed 's/^/    /' >&2
		say "  wrangler keys d1_migrations on the FULL NAME, so both apply and both are recorded." >&2
		say "  Their order is readdir order, which is not sorted: one ordering fails loudly and the" >&2
		say "  other passes in silence. Renumbering is only safe if they share NO OBJECT — check 2." >&2
	fi
done

# ---- 2 · two files creating the SAME TABLE --------------------------------------------------
# This is the one that matters and the one no other instrument sees. Newlines are folded first
# because `CREATE TABLE\n  name (` is the normal formatting in this tree.
TABLES=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	b=$(basename -- "$f")
	for t in $(tr '\n' ' ' < "$f" | tr -s ' ' |
		grep -oiE 'CREATE TABLE (IF NOT EXISTS )?[A-Za-z_][A-Za-z_0-9]*' |
		awk '{print tolower($NF)}' | sort -u); do
		TABLES="${TABLES}${t} ${b}"$'\n'
	done
done <<EOF
$FILES
EOF

for t in $(printf '%s' "$TABLES" | awk '{print $1}' | sort -u); do
	owners=$(printf '%s' "$TABLES" | awk -v k="$t" '$1==k {print $2}' | sort -u)
	c=$(printf '%s' "$owners" | grep -c . || true)
	[ "$c" -gt 1 ] || continue
	FINDINGS=$((FINDINGS + 1))
	say "check-d1-migrations: TABLE CREATED TWICE — ${t} is created by ${c} files:" >&2
	printf '%s\n' "$owners" | sed 's/^/    /' >&2
	say "  Two definitions of one table cannot both be right, and git will not say so: to it these" >&2
	say "  are different files with no overlapping lines. Decide which definition survives and port" >&2
	say "  what the other adds — renumbering does NOT resolve this, it only hides it." >&2

	# ---- 3 · IF NOT EXISTS is the silencer, and only HERE is it a finding ------------------
	# N asked for this as a rule of its own: every unjustified `CREATE TABLE IF NOT EXISTS`.
	# MEASURED BEFORE NARROWING IT, the way this repository decides such things: on main, ALL
	# FOUR tables in the directory use it (0001 x3, 0003 x1). A universal rule would therefore
	# open with four findings on a correct tree, and a gate that cries every run is a gate
	# nobody reads -- this repository's own words, and the reason rule 4b was retired from
	# check-session-numbers.
	#
	# The narrowing keeps the intent exactly: IF NOT EXISTS is harmless when ONE file creates
	# the table, and it is the silencer only when a SECOND one does. So it is reported here,
	# where it changes a loud failure into a mute one, and nowhere else.
	for o in $owners; do
		if tr '\n' ' ' < "$DIR/$o" | tr -s ' ' |
			grep -qiE "CREATE TABLE IF NOT EXISTS ${t}([^A-Za-z_0-9]|$)"; then
			say "    ⛔ $o uses CREATE TABLE IF NOT EXISTS for ${t}: on a tree where another file" >&2
			say "       already created it, this does not fail — it does NOTHING, is recorded as" >&2
			say "       applied, and breaks later against the other file's schema." >&2
		fi
	done
done

# ---- 4 · a PUBLISHED migration was RENAMED ---------------------------------------------------
# The residual of checks 1-3, brought with its number: they see the clash when two branches
# CONVERGE, and this path leaves them CLEAN while still breaking in silence.
#
#   0006_x.sql is applied. d1_migrations stores the FULL NAME -- measured against the sandbox on
#   2026-08-08: five rows, 0001_init.sql through 0005_dodo_delivery_ledger.sql, the last applied
#   on 08-05. Someone renames it to 0013_x.sql and deletes the old one. Now ONE file creates the
#   table, so checks 2 and 3 are correctly clean. At the next `migrations apply` the ledger does
#   not know 0013_x.sql, so it RUNS it; the table already exists, so CREATE TABLE IF NOT EXISTS
#   does nothing, without error; and if the renamed schema differs from the live one, the
#   database keeps the old shape and nobody is told.
#
# FIVE files are in that class today -- the five applied ones -- and it grows with every deploy.
#
# It is decidable statically, which is what makes it worth having: a rename RELATIVE TO THE
# PUBLISHED TREE. A published migration is never renamed, because renaming one is not editing a
# migration, it is creating a different one that the ledger has never seen.
#
# THE DISTINCTION THAT MAKES THIS SAFE, verified before writing it: renumbering a migration that
# exists only in a BRANCH is an ADD against main, not a rename. Measured on PR #598, whose 0006
# and 0007 are exactly that case -- `git diff -M --name-status origin/main` reports them `A`.
# Renaming a published one (0005) reports `R100`. So the merge order this repository adjudicated,
# which renumbers #598 before landing it, does NOT trip this rule. A gate that forbade the plan
# it was written alongside would be wrong about one of the two.
#
# ⚠ CHECKS 4 AND 5 BOTH NEED THE BASE, AND UNTIL 2026-08-08 NOT HAVING IT WAS SILENT. An
# adversarial contrast measured three ways out of this rule, all of them ending in exit 0:
#
#   (i)   `git rev-parse --verify -q origin/main` failing made the whole `if` fall through, and the
#         gate still printed CLEAN. This is not hypothetical: `actions/checkout` without `ref:`
#         leaves NO `origin/main` in the runner, so in CI — the one place this must hold — both
#         base-dependent rules were skipped every single run and nobody could tell from the output.
#   (ii)  `OLIVARES_D1_SKIP_RENAME_CHECK` turned them off with no trace in the verdict.
#   (iii) `git diff … || true` turned any git failure into an empty rename list, i.e. into "no
#         renames found", which is the same sentence as a clean tree.
#
# All three now answer the third answer instead of the first: an unresolvable base is COULD NOT
# LOOK (exit 2), the env override is honoured but SAID OUT LOUD, and git's status is read. A gate
# that cannot see its base has not checked anything against it, and this file's own doctrine at
# :76 already says zero units is red — it just did not apply it to itself.
BASE="${BASE_REF:-origin/main}"
if [ -n "${OLIVARES_D1_SKIP_RENAME_CHECK:-}" ]; then
	say "check-d1-migrations: ⚠ base-dependent checks 4 and 5 DISABLED by OLIVARES_D1_SKIP_RENAME_CHECK." >&2
	say "  A published-migration rename and a below-max add are NOT covered by this run." >&2
elif ! git rev-parse --verify -q "$BASE" >/dev/null 2>&1; then
	# NOT an immediate death, and the difference is the whole point of having three answers rather
	# than two. Rules 1-3 need no base and DID run: a duplicate prefix is a duplicate prefix whether
	# or not history is reachable, and reporting it as "could not look" would lose a real finding.
	# So the base failure is REMEMBERED and settled at the bottom: findings still exit 1, and only
	# a tree with nothing found is refused, because that is the verdict that would be a lie.
	BASE_UNVERIFIED=1
	say "check-d1-migrations: ⚠ the base ref '$BASE' does not resolve — checks 4 and 5 measured NOTHING." >&2
	say "  This is what actions/checkout without 'ref:' produces: no origin/main in the runner, so" >&2
	say "  the two rules that need history were skipped on every CI run while the gate said CLEAN." >&2
	say "  repair: git fetch --no-tags origin main, or pass --base <ref>." >&2
fi
if [ -z "${OLIVARES_D1_SKIP_RENAME_CHECK:-}" ] && [ -z "${BASE_UNVERIFIED:-}" ]; then
	renames=$(git diff -M --name-status --diff-filter=R "$BASE" HEAD -- "$DIR" 2>/dev/null) ||
		cannot_look "'git diff -M --diff-filter=R $BASE HEAD' failed; an empty rename list and a
  failed one print the same nothing, so this is not read as 'no renames'."
	while IFS=$'\t' read -r st old new; do
		case "$st" in R*) ;; *) continue ;; esac
		[ -n "${new:-}" ] || continue
		FINDINGS=$((FINDINGS + 1))
		say "check-d1-migrations: PUBLISHED MIGRATION RENAMED — $(basename -- "$old") -> $(basename -- "$new")" >&2
		say "  d1_migrations keys on the FULL FILENAME, so this is not a rename to wrangler: the old" >&2
		say "  name stays recorded as applied and the new one is an UNAPPLIED migration it will run." >&2
		say "  If the table already exists, CREATE TABLE IF NOT EXISTS does nothing, without error," >&2
		say "  and a differing schema is silently kept at the old shape." >&2
		say "  repair: leave the published file alone and add a NEW migration that alters what you" >&2
		say "          meant to change. Renumbering is only for migrations that have never shipped." >&2
	done <<EOF
$renames
EOF
fi

# ---- 5 · a NEW migration numbered BELOW one already published --------------------------------
# FOUND BY USING THIS GATE ON A REAL CASE, 2026-08-08, and it answered CLEAN. Integrating PR #539
# (`0006_crl_attestations.sql`) against a main whose directory is 0001-0005 plus 0014 and 0015:
# checks 1-3 are correctly clean — the prefix 0006 is unique and no table is created twice — and
# check 4 sees an ADD, not a rename, which is also correct. Every rule in this file said yes to a
# migration that cannot run where it claims to.
#
# WHY IT BREAKS, and it is the ledger again, from the other side. wrangler applies the UNAPPLIED
# migrations in sorted order. 0014 and 0015 are recorded; 0006 is not, so it is picked up and run
# NEXT — that is, AFTER the two files that sort above it have already changed the schema. Its SQL
# was authored against the 0005 state and executes against the 0015 one. Nothing errors at the
# filename level, and whether it errors at all depends on whether the two schemas happen to be
# compatible: the quiet outcome again.
#
# THE RULE IS ABOUT THE ADD, NOT ABOUT THE GAP. Gaps in the published sequence are fine and this
# repository has one (0005 -> 0014). What is refused is ADDING a file that sorts below what the
# base already carries, because sort position is the only ordering wrangler has and the author
# cannot have written for a state that will already be past.
#
# It does not fire on the ordinary case: base at 0005, branch adds 0006, 6 > 5. It does not fire
# on the merge-order plan either — renumbering a branch-only migration UP is exactly what that
# plan does. The direction is the whole rule.
# The base is validated ONCE, above, with the three fail-open paths closed there. Rule 5 rides the
# same gate: it had its own copy of the same silent `rev-parse … && ` and therefore its own copy of
# the same hole. One guard, one verdict, one place to get it wrong.
if [ -z "${OLIVARES_D1_SKIP_RENAME_CHECK:-}" ] && [ -z "${BASE_UNVERIFIED:-}" ]; then
	base_max=0
	base_max_name=""
	base_listing=$(git ls-tree -r --name-only "$BASE" -- "$DIR" 2>/dev/null) ||
		cannot_look "'git ls-tree $BASE -- $DIR' failed; the published maximum is UNKNOWN, and an
  unknown maximum silently disables check 5 rather than failing it."
	while IFS= read -r bf; do
		[ -n "$bf" ] || continue
		bb=$(basename -- "$bf")
		bp=$(printf '%s' "$bb" | grep -oE '^[0-9]+' || true)
		[ -n "$bp" ] || continue
		bpn=$((10#$bp))
		if [ "$bpn" -gt "$base_max" ]; then base_max=$bpn; base_max_name=$bb; fi
	done <<EOF
$(printf '%s\n' "$base_listing" | grep '\.sql$' || true)
EOF

	if [ "$base_max" -gt 0 ]; then
		added=$(git diff -M --name-status --diff-filter=A "$BASE" HEAD -- "$DIR" 2>/dev/null) ||
			cannot_look "'git diff --diff-filter=A $BASE HEAD' failed; no adds and a failed listing
  of adds are the same empty string, and only one of them is clean."
		while IFS=$'\t' read -r st path; do
			case "$st" in A*) ;; *) continue ;; esac
			[ -n "${path:-}" ] || continue
			nb=$(basename -- "$path")
			np=$(printf '%s' "$nb" | grep -oE '^[0-9]+' || true)
			[ -n "$np" ] || continue
			[ "$((10#$np))" -lt "$base_max" ] || continue
			FINDINGS=$((FINDINGS + 1))
			say "check-d1-migrations: OUT-OF-ORDER NEW MIGRATION — $nb sorts below the published $base_max_name" >&2
			say "  wrangler runs the UNAPPLIED migrations in sorted order, and $base_max_name is already" >&2
			say "  recorded as applied. So $nb does not run before it — it runs AFTER, against a schema" >&2
			say "  its SQL was never written for. Nothing errors at the filename level and whether it" >&2
			say "  errors at all depends on whether the two schemas happen to be compatible." >&2
			say "  repair: renumber $nb ABOVE $base_max_name. It has never shipped, so renumbering it is" >&2
			say "          an add and not a rename — which is exactly what check 4 permits." >&2
		done <<EOF
$added
EOF
	fi
fi

if [ "$FINDINGS" -ne 0 ]; then
	say "check-d1-migrations: DIRTY — $FINDINGS collision(s) across $N migration(s) in $DIR." >&2
	[ -z "${BASE_UNVERIFIED:-}" ] || say "  (and checks 4 and 5 never ran: the base did not resolve.)" >&2
	exit 1
fi

# NOTHING FOUND PLUS A BASE WE COULD NOT READ IS NOT CLEAN. Two of the five rules did not run, so
# "no collisions" is a claim about three rules wearing the clothes of five.
[ -z "${BASE_UNVERIFIED:-}" ] || cannot_look "checks 1-3 found nothing, but 4 and 5 never ran
  because the base did not resolve. Three rules out of five is not a clean verdict."

say "check-d1-migrations: CLEAN — $N migration(s) in $DIR, no duplicate prefix, no table created twice,"
say "  and nothing new numbered below what is already published."
exit 0
