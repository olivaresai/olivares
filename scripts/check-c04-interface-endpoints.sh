#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: four Interface VPC endpoints. NAT stays. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-interface-endpoints: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-interface-endpoints: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04IE_JSON:-design/c04-interface-endpoints.json}"
DOC="${OLIVARES_C04IE_DOC:-design/C04-INTERFACE-ENDPOINTS-2026-08-20.md}"
IFACE="${OLIVARES_C04IE_TF:-deploy/aws/modules/network/interface-endpoints.tf}"
MAIN="${OLIVARES_C04IE_MAIN:-deploy/aws/modules/network/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$IFACE" ] || cannot "missing $IFACE"
[ -f "$MAIN" ] || cannot "missing $MAIN"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$IFACE" || fail "$IFACE lost NEVER APPLIED"
grep -q 'NAT stays' "$DOC" || fail "$DOC lost NAT-stays"
if grep -qiE 'estate applied|FIRMA A claimed|NAT removed' "$DOC"; then
	fail "$DOC claims an apply or NAT drop this lote does not have"
fi

python3 - "$JSON" "$IFACE" "$MAIN" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
iface = open(sys.argv[2], encoding="utf-8").read()
main = open(sys.argv[3], encoding="utf-8").read()

if data.get("schema") != "c04-interface-endpoints/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("nat_kept") is not True:
    raise SystemExit("nat_kept must stay true")
if data.get("gateway_s3_untouched") is not True:
    raise SystemExit("gateway_s3_untouched must stay true")
if data.get("private_dns") is not True:
    raise SystemExit("private_dns must stay true")
want = ["ecr.api", "ecr.dkr", "logs", "secretsmanager"]
if data.get("interface_services") != want:
    raise SystemExit("interface_services must stay %s" % want)
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

if 'resource "aws_vpc_endpoint" "s3"' not in main:
    raise SystemExit("S3 Gateway endpoint lost from main.tf")
if 'resource "aws_nat_gateway"' not in main:
    raise SystemExit("NAT gateway resource lost from main.tf")
if re.search(r'vpc_endpoint_type\s*=\s*"Interface"', main):
    raise SystemExit("Interface endpoint leaked into main.tf (S3 lote)")

if "aws_interface_services" not in iface:
    raise SystemExit("for_each set of interface services missing")
if not re.search(r'vpc_endpoint_type\s*=\s*"Interface"', iface):
    raise SystemExit("Interface type missing")
if not re.search(r"private_dns_enabled\s*=\s*true", iface):
    raise SystemExit("private_dns_enabled lost")
for svc in want:
    if '"%s"' % svc not in iface:
        raise SystemExit("service %s missing from the set" % svc)
if "aws_nat_gateway" in iface:
    raise SystemExit("NAT must stay in main.tf, not this file")
PY

say "check-c04-interface-endpoints: CLEAN — four Interface endpoints; NAT stays; unapplied."
exit 0
