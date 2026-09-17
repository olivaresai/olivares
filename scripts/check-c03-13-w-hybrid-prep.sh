#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-13 unique leftover unique vs check-c03-13-w-hybrid.sh (LOOK 2 on
# origin/main without the 2026-08-19 census doc) and unique leftover
# unique vs #1005. 0 CLEAN · 1 finding · 2 could not look.
#
# 2026-09-14 (R115 paid renewal, Root-ratified MIXED-COHORT-INTERFACE): the source half used to
# pin the defect itself — `paidIssue ? "refund_window" : "term"`, one phase for every line chosen
# by an action the barrier hardcoded to `issue`. The prep JSON and doc below are a dated record of
# the 2026-08-20 remeasure at their `hub` commit and are still checked as that record. The source
# half now pins the ratified construction instead: a per-line plan from each line's committed
# history treatment, applied by line_key, reached by the three real producers (normal join,
# operator replay, sequence rebuild). Comments are stripped before matching, so prose quoting a
# removed expression neither satisfies nor trips a pin.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-13-w-hybrid-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-13-w-hybrid-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0313P_JSON:-design/c03-13-w-hybrid-prep-2026-08-20.json}"
DOC="${OLIVARES_C0313P_DOC:-design/C03-13-W-HYBRID-PREP-2026-08-20.md}"
SRC="${OLIVARES_C0313P_SRC:-commercial/license-worker/src/license/issue-context.ts}"
COHORT="${OLIVARES_C0313P_COHORT:-commercial/license-worker/src/dodo/cohort.ts}"
CRED="${OLIVARES_C0313P_CRED:-commercial/license-worker/src/license/credential-v3.ts}"
WH="${OLIVARES_C0313P_WH:-commercial/license-worker/src/dodo/webhook.ts}"

for f in "$JSON" "$DOC" "$SRC" "$COHORT" "$CRED" "$WH"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-13-w-hybrid.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original W-hybrid check"
grep -F -q 'Unique leftover unique vs `#1005`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1005"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'W hybrid not landed' "$DOC" \
  || fail "prepare doc lost W-hybrid HOLD"
grep -F -q 'Paid issue still hardcoded refund_window' "$DOC" \
  || fail "prepare doc lost hardcoded-refund_window remasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|W hybrid landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if grep -q 'phaseForPaidIssue' "$SRC"; then
  fail "phaseForPaidIssue landed — this HOLD lote does not apply C03-13"
fi
if grep -q 'PERMISSIVE_MARGIN_DAYS' "$SRC"; then
  fail "PERMISSIVE_MARGIN_DAYS landed — this HOLD lote does not apply C03-13"
fi

python3 - "$SRC" "$COHORT" "$CRED" "$WH" <<'PY' || exit $?
import re, sys

def fail(msg):
    print(f"check-c03-13-w-hybrid-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-13-w-hybrid-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def code(path):
    try:
        text = open(path, encoding="utf-8").read()
    except Exception as e:
        cannot(f"source not readable: {e}")
    # The same normalization as test/dodo-source-anchors.test.ts: block comments and whole-line
    # `//` comments go, string literals stay.
    text = re.sub(r"/\*[\s\S]*?\*/", "", text)
    return re.sub(r"(?m)^\s*//.*$", "", text)

issue, cohort, cred, webhook = (code(p) for p in sys.argv[1:5])

def need(src, name, needle, count):
    n = src.count(needle)
    if n != count:
        fail(f"{name}: expected {count} of {needle!r}, found {n}")

def forbid(src, name, needle, why):
    if needle in src:
        fail(f"{name}: {why} ({needle!r})")

# No purchase-wide phase chosen by the aggregate commercial action.
forbid(issue, "issue-context.ts", 'paidIssue ? "refund_window" : "term"',
       "one phase for every line, chosen by the aggregate action, is back")
forbid(issue, "issue-context.ts", "purchase.action === ",
       "a phase or deadline keyed on the aggregate action is back")
# issuanceFor derives one plan entry per line from that line's treatment.
need(issue, "issue-context.ts", "const lines = linePlanFor(purchase, settledAt, paidThrough);", 1)
need(issue, "issue-context.ts", 'if (treatment === "renewed") {', 1)
need(issue, "issue-context.ts",
     'return { lineKey: line.lineKey, phase: "term", guaranteeDeadline: null, promotionHoldDeadline: null };', 1)
need(issue, "issue-context.ts", 'if (treatment === "initial_eligible") {', 1)
need(issue, "issue-context.ts", 'phase: "refund_window",', 1)

# The action and each line's treatment come from committed history, per exact line_key.
forbid(cohort, "cohort.ts", 'action: "issue",', "the barrier hardcodes the commercial action again")
need(cohort, "cohort.ts", "export function classifyPaidPurchase(", 1)
need(cohort, "cohort.ts", 'action: prior.size > 0 ? "renew" : "issue"', 1)
need(cohort, "cohort.ts", 'treatment: prior.has(line.lineKey) ? "renewed" : "initial_eligible",', 1)

# The signer applies the plan by line_key, never by position.
need(cred, "credential-v3.ts", "export function credentialFromLinePlan(", 1)
need(cred, "credential-v3.ts", "const entry = byLine.get(line.lineKey)!;", 1)

# The real producers: normal join and operator replay classify from the history they read, the
# sequence rebuild reclassifies from the history it re-read, and the route signs the plan.
need(webhook, "webhook.ts", "classifyPaidPurchase(decision.purchase, prior)", 2)
need(webhook, "webhook.ts", "classifyPaidPurchase(refreshed.purchase, prior)", 1)
need(webhook, "webhook.ts", "credentialFromLinePlan(purchase, issuance.lines, issuance.ctx)", 1)
forbid(webhook, "webhook.ts", "credentialFromPurchase(", "the route signs through the uniform adapter again")
forbid(webhook, "webhook.ts", "issuance.phase", "the route reads a purchase-wide phase again")
print("source-ok")
PY

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-13-w-hybrid-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-13-w-hybrid-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-13-w-hybrid-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("w_hybrid_landed") is not False:
    fail("w_hybrid_landed must stay false")
if data.get("paid_issue_hardcoded_refund_window") is not True:
    fail("paid_issue_hardcoded_refund_window must stay true")
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

say "check-c03-13-w-hybrid-prep: CLEAN — W hybrid HOLD; the 2026-08-20 prep record is intact; issuance derives each line's phase from its committed-history treatment through the normal, operator and rebuild producers; overlay remasure not in this gate."
exit 0
