#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c05-both-pdt-applied.sh — C05. Both Cloud pdt_ apply a plan.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-both-pdt-applied: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-both-pdt-applied: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05PDT_JSON:-design/c05-both-pdt-applied.json}"
DOC="${OLIVARES_C05PDT_DOC:-design/C05-BOTH-PDT-APPLIED-2026-08-20.md}"
MAP="${OLIVARES_C05PDT_MAP:-cloud/control-plane/internal/billing/dodo-cloud-product-map.json}"
WEBHOOK="${OLIVARES_C05PDT_WEBHOOK:-cloud/control-plane/internal/billing/dodoenvelope_test.go}"
TENANT="${OLIVARES_C05PDT_TENANT:-cloud/control-plane/internal/tenant/manager_test.go}"
HOLDER="${OLIVARES_C05PDT_HOLDER:-cloud/control-plane/internal/tenant/manager.go}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$MAP" ] || cannot "missing $MAP"
[ -f "$WEBHOOK" ] || cannot "missing $WEBHOOK"
[ -f "$TENANT" ] || cannot "missing $TENANT"
[ -f "$HOLDER" ] || cannot "missing $HOLDER"

grep -q 'sandbox e2e NOT RUN' "$DOC" || fail "$DOC lost sandbox e2e NOT RUN"
if grep -qiE 'sandbox e2e passed|FIRMA A claimed|bytes are real' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'CreateUser' "$HOLDER" || fail "manager lost CreateUser invite"
grep -q 'GrantMembership' "$HOLDER" || fail "manager lost GrantMembership invite"

python3 - "$JSON" "$MAP" "$WEBHOOK" "$TENANT" <<'PY' || fail "JSON/map/tests failed the C05 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
mp = json.load(open(sys.argv[2], encoding="utf-8"))
webhook = open(sys.argv[3], encoding="utf-8").read()
tenant = open(sys.argv[4], encoding="utf-8").read()

if data.get("schema") != "c05-both-pdt-applied/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("sandbox_e2e_run") is not False:
    raise SystemExit("sandbox_e2e_run must stay false")
if data.get("invite_via") != "users-and-memberships":
    raise SystemExit("invite_via must stay users-and-memberships")
if data.get("onboard_aal3") is not True:
    raise SystemExit("onboard_aal3 must stay true")
monthly = data.get("monthly_pdt")
annual = data.get("annual_pdt")
if monthly != "pdt_0NlE7N9AZ9CV7wNAemXAO":
    raise SystemExit("monthly_pdt drifted")
if annual != "pdt_0NlE7ZtwL8GfOeYefL7M8":
    raise SystemExit("annual_pdt drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

prods = mp.get("products") or {}
if monthly not in prods or prods[monthly].get("tier") != "cloud-standard-m":
    raise SystemExit("map lost monthly Cloud SKU")
if annual not in prods or prods[annual].get("tier") != "cloud-standard-y":
    raise SystemExit("map lost annual Cloud SKU")
if monthly not in webhook or annual not in webhook:
    raise SystemExit("webhook tests lost a Cloud pdt_")
if "cloud-standard-y" not in webhook:
    raise SystemExit("webhook tests lost the annual tier")
if "cloud-standard-m" not in tenant or "cloud-standard-y" not in tenant:
    raise SystemExit("tenant tests lost a Cloud tier")
if "CreateUser" not in tenant or "GrantMembership" not in tenant:
    raise SystemExit("tenant tests lost the invite assertions")
PY

say "check-c05-both-pdt-applied: CLEAN — both Cloud pdt_ apply a plan; invite via users+memberships; sandbox e2e not run."
exit 0
