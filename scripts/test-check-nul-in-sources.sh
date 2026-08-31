#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-nul-in-sources.sh — the battery for scripts/check-nul-in-sources.sh.
#
# It drives the gate against REAL throwaway repositories, because three of the four things the
# gate must get right are properties of a repository and not of a string: which paths git
# tracks, which of them .gitattributes marks `binary`, and what `git check-attr` answers.
#
# EVERY GREEN CASE HAS A MUTANT. A gate that skips binaries is only useful if the skip is
# narrow; a gate that flags NULs is only useful if it looks. Both directions are mutated, and a
# mutant that cannot RUN is a FAILURE and never a skip — the pair-with-`&&` shape that let four
# legs disappear in silence on 2026-08-07 is not repeated here.
#
# Exit 0 = every case passed. Exit 1 = a case failed (named). Exit 2 = could not run — NOT a pass.
set -u

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

# mktemp + git in the same script: without this the throwaway repositories below are driven
# into the LIVE repository, because GIT_DIR is exported to hooks from every linked worktree and
# OUTRANKS `-C`. See lib/git-env.sh.
# shellcheck source=/dev/null
. "$HERE/lib/git-env.sh" || {
	echo "FATAL: cannot source $HERE/lib/git-env.sh (git-env isolation)" >&2
	exit 2
}

command -v git >/dev/null 2>&1 || { echo "test-check-nul: no git — could not run. NOT a pass." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "test-check-nul: no python3 — could not run. NOT a pass." >&2; exit 2; }
ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "test-check-nul: not in a repo — NOT a pass." >&2; exit 2; }
GATE="$ROOT/scripts/check-nul-in-sources.sh"
[ -f "$GATE" ] || { echo "test-check-nul: $GATE not found — NOT a pass." >&2; exit 2; }

WORK="$(mktemp -d -p "${TMPDIR:-/tmp}" check-nul-tests.XXXXXX)" || {
	echo "test-check-nul: cannot create a scratch directory — NOT a pass." >&2; exit 2; }
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# mkrepo <name> — a repository the gate calls clean: one ordinary text source, tracked.
mkrepo() {
	local d="$WORK/$1"
	rm -rf "$d"
	mkdir -p "$d"
	git init -q "$d" 2>/dev/null || return 1
	printf 'const SEP = "\\u0000";\nexport const x = 1;\n' >"$d/ok.ts"
	git -C "$d" add -A >/dev/null 2>&1
	printf '%s' "$d"
}

# nul_into <file> — append a RAW NUL byte, the thing the gate exists to refuse.
nul_into() { printf 'a\000b\n' >>"$1"; }

# expect <label> <repo> <gate> <want-rc> <want-substring>
expect() {
	local label="$1" repo="$2" gate="$3" wrc="$4" wsub="$5" out rc
	out="$(cd "$repo" && bash "$gate" 2>&1)"
	rc=$?
	if [ "$rc" != "$wrc" ]; then
		fail "$label" "exit $rc, wanted $wrc — $(printf '%s' "$out" | head -2 | tr '\n' ' ')"
		return
	fi
	case "$out" in
	*"$wsub"*) pass "$label" ;;
	*) fail "$label" "exit code right ($rc) but the verdict never says '$wsub'. Got: $(printf '%s' "$out" | head -3 | tr '\n' ' ')" ;;
	esac
}

# expect_mutant <name> <old> <new> <label> <repo> <want-rc> <want-substring>
# One function, not a `M=$(mutant) && expect` pair: a leg that cannot run must FAIL, not vanish.
expect_mutant() {
	local mname="$1" mold="$2" mnew="$3" mlabel="$4" mrepo="$5" mwrc="$6" mwsub="$7"
	local m="$WORK/mutant-$mname.sh"
	if ! python3 - "$GATE" "$m" "$mold" "$mnew" <<'PY'
import sys
src, dst, old, new = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
s = open(src).read()
if old not in s:
    sys.exit(3)
open(dst, "w").write(s.replace(old, new, 1))
PY
	then
		fail "$mlabel" "the mutation did not apply — the code it targets moved. The leg did NOT run, which is a failure and not a skip"
		return
	fi
	if ! bash -n "$m" 2>/dev/null; then
		fail "$mlabel" "the mutated gate is not valid bash — it would redden every case for the wrong reason"
		return
	fi
	expect "$mlabel" "$mrepo" "$m" "$mwrc" "$mwsub"
}

