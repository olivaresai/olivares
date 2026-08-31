#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(
  cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
  pwd
)" || exit 2
CHECK="$ROOT/scripts/check-ver-09-stub-r2-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/ver09prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
if [ ! -r "$ROOT/scripts/check-commerce-preflight.sh" ]; then
  printf 'SKIP %s: check-commerce-preflight.sh es hub-only y no esta en este arbol\n' \
    "$(basename "${BASH_SOURCE[0]}")"
  exit 0
fi

pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts/lib" \
    "$TMP/tree/commercial/license-worker/src/download" \
    "$TMP/tree/commercial/license-worker/src/dodo" \
    "$TMP/tree/commercial/license-worker/src/polar"
  cp "$ROOT/design/ver-09-stub-r2-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/VER-09-STUB-R2-PREP-2026-08-20.md" "$TMP/tree/design/"
  # export-closure: hub-only scripts/check-commerce-preflight.sh — el preflight de comercio, que interroga infraestructura viva NO viaja
  # en el arbol publicado, y este test lo COPIA a su arbol de pruebas. Alli el `cp`
  # moriria por `set -e` y el rojo no seria del test. Guarda EN EL SITIO DE LLAMADA —no
  # basta una salida temprana, que alguien puede retirar dejando el cp desnudo— y con
  # `if`, nunca `[ -f X ] && cp`: una lista que acaba en `&&` con el lado izquierdo
  # falso devuelve 1 y `set -e` mata el guion.
  if [ -f "$ROOT/scripts/check-commerce-preflight.sh" ]; then
    cp "$ROOT/scripts/check-commerce-preflight.sh" "$TMP/tree/scripts/"
  fi
  cp "$ROOT/scripts/lib/git-env.sh" "$TMP/tree/scripts/lib/"
  cp "$ROOT/commercial/license-worker/src/download/artifacts.ts" \
    "$ROOT/commercial/license-worker/src/download/sets.ts" \
    "$ROOT/commercial/license-worker/src/download/manifests.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/src/dodo/catalog.ts" \
    "$TMP/tree/commercial/license-worker/src/dodo/"
  cp "$ROOT/commercial/license-worker/src/polar/products.ts" \
    "$TMP/tree/commercial/license-worker/src/polar/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-ver-09-stub-r2-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-ver-09-stub-r2-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe VER-09 pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = '        if size < STUB:\n            n_stub+=1'
new = '        if False and size < STUB:\n            n_stub+=1'
if s.count(old) != 1:
    raise SystemExit("live n_stub mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
if [ -f "$TMP/tree/scripts/check-commerce-preflight.sh" ]; then
  if ! bash -n "$TMP/tree/scripts/check-commerce-preflight.sh"; then
    bad "live n_stub mutant is not Bash-valid"
  fi
else
  bad "live n_stub mutant lost the hub-only preflight fixture"
fi
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (live n_stub classifier neutralized) is killed"
else
  bad "live n_stub mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = 'if r2_all_stub_refusal "${n_obj:-0}" "${n_stub:-0}"; then'
new = 'if false && r2_all_stub_refusal "${n_obj:-0}" "${n_stub:-0}"; then'
if s.count(old) != 1:
    raise SystemExit("live all-stub wiring mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
if [ -f "$TMP/tree/scripts/check-commerce-preflight.sh" ]; then
  if ! bash -n "$TMP/tree/scripts/check-commerce-preflight.sh"; then
    bad "live all-stub wiring mutant is not Bash-valid"
  fi
else
  bad "live all-stub wiring mutant lost the hub-only preflight fixture"
fi
run
if [ "$(cat "$TMP/rc")" = 1 ] && \
  grep -Fq 'live path refuses an all-stub canonical population' "$TMP/err"; then
  ok "mutant (live all-stub refusal disconnected) is killed"
else
  bad "live all-stub wiring mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = "if door_match and door_match.group(1) in allowed:"
new = 'if door_match and door_match.group(1) == "biz":'
if s.count(old) != 1:
    raise SystemExit("all-slug classifier mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'classifier lost paid slug' "$TMP/err"; then
  ok "mutant (classifier accepts only biz) is killed by the all-slug sweep"
else
  bad "biz-only classifier mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = 'if [ "$ver_ok" = "True" ] && [ "$n_obj" = "ILEGIBLE" ]; then'
new = 'if false && [ "$ver_ok" = "True" ] && [ "$n_obj" = "ILEGIBLE" ]; then'
if s.count(old) != 1:
    raise SystemExit("R2 COULD NOT LOOK wiring mutant did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'malformed R2 listings' "$TMP/err"; then
  ok "mutant (R2 ILEGIBLE disconnected from rc2) is killed"
else
  bad "R2 ILEGIBLE wiring mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = 'if (keys.has(manifestKey("stable", set)) && keys.has(manifestSigKey("stable", set))) {'
new = 'if (keys.has(manifestKey("stable", set))) {'
if s.count(old) != 1:
    raise SystemExit("manifest-signature mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'requires the detached manifest signature' "$TMP/err"; then
  ok "mutant (manifest counted without signature) is killed"
else
  bad "manifest-signature mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/ver-09-stub-r2-prep-2026-08-20.json" <<'PY'
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
sed -i 's/classify_r2()/classify_objects()/' "$TMP/tree/scripts/check-commerce-preflight.sh"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (classify_r2 dropped) is killed"
else bad "classify_r2 stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/ver-09-stub-r2-prep-2026-08-20.json" <<'PY'
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
rm -f "$TMP/tree/design/ver-09-stub-r2-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/scripts/lib/git-env.sh"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing preflight dependency is COULD NOT LOOK"
else bad "missing git-env.sh rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/scripts/check-commerce-preflight.sh" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = "classify_r2() {"
new = "function classify_r2 {"
if s.count(old) != 1:
    raise SystemExit("classify_r2 no-fire fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
if [ -f "$TMP/tree/scripts/check-commerce-preflight.sh" ]; then
  if ! bash -n "$TMP/tree/scripts/check-commerce-preflight.sh"; then
    bad "classify_r2 formatting no-fire is not Bash-valid"
  fi
else
  bad "classify_r2 formatting no-fire lost the hub-only preflight fixture"
fi
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: equivalent classify_r2 syntax stays CLEAN"
else bad "equivalent classify_r2 syntax should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-ver-09-stub-r2-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
