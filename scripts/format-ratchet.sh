#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# El TRINQUETE de formato del front: corre desde el día uno, publica el censo en cada corrida y
# sólo falla si la deuda SUBE. Adjudicado por el carril de integración el 2026-08-15:
#
#   «cablea PRIMERO con línea base declarada … el gate corre desde el día uno, imprime cuántos
#    ficheros incumplen y sólo falla si el número sube … un cap silencioso es lo que convierte un
#    verde en nada.»
#
# ⛔ POR QUÉ EXISTE. `prettier --check` vive en `format:check`, que cuelga de `lint:web` y de
# `fmt:check`. El `pre-push` salta `lint:web` a propósito y NINGÚN workflow lo invoca ⇒ medido el
# 2026-08-15 sobre `origin/main`: **191 ficheros** de `web/` no pasan el formateador y nada en el
# repositorio lo dice. Arreglar los 191 y cablear a la vez es una tanda que choca con toda rama
# viva; cablear primero da el control HOY y no bloquea a nadie.
#
# ⛔ Y POR QUÉ LA LÍNEA BASE ES UNA LISTA, NO UN NÚMERO. Un contador deja pasar la SUSTITUCIÓN:
# arreglas `a.tsx`, rompes `b.tsx`, el total sigue igual y el trinquete calla. La lista nombra al
# culpable nuevo aunque el total baje. Es más estricto que lo adjudicado, y el número se sigue
# imprimiendo porque es lo que pidió.
#
# Tres respuestas, nunca dos:
#   0  la deuda no sube (y dice si puede bajar)
#   1  hay un incumplidor NUEVO — lo nombra
#   2  NO HE PODIDO MIRAR (sin formateador, sin línea base, salida ilegible). Nunca es un verde.
set -uo pipefail

# ⛔ MODO `--gate`: LA PRODUCCIÓN NO ADMITE NINGUNA INYECCIÓN. Hallazgo HIGH del contraste
# the model (2026-08-15): ni el Task, ni la vía pesada del hook, ni el paso de CI limpiaban el
# entorno, y **un `FORMAT_RATCHET_CMD` heredado hizo pasar el `lint:format-ratchet` COMPLETO —las
# 16 casillas incluidas— sin ejecutar prettier ni una vez**. Una escotilla que la batería necesita
# no puede quedar viva en la puerta que la batería audita: el aviso ruidoso lo lee un humano, pero
# el verde le llega igual a la máquina.
#
# Así que el punto de entrada de producción pasa `--gate` y aquí se RECHAZA cualquier variable de
# anulación. La batería sigue inyectando, porque llama sin `--gate`.
GATE=0
for _a in "$@"; do [ "$_a" = "--gate" ] && GATE=1; done
if [ "$GATE" -eq 1 ]; then
  _sucias=""
  for _v in FORMAT_RATCHET_CMD FORMAT_RATCHET_ROOT FORMAT_RATCHET_GLOB FORMAT_RATCHET_BASELINE FORMAT_RATCHET_BASE_REF; do
    eval "[ -n \"\${${_v}:-}\" ]" && _sucias="${_sucias} ${_v}"
  done
  if [ -n "$_sucias" ]; then
    echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: en modo --gate no se admite ninguna anulación," >&2
    echo "format-ratchet:    y el entorno trae:${_sucias}. Un verde comprado con el entorno no es" >&2
    echo "format-ratchet:    una medida del árbol." >&2
    exit 2
  fi
fi

