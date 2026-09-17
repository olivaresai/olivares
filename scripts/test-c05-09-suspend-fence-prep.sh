#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-c05-09-suspend-fence-prep.sh. Every negative control asserts the exact
# rc AND the phrase of the guard that must fire: on 2026-09-06 the live tree was red on
# `Suspend 3-arg signature drifted`, and the case "Suspend takes subscriptionID" was passing
# with rc=1 for THAT reason, not its own — its `sed` no longer matched the multi-line
# signature and the mutant never applied. Anchored substitution (subst-once.py) refuses an
# absent anchor, and the needle refuses a kill by another guard. The independent review of
# cf45f7f699 then showed a closure that mentions h.provider and returns ProviderPolar passing
# as "bound": the reader is Go's parser now (scripts/suspend-fence-guard) and the cases below
# marked "review" are that counterexample and its neighbours.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/internal/billing/fence.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/polar.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/fence.go ]; then
	printf '%s\n' "test-c05-09-suspend-fence-prep: COULD NOT LOOK — cloud/control-plane/internal/billing/fence.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/billing/polar.go ]; then
	printf '%s\n' "test-c05-09-suspend-fence-prep: COULD NOT LOOK — cloud/control-plane/internal/billing/polar.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/tenant/manager.go ]; then
	printf '%s\n' "test-c05-09-suspend-fence-prep: COULD NOT LOOK — cloud/control-plane/internal/tenant/manager.go is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-09-suspend-fence-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0509prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

JSON_REL=design/c05-09-suspend-fence-prep-2026-08-20.json
DOC_REL=design/C05-09-SUSPEND-FENCE-PREP-2026-08-20.md
MANAGER_REL=cloud/control-plane/internal/tenant/manager.go
POLAR_REL=cloud/control-plane/internal/billing/polar.go
FENCE_REL=cloud/control-plane/internal/billing/fence.go

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/cloud/control-plane/internal/billing" \
    "$TMP/tree/cloud/control-plane/internal/tenant"
  cp "$ROOT/$JSON_REL" "$TMP/tree/design/"
  cp "$ROOT/$DOC_REL" "$TMP/tree/design/"
  cp "$ROOT/$FENCE_REL" "$TMP/tree/cloud/control-plane/internal/billing/"
  cp "$ROOT/$MANAGER_REL" "$TMP/tree/cloud/control-plane/internal/tenant/"
  cp "$ROOT/$POLAR_REL" "$TMP/tree/cloud/control-plane/internal/billing/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-09-suspend-fence-prep.sh"
  # The parser the checker builds, and the cache library that builds it once per content.
  mkdir -p "$TMP/tree/scripts/suspend-fence-guard" "$TMP/tree/scripts/lib"
  cp "$ROOT/scripts/suspend-fence-guard/go.mod" "$ROOT/scripts/suspend-fence-guard/main.go" \
    "$TMP/tree/scripts/suspend-fence-guard/"
  cp "$ROOT/scripts/lib/gate-bin-cache.sh" "$TMP/tree/scripts/lib/"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-09-suspend-fence-prep.sh" \
    >"$TMP/out" 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}
subst() { # subst <fichero-relativo> <ancla> <reemplazo> — una vez, y falla si el ancla no esta
  python3 "$ROOT/scripts/lib/subst-once.py" "$TMP/tree/$1" "$2" "$3"
}
expect() { # expect <rc> <needle> <label>: rc exacto y, si rc != 0, la frase de SU guarda
  local want="$1" needle="$2" label="$3" got
  run
  got="$(cat "$TMP/rc")"
  if [ "$got" != "$want" ]; then
    bad "$label — rc=$got, want $want ($(head -c 300 "$TMP/err"))"
    return
  fi
  if [ "$want" != 0 ] && ! command grep -qF -- "$needle" "$TMP/err"; then
    bad "$label — rc=$want but the message does not name its guard; got: $(head -c 300 "$TMP/err")"
    return
  fi
  ok "$label"
}
json_set() { # json_set <key> <python-literal>
  python3 - "$TMP/tree/$JSON_REL" "$1" "$2" <<'PY'
import json, sys
p, k, v = sys.argv[1:4]
d = json.load(open(p, encoding="utf-8"))
d[k] = eval(v)
json.dump(d, open(p, "w", encoding="utf-8"))
PY
}

