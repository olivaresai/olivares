#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# rerere-build-postimages.sh — lista (y solo con --purge retira) las resoluciones de `rerere`
# cuyo POSTIMAGE es una SALIDA DE BUILD.
#
# ⛔ QUE DEFECTO PERSIGUE, medido el 2026-08-28. `rerere` graba «cuando veas ESTE conflicto, la
#    respuesta es ESTA». Para texto es util. Para un fichero GENERADO no lo es: el postimage que
#    se graba es el sello que produjo MI build, y describe MI arbol. Si el mismo par de digests
#    vuelve a chocar en otro sitio, `rerere` lo pega ahi — y sale verde describiendo un arbol que
#    puede no ser ese.
#
#    En el `rr-cache` de este clon habia **56** entradas con esa forma, de varios carriles, la mas
#    reciente de ese mismo dia. `rr-cache` vive en el git-common-dir, o sea COMPARTIDA por los ~48
#    worktrees: lo que graba un carril lo puede re-aplicar cualquiera.
#
# ⛔ Y NO SE LEE MAS GRAVE DE LO QUE ES, que es la otra mitad del trabajo:
#    · `rerere` solo casa el conflicto EXACTO (los dos mismos lados). 55 entradas no son 55
#      re-aplicaciones probables: hace falta repetir el mismo par —rehacer un merge tras
#      `--abort`, re-basar el mismo paso.
#    · `rerere.autoupdate` no esta fijado, asi que incluso al casar deja el indice SIN resolver.
#    · y `lint:web-bundle-freshness` lo caza despues. La ventana es «entre el merge y el gate».
#    La regla barata que lo hace inocuo sigue siendo: tras un merge que toque
#    `core/internal/webui/`, `task build:web` INCONDICIONAL.
#
# ⛔ POR DEFECTO NO BORRA NADA. El `rr-cache` es del clon y de todos los carriles; retirar
#    entradas ajenas por cuenta propia es exactamente lo que este arbol castiga. `--purge` es
#    explicito y se ejecuta a sabiendas.
#
# Uso:  bash scripts/rerere-build-postimages.sh [--purge] [--selftest]
# Sale: 0 siempre que pueda mirar (listar no es un veredicto); 2 si NO ha podido mirar.
set -uo pipefail

# ⛔ AISLAMIENTO DEL ENTORNO GIT. Este guion crea un `rr-cache` sintetico con `mktemp -d` en su
#    selftest y consulta `git rev-parse --git-common-dir` en la corrida real. **`GIT_DIR` MANDA
#    SOBRE `-C` y sobre el directorio de trabajo**, asi que un `GIT_DIR` heredado —git lo exporta
#    a todo hook `pre-push` desde un worktree enlazado— haria que leyera el `rr-cache` de OTRO
#    repositorio. Y con `--purge` **borraria del almacen equivocado**, que es lo unico
#    irreversible que hace este guion.
#
#    Lo caza `task lint:git-env`, y me lo cazo: la primera version no sourceaba la lib y el gate
#    salio `BROKEN` en el minuto 60 de un push. Un guion nuevo que usa git paga DOS gates que no
#    se ven hasta entonces —`git-env` y `taskfile-graph`— y los dos caben en el pre-vuelo.
#
# Fail-closed: no poder cargar el saneador es «no he podido aislar», nunca «no hacia falta».
_rrbp_here="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)" || {
  echo "rerere-build-postimages: 2 NO HE PODIDO MIRAR — no pude resolver mi propio directorio" >&2
  exit 2
}
# shellcheck source=/dev/null
. "${_rrbp_here}/lib/git-env.sh" || {
  echo "rerere-build-postimages: 2 NO HE PODIDO MIRAR — no pude cargar lib/git-env.sh" >&2
  exit 2
}

