#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# VER-02: 16/16 NOT_RUN. Hub-safe HOLD. Overlay remasure stays on #1117.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-ver-02-cut-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-ver-02-cut-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_VER02_JSON:-design/ver-02-cut-hold.json}"
DOC="${OLIVARES_VER02_DOC:-design/VER-02-CUT-HOLD-2026-08-20.md}"
BACKLOG="${OLIVARES_VER02_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$BACKLOG" ] || cannot "missing $BACKLOG"

grep -q '16/16 NOT_RUN' "$DOC" || fail "$DOC lost 16/16 NOT_RUN"
if grep -qiE '16/16 passed|cut battery passed|VER-02 closed' "$DOC"; then
  fail "$DOC claims a close this lote does not have"
fi
grep -q 'VER-02' "$BACKLOG" || fail "$BACKLOG lost the VER-02 row"

python3 - "$JSON" <<'PY' || fail "JSON failed the VER-02 contract"
import json, sys

tags = ["addon_airs", "addon_cp", "addon_ids", "addon_reg"]
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "ver-02-cut-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("ver02_run") is not False:
    raise SystemExit("ver02_run must stay false")
if data.get("ver02_full_16_of_16") is not False:
    raise SystemExit("ver02_full_16_of_16 must stay false")
if data.get("overlay_main_taskfile_invokes_cut") is not False:
    raise SystemExit("overlay main Taskfile still does not invoke the cut")
if data.get("cut_script_on_overlay_main") is not True:
    raise SystemExit("cut script must stay present on overlay main")
if data.get("origin_main_assembled") is not False:
    raise SystemExit("origin_main_assembled must stay false")
if data.get("build_tree") != "unset":
    raise SystemExit("build_tree must stay unset")
if data.get("subsets_declared") != 16:
    raise SystemExit("subsets_declared must stay 16")
if data.get("subsets_run_on_overlay_main") != 0:
    raise SystemExit("subsets_run_on_overlay_main must stay 0")
if data.get("c02_01_landed") is not False:
    raise SystemExit("C02-01 must stay not landed")
if data.get("c02_01_pr") != 69:
    raise SystemExit("C02-01 vehicle must stay overlay 69")
if data.get("cut_gate_pr") != 74:
    raise SystemExit("cut-gate DRAFT must stay overlay 74")
if data.get("pr74_quick_subsets_run") != 6:
    raise SystemExit("pr74 --quick must stay 6 of 16")
if data.get("pr74_quick_subsets_unmeasured") != 10:
    raise SystemExit("pr74 --quick unmeasured must stay 10")
if data.get("pr74_full_run") is not False:
    raise SystemExit("pr74 --full must stay not run")
if list(data.get("addon_tags") or []) != tags:
    raise SystemExit("addon_tags must stay the four overlay-main tags")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-ver-02-cut-hold: CLEAN — 16/16 NOT_RUN; cut script present; C02-01 not landed."
exit 0
