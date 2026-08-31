#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: WAF logs to CloudWatch. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-waf-logs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-waf-logs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04WAF_JSON:-design/c04-waf-logs.json}"
DOC="${OLIVARES_C04WAF_DOC:-design/C04-WAF-LOGS-2026-08-20.md}"
TF="${OLIVARES_C04WAF_TF:-deploy/aws/modules/ingress/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing ingress terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "ingress module lost NEVER APPLIED"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

def block_after(src, pat):
    m = re.search(pat, src)
    if not m:
        return None
    i = m.end()
    depth = 1
    while i < len(src) and depth:
        if src[i] == "{":
            depth += 1
        elif src[i] == "}":
            depth -= 1
        i += 1
    return src[m.start():i]

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-waf-logs/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("logging") is not True:
    raise SystemExit("logging must stay true")
if data.get("destination") != "cloudwatch":
    raise SystemExit("destination must stay cloudwatch")
if data.get("log_group_prefix") != "aws-waf-logs-":
    raise SystemExit("log_group_prefix drifted")
if data.get("redacted_header") != "authorization":
    raise SystemExit("redacted_header drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
lg = block_after(tf, r'resource\s+"aws_cloudwatch_log_group"\s+"waf"\s*\{')
if not lg:
    raise SystemExit("aws_cloudwatch_log_group.waf missing")
if 'name              = "aws-waf-logs-${var.name}"' not in lg and \
        not re.search(r'name\s*=\s*"aws-waf-logs-\$\{var\.name\}"', lg):
    raise SystemExit("log group name lost aws-waf-logs- prefix")
if re.search(r'name\s*=\s*"/olivares/', lg):
    raise SystemExit("log group used /olivares/ path AWS rejects")

cfg = block_after(
    tf, r'resource\s+"aws_wafv2_web_acl_logging_configuration"\s+"alb"\s*\{'
)
if not cfg:
    raise SystemExit("aws_wafv2_web_acl_logging_configuration.alb missing")
if "aws_cloudwatch_log_group.waf.arn" not in cfg:
    raise SystemExit("logging destination is not the WAF log group")
if "aws_wafv2_web_acl.alb.arn" not in cfg:
    raise SystemExit("logging resource_arn is not the ALB web ACL")
if "kinesis" in cfg.lower() or "firehose" in cfg.lower():
    raise SystemExit("logging destination drifted to Firehose")
if not re.search(r'name\s*=\s*"authorization"', cfg):
    raise SystemExit("authorization header is not redacted")
PY

say "check-c04-waf-logs: CLEAN — WAF logs to CloudWatch; estate unapplied."
exit 0
