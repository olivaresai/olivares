#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c05-first-owner.sh — C05. Mapped Cloud SKUs apply a plan. First
# owner is created via /v1/users + /v1/memberships (not AAL3 /v1/onboard).
# Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-first-owner: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-first-owner: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

# export-closure: hub-only cloud/control-plane/cmd/cloud-cp/main.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/billing/dodo-cloud-product-map.json — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/engine/client.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -d cloud/control-plane ]; then
	printf '%s\n' "check-c05-first-owner: COULD NOT LOOK — cloud/control-plane is not in this tree" >&2
	exit 2
fi

MAP=cloud/control-plane/internal/billing/dodo-cloud-product-map.json
CLI=cloud/control-plane/internal/engine/client.go
MGR=cloud/control-plane/internal/tenant/manager.go
BOOT=cloud/control-plane/cmd/cloud-cp/main.go
WF=commercial/license-worker/wrangler.jsonc
M="pdt_0NlE7N9AZ9CV7wNAemXAO"
Y="pdt_0NlE7ZtwL8GfOeYefL7M8"

[ -f "$MAP" ] || cannot "missing $MAP"
[ -f "$CLI" ] || cannot "missing $CLI"
[ -f "$MGR" ] || cannot "missing $MGR"
[ -f "$BOOT" ] || cannot "missing $BOOT"
[ -f "$WF" ] || cannot "missing $WF"

grep -q "$M" "$MAP" || fail "embed map lost monthly Cloud SKU $M"
grep -q "$Y" "$MAP" || fail "embed map lost yearly Cloud SKU $Y"
grep -q 'cloud-standard-m' "$MAP" || fail "embed map lost cloud-standard-m"
grep -q 'cloud-standard-y' "$MAP" || fail "embed map lost cloud-standard-y"

grep -q 'cloud_products' "$WF" || fail "wrangler.jsonc lost cloud_products"
grep -q "$M" "$WF" || fail "wrangler.jsonc lost monthly Cloud SKU"
grep -q "$Y" "$WF" || fail "wrangler.jsonc lost yearly Cloud SKU"

grep -Fq 'func (c *Client) CreateUser' "$CLI" \
	|| fail "engine client lost CreateUser (POST /v1/users)"
grep -Fq 'func (c *Client) GrantMembership' "$CLI" \
	|| fail "engine client lost GrantMembership (POST /v1/memberships)"
if grep -E 'MethodPost, "/v1/onboard"|Post\("/v1/onboard"' "$CLI"; then
	fail "engine client calls /v1/onboard — that door is AAL3; #948 swallows 403"
fi

grep -q 'inviteFirstOwner' "$MGR" || fail "tenant manager lost inviteFirstOwner"
grep -q 'CreateUser' "$MGR" || fail "tenant manager does not CreateUser"
grep -q 'GrantMembership' "$MGR" || fail "tenant manager does not GrantMembership"
grep -q 'DecidedDodoCloudProductMap' "$BOOT" \
	|| fail "bootProducts no longer loads the decided embed when unset"

say "check-c05-first-owner: CLEAN — Cloud SKUs mapped; first owner via /v1/users + /v1/memberships."
exit 0
