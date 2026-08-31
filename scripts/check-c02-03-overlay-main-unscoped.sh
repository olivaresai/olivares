#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C02-03 remainder: overlay main still publishes unscoped enterprise/{{ .Version }}
# and commercial :latest. Overlay via OLIVARES_ENT_DIR.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-03-overlay-main-unscoped: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-03-overlay-main-unscoped: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0203M_JSON:-design/c02-03-overlay-main-unscoped.json}"
DOC="${OLIVARES_C0203M_DOC:-design/C02-03-OVERLAY-MAIN-UNSCOPED-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'Producer not on main' "$DOC" || fail "$DOC lost Producer not on main"
grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'land_key_before_producer' "$DOC" || fail "$DOC lost the order pin"
if grep -qiE 'producer landed on main|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("producer_on_main") is not False:
    raise SystemExit("producer_on_main must stay false")
if data.get("land_key_before_producer") is not True:
    raise SystemExit("land_key_before_producer is the measured half-stitch")
if data.get("latest_present") is not True:
    raise SystemExit("latest_present must stay true while main is unscoped")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory"
GR="$ENT/.goreleaser.yaml"
[ -f "$GR" ] || cannot "missing goreleaser config"

python3 - "$GR" <<'PY' || fail "overlay release config is no longer the unscoped producer"
import sys
text = open(sys.argv[1], encoding="utf-8").read()
if 'directory: "enterprise/{{ .Version }}"' not in text:
    raise SystemExit("unscoped blobs.directory missing")
# A set dimension in the directory is the producer landing — this pin is that it has not.
if "enterprise/{{ .Version }}/{{" in text:
    raise SystemExit("blobs.directory already has a set dimension")
if ":latest" not in text and ':latest' not in text:
    raise SystemExit("commercial :latest tags missing")
if "olivares-enterprise:latest" not in text:
    raise SystemExit("unscoped :latest image template missing")
PY

say "check-c02-03-overlay-main-unscoped: CLEAN — overlay main unscoped; producer not landed; hub key already on main."
exit 0
