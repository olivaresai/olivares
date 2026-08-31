#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-04: overlay #55 is not landable as-is. Hub-safe HOLD.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-04-no-land-55: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-04-no-land-55: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1304_JSON:-design/c13-04-no-land-55.json}"
DOC="${OLIVARES_C1304_DOC:-design/C13-04-NO-LAND-55-2026-08-20.md}"
BACKLOG="${OLIVARES_C1304_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"
VOCAB="${OLIVARES_C1304_VOCAB:-design/VOCABULARIO-MODULOS-2026-08-08.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$BACKLOG" ] || cannot "missing $BACKLOG"
[ -f "$VOCAB" ] || cannot "missing $VOCAB"

grep -q 'NOT MERGED' "$DOC" || fail "$DOC lost NOT MERGED"
grep -q 'as-is refused' "$DOC" || fail "$DOC lost as-is refused"
if grep -qiE 'landed overlay #55|catalog closed|FIRMA A claimed' "$DOC"; then
  fail "$DOC claims a close this lote does not have"
fi
grep -q 'C13-04' "$BACKLOG" || fail "$BACKLOG lost the C13-04 row"
# El nombre de rama es un token de sesion y este fichero viaja al arbol publico:
# vive en el JSON que este guion ya lee, que no viaja.
_vocab="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1],encoding="utf-8"));print("|".join(d["vocab_must_name"]))' "$JSON")" \
  || cannot "$JSON lost vocab_must_name"
[ -n "$_vocab" ] || cannot "$JSON has an empty vocab_must_name"
grep -qE "$_vocab" "$VOCAB" \
  || fail "$VOCAB lost the #55 branch name"

python3 - "$JSON" <<'PY' || fail "JSON failed the C13-04 contract"
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c13-04-no-land-55/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("as_is_landable") is not False:
    raise SystemExit("as_is_landable must stay false")
if data.get("merged") is not False:
    raise SystemExit("merged must stay false")
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("overlay_pr") != 55:
    raise SystemExit("overlay_pr must stay 55")
if data.get("c13_01_03_pr") != 72:
    raise SystemExit("C13-01/03 vehicle must stay overlay 72")
if data.get("do_not_land_ent58") is not True:
    raise SystemExit("ent#58 must stay refused as-is")
if data.get("overlay_main_addon_count") != 20:
    raise SystemExit("overlay main addon count must stay 20")
if data.get("pr55_addon_count") != 29:
    raise SystemExit("pr55 addon count must stay 29")
if data.get("pr72_addon_count") != 29:
    raise SystemExit("pr72 addon count must stay 29")
if data.get("rewrites_addongate") is not True:
    raise SystemExit("pr55 still rewrites addongate")
if data.get("rewrites_canondrift") is not True:
    raise SystemExit("pr55 still ships canondrift")
if data.get("rewrites_pack") is not True:
    raise SystemExit("pr55 still rewrites pack membership")
if data.get("canondrift_on_overlay_main") is not False:
    raise SystemExit("canondrift is not on overlay main")
PY

say "check-c13-04-no-land-55: CLEAN — overlay #55 NOT MERGED; as-is refused."
exit 0
