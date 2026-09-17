#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-test-budget-placement.sh — a SHORT test budget must not start before the fixture it
# is not measuring.
#
# CENSUS-SUBJECT: tree
#   Its subject is the working tree's `_test.go` files. Over a tree with none it answers
#   COULD NOT LOOK (2), never CLEAN: a zero found by not looking reads exactly like a zero
#   found by being clean, and this repository's canon (§0-COBERTURA) refuses that trade.
#
# ⛔ WHY THIS EXISTS, and it is a class, not an incident. Three times in one week a required
# CI leg went red with `context deadline exceeded` and NO data race, on three different
# tests, with the same shape every time:
#
#     ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
#     defer cancel()
#     st, err := Open(ctx, cfg, nil)        // the compiled migration plan
#     tenant := provisionTenant(t, st, …)   // more fixture
#     …
#     select {                              // the behaviour under test
#     case <-held:
#     case <-ctx.Done():                    // and THIS is what fires
#         t.Fatalf(…)
#     }
#
# The budget is wall clock. Under `-race` on a contended self-hosted runner the FIXTURE
# spends it, and the test fails at the wait instead of at the behaviour. The three fixes,
# all test-only and all the same remedy — run the setup on an unbounded context and start
# the budget immediately before the behaviour:
#
#     01f81b8e81  TestUserAuthorityBundleWriterRaces                            (race-core p2)
#     4859cc43f3  TestDirectoryEpochSQLiteLifecyclePathsShareGlobalWriterReservation (p4)
#     346bce0c8a  TestSQLiteRowLockerFencesAnotherStoreInstance                 (p4)
#
# rule for the fourth: fix the class, not the instance. That is this file.
#
# ═══════════════════════════════════════════════════════════════════════════════════════
# WHAT THIS GATE DOES NOT CLAIM — read this before citing its green (canon §0-COBERTURA:
# a gate says what its DISCOVERY reaches, not what it checks).
#
#  1. It reads TEXT, not Go types. It sees the fixture heads in the table below; a fixture
#     under a new name is invisible to it BY CONSTRUCTION. Widen the table when a new
#     fixture costs seconds. Discovery also PRUNES `testdata`, `vendor`, `node_modules` and
#     `.git`, and does NOT follow symlinks (`find` without `-L`). A `_test.go` reachable only
#     through a symlinked subtree is therefore not examined AND NOT REFUSED — `find` exits 0
#     over it, unlike the unreadable directory of limit 8, so this one is silent. Measured on
#     this tree 2026-09-16: `find . -path ./.git -prune -o -type l -print` returns **zero**
#     symlinks of any kind. Written down because the unreadable count does not bound it.
#  8. Discovery's own status IS checked: a directory `find` cannot descend, or a `sort` that
#     fails, answers COULD NOT LOOK (2). Both have a battery row and a mutant.
#  2. A budget whose duration it cannot resolve is NOT silently skipped: it is COUNTED and
#     the count is printed on the verdict line, so the green is bounded by what was read.
#     `--unreadable` lists them. Resolvable today: a literal `N*time.Unit` (either operand
#     order), a bare `time.Unit`, and a NAME bound by `NAME = N*time.Unit` somewhere in the
#     same directory (`:=` is not read) — the last one because `core/audit` writes its budgets
#     as a package constant and a rule that cannot read one would have called that package
#     clean without looking at it. Limit 5 states what "somewhere in the same directory"
#     costs.
#  3. The budget must be written on ONE line. A `context.WithTimeout(\n\tctx, 15*time.Second)`
#     is one of the unreadable ones above, counted, not assumed.
#  4. It cannot follow a budget created in one function and used in another, nor one
#     returned by a helper. Those are unreachable to a line reader and are not counted
#     either — this is the one gap the count does not bound. The `go/ast` analyser that DOES
#     see them is not in this repository: it is an analysis instrument kept with its own
#     receipts, in the CONTROL repository (olivares-ai-control), at the path below — on ONE
#     line, because a path a reader cannot select and paste is a citation that still cannot
#     be opened:
#     assessments/engineering/r116-test-budget-sweep/receipts/census-instrument.go.txt
#  5. A duration held in a NAME is resolved from `NAME = <duration>` anywhere in the same
#     DIRECTORY, not only from a package-level `const`, so two bindings of one name in one
#     directory resolve to the last one read. MEASURED on this tree 2026-09-16: eleven such
#     collisions exist — `core/internal/store/sqlstore/|budget` alone is bound to seven
#     values between 2 s and `time.Hour` — and NONE of them is reached, because no budget
#     site resolves its duration through a colliding name today (the 21 identifier-duration
#     budgets use `callerBudget`, `serializationTestTimeout`, `workLaunchAuthorityBound` and
#     the like). It is written down because the unreadable count does NOT bound it: a name
#     that resolves to the wrong binding reads as readable-and-out-of-scope, not as
#     unreadable, so this gap would be silent rather than counted.
#  6. The window closes at the FIRST `select {` or receive after the budget, because that is
#     where the deadline is normally paid. A receive that is not the deadline-bound wait
#     therefore closes it early: `<-ready` between the budget and the fixture makes that
#     fixture invisible to this rule. The rule is deliberately this narrow — row 17 and its
#     mutant record what happened when it closed on ANY `<-`, sends included — but "first
#     receive" is a choice with a cost, and this is the cost. Not fixed here: moving the
#     close to the LAST receive needs its own measurement of the false reds it would arm.
#  7. `OLIVARES_TEST_BUDGET_WINDOW=0` silences the rule from the environment. It is NOT a
#     hidden switch: the window is printed on the header line of every VERDICT run, so a green
#     with `window 0 lines` names itself. (`--unreadable` answers before that line is printed,
#     which is the one path where the knob is not announced.) There is deliberately no
#     exemption override — see the exemption table below.
#
# ⛔ AND IT DOES NOT REQUIRE THE FIXTURE TO TAKE THE ctx. The measured tree refutes that
# narrowing: `TestUserAuthorityBundleWriterRaces` was spent by `f2aFreshTarget(t, engine)`,
# which never receives a context. A deadline is consumed by ELAPSED TIME, not by an
# argument list, so the rule is positional.
# ═══════════════════════════════════════════════════════════════════════════════════════
#
# THE THREE ANSWERS
#   CLEAN (0)          no readable short budget starts before a fixture, and every exemption
#                      matched. The unreadable count is printed beside it.
#   FINDING (1)        at least one does — each is NAMED with its budget, fixture and wait
#                      line; or an exemption no longer matches anything (a stale waiver is a
#                      gate that has quietly stopped covering its case)
#   COULD NOT LOOK (2) the root is unreadable; discovery found no `_test.go` at all;
#                      discovery could not finish listing the tree (a directory it cannot
#                      descend, or a `sort` that failed); or the reader could not read the
#                      files discovery found
#
# USAGE
#   scripts/check-test-budget-placement.sh [root]              # default: the repository root
#   scripts/check-test-budget-placement.sh --unreadable [root] # list the budgets it cannot read
# KNOBS (for the battery; the defaults are the rule)
#   OLIVARES_TEST_BUDGET_MAX_SECONDS  budgets of this many seconds or less are in scope (20)
#   OLIVARES_TEST_BUDGET_WINDOW       lines scanned after a budget before giving up (160)
#
# ⛔ LOCALE FIXED. The comparison is numeric and the report prints durations with `%g`;
# under a comma-decimal locale bash refuses "2.5" and the instrument exits 1 with the
# verdict already written. Same trap `check-test-timeout-headroom.sh` measured on 2026-09-05.
set -uo pipefail
export LC_ALL=C

