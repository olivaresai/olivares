#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c13-02-worker-map.sh — C13-02 remainder. Worker copy of the
# slug→package map equals the source. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-02-worker-map: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-02-worker-map: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

SRC="${OLIVARES_C1302_SRC:-commercial/module-slug-package.json}"
COPY="${OLIVARES_C1302_COPY:-commercial/license-worker/src/catalog/module-slug-package.json}"
TS="${OLIVARES_C1302_TS:-commercial/license-worker/src/catalog/slug-package.ts}"

[ -f "$SRC" ] || cannot "missing C13-02 source $SRC"
[ -f "$COPY" ] || cannot "missing Worker copy $COPY"
[ -f "$TS" ] || cannot "missing $TS"

python3 - "$SRC" "$COPY" <<'PY' || fail "Worker copy drifted from commercial/module-slug-package.json"
import json, sys
a = json.load(open(sys.argv[1], encoding="utf-8"))
b = json.load(open(sys.argv[2], encoding="utf-8"))
if a != b:
    raise SystemExit("JSON mismatch")
n = len(a.get("entries") or [])
if n < 20:
    raise SystemExit("source too short (%d)" % n)
PY

grep -q 'packageForSlug' "$TS" || fail "$TS does not export packageForSlug"
grep -q 'module-slug-package.json' "$TS" || fail "$TS does not import the map"

say "check-c13-02-worker-map: CLEAN — Worker map equals C13-02 source."
exit 0
