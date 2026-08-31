#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# REUSE-IgnoreStart
# (Like the gate it drives, this file legitimately contains the string
#  SPDX-License-Identifier in fixtures and assertions. Its own licence is the header
#  above, outside this block.)
#
# test-check-spdx.sh — the battery for scripts/check-spdx.sh.
#
# WHAT IT IS FOR. On 2026-08-14 the gate was measured blind to 44 tracked files: the same
# 200 bytes (a commercial identifier inside AGPL web/) answered `MISMATCH … rc=1` as `.js`
# and `SPDX check OK: 4855 source files … rc=0` as `.mjs`, `.cjs`, `.mts` or `.cts`. Nothing
# in the repository could have told anyone that, because the gate had no battery at all: a
# licence gate whose enumeration nobody tests can only be measured by walking into it.
#
# HOW IT IS SHAPED, and why the shape is not the obvious one.
#
#   * The FIXTURE legs (F*) run against throwaway repositories and assert on the LINES the
#     gate prints about a planted file. A fixture is a handful of files, so the gate's
#     population floor fires by construction and the exit code is 2 for all of them — that
#     is correct behaviour, not a limitation being worked around, and the exit-code ladder
#     gets legs of its own (F8, F9).
#   * The REAL-TREE leg (R1) is the contrafactual proper, and it is ONE run of the gate over
#     the actual repository with TWO defects planted at once: `.mjs` (the coverage that was
#     missing) and `.js` (the coverage that already existed and must not be lost). One run,
#     both directions, ~20s — the cost is real and it buys the only measurement that speaks
#     for the tree that ships.
#   * Every green case has a MUTANT with a NAMED WITNESS. A mutant that cannot be applied is
#     a FAILURE, never a skip: the leg that silently disappears is the defect this file
#     exists to refuse.
#
# Exit 0 = every case passed. Exit 1 = a case failed (named). Exit 2 = could not run — NOT a pass.
# export-closure: absent-by-design scripts/gen-dodo-product-art.mjs — this battery asserts on the
# DIAGNOSTIC TEXT the gate prints for the D-2 licence debt and writes that debt line into a
# disposable fixture. Both are bytes compared as data; nothing here executes the generator.

set -u

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

# mktemp + git in the same script: without this the throwaway repositories below are driven
# into the LIVE repository, because GIT_DIR is exported to hooks from every linked worktree
# and OUTRANKS `-C`. See lib/git-env.sh.
# shellcheck source=/dev/null
. "$HERE/lib/git-env.sh" || {
	echo "FATAL: cannot source $HERE/lib/git-env.sh (git-env isolation)" >&2
	exit 2
}

command -v git >/dev/null 2>&1 || { echo "test-check-spdx: no git — could not run. NOT a pass." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "test-check-spdx: no python3 — could not run. NOT a pass." >&2; exit 2; }
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "test-check-spdx: not in a repo — NOT a pass." >&2; exit 2; }
GATE="$ROOT/scripts/check-spdx.sh"
[ -f "$GATE" ] || { echo "test-check-spdx: $GATE not found — NOT a pass." >&2; exit 2; }

WORK="$(mktemp -d -p "${TMPDIR:-/tmp}" check-spdx-tests.XXXXXX)" || {
	echo "test-check-spdx: cannot create a scratch directory — NOT a pass." >&2; exit 2; }

