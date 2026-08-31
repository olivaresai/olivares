#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-tier-card.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/tier-card.XXXXXX")"

# ⛔ ${TMPDIR:-/tmp} PUEDE ESTAR MONTADO noexec —el /tmp de este contenedor lo está— y el sujeto de
# esta batería comprueba `[ -x scripts/addon-sets.sh ]`. En un montaje noexec ese test consulta
# access(X_OK), que devuelve EACCES AUNQUE EL BIT ESTÉ PUESTO: el `chmod +x` de más abajo funciona,
# `ls -l` enseña `-rwxr-xr-x`, y `[ -x ]` sale FALSO igual.
#
# El síntoma engaña más que el de sus hermanas: no falla al EJECUTAR, falla al PREGUNTAR, así que el
# gate informa «addon-sets.sh missing» sobre un fichero que existe, es ejecutable y da CLEAN si lo
# corres a mano. Medido el 2026-08-19: `lint:addon-sets-gate` ROJO en `origin/main` limpio, y por
# vivir en el carril rápido bloqueaba el push de toda máquina con /tmp noexec.
#
# Es la misma clase que ya documentaron test-alias-image-digest.sh (2026-08-01) y
# test-publish-enterprise-artifacts.sh (#1065). El nombre del respaldo usa el prefijo que
# `.gitignore` ya cubre (`/.tmpexec.*`), para que un residuo nunca salga untracked.
printf '#!/bin/sh\nexit 0\n' >"$TMP/.execprobe" && chmod +x "$TMP/.execprobe"
if ! [ -x "$TMP/.execprobe" ]; then
	rm -rf "$TMP"
	TMP="$(mktemp -d "$ROOT/.tmpexec.tier-card.XXXXXX")" || exit 2
fi
rm -f "$TMP/.execprobe"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/commercial" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
  cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
  cp "$ROOT/scripts/addon-sets.sh" "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/"*.sh
}
run() { OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-tier-card.sh" >/dev/null 2>"$TMP/err"; }

stage
if run; then ok "live map + named HOLDs is CLEAN"; else bad "live tree should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["entries"]=[e for e in d["entries"] if e["slug"]!="content-firewall"]
json.dump(d, open(p,"w"))
PY
if run; then bad "dropping a shipping slug stayed CLEAN"; else ok "sold map missing a shipping slug is a finding"; fi

stage
# Mutant: empty the HOLD list so Appendix A slugs look like unexplained holes.
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
import re,sys
p=sys.argv[1]
text=open(p,encoding="utf-8").read()
text=re.sub(r"^hold-slug:.*$","",text,flags=re.M)
open(p,"w",encoding="utf-8").write(text)
PY
if run; then bad "empty HOLD list stayed CLEAN"; else ok "mutant (drop HOLD names) is killed"; fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
if run; then bad "missing JSON stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing JSON is COULD NOT LOOK"
  else bad "missing JSON should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-tier-card selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
