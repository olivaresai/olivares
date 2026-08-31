#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-16-exit-restore.sh — ECO-16. Exit bundle not restored.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-16-exit-restore: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-16-exit-restore: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO16_JSON:-design/eco-16-exit-restore.json}"
DOC="${OLIVARES_ECO16_DOC:-design/ECO-16-EXIT-RESTORE-2026-08-19.md}"
CANON="${OLIVARES_ECO16_CANON:-design/PRICING-CANON.md}"
CLOUD="${OLIVARES_ECO16_CLOUD:-cloud}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT RESTORED' "$DOC" || fail "$DOC lost NOT RESTORED"
if grep -qiE 'restore succeeded|restored a tenant' "$DOC"; then
	fail "$DOC claims a restore this lote does not have"
fi

python3 - "$JSON" "$CANON" "$CLOUD" <<'PY' || fail "JSON/canon/cloud failed the ECO-16 contract"
import json, os, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
canon = open(sys.argv[2], encoding="utf-8").read()
cloud = sys.argv[3]
if data.get("restored") is not False:
    raise SystemExit("restored must be false")
if data.get("format") != "olivares.cloud-export.v1":
    raise SystemExit("format must stay olivares.cloud-export.v1")
if data.get("restore_target") != "self_hosted.business":
    raise SystemExit("restore_target must stay self_hosted.business")
if data.get("read_export_window_days") != 30:
    raise SystemExit("window must stay 30")
if data.get("destructive_auto_delete") is not False:
    raise SystemExit("destructive_auto_delete must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
if "olivares.cloud-export.v1" not in canon:
    raise SystemExit("canon lost the export format")
if "restore_target: self_hosted.business" not in canon:
    raise SystemExit("canon lost restore_target")
if "read_export_window_days: 30" not in canon:
    raise SystemExit("canon lost the 30-day window")
hits = 0
if os.path.isdir(cloud):
    for root, _dirs, files in os.walk(cloud):
        for name in files:
            if not name.endswith(".go"):
                continue
            path = os.path.join(root, name)
            try:
                text = open(path, encoding="utf-8").read()
            except OSError:
                continue
            if "cloud-export" in text:
                hits += 1
if hits != 0:
    raise SystemExit("cloud/ Go now mentions cloud-export (%d files); lote still says false" % hits)
if data.get("format_in_cloud_go") is not False:
    raise SystemExit("format_in_cloud_go must be false while hits=0")
PY

say "check-eco-16-exit-restore: CLEAN — format sourced; not restored; no cloud/ exporter."
exit 0