# Probes planted in the LIVE tree for R1. Named so a leftover is unmistakable, and removed
# by the same trap that removes the scratch directory — including on interrupt.
PROBE_BASE="ng-spdx-selftest-probe-$$"
PROBE_DIR="$ROOT/web/$PROBE_BASE"
# La ventana del selftest (ver check-spdx.sh) se abre SOLO alrededor de R1, no aqui: el contraste
# de señalo que abrirla durante toda la bateria deja al gate sin poder reportar un defecto REAL
# durante mucho mas tiempo del necesario (C1). El fichero lleva el PID EN EL NOMBRE para que dos
# selftests simultaneos no se pisen (C3).
INFLIGHT="$(git rev-parse --git-dir 2>/dev/null)/ng-spdx-selftest-inflight.$$"
cleanup() { rm -rf "$WORK" "$PROBE_DIR"; rm -f "$INFLIGHT"; }
trap cleanup EXIT INT TERM

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# --- fixtures ----------------------------------------------------------------------------
# A throwaway repository the gate calls clean. Files live under web/ because the module map
# maps web/ to AGPL-3.0-only; PUBLIC-EXPORT.md is present because it is the gate's own
# sanctioned marker for "this tree legitimately does not carry the D-1 debt subject", and
# without it every fixture would also print an unrelated STALE line.
mkrepo() {
	local d="$WORK/$1"
	rm -rf "$d"
	mkdir -p "$d/web"
	git init -q "$d" 2>/dev/null || return 1
	printf 'curated-export marker fixture\n' >"$d/PUBLIC-EXPORT.md"
	hdr_file "$d/web/ok.ts" AGPL-3.0-only
	hdr_file "$d/web/ok.mjs" AGPL-3.0-only
	printf '%s' "$d"
}

# hdr_file <path> <identifier> — a source file whose declared licence is <identifier>.
hdr_file() {
	{
		printf '// SPDX-FileCopyrightText: 2026 Olivares.AI\n'
		printf '// SPDX-License-Identifier: %s\n' "$2"
		printf 'export const x = 1;\n'
	} >"$1"
}

# run_gate <dir> — echo the gate's combined output; return its exit code.
run_gate() {
	local out rc
	out="$(cd "$1" && sh "$2" 2>&1)"
	rc=$?
	printf '%s' "$out"
	return $rc
}

# expect_says <label> <dir> <gate> <substring> — the output must CONTAIN it.
expect_says() {
	local label="$1" dir="$2" gate="$3" want="$4" out
	out="$(run_gate "$dir" "$gate")"
	case "$out" in
	*"$want"*) pass "$label" ;;
	*) fail "$label" "the verdict never says '$want'. Got: $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)" ;;
	esac
}

# expect_silent <label> <dir> <gate> <substring> — the output must NOT contain it.
expect_silent() {
	local label="$1" dir="$2" gate="$3" nope="$4" out
	out="$(run_gate "$dir" "$gate")"
	case "$out" in
	*"$nope"*) fail "$label" "the verdict says '$nope' and must not. Got: $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)" ;;
	*) pass "$label" ;;
	esac
}

# expect_rc <label> <dir> <gate> <want-rc> <substring>
expect_rc() {
	local label="$1" dir="$2" gate="$3" wrc="$4" want="$5" out rc
	out="$(run_gate "$dir" "$gate")"
	rc=$?
	if [ "$rc" != "$wrc" ]; then
		fail "$label" "exit $rc, wanted $wrc — $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)"
		return
	fi
	case "$out" in
	*"$want"*) pass "$label" ;;
	*) fail "$label" "exit code right ($rc) but the verdict never says '$want'. Got: $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-200)" ;;
	esac
}

