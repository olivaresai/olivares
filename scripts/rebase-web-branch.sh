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

# ⛔ RERERE APAGADO PARA TODA LA CORRIDA, y no es precaucion: es una medida.
#
#    Este clon comparte `.git` entre los worktrees de los cinco carriles y tiene
#    `rerere.enabled=true` con 351 resoluciones en cache. Durante el rebase que hace este
#    guion, una preimagen que case se resuelve SOLA: el fichero queda sin marcadores, `git
#    status` sale limpio, y el bucle de mas abajo hace `git add` de un merge QUE NADIE HA
#    VISTO — con la mano de otro carril dentro.
#
#    Medido el 2026-08-25 en una corrida real sobre: los dos ficheros del gate salieron
#    marcados unmerged y SIN un solo marcador. Salio bien de casualidad, porque la resolucion
#    grabada era de esa misma sesion. La comprobacion de marcadores del final NO lo habria
#    cazado: precisamente lo que rerere deja no tiene marcadores.
#
#    Por entorno y no con `-c` en cada llamada, porque asi lo heredan TODOS los git hijos,
#    incluidos los que se anadan a este guion despues.
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
    [ -n "$DESDE" ] || { printf 'rebase-web-branch: NO HE PODIDO MIRAR: --desde exige un <rev>.\n' >&2; exit 2; }
    ;;
  --verificar-amend) ;;
  --clasifica-cabeza) ;;  # lo atiende su bloque, más abajo, ANTES de tocar nada
  *)
    printf 'rebase-web-branch: ⛔ NO HE PODIDO MIRAR: argumento desconocido: %s\n' "$1" >&2
    printf '   Uso: bash scripts/rebase-web-branch.sh [--push | --desde <rev> | --verificar-amend <censo> | --clasifica-cabeza <rev>]\n' >&2
    exit 2 ;;
esac

morir() { printf 'rebase-web-branch: ⛔ %s\n' "$1" >&2; exit 1; }
no_he_podido() { printf 'rebase-web-branch: ⛔ NO HE PODIDO MIRAR: %s\n' "$1" >&2; exit 2; }

command -v git >/dev/null 2>&1 || no_he_podido "no encuentro git"

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
  local censo="$1" commiteado
  commiteado=$(git show "HEAD:$RATCHET_FILE" 2>/dev/null \
    | grep -oE 'consoleUncoveredBudget = [0-9]+' | tail -1 | grep -oE '[0-9]+$')
  [ -n "$commiteado" ] || no_he_podido "no leo el trinquete en el commit de cabeza"
  [ "$commiteado" = "$censo" ] \
    || morir "el commit lleva trinquete $commiteado y el censo mide $censo: el amend no lleva lo medido"
  git diff --quiet -- $GEN_DIRS "$RATCHET_FILE" \
    || morir "quedan cambios regenerados FUERA del commit (dist / sello / trinquete)"
  printf 'rebase-web-branch: ✓ post-condicion: lo commiteado coincide con lo medido (%s)\n' "$censo"
}

# Entrada interna para el testigo (`scripts/test-rebase-web-branch.sh`): corre SOLO la
# post-condicion sobre el repo del directorio actual y sale con su codigo. Existe para que la
# bateria pruebe LA FUNCION QUE CORRE EN PRODUCCION en vez de una copia suya.
if [ "${1:-}" = "--clasifica-cabeza" ]; then
  [ -n "${2:-}" ] || no_he_podido "--clasifica-cabeza necesita el rev a clasificar"
  git rev-parse --verify -q "$2^{commit}" >/dev/null 2>&1 || no_he_podido "no resuelvo el rev $2"
  if cabeza_es_bundle "$2"; then printf 'ENMIENDA\n'; else printf 'COMMIT-PROPIO\n'; fi
  exit 0
fi

if [ "${1:-}" = "--verificar-amend" ]; then
  [ -n "${2:-}" ] || no_he_podido "--verificar-amend necesita el censo esperado"
  verificar_amend "$2"
  exit $?
fi

