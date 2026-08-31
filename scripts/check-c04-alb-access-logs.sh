#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB access logs to the plane bucket. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-access-logs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-access-logs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04AL_JSON:-design/c04-alb-access-logs.json}"
DOC="${OLIVARES_C04AL_DOC:-design/C04-ALB-ACCESS-LOGS-2026-08-20.md}"
ING="${OLIVARES_C04AL_ING:-deploy/aws/modules/ingress/main.tf}"
DATA="${OLIVARES_C04AL_DATA:-deploy/aws/modules/data/main.tf}"
ROOTTF="${OLIVARES_C04AL_ROOT:-deploy/aws/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ING" ] || cannot "missing ingress terraform"
[ -f "$DATA" ] || cannot "missing data terraform"
[ -f "$ROOTTF" ] || cannot "missing root terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$ING" || fail "ingress module lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$DATA" || fail "data module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$ING" "$DATA" "$ROOTTF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-alb-access-logs/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("access_logs") is not True:
    raise SystemExit("access_logs must be true")
if data.get("prefix") != "alb":
    raise SystemExit("prefix must stay alb")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

ing = open(sys.argv[2], encoding="utf-8").read()
dat = open(sys.argv[3], encoding="utf-8").read()
root = open(sys.argv[4], encoding="utf-8").read()
if "access_logs" not in ing:
    raise SystemExit("ALB access_logs block missing")
if not re.search(r'prefix\s*=\s*"alb"', ing):
    raise SystemExit("access_logs prefix lost alb")
if "access_logs_bucket" not in ing:
    raise SystemExit("access_logs_bucket not wired on the ALB")
if not re.search(r'resource\s+"aws_s3_bucket_policy"\s+"plane_alb_logs"', dat):
    raise SystemExit("plane ALB log bucket policy missing")
if "s3:PutObject" not in dat:
    raise SystemExit("PutObject lost from the log policy")
if "aws_elb_service_account" not in dat:
    raise SystemExit("ELB service account data source missing")
if "plane_bucket_id" not in root or "access_logs_bucket" not in root:
    raise SystemExit("root module does not pass the plane bucket to ingress")
PY

say "check-c04-alb-access-logs: CLEAN — ALB logs to plane bucket; estate unapplied."
exit 0
