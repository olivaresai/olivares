#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg-18-helm-render-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg18rprep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/cfg-18-helm-render-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/CFG-18-HELM-RENDER-PREP-2026-08-20.md" "$TMP/tree/design/"
  cat >"$TMP/tree/scripts/check-helm-render.sh" <<'EOF'
#!/bin/sh
echo "check-helm-render: helm not found on PATH" >&2
exit 2
EOF
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-helm-render.sh" \
    "$TMP/tree/scripts/check-cfg-18-helm-render-prep.sh"
  cat >"$TMP/tree/Taskfile.yml" <<'EOF'
  lint:addon-sets:
    cmds:
      - bash scripts/check-helm-chart-prep.sh

  lint:addon-sets-gate:
    cmds:
      - bash scripts/test-helm-chart-prep.sh
EOF
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  PATH="/usr/bin:/bin" \
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg-18-helm-render-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe helm-render pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/cfg-18-helm-render-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["original_in_addon_sets"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (original in addon-sets flag) is killed"
else bad "addon-sets flag stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
cat >"$TMP/tree/Taskfile.yml" <<'EOF'
  lint:addon-sets:
    cmds:
      - bash scripts/check-helm-render.sh

  lint:addon-sets-gate:
    cmds:
      - bash scripts/test-helm-chart-prep.sh
EOF
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (original wired into addon-sets) is killed"
else bad "wired original stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nFIRMA A claimed\n' >> \
  "$TMP/tree/design/CFG-18-HELM-RENDER-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims FIRMA A) is killed"
else bad "doc FIRMA A stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/cfg-18-helm-render-prep-2026-08-20.json" <<'PY'
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
rm -f "$TMP/tree/design/cfg-18-helm-render-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-cfg-18-helm-render-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
