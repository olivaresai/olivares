#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-ver-01-nm-remasure.sh — VER-01. B-02 not closed on overlay main.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-ver-01-nm-remasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-ver-01-nm-remasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_VER01_JSON:-design/ver-01-nm-remasure.json}"
DOC="${OLIVARES_VER01_DOC:-design/VER-01-NM-REMEASURE-2026-08-19.md}"
PRIOR="${OLIVARES_VER01_PRIOR:-design/VER-ALC-MEDIDO-2026-08-18.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$PRIOR" ] || cannot "missing $PRIOR"

grep -q 'NOT CLOSED' "$DOC" || fail "$DOC lost NOT CLOSED"
if grep -qiE 'B-02 closed on overlay main|overlay main assembled|cut battery passed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'credminter' "$PRIOR" || fail "$PRIOR lost the credminter HOLD"

python3 - "$JSON" <<'PY' || fail "JSON failed the VER-01 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "ver-01-nm-remasure/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("b02_closed") is not False:
    raise SystemExit("b02_closed must stay false")
if data.get("origin_main_assembled") is not False:
    raise SystemExit("origin_main_assembled must stay false")
if data.get("assembled_tree_is_overlay_main") is not False:
    raise SystemExit("assembled tree is not overlay main")
if data.get("probe_blind") is not False:
    raise SystemExit("probe_blind must stay false (activation in both)")
if data.get("assembled_tree_sha") != "928ad96":
    raise SystemExit("assembled_tree_sha must stay the remasured C13 tree")
if data.get("ver02_run") is not False:
    raise SystemExit("ver02_run must stay false")
if data.get("credminter_in_base") is not True:
    raise SystemExit("credminter_in_base must stay true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

full = data.get("full") or {}
base = data.get("base") or {}
if full.get("activation") != 43 or base.get("activation") != 43:
    raise SystemExit("activation control+ must stay 43/43")
if base.get("contentfirewall") != 0:
    raise SystemExit("BASE contentfirewall must stay 0 on the remasured tree")
if full.get("contentfirewall") != 26:
    raise SystemExit("full contentfirewall must stay 26 on the remasured tree")
if base.get("credminter") != 15 or full.get("credminter") != 15:
    raise SystemExit("credminter must stay 15/15")
cuts = (
    "hookhardening",
    "retrievalscan",
    "federation",
    "durablebus",
    "compliancedepth",
    "iso42001",
    "doraregister",
    "oscalingest",
)
for name in cuts:
    if base.get(name) != 0:
        raise SystemExit("BASE %s must stay 0" % name)
    if not full.get(name):
        raise SystemExit("full %s must stay a positive control" % name)
PY

say "check-ver-01-nm-remasure: CLEAN — remasured; B-02 open on overlay main."
exit 0
