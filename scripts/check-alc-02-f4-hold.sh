#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# ALC-02-F4: per-tenant CMEK does not start. One RDS volume key only.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-02-f4-hold: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-02-f4-hold: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC02F4_JSON:-design/alc-02-f4-hold.json}"
DOC="${OLIVARES_ALC02F4_DOC:-design/ALC-02-F4-HOLD-2026-08-20.md}"
AWS="${OLIVARES_ALC02F4_AWS:-deploy/aws}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -d "$AWS" ] || cannot "missing AWS estate directory"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'F4 does not start' "$DOC" || fail "$DOC lost F4-does-not-start"
if grep -qiE 'CMEK applied|F4 started|per-tenant key live|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an F4 close this lote does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-02-f4-hold/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("f4_started") is not False:
    raise SystemExit("f4_started must stay false")
if data.get("per_tenant_cmek") is not False:
    raise SystemExit("per_tenant_cmek must stay false")
want = data.get("plane_kms_keys")
if want != ["logs", "rds", "secrets", "tasks"]:
    raise SystemExit("plane_kms_keys must stay the four C04 plane keys, got %r" % want)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

python3 - "$AWS" <<'PY' || fail "KMS census drifted"
import os, re, sys

root = sys.argv[1]
keys = []
for dirpath, _, files in os.walk(root):
    for name in files:
        if not name.endswith(".tf"):
            continue
        path = os.path.join(dirpath, name)
        text = open(path, encoding="utf-8").read()
        keys.extend(re.findall(r'resource\s+"aws_kms_key"\s+"([^"]+)"', text))
allowed = {"logs", "rds", "secrets", "tasks"}
forbidden = re.compile(r"tenant|archive|cmek|silo|per.?tenant", re.I)
bad = [k for k in keys if k not in allowed or forbidden.search(k)]
if bad:
    raise SystemExit("unexpected KMS keys %r (census %r)" % (bad, keys))
print("plane-kms-ok %s" % ",".join(sorted(set(keys))))
PY

say "check-alc-02-f4-hold: CLEAN — C04 plane keys only; per-tenant CMEK does not start."
exit 0