RAIZ=${FORMAT_RATCHET_ROOT:-web}
BASE=${FORMAT_RATCHET_BASELINE:-web/format-ratchet-baseline.txt}
# ⛔ EL ALCANCE ES `.`, EL MISMO QUE `format:check`, Y ESTO EMPEZÓ SIENDO `src/**/*.{ts,tsx}`.
#    El trinquete decía vigilar lo que `format:check` mide y vigilaba MENOS: `web/package.json:18`
#    corre `prettier --check .` sobre TODO `web/`. Medido el 2026-08-15, la diferencia no era
#    teórica — **191 incumplidores contra los 181 que veía mi glob**: diez ficheros en `e2e/`,
#    `e2e-visual/`, un `.md` y un `.html` que el trinquete habría dejado empeorar en silencio
#    mientras publicaba «la deuda no sube». Un barrido contesta lo que su FORMA permite, no lo que
#    yo afirmo con él, y la casilla `el alcance NO se estrecha` de la batería lo fija contra
#    `package.json` para que no vuelva a separarse.
PATRON=${FORMAT_RATCHET_GLOB:-.}

# ⛔ EL FORMATEADOR ES MI HERRAMIENTA, NO PARTE DEL SUJETO — y resolverlo relativo al árbol medido
# es lo que puso ROJO el check `web` para TODA la cola el 2026-08-15.
#
# Cada llamada hacía `cd "$RAIZ" && npx --no-install prettier`, así que la resolución dependía del
# directorio que estoy midiendo. En el árbol real funciona; en el señuelo de una casilla —un
# `mktemp -d` con `web/node_modules` ENLAZADO dentro— funcionaba en local y **no en CI**, y la
# casilla, fail-closed como debe, tumbaba el job entero. El carril de integración lo fechó contra la última
# corrida verde y acotó el mecanismo sin inventarse cuál de las dos resoluciones falla; yo tampoco
# lo sé, y por eso **quito la dependencia entera** en vez de adivinar.
#
# El binario se resuelve UNA vez desde el repositorio de este guion, por ruta absoluta. Si no está,
# se cae a `npx --no-install` y, si tampoco, el veredicto es 2 — nunca un verde.
_REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
if [ -x "$_REPO_ROOT/web/node_modules/.bin/prettier" ]; then
  PRETTIER="$_REPO_ROOT/web/node_modules/.bin/prettier"
elif npx --no-install prettier --version >/dev/null 2>&1; then
  PRETTIER="npx --no-install prettier"
else
  PRETTIER=""
fi

# ⛔ ESCOTILLA SÓLO PARA LA BATERÍA, Y RUIDOSA A PROPÓSITO. La batería no puede depender de que
#    `node_modules` esté instalado, así que puede inyectar el listador. Una escotilla silenciosa
#    sería más fuerte que la puerta que audita: por eso, cuando está puesta, lo GRITA y ningún
#    cableado del gate la define.
LISTADOR=${FORMAT_RATCHET_CMD:-}

if [ -n "$LISTADOR" ]; then
  echo "format-ratchet: ⚠ LISTADOR INYECTADO (\$FORMAT_RATCHET_CMD) — esto NO es una medición del árbol"
fi

if [ ! -r "$BASE" ]; then
  echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: no leo la línea base ${BASE}" >&2
  echo "format-ratchet: una línea base ausente no es «cero deuda»; es no haber mirado." >&2
  exit 2
fi

# --- censo de incumplidores -------------------------------------------------------------------
if [ -n "$LISTADOR" ]; then
  salida=$(eval "$LISTADOR" 2>&1); rc=$?
else
  if [ -z "$LISTADOR" ] && [ -z "$PRETTIER" ]; then
    echo "format-ratchet: NO HE PODIDO MIRAR: no encuentro el formateador (ni" >&2
    echo "  ${_REPO_ROOT}/web/node_modules/.bin/prettier ni npx --no-install). Sin él no hay censo." >&2
    exit 2
  fi
  if [ ! -d "$RAIZ" ]; then
    echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: no existe ${RAIZ}" >&2; exit 2
  fi
  salida=$(cd "$RAIZ" && NO_COLOR=1 timeout 900 $PRETTIER --check "$PATRON" 2>&1); rc=$?
fi

