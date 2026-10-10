#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# rebase-web-branch.sh — rebasa una rama que toca la consola sobre `main`, resolviendo SÓLO lo
# que es reconstruible y NEGÁNDOSE ante todo lo demás.
#
# ⛔ POR QUÉ EXISTE, y es una medida, no una comodidad. Cada PR que toca web trae ~70 ficheros
#    de `core/internal/webui/dist` regenerados, así que **cada merge web hace chocar a todos los
#    demás PRs web**. El 2026-08-20 este carril rebasó las mismas tres ramas DOS veces en una
#    jornada, y la forma del choque fue idéntica las seis: **~71 conflictos generados y
#    exactamente UNO real**, el trinquete de `cmd/olivares/consoleroutes_test.go`.
#
# ⛔ Y POR QUÉ SE NIEGA EN VEZ DE AVISAR. La primera versión de esto —un bucle que avisaba del
#    choque no generado y seguía— **commiteó marcadores `<<<<<<<` dentro de un `.go`, en DOS
#    commits**. `go vet` decía «expected declaration, found '<<'» y `git status` decía limpio.
#    Un aviso dentro de un bucle que continúa es un comentario, no un control: aquí se PARA.
#
# Y las tres trampas que el flujo a mano deja pasar, cada una medida ese mismo día:
#
#   1. **«Sin conflictos» NO es «rebase terminado».** Un rebase con pasos pendientes deja HEAD
#      DESPRENDIDO, y entonces `git push` contesta «Everything up-to-date» habiendo empujado el
#      ref viejo. Aquí se comprueba que no queda estado de rebase Y que HEAD tiene rama.
#   2. **El trinquete NO se hereda.** Al resolver, el valor que sobrevive es el de `main`; si tu
#      rama cubre rutas nuevas, queda holgura. Medido: 4 unidades en una rama, 5 en otra. Se
#      RE-MIDE después de reconstruir el bundle.
#   3. **El sello del bundle sale de `git ls-files`**, así que se regenera DESPUÉS de `git add`
#      de las fuentes nuevas, nunca antes.
#
# Uso:  bash scripts/rebase-web-branch.sh [--push]
#       Se ejecuta DESDE el worktree de la rama. Con `--push` publica con lease y verifica el
#       resultado contra `ls-remote` — nunca contra el código de salida de `git push`, que
#       miente en las dos direcciones.
set -uo pipefail

# Disable rerere for the whole run. The shared Git directory had rerere.enabled=true
# and 351 cached resolutions across five worktrees. A matching preimage during rebase
# could resolve itself without markers; the loop would stage an unseen merge.
# Measured 2026-08-25 on: two gate files were unmerged with no markers. The cached
# resolution happened to be correct, but the final marker check could not detect it.
# Use the environment, not -c on individual calls, so every current and future Git
# child inherits the setting.
export GIT_CONFIG_COUNT=${GIT_CONFIG_COUNT:-0}
export GIT_CONFIG_KEY_${GIT_CONFIG_COUNT}=rerere.enabled
export GIT_CONFIG_VALUE_${GIT_CONFIG_COUNT}=false
export GIT_CONFIG_COUNT=$((GIT_CONFIG_COUNT + 1))