# The anchors, each unique in its file on the live tree (subst refuses an absent one).
SUSPEND_PARAMS='	polarCustomerID, reason, polarEventID string,'
REACTIVATE_PARAMS='	polarCustomerID, polarEventID string,'
SUSPEND_CALL='ctx, providerOrPolar(h.provider), data.Customer.ID, "billing:"+envelope.Type, webhookID,'

stage
expect 0 "" "hub-safe suspend-fence pin is CLEAN"

stage
json_set remainder_applied True
expect 1 "remainder_applied must stay false" "mutant (remainder-applied) is killed"

stage
printf '%s\n' 'func EventNotStale() {}' >>"$TMP/tree/$FENCE_REL"
expect 1 "EventNotStale landed" "mutant (EventNotStale landed) is killed"

stage
subst "$MANAGER_REL" "$SUSPEND_PARAMS" '	polarCustomerID, subscriptionID, reason, polarEventID string,'
expect 1 "Suspend takes subscriptionID" "mutant (Suspend takes subscriptionID, multi-line signature) is killed"

stage
subst "$MANAGER_REL" "$REACTIVATE_PARAMS" '	polarCustomerID, subscriptionID, polarEventID string,'
expect 1 "Reactivate takes subscriptionID" "mutant (Reactivate takes subscriptionID) is killed"

stage
subst "$MANAGER_REL" 'billing.BelongingSubscription(t.PolarSubscriptionID, subscriptionID)' 'billing.BelongingSubscription(nil, subscriptionID)'
expect 1 "UpdatePlan no longer calls BelongingSubscription" "mutant (UpdatePlan fence dropped) is killed"

stage
json_set overlay_remeasured_in_this_gate True
expect 1 "overlay remasure leaked" "mutant (overlay remasure leaked) is killed"

stage
rm -f "$TMP/tree/$JSON_REL"
expect 2 "missing" "missing JSON is COULD NOT LOOK"

# ── layout is not the contract ──────────────────────────────────────────────
stage
python3 - "$TMP/tree/$MANAGER_REL" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
old = ("func (m *Manager) Suspend(\n\tctx context.Context,\n\tprovider billing.WebhookProvider,\n"
       "\tpolarCustomerID, reason, polarEventID string,\n) (string, error) {")
new = ("func (m *Manager) Suspend(ctx context.Context, provider billing.WebhookProvider, "
       "polarCustomerID, reason, polarEventID string) (string, error) {")
if old not in s:
    sys.exit("anchor absent: the five-line Suspend signature")
open(p, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
expect 0 "" "no-fire: Suspend signature collapsed to one line stays CLEAN"

stage
subst "$MANAGER_REL" "$SUSPEND_PARAMS" '	// reason names the billing event; polarEventID is the delivery id
	polarCustomerID, reason, /* grouped */ polarEventID string,'
expect 0 "" "no-fire: comments inside the parameter list stay CLEAN"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx,
			providerOrPolar(h.provider),
			data.Customer.ID,
			"billing:" + envelope.Type,
			webhookID,'
expect 0 "" "no-fire: handler Suspend call re-laid out one argument per line stays CLEAN"

# ── provider binding: the a44a636468 scope, in the direction it was measured ──
stage
subst "$MANAGER_REL" '	provider billing.WebhookProvider,
	polarCustomerID, reason, polarEventID string,' "$SUSPEND_PARAMS"
expect 1 "Suspend signature drifted from the remeasured pin" "mutant (Suspend drops the provider parameter) is killed"

stage
subst "$MANAGER_REL" 'm.store.GetByCommerceCustomer(ctx, string(provider), polarCustomerID)' 'm.store.GetByCommerceCustomer(ctx, "polar", polarCustomerID)'
expect 1 "looks the tenant up without the provider it was given" "mutant (a method looks the tenant up with a literal provider) is killed"

stage
subst "$MANAGER_REL" 'func (m *Manager) Suspend(' 'func (m *Manager) SuspendTenant('
expect 1 "Suspend is gone from manager.go" "mutant (Suspend renamed away) is killed"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, providerOrPolar(h.provider), data.Customer.ID, data.Subscription.ID, "billing:"+envelope.Type, webhookID,'
expect 1 "handler Suspend passes subscription id" "mutant (handler Suspend passes the delivery subscription id) is killed"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, ProviderPolar, data.Customer.ID, "billing:"+envelope.Type, webhookID,'
expect 1 "handler Suspend binds the provider to ProviderPolar instead of its own h.provider" "mutant (handler Suspend binds a literal provider) is killed"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, providerOrPolar(h.provider), data.Customer.ID, "billing:"+envelope.Type, envelope.ID,'
expect 1 "handler Suspend call drifted: parameter polarEventID receives envelope.ID" "mutant (handler Suspend hands another id as the event id) is killed"

# ── unreadable is not clean ─────────────────────────────────────────────────
stage
subst "$MANAGER_REL" '	polarCustomerID, reason, polarEventID string,
) (string, error) {' '	polarCustomerID, reason, polarEventID string,
 (string, error) {'
expect 2 "cannot parse manager.go" "an unbalanced Suspend signature is COULD NOT LOOK, not a pass"

# ── review: the provider must be bound, not mentioned ───────────────────────
PROVIDER_FN='func providerOrPolar(p WebhookProvider) WebhookProvider {
	if p == "" {
		return ProviderPolar
	}
	return p
}'

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, func(_ WebhookProvider) WebhookProvider { return ProviderPolar }(h.provider), data.Customer.ID, "billing:"+envelope.Type, webhookID,'
expect 1 "which returns ProviderPolar instead of its parameter" "review: a closure that mentions h.provider and returns ProviderPolar is killed"

stage
subst "$POLAR_REL" "$PROVIDER_FN" 'func providerOrPolar(p WebhookProvider) WebhookProvider {
	if p == "" {
		return ProviderPolar
	}
	return ProviderPolar
}'
expect 1 "which returns ProviderPolar instead of its parameter" "mutant (providerOrPolar itself stops returning its parameter) is killed"

