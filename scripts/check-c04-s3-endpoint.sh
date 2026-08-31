#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: S3 Gateway VPC endpoint; NAT stays. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-s3-endpoint: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-s3-endpoint: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04EP_JSON:-design/c04-s3-endpoint.json}"
DOC="${OLIVARES_C04EP_DOC:-design/C04-S3-ENDPOINT-2026-08-20.md}"
TF="${OLIVARES_C04EP_TF:-deploy/aws/modules/network/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing network terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "network module lost NEVER APPLIED"
grep -q 'NAT stays' "$DOC" || fail "$DOC lost NAT-stays"
if grep -qiE 'estate applied|FIRMA A claimed|NAT removed' "$DOC"; then
	fail "$DOC claims an apply or NAT drop this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-s3-endpoint/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("gateway_s3") is not True:
    raise SystemExit("gateway_s3 must be true")
if data.get("nat_kept") is not True:
    raise SystemExit("nat_kept must stay true")
if data.get("interface_endpoints") is not False:
    raise SystemExit("interface_endpoints must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
if not re.search(r'resource\s+"aws_vpc_endpoint"\s+"s3"', tf):
    raise SystemExit("S3 VPC endpoint missing")
if not re.search(r'vpc_endpoint_type\s*=\s*"Gateway"', tf):
    raise SystemExit("endpoint type lost Gateway")
if not re.search(r"aws_nat_gateway", tf):
    raise SystemExit("NAT gateway lost")
if re.search(r'vpc_endpoint_type\s*=\s*"Interface"', tf):
    raise SystemExit("Interface endpoint is not this lote")
if ".s3" not in tf:
    raise SystemExit("S3 service_name lost")
PY

say "check-c04-s3-endpoint: CLEAN — S3 Gateway endpoint; NAT stays; unapplied."
exit 0
