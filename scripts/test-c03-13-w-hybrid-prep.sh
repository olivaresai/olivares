#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-13-w-hybrid-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0313prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

LW="commercial/license-worker/src"
stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/$LW/license" "$TMP/tree/$LW/dodo"
  cp "$ROOT/design/c03-13-w-hybrid-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C03-13-W-HYBRID-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/$LW/license/issue-context.ts" "$ROOT/$LW/license/credential-v3.ts" "$TMP/tree/$LW/license/"
  cp "$ROOT/$LW/dodo/cohort.ts" "$ROOT/$LW/dodo/webhook.ts" "$TMP/tree/$LW/dodo/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-13-w-hybrid-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-13-w-hybrid-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}
expect() {
  run
  if [ "$(cat "$TMP/rc")" = "$2" ]; then ok "$1"
  else bad "$1: rc=$(cat "$TMP/rc") want $2 ($(cat "$TMP/err"))"; fi
}
# A kill must be for the named reason: the rc and the message the guard gives for it.
expect_named() {
  run
  if [ "$(cat "$TMP/rc")" = "$2" ] && grep -F -q -- "$3" "$TMP/err"; then ok "$1"
  else bad "$1: rc=$(cat "$TMP/rc") want $2 naming '$3' ($(cat "$TMP/err"))"; fi
}
# Replace exactly one occurrence, or stop the battery: a mutant that silently does not apply
# would be reported as "killed" by a check that never saw it.
mutate() {
  python3 - "$TMP/tree/$1" "$2" "$3" <<'PY'
import sys
path, needle, replacement = sys.argv[1:4]
text = open(path, encoding="utf-8").read()
if text.count(needle) != 1:
    print(f"mutant did not apply: {needle!r} occurs {text.count(needle)} times in {path}", file=sys.stderr)
    sys.exit(3)
open(path, "w", encoding="utf-8").write(text.replace(needle, replacement))
PY
}
json_flag() {
  python3 - "$TMP/tree/design/c03-13-w-hybrid-prep-2026-08-20.json" "$1" <<'PY'
import json, sys
p, key = sys.argv[1:3]
d = json.load(open(p, encoding="utf-8"))
d[key] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
}

stage
expect "live per-line construction and prep record are CLEAN" 0

stage
json_flag remainder_applied
expect "mutant (remainder-applied) is killed" 1

stage
printf '\nexport function phaseForPaidIssue() { return "term" }\n' >> "$TMP/tree/$LW/license/issue-context.ts"
expect "mutant (phaseForPaidIssue landed) is killed" 1

stage
mutate "$LW/license/issue-context.ts" \
  'const lines = linePlanFor(purchase, settledAt, paidThrough);' \
  'const paidIssue = purchase.action === "issue";
  const lines = purchase.lines.map((line) => ({ lineKey: line.lineKey, phase: paidIssue ? "refund_window" : "term", guaranteeDeadline: null, promotionHoldDeadline: null }));'
expect "mutant (one aggregate phase for every line restored) is killed" 1

stage
mutate "$LW/dodo/cohort.ts" \
  'treatment: continues ? "renewed" : "initial_eligible",' \
  'treatment: prior.has(line.lineKey) ? "renewed" : "initial_eligible",'
expect_named "mutant (the exact-key treatment restored) is killed" 1 \
  "the treatment is keyed on the exact line key alone again"

stage
mutate "$LW/dodo/cohort.ts" \
  'const continues = prior.has(line.lineKey) || (code !== null && held.setCodes.has(code));' \
  'const continues = prior.has(line.lineKey);'
expect_named "mutant (the set-code arm dropped) is killed" 1 \
  "expected 1 of 'const continues = prior.has(line.lineKey) || (code !== null && held.setCodes.has(code));', found 0"

stage
mutate "$LW/dodo/cohort.ts" \
  'treatment: continues ? "renewed" : "initial_eligible",' \
  'treatment: prior.size > 0 ? "renewed" : "initial_eligible",'
expect_named "mutant (per-line classification removed: treatment from the aggregate) is killed" 1 \
  "the treatment is keyed on the aggregate history again"

stage
mutate "$LW/dodo/cohort.ts" \
  'action: prior.size > 0 ? "renew" : "issue"' \
  'action: "issue"'
