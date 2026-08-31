#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-15 unique leftover unique vs check-c05-15-corpus.sh
# (named on main, CHECK not in lint:addon-sets) and unique leftover
# unique vs #1392 / #1406. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-15-corpus-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-15-corpus-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0515P_JSON:-design/c05-15-corpus-prep-2026-08-20.json}"
DOC="${OLIVARES_C0515P_DOC:-design/C05-15-CORPUS-CONTRACT-PREP-2026-08-20.md}"
EVIDENCE="${OLIVARES_C0515P_EVIDENCE:-commercial/dodo-sandbox/evidence/dodo-10/wh-deliveries/delivery-0010.json}"
GO="${OLIVARES_C0515P_GO:-cloud/control-plane/internal/billing/c05_15_corpus_test.go}"
TS="${OLIVARES_C0515P_TS:-commercial/license-worker/test/c05-15-corpus-contract.test.ts}"
CLASSIFIER="${OLIVARES_C0515P_CLASSIFIER:-commercial/license-worker/src/dodo/events.ts}"
PARSER="${OLIVARES_C0515P_PARSER:-cloud/control-plane/internal/billing/dodoenvelope.go}"

for f in "$JSON" "$DOC" "$EVIDENCE" "$GO" "$TS" "$CLASSIFIER" "$PARSER"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c05-15-corpus.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original corpus check"
grep -F -q 'Unique leftover unique vs `#1392`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1392"
grep -F -q 'Unique leftover unique vs `#1406`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1406"
grep -q 'Does not honour cloud add-ons' "$DOC" \
  || fail "prepare doc lost cloud-addons HOLD"
grep -q 'Does not restack `#948`' "$DOC" \
  || fail "prepare doc lost #948 HOLD"
if grep -qiE 'honours cloud add-ons|FIRMA A claimed|corpus closed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

grep -q '"raw_body"' "$EVIDENCE" || fail "evidence lost raw_body"
grep -q 'delivery-0010.json' "$GO" || fail "Go contract does not load delivery-0010.json"
grep -q 'EventDataFromDodo' "$GO" || fail "Go contract does not call EventDataFromDodo"
grep -q 'classifyDodo' "$TS" || fail "Worker contract does not call classifyDodo"
grep -q 'delivery-0010.json' "$TS" || fail "Worker contract does not load delivery-0010.json"
grep -q 'INVALID_PAYLOAD' "$TS" || fail "Worker contract lost the reduced-body INVALID_PAYLOAD control"
grep -q 'export function classifyDodo' "$CLASSIFIER" || fail "classifyDodo is no longer exported"
grep -q 'func EventDataFromDodo' "$PARSER" || fail "EventDataFromDodo is gone"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-15-corpus-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-15-corpus-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-15-corpus-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
for k in ("delivery_0010_raw_body", "classify_dodo_exported",
          "event_data_from_dodo", "invalid_payload_control"):
    if data.get(k) is not True:
        fail("%s must stay true" % k)
if data.get("cloud_addons_honoured") is not False:
    fail("cloud_addons_honoured must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c05-15-corpus-prep: CLEAN — both halves name delivery-0010; cloud add-ons not honoured."
exit 0
