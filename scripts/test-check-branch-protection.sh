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
  local nombre="$1" f="$2" esperado="$3" texto="${4:-}" salida rc
  salida=$(OLIVARES_PROTECTION_JSON="$f" bash "$GATE" 2>&1); rc=$?
  if [ "$rc" -ne "$esperado" ]; then
    echo "  ✖ $nombre — rc=$rc, esperaba $esperado"; echo "$salida" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  if [ -n "$texto" ] && ! grep -qF "$texto" <<<"$salida"; then
    echo "  ✖ $nombre — rc correcto pero NO dice «$texto»"; echo "$salida" | head -3 | sed 's|^|      |'
    falla=$((falla+1)); return
  fi
  pasa=$((pasa+1))
}

# ── 1. La configuración CORRECTA pasa ────────────────────────────────────────────────────────
# ⛔ Y ES EL CASO QUE MÁS IMPORTA: los tres campos valen `false`/`true` de verdad, y la primera
#    versión del gate los leía con `x // "ausente"` — en jq, `//` devuelve la alternativa cuando el
#    izquierdo es null O FALSE, así que acusaba de rota una protección impecable.
viva "$TMP/ok" true false false classify control-plane race-hot web
comprueba "correcta · pasa (y un false NO es 'ausente')" "$TMP/ok" 0 "OK"

# ── 2. Cada control apagado, uno a uno, y NOMBRADO ───────────────────────────────────────────
viva "$TMP/fp" true true false classify control-plane race-hot web
comprueba "force-push permitido · ROTO y lo nombra" "$TMP/fp" 1 "allow_force_pushes"
viva "$TMP/del" true false true classify control-plane race-hot web
comprueba "borrados permitidos · ROTO y lo nombra" "$TMP/del" 1 "allow_deletions"
viva "$TMP/st" false false false classify control-plane race-hot web
comprueba "strict apagado · ROTO y lo nombra" "$TMP/st" 1 "strict"

# ── 3. Un contexto que falta se NOMBRA (no un contador) ──────────────────────────────────────
viva "$TMP/ctx" true false false classify control-plane web
comprueba "falta un contexto · lo nombra" "$TMP/ctx" 1 "race-hot"

# ── 4. Ausente DE VERDAD (la clave no está) sigue siendo ROTO ────────────────────────────────
#    Distinto del caso 1: aquí el campo NO existe, y eso no puede leerse como 'false'.
cat > "$TMP/sinclave" <<'JSON'
{ "required_status_checks": { "strict": true, "contexts": ["classify","control-plane","race-hot","web"] } }
JSON
comprueba "campo AUSENTE · ROTO, no un falso verde" "$TMP/sinclave" 1 "ausente"

# ── 5. Las tres respuestas: lo ilegible no es lo limpio ──────────────────────────────────────
echo 'esto no es json' > "$TMP/basura"
comprueba "respuesta no-JSON · NO HE PODIDO MIRAR" "$TMP/basura" 2 "NO HE PODIDO MIRAR"
: > "$TMP/vacio"
comprueba "respuesta vacía · NO HE PODIDO MIRAR" "$TMP/vacio" 2 "VACÍA"
comprueba "fichero inexistente · NO HE PODIDO MIRAR" "$TMP/no-existe" 2 "no puedo leer"

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
salida=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/aplica-dos-perfiles.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && grep -qF "classify,control-plane,race-hot,web" <<<"$salida"; then
  pasa=$((pasa+1))
else
  echo "  ✖ dos perfiles · debe leer el bloque hub — rc=$rc"; echo "$salida" | head -3 | sed 's|^|      |'
  falla=$((falla+1))
fi

echo 'sin contextos aqui' > "$TMP/aplica-sin.sh"
salida=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/aplica-sin.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ fuente sin contextos · esperaba rc=2, dio $rc"; falla=$((falla+1)); fi

salida=$(OLIVARES_PROTECTION_JSON="$TMP/ok" OLIVARES_PROTECTION_SOURCE="$TMP/no-existe.sh" \
  bash "$GATE" 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then pasa=$((pasa+1)); else
  echo "  ✖ fuente ilegible · esperaba rc=2, dio $rc"; falla=$((falla+1)); fi

echo
echo "$pasa passed, $falla failed"
[ "$falla" -eq 0 ]
