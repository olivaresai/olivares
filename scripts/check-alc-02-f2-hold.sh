#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-02-F2: regional DR does not start. One AWS provider, no alias.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-02-f2-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-02-f2-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC02F2_JSON:-design/alc-02-f2-hold.json}"
DOC="${OLIVARES_ALC02F2_DOC:-design/ALC-02-F2-HOLD-2026-08-20.md}"
VERS="${OLIVARES_ALC02F2_VERS:-deploy/aws/versions.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$VERS" ] || cannot "missing AWS versions.tf"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'F2 does not start' "$DOC" || fail "$DOC lost F2-does-not-start"
if grep -qiE 'failover rehearsed|F2 started|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an F2 close this lote does not have"
fi
grep -q 'NEVER APPLIED' "$VERS" || fail "$VERS lost NEVER APPLIED"

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-02-f2-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("f2_started") is not False:
    raise SystemExit("f2_started must stay false")
if data.get("second_region") is not False:
    raise SystemExit("second_region must stay false")
if data.get("rpo_rto_measured") is not False:
    raise SystemExit("rpo_rto_measured must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

n="$(grep -cE '^[[:space:]]*provider[[:space:]]+"aws"' "$VERS" || true)"
[ "$n" = "1" ] || fail "expected one aws provider, got $n"
if grep -E '^[[:space:]]*alias[[:space:]]*=' "$VERS" >/dev/null; then
	fail "AWS provider grew an alias — second region would have started"
fi

say "check-alc-02-f2-hold: CLEAN — one region; F2 DR does not start."
exit 0
