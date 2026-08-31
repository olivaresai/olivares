#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# test-rebase-web-branch.sh — batería de `rebase-web-branch.sh`.
#
# Mide LO QUE IMPORTA, que es lo que se NIEGA a hacer. La versión que este guion sustituye avisaba
# de un choque no reconstruible y seguía, y con eso commiteó marcadores `<<<<<<<` dentro de un
# `.go` en dos commits. Una batería que sólo comprobara el camino feliz habría salido verde.
#
# Los señuelos son repositorios git de verdad bajo un temporal PROPIO (no `/tmp`, que en este
# contenedor es `noexec` y además lo comparten cinco carriles), con su `origin` local, y el SUT se
# invoca con `bash` para que el bit de ejecución no entre en la medida.
set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT. Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO
# —o sea, desde cualquier sesion en paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, los
# repositorios desechables que construye este banco son el repositorio VIVO de quien lo invoque.
# MEDIDO el 2026-08-30 contra un repositorio de destino desechable, con este mismo fichero y sin
# esta linea: el destino recibio COMMITS. Falla cerrado: no poder aislar es «no he podido».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate

SUT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/rebase-web-branch.sh"
[ -f "$SUT" ] || { echo "test-rebase-web-branch: ⛔ NO HE PODIDO MIRAR: no encuentro $SUT" >&2; exit 2; }

PASA=0; FALLA=0
juzgar() { # <rc esperado> <rc obtenido> <nombre> <salida> [fragmento exigido]
  local esp="$1" obt="$2" nom="$3" out="$4" frag="${5:-}"
  if [ "$esp" != "$obt" ]; then
    printf 'FALLO  %-58s esperaba=%s obtuvo=%s\n' "$nom" "$esp" "$obt"; FALLA=$((FALLA+1))
    printf '%s\n' "$out" | head -4 | sed 's/^/       SUT| /'
    return
  fi
  # ⛔ MISMA RAZON QUE EN EL SUT: `<productor> | grep -q` sale 141 CUANDO ACIERTA bajo pipefail.
  #    Aqui ademas seria una bateria que falla justo cuando el caso PASA.
  if [ -n "$frag" ] && case "$out" in *"$frag"*) false ;; *) true ;; esac; then
    printf 'FALLO  %-58s rc correcto pero sin decir «%s»\n' "$nom" "$frag"; FALLA=$((FALLA+1))
    printf '%s\n' "$out" | head -4 | sed 's/^/       SUT| /'
    return
  fi
  printf 'ok     %-58s %s\n' "$nom" "rc=$obt"; PASA=$((PASA+1))
}

# ⛔ FUERA DEL REPOSITORIO, Y ESTO LO ENSENO UN INCIDENTE PROPIO. La primera version puso los
#    senuelos en `<repo>/.rebase-web-selftest`, es decir DENTRO del arbol: el caso «fuera de un
#    repositorio git» corrio entonces el SUT contra MI PROPIO repositorio, que si es uno — llego a
#    ejecutar `task build:web` y a reescribir `bundle-source.stamp` en mi worktree. El caso «pasaba»
#    por una razon que no era la suya, y su mutante SOBREVIVIA. Un senuelo dentro del sujeto no es
#    un senuelo.
RAIZ_TMP=$(mktemp -d "${TMPDIR:-/var/tmp}/rebase-web-selftest.XXXXXX") || {
  echo "test-rebase-web-branch: ⛔ NO HE PODIDO MIRAR: no he podido crear el temporal" >&2; exit 2; }
trap 'rm -rf "$RAIZ_TMP"' EXIT

g() { GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
      GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
      git "$@"; }

