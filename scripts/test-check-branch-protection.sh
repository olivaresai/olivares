#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Batería de check-branch-protection.sh. Hermética: la respuesta de la API se INYECTA por fichero,
# así que no toca la red ni depende de la configuración viva de nadie.
#
# Dos de sus casos reproducen defectos que este gate TUVO, y por eso están escritos como pruebas y
# no como comentarios: el `//` de jq convirtiendo un `false` correcto en «ausente», y la extracción
# de contextos leyendo el perfil equivocado.

set -uo pipefail
cd "$(dirname "$0")/.."
GATE="scripts/check-branch-protection.sh"

pasa=0; falla=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/bp-XXXXXX")
trap 'rm -rf "$TMP"' EXIT

viva() { # viva <fichero> <strict> <force> <deletions> <contexto...>
  local f="$1" st="$2" fp="$3" del="$4"; shift 4
  local ctx="" c
  for c in "$@"; do ctx="${ctx:+$ctx,}\"$c\""; done
  cat > "$f" <<JSON
{ "required_status_checks": { "strict": $st, "contexts": [$ctx] },
  "allow_force_pushes": { "enabled": $fp },
  "allow_deletions": { "enabled": $del },
  "enforce_admins": { "enabled": false } }
JSON
}

comprueba() { # <nombre> <fichero> <rc esperado> [texto que DEBE salir]
  local nombre="$1" f="$2" esperado="$3" texto="${4:-}" output rc
  output=$(OLIVARES_PROTECTION_JSON="$f" bash "$GATE" 2>&1); rc=$?
  if [ "$rc" -ne "$esperado" ]; then
    echo "  ✖ $nombre — rc=$rc, expected $esperado"; echo "$output" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  if [ -n "$texto" ] && ! grep -qF "$texto" <<<"$output"; then
    echo "  ✖ $nombre — correct rc but does NOT report «$texto»"; echo "$output" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  pasa=$((pasa+1))
}

# ── 1. La configuración CORRECTA pasa ────────────────────────────────────────────────────────
# ⛔ Y ES EL CASO QUE MÁS IMPORTA: los tres campos valen `false`/`true` de verdad, y la primera
#    versión del gate los leía con `x // "ausente"` — en jq, `//` devuelve la alternativa cuando el
#    izquierdo es null O FALSE, así que acusaba de rota una protección impecable.
viva "$TMP/ok" true false false classify control-plane race-hot web
comprueba "correct · passes (and false is NOT 'missing')" "$TMP/ok" 0 "OK"

# ── 2. Cada control apagado, uno a uno, y NOMBRADO ───────────────────────────────────────────
viva "$TMP/fp" true true false classify control-plane race-hot web
comprueba "force-push allowed · BROKEN and named" "$TMP/fp" 1 "allow_force_pushes"
viva "$TMP/del" true false true classify control-plane race-hot web
comprueba "deletions allowed · BROKEN and named" "$TMP/del" 1 "allow_deletions"
viva "$TMP/st" false false false classify control-plane race-hot web
comprueba "strict disabled · BROKEN and named" "$TMP/st" 1 "strict"

# ── 3. Un contexto que falta se NOMBRA (no un contador) ──────────────────────────────────────
viva "$TMP/ctx" true false false classify control-plane web
comprueba "missing context · names it" "$TMP/ctx" 1 "race-hot"

# ── 4. Ausente DE VERDAD (la clave no está) sigue siendo ROTO ────────────────────────────────
#    Distinto del caso 1: aquí el campo NO existe, y eso no puede leerse como 'false'.
cat > "$TMP/sinclave" <<'JSON'
{ "required_status_checks": { "strict": true, "contexts": ["classify","control-plane","race-hot","web"] } }
JSON
comprueba "MISSING field · BROKEN, not a false pass" "$TMP/sinclave" 1 "missing"

# ── 5. Las tres respuestas: lo ilegible no es lo limpio ──────────────────────────────────────
echo 'esto no es json' > "$TMP/basura"
comprueba "non-JSON response · COULD NOT LOOK" "$TMP/basura" 2 "COULD NOT CHECK"
: > "$TMP/vacio"
comprueba "empty response · COULD NOT LOOK" "$TMP/vacio" 2 "EMPTY"
comprueba "nonexistent file · COULD NOT LOOK" "$TMP/no-existe" 2 "cannot read"

# ── 6. La fuente de los contextos: perfil equivocado y fuente ilegible ───────────────────────
# ⛔ La primera versión cogía el PRIMER `CONTEXTS=` del fichero, que es el del perfil PÚBLICO y vale
#    una variable. Este caso fija que se lee el bloque `hub)` y no otro.
cat > "$TMP/aplica-dos-perfiles.sh" <<'SH'
case "$PERFIL" in
  public)
    CONTEXTS="${CONTEXTS:-$DEFAULT_PUBLIC_CONTEXTS}"
    ;;
  hub)
    CONTEXTS="${CONTEXTS:-classify,control-plane,race-hot,web}"
    ;;
esac
SH
output=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/aplica-dos-perfiles.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && grep -qF "classify,control-plane,race-hot,web" <<<"$output"; then
  pasa=$((pasa+1))
else
  echo "  ✖ two profiles · must read the repository-specific block — rc=$rc"; echo "$output" | head -3 | sed 's|^|      |'
  falla=$((falla+1))
fi

echo 'sin contextos aqui' > "$TMP/aplica-sin.sh"
output=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/aplica-sin.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ source without contexts · expected rc=2, got $rc"; falla=$((falla+1)); fi

output=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/no-existe.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ unreadable source · expected rc=2, got $rc"; falla=$((falla+1)); fi

echo
echo "$pasa passed, $falla failed"
[ "$falla" -eq 0 ]