# ⛔ SE LE QUITAN LAS SECUENCIAS ANSI A LA SALIDA ANTES DE LEERLA, Y NO ES COSMÉTICA: es la causa
#    medida del rojo del check `web` —contexto REQUERIDO, o sea TODA la cola— entre el 2026-08-15 y
#    el 2026-08-16.
#
#    El censo de más abajo pide el prefijo LITERAL `[warn] `. En local prettier escribe eso. En CI
#    escribe `[<ESC>[33mwarn<ESC>[39m] fichero`, porque chalk detecta GitHub Actions y colorea
#    aunque no haya TTY. El `sed` no casaba NINGUNA línea, el censo salía vacío, y con rc=1 del
#    formateador eso caía en «falló pero no nombró ningún fichero»: un rc=2 sin causa visible.
#    Medido con el binario real: **0 ficheros censados con color, 3 sin él.**
#
#    ⚠ QUÉ ES LOAD-BEARING Y QUÉ NO, porque redundar sin decirlo deja una defensa SIN PROBAR:
#    medido con el binario real (prettier 3.9.6), **`NO_COLOR` gana a `FORCE_COLOR`** — o sea que
#    el `NO_COLOR=1` de arriba basta para prettier y hace legible el log. **Pero no es el que
#    manda**, y por eso no lo prueba ninguna casilla: sólo cubre la rama de prettier. La rama del
#    LISTADOR (`FORMAT_RATCHET_CMD`) no pasa por él, y ahí una salida decorada rompería el censo
#    igual. Este filtro cubre las dos y no depende de que la herramienta obedezca una variable,
#    así que **es él quien sostiene la propiedad y es él quien está fijado por la batería**.
#
#    (Publiqué primero lo contrario —«FORCE_COLOR gana»— midiéndolo con `env $var` en zsh, que
#    NO hace word-splitting y pasaba los dos ajustes como UN argumento. La sonda contestaba por
#    otra cosa. Re-medido con prefijos reales.)
#
#    Y el ESC se construye con `printf`, no se escribe `\x1b` dentro de la expresión: en esta caja
#    los escapes de byte en el patrón NO casan (medido: un barrido con `grep -P '\xNN'` daba 0 sobre
#    un positivo fabricado).
_ESC=$(printf '\033')
salida=$(printf '%s\n' "$salida" | sed "s/${_ESC}\[[0-9;]*[a-zA-Z]//g")

# rc 0 = todo conforme · 1 = hay incumplidores · el resto = el formateador no pudo correr.
if [ "$rc" -ne 0 ] && [ "$rc" -ne 1 ]; then
  echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: el formateador salió $rc" >&2
  printf '%s\n' "$salida" | tail -5 >&2
  exit 2
fi

