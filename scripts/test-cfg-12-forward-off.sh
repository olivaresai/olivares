#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg-12-forward-off.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg12.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/commercial/license-worker/src/dodo" \
           "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
    "$TMP/tree/commercial/license-worker/"
  cp "$ROOT/commercial/license-worker/src/dodo/catalog.ts" \
    "$TMP/tree/commercial/license-worker/src/dodo/"
  cp "$ROOT/design/CFG-12-FORWARD-OFF-PIN-2026-08-20.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-cfg-12-forward-off.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg-12-forward-off.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "live pin (3x false + two Cloud pdt_) is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
t = t.replace('"CLOUD_FORWARD_ENABLED": "false"', '"CLOUD_FORWARD_ENABLED": "true"', 1)
p.write_text(t, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (one block flipped to true) is killed"
else bad "flipped forward stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
old = 'cloud_products\\":[\\"pdt_0NlE7N9AZ9CV7wNAemXAO\\",\\"pdt_0NlE7ZtwL8GfOeYefL7M8\\"]'
new = 'cloud_products\\":[\\"pdt_0NlE7ZtwL8GfOeYefL7M8\\"]'
if old not in t:
    raise SystemExit("fixture lost production cloud_products JSONC literal")
p.write_text(t.replace(old, new, 1), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (dropped a Cloud pdt_) is killed"
else bad "dropped Cloud pdt_ stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
needle = '"FULFILLMENT_ENABLED": "false"'
idx = t.rfind(needle)
if idx < 0:
    raise SystemExit("fixture lost production FULFILLMENT_ENABLED false")
t = t[:idx] + '"FULFILLMENT_ENABLED": "true"' + t[idx + len(needle):]
p.write_text(t, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (production sale ON + forward off) is killed"
else bad "production sale ON stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/commercial/license-worker/wrangler.jsonc"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing wrangler.jsonc is COULD NOT LOOK"
else bad "missing wrangler rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
sed -i 's/CLOUD_FORWARD_ENABLED stays false/CLOUD_FORWARD_ENABLED now true/' \
  "$TMP/tree/design/CFG-12-FORWARD-OFF-PIN-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc drops the HOLD line) is killed"
else bad "doc HOLD drift stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-cfg-12-forward-off selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
