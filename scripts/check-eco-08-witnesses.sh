#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-08-witnesses.sh — ECO-08. Witness reading not executed.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-08-witnesses: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-08-witnesses: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO08_JSON:-design/eco-08-witnesses.json}"
DOC="${OLIVARES_ECO08_DOC:-design/ECO-08-WITNESSES-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO08_CANON:-design/PRICING-CANON.md}"
BOOK="${OLIVARES_ECO08_BOOK:-sessions/campaign-prompts/OPS-CEREMONIA-TESTIGOS-2026-08-31.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"
[ -f "$BOOK" ] || cannot "missing $BOOK"

grep -q 'NOT EXECUTED' "$DOC" || fail "$DOC lost NOT EXECUTED"
if grep -qiE 'ceremony executed|apply verified|six GET succeeded' "$DOC"; then
	fail "$DOC claims a reading this lote does not have"
fi
grep -q 'UNVERIFIED' "$CANON" || fail "canon lost UNVERIFIED for the scheduled apply"
grep -q '2026-08-31' "$BOOK" || fail "runbook lost the witness date"

python3 - "$JSON" "$BOOK" <<'PY' || fail "JSON/runbook failed the ECO-08 contract"
import json, sys

required = [
    "sub_0NkP3LVKahuHbk9zhoK5Z",
    "sub_0NkP75ayilVta6oedIuN2",
    "sub_0NkOxYJS3oGLDht7HuoWF",
    "sub_0NkP3LQNqDAjMzlZ8un3y",
    "sub_0NkP3LaF1OSkPQuHYC7UQ",
    "sub_0NkP0ctMBwPDCSX4NZmSo",
]
data = json.load(open(sys.argv[1], encoding="utf-8"))
book = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "eco-08-witnesses/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("window_open") is not False:
    raise SystemExit("window_open must stay false before 2026-08-31")
if data.get("window_starts") != "2026-08-31T22:00:00Z":
    raise SystemExit("window_starts must stay 2026-08-31T22:00:00Z")
if data.get("secrets_present") is not False:
    raise SystemExit("secrets_present must stay false")
if data.get("life_check_ran") is not False:
    raise SystemExit("life_check_ran must stay false")
if data.get("apply_unverified") is not True:
    raise SystemExit("apply_unverified must stay true")
if data.get("test_mode_only") is not True:
    raise SystemExit("test_mode_only must stay true")
subs = data.get("live_subjects")
if not isinstance(subs, list):
    raise SystemExit("live_subjects missing")
if set(subs) != set(required):
    raise SystemExit("live_subjects must be the six-id set, not a substitute")
if len(subs) != 6:
    raise SystemExit("live_subjects must stay a six-row LIST")
for sid in required:
    if sid not in book:
        raise SystemExit("runbook lost live subject %s" % sid)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-08-witnesses: CLEAN — window closed; reading not executed."
exit 0
