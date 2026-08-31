#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-vpc-flow-logs.sh — C04. VPC flow logs exist, unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-vpc-flow-logs: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-vpc-flow-logs: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

OBS="${OLIVARES_C04FLOW_OBS:-deploy/aws/modules/observability/main.tf}"
ROOTTF="${OLIVARES_C04FLOW_ROOT:-deploy/aws/main.tf}"
DOC="${OLIVARES_C04FLOW_DOC:-design/C04-VPC-FLOW-LOGS-2026-08-19.md}"

[ -f "$OBS" ] || cannot "missing $OBS"
[ -f "$ROOTTF" ] || cannot "missing $ROOTTF"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

grep -q 'resource "aws_flow_log"' "$OBS" \
	|| fail "observability lost aws_flow_log"
grep -qE 'traffic_type[[:space:]]*=[[:space:]]*"ALL"' "$OBS" \
	|| fail "flow log traffic_type is not ALL"
if grep -qE 'traffic_type[[:space:]]*=[[:space:]]*"ACCEPT"' "$OBS"; then
	fail "flow log is ACCEPT-only — REJECT would be invisible"
fi
grep -q 'vpc_id[[:space:]]*=' "$OBS" \
	|| fail "aws_flow_log does not take vpc_id"
grep -q 'vpc_id[[:space:]]*=[[:space:]]*module.network.vpc_id' "$ROOTTF" \
	|| fail "root observability module is not wired to the VPC"

say "check-c04-vpc-flow-logs: CLEAN — VPC flow logs ALL; unapplied."
exit 0
