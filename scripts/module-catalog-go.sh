#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Thin wrapper: derive every projection of the module catalog with the TYPED parser in
# commercial/commerce-lint, and compare the tree against it.
#
#   module-catalog-go.sh check [--overlay DIR]
#   module-catalog-go.sh write [--overlay DIR]
#
# ⛔ THREE ANSWERS, AND THE EXIT CODE CARRIES THEM (canon rule 5):
#   0  CLEAN            every projection matches the derivation
#   1  FINDING          it could look and something differs
#   2  COULD NOT LOOK   the canon or the tool is unreadable, or -overlay points at a non-overlay
#
# ⛔ AND ONE ANSWER MORE THAT IS NOT A VERDICT: `NOT APPLICABLE`. This file is AGPL and lives in
# scripts/, which the PUBLIC EXPORT ships; the deriver it calls lives in commercial/, which that
# export curates out wholesale. In a public tree the tool is simply absent, and the honest answer is
# to say so and exit 0 — not to crash with a build error, and above all not to print nothing and
# exit 0, which is the silent green this repository has been bitten by before. The wording and the
# reasoning are lifted from scripts/addon-sets-go.sh on purpose: two wrappers over the same binary
# that disagree about what an absent binary means is how one of them ends up wrong.
set -eu

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
MODE="${1:-check}"
shift || true

OVERLAY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --overlay) OVERLAY="${2:-}"; shift 2 || { echo "module-catalog: --overlay needs a directory" >&2; exit 2; } ;;
    *) echo "module-catalog: unknown argument $1" >&2; exit 2 ;;
  esac
done

case "$MODE" in
  check|write) ;;
  *) echo "module-catalog: mode must be check or write, not $MODE" >&2; exit 2 ;;
esac

# ⛔ EL TOKEN VA A STDOUT Y ES LEGIBLE POR MÁQUINA, no sólo prosa a stderr. Un llamador que reciba
# 0 sin poder distinguir «coincide» de «aquí no aplica» escribiría CLEAN sobre algo que nadie
# comparó — que es la tercera respuesta colapsada en la primera, el defecto más caro del canon
# (regla 5). Los gates que llaman a este envoltorio leen esta línea y degradan su veredicto a
# SCOPED.
if [ ! -d "$ROOT/commercial/commerce-lint" ]; then
  echo "MODULE-CATALOG-NOT-APPLICABLE"
  echo "module-catalog: NOT APPLICABLE — commercial/commerce-lint is not present in this tree" >&2
  exit 0
fi
if [ ! -r "$ROOT/design/PRICING-CANON.md" ]; then
  echo "module-catalog: COULD NOT LOOK — cannot read the canon at $ROOT/design/PRICING-CANON.md" >&2
  exit 2
fi

# `go build` + execute, not `go run`: go run does not propagate the exit code of the built program
# faithfully in every path, and a gate that cannot tell a refusal (2) from a finding (1) is not a
# gate. The staleness test names every source the binary depends on for THIS mode, so a fix to the
# generator cannot be shadowed by a binary built before it.
# ⛔ AQUÍ SE PODÍA ANUNCIAR `CLEAN` SIN QUE EL GENERADOR PRODUJERA UN BYTE. Tres defectos que el
# contraste `sol max` midió el 2026-09-03 (F05), y los tres estaban en estas veinte líneas:
#
#  1. **La caché era un nombre GLOBAL con vigencia por mtime.** `${TMPDIR}/olivares-module-catalog`
#     no estaba ligado ni al contenido ni al worktree. Con una copia de `/bin/true` y un mtime
#     futuro en ese sitio, el wrapper no reconstruía y `check-c13-02-worker-map.sh` anunciaba
#     `CLEAN`. Y no hace falta un señuelo: un checkout con timestamps antiguos frente a una caché
#     que dejó OTRO worktree reproduce la misma clase.
#  2. **La vía de inyección hacía `exec` con sólo comprobar el bit ejecutable.** Sin handshake, un
#     binario cualquiera pasaba por el derivador.
#  3. **Un rc distinto de 0/1/2** —125 con `timeout` inyectado, o 128+señal si lo matan— caía en la
#     rama «éxito» de cuatro callers.
#
# La caché pasa a llevar el DIGEST de todas las fuentes y la versión de Go en su nombre, así que un
# cambio de fuente no puede reutilizar un binario viejo ni por mtime ni por accidente; y el binario
# —heredado o construido— tiene que **identificarse** antes de que se le crea nada.
# ⛔⛔ Y LA IDENTIDAD DE LA CACHE OMITIA EL GRAFO DE DEPENDENCIAS, ASI QUE LA MISMA CLASE VOLVIA A
# ENTRAR POR OTRA PUERTA. Lo levanto la revision independiente (F1) y esta sesion lo reprodujo sobre
# una copia desechable del arbol entregado, sin tocar la cache compartida:
#
#   1) module-catalog-go.sh check                      -> rc 0, cache caliente, clave 53cf27cb0feefb2c
#   2) se cambia SOLO go.mod (gopkg.in/yaml.v3 v3.0.1 -> v9.9.9); ningun .go se toca
#      control: go build ./commercial/commerce-lint    -> rc 1, «version "v9.9.9" invalid»
#   3) module-catalog-go.sh check                      -> rc 0 «CLEAN — 4 views match»
#      check-c13-02-worker-map.sh                      -> rc 0 «CLEAN»
#      clave identica y mtime del binario identico: NO se reconstruyo nada
#
# `go.mod` y `go.sum` deciden CONTRA QUE `gopkg.in/yaml.v3` se enlaza el derivador, es decir COMO se
# parsea el canon. Fuera de la clave, un bump de dependencia que no toque ningun `.go` se valida con
# el parser ANTERIOR en las SEIS puertas que leen este envoltorio, sobre un TMPDIR caliente. El
# apreton de manos no lo ve: el binario reutilizado SI es el derivador, solo que construido desde
# otro grafo de modulos.
#
# ⇒ La identidad de la cache pasa a ser el contenido de TODO lo que decide el binario: las fuentes
# Go (recursivas, no solo las del primer nivel), `go.mod` y `go.sum`, mas la identidad del compilador.
# `find` en vez de `ls .../*.go` cubre ademas un subpaquete futuro, que `ls` se saltaria en silencio.
# La direccion del fallo queda del lado seguro: una fuente que no construye ya no puede reutilizar un
# binario verde, porque su clave es otra y la construccion vuelve a intentarse y falla con rc 2.
_fuentes() {
  # `-type f` y nombres explicitos: el contenido de `testdata/` no entra porque la herramienta Go no
  # lo compila. `LC_ALL=C sort` porque un orden dependiente del locale haria que la misma fuente
  # produjera dos claves distintas, y entonces la clave no seria una identidad.
  find "$ROOT/commercial/commerce-lint" -type f \
    \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) 2>/dev/null | LC_ALL=C sort
}
_digest() {
  # sha256 del contenido de esas fuentes + la versión de Go. Si no se puede calcular, es «no pude
  # mirar»: una caché sin identidad es exactamente el defecto que esto cierra.
  { _fuentes | while IFS= read -r f; do printf '%s\n' "$f"; cat "$f"; done; go version; } 2>/dev/null |
    sha256sum 2>/dev/null | cut -c1-16
}

