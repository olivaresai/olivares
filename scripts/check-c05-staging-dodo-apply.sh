#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C05 — staging compose boots as Dodo and maps both Cloud SKUs.
# Exit 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT"

COMPOSE="${OLIVARES_C05_COMPOSE:-cloud/staging/docker-compose.yml}"
DOC="$(find design -maxdepth 1 -name 'C05-STAGING-DODO-APPLY-*.md' -print | sort | tail -n 1 || true)"
SKU_M="pdt_0NlE7N9AZ9CV7wNAemXAO"
SKU_Y="pdt_0NlE7ZtwL8GfOeYefL7M8"

if [[ ! -f "$COMPOSE" ]]; then
	echo "C05-staging-apply: COULD NOT LOOK — compose missing" >&2
	exit 2
fi
if [[ -z "$DOC" || ! -f "$DOC" ]]; then
	echo "C05-staging-apply: COULD NOT LOOK — prep doc missing" >&2
	exit 2
fi

fail=0

if ! grep -q 'COMMERCE_PROVIDER' "$COMPOSE"; then
	echo "C05-staging-apply: compose does not name COMMERCE_PROVIDER — the binary will not boot" >&2
	fail=1
fi
if ! grep -q 'COMMERCE_PROVIDER:.*dodo' "$COMPOSE"; then
	echo "C05-staging-apply: compose COMMERCE_PROVIDER is not dodo" >&2
	fail=1
fi
if ! grep -q 'CLOUD_CP_API_KEY' "$COMPOSE"; then
	echo "C05-staging-apply: compose does not name CLOUD_CP_API_KEY — the binary will not boot" >&2
	fail=1
fi
if ! grep -q "$SKU_M" "$COMPOSE"; then
	echo "C05-staging-apply: monthly Cloud SKU missing from compose map default" >&2
	fail=1
fi
if ! grep -q "$SKU_Y" "$COMPOSE"; then
	echo "C05-staging-apply: annual Cloud SKU missing from compose map default" >&2
	fail=1
fi
if ! grep -q 'cloud-standard-m' "$COMPOSE" || ! grep -q 'cloud-standard-y' "$COMPOSE"; then
	echo "C05-staging-apply: decided Cloud plan names missing from compose map default" >&2
	fail=1
fi
if grep -q '"prod_staging_pro"' "$COMPOSE"; then
	echo "C05-staging-apply: Polar staging product is still the compose default" >&2
	fail=1
fi
if ! grep -q 'does not restack' "$DOC"; then
	echo "C05-staging-apply: prep doc lost the restack refusal" >&2
	fail=1
fi
if grep -qiE 'FIRMA A claimed|FIRMA A is met' "$DOC"; then
	echo "C05-staging-apply: prep doc claims FIRMA A" >&2
	fail=1
fi

# The default map must be JSON the binary can parse. Extract the :- default.
if ! default_json="$(python3 - "$COMPOSE" <<'PY'
import json, sys
text = open(sys.argv[1], encoding="utf-8").read()
needle = "${CLOUD_PRODUCT_MAP:-"
i = text.find(needle)
if i < 0:
    sys.exit(2)
rest = text[i + len(needle):]
obj, _ = json.JSONDecoder().raw_decode(rest)
print(json.dumps(obj, separators=(",", ":")))
PY
)"; then
	echo "C05-staging-apply: COULD NOT LOOK — cannot extract CLOUD_PRODUCT_MAP default" >&2
	exit 2
fi

if ! python3 - "$default_json" "$SKU_M" "$SKU_Y" <<'PY'
import json, sys
raw, sku_m, sku_y = sys.argv[1], sys.argv[2], sys.argv[3]
data = json.loads(raw)
prods = data.get("products") or {}
if set(prods) != {sku_m, sku_y}:
    raise SystemExit("product ids %r" % sorted(prods))
if prods[sku_m].get("tier") != "cloud-standard-m":
    raise SystemExit("monthly tier")
if prods[sku_y].get("tier") != "cloud-standard-y":
    raise SystemExit("annual tier")
PY
then
	echo "C05-staging-apply: compose map default is not the two Cloud SKUs" >&2
	fail=1
fi

if [[ "$fail" -ne 0 ]]; then
	echo "C05-staging-apply: $fail finding(s)" >&2
	exit 1
fi
echo "C05-staging-apply: CLEAN — staging compose boots as Dodo with both Cloud SKUs"
exit 0
