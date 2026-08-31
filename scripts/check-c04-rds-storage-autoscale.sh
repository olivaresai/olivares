#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS max_allocated_storage 100 > allocated 20.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-storage-autoscale: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-storage-autoscale: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04SA_JSON:-design/c04-rds-storage-autoscale.json}"
DOC="${OLIVARES_C04SA_DOC:-design/C04-RDS-STORAGE-AUTOSCALE-2026-08-20.md}"
TF="${OLIVARES_C04SA_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'strictly greater' "$DOC" || fail "$DOC lost strictly-greater"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-storage-autoscale/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("allocated_gb") != 20:
    raise SystemExit("allocated_gb must stay 20")
if data.get("max_allocated_gb") != 100:
    raise SystemExit("max_allocated_gb must stay 100")
if data.get("max_allocated_gb") <= data.get("allocated_gb"):
    raise SystemExit("max must be strictly greater than allocated")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
keys = re.findall(r'resource\s+"aws_kms_key"\s+"([^"]+)"', tf)
if keys != ["rds"]:
    raise SystemExit("expected exactly one aws_kms_key named rds, got %r" % keys)
m_alloc = re.search(r"(?m)^[ \t]*allocated_storage\s*=\s*(\d+)", tf)
m_max = re.search(r"(?m)^[ \t]*max_allocated_storage\s*=\s*(\d+)", tf)
if not m_alloc:
    raise SystemExit("allocated_storage missing")
if not m_max:
    raise SystemExit("max_allocated_storage missing")
alloc = int(m_alloc.group(1))
mx = int(m_max.group(1))
if alloc != 20:
    raise SystemExit("allocated_storage drifted from 20")
if mx != 100:
    raise SystemExit("max_allocated_storage drifted from 100")
if mx <= alloc:
    raise SystemExit("max_allocated_storage is not strictly greater")
if re.search(r"max_allocated_storage\s*=\s*0\b", tf):
    raise SystemExit("max_allocated_storage 0 is off")
PY

say "check-c04-rds-storage-autoscale: CLEAN — max 100 > floor 20; estate unapplied."
exit 0