expect "mutant (hardcoded issue action restored) is killed" 1

EMPTY_HISTORY='{ maxIssueSeq: 0, priorLineKeys: [], legacyUnprojected: false }'

stage
mutate "$LW/dodo/webhook.ts" \
  'classifyPaidPurchase(decision.purchase, prior, heldRightsOf(catalog, held))' \
  "classifyPaidPurchase(decision.purchase, $EMPTY_HISTORY, heldRightsOf(catalog, held))"
expect_named "mutant (the completer classifies without committed history) is killed" 1 \
  "expected 1 of 'classifyPaidPurchase(decision.purchase, prior, heldRightsOf(catalog, held))', found 0"

stage
mutate "$LW/dodo/webhook.ts" \
  'return await fulfilCompleteCohort(env, deps, payment, subscription, prior, held, {
    ...input, detailPrefix: "operator replay: ", judgedBy: "operator_replay",' \
  "return await fulfilCompleteCohort(env, deps, payment, subscription, $EMPTY_HISTORY, held, {
    ...input, detailPrefix: \"operator replay: \", judgedBy: \"operator_replay\","
expect_named "mutant (operator replay classifies without committed history) is killed" 1 \
  "judgedBy: \"operator_replay\",', found 0"

stage
mutate "$LW/dodo/webhook.ts" \
  'return await fulfilCompleteCohort(env, deps, payment, subscription, prior, held, {
    ...input, rebuilds: rebuilds + 1,' \
  "return await fulfilCompleteCohort(env, deps, payment, subscription, $EMPTY_HISTORY, held, {
    ...input, rebuilds: rebuilds + 1,"
expect_named "mutant (sequence rebuild ignores the re-read history) is killed" 1 \
  "...input, rebuilds: rebuilds + 1,', found 0"

stage
mutate "$LW/dodo/webhook.ts" \
  'const prior = await deps.store.readDodoPriorGrantState(
    purchase.businessId,
    purchase.subscriptionId,
    purchase.paymentId,
  );' \
  'const prior = { maxIssueSeq: error.observedMaxIssueSeq, priorLineKeys: [], legacyUnprojected: false };'
expect_named "mutant (sequence rebuild does not re-read the history) is killed" 1 \
  "purchase.paymentId,\\n  );', found 0"

stage
printf '\nexport function secondClassifier(p: never, h: never) { return classifyPaidPurchase(p, h, undefined as never); }\n' \
  >> "$TMP/tree/$LW/dodo/webhook.ts"
expect_named "mutant (a second classification site) is killed" 1 \
  "expected 1 of 'classifyPaidPurchase(', found 2"

stage
mutate "$LW/dodo/webhook.ts" \
  'credentialFromLinePlan(purchase, issuance.lines, issuance.ctx)' \
  'credentialFromPurchase(purchase, "refund_window", { ...issuance.ctx, guaranteeDeadline: null, promotionHoldDeadline: null })'
expect "mutant (route signs one uniform phase) is killed" 1

stage
mutate "$LW/license/credential-v3.ts" \
  'const entry = byLine.get(line.lineKey)!;' \
  'const entry = plan[purchase.lines.indexOf(line)];'
expect "mutant (plan applied by position) is killed" 1

stage
json_flag overlay_remeasured_in_this_gate
expect "mutant (overlay remasure leaked) is killed" 1

stage
printf '\n// history: paidIssue ? "refund_window" : "term" was one phase for every line\n' >> \
  "$TMP/tree/$LW/license/issue-context.ts"
printf '\n// history: credentialFromPurchase(purchase, issuance.phase, issuance.ctx) signed it\n' >> \
  "$TMP/tree/$LW/dodo/webhook.ts"
expect "no-fire: prose quoting the removed expressions stays CLEAN" 0

stage
rm -f "$TMP/tree/design/c03-13-w-hybrid-prep-2026-08-20.json"
expect "missing JSON is COULD NOT LOOK" 2

stage
rm -f "$TMP/tree/$LW/dodo/webhook.ts"
expect "missing producer source is COULD NOT LOOK" 2

stage
expect "no-fire: live construction stays CLEAN" 0

echo "check-c03-13-w-hybrid-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
