#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC-01-S3 unique leftover unique vs overlay-gated check-alc-01-s3-motor-hold.sh:
# hub-safe HOLD so lint:addon-sets does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01-s3-motor-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01-s3-motor-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC01S3_JSON:-design/alc-01-s3-motor-hold-prep-2026-08-20.json}"
DOC="${OLIVARES_ALC01S3_DOC:-design/ALC-01-S3-MOTOR-HOLD-PREP-2026-08-20.md}"
WIRE="${OLIVARES_ALC01S3_WIRE:-cmd/olivares/wire_noenterprise.go}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$WIRE" ] || cannot "missing $WIRE"
command -v python3 >/dev/null || cannot "no python3"

grep -q 'Unique leftover unique vs `check-alc-01-s3-motor-hold.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated S3 check"
grep -q 'NO IMPLEMENTADO' "$DOC" \
  || fail "prepare doc lost NO IMPLEMENTADO"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost HOLD"
if grep -qiE 'managed SCIM shipped|S3 motor live' "$DOC"; then
  fail "prepare doc claims a motor this lote does not have"
fi
grep -q 'func newManagedSCIM()' "$WIRE" \
  || fail "default wire lost the named nil seam"
grep -qE 'return nil' "$WIRE" \
  || fail "default wire lost the nil managed-SCIM seam"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-alc-01-s3-motor-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-alc-01-s3-motor-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "alc-01-s3-hold-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("overlay_package_present") is not False:
    fail("overlay_package_present must stay false")
if data.get("catalog_key_present") is not False:
    fail("catalog_key_present must stay false")
if data.get("motor_implemented") is not False:
    fail("motor_implemented must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("hub_new_managed_scim_nil") is not True:
    fail("hub_new_managed_scim_nil must stay true")
sha = data.get("overlay_main_sha") or ""
if len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
    fail("overlay_main_sha is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-alc-01-s3-motor-prep: CLEAN — motor HOLD; hub-safe; overlay not remasured."
exit 0
