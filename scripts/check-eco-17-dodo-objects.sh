#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-17-dodo-objects.sh — ECO-17. Fourteen inventory ids stay UNKNOWN.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-17-dodo-objects: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-17-dodo-objects: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO17_JSON:-design/eco-17-dodo-objects.json}"
DOC="${OLIVARES_ECO17_DOC:-design/ECO-17-DODO-OBJECTS-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO17_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT CREATED' "$DOC" || fail "$DOC lost NOT CREATED"
if grep -qiE 'objects created|ids invented|provider_object_id filled' "$DOC"; then
	fail "$DOC claims a creation this lote does not have"
fi
grep -q 'forbidden-until-non-object-gates' "$CANON" || \
	fail "canon lost live_creation_state forbidden"
grep -q 'canonical_provider_object_ids: false' "$CANON" || \
	fail "canon lost canonical_provider_object_ids false"

python3 - "$JSON" "$CANON" <<'PY' || fail "JSON/canon failed the ECO-17 contract"
import json, re, sys

required = [
    "business-m", "business-y",
    "regulated-m", "regulated-y",
    "ai-runtime-security-m", "ai-runtime-security-y",
    "compliance-packs-m", "compliance-packs-y",
    "identity-scale-m", "identity-scale-y",
    "cloud-standard-m", "cloud-standard-y",
    "cloud-scale-m", "cloud-scale-y",
]
data = json.load(open(sys.argv[1], encoding="utf-8"))
canon = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "eco-17-dodo-objects/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("created") is not False:
    raise SystemExit("created must stay false")
if data.get("adelante") is not False:
    raise SystemExit("adelante must stay false")
if data.get("canonical_provider_object_ids") is not False:
    raise SystemExit("canonical_provider_object_ids must stay false")
if "forbidden" not in str(data.get("live_creation_state")):
    raise SystemExit("live_creation_state must stay forbidden")
objs = data.get("objects")
if not isinstance(objs, list):
    raise SystemExit("objects missing")
keys = []
for row in objs:
    k = row.get("key")
    if k in keys:
        raise SystemExit("duplicate key %s" % k)
    keys.append(k)
    if row.get("provider_object_id") != "UNKNOWN":
        raise SystemExit("%s provider_object_id must stay UNKNOWN" % k)
if set(keys) != set(required):
    raise SystemExit("objects must be the fourteen-key set, not a substitute")
if len(keys) != 14:
    raise SystemExit("objects must stay a fourteen-row LIST")
for k in required:
    if k not in canon:
        raise SystemExit("canon lost inventory key %s" % k)
unknowns = len(re.findall(r"provider_object_id: UNKNOWN", canon))
if unknowns < 14:
    raise SystemExit("canon must keep fourteen UNKNOWN provider_object_id rows, got %d" % unknowns)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-17-dodo-objects: CLEAN — fourteen keys UNKNOWN; not created."
exit 0
