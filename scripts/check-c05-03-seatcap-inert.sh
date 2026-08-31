#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C05-03 remainder: sold Cloud map max_seats=0 makes AdmitSeat inert.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-03-seatcap-inert: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-03-seatcap-inert: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0503_JSON:-design/c05-03-seatcap-inert.json}"
DOC="${OLIVARES_C0503_DOC:-design/C05-03-SEATCAP-INERT-2026-08-20.md}"
MAP="${OLIVARES_C0503_MAP:-cloud/control-plane/internal/billing/dodo-cloud-product-map.json}"
ENT="${OLIVARES_C0503_ENT:-cloud/control-plane/internal/billing/entitlement.go}"
ADMIN="${OLIVARES_C0503_ADMIN:-cloud/control-plane/internal/admin/api.go}"
MGR="${OLIVARES_C0503_MGR:-cloud/control-plane/internal/tenant/manager.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$MAP" ] || cannot "missing $MAP"
[ -f "$ENT" ] || cannot "missing entitlement.go"
[ -f "$ADMIN" ] || cannot "missing admin api"
[ -f "$MGR" ] || cannot "missing tenant manager"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'Seat six on Cloud Standard' "$DOC" || fail "$DOC lost seat-six fact"
if grep -qiE 'sixth seat refused|C05-03 closed|FIRMA A claimed|sandbox e2e passed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$MAP" "$ENT" "$ADMIN" "$MGR" <<'PY' || fail "JSON/map/doors drifted"
import json, re, sys

monthly = "pdt_0NlE7N9AZ9CV7wNAemXAO"
annual = "pdt_0NlE7ZtwL8GfOeYefL7M8"

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c05-03-seatcap-inert/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("sixth_seat_refused") is not False:
    raise SystemExit("sixth_seat_refused must stay false")
if data.get("seat_cap_inert") is not True:
    raise SystemExit("seat_cap_inert must stay true")
if data.get("monthly_max_seats") != 0:
    raise SystemExit("monthly_max_seats must stay 0")
if data.get("annual_max_seats") != 0:
    raise SystemExit("annual_max_seats must stay 0")
if data.get("features_empty") is not True:
    raise SystemExit("features_empty must stay true")
if data.get("extra_seat_door_wired") is not True:
    raise SystemExit("extra_seat_door_wired must stay true")
if data.get("sandbox_e2e_run") is not False:
    raise SystemExit("sandbox_e2e_run must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

mp = json.load(open(sys.argv[2], encoding="utf-8"))
prods = mp.get("products") or {}
for sku, want_tier in ((monthly, "cloud-standard-m"), (annual, "cloud-standard-y")):
    row = prods.get(sku) or {}
    if row.get("tier") != want_tier:
        raise SystemExit("%s lost tier %s" % (sku, want_tier))
    if row.get("max_seats") != 0:
        raise SystemExit("%s max_seats is %r, not 0" % (sku, row.get("max_seats")))
    feats = row.get("features")
    if feats is None or list(feats) != []:
        raise SystemExit("%s features is %r, not empty" % (sku, feats))

ent = open(sys.argv[3], encoding="utf-8").read()
if "func AdmitSeat(" not in ent:
    raise SystemExit("AdmitSeat missing")
if "maxSeats <= 0" not in ent:
    raise SystemExit("AdmitSeat lost the unlimited branch")
if "func CheckFeature(" not in ent:
    raise SystemExit("CheckFeature missing")

admin = open(sys.argv[4], encoding="utf-8").read()
if "billing.CheckAdmitSeat" not in admin:
    raise SystemExit("admin lost CheckAdmitSeat")
if "billing.CheckFeature" not in admin:
    raise SystemExit("admin lost CheckFeature")

mgr = open(sys.argv[5], encoding="utf-8").read()
# first-owner invite is seat 1; the sixth-seat door is admin.admitSeat
if "func (m *Manager) inviteFirstOwner" not in mgr:
    raise SystemExit("inviteFirstOwner missing")
start = mgr.index("func (m *Manager) inviteFirstOwner")
rest = mgr[start:]
end = rest.find("\nfunc ", 1)
block = rest if end < 0 else rest[:end]
if "CheckAdmitSeat" in block:
    raise SystemExit("inviteFirstOwner now calls CheckAdmitSeat; remasure")
PY

say "check-c05-03-seatcap-inert: CLEAN — sold Cloud map max_seats=0; extra-seat door inert; HOLD."
exit 0
