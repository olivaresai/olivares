#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-09 unique leftover unique vs #1265 (original OPEN product PR;
# no original check-c05-09-lifecycle-fence.sh on origin/main).
# 0 CLEAN · 1 finding · 2 could not look.
#
# What this gate holds: the C05-09 remainder (Suspend/Reactivate taking the delivery's
# subscription id, EventNotStale) is NOT applied, while the UpdatePlan subscription fence
# that IS on origin/main stays. Until 2026-09-06 the positive control was one grep of the
# exact one-line signature, and it went red on a44a636468 (2026-09-05, scope tenants by
# commerce account): Suspend/Reactivate/UpdatePlan gained `provider billing.WebhookProvider`
# and were laid out over several lines. Neither is the remainder.
#
# The first correction read the Go as comment-stripped text, and the independent review of
# cf45f7f699 showed that an argument merely CONTAINING `h.provider` passed as bound: a func
# literal `func(_ WebhookProvider) WebhookProvider { return ProviderPolar }(h.provider)`
# compiled, pinned every suspension to Polar, and the gate said CLEAN. The same text model
# would have accepted a body obligation quoted inside a string. So the facts now come from
# Go's own parser (`scripts/suspend-fence-guard`): the parameter lists, the calls and
# identifiers in each method body, and — for every call the handler makes on its manager —
# each argument with a structural class. The provider grammar is bounded on purpose: the
# handler's own `h.provider`, or a one-parameter helper / func literal whose body is optional
# `if p == "" { return … }` defaults followed by `return p` (the measured providerOrPolar).
# A helper that returns something else is a finding; any other shape is "could not look".
# The parameter lists are compared with the lists pinned in the JSON, each handler argument
# is matched to the parameter it lands on, so layout is not the contract.

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
# The 2026-09-06 ledger: the provider scope is recorded as what it is, not as the remainder.
grep -F -q 'Suspend/Reactivate/UpdatePlan take `provider billing.WebhookProvider`' "$DOC" \
  || fail "prepare doc lost the 2026-09-06 provider-scope remeasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|EventNotStale landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'func BelongingSubscription' "$FENCE" \
  || fail "BelongingSubscription is gone — UpdatePlan fence on origin/main must stay"
if grep -q 'func EventNotStale' "$FENCE"; then
  fail "EventNotStale landed — this HOLD lote does not apply C05-09 remainder"
fi

if grep -q 'suspend not applied — subscription fence' "$POLAR"; then
  fail "Suspend foreign-miss settlement landed — this HOLD lote does not apply C05-09 remainder"
fi
grep -q 'reason": "foreign-subscription"' "$POLAR" \
  || fail "UpdatePlan foreign-miss 2xx settlement drifted"

# ── Facts from Go's parser, not from a text search ─────────────────────────
command -v go >/dev/null || cannot "no Go toolchain to parse manager.go and polar.go"
FENCE_GUARD="$ROOT/scripts/suspend-fence-guard"
[ -r "$FENCE_GUARD/go.mod" ] && [ -r "$FENCE_GUARD/main.go" ] \
  || cannot "missing suspend-fence parser source under scripts/suspend-fence-guard"
# shellcheck source=lib/gate-bin-cache.sh
. "$ROOT/scripts/lib/gate-bin-cache.sh" \
  || cannot "missing scripts/lib/gate-bin-cache.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || cannot "cannot create $_tmp_base"
_fence_guard_bin="$(olivares_cached_gate_bin "$FENCE_GUARD" suspend-fence-guard)" \
  || cannot "cannot build the pinned suspend-fence parser"
FACTS="$(mktemp "$_tmp_base/suspend-fence.XXXXXX")" \
  || cannot "cannot create a scratch file under $_tmp_base"
trap 'rm -f "$FACTS" "$FACTS.err"' EXIT
_facts_rc=0
"$_fence_guard_bin" "$MANAGER" "$POLAR" >"$FACTS" 2>"$FACTS.err" || _facts_rc=$?
case "$_facts_rc" in
  0) ;;
  2) cannot "$(cat "$FACTS.err")" ;;
  *) cannot "the suspend-fence parser exited unexpectedly with $_facts_rc" ;;
esac

