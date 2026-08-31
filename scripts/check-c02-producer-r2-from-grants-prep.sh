#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02 unique leftover unique vs check-c02-producer-r2-from-grants.sh
# (on main, not in lint:addon-sets) and unique leftover unique vs #1381.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-producer-r2-from-grants-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-producer-r2-from-grants-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02PKP_JSON:-design/c02-producer-r2-from-grants-prep-2026-08-20.json}"
DOC="${OLIVARES_C02PKP_DOC:-design/C02-PRODUCER-R2-FROM-GRANTS-PREP-2026-08-20.md}"
ART="${OLIVARES_C02PKP_ART:-commercial/license-worker/src/download/artifacts.ts}"
GATE="${OLIVARES_C02PKP_GATE:-commercial/license-worker/src/download/gate.ts}"
TEST="${OLIVARES_C02PKP_TEST:-commercial/license-worker/test/download.test.ts}"
PUB="${OLIVARES_C02PKP_PUB:-scripts/publish-enterprise-artifacts.sh}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ART" ] || cannot "missing $ART"
[ -r "$GATE" ] || cannot "missing $GATE"
[ -r "$TEST" ] || cannot "missing $TEST"
[ -r "$PUB" ] || cannot "missing $PUB"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c02-producer-r2-from-grants.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original producer-R2 check"
grep -q 'delivery NOT CLOSED' "$DOC" || fail "prepare doc lost delivery NOT CLOSED"
if grep -qiE 'bytes are real|FIRMA A claimed|stub gone' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
grep -q 'enterprise/${VERSION}/${SET}/' "$PUB" \
  || fail "$PUB lost the per-set binary key"
if grep -qE 'enterprise/\$\{VERSION\}/\$\(basename' "$PUB"; then
  fail "$PUB still writes the unscoped monolith binary key"
fi

python3 - "$JSON" "$ART" "$GATE" "$TEST" <<'PY' || exit $?
import json, re, sys

def fail(msg):
    print(f"check-c02-producer-r2-from-grants-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-producer-r2-from-grants-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    art = open(sys.argv[2], encoding="utf-8").read()
    gate = open(sys.argv[3], encoding="utf-8").read()
    test = open(sys.argv[4], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-producer-r2-from-grants-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("binary_key_includes_set") is not True:
    fail("binary_key_includes_set must stay true")
if data.get("set_source") != "live_grants":
    fail("set_source must stay live_grants")
if data.get("set_on_binary_query") != "refused":
    fail("set_on_binary_query must stay refused")
if data.get("monolith_fallback") is not False:
    fail("monolith_fallback must stay false")
if data.get("delivery_404_closed") is not False:
    fail("delivery_404_closed must stay false")
if data.get("r2_objects_verified") is not False:
    fail("r2_objects_verified must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("overlay_producer_pr") != 75:
    fail("overlay_producer_pr must stay 75")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

sig = re.search(r"export function artifactKey\(([^)]*)\)", art)
if not sig:
    fail("artifactKey signature missing")
params = [p.strip() for p in sig.group(1).split(",") if p.strip()]
if len(params) != 4:
    fail("artifactKey must take four params, got %s" % params)
if "enterprise/${version}/${set}/" not in art:
    fail("artifacts.ts lost the per-set R2 path")
if "setSlug(live)" not in gate:
    fail("gate does not derive the set from live grants")
if "no live grant for set" not in gate:
    fail("gate lost the empty-grants refusal")
if "variant is not a binary download query" not in gate:
    fail("gate lost the variant refusal")
if "no live grant for set" not in test:
    fail("tests lost the empty-grants mutant")
print("json-ok")
PY

say "check-c02-producer-r2-from-grants-prep: CLEAN — set-keyed R2 from grants; delivery NOT CLOSED."
exit 0
