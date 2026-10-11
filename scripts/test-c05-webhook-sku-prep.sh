#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(
  cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
  pwd
)" || exit 2
CHECK="$ROOT/scripts/check-c05-webhook-sku-prep.sh"

# Keep fixture Go modules outside an ancestor workspace.
sin_go_work() { # 0 si ni el dir ni ningun ancestro tiene go.work
  local p
  p="$(cd -- "$1" 2>/dev/null && pwd -P)" || return 1
  while :; do
    [ -e "$p/go.work" ] && return 1
    [ "$p" = / ] && return 0
    p="$(dirname -- "$p")"
  done
}
elige_scratch() {
  local base d
  # Include RUNNER_TEMP after the review: CI supplies its preferred scratch path,
  # so omitting it falls back unnecessarily to /var/tmp. Probe it like every candidate;
  # the only deciding condition is being outside every go.work tree.
  for base in "${TMPDIR:-}" "${RUNNER_TEMP:-}" /workspace/.olivares-tmptest /var/tmp; do
    [ -n "$base" ] || continue
    mkdir -p "$base" 2>/dev/null || continue
    d="$(TMPDIR="$base" mktemp -d -t c05whprep.XXXXXX 2>/dev/null)" || continue
    if sin_go_work "$d"; then printf '%s' "$d"; return 0; fi
    rm -rf "$d" 2>/dev/null
  done
  return 1
}
TMP="$(elige_scratch)" || {
  echo "test-c05-webhook-sku-prep: COULD NOT LOOK — no scratch directory works: one is needed OUTSIDE" >&2
  echo "  any tree with go.work, because this test stages cloud/control-plane and compiles it." >&2
  echo "  Tried: TMPDIR=${TMPDIR:-unset}, RUNNER_TEMP=${RUNNER_TEMP:-unset}," >&2
  echo "  /workspace/.olivares-tmptest, /var/tmp." >&2
  exit 2
}
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" "$TMP/tree/cloud"
  cp -R "$ROOT/cloud/control-plane" "$TMP/tree/cloud/"
  cp "$ROOT/design/c05-webhook-sku-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C05-WEBHOOK-SKU-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-webhook-sku-prep.sh"
}
mutate_empty_boot_fallback() {
  python3 - "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
old = "\t\traw = billing.DecidedDodoCloudProductMap\n"
new = (
    "\t\traw = `{\"products\":{}}` // "
    "billing.DecidedDodoCloudProductMap remains named for text-only gates\n"
)
if text.count(old) != 1:
    raise SystemExit("boot fallback shape changed: exact B2 mutant could not be applied")
p.write_text(text.replace(old, new), encoding="utf-8")
PY
}
mutate_semantic_boot_shape() {
  python3 - "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go" \
    "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main_test.go" <<'PY'
from pathlib import Path
import sys

main = Path(sys.argv[1])
test = Path(sys.argv[2])
main_text = main.read_text(encoding="utf-8").replace("bootProducts", "loadProductsAtBoot")
test_text = test.read_text(encoding="utf-8").replace("bootProducts", "loadProductsAtBoot")
old = (
    "\tif strings.TrimSpace(raw) == \"\" {\n"
    "\t\traw = billing.DecidedDodoCloudProductMap\n"
    "\t}\n"
)
new = (
    "\tdecidedFallback := billing.DecidedDodoCloudProductMap\n"
    "\tif strings.TrimSpace(raw) == \"\" {\n"
    "\t\traw = decidedFallback\n"
    "\t}\n"
)
if main_text.count(old) != 1:
    raise SystemExit("boot fallback shape changed: semantic no-fire could not be applied")
main.write_text(main_text.replace(old, new), encoding="utf-8")
test.write_text(test_text, encoding="utf-8")
PY
}
mutate_missing_boot_witness() {
  python3 - "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main_test.go" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
old = "func TestBootProducts("
if text.count(old) != 1:
    raise SystemExit("boot witness shape changed: exact UNKNOWN mutant could not be applied")
p.write_text(text.replace(old, "func TestBootProductsRenamed("), encoding="utf-8")
PY
}
run_boot_products() {
  local rc=0
  TMPDIR="$TMP" go -C "$TMP/tree/cloud/control-plane" test -count=1 \
    -run '^TestBootProducts$' ./cmd/cloud-cp >"$TMP/boot.out" 2>&1 || rc=$?
  echo "$rc" >"$TMP/boot.rc"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  TMPDIR="$TMP" OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-webhook-sku-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe webhook SKU pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
mutate_empty_boot_fallback
run
run_boot_products
if [ "$(cat "$TMP/boot.rc")" != 1 ] || ! grep -q -- '--- FAIL: TestBootProducts' "$TMP/boot.out"; then
  bad "B2 empty-fallback mutant did not produce the expected TestBootProducts red ($(cat "$TMP/boot.out"))"
elif [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "executable mutant (empty fallback) is killed"
else
  bad "executable empty-fallback mutant survived checker rc=$(cat "$TMP/rc"); TestBootProducts was red"
fi

stage
mutate_semantic_boot_shape
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
  ok "no-fire: semantic fallback formatting and helper rename stay CLEAN"
else
  bad "semantic fallback no-fire returned rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
mutate_missing_boot_witness
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
  ok "missing exact Go witness is COULD NOT LOOK"
else
  bad "missing exact Go witness rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c05-webhook-sku-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["remainder_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (remainder-applied) is killed"
else bad "remainder-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nfunc (c *Client) OnboardFirstOwner() {}\n' >> \
  "$TMP/tree/cloud/control-plane/internal/engine/client.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (OnboardFirstOwner landed) is killed"
else bad "OnboardFirstOwner stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-webhook-sku-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c05-webhook-sku-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-webhook-sku-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
