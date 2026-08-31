#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-eco-18-false-closures.sh — ECO-18. The seven §10.2 false closures
# stay reopened. A numeric U_f here is a finding. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-18-false-closures: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-18-false-closures: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON=design/eco-18-false-closures.json
REG=design/REGISTRO-DECISIONES-2026-08-01.md
DOC=design/ECO-18-FALSOS-CIERRES-2026-08-19.md
[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$REG" ] || cannot "missing $REG"
[ -r "$DOC" ] || cannot "missing $DOC"

command -v python3 >/dev/null 2>&1 || cannot "python3 missing"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-18 contract"
import json, sys
path = sys.argv[1]
data = json.load(open(path, encoding="utf-8"))
want = ["YA-46", "YA-72", "YA-05", "YA-07", "YA-23", "YA-34", "YA-74"]
if data.get("price_unsigned") is not True:
    raise SystemExit("price_unsigned is not true")
for k in ("u_f", "u_d"):
    v = data.get(k)
    if v != "UNKNOWN":
        raise SystemExit("%s is %r, want UNKNOWN" % (k, v))
rows = data.get("rows") or []
got = [r.get("id") for r in rows]
if got != want:
    raise SystemExit("row ids %s, want %s" % (got, want))
for r in rows:
    if r.get("decides_price") is not False:
        raise SystemExit("%s decides_price must be false" % r.get("id"))
    st = r.get("status") or ""
    if st.startswith("CLOSED") or st == "YA-RESPONDIDA":
        raise SystemExit("%s status %s re-closes a false closure" % (r.get("id"), st))
print("json-ok", len(rows))
PY

for id in YA-46 YA-72 YA-05 YA-07 YA-23 YA-34 YA-74; do
  grep -q "$id \*\*REABIERTA" "$REG" \
    || fail "register lost REABIERTA on $id"
done

grep -q 'Not a decision' "$DOC" \
  || fail "the remasurement claims a decision"

say "check-eco-18-false-closures: CLEAN — seven rows reopened; U_f/U_d UNKNOWN; price unsigned."
exit 0