python3 - "$JSON" "$FACTS" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-09-suspend-fence-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-09-suspend-fence-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

json_path, facts_path = sys.argv[1:3]
try:
    data = json.load(open(json_path, encoding="utf-8"))
    facts = open(facts_path, encoding="utf-8").read().splitlines()
except Exception as e:
    cannot(f"inputs not readable: {e}")

# ── the evidence ────────────────────────────────────────────────────────────
if data.get("schema") != "c05-09-suspend-fence-prep/v2":
    fail("unknown schema %r (v2 pins the parameter lists; a v1 JSON predates the provider scope)"
         % data.get("schema"))
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
if data.get("scoped_by_commerce_provider") is not True:
    fail("scoped_by_commerce_provider must record the a44a636468 tenant scope (true)")
for k in ("hub", "provider_scope_commit"):
    v = data.get(k) or ""
    if len(v) != 40 or any(c not in "0123456789abcdef" for c in v):
        fail("%s is not 40-hex" % k)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

PINS = (("suspend_params", "Suspend"), ("reactivate_params", "Reactivate"),
        ("update_plan_params", "UpdatePlan"))
pins = {}
for key, name in PINS:
    want = data.get(key)
    if (not isinstance(want, list) or not want
            or not all(isinstance(p, str) and len(p.split()) >= 2 for p in want)):
        fail("%s must be a non-empty list of 'name type' strings" % key)
    pins[name] = [" ".join(p.split()) for p in want]

def subscription_named(params):
    return [p for p in params if "subscription" in p.split()[0].lower()]

# The pins must say what the flags say: an evidence file that lists the remainder while
# swearing it is not applied is a finding on the evidence, before the source is read.
for name in ("Suspend", "Reactivate"):
    if subscription_named(pins[name]):
        fail("%s_params names a subscription id while the HOLD says the remainder is not applied"
             % name.lower())
if not subscription_named(pins["UpdatePlan"]):
    fail("update_plan_params lost subscriptionID: the UpdatePlan fence has nothing to fence")
for name in pins:
    if "provider billing.WebhookProvider" not in pins[name]:
        fail("%s pin lacks 'provider billing.WebhookProvider' while scoped_by_commerce_provider is true"
             % name)
    if pins[name][0] != "ctx context.Context":
        fail("%s pin does not start with ctx context.Context" % name)

# ── the facts: METHOD / CALL / IDENT / HANDLER / ARG, as suspend-fence-guard prints them ──
methods, calls, idents, handlers = {}, {}, {}, {}
for line in facts:
    f = line.split("\t")
    try:
        if f[0] == "METHOD" and len(f) == 4:
            methods[f[1]] = (f[2], f[3].split("|") if f[3] else [])
        elif f[0] == "CALL" and len(f) == 3:
            calls.setdefault(f[1], set()).add(f[2])
        elif f[0] == "IDENT" and len(f) == 3:
            idents.setdefault(f[1], set()).add(f[2])
        elif f[0] == "HANDLER" and len(f) == 5:
            handlers.setdefault(f[1], {})[int(f[2])] = {"recv": f[3], "argc": int(f[4]), "args": {}}
        elif f[0] == "ARG" and len(f) == 7:
            handlers[f[1]][int(f[2])]["args"][int(f[3])] = (f[4], f[5], f[6])
        else:
            raise ValueError(line)
    except (KeyError, ValueError):
        cannot("unreadable fact from the suspend-fence parser: %r" % line)