# mutate <name> <old> <new> — write a mutated copy of the gate, echo its path, fail loudly
# if the text it targets has moved. A mutant that cannot be applied did NOT run.
mutate() {
	local mname="$1" mold="$2" mnew="$3"
	local m="$WORK/mutant-$mname.sh"
	if ! python3 - "$GATE" "$m" "$mold" "$mnew" <<'PY'; then
import sys
src, dst, old, new = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
s = open(src, encoding="utf-8").read()
if s.count(old) != 1:
    sys.exit(3)
open(dst, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
		return 1
	fi
	sh -n "$m" 2>/dev/null || return 1
	printf '%s' "$m"
}

# expect_mutant <name> <old> <new> <label> <dir> <mode> <arg>
#   mode=says     -> the mutated gate's output must contain <arg>
#   mode=silent   -> the mutated gate's output must NOT contain <arg>  (the usual witness:
#                    the mutant makes the gate stop saying the true thing)
expect_mutant() {
	local mname="$1" mold="$2" mnew="$3" mlabel="$4" mdir="$5" mmode="$6" marg="$7" m
	if ! m="$(mutate "$mname" "$mold" "$mnew")" || [ -z "$m" ]; then
		fail "$mlabel" "the mutation did not apply (text absent, ambiguous, or the mutant is not valid sh). The leg did NOT run, which is a failure and not a skip"
		return
	fi
	case "$mmode" in
	says) expect_says "$mlabel" "$mdir" "$m" "$marg" ;;
	silent) expect_silent "$mlabel" "$mdir" "$m" "$marg" ;;
	rc0ok) expect_rc "$mlabel" "$mdir" "$m" 0 "$marg" ;;
	rc1) expect_rc "$mlabel" "$mdir" "$m" 1 "$marg" ;;
	esac
}

printf 'test-check-spdx: the enumeration, the third answer, the floor and the order\n'

# ---------------------------------------------------------------- F1 negative control
# Without this leg, every leg below could be passing because the gate is always red.
R="$(mkrepo clean)" || { echo "test-check-spdx: could not build a fixture — NOT a pass." >&2; exit 2; }
expect_silent "F1  a clean fixture names no MISMATCH (negative control)" "$R" "$GATE" "MISMATCH"
expect_silent "F1b a clean fixture names no MISSING" "$R" "$GATE" "MISSING"
expect_silent "F1c a clean fixture names no UNCLASSIFIED" "$R" "$GATE" "UNCLASSIFIED"

# ---------------------------------------------- F2 the family that was blind, one by one
R="$(mkrepo family)" || exit 2
for ext in mjs cjs mts cts; do
	hdr_file "$R/web/bad.$ext" LicenseRef-Olivares-Commercial
	expect_says "F2  a commercial identifier in AGPL web/ is caught as .$ext" \
		"$R" "$GATE" "MISMATCH web/bad.$ext"
	rm -f "$R/web/bad.$ext"
done

# ------------------------------------------------------------- F3 the control positive
# The extensions the gate already covered must still be covered. Widening an enumeration is
# also a way to lose coverage, and nothing else in this file would notice.
R="$(mkrepo covered)" || exit 2
for ext in js ts tsx jsx go py sh css html java; do
	hdr_file "$R/web/bad.$ext" LicenseRef-Olivares-Commercial
	expect_says "F3  still caught as .$ext (control positive)" "$R" "$GATE" "MISMATCH web/bad.$ext"
	rm -f "$R/web/bad.$ext"
done

# ------------------------------------------------------ F4 a missing header, not a wrong one
R="$(mkrepo missing)" || exit 2
printf 'export const x = 1;\n' >"$R/web/nohdr.mjs"
expect_says "F4  an .mjs with no identifier at all is MISSING" "$R" "$GATE" "MISSING  web/nohdr.mjs"

# ------------------------------------------------------------------- F5 the third answer
R="$(mkrepo unknownext)" || exit 2
hdr_file "$R/web/thing.vue" AGPL-3.0-only
expect_rc "F5  an extension with no rule is UNCLASSIFIED and the gate refuses to rule" \
	"$R" "$GATE" 2 "UNCLASSIFIED web/thing.vue"
expect_says "F5b and the message names the extension, not just the file" "$R" "$GATE" "Extensions: .vue"

# ------------------------------------------- F6 extension-less files: a CONTENT test
R="$(mkrepo shebang)" || exit 2
mkdir -p "$R/.githooks"
{
	printf '#!/bin/sh\n'
	printf '# SPDX-License-Identifier: LicenseRef-Olivares-Commercial\n'
	printf 'exit 0\n'
} >"$R/.githooks/probe"
expect_says "F6  an extension-less file WITH a shebang is source" "$R" "$GATE" "MISMATCH .githooks/probe"
printf 'v1.2.3\n' >"$R/RELEASE-VERSION"
expect_silent "F6b an extension-less file WITHOUT a shebang is not" "$R" "$GATE" "RELEASE-VERSION"