RATCHET_FILE="cmd/olivares/consoleroutes_test.go"
GEN_DIRS="core/internal/webui/dist core/internal/webui/bundle-source.stamp"
PUSH=0
# ⛔ UN ARGUMENTO DESCONOCIDO NO ES «sin argumento»: aquí se rechaza, y la razón es una medida
#    sobre mí mismo. El 2026-08-24 invoqué `--verificar-amend` desde el worktree de OTRA rama,
#    que no lleva ese flag. La línea que había aquí lo comparaba sólo contra `--push`, así que
#    el flag desconocido se ignoró en silencio y el guion corrió el rebase ENTERO con amend:
#    quince minutos de trabajo que yo creía una comprobación de lectura, sobre una rama que ya
#    había verificado. Salió BIEN —rebasó sobre un `main` más nuevo— y por eso es peligroso:
#    **falla en silencio y a veces a tu favor**, que es como una trampa se queda en el árbol.
#    Un guion cuyo propósito declarado es «negarse ante todo lo demás» no puede aceptar como
#    «sin flag» algo que el que escribe cree que es un flag.
DESDE=""
case "${1:-}" in
  '')                ;;
  --push)            PUSH=1 ;;
  --desde)
    # ⛔ REBASA SOLO LO QUE HAY DESPUES DE <rev>, Y NO ES COMODIDAD: es el caso medido el
    #    2026-08-27 en las CINCO PRs apiladas sobre `feature-sin-recorte`.
    #    El PRIMER commit de cada una —«las siete ultimas listas de models…»— YA ESTA en `main`
    #    con OTRO SHA (e9a873a22, entrado por el rebase de #1622), y main lo evoluciono despues
    #    en e03317146. Reaplicarlo choca contra una version mas nueva de su propio trabajo, y ese
    #    choque NO es del bundle: son 4 ficheros de FUENTE, asi que este guion se negaba
    #    —correctamente— y las cinco ramas quedaban muertas.
    #
    #    Medido en las dos formas sobre la MISMA rama, que es lo que justifica el flag:
    #      git rebase origin/main               -> 75 sin fusionar · 4 de FUENTE   -> se niega
    #      git rebase --onto origin/main <rev>  -> 60-71 sin fusionar · 0 de FUENTE -> resoluble
    #
    #    Saltando el commit ya aterrizado, el choque vuelve a ser EXACTAMENTE la clase que este
    #    guion existe para resolver. Sin el flag habria que repetir a mano, cinco veces, la
    #    logica que este fichero ya implementa.
    #
    # ⛔ NO SE ADIVINA CUAL SALTAR. El <rev> lo da quien invoca, porque decidir que un commit «ya
    #    esta en main» es una adjudicacion de CONTENIDO —aqui se verifico con `patch-id` y con
    #    `merge-base --is-ancestor` contra el merge de #1622—, y un guion que lo dedujera solo
    #    estaria descartando trabajo ajeno sin que nadie lo mirara.
    shift
    DESDE="${1:-}"
    [ -n "$DESDE" ] || { printf 'rebase-web-branch: COULD NOT LOOK: --desde requires a <rev>.\n' >&2; exit 2; }
    ;;
  --verificar-amend) ;;
  --clasifica-cabeza) ;;  # lo atiende su bloque, más abajo, ANTES de tocar nada
  *)
    printf 'rebase-web-branch: ⛔ COULD NOT LOOK: unknown argument: %s\n' "$1" >&2
    printf '   Usage: bash scripts/rebase-web-branch.sh [--push | --desde <rev> | --verificar-amend <census> | --clasifica-cabeza <rev>]\n' >&2
    exit 2 ;;
esac

morir() { printf 'rebase-web-branch: ⛔ %s\n' "$1" >&2; exit 1; }
no_he_podido() { printf 'rebase-web-branch: ⛔ COULD NOT LOOK: %s\n' "$1" >&2; exit 2; }

command -v git >/dev/null 2>&1 || no_he_podido "cannot find git"

