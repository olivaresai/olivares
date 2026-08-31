#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-07 remainder: hold-growth-slug equals canon modules_growth (5).
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-07-growth-slugs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-07-growth-slugs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
CANON="${OLIVARES_C1307_CANON:-design/PRICING-CANON.md}"
HOLD="${OLIVARES_C1307_HOLD:-design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md}"

[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$HOLD" ] || cannot "missing $HOLD"
command -v python3 >/dev/null || cannot "no python3"

python3 - "$CANON" "$HOLD" <<'PY' || fail "hold-growth-slug does not equal canon modules_growth"
import re, sys

canon = open(sys.argv[1], encoding="utf-8").read()
hold = open(sys.argv[2], encoding="utf-8").read()

m = re.search(
    r"self_hosted\.business\.addons\.ai-runtime-security:.*?modules_growth:.*?\n((?:[ \t]+-[ \t]+[a-z0-9-]+\n)+)",
    canon,
    flags=re.S,
)
if not m:
    print("could not parse modules_growth under ai-runtime-security", file=sys.stderr)
    sys.exit(2)
growth = [ln.strip()[2:].strip() for ln in m.group(1).splitlines() if ln.strip().startswith("- ")]
if len(growth) != 5:
    print("want 5 modules_growth slugs, got %r" % growth, file=sys.stderr)
    sys.exit(1)

named = re.findall(r"^hold-growth-slug:\s*([a-z0-9-]+)\s*$", hold, flags=re.M)
if set(named) != set(growth):
    print(
        "hold-growth-slug must equal canon modules_growth "
        "(canon=%s hold=%s)" % (sorted(growth), sorted(named)),
        file=sys.stderr,
    )
    sys.exit(1)

for row in ("AR-1", "AR-2", "AR-3", "AR-4"):
    if "| " + row + " |" not in hold and "| " + row + " |" not in hold.replace("\t", " "):
        # table cells are "| AR-1 |"
        if not re.search(r"\|\s*" + re.escape(row) + r"\s*\|", hold):
            print("HOLD lost measurable row %s" % row, file=sys.stderr)
            sys.exit(1)
PY

say "check-c13-07-growth-slugs: CLEAN — five growth slugs match canon; AR-1..AR-4 still named."
exit 0
