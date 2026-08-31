#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-10-split-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0210prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/cmd/olivares/firstparty"
  cp "$ROOT/design/c02-10-split-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C02-10-SPLIT-CONNECTORS-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/cmd/olivares/firstparty/embed.go" "$TMP/tree/cmd/olivares/firstparty/"
  cp "$ROOT/cmd/olivares/firstparty/embed_binaries_release.go" \
    "$TMP/tree/cmd/olivares/firstparty/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c02-10-split-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-10-split-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe sidecar HOLD is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c02-10-split-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["sidecar_landed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (sidecar landed flag) is killed"
else bad "sidecar flag stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nconst sidecar = "OLIVARES_CONNECTOR_BUNDLE"\n' >> \
  "$TMP/tree/cmd/olivares/firstparty/embed.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (sidecar env landed) is killed"
else bad "sidecar env stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nFIRMA A claimed\n' >> \
  "$TMP/tree/design/C02-10-SPLIT-CONNECTORS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims FIRMA A) is killed"
else bad "doc FIRMA A stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c02-10-split-prep-2026-08-20.json" <<'PY'
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
rm -f "$TMP/tree/design/c02-10-split-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c02-10-split-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