senuelo() { # <fichero en conflicto> -> imprime el worktree de la rama
  # ⛔ DOS `local` SEPARADOS, y no es estilo: `local a="$1" b="$a"` declara AMBOS nombres antes de
  #    asignar, asi que `$a` en la segunda expansion es el local recien declarado y VACIO. Con
  #    `set -u` eso aborta la funcion; sin el, los tres senuelos habrian compartido directorio y
  #    dos casos habrian «pasado» dentro del rebase abierto de un tercero. Medido aqui.
  local choque="$1"
  local d="$RAIZ_TMP/$(printf '%s' "$choque" | tr '/.' '__')"
  mkdir -p "$d/origen" "$d/clon"
  ( cd "$d/origen" && g init -q -b main . \
      && mkdir -p "$(dirname "$choque")" && printf 'base\n' > "$choque" \
      && g add -A && g commit -qm base ) >/dev/null 2>&1
  # ⛔ EL MONTAJE SE COMPRUEBA, NO SE SUPONE: sin HEAD el clon no tiene contra qué rebasar y el
  #    SUT fallaría por una razón que no es la que el caso mide.
  g -C "$d/origen" rev-parse --verify -q HEAD >/dev/null 2>&1 || { printf ''; return 2; }
  ( cd "$d" && g clone -q origen clon ) >/dev/null 2>&1
  local lado_rama="${2:-de la rama}"
  local lado_main="${3:-de main}"
  ( cd "$d/clon" && g checkout -q -b feature/x \
      && printf '%s\n' "$lado_rama" > "$choque" && g add -A && g commit -qm rama ) >/dev/null 2>&1
  # y main se mueve en el MISMO fichero, para que choque
  ( cd "$d/origen" && printf '%s\n' "$lado_main" > "$choque" && g add -A && g commit -qm avance ) >/dev/null 2>&1
  printf '%s' "$d/clon"
}

# ── 1 · un choque en FUENTE se niega, y lo dice por su nombre ──────────────────────────────────
t=$(senuelo "core/api/algo.go"); rc_m=$?
if [ "$rc_m" = "2" ] || [ -z "$t" ]; then
  echo 'FALLO  el señuelo de fuente no se construyó — no concluyente'; FALLA=$((FALLA+1))
else
  out=$( cd "$t" && bash "$SUT" 2>&1 ); rc=$?
  juzgar 1 "$rc" 'un choque en un fichero FUENTE se NIEGA' "$out" 'core/api/algo.go'
  juzgar 1 "$rc" 'y avisa de que el rebase queda ABIERTO' "$out" 'EL REBASE QUEDA ABIERTO'
  # y no ha tocado el árbol más allá del rebase: el fichero conserva sus marcadores para el humano
  if [ "$(cd "$t" && git grep -c '^<<<<<<< ' -- 'core/api/algo.go' 2>/dev/null | tail -1)" = "" ]; then
    printf 'FALLO  %-58s el choque no quedó a la vista del humano\n' 'deja el conflicto sin resolver'; FALLA=$((FALLA+1))
  else
    printf 'ok     %-58s\n' 'deja el conflicto sin resolver, para el humano'; PASA=$((PASA+1))
  fi
fi

# ── 2 · sobre `main` se niega sin tocar nada ───────────────────────────────────────────────────
t=$(senuelo "core/api/otro.go")
if [ -n "$t" ]; then
  out=$( cd "$t" && git checkout -q main 2>/dev/null; cd "$t" && bash "$SUT" 2>&1 ); rc=$?
  juzgar 1 "$rc" 'sobre main se NIEGA' "$out" 'no main'
fi

# ── 3 · con HEAD desprendido se niega ANTES de rebasar ─────────────────────────────────────────
t=$(senuelo "core/api/tercero.go")
if [ -n "$t" ]; then
  out=$( cd "$t" && git checkout -q --detach 2>/dev/null; cd "$t" && bash "$SUT" 2>&1 ); rc=$?
  juzgar 1 "$rc" 'con HEAD DESPRENDIDO se niega antes de empezar' "$out" 'desprendido'
fi

# ── 4 · fuera de un repositorio git: NO HE PODIDO MIRAR (2), no un verde ───────────────────────
mkdir -p "$RAIZ_TMP/sin-git"
out=$( cd "$RAIZ_TMP/sin-git" && bash "$SUT" 2>&1 ); rc=$?
juzgar 2 "$rc" 'fuera de un repositorio git dice NO HE PODIDO MIRAR' "$out" 'NO HE PODIDO MIRAR'