# El HANDSHAKE: se le pide al binario que se identifique con la bandera que sólo él conoce, y su
# respuesta tiene que llevar la marca. Un `/bin/true` sale 0 y no imprime nada; un `timeout` sale
# 125. Los dos fallan aquí, ANTES de que su silencio se lea como acuerdo.
_identifica() {
  local b="$1" out
  out="$("$b" -module-catalog=identify 2>/dev/null)" || return 1
  case "$out" in *MODULE-CATALOG-DERIVER*) return 0 ;; esac
  return 1
}

if [ -n "${OLIVARES_MODULE_CATALOG_BIN:-}" ]; then
  [ -x "$OLIVARES_MODULE_CATALOG_BIN" ] || {
    echo "module-catalog: COULD NOT LOOK — OLIVARES_MODULE_CATALOG_BIN=$OLIVARES_MODULE_CATALOG_BIN is not executable" >&2
    exit 2
  }
  _identifica "$OLIVARES_MODULE_CATALOG_BIN" || {
    echo "module-catalog: COULD NOT LOOK — el binario inyectado en OLIVARES_MODULE_CATALOG_BIN no se" >&2
    echo "  identifica como el derivador (no contesta a -module-catalog=identify). Un binario que no" >&2
    echo "  se identifica NO se ejecuta: su silencio se leeria como que las vistas coinciden." >&2
    exit 2
  }
  set -- -root "$ROOT" -module-catalog="$MODE"
  [ -n "$OVERLAY" ] && set -- "$@" -overlay-root "$OVERLAY"
  exec "$OLIVARES_MODULE_CATALOG_BIN" "$@"
fi

_d="$(_digest)"
[ -n "$_d" ] || { echo "module-catalog: COULD NOT LOOK — no puedo calcular el digest de las fuentes" >&2; exit 2; }
BIN="${TMPDIR:-/tmp}/olivares-module-catalog.$_d"
if [ ! -x "$BIN" ] || ! _identifica "$BIN"; then
  # Publicacion atomica: se construye a un temporal del MISMO directorio y se renombra, para que dos
  # carriles que compartan TMPDIR no se encuentren un binario a medio escribir.
  _tmpbin="$(mktemp "${TMPDIR:-/tmp}/olivares-module-catalog.XXXXXX")" || {
    echo "module-catalog: COULD NOT LOOK — no puedo crear el temporal del binario" >&2; exit 2; }
  ( cd "$ROOT/commercial/commerce-lint" && env GOWORK=off go build -o "$_tmpbin" . ) >&2 || {
    rm -f "$_tmpbin"
    echo "module-catalog: COULD NOT LOOK — the deriver did not build" >&2
    exit 2
  }
  chmod +x "$_tmpbin" && mv -f "$_tmpbin" "$BIN" || {
    rm -f "$_tmpbin"
    echo "module-catalog: COULD NOT LOOK — no pude publicar el binario construido" >&2; exit 2; }
  _identifica "$BIN" || {
    echo "module-catalog: COULD NOT LOOK — el binario recien construido no se identifica" >&2; exit 2; }
fi

set -- -root "$ROOT" -module-catalog="$MODE"
if [ -n "$OVERLAY" ]; then
  set -- "$@" -overlay-root "$OVERLAY"
fi
"$BIN" "$@"
