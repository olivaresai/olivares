#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c04-nlb-idle-nat.sh — C04. Collector TCP idle timeout is owned
# (not the 350 s TLS default) and NAT egress exists. Unapplied.
# Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-nlb-idle-nat: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-nlb-idle-nat: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

ING="${OLIVARES_C04_INGRESS:-deploy/aws/modules/ingress/main.tf}"
NET="${OLIVARES_C04_NETWORK:-deploy/aws/modules/network/main.tf}"
DOC="${OLIVARES_C04_DOC:-design/C04-NLB-IDLE-NAT-2026-08-19.md}"

[ -f "$ING" ] || cannot "missing $ING"
[ -f "$NET" ] || cannot "missing $NET"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'tcp_idle_timeout_seconds' "$ING" \
	|| fail "NLB collector listener has no tcp_idle_timeout_seconds — 350 s TLS default by another name"
if grep -q 'tcp_idle_timeout_seconds *= *350' "$ING"; then
	fail "tcp_idle_timeout_seconds is 350 — that is the TLS listener ceiling this design rejected"
fi
idle="$(grep -E 'tcp_idle_timeout_seconds[[:space:]]*=' "$ING" | head -1 | tr -dc '0-9')"
[ -n "$idle" ] || fail "could not read tcp_idle_timeout_seconds"
[ "$idle" -ge 60 ] && [ "$idle" -le 6000 ] \
	|| fail "tcp_idle_timeout_seconds=$idle is outside AWS 60–6000"

grep -q 'resource "aws_nat_gateway"' "$NET" \
	|| fail "network module lost aws_nat_gateway — Fargate has no internet"
grep -q 'nat_gateway_id' "$NET" \
	|| fail "private default route is not NAT"
grep -q 'resource "aws_eip"' "$NET" \
	|| fail "NAT has no aws_eip"

grep -q 'NO APLICADO' "$DOC" || fail "$DOC lost NO APLICADO"
if grep -qiE 'tofu apply ran|applied the estate' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

say "check-c04-nlb-idle-nat: CLEAN — TCP idle timeout owned; NAT present; unapplied."
exit 0