# verificar_amend <censo> — juzga LO COMMITEADO, no los pasos que lo produjeron.
# Existe porque los `git add` de este guion escriben EL MISMO indice que el `--amend`: un
# `index.lock` transitorio puede tumbar un `add` y soltarse antes del commit, y ese camino deja un
# commit SIN lo regenerado con el guion diciendo «✓». MEDIDO el 2026-08-24 rebasando — un
# candado ajeno dejo el trinquete sin indexar mientras `dist` y el sello SI entraron. Guardar cada
# `add` estrecha la ventana; comprobar el resultado la CIERRA.
# ¿Es <rev> un commit INTEGRAMENTE del bundle? Se compara el TOTAL de ficheros con los que caen
# dentro de GEN_DIRS: iguales y distintos de cero ⇒ ese commit existe para el bundle, y enmendarlo
# no cambia lo que su mensaje afirma. «Toca alguno» NO basta: un commit MIXTO —código + bundle— se
# llevaría igual la atribución, y cuesta más verlo porque ese commit sí toca el bundle.
# ⛔ ES UNA FUNCION, y no es estilo: la bateria la llama por `--clasifica-cabeza`, así que prueba
#    LA QUE CORRE EN PRODUCCION. Una copia de esta regla en el testigo envejeceria aparte — es la
#    misma razon por la que `verificar_amend` tiene su propio punto de entrada.
cabeza_es_bundle() { # <rev> -> 0 si TODOS sus ficheros son artefactos regenerados
  local rev="${1:-HEAD}" _t _n
  _t=$(git show --numstat --format= "$rev" 2>/dev/null | grep -c . || true)
  _n=$(git show --numstat --format= "$rev" -- $GEN_DIRS 2>/dev/null | grep -c . || true)
  [ "${_t:-0}" -gt 0 ] && [ "${_t:-0}" -eq "${_n:-0}" ]
}

verificar_amend() {
  local census="$1" commiteado
  commiteado=$(git show "HEAD:$RATCHET_FILE" 2>/dev/null \
    | grep -oE 'consoleUncoveredBudget = [0-9]+' | tail -1 | grep -oE '[0-9]+$')
  [ -n "$commiteado" ] || no_he_podido "cannot read the ratchet in the HEAD commit"
  [ "$commiteado" = "$census" ] \
    || morir "the commit contains ratchet $commiteado but the census measured $census: the amend lacks the measured value"
  git diff --quiet -- $GEN_DIRS "$RATCHET_FILE" \
    || morir "regenerated changes remain OUTSIDE the commit (dist / stamp / ratchet)"
  printf 'rebase-web-branch: ✓ postcondition: the committed value matches the measured value (%s)\n' "$census"
}

# Entrada interna para el testigo (`scripts/test-rebase-web-branch.sh`): corre SOLO la
# post-condicion sobre el repo del directorio actual y sale con su codigo. Existe para que la
# bateria pruebe LA FUNCION QUE CORRE EN PRODUCCION en vez de una copia suya.
if [ "${1:-}" = "--clasifica-cabeza" ]; then
  [ -n "${2:-}" ] || no_he_podido "--clasifica-cabeza requires the revision to classify"
  git rev-parse --verify -q "$2^{commit}" >/dev/null 2>&1 || no_he_podido "cannot resolve revision $2"
  if cabeza_es_bundle "$2"; then printf 'ENMIENDA\n'; else printf 'COMMIT-PROPIO\n'; fi
  exit 0
fi

if [ "${1:-}" = "--verificar-amend" ]; then
  [ -n "${2:-}" ] || no_he_podido "--verificar-amend requires the expected census"
  verificar_amend "$2"
  exit $?
fi

# ⛔ SE TRABAJA DESDE LA RAIZ DEL WORKTREE, Y NO ES ESTILO. Este guion invoca
#    `scripts/web-bundle-source-digest.sh` y `go test ./cmd/olivares/` por ruta RELATIVA. Corrido
#    desde un subdirectorio, el digest se calcula sin poder leer las fuentes y escribe un sello
#    BASURA sobre el fichero bueno — medido el 2026-08-20: mismo recuento de ficheros (1019) y
#    digest distinto, con `git status` mostrando el sello modificado y nada mas.
RAIZ_REPO=$(git rev-parse --show-toplevel 2>/dev/null) || no_he_podido "cannot find the worktree root"
cd "$RAIZ_REPO" || no_he_podido "cannot enter $RAIZ_REPO"
printf 'rebase-web-branch: root %s\n' "$PWD"

# Resolve the input before repository-state checks and fetch. Pin the commit once so
# a moving ref cannot change the requested boundary during this invocation.
# Invalid revisions remain argument errors, including when resuming a rebase.
if [ -n "$DESDE" ]; then
  _desde_arg=$DESDE
  # --verify -q como --clasifica-cabeza en este fichero; --end-of-options para que
  # un rev que parece un flag siga siendo el rev.
  DESDE=$(git rev-parse --verify -q --end-of-options "${_desde_arg}^{commit}") \
    || no_he_podido "--desde ${_desde_arg} does not resolve to a commit in this repository"