stage
subst "$POLAR_REL" "$PROVIDER_FN" 'func providerOrPolar(p WebhookProvider) WebhookProvider {
	if p == "" {
		return ProviderPolar
	}
	slog.Debug("provider", "p", p)
	return p
}'
expect 2 "cannot bind it to h.provider" "a providerOrPolar body outside the bounded grammar is COULD NOT LOOK, not a pass"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, providerOrPolar(ProviderDodo), data.Customer.ID, "billing:"+envelope.Type, webhookID,'
expect 1 "binds the provider to providerOrPolar(ProviderDodo) instead of its own h.provider" "mutant (the helper is fed a constant, not h.provider) is killed"

stage
subst "$POLAR_REL" "$SUSPEND_CALL" 'ctx, h.provider, data.Customer.ID, "billing:"+envelope.Type, webhookID,'
expect 0 "" "no-fire: handing h.provider directly is bound"

stage
subst "$MANAGER_REL" '	if err := requireCommerceAccount(provider, polarCustomerID); err != nil {' '	if err := error(nil); err != nil {
		_ = "requireCommerceAccount(provider, polarCustomerID)"'
expect 1 "accepts a provider it does not check" "review class: a body obligation quoted in a string is not a call"

stage
rm -f "$TMP/tree/scripts/suspend-fence-guard/main.go"
expect 2 "missing suspend-fence parser source" "without the parser source the verdict is COULD NOT LOOK"

# ── the evidence cannot say two things ──────────────────────────────────────
stage
json_set schema "'c05-09-suspend-fence-prep/v1'"
expect 1 "unknown schema" "mutant (the v1 JSON that predates the provider scope) is killed"

stage
json_set suspend_params "['ctx context.Context','provider billing.WebhookProvider','polarCustomerID string','subscriptionID string','reason string','polarEventID string']"
expect 1 "suspend_params names a subscription id" "mutant (JSON pins the remainder while swearing HOLD) is killed"

stage
json_set scoped_by_commerce_provider False
expect 1 "scoped_by_commerce_provider" "mutant (JSON denies the provider scope the tree has) is killed"

stage
subst "$DOC_REL" 'Suspend/Reactivate/UpdatePlan take `provider billing.WebhookProvider`' 'Suspend/Reactivate/UpdatePlan take a provider'
expect 1 "prepare doc lost the 2026-09-06 provider-scope remeasure" "mutant (doc ledger loses the provider-scope sentence) is killed"

stage
expect 0 "" "no-fire: live pin stays CLEAN"

echo "check-c05-09-suspend-fence-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
