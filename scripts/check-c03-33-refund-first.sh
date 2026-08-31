#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-33 — the 08-10 sequential refund-first race is dead. Old symbols
# stay absent; issue_grant stays foreign; schedule_end keeps the
# succeeded-refund-saga precondition. Does not restack #662.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-33-refund-first: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-33-refund-first: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0333_JSON:-design/c03-33-refund-first.json}"
DOC="${OLIVARES_C0333_DOC:-design/C03-33-REFUND-FIRST-REMEASURE-2026-08-20.md}"
PUMP="commercial/commerce/internal/pump/pump.go"
MAIN="commercial/commerce/cmd/commerce/main.go"
OUTBOX="commercial/commerce/internal/pump/outbox.go"
COMM="commercial/commerce"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$PUMP" ] || cannot "missing $PUMP"
[ -f "$MAIN" ] || cannot "missing $MAIN"
[ -f "$OUTBOX" ] || cannot "missing $OUTBOX"
[ -d "$COMM" ] || cannot "missing $COMM"

grep -Fq 'sequential race is dead' "$DOC" \
  || fail "doc lost the refutation"
grep -Fq 'does **not** make `issue_grant` local' "$DOC" \
  || fail "doc no longer refuses a local issue_grant"
grep -Fiq 'does not restack' "$DOC" \
  || fail "doc dropped the #662 refusal"
if grep -qiE 'enqueueGrants still runs|race survives in main.go' "$DOC"; then
  fail "doc claims the 08-10 sequential race still lives"
fi

python3 - "$JSON" "$PUMP" "$MAIN" "$OUTBOX" "$COMM" <<'PY' || exit $?
import json, os, re, sys

def fail(msg):
    print("check-c03-33-refund-first: FAIL — %s" % msg, file=sys.stderr)
    raise SystemExit(1)

def cannot(msg):
    print("check-c03-33-refund-first: COULD NOT LOOK — %s" % msg, file=sys.stderr)
    raise SystemExit(2)

data = json.load(open(sys.argv[1], encoding="utf-8"))
for key, want in (
    ("sequential_race_dead", True),
    ("old_symbols_absent", True),
    ("issue_grant_local", False),
    ("schedule_end_requires_succeeded_refund_saga", True),
    ("does_not_restack_662", True),
):
    if data.get(key) is not want:
        fail("%s must stay %r" % (key, want))
hub = data.get("hub") or ""
if not re.fullmatch(r"[0-9a-f]{40}", hub):
    fail("hub is not a 40-hex object id")

pump = open(sys.argv[2], encoding="utf-8").read()
main = open(sys.argv[3], encoding="utf-8").read()
outbox = open(sys.argv[4], encoding="utf-8").read()
comm = sys.argv[5]

old = ("enqueueGrants", "DeliverPending", "reconciler.Reconcile")
hits = []
for dirpath, _, files in os.walk(comm):
    for name in files:
        if not name.endswith(".go"):
            continue
        path = os.path.join(dirpath, name)
        text = open(path, encoding="utf-8").read()
        for sym in old:
            if sym in text:
                hits.append("%s:%s" % (path, sym))
if hits:
    fail("old sequential-race symbols returned: %s" % ", ".join(hits[:8]))

# Ownership rows: Kind then the next Local/Owner field.
rows = re.findall(
    r'Kind:\s+"([^"]+)",\s*(Local:\s+(true|false),|Owner:)',
    pump,
)
kinds = {}
for kind, marker, local in rows:
    kinds[kind] = "local" if marker.startswith("Local") and local == "true" else "foreign"

if kinds.get("schedule_end") != "local":
    fail("schedule_end is not Local on GrantCommandOwners")
if kinds.get("issue_grant") == "local":
    fail("issue_grant is Local — rights projection would live in commerce")
if "issue_grant" not in kinds:
    fail("issue_grant missing from GrantCommandOwners")
if "license-worker" not in pump:
    fail("issue_grant lost its worker owner")

if "succeeded refund saga" not in pump:
    fail("schedule_end Why lost the succeeded refund saga precondition")
if "SUCCEEDED refund saga" not in outbox and "succeeded refund saga" not in outbox:
    fail("ScheduleEndExecutor lost the refund-saga precondition")

if '"schedule_end"' not in main or "ScheduleEndExecutor" not in main:
    fail("composition root no longer wires schedule_end")
if re.search(r'"issue_grant"\s*:', main):
    fail("composition root wires issue_grant — F-06")
PY

say "check-c03-33-refund-first: CLEAN — old symbols absent; issue_grant foreign; schedule_end needs a succeeded refund saga."
exit 0
