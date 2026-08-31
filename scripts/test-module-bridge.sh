#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-module-bridge.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/mod-bridge.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/commercial" "$TMP/tree/scripts"
  cp "$ROOT/design/VOCABULARIO-MODULOS-2026-08-08.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-module-bridge.sh"
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-module-bridge.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live map is CLEAN"; else bad "live map should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"]=[e for e in d["entries"] if e["slug"]!="iso42001"]
json.dump(d, open(p,"w"))
PY
if run; then bad "dropping iso42001 stayed CLEAN"; else ok "JSON missing a VOCABULARIO slug is a finding"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"].append({"slug":"invented-addon","package":"enterprise/invented"})
json.dump(d, open(p,"w"))
PY
if run; then bad "invented slug stayed CLEAN"; else ok "JSON slug not in VOCABULARIO is a finding"; fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
if run; then bad "missing JSON stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing JSON is COULD NOT LOOK"
  else bad "missing JSON should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-module-bridge selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
