#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-aws-pasos-fran.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/awsfran.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/.github/workflows" "$TMP/tree/deploy/aws/modules/secrets"
  cp "$ROOT/design/aws-pasos-fran-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/AWS-PASOS-FRAN.md" "$TMP/tree/design/"
  cp "$ROOT/.github/workflows/aws-terraform.yml" "$TMP/tree/.github/workflows/"
  cp "$ROOT/deploy/aws/modules/secrets/main.tf" "$TMP/tree/deploy/aws/modules/secrets/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-aws-pasos-fran.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-aws-pasos-fran.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "owner steps HOLD is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/aws-pasos-fran-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["never_applied"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (never_applied false) is killed"
else bad "never_applied false stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nestate applied\n' >> "$TMP/tree/design/AWS-PASOS-FRAN.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims applied) is killed"
else bad "doc applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/aws-pasos-fran-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["tofu_applied_in_this_lote"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (tofu applied flag) is killed"
else bad "tofu flag stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/deploy/aws/modules/secrets/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
p.write_text(text.replace('"dsn", ', ""), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (secrets slots lost dsn) is killed"
else bad "slots mutant stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# La deriva que de verdad ocurrió (2026-08-31) fue una ADICIÓN, no una pérdida: el módulo
# creció dos ranuras en 0a13422ee y el control seguía enumerando seis. El caso de arriba
# QUITA una ranura y no habría visto nada. Éste añade.
stage
python3 - "$TMP/tree/deploy/aws/modules/secrets/main.tf" <<'MUT'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
assert '"cloud-cp-runtime",' in t
p.write_text(t.replace('"cloud-cp-runtime",', '"cloud-cp-runtime",\n    "cloud-cp-noveno",'), encoding="utf-8")
MUT
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (secrets slots GREW a ninth) is killed"
else bad "added-slot mutant stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Y el JSON y el módulo son DOS enumeraciones del mismo hecho: que una avance sin la otra
# es exactamente la clase de deriva que dejó `main` en rojo. El control las ata a las dos.
stage
python3 - "$TMP/tree/design/aws-pasos-fran-2026-08-20.json" <<'MUT'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["secret_slots"] = d["secret_slots"][:-1]      # el JSON se queda en siete, el módulo en ocho
json.dump(d, open(p, "w", encoding="utf-8"))
MUT
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (JSON slots drift from the module) is killed"
else bad "json-vs-module mutant stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/aws-pasos-fran-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-aws-pasos-fran selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
