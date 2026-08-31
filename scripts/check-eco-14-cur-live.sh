#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ECO-14: live CUR is UNKNOWN. Envelopes are sourced from the canon.
# C04 is unapplied. Cloud sales lanes stay closed.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-14-cur-live: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-14-cur-live: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO14_JSON:-design/eco-14-cur-live.json}"
DOC="${OLIVARES_ECO14_DOC:-design/ECO-14-CUR-LIVE-2026-08-20.md}"
CANON="${OLIVARES_ECO14_CANON:-design/PRICING-CANON.md}"
ESTATE="${OLIVARES_ECO14_ESTATE:-deploy/aws/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"
[ -f "$ESTATE" ] || cannot "missing $ESTATE"

grep -q 'NOT OBSERVED' "$DOC" || fail "$DOC lost NOT OBSERVED"
if grep -qiE 'live CUR is [0-9]|Cost Explorer returned|opened the lane' "$DOC"; then
  fail "$DOC claims a live CUR this lote does not have"
fi

python3 - "$JSON" "$CANON" <<'PY' || fail "JSON/canon failed the ECO-14 contract"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
canon = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "eco-14-cur-live/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("observed") != "UNKNOWN":
    raise SystemExit("observed must be UNKNOWN, not an invented USD")
if data.get("c04_applied") is not False:
    raise SystemExit("c04_applied must be false")
if data.get("sales_lane_opened") is not False:
    raise SystemExit("sales_lane_opened must be false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s is %r, want UNKNOWN" % (k, data.get(k)))
if data.get("cur_base_envelope_minor") != 14000:
    raise SystemExit("CUR-BASE envelope must stay 14000 minor")
if data.get("cur_scale_envelope_minor") != 17500:
    raise SystemExit("CUR-SCALE envelope must stay 17500 minor")
if data.get("cur_scale_status") != "provisional-tripwire-not-observed-cost":
    raise SystemExit("CUR-SCALE status must stay the canon tripwire")
if data.get("canon_raw_aws_is_live_cur") is not False:
    raise SystemExit("raw_aws_cost is not live CUR")
if data.get("c04_apply_marker") != "NEVER APPLIED":
    raise SystemExit("c04_apply_marker must stay NEVER APPLIED")
if "protected_envelope_minor_per_shard_month: 14000" not in canon:
    raise SystemExit("canon lost CUR-BASE 14000")
if "protected_envelope_minor_per_shard_month: 17500" not in canon:
    raise SystemExit("canon lost CUR-SCALE 17500")
if "provisional-tripwire-not-observed-cost" not in canon:
    raise SystemExit("canon lost CUR-SCALE tripwire status")
m = re.search(r"(?m)^  cloud-standard-monthly:\n    state: (\S+)", canon)
if not m or m.group(1) != "closed":
    raise SystemExit("cloud-standard-monthly must stay closed")
m = re.search(r"(?m)^  cloud-scale-monthly:\n    state: (\S+)", canon)
if not m or m.group(1) != "closed":
    raise SystemExit("cloud-scale-monthly must stay closed")
PY

grep -q 'NEVER APPLIED' "$ESTATE" || fail "$ESTATE lost NEVER APPLIED (C04 still unapplied)"

say "check-eco-14-cur-live: CLEAN — CUR UNKNOWN; envelopes sourced; C04 unapplied; lanes closed."
exit 0
