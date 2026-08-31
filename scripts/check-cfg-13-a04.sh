#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-13 unique leftover: A-04 whole-detector path exemption is gone.
# `.*_test.go$` and `.*/testdata/.*` must not sit in any paths allowlist.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-13-a04: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-13-a04: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC="${OLIVARES_CFG13_DOC:-design/CFG-13-A04-PATH-EXEMPT-GONE-2026-08-20.md}"
GL="${OLIVARES_CFG13_GL:-.gitleaks.toml}"
CANARY="${OLIVARES_CFG13_CANARY:-scripts/test-check-secrets.sh}"

[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$GL" ] || cannot "missing $GL"
[ -r "$CANARY" ] || cannot "missing $CANARY"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'a04-path-exemption: gone' "$DOC" \
  || fail "prepare doc lost a04-path-exemption: gone"
grep -q 'Unique leftover unique vs DP-14' "$DOC" \
  || fail "prepare doc lost uniqueness vs DP-14"
grep -Fq 'A-04: a private key in *_test.go is a FINDING' "$CANARY" \
  || fail "canary lost case 9 (private key in _test.go)"
grep -Fq 'A-04: a private key in testdata/ is a FINDING' "$CANARY" \
  || fail "canary lost case 10 (private key in testdata/)"

python3 - "$GL" <<'PY' || exit $?
import sys, tomllib

def fail(msg):
    print(f"check-cfg-13-a04: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cfg-13-a04: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    doc = tomllib.loads(open(sys.argv[1], encoding="utf-8").read())
except Exception as e:
    cannot(f".gitleaks.toml is not TOML: {e}")

forbidden = {
    r".*_test.go$",
    r".*_test\.go$",
    r".*/testdata/.*",
}

als = doc.get("allowlists")
if not isinstance(als, list) or not als:
    cannot("no [[allowlists]] blocks")

found_a04_regex = False
for i, block in enumerate(als):
    if not isinstance(block, dict):
        continue
    desc = block.get("description") or ""
    if desc.startswith("A-04:"):
        found_a04_regex = True
        if block.get("paths"):
            fail("A-04 decoy allowlist gained a paths exemption")
    for p in block.get("paths") or []:
        if p in forbidden:
            fail(f"allowlists[{i}] paths still exempt {p!r} (A-04 whole-detector hole)")

if not found_a04_regex:
    fail("A-04 exact-decoy regex allowlist is missing")

print("check-cfg-13-a04: CLEAN — _test.go and testdata are scanned; A-04 path exemption gone.")
PY