# ⛔ SE TRABAJA DESDE LA RAIZ DEL WORKTREE, Y NO ES ESTILO. Este guion invoca
#    `scripts/web-bundle-source-digest.sh` y `go test ./cmd/olivares/` por ruta RELATIVA. Corrido
#    desde un subdirectorio, el digest se calcula sin poder leer las fuentes y escribe un sello
#    BASURA sobre el fichero bueno — medido el 2026-08-20: mismo recuento de ficheros (1019) y
#    digest distinto, con `git status` mostrando el sello modificado y nada mas.
RAIZ_REPO=$(git rev-parse --show-toplevel 2>/dev/null) || no_he_podido "no encuentro la raiz del worktree"
cd "$RAIZ_REPO" || no_he_podido "no he podido entrar en $RAIZ_REPO"
printf 'rebase-web-branch: raiz %s\n' "$PWD"

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
  [ -r "$_hn" ] || no_he_podido "hay un rebase abierto y no puedo leer head-name: no se que rama es"
  RAMA=$(sed "s|^refs/heads/||" <"$_hn")
  [ -n "$RAMA" ] || no_he_podido "head-name esta vacio: no se que rama rebasar"
  REANUDANDO=1
  printf "rebase-web-branch: REANUDANDO el rebase abierto de %s\n" "$RAMA"
else
  RAMA=$(git branch --show-current)
fi
[ -n "$RAMA" ] || morir "HEAD está desprendido antes de empezar: no sé qué rama rebasar"
case "$RAMA" in main) morir "esto rebasa ramas de trabajo, no main" ;; esac

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
    sys.exit("sin choque con la forma esperada")
lado_main = m.group(1)          # en un rebase, HEAD es upstream
s = s[:m.start()] + lado_main + '\n' + s[m.end():]
if '<<<<<<<' in s or '>>>>>>>' in s or '\n=======\n' in s:
    sys.exit("queda más de un choque en el fichero: no es el caso mecánico")
open(p, 'w').write(s)
print(lado_main.strip())
PY
}

git fetch -q origin main || no_he_podido "no he podido traer origin/main"
printf 'rebase-web-branch: rama %s · %s commits sobre main\n' \
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
    git rev-parse -q --verify "${DESDE}^{commit}" >/dev/null 2>&1 \
      || no_he_podido "--desde $DESDE no resuelve a un commit en este repositorio"
    git merge-base --is-ancestor "$DESDE" HEAD 2>/dev/null \
      || morir "--desde $DESDE no es ancestro de HEAD: saltarlo no describe esta rama"
    printf 'rebase-web-branch: saltando todo hasta %s inclusive (--desde)\n' "$(git rev-parse --short "$DESDE")"
    _reb_out="$(git rebase --onto origin/main "$DESDE" 2>&1)" || true
  else
    _reb_out="$(git rebase origin/main 2>&1)" || true
  fi
  case "$_reb_out" in
  *"using previous resolution"*)
    printf 'rebase-web-branch: ⛔ rerere APLICO una resolucion guardada pese al apagado:\n' >&2
    while IFS= read -r _l; do
      case "$_l" in
      *"using previous resolution"*) printf 'rebase-web-branch:    %s\n' "$_l" >&2 ;;
      esac
    done <<EOF_REB
$_reb_out
EOF_REB
    printf 'rebase-web-branch:    En este clon el .git es COMPARTIDO: esa resolucion puede ser\n' >&2
    printf 'rebase-web-branch:    de otro carril y no deja marcadores. PARO.\n' >&2
    exit 2
    ;;
  esac