# ── 5 · desde un SUBDIRECTORIO se ancla a la raiz del worktree ─────────────────────────────────
#    Reproduce el incidente que costo el arreglo: corrido desde un subdirectorio, el guion
#    calculaba el digest sin poder leer las fuentes y escribia un sello BASURA sobre el bueno.
t=$(senuelo "core/api/cuarto.go")
if [ -n "$t" ]; then
  mkdir -p "$t/un/sub/dir"
  out=$( cd "$t/un/sub/dir" && bash "$SUT" 2>&1 ); rc=$?
  raiz=$(printf '%s' "$out" | sed -n 's/^rebase-web-branch: raiz //p' | head -1)
  real=$(cd "$t" && pwd -P)
  if [ "$(cd "${raiz:-/nada}" 2>/dev/null && pwd -P)" = "$real" ]; then
    printf 'ok     %-58s\n' 'desde un subdirectorio se ancla a la raiz'; PASA=$((PASA+1))
  else
    printf 'FALLO  %-58s ancla=%s raiz=%s\n' 'desde un subdirectorio se ancla a la raiz' "${raiz:-<vacio>}" "$real"
    FALLA=$((FALLA+1))
    printf '%s\n' "$out" | head -3 | sed 's/^/       SUT| /'
  fi
fi

# ── 6 · el choque del TRINQUETE se resuelve y se dice, en vez de refusarse ─────────────────────
#    ⛔ ESTE CASO EXISTE PORQUE FALTABA. Los cinco de arriba chocan en un FUENTE, asi que el SUT se
#    niega antes de llegar a la resolucion del trinquete — el camino que de verdad hace trabajo
#    estaba sin una sola casilla. Se destapo al reescribir ese `if` como `case` para quitarle una
#    tuberia a `grep -q` (que bajo pipefail sale 141 CUANDO ACIERTA): el cambio compilaba, la
#    bateria seguia en verde, y no media nada.
#
#    No llega al final a proposito: tras resolver, el SUT reconstruye el bundle y el senuelo no es
#    el repo. Lo que se afirma es que RESUELVE Y LO DICE, no que termine.
t=$(senuelo "cmd/olivares/consoleroutes_test.go" "const consoleUncoveredBudget = 61" "const consoleUncoveredBudget = 60")
if [ -n "$t" ]; then
  out=$( cd "$t" && bash "$SUT" 2>&1 )
  case "$out" in
    *"trinquete resuelto al valor de main"*)
      printf 'ok     %-58s\n' 'el choque del trinquete se resuelve y se dice'; PASA=$((PASA+1)) ;;
    *)
      printf 'FALLO  %-58s el SUT no resolvio el trinquete\n' 'el choque del trinquete se resuelve y se dice'
      FALLA=$((FALLA+1)); printf '%s\n' "$out" | head -4 | sed 's/^/       SUT| /' ;;
  esac
fi


# ── 7 · la POST-CONDICION del amend: se juzga LO COMMITEADO ───────────────────────────────────
#    El SUT indexa lo regenerado con varios `git add` y luego enmienda el commit de cabeza. Los
#    `add` y el `--amend` escriben EL MISMO indice, asi que un `index.lock` transitorio puede
#    tumbar un `add` y soltarse antes del commit: ese camino deja un commit SIN lo regenerado y el
#    SUT diciendo «✓», es decir HEREDANDO el trinquete que existe para RE-MEDIR.
#    MEDIDO el 2026-08-24 rebasando: un candado ajeno dejo el trinquete sin indexar mientras
#    `dist` y el sello SI entraron. Guardar cada `add` estrecha la ventana; comprobar el RESULTADO
#    la cierra. Estas casillas entran por `--verificar-amend`, que corre LA MISMA funcion que la
#    ruta de produccion: una copia de la logica en la bateria envejeceria aparte.
montar_trinquete() { # <dir> <valor commiteado>
  mkdir -p "$1/cmd/olivares" "$1/core/internal/webui/dist" || return 1
  ( cd "$1" || exit 1
    git init -q .
    git config user.email testigo@olivares.ai
    git config user.name testigo
    printf 'const consoleUncoveredBudget = %s\n' "$2" > cmd/olivares/consoleroutes_test.go
    printf 'x\n' > core/internal/webui/dist/a.js
    printf 'sello\n' > core/internal/webui/bundle-source.stamp
    git add -- cmd/olivares/consoleroutes_test.go core/internal/webui/dist \
               core/internal/webui/bundle-source.stamp
    git commit -qm base ) >/dev/null 2>&1
}

