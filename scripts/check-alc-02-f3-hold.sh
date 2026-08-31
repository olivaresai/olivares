#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-02-F3: active-active routing does not start.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-02-f3-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-02-f3-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC02F3_JSON:-design/alc-02-f3-hold.json}"
DOC="${OLIVARES_ALC02F3_DOC:-design/ALC-02-F3-HOLD-2026-08-20.md}"
AWS="${OLIVARES_ALC02F3_AWS:-deploy/aws}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -d "$AWS" ] || cannot "missing AWS estate directory"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'F3 does not start' "$DOC" || fail "$DOC lost F3-does-not-start"
if grep -qiE 'active-active built|F3 started|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an F3 close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-02-f3-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("f3_started") is not False:
    raise SystemExit("f3_started must stay false")
if data.get("active_active") is not False:
    raise SystemExit("active_active must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

if grep -R -E 'aws_route53_record|aws_globalaccelerator|failover_routing' \
	--include='*.tf' "$AWS" >/dev/null 2>&1; then
	fail "estate grew routed multi-region resources while F3 is HOLD"
fi

say "check-alc-02-f3-hold: CLEAN — no routed multi-region; F3 does not start."
exit 0
