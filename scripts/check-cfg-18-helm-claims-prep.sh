#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-18 unique leftover unique vs check-helm-claims.sh (library pin,
# LOOK 2 without a manifest, must not enter lint:addon-sets) and unique
# leftover unique vs #1410. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-18-helm-claims-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-18-helm-claims-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG18P_JSON:-design/cfg-18-helm-claims-prep-2026-08-20.json}"
DOC="${OLIVARES_CFG18P_DOC:-design/CFG-18-HELM-CLAIMS-PREP-2026-08-20.md}"
ORIG="${OLIVARES_CFG18P_ORIG:-scripts/check-helm-claims.sh}"
TF="${OLIVARES_CFG18P_TF:-Taskfile.yml}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ORIG" ] || cannot "missing $ORIG"
[ -r "$TF" ] || cannot "missing $TF"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-helm-claims.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original helm-claims check"
grep -F -q 'Unique leftover unique vs `#1410`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1410"
grep -F -q 'Original CHECK must not enter lint:addon-sets' "$DOC" \
  || fail "prepare doc lost addon-sets HOLD"
grep -F -q 'Does not run helm' "$DOC" \
  || fail "prepare doc lost helm HOLD"
if grep -qiE 'helm ran|chart published|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a run this lote does not have"
fi

orig_rc=0
bash "$ORIG" >/dev/null 2>/dev/null || orig_rc=$?
[ "$orig_rc" = 2 ] || fail "original check-helm-claims.sh no-args rc=$orig_rc want LOOK 2"

python3 - "$JSON" "$TF" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cfg-18-helm-claims-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cfg-18-helm-claims-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    tf = open(sys.argv[2], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "cfg-18-helm-claims-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("original_requires_manifest") is not True:
    fail("original_requires_manifest must stay true")
if data.get("original_in_addon_sets") is not False:
    fail("original_in_addon_sets must stay false")
if data.get("helm_ran") is not False:
    fail("helm_ran must stay false")
if data.get("chart_published") is not False:
    fail("chart_published must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

start = tf.find("lint:addon-sets:\n")
end = tf.find("lint:addon-sets-gate:")
if start < 0 or end < 0 or end <= start:
    cannot("Taskfile lost lint:addon-sets / lint:addon-sets-gate")
block = tf[start:end]
if "bash scripts/check-helm-claims.sh" in block:
    fail("original check-helm-claims.sh entered lint:addon-sets — LOOK 2 on hub CI")
print("json-ok")
PY

say "check-cfg-18-helm-claims-prep: CLEAN — original still LOOK 2 without a manifest; not in lint:addon-sets."
exit 0