t="$RAIZ_TMP/pc-coincide"
if montar_trinquete "$t" 7; then
  out=$( cd "$t" && bash "$SUT" --verificar-amend 7 2>&1 ); rc=$?
  juzgar 0 "$rc" 'post-condicion: lo commiteado coincide con lo medido' "$out"
else
  printf 'FALLO  %-58s no he podido montar el senuelo\n' 'post-condicion: coincide'; FALLA=$((FALLA+1))
fi

# EL DEFECTO QUE ESTO EXISTE PARA CAZAR: el commit se quedo con el trinquete viejo.
t="$RAIZ_TMP/pc-heredado"
if montar_trinquete "$t" 51; then
  out=$( cd "$t" && bash "$SUT" --verificar-amend 38 2>&1 ); rc=$?
  juzgar 1 "$rc" 'post-condicion: trinquete heredado (51) con censo 38' "$out" 'no lleva lo medido'
else
  printf 'FALLO  %-58s no he podido montar el senuelo\n' 'post-condicion: heredado'; FALLA=$((FALLA+1))
fi

t="$RAIZ_TMP/pc-sucio"
if montar_trinquete "$t" 7; then
  printf 'y\n' > "$t/core/internal/webui/dist/a.js"
  out=$( cd "$t" && bash "$SUT" --verificar-amend 7 2>&1 ); rc=$?
  juzgar 1 "$rc" 'post-condicion: regenerado FUERA del commit' "$out" 'FUERA del commit'
else
  printf 'FALLO  %-58s no he podido montar el senuelo\n' 'post-condicion: sucio'; FALLA=$((FALLA+1))
fi

# Y la TERCERA RESPUESTA: «no pude mirar» (2) no se confunde con «hallazgo» (1).
t="$RAIZ_TMP/pc-ilegible"
mkdir -p "$t" && ( cd "$t" || exit 1
  git init -q .; git config user.email t@olivares.ai; git config user.name t
  printf 'nada\n' > otro.txt; git add -- otro.txt; git commit -qm base ) >/dev/null 2>&1
out=$( cd "$t" && bash "$SUT" --verificar-amend 7 2>&1 ); rc=$?
juzgar 2 "$rc" 'post-condicion: sin trinquete legible en HEAD sale 2, no 1' "$out"

# ── Un argumento DESCONOCIDO se rechaza, y NO toca el repositorio ─────────────────────────────
#    Antes de esta casilla, cualquier flag que no fuera `--push` se ignoraba en silencio y el SUT
#    corria el rebase ENTERO con amend. Lo medi sobre mi mismo invocando `--verificar-amend` desde
#    el worktree de una rama que no lleva ese flag: quince minutos de trabajo donde yo esperaba una
#    lectura. La casilla exige las DOS mitades, porque un `exit 2` que ya haya amendado no vale de
#    nada: rc=2 **y** la cabeza intacta.
t=$(senuelo "core/api/desconocido.go")
if [ -n "$t" ]; then
  antes=$( cd "$t" && git rev-parse HEAD 2>/dev/null )
  out=$( cd "$t" && bash "$SUT" --no-existe-este-flag 2>&1 ); rc=$?
  despues=$( cd "$t" && git rev-parse HEAD 2>/dev/null )
  juzgar 2 "$rc" 'un flag desconocido dice NO HE PODIDO MIRAR' "$out" 'argumento desconocido'
  if [ -n "$antes" ] && [ "$antes" = "$despues" ]; then
    printf 'ok     %-58s\n' 'y no toca la cabeza de la rama'; PASA=$((PASA+1))
  else
    printf 'FALLO  %-58s antes=%s despues=%s\n' 'y no toca la cabeza de la rama' "${antes:0:9}" "${despues:0:9}"
    FALLA=$((FALLA+1))
  fi
