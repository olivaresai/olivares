#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# export-closure: hub-only cloud/control-plane/cmd/cloud-cp/main.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager.go — el modulo cloud/ no viaja al export
# export-closure: hub-only cloud/control-plane/internal/tenant/manager_test.go — el modulo cloud/ no viaja al export
# Sin el modulo no hay sujeto que comprobar. La respuesta correcta es la TERCERA del
# canon —«no he podido mirar»—, no un verde y no el error crudo con el que muere hoy.
if [ ! -f "$ROOT"/cloud/control-plane/cmd/cloud-cp/main.go ]; then
	printf '%s\n' "test-c05-02-mailer-required-prep: COULD NOT LOOK — cloud/control-plane/cmd/cloud-cp/main.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/tenant/manager.go ]; then
	printf '%s\n' "test-c05-02-mailer-required-prep: COULD NOT LOOK — cloud/control-plane/internal/tenant/manager.go is not in this tree" >&2
	exit 2
fi
if [ ! -f "$ROOT"/cloud/control-plane/internal/tenant/manager_test.go ]; then
	printf '%s\n' "test-c05-02-mailer-required-prep: COULD NOT LOOK — cloud/control-plane/internal/tenant/manager_test.go is not in this tree" >&2
	exit 2
fi
CHECK="$ROOT/scripts/check-c05-02-mailer-required-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0502prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/cloud/control-plane/internal/tenant" \
    "$TMP/tree/cloud/control-plane/cmd/cloud-cp"
  cp "$ROOT/design/c05-02-mailer-required-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C05-02-MAILER-REQUIRED-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/cloud/control-plane/internal/tenant/manager.go" \
    "$TMP/tree/cloud/control-plane/internal/tenant/"
  cp "$ROOT/cloud/control-plane/internal/tenant/manager_test.go" \
    "$TMP/tree/cloud/control-plane/internal/tenant/"
  cp "$ROOT/cloud/control-plane/cmd/cloud-cp/main.go" \
    "$TMP/tree/cloud/control-plane/cmd/cloud-cp/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c05-02-mailer-required-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c05-02-mailer-required-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe mailer-required pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-02-mailer-required-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["mailer_required"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (mailer optional) is killed"
else bad "optional mailer stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i '/mailer not wired/d' \
  "$TMP/tree/cloud/control-plane/internal/tenant/manager.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (lost refuse) is killed"
else bad "lost refuse stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i '/WithFirstOwnerMailer/d' \
  "$TMP/tree/cloud/control-plane/cmd/cloud-cp/main.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (boot lost mailer) is killed"
else bad "boot lost mailer stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nFIRMA A claimed\n' >> \
  "$TMP/tree/design/C05-02-MAILER-REQUIRED-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims FIRMA A) is killed"
else bad "doc FIRMA A stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c05-02-mailer-required-prep-2026-08-20.json" <<'PY'
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
rm -f "$TMP/tree/design/c05-02-mailer-required-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c05-02-mailer-required-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
