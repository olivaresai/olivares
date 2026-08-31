#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05 Cloud/self-hosted catalogue-channel disjointness, unique leftover vs
# #881 (original OPEN product PR). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-cloud-skus-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-cloud-skus-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05SKU881_JSON:-design/c05-cloud-skus-prep-2026-08-20.json}"
DOC="${OLIVARES_C05SKU881_DOC:-design/C05-CLOUD-SKUS-PREP-2026-08-20.md}"
CAT="${OLIVARES_C05SKU881_CAT:-commercial/license-worker/src/dodo/catalog.ts}"

for f in "$JSON" "$DOC" "$CAT"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"
command -v node >/dev/null || cannot "no node"

check_catalog_channel_boundary() {
  local output rc=0
  output="$(
    node --input-type=module - "$CAT" 2>&1 <<'JS'
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

let parseDodoCatalog;
try {
  ({ parseDodoCatalog } = await import(pathToFileURL(resolve(process.argv[2])).href));
} catch (error) {
  console.error(`catalog import failed: ${error}`);
  process.exit(2);
}

const overlap = JSON.stringify({
  currency: "USD",
  products: { pdt_cloud: 19900, pdt_self_hosted: 12900 },
  addons: { adn_audit: 9900 },
  cloud_products: ["pdt_cloud"],
  set_codes: { pdt_cloud: "biz" },
});
let overlapRefused = false;
try {
  parseDodoCatalog(overlap);
} catch {
  overlapRefused = true;
}
if (!overlapRefused) {
  console.error("catalog accepted one id as both a Cloud product and an OTA set-code source");
  process.exit(1);
}

try {
  const disjoint = parseDodoCatalog(JSON.stringify({
    currency: "USD",
    products: { pdt_cloud: 19900, pdt_self_hosted: 12900 },
    addons: { adn_audit: 9900 },
    cloud_products: ["pdt_cloud"],
    set_codes: { pdt_self_hosted: "biz", adn_audit: "reg" },
  }));
  if (!disjoint.cloudProducts.has("pdt_cloud") || disjoint.setCodes.has("pdt_cloud")) {
    throw new Error("disjoint channel projection changed");
  }
  if (disjoint.setCodes.get("pdt_self_hosted") !== "biz" ||
      disjoint.setCodes.get("adn_audit") !== "reg") {
    throw new Error("valid self-hosted set-code mapping changed");
  }
} catch (error) {
  console.error(`catalog refused or altered a disjoint configuration: ${error}`);
  process.exit(1);
}
console.log("catalog-channel-boundary-ok");
JS
  )" || rc=$?
  case "$rc" in
    0) ;;
    1) fail "$output" ;;
    2) cannot "$output" ;;
    *) cannot "catalog boundary witness exited $rc: $output" ;;
  esac
}

grep -F -q 'Unique leftover unique vs `#881`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #881"
grep -F -q 'HOLD-RUNTIME-UNREACHABLE' "$DOC" \
  || fail "prepare doc lost the named runtime HOLD"
grep -F -q 'Does not copy `#881`' "$DOC" \
  || fail "prepare doc lost stale-branch HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|runtime authority impact (closed|proven)' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

check_catalog_channel_boundary

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-cloud-skus-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-cloud-skus-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-cloud-skus-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
for k in (
    "remainder_applied",
    "overlay_remeasured_in_this_gate",
):
    if data.get(k) is not False:
        fail("%s must stay false" % k)
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c05-cloud-skus-prep: CLEAN — cloud_products/set_codes disjoint; runtime authority impact HOLD-RUNTIME-UNREACHABLE."
exit 0