# ⛔ LA LÍNEA DE RESUMEN TAMBIÉN EMPIEZA POR `[warn]`, y contarla infla el censo en uno: medido el
#    2026-08-15, `grep -c '^\[warn\] '` dijo 182 donde prettier decía 181. Se exige que la línea
#    NO sea la del resumen, y el resumen se reconoce por su texto.
# ⛔ Y LA EXCLUSIÓN ES POR EXISTENCIA, NO POR TEXTO — porque por texto se autoexcluye cualquiera.
#    Medido por el contraste the model el 2026-08-15 con positivos FABRICADOS: un árbol con
#    `ordinary.ts`, `Code style issues hidden.ts`, `Forgot to run hidden.ts` y `[diagnostic]
#    hidden.ts`, los CUATRO mal formateados. Prettier nombraba los cuatro; este script publicaba
#    «1 incumplidor(es) hoy · nuevos 0» y salía 0. Es decir: **bastaba llamar a un fichero como el
#    resumen para sacarlo del censo**, y con él su deuda.
#
#    Una línea del resumen NO es una ruta que exista; un incumplidor SÍ. Preguntarle al sistema de
#    ficheros distingue las dos cosas sin depender del idioma ni de la versión del formateador —
#    que era el otro techo del filtro anterior, escrito en inglés y por tanto frágil ante un
#    `LC_ALL` distinto.
#
# ⛔⛔ Y UN REGISTRO QUE NO ES UN FICHERO NO SE TIRA A LA BASURA: ES UN CENSO QUE NO SÉ LEER.
#    Aquí estaban los DOS bypass que quedaban del contraste the model, y son el mismo descuido:
#    el filtro de existencia DESCARTABA en silencio todo registro que no fuese un fichero.
#
#    · MEDIUM (`…-format-ratchet-contrast.md:215-217`): el protocolo por líneas no puede
#      representar un nombre con un salto de línea dentro. Reproducido el 2026-08-16 con prettier
#      3.9.6 sobre un árbol con `old.ts` (en la línea base) y un `new⏎line.ts` mal formateado:
#      prettier saca `[warn] new` y `[warn] line.ts` —y su resumen dice «2 files»—, ninguno de los
#      dos fragmentos existe, los dos se descartaban, y el trinquete publicaba **«1 incumplidor(es)
#      · nuevos 0 · ✔ la deuda no sube» con rc=0** mientras el fichero seguía sin formatear.
#    · LOW (`…:222-225`): un formateador que BORRA un fichero entre su salida y el filtrado deja el
#      mismo hueco. Reproducido el 2026-08-16 interponiendo un `npx` falso: nombraba `old.ts` y
#      `new.ts`, borraba `new.ts`, y el trinquete daba **rc=0 con 1/1**; restaurar `new.ts` después
#      del veredicto dejaba la deuda dentro del árbol, ya certificada.
#
#    Un descarte silencioso convierte «no sé qué es esto» en «no hay nada»: es la forma exacta del
#    cap silencioso que este trinquete existe para no tener. Así que ahora se CLASIFICA cada
#    registro y el que no encaje en ninguna clase vale 2, nunca 0 — y se nombra.
#
# ⚠ CORRECCIÓN DE MI PROPIA FRASE, medida el 2026-08-17 al verificar este arreglo. Aquí decía que
#    «el orden de las dos preguntas es sustantivo, no estilo», y que al revés bastaría llamar a un
#    fichero como el resumen para salir del censo. **De esta función no es cierto**: las dos
#    preguntas de `_huerfanos_de` son dos aceptaciones INDEPENDIENTES que hacen `continue`, así que
#    intercambiarlas no mueve ni un veredicto — un mutante que sólo las intercambia sobrevive la
#    batería entera (26/26 el 2026-08-17). El mutante que se declaró como «invierte el orden» hacía
#    DOS cambios a la vez, y el muerto lo firmaba el segundo.
#
#    Lo que de verdad sostiene el censo es más fuerte que un orden: **el censo del ÁRBOL no pregunta
#    por texto en absoluto**, sólo por existencia (el bucle de abajo; cerrado en `82b4efdf9`). Y eso
#    SÍ tiene testigo — meter el filtro léxico en ese bucle mata la casilla NOMBRE AUTOEXCLUYENTE,
#    25/1, diciendo «2 incumplidor(es)» donde hay cuatro. El filtro por texto sólo sobrevive en la
#    rama del señuelo, donde no hay árbol al que preguntar, y allí queda dicho en voz alta.
_huerfanos_de() { # <payloads de [warn], uno por línea> -> los registros que no sé clasificar
  local _l
  while IFS= read -r _l; do
    [ -n "$_l" ] || continue
    [ -f "${RAIZ}/${_l}" ] && continue
    case "$_l" in 'Code style issues'*|'Forgot to run'*) continue ;; esac
    printf '%s\n' "$_l"
  done <<EOF_HUERFANOS
$1
EOF_HUERFANOS
}