# ------------------------------------------- F7 the floor: a small tree is not a green tree
R="$(mkrepo small)" || exit 2
expect_rc "F7  a tree far below the measured population is CANNOT LOOK, not OK" \
	"$R" "$GATE" 2 "CANNOT LOOK"
expect_says "F7b and it says how many it read and what it expected" "$R" "$GATE" "the enumeration stopped working"

# ------------------------------------------------------------ F8 the zero case, unchanged
EMPTY="$WORK/empty"
mkdir -p "$EMPTY"
expect_rc "F8  an empty directory is CANNOT LOOK (exit 2)" "$EMPTY" "$GATE" 2 "zero licensed source files"

# ------------------------------------- F9 could-not-look OUTRANKS the licence verdict
# In an empty tree the D-1 debt subject is also absent, so the gate has two things to say.
# The one that must win is "I read nothing" — exit 2 — and not "licence violation" (exit 1).
expect_silent "F9  and it does not blame a licence debt for an empty read" "$EMPTY" "$GATE" "SPDX check FAILED"

# ------------------------- F10 the named licence debts: ONE path each, never a pattern
# D-2 (2026-08-14) excepts scripts/gen-dodo-product-art.mjs to LicenseRef-Olivares-Internal,
# because that file carries the commercial price table and AGPL-3.0-only is a licence to
# republish what it covers. The property that has to hold is that the exception is a PATH and
# not an extension: the day it becomes `scripts/*.mjs`, the 16 correctly-AGPL siblings stop
# being checkable and this gate's widening has been spent undoing itself. F10b is the leg that
# says so, and it is the witness the widening mutant has to walk into.
R="$(mkrepo debt)" || exit 2
mkdir -p "$R/scripts/sub"
hdr_file "$R/scripts/gen-dodo-product-art.mjs" LicenseRef-Olivares-Internal
expect_silent "F10  the named debt path may declare LicenseRef-Olivares-Internal" \
	"$R" "$GATE" "MISMATCH scripts/gen-dodo-product-art.mjs"
hdr_file "$R/scripts/other-generator.mjs" LicenseRef-Olivares-Internal
expect_says "F10b a SIBLING .mjs may not — the exception is one path, not the extension" \
	"$R" "$GATE" "MISMATCH scripts/other-generator.mjs"
hdr_file "$R/scripts/sub/gen-dodo-product-art.mjs" LicenseRef-Olivares-Internal
expect_says "F10c nor the same BASENAME elsewhere — it is the exact path, matched whole" \
	"$R" "$GATE" "MISMATCH scripts/sub/gen-dodo-product-art.mjs"

# F10d the ratchet, on the second debt as on the first: an exception that outlives its subject
# is a permanent hole, so a tree that is NOT a stamped export and no longer has the file must
# say so and name which entry to delete. PUBLIC-EXPORT.md is removed here precisely because
# its presence is what sanctions the absence.
R="$(mkrepo debtgone)" || exit 2
rm -f "$R/PUBLIC-EXPORT.md"
expect_says "F10d a non-export tree without the subject reports the D-2 exception STALE" \
	"$R" "$GATE" "named licence debt D-2 whose subject no longer exists"

# ================================ MUTANTS =================================================
# Each names the witness that kills it. A witness that could be killed by another mutant is
# not a witness, so they are deliberately different legs.