MODE=verdict
if [ "${1:-}" = "--unreadable" ]; then
	MODE=unreadable
	shift
fi
ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
MAXS="${OLIVARES_TEST_BUDGET_MAX_SECONDS:-20}"
WINDOW="${OLIVARES_TEST_BUDGET_WINDOW:-160}"

say() { printf 'check-test-budget-placement: %s\n' "$*"; }
blind() { printf 'check-test-budget-placement: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

[ -d "$ROOT" ] || blind "$ROOT is not a directory"
cd "$ROOT" || blind "cannot enter $ROOT"

# The five names exist BEFORE the trap so that a failing mktemp cannot make the trap itself
# die on an unbound variable under `set -u` — which would leak the files it exists to remove.
exempt_f=""; hits_f=""; files_f=""; consts_f=""; raw_f=""
trap 'rm -f "$exempt_f" "$hits_f" "$files_f" "$consts_f" "$raw_f"' EXIT
exempt_f="$(mktemp "${TMPDIR:-/tmp}/test-budget-exempt.XXXXXX")" || blind "cannot create a scratch file"
hits_f="$(mktemp "${TMPDIR:-/tmp}/test-budget-hits.XXXXXX")" || blind "cannot create a scratch file"
files_f="$(mktemp "${TMPDIR:-/tmp}/test-budget-files.XXXXXX")" || blind "cannot create a scratch file"
consts_f="$(mktemp "${TMPDIR:-/tmp}/test-budget-consts.XXXXXX")" || blind "cannot create a scratch file"
raw_f="$(mktemp "${TMPDIR:-/tmp}/test-budget-raw.XXXXXX")" || blind "cannot create a scratch file"

# ═══════════════════════════════════════════════════════════════════════════════════════
# REVIEWED EXEMPTIONS — `<path>|<enclosing func>|<reason>`
#
# An entry belongs here only when the expensive call inside the budget IS the behaviour
# under test, so moving the budget would delete the assertion. Each carries the measurement
# that makes the budget safe; a reason without a number is not a reason. A stale entry is a
# FINDING (see above): a waiver that stopped matching is a gate that stopped covering.
# ═══════════════════════════════════════════════════════════════════════════════════════
EXEMPTIONS=(
	"core/internal/store/sqlstore/drcontrolintegrity_test.go|TestDRRecordFIFORefusesThroughActualOpen|Open IS the subject: the test proves Open REFUSES a FIFO record path instead of blocking on it, so the budget must cover Open or there is nothing to assert. The refusal is measured at 0.00 s under -race with real PostgreSQL (2026-09-16; receipt in the CONTROL repository, olivares-ai-control/assessments/engineering/r116-test-budget-sweep/receipts/), i.e. three orders of magnitude of headroom inside the 3 s budget."
)

for e in "${EXEMPTIONS[@]}"; do
	printf '%s\n' "${e%%|*}|$(printf '%s' "$e" | cut -d'|' -f2)"
done > "$exempt_f"

# DISCOVERY. `find`, not `git ls-files`: the battery runs this over throwaway trees that are
# not repositories, and a gate that only works inside a checkout cannot be tested hermetically.
#
# ⛔ AND THE STATUS OF `find` IS CHECKED, which is the same lesson as the awk one below, one
# step earlier. `find` that meets a directory it cannot descend PRINTS THE REST and exits
# NONZERO. This script runs under `set -uo pipefail` WITHOUT `-e`, so a nonzero pipeline is
# a value nobody reads: the gate answered CLEAN over a tree it had not finished listing, and
# a `_test.go` holding the class inside that directory was never examined at all. Discovery
# is therefore its own statement — `find` into a raw file, its status checked, then `sort` —
# rather than one pipeline whose two failures share a single discarded status.
find . -type d \( -name testdata -o -name vendor -o -name node_modules -o -name .git \) -prune -o \
	-type f -name '*_test.go' -print > "$raw_f" || blind "discovery could not read the whole tree under $ROOT"
LC_ALL=C sort "$raw_f" > "$files_f" || blind "cannot sort the discovered file list"
n_files="$(wc -l < "$files_f")"
[ "$n_files" -gt 0 ] || blind "discovery found no _test.go under $ROOT — nothing was examined"

# ⛔ THE FILE LIST REACHES awk AS AN ARRAY, NOT AS AN UNQUOTED EXPANSION, and the status of
# awk is CHECKED. Both halves are the same lesson. An unquoted `$(cat …)` splits on spaces —
# so a path with a space arrives as two arguments — and it also GLOBS, so `a[a]_test.go`
# silently becomes `aa_test.go` (read twice, the real file never read). And awk then reports
# "cannot open" on stderr while the verdict line still says CLEAN: a green over files the
# reader never opened, which is exactly the trade §0-COBERTURA refuses.
mapfile -t files < "$files_f" || blind "cannot read the discovered file list"
[ "${#files[@]}" -eq "$n_files" ] || blind "the discovered file list did not survive reading"

# PASS 1 — package-level duration constants, keyed by directory. See limit 2 in the header.
awk '
	function dir(p,   i) { i = length(p); while (i > 0 && substr(p, i, 1) != "/") i--; return substr(p, 1, i) }
	{
		code = $0
		sub(/\/\/.*$/, "", code)
		if (match(code, /(^|[ \t])[A-Za-z_][A-Za-z0-9_]*[ \t]*=[ \t]*([0-9]+[ \t]*\*[ \t]*time\.[A-Za-z]+|time\.[A-Za-z]+[ \t]*\*[ \t]*[0-9]+|time\.[A-Za-z]+)[ \t]*$/)) {
			line = substr(code, RSTART, RLENGTH)
			name = line; sub(/^[ \t]*/, "", name); sub(/[ \t]*=.*$/, "", name)
			val = line; sub(/^[^=]*=[ \t]*/, "", val); sub(/[ \t]*$/, "", val)
			rel = FILENAME; sub(/^\.\//, "", rel)
			printf "%s|%s\t%s\n", dir(rel), name, val
		}
	}
' "${files[@]}" > "$consts_f" || blind "awk could not read the discovered files (pass 1)"

awk -v maxs="$MAXS" -v window="$WINDOW" -v exf="$exempt_f" -v cof="$consts_f" \
	-v FIXTURE='(^|[^A-Za-z0-9_.])(Open|isolatedPG[A-Za-z0-9_]*|[A-Za-z0-9_]*FreshTarget|provisionTenant[A-Za-z0-9_]*|drOpenSuper|chatTransport|chatPrepared|new[A-Za-z0-9_]*(Harness|Fixture|Target|Estate))\(' '
	BEGIN {
		while ((getline l < exf) > 0) { if (l != "") ex[l] = 1 }
		close(exf)
		while ((getline l < cof) > 0) {
			if (l == "") continue
			k = l; sub(/\t.*$/, "", k)
			v = l; sub(/^[^\t]*\t/, "", v)
			konst[k] = v
		}
		close(cof)
	}
	function dir(p,   i) { i = length(p); while (i > 0 && substr(p, i, 1) != "/") i--; return substr(p, 1, i) }
	function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
	function reset() { armed = 0; ctx = ""; bline = 0; secs = 0; fline = 0; ftxt = "" }
	# seconds(expr) -> the duration in seconds, or -1 when it cannot be read.
	function seconds(e,   n, u, mul) {
		gsub(/[ \t]/, "", e)
		if (e ~ /^[0-9]+\*time\.[A-Za-z]+$/)      { n = e; sub(/\*.*$/, "", n); u = e; sub(/^.*time\./, "", u) }
		else if (e ~ /^time\.[A-Za-z]+\*[0-9]+$/) { n = e; sub(/^.*\*/, "", n); u = e; sub(/^time\./, "", u); sub(/\*.*$/, "", u) }
		else if (e ~ /^time\.[A-Za-z]+$/)         { n = 1; u = e; sub(/^time\./, "", u) }
		else return -1
		if (u == "Nanosecond") mul = 1e-9
		else if (u == "Microsecond") mul = 1e-6
		else if (u == "Millisecond") mul = 1e-3
		else if (u == "Second") mul = 1
		else if (u == "Minute") mul = 60
		else if (u == "Hour") mul = 3600
		else return -1
		return n * mul
	}
	FNR == 1 {
		reset(); fn = "<file scope>"
		rel = FILENAME; sub(/^\.\//, "", rel)
		d = dir(rel)
		delete konstlocal
		for (k in konst) if (index(k, d "|") == 1) { nm = k; sub(/^[^|]*\|/, "", nm); konstlocal[nm] = konst[k] }
	}
	{
		code = $0
		# ⛔ STRINGS AND COMMENTS GO FIRST, and the strings half is not pedantry: a
		# `t.Fatalf("Open(ctx) returned nil store")` between a budget and its wait was
		# reported as "the fixture inside the budget" — a FALSE RED in the fast lane, which
		# in a hook leg poisons the push of every branch on every box.
		gsub(/"([^"\\]|\\.)*"/, "\"\"", code)
		gsub(/`[^`]*`/, "``", code)
		sub(/\/\/.*$/, "", code)

		if (code ~ /^func[ \t]/) {
			reset()
			fn = code
			# `[^)]*` and not `.*`: a greedy receiver match eats up to the LAST ")" on a
			# method line and leaves the body behind, so a finding inside a method would be
			# reported under a garbage name and its exemption key would never match.
			sub(/^func[ \t]+\([^)]*\)[ \t]*/, "", fn)
			sub(/^func[ \t]+/, "", fn)
			sub(/[ \t]*\(.*$/, "", fn)
		}

		if (armed && FNR - bline > window) reset()

		if (armed) {
			# ⛔ THE WINDOW CLOSES ON A RECEIVE OR A `select`, NOT ON ANY "<-". A SEND
			# (`errs <- warmUp()`) and a channel TYPE (`chan<- struct{}`) are not waits, and
			# accepting them disarmed the rule one line before the fixture — measured: a
			# `go func() { errs <- warmUp() }()` above an `Open(ctx…)` went unreported, and
			# 9 of 13 true findings printed a send or a func signature as "the wait".
			probe = code
			gsub(/chan[ \t]*<-/, "CHANSEND", probe)
			gsub(/<-[ \t]*chan/, "CHANRECVTYPE", probe)
			isrecv = (probe ~ /(^|[=(,{;!&|:]|case|return|go|defer)[ \t]*<-/)
			isselect = (probe ~ /(^|[^A-Za-z0-9_])select[ \t]*\{/)
			if (isrecv || isselect) {
				if (fline > 0) {
					key = rel "|" fn
					tag = (key in ex) ? "EXEMPT" : "FINDING"
					if (key in ex) used[key] = 1
					printf "%s\t%s\t%d\t%s\t%s\t%g\t%d\t%s\t%d\t%s\n",
						tag, rel, bline, fn, ctx, secs, fline, ftxt, FNR, trim(code)
				}
				reset()
			} else if (fline == 0 && code ~ FIXTURE) {
				fline = FNR
				ftxt = trim(code)
			}
		}

		if (!armed && match(code, /context\.With(Timeout|Deadline)\(/)) {
			# ⛔ THE DURATION IS FOUND BY PATTERN, NOT BY SPLITTING ON ")". The first draft
			# cut the argument list at the first ")", which on the canonical
			# `context.WithTimeout(context.Background(), 15*time.Second)` is the ")" of
			# Background() — so every budget in the tree became "unreadable" (475 of 475)
			# and the gate armed on nothing while printing a shape of a result.
			rest = substr(code, RSTART + RLENGTH)
			# WithDeadline carries its duration inside time.Now().Add(D).
			if (rest ~ /Add\(/) sub(/^.*Add\(/, "", rest)
			s = -1
			if (match(rest, /[0-9]+[ \t]*\*[ \t]*time\.[A-Za-z]+/) ||
				match(rest, /time\.[A-Za-z]+[ \t]*\*[ \t]*[0-9]+/) ||
				match(rest, /time\.[A-Za-z]+/)) {
				s = seconds(substr(rest, RSTART, RLENGTH))
			} else if (match(rest, /,[ \t]*[A-Za-z_][A-Za-z0-9_]*[ \t]*\)/)) {
				id = substr(rest, RSTART, RLENGTH)
				sub(/^,[ \t]*/, "", id); sub(/[ \t]*\)$/, "", id)
				if (id in konstlocal) s = seconds(konstlocal[id])
			}
			if (s < 0) {
				printf "UNREADABLE\t%s\t%d\t%s\t\t0\t0\t%s\t0\t\n", rel, FNR, fn, trim(code)
			} else if (s <= maxs + 0) {
				c = code
				if (match(c, /[A-Za-z_][A-Za-z0-9_]*[ \t]*,[ \t]*[A-Za-z_][A-Za-z0-9_]*[ \t]*:?=[ \t]*context\.With(Timeout|Deadline)/)) {
					c = substr(c, RSTART, RLENGTH); sub(/[ \t]*,.*$/, "", c); ctx = c
				} else { ctx = "<unnamed>" }
				armed = 1; bline = FNR; secs = s; fline = 0; ftxt = ""
			}
		}
	}
	END { for (k in ex) if (!(k in used)) printf "STALE\t%s\t0\t\t\t0\t0\t\t0\t\n", k }
' "${files[@]}" > "$hits_f" || blind "awk could not read the discovered files (pass 2)"

findings="$(grep -c '^FINDING' "$hits_f")"
exempted="$(grep -c '^EXEMPT' "$hits_f")"
stale="$(grep -c '^STALE' "$hits_f")"
unreadable="$(grep -c '^UNREADABLE' "$hits_f")"

if [ "$MODE" = unreadable ]; then
	say "$unreadable budget(s) whose duration this gate cannot read:"
	grep '^UNREADABLE' "$hits_f" | awk -F'\t' '{printf "    %s:%s  %s\n", $2, $3, $8}'
	exit 0
fi

say "$n_files test file(s) examined · budgets of ${MAXS}s or less · window ${WINDOW} lines"
say "$findings finding(s) · $exempted reviewed exemption(s) matched · $stale stale exemption(s) · $unreadable budget(s) this gate cannot read (--unreadable)"

if [ "$findings" -eq 0 ] && [ "$stale" -eq 0 ]; then
	say "CLEAN — every short budget this gate can read starts after its fixture."
	exit 0
fi

if [ "$findings" -gt 0 ]; then
	say "⛔ FINDING — a short budget starts BEFORE an expensive fixture:" >&2
	while IFS="$(printf '\t')" read -r tag rel bline fn ctx secs fline ftxt wline wtxt; do
		[ "$tag" = FINDING ] || continue
		printf '    %s:%s  %ss budget (%s) in %s\n' "$rel" "$bline" "$secs" "$ctx" "$fn" >&2
		printf '        fixture inside the budget  %s:%s  %s\n' "$rel" "$fline" "$ftxt" >&2
		printf '        wait that pays for it      %s:%s  %s\n' "$rel" "$wline" "$wtxt" >&2
	done < "$hits_f"
	say "  Run the setup on an unbounded context and create the budget immediately before the" >&2
	say "  behaviour, as 01f81b8e81 / 4859cc43f3 / 346bce0c8a did. If the fixture IS the subject," >&2
	say "  add a reviewed exemption to this script WITH ITS MEASUREMENT." >&2
fi

if [ "$stale" -gt 0 ]; then
	say "⛔ STALE EXEMPTION — waived a case that no longer exists:" >&2
	grep '^STALE' "$hits_f" | cut -f2 | sed 's/^/    /' >&2
	say "  Delete the entry. A waiver that matches nothing is a gate that covers nothing." >&2
fi
exit 1