printf 'test-check-nul: seven cases, with mutants in both directions\n'

# ------------------------------------------------------------------ 1. clean
R="$(mkrepo clean)" || { echo "could not build fixture" >&2; exit 2; }
expect "1 an escaped \\u0000 in source is CLEAN (the correct form)" "$R" "$GATE" 0 "CLEAN"

# ------------------------------------------------------------------ 2. a raw NUL in a source
R="$(mkrepo rawnul)" || exit 2
nul_into "$R/ok.ts"
git -C "$R" add -A >/dev/null 2>&1
expect "2 a RAW NUL in a .ts is DIRTY" "$R" "$GATE" 1 "DIRTY"
expect "2b and the file is NAMED with its offset" "$R" "$GATE" 1 "ok.ts: 1 NUL byte(s), first at offset"

# ------------------------------------------------------------------ 3. a real binary is skipped
R="$(mkrepo binext)" || exit 2
printf 'PNG\000\000\000fake\n' >"$R/logo.png"
git -C "$R" add -A >/dev/null 2>&1
expect "3 a NUL inside a .png is skipped by extension" "$R" "$GATE" 0 "CLEAN"
expect_mutant noskip \
	'    if is_binary_by_name(p):
        skipped_ext += 1
        continue' \
	'    if False:
        skipped_ext += 1
        continue' \
	"3m mutant (extension skip removed) flags the .png" "$R" 1 "DIRTY"

# ------------------------------------------------------- 4. .gitattributes `binary` is honoured
R="$(mkrepo attrbin)" || exit 2
printf 'THUMB\000\000data\n' >"$R/.thumbnail"
printf '.thumbnail binary\n' >"$R/.gitattributes"
git -C "$R" add -A >/dev/null 2>&1
expect "4 a path declared binary in .gitattributes is skipped" "$R" "$GATE" 0 "CLEAN"
expect_mutant noattr \
	'    if p in declared:
        skipped_attr += 1
        continue' \
	'    if p in set():
        skipped_attr += 1
        continue' \
	"4m mutant (attribute skip removed) flags the declared binary" "$R" 1 "DIRTY"

# ---------------------------------------------- 5. the scan itself is load-bearing
R="$(mkrepo lookmut)" || exit 2
nul_into "$R/ok.ts"
git -C "$R" add -A >/dev/null 2>&1
expect_mutant nolook \
	'    at = data.find(b"\0")' \
	'    at = -1' \
	"5m mutant (never looks for a NUL) reports CLEAN on a dirty tree" "$R" 0 "CLEAN"

# ---------------------------------------------- 6. the scope line is not decoration
R="$(mkrepo scope)" || exit 2
printf 'PNG\000x\n' >"$R/a.png"
git -C "$R" add -A >/dev/null 2>&1
expect "6 the verdict DECLARES what it did not look at" "$R" "$GATE" 0 "skipped by binary extension"

# ---------------------------------------------- 7. not a repository -> exit 2, not a pass
NR="$WORK/notarepo"
mkdir -p "$NR"
expect "7 outside a working tree -> exit 2 (COULD NOT LOOK)" "$NR" "$GATE" 2 "COULD NOT LOOK"

if [ "$fails" -ne 0 ]; then
	printf 'test-check-nul: %d FAILED\n' "$fails" >&2
	exit 1
fi
printf 'test-check-nul: OK\n'
exit 0
