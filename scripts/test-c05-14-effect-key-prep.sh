#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/internal/store/tenantstatements.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/migrations/004_suspension_log.up.sql — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/migrations/013_plan_enforcement.up.sql — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/internal/store/tenantstatements.go ]; then
	printf '%s\n' "test-c05-14-effect-key-prep: COULD NOT LOOK — cloud/control-plane/internal/store/tenantstatements.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/migrations/004_suspension_log.up.sql ]; then
	printf '%s\n' "test-c05-14-effect-key-prep: COULD NOT LOOK — cloud/control-plane/migrations/004_suspension_log.up.sql is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/migrations/013_plan_enforcement.up.sql ]; then
	printf '%s\n' "test-c05-14-effect-key-prep: COULD NOT LOOK — cloud/control-plane/migrations/013_plan_enforcement.up.sql is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-14-effect-key-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0514prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/cloud/control-plane/migrations" \
    "$TMP/tree/cloud/control-plane/internal/store"
  cp "$ROOT/design/c05-14-effect-key-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C05-14-EFFECT-KEY-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/cloud/control-plane/migrations/004_suspension_log.up.sql" \
    "$TMP/tree/cloud/control-plane/migrations/"
  cp "$ROOT/cloud/control-plane/migrations/013_plan_enforcement.up.sql" \
    "$TMP/tree/cloud/control-plane/migrations/"
  cp "$ROOT/cloud/control-plane/internal/store/tenantstatements.go" \
    "$TMP/tree/cloud/control-plane/internal/store/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-14-effect-key-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-14-effect-key-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe effect-key pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-14-effect-key-prep-2026-08-20.json" <<'PY'
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
printf '\n    effect_key TEXT\n' >> \
  "$TMP/tree/cloud/control-plane/migrations/004_suspension_log.up.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (004 gained effect_key) is killed"
else bad "004 effect_key stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '%s\n' '-- collide' > \
  "$TMP/tree/cloud/control-plane/migrations/013_suspension_log_effect_key.up.sql"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (013 filename collide) is killed"
else bad "013 collide stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-14-effect-key-prep-2026-08-20.json" <<'PY'
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
rm -f "$TMP/tree/design/c05-14-effect-key-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-14-effect-key-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
