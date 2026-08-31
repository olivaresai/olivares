#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-c03-33-refund-first.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c03-33-refund-first.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0333.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
           "$TMP/tree/commercial/commerce/internal/pump" \
           "$TMP/tree/commercial/commerce/cmd/commerce"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c03-33-refund-first.sh"
  cp "$ROOT/design/c03-33-refund-first.json" "$TMP/tree/design/"
  cp "$ROOT/design/C03-33-REFUND-FIRST-REMEASURE-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/commerce/internal/pump/pump.go" \
     "$TMP/tree/commercial/commerce/internal/pump/"
  cp "$ROOT/commercial/commerce/internal/pump/outbox.go" \
     "$TMP/tree/commercial/commerce/internal/pump/"
  cp "$ROOT/commercial/commerce/cmd/commerce/main.go" \
     "$TMP/tree/commercial/commerce/cmd/commerce/"
}

run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c03-33-refund-first.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live remasure is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
printf '\nfunc enqueueGrants() {}\n' >> "$TMP/tree/commercial/commerce/cmd/commerce/main.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "firing: enqueueGrants returned is FAIL"
else bad "enqueueGrants should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/commerce/internal/pump/pump.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text()
t = t.replace(
    'Kind:  "issue_grant",\n\t\tOwner: "commercial/license-worker (V8-B)",',
    'Kind:  "issue_grant",\n\t\tLocal: true,',
    1,
)
p.write_text(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "firing: local issue_grant is FAIL"
else bad "local issue_grant should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/commerce/internal/pump/pump.go" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace("succeeded refund saga", "no precondition", 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "firing: lost refund-saga Why is FAIL"
else bad "lost Why should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c03-33-refund-first.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["issue_grant_local"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "firing: JSON claims issue_grant local is FAIL"
else bad "JSON local claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/C03-33-REFUND-FIRST-REMEASURE-2026-08-20.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace("sequential race is dead", "race survives in main.go", 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "firing: doc recants the refutation is FAIL"
else bad "recant should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/commercial/commerce/internal/pump/pump.go"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing pump.go is COULD NOT LOOK"
else bad "missing pump.go should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: restored live stays CLEAN"
else bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"; fi

echo "check-c03-33-refund-first selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
