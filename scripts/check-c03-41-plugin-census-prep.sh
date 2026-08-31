#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-41 unique leftover unique vs overlay-remeasuring
# check-c03-41-plugin-census.sh: hub-safe HOLD so lint:addon-sets
# does not remasure overlay. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-41-plugin-census-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-41-plugin-census-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0341P_JSON:-design/c03-41-plugin-census-prep-2026-08-20.json}"
DOC="${OLIVARES_C0341P_DOC:-design/C03-41-PLUGIN-CENSUS-PREP-2026-08-20.md}"
EMBED="${OLIVARES_C0341P_EMBED:-cmd/olivares/firstparty/embed.go}"
BINS="${OLIVARES_C0341P_BINS:-cmd/olivares/firstparty/bins}"
BACKLOG="${OLIVARES_C0341P_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$EMBED" ] || cannot "missing $EMBED"
[ -d "$BINS" ] || cannot "missing $BINS"
[ -r "$BACKLOG" ] || cannot "missing $BACKLOG"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-41-plugin-census.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-remeasuring census check"
grep -q 'NOT EXECUTED' "$DOC" || fail "prepare doc lost NOT EXECUTED"
grep -q 'firstparty is source-connectors' "$DOC" \
  || fail "prepare doc lost source-connectors pin"
if grep -qiE 'moved add-ons out of process|firstparty embeds enterprise|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
grep -q 'C03-41' "$BACKLOG" || fail "backlog lost the C03-41 row"
grep -q 'SOURCE-connector' "$EMBED" \
  || fail "firstparty embed.go lost SOURCE-connector scope"
[ -f "$BINS/PLACEHOLDER" ] || fail "firstparty bins lost PLACEHOLDER"

python3 - "$JSON" "$BINS" <<'PY' || exit $?
import json, os, sys

def fail(msg):
    print(f"check-c03-41-plugin-census-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-41-plugin-census-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    names = sorted(os.listdir(sys.argv[2]))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-41-plugin-census-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("executed") is not False:
    fail("executed must stay false")
if data.get("firstparty_is_source_connectors_only") is not True:
    fail("firstparty must stay source-connectors only")
if data.get("firstparty_embeds_enterprise") is not False:
    fail("firstparty must not claim to embed enterprise add-ons")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("firstparty_bins_on_main") != ["PLACEHOLDER"]:
    fail("firstparty bins pin drifted")
if data.get("enterprise_packages") != 30:
    fail("enterprise_packages must stay 30")
if data.get("source_connector_impls") != 1:
    fail("source_connector_impls must stay 1")
if data.get("source_connector_package") != "connectors/conjur":
    fail("source_connector_package pin drifted")
if data.get("output_connector_impls") != 1:
    fail("output_connector_impls must stay 1")
if data.get("output_connector_package") != "incidentloop":
    fail("output_connector_package pin drifted")
if data.get("in_process_remainder") != 28:
    fail("in_process_remainder must stay 28")
if data.get("overlay_catalog_rows") != 20:
    fail("overlay_catalog_rows must stay 20")
want = {"addongate", "activation", "seats", "ssoenforce", "federation"}
got = data.get("cannot_leave_process")
if not isinstance(got, list) or set(got) != want:
    fail("cannot_leave_process pin drifted")
if data["in_process_remainder"] != (
    data["enterprise_packages"] - data["source_connector_impls"] - data["output_connector_impls"]
):
    fail("remainder is not packages minus the two plugin interfaces")
if names != ["PLACEHOLDER"]:
    fail("bins/ on disk is %r, want PLACEHOLDER only" % names)
sha = data.get("overlay_main_sha") or ""
if len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
    fail("overlay_main_sha is not 40-hex")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c03-41-plugin-census-prep: CLEAN — census HOLD; hub-safe; overlay not remasured."
exit 0
