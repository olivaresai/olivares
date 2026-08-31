#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-01-S3: managed-SCIM motor still absent on overlay main.
# Overlay via OLIVARES_ENT_DIR. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-01-s3-motor-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-01-s3-motor-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC01S3_JSON:-design/alc-01-s3-motor-hold.json}"
DOC="${OLIVARES_ALC01S3_DOC:-design/ALC-01-S3-MOTOR-HOLD-2026-08-20.md}"
WIRE="${OLIVARES_ALC01S3_WIRE:-cmd/olivares/wire_noenterprise.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WIRE" ] || cannot "missing default wire"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NO IMPLEMENTADO' "$DOC" || fail "$DOC lost NO IMPLEMENTADO"
if grep -qiE 'managed SCIM shipped|S3 motor live|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a motor this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-01-s3-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("overlay_package_present") is not False:
    raise SystemExit("overlay_package_present must stay false")
if data.get("catalog_key_present") is not False:
    raise SystemExit("catalog_key_present must stay false")
if data.get("motor_implemented") is not False:
    raise SystemExit("motor_implemented must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

grep -q 'func newManagedSCIM()' "$WIRE" || fail "default wire lost the named nil seam"
if ! grep -qE 'return nil' "$WIRE"; then
	fail "default wire lost the nil managed-SCIM seam"
fi

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"

PKG="$ENT/enterprise/managedscim"
if [ -d "$PKG" ]; then
	fail "overlay grew enterprise/managedscim while HOLD says absent"
fi

CAT="$ENT/enterprise/activation/catalog.go"
[ -f "$CAT" ] || cannot "missing activation catalog"
if grep -Ei 'Key:[[:space:]]*"(managed-scim|managedscim|scim)"' "$CAT" >/dev/null; then
	fail "activation catalog lists a SCIM key while HOLD says absent"
fi

say "check-alc-01-s3-motor-hold: CLEAN — overlay motor unbuilt; catalog has no SCIM key."
exit 0