# <cuántos> <de dónde> — imprime el diagnóstico y sale 2. Nunca devuelve.
_muere_por_huerfanos() { # <lista de huérfanos> <descripción de la medida>
  echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: el formateador nombró registro(s) que no son un" >&2
  echo "format-ratchet:    fichero de ${RAIZ} ni su línea de resumen, midiendo ${2}:" >&2
  printf '%s\n' "$1" | sed 's/^/  /' >&2
  echo "format-ratchet:    Un registro sin clasificar NO es «no hay deuda». Las dos causas medidas:" >&2
  echo "format-ratchet:    un nombre con un SALTO DE LÍNEA dentro (prettier lo parte en dos y" >&2
  echo "format-ratchet:    ninguna mitad existe), o el árbol cambiando durante la medida. Renombra" >&2
  echo "format-ratchet:    el fichero o vuelve a medir sobre un árbol quieto." >&2
  exit 2
}

crudas=$(printf '%s\n' "$salida" | sed -n 's/^\[warn\] \(.*\)$/\1/p')
if [ -n "$LISTADOR" ]; then
  # Con señuelo no hay árbol donde comprobar: se mantiene el filtro léxico, y queda dicho.
  actuales=$(printf '%s\n' "$crudas" | grep -vE '^Code style issues|^Forgot to run' | LC_ALL=C sort -u)
else
  _huerfanos=$(_huerfanos_de "$crudas")
  [ -n "$_huerfanos" ] && _muere_por_huerfanos "$_huerfanos" "el árbol"
  actuales=$(while IFS= read -r _l; do
    # `-f` y no `-e`: el contraste midió un directorio llamado como un diagnóstico del
    # formateador colándose como incumplidor (rojo FALSO). Un registro del censo es un fichero.
    [ -n "$_l" ] && [ -f "${RAIZ}/${_l}" ] && printf '%s\n' "$_l"
  done <<EOF_CENSO | LC_ALL=C sort -u
$crudas
EOF_CENSO
  )
fi
n_actuales=$(printf '%s' "$actuales" | grep -c . || true)

# Una salida SIN incumplidores y SIN el resumen esperado es ilegible, no limpia.
if [ "$rc" -eq 1 ] && [ "$n_actuales" -eq 0 ]; then
  echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: el formateador falló pero no nombró ningún fichero" >&2
  printf '%s\n' "$salida" | tail -5 >&2
  exit 2
fi

# ⛔ LA LÍNEA BASE ES LA AUTORIDAD, Y UNA AUTORIDAD QUE EL MISMO CAMBIO PUEDE AMPLIAR NO CONTROLA
#    NADA. Hallazgo HIGH del contraste: añadir un fichero mal formateado **y su ruta a la línea
#    base en el mismo cambio** devolvía rc=0. El trinquete comparaba conjuntos con rigor contra un
#    patrón que el autor acababa de escribir.
#
#    La línea base se compara ahora con la del BASE DE CONFIANZA: puede ENCOGER —eso es pagar
#    deuda— pero no puede CRECER. Si no existe allí, es su primer aterrizaje y se dice en voz alta
#    en vez de suponerlo.
BASE_REF=${FORMAT_RATCHET_BASE_REF:-origin/main}
# NOTA: esta comprobación NO se salta con el señuelo — no necesita prettier, sólo git y el fichero.
# Si dependiera del señuelo, ninguna casilla podría alcanzarla, que es el error que ya cometí una
# vez con la verificación de los «arreglados».
if command -v git >/dev/null 2>&1; then
  if base_previa=$(git show "${BASE_REF}:${BASE}" 2>/dev/null); then
    _antes=$(printf '%s\n' "$base_previa" | grep -vE '^\s*#|^\s*$' | LC_ALL=C sort -u)
    _ahora=$(grep -vE '^\s*#|^\s*$' "$BASE" | LC_ALL=C sort -u)
    _crecio=$(LC_ALL=C comm -13 <(printf '%s\n' "$_antes") <(printf '%s\n' "$_ahora") | grep -c . || true)
    if [ "${_crecio:-0}" -gt 0 ]; then
      echo "format-ratchet: ⛔ la LÍNEA BASE ha CRECIDO en ${_crecio} ruta(s) respecto a ${BASE_REF}."
      echo "format-ratchet:    Una línea base que crece con la deuda no es un trinquete: sólo puede"
      echo "format-ratchet:    encoger. Rutas añadidas:"
      LC_ALL=C comm -13 <(printf '%s\n' "$_antes") <(printf '%s\n' "$_ahora") | sed 's/^/  /'
      exit 1
    fi
  else
    echo "format-ratchet: ⓘ ${BASE} no existe en ${BASE_REF}: primer aterrizaje, sin base con la que comparar."
  fi
