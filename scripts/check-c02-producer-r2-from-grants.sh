#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-producer-r2-from-grants.sh — C02. Set-keyed R2 from live grants.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-producer-r2-from-grants: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-producer-r2-from-grants: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02PK_JSON:-design/c02-producer-r2-from-grants.json}"
DOC="${OLIVARES_C02PK_DOC:-design/C02-PRODUCER-R2-FROM-GRANTS-2026-08-19.md}"
ART="${OLIVARES_C02PK_ART:-commercial/license-worker/src/download/artifacts.ts}"
GATE="${OLIVARES_C02PK_GATE:-commercial/license-worker/src/download/gate.ts}"
TEST="${OLIVARES_C02PK_TEST:-commercial/license-worker/test/download.test.ts}"
PUB="${OLIVARES_C02PK_PUB:-scripts/publish-enterprise-artifacts.sh}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ART" ] || cannot "missing $ART"
[ -f "$GATE" ] || cannot "missing $GATE"
[ -f "$TEST" ] || cannot "missing $TEST"
[ -f "$PUB" ] || cannot "missing $PUB"
grep -q 'enterprise/${VERSION}/${SET}/' "$PUB" || fail "$PUB lost the per-set binary key"
if grep -qE 'enterprise/\$\{VERSION\}/\$\(basename' "$PUB"; then
	fail "$PUB still writes the unscoped monolith binary key"
fi

grep -q 'delivery NOT CLOSED' "$DOC" || fail "$DOC lost delivery NOT CLOSED"
if grep -qiE 'bytes are real|FIRMA A claimed|stub gone' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$ART" "$GATE" "$TEST" <<'PY' || fail "JSON/worker failed the C02 producer-key contract"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
art = open(sys.argv[2], encoding="utf-8").read()
gate = open(sys.argv[3], encoding="utf-8").read()
test = open(sys.argv[4], encoding="utf-8").read()

if data.get("schema") != "c02-producer-r2-from-grants/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("binary_key_includes_set") is not True:
    raise SystemExit("binary_key_includes_set must stay true")
if data.get("set_source") != "live_grants":
    raise SystemExit("set_source must stay live_grants")
if data.get("set_on_binary_query") != "refused":
    raise SystemExit("set_on_binary_query must stay refused")
if data.get("monolith_fallback") is not False:
    raise SystemExit("monolith_fallback must stay false")
if data.get("delivery_404_closed") is not False:
    raise SystemExit("delivery_404_closed must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

sig = re.search(r"export function artifactKey\(([^)]*)\)", art)
if not sig:
    raise SystemExit("artifactKey signature missing")
params = [p.strip() for p in sig.group(1).split(",") if p.strip()]
if len(params) != 4:
    raise SystemExit("artifactKey must take four params, got %s" % params)
if "enterprise/${version}/${set}/" not in art:
    raise SystemExit("artifacts.ts lost the per-set R2 path")
if "setSlug(live)" not in gate and "setSlug(live )" not in gate:
    raise SystemExit("gate does not derive the set from live grants")
if "no live grant for set" not in gate:
    raise SystemExit("gate lost the empty-grants refusal")
if "variant is not a binary download query" not in gate:
    raise SystemExit("gate lost the variant refusal")
if "set is the manifest axis, not a binary key" not in gate:
    raise SystemExit("gate lost the set-on-binary refusal")
if "no live grant for set" not in test:
    raise SystemExit("tests lost the empty-grants mutant")
if "engine-shaped query streams the set-keyed artifact" not in test:
    raise SystemExit("tests lost the engine-shaped no-fire")
PY

say "check-c02-producer-r2-from-grants: CLEAN — set-keyed R2 from grants; query set/variant refused."
exit 0