# M1 — the defect of 2026-08-14, reproduced exactly: the ESM/CJS family moved back out of
# `source` and into the silent bucket. WITNESS: F2's .mjs leg.
R="$(mkrepo family)" || exit 2
hdr_file "$R/web/bad.mjs" LicenseRef-Olivares-Commercial
expect_mutant esm-blind \
	'*.go|*.ts|*.tsx|*.mts|*.cts|*.js|*.jsx|*.mjs|*.cjs|*.html|*.css|*.py|*.java|*.sh|*.awk)
      echo source; return ;;' \
	'*.mjs|*.cjs|*.mts|*.cts) echo data; return ;;
    *.go|*.ts|*.tsx|*.js|*.jsx|*.html|*.css|*.py|*.java|*.sh|*.awk)
      echo source; return ;;' \
	"M1  mutant (ESM/CJS back in the silent bucket) stops seeing the .mjs offender [witness F2]" \
	"$R" silent "MISMATCH web/bad.mjs"

# M2 — the third answer neutered: unknown extensions become data, which is precisely the
# fail-open the classifier replaced. WITNESS: F5.
R="$(mkrepo unknownext)" || exit 2
hdr_file "$R/web/thing.vue" AGPL-3.0-only
expect_mutant no-third-answer \
	'      echo unknown; return ;;' \
	'      echo data; return ;;' \
	"M2  mutant (unknown -> data) stops reporting the unclassified extension [witness F5]" \
	"$R" silent "UNCLASSIFIED"

# M3 — the population floor neutered. WITNESS: F7 — the gate PRINTS OK over a three-file
# tree, which is the whole failure the floor exists to refuse.
R="$(mkrepo small)" || exit 2
expect_mutant no-floor \
	'SPDX_MIN_CHECKED=4200' \
	'SPDX_MIN_CHECKED=0' \
	"M3  mutant (floor removed) approves a three-file tree in green [witness F7]" \
	"$R" rc0ok "SPDX check OK"

# M4 — the shebang sniff neutered, so extension-less programs go back to being invisible.
# WITNESS: F6.
R="$(mkrepo shebang)" || exit 2
mkdir -p "$R/.githooks"
{
	printf '#!/bin/sh\n'
	printf '# SPDX-License-Identifier: LicenseRef-Olivares-Commercial\n'
	printf 'exit 0\n'
} >"$R/.githooks/probe"
expect_mutant no-shebang \
	"      if [ \"\$(head -n 1 \"\$1\" 2>/dev/null | cut -c1-2)\" = '#!' ]; then" \
	"      if false; then" \
	"M4  mutant (shebang sniff removed) stops checking the extension-less hook [witness F6]" \
	"$R" silent "MISMATCH .githooks/probe"

# M5 — the ORDER: the licence verdict decided before the could-not-look verdict, which is
# what the gate did until 2026-08-14. WITNESS: F9 — an empty tree starts answering with a
# licence failure instead of admitting it read nothing.
expect_mutant verdict-order \
	'if [ "$cannot_look" -ne 0 ]; then
  exit 2
fi' \
	'if [ "$cannot_look" -ne 0 ] && [ "$((missing + mismatch + orphan + stale))" -eq 0 ]; then
  exit 2
fi' \
	"M5  mutant (licence verdict first) blames a debt for an empty read [witness F9]" \
	"$EMPTY" rc1 "SPDX check FAILED"