fi

esperados=$(grep -vE '^\s*#|^\s*$' "$BASE" | LC_ALL=C sort -u)
n_esperados=$(printf '%s' "$esperados" | grep -c . || true)

nuevos=$(LC_ALL=C comm -23 <(printf '%s\n' "$actuales") <(printf '%s\n' "$esperados") | grep -c . || true)
arreglados=$(LC_ALL=C comm -13 <(printf '%s\n' "$actuales") <(printf '%s\n' "$esperados") | grep -c . || true)

# ⛔ Y LA SUPERFICIE DE EXCLUSIONES ES OTRA AUTORIDAD. Tercer hallazgo HIGH del contraste: el
#    arreglo anterior cierra que DESAPAREZCA una ruta de la línea base, pero no que se AÑADA un
#    fichero malo bajo un `.prettierignore` ampliado — ése nunca entra al censo, así que no hay
#    nada que verificar. Cuando el fichero de exclusiones cambia respecto al base de confianza, se
#    vuelve a medir el censo CON EL ANTERIOR: lo que aparezca ahí y no esté en la línea base es
#    deuda que la ampliación estaba tapando.
IGNORE="${RAIZ}/.prettierignore"
if [ -z "$LISTADOR" ] && [ -f "$IGNORE" ] && command -v git >/dev/null 2>&1; then
  if ign_previa=$(git show "${BASE_REF}:${IGNORE}" 2>/dev/null); then
    if ! printf '%s\n' "$ign_previa" | diff -q - "$IGNORE" >/dev/null 2>&1; then
      _ti=$(mktemp); printf '%s\n' "$ign_previa" > "$_ti"
      _sb=$(cd "$RAIZ" && NO_COLOR=1 timeout 900 $PRETTIER --ignore-path "$_ti" --check "$PATRON" 2>&1)
      _rcb=$?
      # ⛔ EL MISMO FILTRO ANSI QUE EL CENSO, y por la MISMA razón medida — que aquí faltaba.
      #    El censo (:143) desnuda la salida porque chalk colorea al detectar GitHub Actions
      #    aunque no haya TTY, y entonces la línea es `[<ESC>[33mwarn<ESC>[39m] fichero`, que el
      #    ancla `^\[warn\] ` NO casa. La re-medida pedía el mismo prefijo literal y NO limpiaba:
      #    mismo defecto, mismo fichero, arreglado en un sitio y no en el otro.
      #    Medido el 2026-08-20 en mainline-ci: `rc=1 · avisos=0 · presentes=0 · esperados=1`
      #    — prettier decía haber ENCONTRADO incumplidores y no se podía leer ni uno.
      _sb=$(printf '%s\n' "$_sb" | sed "s/${_ESC}\[[0-9;]*[a-zA-Z]//g")
      rm -f "$_ti"
      if [ "$_rcb" -eq 0 ] || [ "$_rcb" -eq 1 ]; then
        # El MISMO descarte silencioso vivía aquí, y aquí es peor: esta re-medida existe para
        # DESTAPAR deuda, así que un registro tirado hace ver menos de lo que hay justo en la
        # comprobación que busca lo escondido.
        _crudas_b=$(printf '%s\n' "$_sb" | sed -n 's/^\[warn\] \(.*\)$/\1/p')
        _huerf_b=$(_huerfanos_de "$_crudas_b")
        [ -n "$_huerf_b" ] && _muere_por_huerfanos "$_huerf_b" "con el .prettierignore de ${BASE_REF}"
        _con_ign_vieja=$(printf '%s\n' "$_crudas_b" \
          | while IFS= read -r _l; do [ -n "$_l" ] && [ -f "${RAIZ}/${_l}" ] && printf '%s\n' "$_l"; done \
          | LC_ALL=C sort -u)
        # ⛔ rc=1 CON CERO AVISOS ES UNA CONTRADICCIÓN, NO UN LIMPIO. `prettier --check` sale 1
        #    cuando ENCUENTRA ficheros sin formatear; si además no hay ni una línea `[warn]` que
        #    leer, lo que ha fallado es el PARSEO, no la búsqueda. Tratar ese cero como «nada
        #    tapado» convierte un «no he podido mirar» en un verde — y es exactamente lo que
        #    mainline-ci llevaba horas haciendo: `rc=1 · avisos=0 · presentes=0 · esperados=1`,
        #    mientras en local el mismo caso da avisos>0. Medido el 2026-08-20 tras instrumentar
        #    las tres salidas silenciosas de esta re-medida.
        if [ "${_rcb}" -eq 1 ] && [ -z "$(printf '%s\n' "$_crudas_b" | grep -c . | grep -v '^0$')" ]; then
          echo "format-ratchet: ⛔ NO HE PODIDO MIRAR: la re-medida con el .prettierignore de ${BASE_REF}" >&2
          echo "                salió rc=1 (hay incumplidores) y NO pude leer ni un aviso: el parseo falló." >&2
          echo "                Un cero que viene de no saber leer no es «no hay deuda tapada»." >&2
          exit 2
        fi
        _tapados=$(LC_ALL=C comm -23 <(printf '%s\n' "$_con_ign_vieja") <(printf '%s\n' "$esperados") | grep -c . || true)
        if [ "${_tapados:-0}" -gt 0 ]; then
          echo "format-ratchet: ⛔ el fichero de exclusiones creció y TAPA ${_tapados} incumplidor(es)"
          echo "format-ratchet:    que no están en la línea base. Ampliar .prettierignore no paga deuda:"
          LC_ALL=C comm -23 <(printf '%s\n' "$_con_ign_vieja") <(printf '%s\n' "$esperados") | sed 's/^/  /'
          exit 1
        else
          # ⛔ LA TERCERA SALIDA SILENCIOSA, y es la que el runner toma. Llegar aquí significa que
          #    la re-medida SÍ corrió y no encontró nada tapado — indistinguible, en el log, de
          #    «no se re-midió». Medido el 2026-08-20: en CI las dos casillas que dependen de esta
          #    rama fallan con rc=0 y NINGUNA de las otras dos guardas se dispara, así que el flujo
          #    pasa por aquí. Sin estos números no se puede saber si prettier no vio el fichero,
          #    si la lista de esperados ya lo contenía, o si la re-medida devolvió vacío.
          printf 'format-ratchet: ⓘ re-medí con el .prettierignore de %s y no hay nada tapado (rc=%s · avisos=%s · presentes=%s · esperados=%s)\n' \
            "${BASE_REF}" "${_rcb}" \
            "$(printf '%s\n' "$_crudas_b" | grep -c . || true)" \
            "$(printf '%s\n' "$_con_ign_vieja" | grep -c . || true)" \
            "$(printf '%s\n' "$esperados" | grep -c . || true)" >&2
        fi
      else
        echo "format-ratchet: ⓘ no pude re-medir con el .prettierignore de ${BASE_REF} (rc=$_rcb)" >&2
      fi
    fi
  else
    # ⛔ SALIDA SILENCIOSA. Si este `git show` falla no hay re-medida, el SUT sigue al censo
    #    y sale 0 — un «no he podido» que se lee como «la deuda no sube». Medido el
    #    2026-08-20: dos casillas del auto-test rojas en CI durante horas y el log sin UN
    #    dato para distinguirlo de un veredicto. `git show <ref>:<ruta>` exige ruta RELATIVA
    #    al repositorio, y ${IGNORE} se compone con $RAIZ.
    echo "format-ratchet: ⓘ no re-mido: no puedo leer '${BASE_REF}:${IGNORE}' con git show" >&2
  fi