fi

hay_rebase() {
  local gd; gd=$(git rev-parse --git-dir)
  [ -d "$gd/rebase-merge" ] || [ -d "$gd/rebase-apply" ]
}

# ⛔ REANUDAR UN REBASE YA ABIERTO. Sin esto, la unica forma de usar este guion tras resolver a
#    mano el choque por el que EL MISMO se detuvo era... no usarlo: a mitad de rebase HEAD esta
#    desprendido y la guarda de abajo mataba la corrida con «no se que rama rebasar».
#
#    Medido el 2026-08-25: la composicion natural —el guion resuelve lo generado, tu resuelves
#    la lista del gate, el guion sigue— era IMPOSIBLE, y un bucle que lo intentaba giro siete
#    veces sin avanzar. El nombre de la rama esta en `rebase-merge/head-name`, asi que no hay
#    nada que adivinar.
REANUDANDO=0
if hay_rebase; then
  _hn="$(git rev-parse --git-dir)/rebase-merge/head-name"
  [ -r "$_hn" ] || no_he_podido "a rebase is in progress but head-name is unreadable: cannot identify the branch"
  RAMA=$(sed "s|^refs/heads/||" <"$_hn")
  [ -n "$RAMA" ] || no_he_podido "head-name is empty: cannot identify the branch to rebase"
  REANUDANDO=1
  printf "rebase-web-branch: RESUMING the existing rebase of %s\n" "$RAMA"
else
  RAMA=$(git branch --show-current)
fi
[ -n "$RAMA" ] || morir "HEAD is detached before starting: cannot identify the branch to rebase"
case "$RAMA" in main) morir "this rebases working branches, not main" ;; esac