no_puedo() { printf 'rerere-build-postimages: 2 NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

# La FORMA de un sello: UNA linea, digest de 64 hex, un espacio, un entero. No es el nombre del
# fichero: el nombre no viaja en el `rr-cache`, solo el contenido del conflicto y su resolucion.
es_de_build() {
  local f="$1"
  [ -f "$f" ] || return 1
  [ "$(wc -l < "$f" 2>/dev/null || echo 9)" = "1" ] || return 1
  LC_ALL=C grep -qE '^[0-9a-f]{64} [0-9]+$' "$f"
}

censo() {
  local cache="$1" purgar="$2" repo="${3:-}"
  local n=0 p=0
  [ -d "$cache" ] || { printf '  rr-cache no existe: %s\n' "$cache"; return 0; }
  local e f
  while IFS= read -r e; do
    [ -n "$e" ] || continue
    f="$cache/$e/postimage"
    es_de_build "$f" || continue
    n=$((n + 1))
    printf '  %s  %s  %s\n' "$e" \
      "$(date -u -r "$cache/$e" +%Y-%m-%dT%H:%MZ 2>/dev/null || echo '????-??-??T??:??Z')" \
      "$(head -c 24 "$f")"
    if [ "$purgar" = "1" ]; then
      rm -rf -- "$cache/$e" && p=$((p + 1))
    fi
  done < <(ls -1 "$cache" 2>/dev/null | LC_ALL=C sort)
  printf 'rerere-build-postimages: %s entrada(s) con postimage de build' "$n"
  [ "$purgar" = "1" ] && printf ' · %s retirada(s)' "$p"
  printf '\n'
  return 0
}

selftest() {
  local t; t=$(mktemp -d "${TMPDIR:-/tmp}/rrbp.XXXXXX") || no_puedo "no pude crear el temporal"
  # shellcheck disable=SC2064
  trap "rm -rf '$t'" EXIT
  local c="$t/rr-cache" oks=0 fails=0
  ok()  { oks=$((oks + 1)); printf '  ok    %s\n' "$*"; }
  bad() { fails=$((fails + 1)); printf '  FAIL  %s\n' "$*"; }
  mkdir -p "$c/aaa" "$c/bbb" "$c/ccc"
  printf '%s 1051\n' "$(printf 'a%.0s' $(seq 64))" > "$c/aaa/postimage"
  # ⛔ `z` y no `b`: la primera version de este fixture uso `bbb…` como «no hexadecimal» y **la
  #    `b` SI es un digito hex** ([0-9a-f]). El guion contaba 2 y tenia razon; el fixture estaba
  #    mal. Lo caza su propio selftest, que es para lo que existe.
  printf '%s 42\n' "$(printf 'z%.0s' $(seq 64))" > "$c/bbb/postimage"
  printf 'una resolucion de TEXTO\ncon dos lineas\n' > "$c/ccc/postimage"
  local out; out=$(censo "$c" 0)
  case "$out" in
    *"1 entrada(s) con postimage de build"*) ok "cuenta SOLO la que tiene forma de sello (hex)" ;;
    *) bad "esperaba 1 entrada; salio: $(printf '%s' "$out" | tail -1)" ;;
  esac
  case "$out" in
    *ccc*) bad "una resolucion de texto NO puede entrar en el censo" ;;
    *)     ok "la resolucion de TEXTO se queda fuera" ;;
  esac
  # y con --purge: retira la de build y deja las otras dos
  out=$(censo "$c" 1)
  case "$out" in
    *"1 retirada(s)"*) ok "--purge retira exactamente las de build" ;;
    *) bad "esperaba 1 retirada; salio: $(printf '%s' "$out" | tail -1)" ;;
  esac
  if [ -d "$c/ccc" ] && [ -d "$c/bbb" ] && [ ! -d "$c/aaa" ]; then
    ok "deja intactas las que no son de build"
  else
    bad "deberia quedar bbb y ccc y no aaa: $(ls -1 "$c" | tr '\n' ' ')"
  fi
  # ⛔ y se comprueba que `rerere.autoupdate` NO esta fijado: si lo estuviera, una re-aplicacion
  #    quedaria INDEXADA sola y la ventana de riesgo dejaria de ser «entre el merge y el gate».
  local au; au=$(git config --get rerere.autoupdate 2>/dev/null || true)
  case "$au" in
    ""|false) ok "rerere.autoupdate sin fijar o false (la re-aplicacion no se indexa sola)" ;;
    *)        bad "rerere.autoupdate=$au — una re-aplicacion se indexaria SOLA" ;;
  esac
  # ⛔ AISLAMIENTO: con un `GIT_DIR` envenenado, este guion NO puede acabar operando sobre otro
  #    repositorio. `GIT_DIR` manda sobre `-C` y sobre el cwd, asi que sin el saneador un
  #    `--purge` borraria del `rr-cache` equivocado. Se comprueba que tras sourcear la lib la
  #    variable ya no esta en el entorno: eso es lo que hace `olivares_git_env_isolate`.
  if [ -z "${GIT_DIR:-}" ] && [ -z "${GIT_WORK_TREE:-}" ]; then
    ok "el entorno git quedo aislado (GIT_DIR y GIT_WORK_TREE sin fijar)"
  else
    bad "GIT_DIR='${GIT_DIR:-}' GIT_WORK_TREE='${GIT_WORK_TREE:-}' — el saneador no actuo"
  fi
  printf 'rerere-build-postimages selftest: %s passed, %s failed\n' "$oks" "$fails"
  [ "$fails" = "0" ] || return 1
  return 0
}

PURGAR=0
for a in "$@"; do
  case "$a" in
    --selftest) selftest; exit $? ;;
    --purge)    PURGAR=1 ;;
    *)          no_puedo "argumento desconocido: $a" ;;
  esac
done

command -v git >/dev/null 2>&1 || no_puedo "no hay git en el PATH"
CACHE="$(git rev-parse --git-common-dir 2>/dev/null)/rr-cache" || no_puedo "no estoy en un repositorio git"
printf 'rerere-build-postimages: %s\n' "$CACHE"
censo "$CACHE" "$PURGAR"