else
  printf 'FALLO  %-58s no he podido montar el senuelo\n' 'flag desconocido'; FALLA=$((FALLA+1))
fi

# ── A QUIEN SE LE ATRIBUYE EL BUNDLE ────────────────────────────────────────────────────────
# El SUT reconstruye el bundle y lo mete en un commit. Hasta el 2026-08-26 lo metia SIEMPRE en la
# cabeza con `--amend`, y eso ATRIBUYE su trabajo a quien pasara por ultimo: medido en dos ramas
# reales, un commit de UNA linea titulado `docs(audit): ...` salio con 182 y 183 ficheros, el
# bundle dentro. No rompe nada -el contenido y el merge son identicos- y por eso sobrevivio: el
# dano es que `git blame` sobre dist/assets/*.js apunta a un commit de documentacion.
#
# Regla: se enmienda SOLO si TODOS los ficheros de la cabeza caen en GEN_DIRS. «Toca alguno» NO
# basta, y es el caso que se escapa: un commit MIXTO (codigo + bundle) tambien se llevaria la
# atribucion, y cuesta mas verlo porque ese commit si toca el bundle. El caso mixto de abajo es
# justo el contraejemplo de la version laxa.
clasifica() { # <repo> <sha> -> ENMIENDA | COMMIT-PROPIO
  # ⛔ LLAMA AL SUT, NO REIMPLEMENTA LA REGLA. La primera version de esta bateria copiaba aqui los
  #    dos `show --numstat` y el `-eq`, con el comentario «la logica EXACTA del SUT» — y eso es
  #    precisamente lo que envejece aparte: el dia que produccion cambie la regla, la copia seguiria
  #    verde probando la regla vieja. Es la misma razon por la que `verificar_amend` tiene su punto
  #    de entrada; este usa `--clasifica-cabeza`.
  ( cd "$1" && bash "$SUT" --clasifica-cabeza "$2" 2>/dev/null )
}

atrib="$RAIZ_TMP/atribucion"
mkdir -p "$atrib/core/internal/webui/dist/assets" "$atrib/docs" "$atrib/web/src"
( cd "$atrib" && g init -q -b main . && printf 'seed\n' > README.md && g add -A && g commit -qm seed ) >/dev/null 2>&1
if ! g -C "$atrib" rev-parse --verify -q HEAD >/dev/null 2>&1; then
  printf 'FALLO  %-58s %s\n' 'atribucion: montaje' 'no he podido crear el repo: los tres casos no prueban nada'
  FALLA=$((FALLA+1))
else
  ( cd "$atrib" && printf 'var a=1\n' > core/internal/webui/dist/assets/a.js \
      && printf 'stamp\n' > core/internal/webui/bundle-source.stamp && g add -A \
      && g commit -qm 'build(web): refresh versioned bundle' ) >/dev/null 2>&1
  solo_bundle=$(g -C "$atrib" rev-parse HEAD)
  ( cd "$atrib" && printf 'texto\n' > docs/nota.md && g add -A && g commit -qm 'docs(audit): una linea' ) >/dev/null 2>&1
  solo_docs=$(g -C "$atrib" rev-parse HEAD)
  ( cd "$atrib" && printf 'export const x=1\n' > web/src/x.ts \
      && printf 'var b=2\n' > core/internal/webui/dist/assets/b.js && g add -A \
      && g commit -qm 'feat(web): codigo Y bundle' ) >/dev/null 2>&1
  mixto=$(g -C "$atrib" rev-parse HEAD)

  for par in "solo_bundle:ENMIENDA:una cabeza INTEGRAMENTE del bundle se enmienda" \
             "solo_docs:COMMIT-PROPIO:una cabeza de docs NO se enmienda" \
             "mixto:COMMIT-PROPIO:una cabeza MIXTA tampoco se enmienda"; do
    var="${par%%:*}"; resto="${par#*:}"; esp="${resto%%:*}"; nom="${resto#*:}"
    eval "sha=\$$var"
    got=$(clasifica "$atrib" "$sha")
    if [ "$got" = "$esp" ]; then
      printf 'ok     %-58s %s\n' "$nom" "$got"; PASA=$((PASA+1))
    else
      printf 'FALLO  %-58s esperaba=%s obtuvo=%s\n' "$nom" "$esp" "$got"; FALLA=$((FALLA+1))
    fi
  done

  # CONTRAFACTUAL: la regla laxa («toca alguno») sobre el MIXTO da ENMIENDA. Si dejara de darlo,
  # este caso ya no estaria distinguiendo nada y habria que rehacerlo.
  laxa=$(g -C "$atrib" show --numstat --format= "$mixto" -- core/internal/webui/dist core/internal/webui/bundle-source.stamp 2>/dev/null | grep -c . || true)
  if [ "${laxa:-0}" -gt 0 ]; then
    printf 'ok     %-58s %s\n' 'el caso mixto DISTINGUE las dos reglas' 'la laxa lo enmendaria'
    PASA=$((PASA+1))
  else
    printf 'FALLO  %-58s %s\n' 'el caso mixto DISTINGUE las dos reglas' 'el mixto no toca el bundle: no separa nada'
    FALLA=$((FALLA+1))
  fi
