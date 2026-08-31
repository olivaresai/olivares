#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-09 unique leftover unique vs #1265 (original OPEN product PR;
# no original check-c05-09-lifecycle-fence.sh on origin/main).
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-09-suspend-fence-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-09-suspend-fence-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0509P_JSON:-design/c05-09-suspend-fence-prep-2026-08-20.json}"
DOC="${OLIVARES_C0509P_DOC:-design/C05-09-SUSPEND-FENCE-PREP-2026-08-20.md}"
FENCE="${OLIVARES_C0509P_FENCE:-cloud/control-plane/internal/billing/fence.go}"
MANAGER="${OLIVARES_C0509P_MANAGER:-cloud/control-plane/internal/tenant/manager.go}"
POLAR="${OLIVARES_C0509P_POLAR:-cloud/control-plane/internal/billing/polar.go}"

for f in "$JSON" "$DOC" "$FENCE" "$MANAGER" "$POLAR"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#1265`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1265"
grep -F -q 'Unique leftover unique vs `hub-comercio/c05-09-lifecycle-fence`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Remainder is Suspend/Reactivate + EventNotStale' "$DOC" \
  || fail "prepare doc lost remainder sentence"
grep -F -q 'UpdatePlan BelongingSubscription already on origin/main' "$DOC" \
  || fail "prepare doc lost UpdatePlan remasure"
grep -F -q 'Polar missing outer timestamp is not refuse' "$DOC" \
  || fail "prepare doc lost Polar-timestamp remainder spec"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|EventNotStale landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'func BelongingSubscription' "$FENCE" \
  || fail "BelongingSubscription is gone — UpdatePlan fence on origin/main must stay"
if grep -q 'func EventNotStale' "$FENCE"; then
  fail "EventNotStale landed — this HOLD lote does not apply C05-09 remainder"
fi

grep -q 'billing.BelongingSubscription(t.PolarSubscriptionID, subscriptionID)' "$MANAGER" \
  || fail "UpdatePlan no longer calls BelongingSubscription"
if grep -q 'func (m \*Manager) Suspend(ctx context.Context, polarCustomerID, subscriptionID, reason, polarEventID string)' "$MANAGER"; then
  fail "Suspend takes subscriptionID — this HOLD lote does not apply C05-09 remainder"
fi
if grep -q 'func (m \*Manager) Reactivate(ctx context.Context, polarCustomerID, subscriptionID, polarEventID string)' "$MANAGER"; then
  fail "Reactivate takes subscriptionID — this HOLD lote does not apply C05-09 remainder"
fi
grep -q 'func (m \*Manager) Suspend(ctx context.Context, polarCustomerID, reason, polarEventID string)' "$MANAGER" \
  || fail "Suspend 3-arg signature drifted"
grep -q 'func (m \*Manager) Reactivate(ctx context.Context, polarCustomerID, polarEventID string)' "$MANAGER" \
  || fail "Reactivate 2-arg signature drifted"

if grep -q 'suspend not applied — subscription fence' "$POLAR"; then
  fail "Suspend foreign-miss settlement landed — this HOLD lote does not apply C05-09 remainder"
fi
if grep -q 'h.manager.Suspend(ctx, data.Customer.ID, data.Subscription.ID' "$POLAR"; then
  fail "handler Suspend passes subscription id — this HOLD lote does not apply C05-09 remainder"
fi
grep -q 'h.manager.Suspend(ctx, data.Customer.ID, "billing:"+envelope.Type, webhookID)' "$POLAR" \
  || fail "handler Suspend call drifted"
grep -q 'reason": "foreign-subscription"' "$POLAR" \
  || fail "UpdatePlan foreign-miss 2xx settlement drifted"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-09-suspend-fence-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-09-suspend-fence-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-09-suspend-fence-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("lote") != "C05-09":
    fail("lote drifted")
if data.get("update_plan_fenced") is not True:
    fail("update_plan_fenced must stay true")
if data.get("suspend_takes_subscription_id") is not False:
    fail("suspend_takes_subscription_id must stay false")
if data.get("reactivate_takes_subscription_id") is not False:
    fail("reactivate_takes_subscription_id must stay false")
if data.get("event_not_stale_present") is not False:
    fail("event_not_stale_present must stay false")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c05-09-suspend-fence-prep: CLEAN — UpdatePlan fenced; Suspend/Reactivate + EventNotStale HOLD; overlay remasure not in this gate."
exit 0
