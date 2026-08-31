#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS Enhanced Monitoring 60s on a dedicated IAM role.
# Estate unapplied. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-em: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-em: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04EM_JSON:-design/c04-rds-em.json}"
DOC="${OLIVARES_C04EM_DOC:-design/C04-RDS-EM-2026-08-20.md}"
TF="${OLIVARES_C04EM_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'coarsest enabled interval' "$DOC" || fail "$DOC lost 60s coarsest"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-em/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("enhanced_monitoring") is not True:
    raise SystemExit("enhanced_monitoring must be true")
if data.get("interval_seconds") != 60:
    raise SystemExit("interval_seconds must stay 60")
if data.get("second_kms_key") is not False:
    raise SystemExit("second_kms_key must stay false")
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
if not re.search(r'resource\s+"aws_iam_role"\s+"rds_monitoring"', tf):
    raise SystemExit("rds_monitoring role missing")
if "AmazonRDSEnhancedMonitoringRole" not in tf:
    raise SystemExit("managed Enhanced Monitoring policy lost")
if not re.search(r"monitoring_interval\s*=\s*60", tf):
    raise SystemExit("monitoring_interval lost 60")
if not re.search(
    r"monitoring_role_arn\s*=\s*aws_iam_role\.rds_monitoring\.arn", tf
):
    raise SystemExit("instance is not bound to rds_monitoring")
if re.search(r"monitoring_interval\s*=\s*0", tf):
    raise SystemExit("monitoring_interval 0 is off")
PY

say "check-c04-rds-em: CLEAN — Enhanced Monitoring 60s; estate unapplied."
exit 0