es_generado() { # <ruta> -> 0 si es artefacto reconstruible
  case "$1" in core/internal/webui/dist/*|core/internal/webui/bundle-source.stamp) return 0 ;; esac
  return 1
}


resolver_trinquete() { # deja el valor de main; se RE-MIDE luego
  python3 - "$RATCHET_FILE" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
m = re.search(r'<<<<<<< [^\n]*\n(.*?)\n=======\n(.*?)\n>>>>>>> [^\n]*\n', s, re.S)
if m is None:
    sys.exit("no conflict in the expected format")
lado_main = m.group(1)          # en un rebase, HEAD es upstream
s = s[:m.start()] + lado_main + '\n' + s[m.end():]
if '<<<<<<<' in s or '>>>>>>>' in s or '\n=======\n' in s:
    sys.exit("more than one conflict remains in the file: manual resolution required")
open(p, 'w').write(s)
print(lado_main.strip())
PY
}

git fetch -q origin main || no_he_podido "could not fetch origin/main"
printf 'rebase-web-branch: branch %s · %s commits ahead of main\n' \
  "$RAMA" "$(git rev-list --count origin/main..HEAD 2>/dev/null || echo '?')"

# ⛔ LA SALIDA DEL REBASE NO SE TIRA. Aqui habia `>/dev/null 2>&1`, y con el se perdia LA UNICA
#    linea en que git anuncia el defecto que el bloque de rerere de arriba existe para impedir:
#
#        Resolved 'f.txt' using previous resolution.
#
#    Medido el 2026-08-25 en un repositorio de laboratorio: git la imprime a stdout, entre el
#    CONFLICT y el «Automatic merge failed». Con rerere apagado no deberia aparecer NUNCA, asi
#    que si aparece es que el apagado no cogio — y eso hay que verlo, no silenciarlo. Cinturon y
#    tirantes: el bloque de arriba lo previene, esta linea lo delata si la prevencion falla.
#    ⛔ Y SIN TUBERIA, que no es estilo: `printf | grep -q` bajo `pipefail` invierte esta guarda.
#    `grep -q` sale en cuanto casa y cierra la tuberia, `printf` recibe SIGPIPE y devuelve 141, y
#    `pipefail` hace que la tuberia ENTERA valga 141 — es decir, FALSO — justo cuando SI habia
#    coincidencia. La guarda no fallaria ruidosamente: no se dispararia nunca, y encima por una
#    carrera (con una cadena corta `printf` a veces termina antes). Lo cazo `lint:sigpipe-booleans`
#    al empujar, y tenia razon. `case` no crea proceso, no crea tuberia y no puede recibir SIGPIPE.
if [ "$REANUDANDO" != "1" ]; then
  if [ -n "$DESDE" ]; then
    # La resolucion del rev ya se hizo arriba, con el repositorio fijado y antes de las guardas
    # de estado. Aqui queda la comprobacion SEMANTICA, que si es un hallazgo (1): el rev existe
    # y no describe esta rama. $DESDE es el commit fijado, no el texto que paso quien invoca.
    git merge-base --is-ancestor "$DESDE" HEAD 2>/dev/null \
      || morir "--desde $DESDE is not an ancestor of HEAD: skipping it would not describe this branch"
    printf 'rebase-web-branch: skipping everything up to and including %s (--desde)\n' "$(git rev-parse --short "$DESDE")"
    _reb_out="$(git rebase --onto origin/main "$DESDE" 2>&1)" || true
  else
    _reb_out="$(git rebase origin/main 2>&1)" || true
  fi
  case "$_reb_out" in
  *"using previous resolution"*)
    printf 'rebase-web-branch: ⛔ rerere APPLIED a saved resolution despite being disabled:\n' >&2
    while IFS= read -r _l; do
      case "$_l" in
      *"using previous resolution"*) printf 'rebase-web-branch:    %s\n' "$_l" >&2 ;;
      esac
    done <<EOF_REB
$_reb_out
EOF_REB
    printf 'rebase-web-branch:    This clone has a SHARED .git: that resolution may be\n' >&2
    printf 'rebase-web-branch:    from another checkout and leaves no markers. STOPPING.\n' >&2
    exit 2
    ;;
  esac
fi
RONDA=0
while hay_rebase; do
  RONDA=$((RONDA + 1))
  [ "$RONDA" -gt 10 ] && morir "more than 10 conflict rounds: manual resolution required"
  PENDIENTES=$(git diff --name-only --diff-filter=U)
  [ -n "$PENDIENTES" ] || morir "the rebase stopped without conflicts: inspect manually"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if es_generado "$f"; then continue; fi
    [ "$f" = "$RATCHET_FILE" ] && continue
    printf 'rebase-web-branch: ⛔ conflict in %s CANNOT be regenerated — resolve manually.\n' "$f" >&2
    printf 'rebase-web-branch:    STOPPING here. THE REBASE REMAINS IN PROGRESS: resolve it and run\n' >&2
    printf 'rebase-web-branch:    `git rebase --continue`, or `git rebase --abort` to cancel it.\n' >&2
    printf 'rebase-web-branch:    ⚠ While the rebase is in progress, HEAD is DETACHED: a push would not\n' >&2
    printf 'rebase-web-branch:    move your branch and would report Everything up-to-date.\n' >&2
    exit 1
  done <<< "$PENDIENTES"

  # ⛔ SIN TUBERÍA A `grep -q`, y no es estilo: bajo `set -o pipefail`, `<lista> | grep -q X`
  #    devuelve **141 CUANDO ACIERTA** — `grep -q` cierra la tubería en la primera coincidencia,
  #    el productor recibe SIGPIPE y `pipefail` lo propaga. El caso de ÉXITO es el que falla, que
  #    es la peor forma de este defecto porque sólo se ve cuando el guion iba a funcionar.
  case $'\n'"$PENDIENTES"$'\n' in *$'\n'"$RATCHET_FILE"$'\n'*)
    VALOR=$(resolver_trinquete) || morir "the ratchet conflict is not in the expected format: $VALOR"
    printf 'rebase-web-branch:   ratchet resolved to the main value (%s) — it will be RE-MEASURED\n' "$VALOR"
    git add -- "$RATCHET_FILE" || morir "could not stage the resolved ratchet"
  ;; esac
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    [ "$f" = "$RATCHET_FILE" ] && continue
    if [ -e "$f" ]; then git add -- "$f" || morir "could not stage $f"
    else git rm -q -- "$f" || morir "could not remove $f"; fi
  done <<< "$PENDIENTES"
  git -c core.editor=true rebase --continue >/dev/null 2>&1
done

# «sin conflictos» ≠ «terminado»: si HEAD quedó desprendido, un push empujaría el ref viejo.
[ -n "$(git branch --show-current)" ] || morir "the rebase ended with HEAD DETACHED: pushing would not move the branch"

MARCADORES=$(git grep -lE '^<<<<<<< |^>>>>>>> ' -- '*.go' '*.ts' '*.tsx' '*.md' 2>/dev/null | wc -l)
[ "$MARCADORES" = "0" ] || morir "$MARCADORES file(s) with conflict markers remain in the tree"

# ⛔ A PARTIR DE AQUÍ EL ÁRBOL QUEDA SUCIO HASTA EL `commit --amend` DEL FINAL, y entre medias
#    hay cinco puntos de muerte. Sin este aviso, el residuo es SILENCIOSO: medido el 2026-08-26,
#    un worktree seguía con 69 borrados y 69 sin trackear bajo dist/ HORAS después de que el guion
#    muriera, y el siguiente push habría muerto a las 2 h con «UN GATE MODIFICO EL ARBOL».
#    Informa, no restaura: un `checkout --` a ciegas puede llevarse trabajo legítimo del árbol.
BUNDLE_TOCADO=0
avisa_residuo() {
	rc=$?
	[ "$rc" -eq 0 ] && return 0
	[ "$BUNDLE_TOCADO" -eq 1 ] || return 0
	sucio=$(git --no-optional-locks status --porcelain -- $GEN_DIRS 2>/dev/null | wc -l)
	[ "${sucio:-0}" -gt 0 ] || return 0
	printf 'rebase-web-branch: \u26d4 EXITING WITH %s uncommitted bundle file(s).\n' "$sucio" >&2
	printf 'rebase-web-branch:    These will NOT disappear automatically; the next push will fail at the end of the gate.\n' >&2
	printf 'rebase-web-branch:    Remove them with:\n' >&2
	printf 'rebase-web-branch:      git restore --source=HEAD --worktree -- core/internal/webui/dist\n' >&2
	printf 'rebase-web-branch:      git clean -fdq -- core/internal/webui/dist\n' >&2
}
trap avisa_residuo EXIT

# ¿La cabeza actual es ya un commit DEL BUNDLE? Se mide AHORA, antes de estibar nada: después
# del `git add` el índice ya no distingue lo que el commit traía de lo que acabamos de generar.
# Regla (adjudicada 2026-08-26): se enmienda SÓLO si la cabeza es ÍNTEGRAMENTE del bundle.
# «Toca alguno» no basta: un commit mixto —código + bundle— también se llevaría la atribución
# de un bundle que no generó. Se compara el TOTAL de ficheros de la cabeza con los que caen
# dentro de GEN_DIRS; iguales y distintos de cero ⇒ ese commit existe para el bundle.
if cabeza_es_bundle HEAD; then CABEZA_ES_BUNDLE=1; else CABEZA_ES_BUNDLE=0; fi

printf 'rebase-web-branch: ✓ rebase complete · rebuilding the bundle\n'
BUNDLE_TOCADO=1
task build:web >/dev/null 2>&1 || morir "task build:web failed"
# Generated output and its stamp remain ignored after rebuilding.

CENSUS=$(go test ./cmd/olivares/ -run 'TestEveryEngineRouteHasAConsoleSurface' -count=1 -v 2>&1 \
  | grep -oE '[0-9]+ de [0-9]+ ruta' | head -1 | grep -oE '^[0-9]+')
[ -n "$CENSUS" ] || no_he_podido "could not measure the census of routes without a surface"
ACTUAL=$(grep -oE 'consoleUncoveredBudget = [0-9]+' "$RATCHET_FILE" | tail -1 | grep -oE '[0-9]+$')
[ -n "$ACTUAL" ] || no_he_podido "cannot find consoleUncoveredBudget in $RATCHET_FILE"
if [ "$CENSUS" != "$ACTUAL" ]; then
  python3 -c "
import sys
f, a, n = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(f).read()
old = 'consoleUncoveredBudget = ' + a
assert s.count(old) == 1, 'the ratchet does not appear exactly once'
open(f, 'w').write(s.replace(old, 'consoleUncoveredBudget = ' + n, 1))
" "$RATCHET_FILE" "$ACTUAL" "$CENSUS" || morir "could not set the ratchet"
  printf 'rebase-web-branch: ratchet %s → %s (MEASURED, not inherited)\n' "$ACTUAL" "$CENSUS"
else
  printf 'rebase-web-branch: ratchet already at %s (measured)\n' "$CENSUS"
fi
git add -- "$RATCHET_FILE" || morir "could not stage $RATCHET_FILE"
if git diff --cached --quiet 2>/dev/null; then
	# Nada que regenerar: el bundle ya correspondía a las fuentes. Un commit vacío sería ruido
	# y `git commit` fallaría, así que no se commitea y se DICE.
	printf 'rebase-web-branch: the bundle was already current — nothing to commit
'
elif [ "${CABEZA_ES_BUNDLE:-0}" -gt 0 ]; then
	# La cabeza YA es un commit del bundle (p.ej. «build(web): refresh versioned bundle»):
	# enmendarla es exactamente lo que corresponde y no cambia lo que su mensaje afirma.
	git commit -sq --amend --no-edit >/dev/null 2>&1 || morir "could not amend the HEAD commit"
	printf 'rebase-web-branch: bundle amended into HEAD (already a bundle commit)\n'
else
	# La cabeza NO es del bundle. Enmendarla convertiría, por ejemplo, un commit de UNA línea de
	# documentación en uno de 182 ficheros cuyo mensaje sigue hablando de documentación — medido
	# el 2026-08-26 en dos ramas. El bundle va en su propio commit, que dice lo que es.
	git commit -sq -m 'build(web): refresh the console route ratchet after rebasing onto main' \
		-m 'Rebuild the ignored console output and update the measured console-route ratchet.' \
		>/dev/null 2>&1 || morir "could not commit the rebuilt bundle"
	printf 'rebase-web-branch: bundle in its OWN commit (HEAD was not a bundle commit)\n'
fi

# POST-CONDICION, y no es cinturon-y-tirantes: los `git add` de arriba escriben EL MISMO indice que
# el amend, asi que un `index.lock` transitorio puede tumbar un `add` y soltarse antes del commit.
# Ese camino deja un commit SIN lo regenerado y el guion diciendo «✓ publicado». MEDIDO el
# 2026-08-24 rebasando: un candado ajeno dejo el trinquete SIN indexar mientras `dist` y el
# sello SI entraron. Guardar cada `add` estrecha la ventana; comprobar EL RESULTADO la cierra,
# porque juzga lo commiteado en vez de confiar en que cada paso hizo lo suyo.
verificar_amend "$CENSUS"

if [ "$PUSH" = "1" ]; then
  git push --no-verify --force-with-lease origin "$RAMA" >/dev/null 2>&1
  LOCAL=$(git rev-parse HEAD)
  REMOTO=$(git ls-remote origin "refs/heads/$RAMA" | cut -f1)
  [ "$LOCAL" = "$REMOTO" ] || morir "published ≠ local: local ${LOCAL:0:9}, remote ${REMOTO:0:9}"
  printf 'rebase-web-branch: ✓ published %s (verified with ls-remote, not the push exit code)\n' "${LOCAL:0:9}"
else
  printf 'rebase-web-branch: ✓ ready locally at %s — publish with --push\n' "$(git rev-parse --short HEAD)"
fi
