#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05 webhook Cloud SKU unique leftover unique vs #895 (original OPEN
# product PR). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-webhook-sku-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-webhook-sku-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05WHP_JSON:-design/c05-webhook-sku-prep-2026-08-20.json}"
DOC="${OLIVARES_C05WHP_DOC:-design/C05-WEBHOOK-SKU-PREP-2026-08-20.md}"
BOOT="${OLIVARES_C05WHP_BOOT:-cloud/control-plane/cmd/cloud-cp/main.go}"
ENG="${OLIVARES_C05WHP_ENG:-cloud/control-plane/internal/engine/client.go}"
CFGMAP="${OLIVARES_C05WHP_CFGMAP:-cloud/control-plane/config/dodo-cloud-product-map.json}"

for f in "$JSON" "$DOC" "$BOOT" "$ENG"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"
command -v go >/dev/null || cannot "no go"

check_boot_fallback() {
  local test_tmp go_rc state
  test_tmp="${TMPDIR:-/workspace/.olivares-tmptest}"
  mkdir -p "$test_tmp" || cannot "cannot create test temp dir $test_tmp"
  BOOT_WITNESS_OUT="$(mktemp "$test_tmp/c05-webhook-sku-boot.XXXXXX")" \
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

grep -F -q 'Unique leftover unique vs `#895`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #895"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'OnboardFirstOwner not landed.' "$DOC" \
  || fail "prepare doc lost OnboardFirstOwner HOLD"
grep -F -q 'Does not copy `#895`' "$DOC" \
  || fail "prepare doc lost stale-branch HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|OnboardFirstOwner landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

check_boot_fallback
if grep -q 'OnboardFirstOwner' "$ENG"; then
  fail "OnboardFirstOwner landed — this HOLD lote does not apply #895"
fi
if [ -e "$CFGMAP" ]; then
  fail "config/dodo-cloud-product-map.json landed — this HOLD lote does not apply #895"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-webhook-sku-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-webhook-sku-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-webhook-sku-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("decided_embed_on_boot") is not True:
    fail("decided_embed_on_boot must stay true")
if data.get("onboard_first_owner_landed") is not False:
    fail("onboard_first_owner_landed must stay false")
if data.get("config_product_map_landed") is not False:
    fail("config_product_map_landed must stay false")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c05-webhook-sku-prep: CLEAN — decided embed already on main; OnboardFirstOwner HOLD; overlay remasure not in this gate."
exit 0