else
  # ⛔ La otra salida silenciosa, por la misma razón.
  echo "format-ratchet: ⓘ no re-mido: LISTADOR='${LISTADOR:-}' · ${IGNORE} existe=$([ -f "$IGNORE" ] && echo si || echo no) · git=$(command -v git >/dev/null 2>&1 && echo si || echo no)" >&2
fi

# --- el censo se publica SIEMPRE, verde o rojo -------------------------------------------------
printf 'format-ratchet: %s incumplidor(es) hoy · línea base %s · nuevos %s · arreglados %s\n' \
  "$n_actuales" "$n_esperados" "$nuevos" "$arreglados"

if [ "$nuevos" -gt 0 ]; then
  echo "format-ratchet: ⛔ la deuda SUBE — estos no estaban en la línea base:"
  LC_ALL=C comm -23 <(printf '%s\n' "$actuales") <(printf '%s\n' "$esperados") | sed 's/^/  /'
  echo "format-ratchet: arréglalos con  (cd ${RAIZ} && npx prettier --write <fichero>)"
  echo "format-ratchet: la línea base NO se sube para acomodar un fichero nuevo."
  exit 1
fi

if [ "$arreglados" -gt 0 ]; then
  # ⛔ «DESAPARECIÓ DEL CENSO» NO ES «SE ARREGLÓ». Medido contra mi propia versión anterior el
  #    2026-08-15: añadir un incumplidor de la línea base a `web/.prettierignore` hacía que el
  #    trinquete contestara «✔ la línea base puede BAJAR 1» con rc=0. **Se podía callar la deuda
  #    escondiendo el fichero, y el gate felicitaba por ello** — que es el «cap silencioso» que
  #    este trinquete existe para no tener.
  #
  #    Así que cada supuesto arreglo se COMPRUEBA saltándose el fichero de exclusiones
  #    (`--ignore-path /dev/null`). Al señuelo se le pregunta lo mismo pasándole el fichero como
  #    $1: si el listador inyectado se saltara esta verificación, NINGUNA casilla podría
  #    ejercitar el camino, y una guarda que las pruebas no alcanzan no está probada.
  ocultos=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if [ -n "$LISTADOR" ]; then
      eval "$LISTADOR" "$f" >/dev/null 2>&1 || ocultos="${ocultos}${f}"$'\n'
    elif [ -e "${RAIZ}/${f}" ] \
      && ! (cd "$RAIZ" && timeout 120 $PRETTIER --ignore-path /dev/null --check "$f" >/dev/null 2>&1); then
      ocultos="${ocultos}${f}"$'\n'
    fi
  done <<EOF_ARREGLADOS
$(LC_ALL=C comm -13 <(printf '%s\n' "$actuales") <(printf '%s\n' "$esperados"))
EOF_ARREGLADOS
  n_ocultos=$(printf '%s' "$ocultos" | grep -c . || true)
  if [ "${n_ocultos:-0}" -gt 0 ]; then
    echo "format-ratchet: ⛔ ${n_ocultos} fichero(s) salieron del censo SIN estar formateados —"
    echo "format-ratchet:    los tapa una exclusión, no un arreglo. Esconder deuda no la baja:"
    printf '%s' "$ocultos" | sed 's/^/  /'
    exit 1
  fi
  echo "format-ratchet: ✔ y la línea base puede BAJAR ${arreglados} — retíralos de ${BASE}:"
  LC_ALL=C comm -13 <(printf '%s\n' "$actuales") <(printf '%s\n' "$esperados") | sed 's/^/  /'
fi

echo "format-ratchet: ✔ la deuda no sube"
exit 0
