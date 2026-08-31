#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg03-prod-switches.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg03.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
           "$TMP/tree/commercial/license-worker"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-cfg03-prod-switches.sh"
  cp "$ROOT/design/CFG-03-PRODUCTION-SWITCHES-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/wrangler.jsonc" \
    "$TMP/tree/commercial/license-worker/"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg03-prod-switches.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live inventory + prod-off is CLEAN"
else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
import re,sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
# Flip only the production occurrence (second FULFILLMENT true would be sandbox).
# Replace the production block's false after the "production": key.
# ⛔ EL PATRON SALTA LAS CLAVES INTERMEDIAS, y la razon esta medida: este bloque gano una
# clave `"triggers"` ANTES de `"vars"` (y comentarios JSONC entre medias). El patron viejo
# exigia `"production": { "vars": {` seguidos, asi que dejo de casar, LA MUTACION NO SE
# APLICO, el checker vio el arbol INTACTO y salio limpio — y el caso lo leyo como «se quedo
# limpio», que es el fallo que este banco reportaba. Un mutante que no se puede aplicar no
# es un mutante que pasa: es cobertura que dejo de existir en silencio.
#
# El salto es NO CODICIOSO y se acota a este bloque: `[^}]*?` no puede cruzar el cierre del
# objeto, asi que no puede alcanzar el "vars" de otro entorno.
t2=re.sub(
    r'("production":\s*\{(?:[^{}]|\{[^{}]*\})*?"vars":\s*\{\s*"FULFILLMENT_ENABLED":\s*)"false"',
    r'\1"true"',
    t, count=1, flags=re.S)
if t2 == t:
    sys.stderr.write("MUTACION NO APLICADA: el patron no caso. El banco no puede acreditar "
                     "un mutante que no existe.\n")
    sys.exit(2)
t = t2
open(p,"w",encoding="utf-8").write(t)
PY
if run; then bad "production FULFILLMENT true stayed CLEAN"
else ok "mutant (flip production sale) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
import json, sys
p = sys.argv[1]
raw = open(p, encoding="utf-8").read()
out, i, n, in_str, esc = [], 0, len(raw), False, False
while i < n:
    c = raw[i]
    if in_str:
        out.append(c)
        if esc: esc = False
        elif c == "\\": esc = True
        elif c == '"': in_str = False
        i += 1
        continue
    if c == '"':
        in_str = True
        out.append(c)
        i += 1
        continue
    if c == "/" and i + 1 < n and raw[i + 1] == "/":
        while i < n and raw[i] != "\n":
            i += 1
        continue
    out.append(c)
    i += 1
doc = json.loads("".join(out))
doc["env"]["production"]["vars"]["ENTERPRISE_VERSION"] = "26.8.0"
json.dump(doc, open(p, "w", encoding="utf-8"), indent=2)
PY
if run; then bad "production CalVer without bytes stayed CLEAN"
else ok "mutant (prod ENTERPRISE_VERSION armed) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
import json, sys
p = sys.argv[1]
raw = open(p, encoding="utf-8").read()
out, i, n, in_str, esc = [], 0, len(raw), False, False
while i < n:
    c = raw[i]
    if in_str:
        out.append(c)
        if esc: esc = False
        elif c == "\\": esc = True
        elif c == '"': in_str = False
        i += 1
        continue
    if c == '"':
        in_str = True
        out.append(c)
        i += 1
        continue
    if c == "/" and i + 1 < n and raw[i + 1] == "/":
        while i < n and raw[i] != "\n":
            i += 1
        continue
    out.append(c)
    i += 1
doc = json.loads("".join(out))
doc["env"]["production"]["vars"]["COMMERCE_PROVIDER"] = "polar"
json.dump(doc, open(p, "w", encoding="utf-8"), indent=2)
PY
if run; then bad "production COMMERCE_PROVIDER polar stayed CLEAN"
else ok "mutant (prod provider polar) is killed"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/wrangler.jsonc" <<'PY'
import json, sys
p = sys.argv[1]
raw = open(p, encoding="utf-8").read()
out, i, n, in_str, esc = [], 0, len(raw), False, False
while i < n:
    c = raw[i]
    if in_str:
        out.append(c)
        if esc: esc = False
        elif c == "\\": esc = True
        elif c == '"': in_str = False
        i += 1
        continue
    if c == '"':
        in_str = True
        out.append(c)
        i += 1
        continue
    if c == "/" and i + 1 < n and raw[i + 1] == "/":
        while i < n and raw[i] != "\n":
            i += 1
        continue
    out.append(c)
    i += 1
doc = json.loads("".join(out))
doc["env"]["production"]["vars"]["ISSUER_PURPOSE"] = "staging"
json.dump(doc, open(p, "w", encoding="utf-8"), indent=2)
PY
if run; then bad "production ISSUER_PURPOSE staging stayed CLEAN"
else ok "mutant (prod issuer staging) is killed"; fi

stage
sed -i 's/does not authorize/authorizes flipping/' \
  "$TMP/tree/design/CFG-03-PRODUCTION-SWITCHES-2026-08-18.md"
if run; then bad "doc claiming it flips stayed CLEAN"
else ok "mutant (inventory authorizes the flip) is killed"; fi

stage
if ! run; then bad "no-fire: live prod-off should stay CLEAN ($(cat "$TMP/err"))"
else ok "no-fire: live production-off inventory stays CLEAN"; fi

stage
rm -f "$TMP/tree/commercial/license-worker/wrangler.jsonc"
if run; then bad "missing wrangler stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing wrangler is COULD NOT LOOK"
  else bad "missing wrangler should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-cfg03-prod-switches selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