CALL_GUARD = "requireCommerceAccount(provider,polarCustomerID)"
CALL_LOOKUP = "m.store.GetByCommerceCustomer(ctx,string(provider),polarCustomerID)"
CALL_FENCE = "billing.BelongingSubscription(t.PolarSubscriptionID,subscriptionID)"
for name in ("Suspend", "Reactivate", "UpdatePlan"):
    if name not in methods:
        fail("%s is gone from manager.go" % name)
    recv, params = methods[name]
    if name != "UpdatePlan" and subscription_named(params):
        fail("%s takes subscriptionID — this HOLD lote does not apply C05-09 remainder" % name)
    if params != pins[name]:
        fail("%s signature drifted from the remeasured pin: manager.go has (%s); the JSON pins (%s)"
             % (name, ", ".join(params), ", ".join(pins[name])))
    body_calls, body_idents = calls.get(name, set()), idents.get(name, set())
    if CALL_GUARD not in body_calls:
        fail("%s accepts a provider it does not check: requireCommerceAccount(provider, "
             "polarCustomerID) is not a call in its body" % name)
    if CALL_LOOKUP not in body_calls:
        fail("%s looks the tenant up without the provider it was given: "
             "GetByCommerceCustomer(ctx, string(provider), polarCustomerID) is not a call in its body"
             % name)
    if name == "UpdatePlan":
        if CALL_FENCE not in body_calls:
            fail("UpdatePlan no longer calls BelongingSubscription")
    elif body_idents & {"BelongingSubscription", "EventNotStale"}:
        fail("%s applies a subscription fence — this HOLD lote does not apply C05-09 remainder" % name)

# ── the handler: what it hands to each method, by the parameter it lands on ──
EXPECT = {
    "Suspend": {"ctx": "ctx", "polarCustomerID": "data.Customer.ID",
                "reason": '"billing:"+envelope.Type', "polarEventID": "webhookID"},
    "Reactivate": {"ctx": "ctx", "polarCustomerID": "data.Customer.ID",
                   "polarEventID": "webhookID"},
    "UpdatePlan": {"ctx": "ctx", "polarCustomerID": "data.Customer.ID",
                   "subscriptionID": "data.Subscription.ID", "plan": "plan"},
}
for name in ("Suspend", "Reactivate", "UpdatePlan"):
    found = handlers.get(name) or {}
    if not found:
        fail("handler no longer calls manager.%s" % name)
    names = [p.split()[0] for p in pins[name]]
    for k in sorted(found):
        call = found[k]
        recv = call["recv"]
        args = [call["args"].get(i) for i in range(call["argc"])]
        if any(a is None for a in args):
            cannot("the suspend-fence parser printed an incomplete argument list for handler %s" % name)
        printed = [a[1] for a in args]
        # Comparisons ignore spaces (the parser prints gofmt's one-line form; the pins are
        # written without them); no pinned argument carries a space inside a string.
        if name != "UpdatePlan" and any("data.Subscription.ID" in a.replace(" ", "") for a in printed):
            fail("handler %s passes subscription id — this HOLD lote does not apply C05-09 remainder"
                 % name)
        if len(args) != len(names):
            fail("handler %s passes %d argument(s) to a method pinned with %d parameter(s): (%s)"
                 % (name, len(args), len(names), ", ".join(printed)))
        for pname, (klass, text, detail) in zip(names, args):
            if pname == "provider":
                if klass == "provider-direct":
                    continue
                if klass == "provider-via-helper":
                    helper, kind, why = detail.split(":", 2)
                    if kind == "identity":
                        continue
                    if kind == "returns":
                        fail("handler %s hands the provider through %s, which returns %s instead of "
                             "its parameter: the selected provider is discarded" % (name, text, why))
                    cannot("handler %s hands the provider through %s and this gate cannot bind it to "
                           "%s.provider (%s: %s); supported: %s.provider, or a one-parameter helper or "
                           "func literal that returns its parameter after optional `if p == \"\"` "
                           "defaults" % (name, text, recv, helper, why, recv))
                if klass == "provider-not-bound":
                    fail("handler %s binds the provider to %s instead of its own %s.provider"
                         % (name, text, recv))
                cannot("handler %s hands the provider through %s and this gate cannot bind it to "
                       "%s.provider; supported: %s.provider, or a one-parameter helper or func "
                       "literal that returns its parameter after optional `if p == \"\"` defaults"
                       % (name, text, recv, recv))
            elif EXPECT[name].get(pname) != text.replace(" ", ""):
                fail("handler %s call drifted: parameter %s receives %s, expected %s"
                     % (name, pname, text, EXPECT[name].get(pname)))
print("json-ok, signatures-ok, handler-binding-ok")
PY

say "check-c05-09-suspend-fence-prep: CLEAN — UpdatePlan fenced; Suspend/Reactivate + EventNotStale HOLD; provider scope read with go/parser; overlay remasure not in this gate."
exit 0
