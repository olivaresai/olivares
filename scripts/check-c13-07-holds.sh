#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c13-07-holds.sh — C13-07. The five modules_growth slugs of AI Runtime
# Security must be named as hold-growth-slug, and AR-1..AR-4 must exist as
# measurable rows. Prose "al pasar AR-1..AR-4" without this file is the hole.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-07-holds: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-07-holds: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
CANON="design/PRICING-CANON.md"
HOLD="design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
[ -r "$CANON" ] || cannot "$CANON missing"
[ -r "$HOLD" ] || cannot "$HOLD missing"
command -v python3 >/dev/null || cannot "no python3"

python3 - "$CANON" "$HOLD" <<'PY'
import re, sys

canon_path, hold_path = sys.argv[1], sys.argv[2]
canon = open(canon_path, encoding="utf-8").read()
hold = open(hold_path, encoding="utf-8").read()

# Isolate the airs add-on block: from its heading to the next `self_hosted.business.addons.`
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
    print(f"want 5 modules_growth slugs, got {growth!r}", file=sys.stderr)
    sys.exit(1)

named = re.findall(r"^hold-growth-slug:\s*([a-z0-9-]+)\s*$", hold, flags=re.M)
if set(named) != set(growth):
    print(
        "hold-growth-slug must equal canon modules_growth "
        f"(canon={sorted(growth)} hold={sorted(named)})",
        file=sys.stderr,
    )
    sys.exit(1)

day = re.search(
    r"self_hosted\.business\.addons\.ai-runtime-security:.*?modules_day_one:.*?\n((?:[ \t]+-[ \t]+[a-z0-9-]+\n)+)",
    canon,
    flags=re.S,
)
if not day:
    print("could not parse modules_day_one under ai-runtime-security", file=sys.stderr)
    sys.exit(2)
day_one = {ln.strip()[2:].strip() for ln in day.group(1).splitlines() if ln.strip().startswith("- ")}
overlap = sorted(set(growth) & day_one)
if overlap:
    print("growth slugs leaked into modules_day_one:", ", ".join(overlap), file=sys.stderr)
    sys.exit(1)

header = re.search(r"^\| Id \| Criterio medible \| Evidencia \|", hold, flags=re.M)
if not header:
    print("HOLD doc lost the measurable-criteria table header", file=sys.stderr)
    sys.exit(1)
for ar in ("AR-1", "AR-2", "AR-3", "AR-4"):
    if not re.search(rf"^\| {ar} \|", hold, flags=re.M):
        print(f"HOLD doc is missing the {ar} row", file=sys.stderr)
        sys.exit(1)

print(
    "ok C13-07 HOLD — growth=" + ",".join(growth)
    + f" day_one={len(day_one)} AR-1..AR-4 present"
)
sys.exit(0)
PY