fi
RONDA=0
while hay_rebase; do
  RONDA=$((RONDA + 1))
  [ "$RONDA" -gt 10 ] && morir "más de 10 rondas de conflicto: esto no es mecánico"
  PENDIENTES=$(git diff --name-only --diff-filter=U)
  [ -n "$PENDIENTES" ] || morir "el rebase está parado y no hay conflictos: mira a mano"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if es_generado "$f"; then continue; fi
    [ "$f" = "$RATCHET_FILE" ] && continue
    printf 'rebase-web-branch: ⛔ choque NO reconstruible en %s — se resuelve a mano.\n' "$f" >&2
    printf 'rebase-web-branch:    NO toco nada mas. EL REBASE QUEDA ABIERTO: resuelvelo y\n' >&2
    printf 'rebase-web-branch:    `git rebase --continue`, o `git rebase --abort` para deshacerlo.\n' >&2
    printf 'rebase-web-branch:    ⚠ Con el rebase abierto HEAD esta DESPRENDIDO: un push no\n' >&2
    printf 'rebase-web-branch:    moveria tu rama y diria «Everything up-to-date».\n' >&2
    exit 1
  done <<< "$PENDIENTES"

  # ⛔ SIN TUBERÍA A `grep -q`, y no es estilo: bajo `set -o pipefail`, `<lista> | grep -q X`
  #    devuelve **141 CUANDO ACIERTA** — `grep -q` cierra la tubería en la primera coincidencia,
  #    el productor recibe SIGPIPE y `pipefail` lo propaga. El caso de ÉXITO es el que falla, que
  #    es la peor forma de este defecto porque sólo se ve cuando el guion iba a funcionar.
  case $'\n'"$PENDIENTES"$'\n' in *$'\n'"$RATCHET_FILE"$'\n'*)
    VALOR=$(resolver_trinquete) || morir "el choque del trinquete no tiene la forma esperada: $VALOR"
    printf 'rebase-web-branch:   trinquete resuelto al valor de main (%s) — se RE-MIDE\n' "$VALOR"
    git add -- "$RATCHET_FILE" || morir "no he podido indexar el trinquete resuelto"
  ;; esac
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    [ "$f" = "$RATCHET_FILE" ] && continue
    if [ -e "$f" ]; then git add -- "$f" || morir "no he podido indexar $f"
    else git rm -q -- "$f" || morir "no he podido retirar $f"; fi
  done <<< "$PENDIENTES"
  git -c core.editor=true rebase --continue >/dev/null 2>&1
done

# «sin conflictos» ≠ «terminado»: si HEAD quedó desprendido, un push empujaría el ref viejo.
[ -n "$(git branch --show-current)" ] || morir "el rebase terminó con HEAD DESPRENDIDO: la rama no se movería con un push"

MARCADORES=$(git grep -lE '^<<<<<<< |^>>>>>>> ' -- '*.go' '*.ts' '*.tsx' '*.md' 2>/dev/null | wc -l)
[ "$MARCADORES" = "0" ] || morir "quedan $MARCADORES fichero(s) con marcadores de choque en el árbol"

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
	printf 'rebase-web-branch: \u26d4 MUERO DEJANDO %s fichero(s) del bundle sin commitear.\n' "$sucio" >&2
	printf 'rebase-web-branch:    Eso NO se va solo, y el próximo push morirá al final del gate.\n' >&2
	printf 'rebase-web-branch:    Retíralo con:\n' >&2
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

printf 'rebase-web-branch: ✓ rebase cerrado · reconstruyo el bundle\n'
BUNDLE_TOCADO=1
task build:web >/dev/null 2>&1 || morir "task build:web ha fallado"
git add core/internal/webui/dist || morir "no he podido indexar el bundle reconstruido"
bash scripts/web-bundle-source-digest.sh > core/internal/webui/bundle-source.stamp \
  || no_he_podido "no he podido regenerar el sello del bundle"
git add core/internal/webui/bundle-source.stamp || morir "no he podido indexar el sello"

CENSO=$(go test ./cmd/olivares/ -run 'TestEveryEngineRouteHasAConsoleSurface' -count=1 -v 2>&1 \
  | grep -oE '[0-9]+ de [0-9]+ ruta' | head -1 | grep -oE '^[0-9]+')
[ -n "$CENSO" ] || no_he_podido "no he podido medir el censo de rutas sin superficie"
ACTUAL=$(grep -oE 'consoleUncoveredBudget = [0-9]+' "$RATCHET_FILE" | tail -1 | grep -oE '[0-9]+$')
[ -n "$ACTUAL" ] || no_he_podido "no encuentro consoleUncoveredBudget en $RATCHET_FILE"
if [ "$CENSO" != "$ACTUAL" ]; then
  python3 -c "