# M6 — THE ONE THIS EXCEPTION EXISTS TO BE PROTECTED FROM: D-2 widened from a path to the
# extension. It is the cheapest way to make the gate green and it silently hands `scripts/`
# .mjs files back to the blind spot the widening had just closed — a glob never expires.
# WITNESS: F10b, the AGPL sibling that stops being reported.
R="$(mkrepo debt)" || exit 2
mkdir -p "$R/scripts"
hdr_file "$R/scripts/gen-dodo-product-art.mjs" LicenseRef-Olivares-Internal
hdr_file "$R/scripts/other-generator.mjs" LicenseRef-Olivares-Internal
expect_mutant debt-widened \
	'  if [ "$1" = "$INTERNAL_DEBT" ]; then
    echo "LicenseRef-Olivares-Internal"
    return
  fi' \
	'  case "$1" in
    scripts/*.mjs)
      echo "LicenseRef-Olivares-Internal"
      return ;;
  esac' \
	"M6  mutant (D-2 widened to scripts/*.mjs) stops checking the AGPL siblings [witness F10b]" \
	"$R" silent "MISMATCH scripts/other-generator.mjs"

# M7 — D-2 excepting nothing, i.e. the state this branch was in before the alignment: the
# gate demands AGPL-3.0-only of the file that carries the price table, and the only way to
# obey is to relicense it. WITNESS: F10 — the accepted path starts being reported.
R="$(mkrepo debt2)" || exit 2
mkdir -p "$R/scripts"
hdr_file "$R/scripts/gen-dodo-product-art.mjs" LicenseRef-Olivares-Internal
expect_mutant debt-absent \
	"INTERNAL_DEBT='scripts/gen-dodo-product-art.mjs'" \
	"INTERNAL_DEBT='scripts/__d2_subject_that_is_not_there__.mjs'" \
	"M7  mutant (D-2 excepting nothing) demands AGPL of the price-table file [witness F10]" \
	"$R" says "MISMATCH scripts/gen-dodo-product-art.mjs"

# M8 — the D-2 ratchet removed, so the exception can outlive its subject: the file moves out
# of scripts/ one day and this line keeps blessing whatever is created at that path next.
# WITNESS: F10d. (M5's witness is the exit-code ORDER and M3's is the floor, so neither can
# stand in for this one.)
R="$(mkrepo debtgone)" || exit 2
rm -f "$R/PUBLIC-EXPORT.md"
expect_mutant debt-no-ratchet \
	'if [ ! -f "$INTERNAL_DEBT" ] && [ ! -f PUBLIC-EXPORT.md ]; then' \
	'if false; then' \
	"M8  mutant (D-2 ratchet removed) lets the exception outlive its subject [witness F10d]" \
	"$R" silent "named licence debt D-2 whose subject no longer exists"

# ======================= R1: THE CONTRAFACTUAL OVER THE REAL TREE =========================
# One run, two planted defects, the same 200 bytes in both. `.mjs` is the coverage that was
# missing on 2026-08-14; `.js` is the coverage that already existed. The gate must name BOTH
# and exit 1 — if only one appears, either the widening did not take or it cost us what we
# already had.
printf '%s\n' "$$" > "$INFLIGHT" 2>/dev/null || {
	echo "test-check-spdx: cannot declare the in-flight window ($INFLIGHT) — NOT a pass." >&2; exit 2; }
mkdir -p "$PROBE_DIR"
hdr_file "$PROBE_DIR/probe.mjs" LicenseRef-Olivares-Commercial
hdr_file "$PROBE_DIR/probe.js" LicenseRef-Olivares-Commercial
r1_out="$(cd "$ROOT" && sh "$GATE" 2>&1)"
r1_rc=$?
rm -rf "$PROBE_DIR"
rm -f "$INFLIGHT"
if [ "$r1_rc" != 1 ]; then
	fail "R1  the real tree with two planted defects exits 1" \
		"exit $r1_rc — $(printf '%s' "$r1_out" | tr '\n' ' ' | cut -c1-240)"
else
	pass "R1  the real tree with two planted defects exits 1"
fi
case "$r1_out" in
*"MISMATCH web/$PROBE_BASE/probe.mjs"*) pass "R1a .mjs is named (the coverage that was missing)" ;;
*) fail "R1a .mjs is named (the coverage that was missing)" "no MISMATCH line for the .mjs probe" ;;
esac
case "$r1_out" in
*"MISMATCH web/$PROBE_BASE/probe.js"*) pass "R1b .js is still named (control positive)" ;;
*) fail "R1b .js is still named (control positive)" "no MISMATCH line for the .js probe" ;;
esac

if [ "$fails" -ne 0 ]; then
	printf 'test-check-spdx: %d FAILED\n' "$fails" >&2
	exit 1
fi
printf 'test-check-spdx: OK\n'
exit 0
# REUSE-IgnoreEnd