fi



# ⛔ LAS TRES NEGATIVAS DE `--desde`, y son negativas A PROPOSITO. El flag existe para SALTAR un
#    commit que ya aterrizo en `main` con otro SHA, o sea para DESCARTAR trabajo de una rama. Un
#    flag asi tiene que rehusar mas de lo que acepta, y las tres formas de equivocarse son: no dar
#    <rev>, dar uno que no existe, y dar uno que no es ancestro de la rama (saltarlo no describiria
#    esa rama, describiria otra).
#
#    Se prueban las tres AQUI y no a mano porque una comprobacion que solo vive en el mensaje de un
#    commit no la vuelve a correr nadie.
GUION="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/rebase-web-branch.sh"

rc_de() { ( cd "$(git rev-parse --show-toplevel)" && bash "$GUION" "$@" >/dev/null 2>&1 ); echo $?; }

rc=$(rc_de --desde)
if [ "$rc" = "2" ]; then
    printf 'ok     %-58s %s\n' '--desde sin <rev> es NO HE PODIDO MIRAR' "rc=$rc"; PASA=$((PASA+1))
else
    printf 'FALLO  %-58s %s\n' '--desde sin <rev> es NO HE PODIDO MIRAR' "esperaba 2, dio $rc"; FALLA=$((FALLA+1))
fi

rc=$(rc_de --desde no-existe-este-rev)
if [ "$rc" = "2" ]; then
    printf 'ok     %-58s %s\n' '--desde <rev inexistente> es NO HE PODIDO MIRAR' "rc=$rc"; PASA=$((PASA+1))
else
    printf 'FALLO  %-58s %s\n' '--desde <rev inexistente> es NO HE PODIDO MIRAR' "esperaba 2, dio $rc"; FALLA=$((FALLA+1))
fi

# Y la que discrimina: un commit que EXISTE pero no es ancestro de HEAD. Sin esta celda las dos
# de arriba pasarian con un guion que rehusara SIEMPRE.
AJENO="$(git rev-list --max-count=1 origin/main 2>/dev/null || true)"
if [ -n "$AJENO" ] && ! git merge-base --is-ancestor "$AJENO" HEAD 2>/dev/null; then
    rc=$(rc_de --desde "$AJENO")
    if [ "$rc" = "1" ]; then
        printf 'ok     %-58s %s\n' '--desde <rev que no es ancestro> se NIEGA' "rc=$rc"; PASA=$((PASA+1))
    else
        printf 'FALLO  %-58s %s\n' '--desde <rev que no es ancestro> se NIEGA' "esperaba 1, dio $rc"; FALLA=$((FALLA+1))
    fi
else
    printf 'ok     %-58s %s\n' '--desde <no ancestro>: sin sujeto en este arbol' 'declarado, no fingido'
    PASA=$((PASA+1))
fi

printf 'test-rebase-web-branch: %s pasan, %s fallan\n' "$PASA" "$FALLA"
[ "$FALLA" -eq 0 ]