import sys
f, a, n = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(f).read()
old = 'consoleUncoveredBudget = ' + a
assert s.count(old) == 1, 'el trinquete no aparece exactamente una vez'
open(f, 'w').write(s.replace(old, 'consoleUncoveredBudget = ' + n, 1))
" "$RATCHET_FILE" "$ACTUAL" "$CENSO" || morir "no he podido fijar el trinquete"
  printf 'rebase-web-branch: trinquete %s → %s (MEDIDO, no heredado)\n' "$ACTUAL" "$CENSO"
else
  printf 'rebase-web-branch: trinquete ya en %s (medido)\n' "$CENSO"
fi
git add -- "$RATCHET_FILE" || morir "no he podido indexar $RATCHET_FILE"
if git diff --cached --quiet 2>/dev/null; then
	# Nada que regenerar: el bundle ya correspondía a las fuentes. Un commit vacío sería ruido
	# y `git commit` fallaría, así que no se commitea y se DICE.
	printf 'rebase-web-branch: el bundle ya estaba al día — nada que commitear
'
elif [ "${CABEZA_ES_BUNDLE:-0}" -gt 0 ]; then
	# La cabeza YA es un commit del bundle (p.ej. «build(web): refresh versioned bundle»):
	# enmendarla es exactamente lo que corresponde y no cambia lo que su mensaje afirma.
	git commit -sq --amend --no-edit >/dev/null 2>&1 || morir "no he podido enmendar el commit de cabeza"
	printf 'rebase-web-branch: bundle enmendado en la cabeza (ya era un commit del bundle)\n'
else
	# La cabeza NO es del bundle. Enmendarla convertiría, por ejemplo, un commit de UNA línea de
	# documentación en uno de 182 ficheros cuyo mensaje sigue hablando de documentación — medido
	# el 2026-08-26 en dos ramas. El bundle va en su propio commit, que dice lo que es.
	git commit -sq -m 'build(web): refresh the console bundle after rebasing onto main' \
		-m 'Generated by scripts/rebase-web-branch.sh: task build:web plus the source stamp and the
console-route ratchet. Separate from the head commit on purpose -- amending it would attach a
rebuilt bundle to a message that describes something else, and git blame over dist/assets/*.js
would then point at a commit that never meant to touch it.' \
		>/dev/null 2>&1 || morir "no he podido commitear el bundle reconstruido"
	printf 'rebase-web-branch: bundle en su PROPIO commit (la cabeza no era del bundle)\n'
fi

# POST-CONDICION, y no es cinturon-y-tirantes: los `git add` de arriba escriben EL MISMO indice que
# el amend, asi que un `index.lock` transitorio puede tumbar un `add` y soltarse antes del commit.
# Ese camino deja un commit SIN lo regenerado y el guion diciendo «✓ publicado». MEDIDO el
# 2026-08-24 rebasando: un candado ajeno dejo el trinquete SIN indexar mientras `dist` y el
# sello SI entraron. Guardar cada `add` estrecha la ventana; comprobar EL RESULTADO la cierra,
# porque juzga lo commiteado en vez de confiar en que cada paso hizo lo suyo.
verificar_amend "$CENSO"

if [ "$PUSH" = "1" ]; then
  git push --no-verify --force-with-lease origin "$RAMA" >/dev/null 2>&1
  LOCAL=$(git rev-parse HEAD)
  REMOTO=$(git ls-remote origin "refs/heads/$RAMA" | cut -f1)
  [ "$LOCAL" = "$REMOTO" ] || morir "publicado ≠ local: local ${LOCAL:0:9}, remoto ${REMOTO:0:9}"
  printf 'rebase-web-branch: ✓ publicado %s (verificado con ls-remote, no con el rc del push)\n' "${LOCAL:0:9}"
else
  printf 'rebase-web-branch: ✓ listo en local %s — publica con --push\n' "$(git rev-parse --short HEAD)"
fi
