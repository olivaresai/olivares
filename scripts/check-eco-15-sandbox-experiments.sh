#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-15-sandbox-experiments.sh — ECO-15. Two class-B sandbox
# experiments stay HOLD until a capture exists. Lanes stay closed.
# Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-15-sandbox-experiments: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-15-sandbox-experiments: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO15_JSON:-design/eco-15-sandbox-experiments.json}"
DOC="${OLIVARES_ECO15_DOC:-design/ECO-15-SANDBOX-EXPERIMENTS-2026-08-19.md}"
CANON="${OLIVARES_ECO15_CANON:-design/PRICING-CANON.md}"
EVID="${OLIVARES_ECO15_EVID:-commercial/dodo-sandbox/evidence}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT RUN' "$DOC" || fail "$DOC lost NOT RUN"
if grep -qiE 'experiments ran|capture written|opened the lane' "$DOC"; then
	fail "$DOC claims a run this lote does not have"
fi

python3 - "$JSON" "$CANON" "$EVID" <<'PY' || fail "JSON/canon/evidence failed the ECO-15 contract"
import json, os, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
canon = open(sys.argv[2], encoding="utf-8").read()
evid = sys.argv[3]
if data.get("implemented") is not False:
    raise SystemExit("implemented must be false")
if data.get("ran") is not False:
    raise SystemExit("ran must be false")
if data.get("sales_lane_opened") is not False:
    raise SystemExit("sales_lane_opened must be false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s is %r, want UNKNOWN" % (k, data.get(k)))
want = [
    ("annual-renewal-price-vintage-sandbox", "exact-sandbox-annual-renewal-honors-price-vintage"),
    ("exact-quantity-billing-sandbox", "exact-sandbox-quantity-2-billing-capture"),
]
rows = data.get("experiments") or []
got = [(r.get("id"), r.get("evidence")) for r in rows]
if got != want:
    raise SystemExit("experiments %s, want %s" % (got, want))
for r in rows:
    if r.get("status") != "HOLD":
        raise SystemExit("%s status is %r, want HOLD" % (r.get("id"), r.get("status")))
    if r.get("capture") is not None:
        raise SystemExit("%s capture must be null while ran is false" % r.get("id"))
# Both lanes stay closed. Match the YAML block, not a comment.
import re
for lane in ("self-hosted-annual", "cloud-scale-monthly"):
    m = re.search(r"(?m)^  %s:\n    state: (\S+)" % re.escape(lane), canon)
    if not m:
        raise SystemExit("canon has no %s state" % lane)
    if m.group(1) != "closed":
        raise SystemExit("%s state is %s, want closed" % (lane, m.group(1)))
# Evidence names must not already exist as captures while we declare HOLD.
if os.path.isdir(evid):
    names = []
    for root, _dirs, files in os.walk(evid):
        names.extend(files)
    blob = " ".join(names)
    for _id, evidence in want:
        if evidence in blob:
            raise SystemExit("evidence %s already exists; lote still says HOLD" % evidence)
PY

say "check-eco-15-sandbox-experiments: CLEAN — two HOLDs, lanes closed, not run."
exit 0
