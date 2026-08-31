#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05 Dodo Cloud SKU unique leftover unique vs #948/#933/#921 and
# check-c05-dodo-sku.sh (original OPEN CHECK would LOOK 2 / FAIL on
# origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-dodo-sku-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-dodo-sku-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05SKUP_JSON:-design/c05-dodo-sku-prep-2026-08-20.json}"
DOC="${OLIVARES_C05SKUP_DOC:-design/C05-DODO-SKU-PREP-2026-08-20.md}"
MAP="${OLIVARES_C05SKUP_MAP:-cloud/control-plane/internal/billing/dodo-cloud-product-map.json}"
BOOT="${OLIVARES_C05SKUP_BOOT:-cloud/control-plane/cmd/cloud-cp/main.go}"
MGR="${OLIVARES_C05SKUP_MGR:-cloud/control-plane/internal/tenant/manager.go}"
ENG="${OLIVARES_C05SKUP_ENG:-cloud/control-plane/internal/engine/client.go}"
POLAR="${OLIVARES_C05SKUP_POLAR:-cloud/control-plane/internal/billing/polar.go}"
CAT="${OLIVARES_C05SKUP_CAT:-commercial/license-worker/src/dodo/catalog.ts}"
WF="${OLIVARES_C05SKUP_WF:-commercial/license-worker/wrangler.jsonc}"
CFGMAP="${OLIVARES_C05SKUP_CFGMAP:-cloud/control-plane/config/dodo-cloud-product-map.json}"
M="pdt_0NlE7N9AZ9CV7wNAemXAO"
Y="pdt_0NlE7ZtwL8GfOeYefL7M8"

for f in "$JSON" "$DOC" "$MAP" "$BOOT" "$MGR" "$ENG" "$POLAR" "$CAT" "$WF"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"
command -v go >/dev/null || cannot "no go"

check_boot_fallback() {
  local test_tmp go_rc state
  test_tmp="${TMPDIR:-/workspace/.olivares-tmptest}"
  mkdir -p "$test_tmp" || cannot "cannot create test temp dir $test_tmp"
  BOOT_WITNESS_OUT="$(mktemp "$test_tmp/c05-dodo-sku-boot.XXXXXX")" \
    || cannot "cannot create boot witness output"
  trap 'rm -f -- "${BOOT_WITNESS_OUT:-}"' EXIT

  go_rc=0
  TMPDIR="$test_tmp" go -C "$ROOT/cloud/control-plane" test -count=1 -json \
    -run '^TestBootProducts$' ./cmd/cloud-cp >"$BOOT_WITNESS_OUT" 2>&1 || go_rc=$?
  state="$(python3 - "$BOOT_WITNESS_OUT" <<'PY'
import json, sys

actions = []
with open(sys.argv[1], encoding="utf-8") as stream:
    for line in stream:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Test") == "TestBootProducts":
            actions.append(event.get("Action"))
if "fail" in actions:
    print("FAIL")
elif "pass" in actions:
    print("PASS")
else:
    print("UNKNOWN")
PY
)"
  case "$state:$go_rc" in
    PASS:0) ;;
    FAIL:*) fail "TestBootProducts rejects the boot fallback" ;;
    *) cannot "could not execute the exact TestBootProducts witness (go rc=$go_rc, state=$state)" ;;
  esac
}

grep -F -q 'Unique leftover unique vs `#948`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #948"
grep -F -q 'Unique leftover unique vs `#933`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #933"
grep -F -q 'Unique leftover unique vs `#921`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #921"
grep -F -q 'Unique leftover unique vs `check-c05-dodo-sku.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original CHECK"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'OnboardFirstOwner not landed.' "$DOC" \
  || fail "prepare doc lost OnboardFirstOwner HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|OnboardFirstOwner landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

check_boot_fallback
grep -q 'func (m \*Manager) UpdatePlan' "$MGR" \
  || fail "tenant manager lost UpdatePlan"
grep -q "$M" "$MAP" || fail "billing embed map lost monthly Cloud SKU $M"
grep -q "$Y" "$MAP" || fail "billing embed map lost yearly Cloud SKU $Y"
grep -q 'cloud-standard-m' "$MAP" || fail "billing embed map lost cloud-standard-m"
grep -q 'cloud-standard-y' "$MAP" || fail "billing embed map lost cloud-standard-y"
if ! grep -q 'cloud_products' "$WF"; then
  fail "wrangler production catalog lost cloud_products"
fi
grep -q "$M" "$WF" || fail "wrangler lost monthly Cloud pdt_"
grep -q "$Y" "$WF" || fail "wrangler lost yearly Cloud pdt_"

if grep -q 'OnboardFirstOwner' "$ENG"; then
  fail "OnboardFirstOwner landed — this HOLD lote does not apply #948"
fi
if grep -q 'SendFirstOwnerInvite' "$MGR"; then
  fail "SendFirstOwnerInvite landed — this HOLD lote does not apply #948"
fi
if grep -q 'WithInviteMailer' "$BOOT"; then
  fail "WithInviteMailer landed — this HOLD lote does not apply #948"
fi
if [ -e "$CFGMAP" ]; then
  fail "config/dodo-cloud-product-map.json landed — this HOLD lote does not apply #948"
fi
if grep -q 'Cloud SKUs are tenants, not OTA sets' "$CAT"; then
  fail "catalog tenant-wording landed — this HOLD lote does not apply #948"
fi
grep -q 'addons-unsupported' "$POLAR" \
  || fail "polar no longer answers addons-unsupported — this HOLD lote does not apply #948"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-dodo-sku-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-dodo-sku-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-dodo-sku-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
want_true = (
    "decided_embed_on_boot",
    "update_plan_landed",
    "wrangler_cloud_products",
    "billing_product_map_landed",
    "polar_addons_unsupported_still",
)
want_false = (
    "onboard_first_owner_landed",
    "send_first_owner_invite_landed",
    "with_invite_mailer_landed",
    "config_product_map_landed",
    "catalog_cloud_sku_tenant_wording_landed",
    "remainder_applied",
    "overlay_remeasured_in_this_gate",
)
for k in want_true:
    if data.get(k) is not True:
        fail("%s must stay true" % k)
for k in want_false:
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

say "check-c05-dodo-sku-prep: CLEAN — embed+UpdatePlan+cloud_products already on main; OnboardFirstOwner HOLD; overlay remasure not in this gate."
exit 0
