#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-screenshot-freshness.sh — a repository gate. ¿Muestran las capturas el producto de HOY?
#
# ⛔ POR QUÉ, medido el 2026-08-15 y sin ningún gate que lo viera:
#   * 12 capturas de 52 rutas (23 %), y **ninguna al día**.
#   * El 2026-08-11, `a8db5d829` sustituyó los SEIS grupos del sidebar por CINCO hubs. El cromo
#     principal aparece en el 100 % del píxel de esas imágenes ⇒ **todas muestran una arquitectura
#     de información RETIRADA**.
#   * Salen de TRES commits distintos, y tres pares claro/oscuro están tomados con CINCO DÍAS de
#     diferencia: un visitante en claro y otro en oscuro ven productos distintos.
#   * Envejecieron cinco semanas y 327 commits **en silencio**: 79 tareas `lint:` y ninguna las mira.
#
# La comprobación NO es «¿son bonitas?» ni «¿cuántas hay?», que es lo que un gate ingenuo mediría.
# Es: **¿son más viejas que la última vez que cambió la interfaz que retratan?** Un fichero de imagen
# no dice cuándo se tomó, pero git sí sabe cuándo se commiteó, y eso es comparable con cuándo cambió
# la UI. Si la UI se movió después, la captura miente aunque nadie la haya tocado.
#
# Salida: 0 dentro del margen · 1 alguna va más de N días por detrás · 2 NO HE PODIDO MIRAR (nunca verde).
set -uo pipefail
LC_ALL=C; export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || { echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: no es un repo" >&2; exit 2; }

# ⛔ EL SUJETO SE FIJA AQUÍ, ANTES DE LEER NADA, Y LA POSICIÓN ES EL ARREGLO ENTERO.
#
#    La primera versión lo capturaba junto a las cachés, ~270 líneas más abajo. Parecía bastar
#    —allí es donde se usan— y NO bastaba: para entonces el guion ya había leído `UI_TS`, la lista
#    de capturas, el registro de pins y el índice de fuentes. Un `HEAD` que se moviera ANTES de esa
#    línea era invisible, así que la corrida MEZCLABA DOS SUJETOS y publicaba rc 0 tan tranquila.
#
#    No es una hipótesis: el contraste de Codex `sol max` lo demostró moviendo un HEAD real durante
#    el primer `git ls-files`, justo después de leer `UI_TS` (`a064294c8 → e3f0789ec`), y el gate
#    dio **rc 0 · OK · 1 examinada** sin decir una palabra de que el árbol se había movido.
#
# ⭐ LA LECCIÓN, escrita aquí porque aquí es donde se vuelve a equivocar quien edite esto: una
#    guarda de intervalo se ancla donde EMPIEZA EL RIESGO, no donde está el código que la motivó.
#    Yo la puse donde estaba mirando —las cachés— y protegí sólo la cola de la corrida.
SUJETO="$(git rev-parse HEAD 2>/dev/null || printf 'sin-head')"

# verifica_sujeto — se llama al terminar de leer, y su trabajo es negarse a publicar si el árbol
# que se midió al principio no es el que hay al final.
verifica_sujeto() {
    local ahora
    ahora="$(git rev-parse HEAD 2>/dev/null || printf 'sin-head')"
    if [ "$ahora" != "$SUJETO" ]; then
        echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: el árbol se movió durante la medida" >&2
        echo "        (empecé en ${SUJETO:0:9} y terminé en ${ahora:0:9}). Lo leído describe a los dos" >&2
        echo "        y el veredicto habría firmado uno. Re-córrelo sobre un árbol quieto." >&2
        exit 2
    fi
}

# ── Cuándo cambió por última vez la interfaz que las capturas retratan ────────────────
UI_TS="$(git log -1 --format=%ct -- 'web/src/components/layout' 'web/src/features' 2>/dev/null)"
if [ -z "${UI_TS:-}" ]; then
    echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: no hay historia de la UI en este clon." >&2
    exit 2
fi

# ── Las capturas ─────────────────────────────────────────────────────────────────────
#
# ⛔ ESTE PATRÓN ESTABA CIEGO AL MATERIAL QUE ESTE GATE EXISTE PARA VIGILAR. Medido el 2026-08-17:
#    `screenshot|captura|docs-site/src/assets|public/img` seleccionaba **exactamente 10 ficheros, y
#    los 10 eran archivo histórico** — nueve de la versión CONGELADA `docs-site/src/assets/console/
#    2026-06/` y una captura SEO de junio. O sea:
#
#      · sus 10 hallazgos eran falsos positivos POR DISEÑO (un snapshot de versión no puede estar
#        «al día»: describe otro producto a propósito), y
#      · su cobertura del material vivo era **CERO**: ni el par del héroe de `.github/assets/`, ni
#        las 28 de `docs-site/public/console/`, ni las 8 del reel.
#
#    Un gate que sólo puede dar rojo y sólo sobre lo que no importa se acaba cableando con
#    `|| true`, y entonces tampoco caza lo que sí. Es la misma enfermedad que un vigía que dispara
#    siempre: consume su propio armado. Y el precio se pagó: el par del héroe del README llevaba
#    desde el 2026-08-11 con el sidebar de SEIS grupos que ese mismo día se retiró —lo que la
#    cabecera de este fichero denuncia— y este gate **no lo miraba**.
#
# ⚠ LO QUE SIGUE FUERA, DICHO EN VEZ DE OMITIDO: `.github/assets/olivares-banner.png` y los tres
#   `social/social-preview-*.png` son COMPOSICIONES de marca, no capturas de la UI. Algunas embeben
#   una captura, así que su frescura es una pregunta real — pero es OTRA pregunta, la responde el
#   libro de activos y este gate no la contesta.
CAPS="$(git ls-files -- '*.png' '*.webp' 2>/dev/null | grep -iE 'screenshot|captura|docs-site/src/assets|docs-site/public/console/|\.github/assets/console-|design/launch-video/assets/console/|public/img' || true)"
N="$(printf '%s\n' "$CAPS" | grep -c . || true)"

# CONTROL POSITIVO: sin capturas no se aprueba nada. «0 obsoletas de 0» y «no encontré ninguna»
# son la misma frase con distinto significado, y la segunda no es un verde.
if [ "${N:-0}" -eq 0 ]; then
    echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: cero capturas encontradas con el patrón actual." >&2
    echo "                            Un conjunto vacío no se aprueba: o el patrón caducó, o se movieron." >&2
    exit 2
fi

# ── Las declaraciones ────────────────────────────────────────────────────────────────
#
# ⛔ ESTE REGISTRO EXISTE PORQUE EL GATE NOMBRABA UN REMEDIO QUE NO PODÍA ACEPTAR. Su mensaje decía
#    —y sigue diciendo— «re-capturar, o **declarar por escrito** por qué esa imagen sigue siendo
#    cierta», y no había ningún sitio donde escribirlo. Un gate cuyo remedio no se puede aplicar es
#    un gate que nadie puede poner verde, y lo que le pasa después es que se cablea con `|| true`.
#
# ⚠ NO ES UNA LISTA DE EXCEPCIONES: es una lista de imágenes cuya ANTIGÜEDAD ES CORRECTA. La
#   diferencia importa. Un snapshot de la documentación de `2026-06` retrata el producto de `2026-06`
#   a propósito: re-capturarlo metería la UI de hoy en la doc de una versión que describe otra cosa,
#   que es falsear el snapshot. «Está pendiente de re-capturar» NO es una razón válida aquí — eso es
#   trabajo, y va en rojo.
#
# ⛔⛔ Y ESA FRASE NO BASTABA: LA PRIMERA VERSIÓN DE ESTE REGISTRO **ERA** UNA LISTA DE EXCEPCIONES,
#    y lo demostró un panel adversarial interno el 2026-08-18 en tres líneas. El formato era
#    `<prefijo><TAB><razón>` con la razón en prosa libre, así que
#
#        .github/assets/console-<TAB>pendiente
#        docs-site/public/console/<TAB>ver ticket
#        an internal design note (not shipped)<TAB>TODO
#
#    **silenciaba las 38 capturas obsoletas y el gate salía rc=0** — con una cabecera que decía, en
#    mayúsculas, que eso no se podía hacer. Un comentario no es un mecanismo.
#
#    Y el remedio evidente —prohibir las palabras «pendiente», «TODO», «ver ticket»— es el remedio
#    equivocado: una lista de palabras prohibidas la esquiva quien escriba una palabra distinta. El
#    gate no puede juzgar prosa, así que **no se le pide que la juzgue**: se le pide que verifique un
#    hecho del árbol.
#
# ⇒ FORMATO NUEVO, de CUATRO campos: `<prefijo><TAB><clase><TAB><anclaje><TAB><razón>`.
#
#    La clase es un conjunto CERRADO —una clase desconocida sale 2, no se ignora— y cada una obliga
#    a un anclaje que este gate comprueba **sin leer la razón**:
#
#    · `snapshot-de-version` · el anclaje es la ruta del CONSUMIDOR congelado. Se exige que (a) la
#      ruta fijada lleve su propia marca de versión, (b) el consumidor lleve **la misma**, y (c) el
#      consumidor **referencie de verdad** la ruta fijada. Es la unión, no la forma: declara «este
#      activo congelado pertenece a aquella documentación congelada» y las tres patas se miden.
#    · `evidencia-fechada` · el anclaje es una fecha ISO que **tiene que aparecer en la propia ruta**
#      (en `YYYY-MM-DD`, `YYYYMMDD`, `DDMMYYYY`, `YYYY-MM` o `YYYYMM`). Una evidencia cuyo valor es su
#      fecha vive en una ruta fechada; si la ruta no la lleva, no es esa clase.
#
#    La consecuencia que cierra el agujero: **una ruta que no lleva marca de versión ni fecha NO SE
#    PUEDE FIJAR EN NINGUNA DE LAS DOS CLASES.** `.github/assets/console-`, `docs-site/public/console/`
#    y `an internal design note (not shipped)` —las tres del panel— no tienen dónde anclarse, y por eso
#    la línea que las silenciaba ya no se puede escribir. No es que se detecte y se rechace: es que
#    no es expresable.
#
# `#` comenta. Cualquier línea que no traiga los cuatro campos sale 2 nombrando el campo que falta.
PINS="$RAIZ/design/screenshot-pins.txt"
declare -a PIN_RUTA=() PIN_RAZON=() PIN_CLASE=() PIN_ANCLA=() PIN_USADO=()

# Primera marca de versión/fecha de una ruta, normalizada a lo que aparece. Vacío si no lleva.
marca_de_version() {
    printf '%s\n' "$1" | grep -oE '20[0-9][0-9]-[0-9][0-9](-[0-9][0-9])?' | head -1
}

if [ -f "$PINS" ]; then
    linea=0
    while IFS=$'\t' read -r ruta clase ancla razon; do
        linea=$((linea+1))
        case "${ruta:-}" in ''|'#'*) continue ;; esac

        if [ -z "${clase:-}" ] || [ -z "${ancla:-}" ] || [ -z "${razon// /}" ]; then
            echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: '$ruta' (línea $linea de design/screenshot-pins.txt)" >&2
            echo "                            no trae los cuatro campos <prefijo><TAB><clase><TAB><anclaje><TAB><razón>." >&2
            echo "                            Clases: snapshot-de-version | evidencia-fechada. Ver la cabecera del registro." >&2
            exit 2
        fi

        case "$clase" in
        snapshot-de-version)
            va="$(marca_de_version "$ruta")"
            vc="$(marca_de_version "$ancla")"
            if [ -z "$va" ]; then
                echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: '$ruta' no lleva marca de versión (20AA-MM)," >&2
                echo "                            así que no puede ser un snapshot de versión. Si está pendiente de" >&2
                echo "                            re-capturar, eso es TRABAJO y va en rojo, no en el registro." >&2
                exit 2
            fi
            if [ "$va" != "$vc" ]; then
                echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: '$ruta' dice ser el snapshot de '$va' pero su" >&2
                echo "                            consumidor declarado '$ancla' es de '${vc:-ninguna versión}'." >&2
                exit 2
            fi
            if ! git ls-files --error-unmatch -- "$ancla" >/dev/null 2>&1 && [ ! -e "$ancla" ]; then
                echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: el consumidor declarado '$ancla' no existe." >&2
                exit 2
            fi
            # ⛔ LA REFERENCIA NO SE BUSCA POR LA RUTA DEL REPO, y esto es medida, no estilo: el
            #    consumidor real cita `../../../../../assets/console/2026-06/access-map-light.png`,
            #    así que un `grep` del prefijo `docs-site/src/assets/console/2026-06/` da CERO sobre
            #    el par legítimo. Un gate que rechaza lo verdadero por cómo está escrita una ruta
            #    relativa se acaba desactivando entero.
            #
            #    Se busca la UNIÓN POR FICHERO: `<versión>/<nombre de la imagen>`. Eso es
            #    independiente de si la cita es relativa, absoluta o por alias, y no lo satisface un
            #    extraño — prueba que la documentación congelada usa ESTAS imágenes.
            ficheros="$(git ls-files -- "$ruta" "$ruta*" 2>/dev/null | head -40)"
            if [ -n "$ficheros" ]; then
                usado=0
                while IFS= read -r img; do
                    [ -z "$img" ] && continue
                    if grep -rqF -- "$va/${img##*/}" "$ancla" 2>/dev/null; then usado=1; break; fi
                done <<EOF
$ficheros
EOF
                if [ "$usado" -eq 0 ]; then
                    echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: '$ancla' no usa ninguna imagen de '$ruta'." >&2
                    echo "                            Un snapshot sin consumidor congelado que lo use no es un snapshot." >&2
                    exit 2
                fi
            fi
            ;;
        evidencia-fechada)
            case "$ancla" in
            20[0-9][0-9]-[0-9][0-9]-[0-9][0-9]) : ;;
            *)
                echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: el anclaje de '$ruta' debe ser una fecha ISO" >&2
                echo "                            AAAA-MM-DD; llegó '$ancla'." >&2
                exit 2 ;;
            esac
            aa="${ancla%%-*}"; resto="${ancla#*-}"; mm="${resto%%-*}"; dd="${ancla##*-}"
            visto=0
            for forma in "$ancla" "$aa$mm$dd" "$dd$mm$aa" "$aa-$mm" "$aa$mm"; do
                case "$ruta" in *"$forma"*) visto=1; break ;; esac
            done
            if [ "$visto" -eq 0 ]; then
                echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: '$ruta' se declara evidencia del $ancla pero su" >&2
                echo "                            ruta no lleva esa fecha en ninguna forma reconocida." >&2
                echo "                            Una evidencia cuyo valor es su fecha vive en una ruta fechada." >&2
                exit 2
            fi
            ;;
        *)
            echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: clase desconocida '$clase' para '$ruta'." >&2
            echo "                            El conjunto es CERRADO: snapshot-de-version | evidencia-fechada." >&2
            echo "                            No saber qué es algo no autoriza a fijarlo." >&2
            exit 2 ;;
        esac

        PIN_RUTA+=("$ruta"); PIN_CLASE+=("$clase"); PIN_ANCLA+=("$ancla")
        PIN_RAZON+=("$razon"); PIN_USADO+=(0)
    done < "$PINS"
fi

# ── Tolerancia: CUÁNTO se retrasa una captura, no CUÁNTAS se retrasan ────────────────
#
# ⛔ EL PRIMER TRINQUETE DE ESTE GATE MEDÍA LO QUE NO ERA, y lo enseñó su propia cifra: tras
#    refrescar las 8 entradas del reel, las 30 restantes salían **todas a «0 día(s)»**. No estaban
#    obsoletas: es que el último commit de UI aterrizó DESPUÉS de tomarlas, que es lo que pasa
#    siempre. Un techo por CONTEO vuelve a 38 con el siguiente cambio de UI **haga quien haga qué**,
#    así que enrojece por la mecánica del repositorio y no por un descuido. Es exactamente el vigía
#    que dispara siempre: el que se acaba cableando con `|| true`, que es de donde veníamos.
#
# La pregunta que sí distingue lo sano de lo podrido es **cuánto** va por detrás una captura. Una
# tomada esta mañana con un commit de UI esta tarde es correcta; una 23 días por detrás —las del
# reel— retrata un producto retirado. Así que el umbral es de DÍAS DE RETRASO, no de recuento, y el
# recuento se sigue informando porque describe el tamaño del trabajo.
#
# La política, dicha aquí y no escondida en el Taskfile: **una captura puede ir hasta 7 días por
# detrás de la UI**. Refrescarlas es trabajo de release, no de commit; una semana es el margen para
# hacerlo sin que el gate mienta en ninguna de las dos direcciones.
TOLERANCIA="${OLIVARES_SCREENSHOT_MAX_LAG_DAYS:-0}"
# La lista COMPLETA de rancias, siempre, aunque la salida recorte a 12. Es el worklist de B7.
LISTA_COMPLETA="${OLIVARES_SCREENSHOT_STALE_LIST:-${TMPDIR:-/tmp}/screenshot-stale.tsv}"
: > "$LISTA_COMPLETA"
case "$TOLERANCIA" in ''|*[!0-9]*)
    echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: tolerancia no numérica '$TOLERANCIA'." >&2
    exit 2 ;;
esac

# ── a repository gate: la edad se mide contra LA FUENTE QUE RETRATA, no contra toda la UI ────────
#
# ⛔ QUÉ ARREGLA. `UI_TS` es el último commit de `web/src/features` + `components/layout` ENTEROS,
#    así que una captura envejece cuando alguien toca CUALQUIER pantalla. Medido: las cuatro de
#    login/setup salían a 8 días mientras `app/pages/login.tsx` no se movía desde el 4 de agosto —
#    y llegó por su cuenta al mismo sitio: son 42 h MÁS NUEVAS que nada que las renderice, o
#    sea que el gate era INSATISFACIBLE para ellas. Ninguna recaptura las arregla, porque no están
#    rancias.
#
# ⛔ LA FUENTE SE DERIVA DE LOS IMPORTS, NO DE UNA LISTA. Una tabla captura→fichero es la forma de
#    gate que más veces hemos encontrado rota: nace correcta y se pudre en silencio en cuanto
#    alguien mueve un fichero. Aquí se parte del componente que da nombre a la captura y se sigue
#    lo que IMPORTA, dos niveles.
#
# ⛔ Y ESO CIERRA EL PUNTO CIEGO SIMÉTRICO que midió: `components/ui/` NO está en `UI_TS`, así
#    que un restyle del botón cambiaría todas las capturas y el gate callaría. Siguiendo imports,
#    un `ui/` compartido entra por donde debe: por quien lo usa.
#
# ⛔ DENY-CLOSED: si la captura no resuelve fuente, se mide contra `UI_TS` como siempre. No saber
#    qué retrata una imagen nunca autoriza a medirla con un reloj más flojo.
INDICE_FUENTES="$(mktemp)"
trap 'rm -f "$INDICE_FUENTES"' EXIT
find web/src -type f \( -name '*.tsx' -o -name '*.ts' \) 2>/dev/null > "$INDICE_FUENTES" || true

# ⛔ MEMOIZACIÓN — Y LA VERSIÓN QUE ESTE COMENTARIO SUSTITUYE NO CACHEABA NADA. La primera
#    redacción decía «sin ella este gate NO TERMINA» y montaba dos cachés que **no guardaban una
#    sola entrada**. Lo destapó el contraste de Codex `sol max` del 2026-08-30 sobre `513b10c62`
#    —«la caché no guarda los éxitos, y sus mutaciones ocurren bajo sustituciones de comando»— y
#    al leerlo se ve entero:
#
#      1. El bucle principal llamaba `_r="$(reloj_de_captura "$f")"`. Una sustitución de comando
#         es un SUBSHELL: todo lo que las funciones escribían en `_TS_FICHERO` y en `_FUENTES`
#         moría con la captura que lo había calculado. Ninguna entrada sobrevivía a su iteración.
#      2. `fuentes_de_captura` sólo escribía el caso NEGATIVO (`_FUENTES[$slug]=""`). El acierto
#         —que es el caso caro— no se guardaba ni aunque hubiera sobrevivido al punto 1.
#
#    Coste medido de esas dos juntas: el padre 10 s y este gate 490 s. Una caché que no persiste
#    es coste sin ahorro, y lo pagan los cinco carriles en cada push porque esto es un fast-lint.
#
# ⛔ POR ESO LOS TRES RESOLUTORES DEVUELVEN POR VARIABLE GLOBAL Y NO POR `stdout`, y no es estilo:
#    es la única forma de que la ESCRITURA de la caché ocurra en el mismo shell que luego la LEE.
#    Se puede hacer porque el bucle principal se alimenta de un here-doc (`done <<EOF`), que no
#    abre subshell; si alguien lo convierte en `… | while`, las tres cachés vuelven a morir en
#    silencio y el gate vuelve a los 490 s sin que nada se ponga rojo.
#
# ⛔ ESTA FRASE DECÍA «`imports_de` sí sigue imprimiendo, y puede: no guarda nada», Y CADUCÓ EN EL
#    COMMIT SIGUIENTE, cuando esa función pasó a memoizar también. La cazó el contraste, no yo, y
#    es la forma exacta que este fichero lleva documentada: al cambiar algo, la afirmación vieja
#    sobrevive donde no estás mirando. Hoy son CUATRO los que devuelven por global —`imports_de`
#    incluida— y ninguno puede llamarse dentro de `$( )`.

declare -A _TS_FICHERO=()
declare -A _FUENTES=()
declare -A _IMPORTS=()

# imports_de <fichero> — deja en IMPORTS_OUT las rutas que ese fichero importa y existen en el
# árbol, una por línea.
#
# ⛔ TAMBIÉN MEMOIZA, Y ÉSTA ES LA MITAD CARA. Curar sólo `git log` bajó el gate de 1 285 s a
#    decenas de segundos y ahí se quedó, porque el coste que quedaba no era git: es que esta
#    función forka un `grep` y un `sed` por fichero y **dos subshells anidados por cada import
#    relativo** (`$(cd … && printf '%s' "$(realpath …)")`). Los ficheros compartidos —`components/ui/`,
#    hooks, `lib/`— los importan casi todas las vistas, así que sin caché se recorren una vez por
#    cada captura que llega a ellos. La caché por SLUG de `fuentes_de_captura` no los cubre: sólo
#    evita repetir la vista entera, no sus hojas.
#
# ⛔ Por eso deja de imprimir y devuelve por global, como las otras tres: mientras su único llamador
#    posible fuera `$( )`, su caché habría muerto igual que murieron aquéllas.
imports_de() {
    local f="$1" d spec p ext acc
    IMPORTS_OUT=""
    [ -f "$f" ] || return 0
    if [ -n "${_IMPORTS[$f]+x}" ]; then IMPORTS_OUT="${_IMPORTS[$f]}"; return 0; fi
    d="$(dirname "$f")"
    acc="$(grep -oE "from '[^']+'" "$f" 2>/dev/null | sed "s/^from '//; s/'$//" | while IFS= read -r spec; do
        case "$spec" in
            @/*)       p="web/src/${spec#@/}" ;;
            ./*|../*)  p="$(cd "$d" 2>/dev/null && printf '%s' "$(realpath -m --relative-to="$RAIZ" "$spec" 2>/dev/null)")" ;;
            *)         continue ;;
        esac
        [ -n "$p" ] || continue
        for ext in '.tsx' '.ts' '/index.tsx' '/index.ts' ''; do
            if [ -f "$p$ext" ]; then printf '%s\n' "$p$ext"; break; fi
        done
    done)"
    _IMPORTS[$f]="$acc"
    IMPORTS_OUT="$acc"
    return 0
}

# ⛔⛔ Y AQUÍ ESTÁ EL TECHO DE ESTE GUION, MEDIDO — NO INTENTES EL LOTE. Con las cachés puestas
#    quedan **594 llamadas** a `git log` (una por captura y una por fuente DISTINTA: las repetidas
#    ya no se pagan), y cada una cuesta **~226 ms** en este árbol ⇒ **~134 s**, que es
#    prácticamente TODO lo que tarda el gate. Es decir: lo que queda no es caché, es git.
#
#    La idea evidente —una sola pasada `git log --format=%ct --name-only` construyendo el mapa de
#    todas las rutas de golpe— **es 370× más barata (359 ms) y DA VEREDICTOS DISTINTOS.** Medido el
#    2026-08-30 contra 40 ficheros reales: la pasada del repo entero discrepa en **3 de 40** y la
#    acotada a `web/src` en **1 de 40**.
#
#    El mecanismo, con su prueba: `git log -1 -- <ruta>` puede devolver un comportamiento de
#    **MERGE**, y `--name-only` **no imprime ficheros para un merge**, así que el lote no puede
#    verlo nunca. El caso que lo destapó es `web/src/components/data/data-table.tsx`, cuyo commit
#    es `86a4a8f69` con **dos padres** (`c0e0da33a 1956c2286`): la llamada suelta da 1787814400 y
#    el lote 1787806143 — 2,3 h más viejo. En un gate cuyo veredicto se mide en días eso parece
#    inocuo, y no lo es: decide de qué lado del margen cae una captura.
#
# ⚠ Y LA LECCIÓN DE MÉTODO, porque casi la compro: el primer control positivo del lote fue UN
#    fichero, y coincidía. Un solo acierto no acredita una equivalencia; hicieron falta 40 para
#    que apareciera el 3/40. Si alguien vuelve a intentar el lote, la barra es **acuerdo total
#    sobre una muestra**, no un ejemplo.
# ts_de_fichero <fichero> — deja en TS_OUT el `%ct` del último commit que lo tocó, 0 si ninguno.
TS_OUT=0
ts_de_fichero() {
    local f="$1" t k
    local k="$SUJETO|$f"
    if [ -n "${_TS_FICHERO[$k]+x}" ]; then TS_OUT="${_TS_FICHERO[$k]}"; return 0; fi
    t="$(git log -1 --format=%ct -- "$f" 2>/dev/null)"
    _TS_FICHERO[$k]="${t:-0}"
    TS_OUT="${t:-0}"
    return 0
}

# fuentes_de_captura <ruta png> — deja en FUENTES_OUT el componente que da nombre a la captura y
# lo que ese componente importa (dos niveles), una ruta por línea y sin repetir. Sale 1 con
# FUENTES_OUT vacío si no resuelve, y entonces el llamante cae al deny-closed.
FUENTES_OUT=""
IMPORTS_OUT=""
fuentes_de_captura() {
    local base slug raiz n1 n2 f acc
    base="$(basename "$1" .png)"
    slug="${base%-dark}"; slug="${slug%-light}"
    if [ -n "${_FUENTES[$slug]+x}" ]; then
        FUENTES_OUT="${_FUENTES[$slug]}"
        [ -n "$FUENTES_OUT" ] || return 1
        return 0
    fi
    raiz="$(grep -E "/(${slug}|${slug}-view|${slug}-page)\.tsx$" "$INDICE_FUENTES" 2>/dev/null | head -1)"
    if [ -z "$raiz" ]; then _FUENTES[$slug]=""; FUENTES_OUT=""; return 1; fi
    acc="$raiz"
    imports_de "$raiz"; n1="$IMPORTS_OUT"
    if [ -n "$n1" ]; then
        acc="${acc}
${n1}"
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            imports_de "$f"; n2="$IMPORTS_OUT"
            [ -n "$n2" ] && acc="${acc}
${n2}"
        done <<NIVEL1
${n1}
NIVEL1
    fi
    acc="$(printf '%s\n' "$acc" | sort -u | sed '/^$/d')"
    _FUENTES[$slug]="$acc"
    FUENTES_OUT="$acc"
    return 0
}

# reloj_de_captura <ruta png> — deja en RELOJ el instante contra el que se juzga ESA captura, y en
# RELOJ_ORIGEN de dónde salió: 'fuente' o 'UI_TS' (el deny-closed).
RELOJ=0
RELOJ_ORIGEN=UI_TS
reloj_de_captura() {
    local maxi f
    RELOJ="$UI_TS"; RELOJ_ORIGEN=UI_TS
    fuentes_de_captura "$1" || return 0
    [ -n "$FUENTES_OUT" ] || return 0
    maxi=0
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        ts_de_fichero "$f"
        [ "$TS_OUT" -gt "$maxi" ] && maxi="$TS_OUT"
    done <<FUENTES
${FUENTES_OUT}
FUENTES
    if [ "$maxi" -gt 0 ]; then RELOJ="$maxi"; RELOJ_ORIGEN=fuente; fi
    return 0
}

VIEJAS=0
FIJADAS=0
DENTRO=0
PEOR=0
POR_FUENTE=0
POR_UITS=0
while IFS= read -r f; do
    [ -z "$f" ] && continue
    ts="$(git log -1 --format=%ct -- "$f" 2>/dev/null)"
    [ -z "${ts:-}" ] && continue
    # El reloj es el de ESTA captura (su fuente), no el de la UI entera. Deny-closed a UI_TS.
    reloj_de_captura "$f"
    if [ "$RELOJ_ORIGEN" = "fuente" ]; then POR_FUENTE=$((POR_FUENTE+1)); else POR_UITS=$((POR_UITS+1)); fi
    [ "$ts" -ge "$RELOJ" ] && continue

    fijada=0
    for i in "${!PIN_RUTA[@]}"; do
        case "$f" in "${PIN_RUTA[$i]}"*) fijada=1; PIN_USADO[$i]=1; break ;; esac
    done
    if [ "$fijada" -eq 1 ]; then
        FIJADAS=$((FIJADAS+1))
        continue
    fi

    d=$(( (RELOJ - ts) / 86400 ))
    [ "$d" -gt "$PEOR" ] && PEOR="$d"
    if [ "$d" -le "$TOLERANCIA" ]; then
        DENTRO=$((DENTRO+1))
        continue
    fi

    VIEJAS=$((VIEJAS+1))
    # ⛔ EL TOPE DE 12 CONTABA 132 Y NOMBRABA DOCE, Y ESO NO ES UN DETALLE DE FORMATO: quien lee
    #    este gate para RE-CAPTURAR sólo puede actuar sobre lo que se le nombra, así que arregla
    #    doce y cree que ha terminado. Medido el 2026-08-25: `132 por detrás … 12 nombradas`.
    #    El recuento nunca mintió; la LISTA sí faltaba, que es la mitad accionable.
    #
    #    El tope se queda —un push no quiere 132 líneas— pero deja de ser silencioso: la lista
    #    ENTERA se escribe siempre en un fichero y el resumen dice dónde. Un cap que no dice qué
    #    recortó es la misma clase que un `head` en una sonda.
    printf '%s\t%s\n' "$f" "$d" >> "$LISTA_COMPLETA"
    [ "$VIEJAS" -le 12 ] && echo "check-screenshot-freshness: ⛔ $f es $d día(s) más vieja que la UI que retrata"
done <<EOF
$CAPS
EOF

# El sujeto, antes de decir nada: ver arriba por qué.
verifica_sujeto

# ⛔ UNA DECLARACIÓN QUE YA NO CUBRE NADA SE PUDRE EN SILENCIO, y entonces el registro deja de
#    describir el árbol: la ruta se movió o se borró y la razón sigue ahí dando permiso a nada. Se
#    trata como «no he podido mirar», no como limpio.
for i in "${!PIN_RUTA[@]}"; do
    if [ "${PIN_USADO[$i]}" -eq 0 ]; then
        echo "check-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: la declaración '${PIN_RUTA[$i]}' no cubre ninguna captura." >&2
        echo "                            O la ruta cambió, o esa imagen ya se re-capturó: retírala del registro." >&2
        exit 2
    fi
done

# ⛔ TODO PIN HONRADO SE IMPRIME, SIEMPRE, aunque el gate salga verde. Un mecanismo que silencia
#    material público y no dice qué ha silenciado se convierte en el sitio donde se esconden cosas:
#    el coste de auditarlo pasa a ser «abrir un fichero que nadie recuerda», y entonces no se audita.
#    Aquí sale en cada corrida, con su clase y su anclaje, delante de quien lea el log.
for i in "${!PIN_RUTA[@]}"; do
    echo "check-screenshot-freshness: · fijada ${PIN_RUTA[$i]} [${PIN_CLASE[$i]} → ${PIN_ANCLA[$i]}]"
done

# Se informa SIEMPRE el reparto: un gate que mide con dos relojes y no dice cuál usó en cada
# caso deja al lector sin saber si un verde es «la captura es fiel» o «no supe qué retrata».
echo "check-screenshot-freshness: relojes — $POR_FUENTE por su FUENTE (imports), $POR_UITS por UI_TS (no resolvieron)"
echo "check-screenshot-freshness: $VIEJAS por detrás más de $TOLERANCIA día(s) · $DENTRO dentro del margen (peor: $PEOR) · $FIJADAS con antigüedad declarada · $N examinadas"

# ── ¿Este push COBRA por la antigüedad, o sólo la informa? (a repository gate) ──────────────────
#
# ⛔ QUÉ ARREGLA, medido el 2026-08-28. Este gate es un fast-lint incondicional: corría en TODA
#    rama y mataba el push por unas capturas que el push no había tocado. La antigüedad la crea
#    el paso del tiempo, no el autor -- `UI_TS` es el último commit de la UI ENTERA, así que un
#    cambio en `web/src/features/<cualquier cosa>` envejece una captura de una pantalla que vive
#    en `web/src/app/pages/` y que nadie ha tocado. El resultado era una factura que llegaba a
#    quien pasara por caja, y ese día llegó a un push de UN fichero de `web/src/lib`.
#
# ⇒ Mismo predicado que `check-session-record`: se acota al DIFF del push. Cobra si el push toca
#   lo que este gate mide -- la captura, la UI contra la que se la compara, o un fichero que la
#   referencia. Si no toca nada de eso, la antigüedad SE IMPRIME (con su lista y su edad) y no
#   mata: el trabajo sigue nombrado y con dueño, pero no lo paga quien pasaba por ahí.
#
# ⛔ DENY-CLOSED EN LAS TRES FORMAS DE NO PODER MIRAR: sin tronco, sin merge-base o con un rango
#    VACÍO, se COBRA. La tercera es la que importa y no es teórica: en un push a `main` el rango
#    contra `origin/main` es vacío, y leerlo como «no toca nada» apagaría el gate justo en la rama
#    donde más vale. No saber qué toca un push nunca autoriza a cobrar menos.
TRUNK_SHOT="${OLIVARES_SCREENSHOT_TRUNK:-origin/main}"
COBRA=1
POR_QUE_COBRA='no he podido acotar el push (deny-closed)'
if [ "$VIEJAS" -gt 0 ]; then
    MB=""
    git rev-parse --verify --quiet "$TRUNK_SHOT" >/dev/null 2>&1 && \
        MB="$(git merge-base "$TRUNK_SHOT" HEAD 2>/dev/null || true)"
    if [ -n "$MB" ]; then
        TOCADO="$(git diff --name-only "$MB"...HEAD 2>/dev/null || true)"
        if [ -n "$TOCADO" ]; then
            COBRA=0
            POR_QUE_COBRA=''
            # (a) la UI contra la que se mide la antigüedad: es LITERALMENTE el minuendo de la
            #     resta, así que moverla es lo que produce el envejecimiento.
            while IFS= read -r t; do
                [ -z "$t" ] && continue
                case "$t" in
                    web/src/components/layout/*|web/src/features/*)
                        COBRA=1; POR_QUE_COBRA="el push mueve la UI que fecha las capturas ($t)"; break ;;
                esac
            done <<TOC
$TOCADO
TOC
            # (b) la captura misma, y (c) un fichero del push que la referencia por su ruta.
            if [ "$COBRA" -eq 0 ]; then
                while IFS="$(printf '\t')" read -r vieja _dias; do
                    [ -z "$vieja" ] && continue
                    while IFS= read -r t; do
                        [ -z "$t" ] && continue
                        if [ "$t" = "$vieja" ]; then
                            COBRA=1; POR_QUE_COBRA="el push toca la captura $vieja"; break
                        fi
                        # Sólo ficheros que el push deja EXISTIENDO y que son texto: un binario
                        # que contenga la ruta por casualidad no es una referencia.
                        if [ -f "$t" ] && grep -Iq -F -- "$vieja" "$t" 2>/dev/null; then
                            COBRA=1; POR_QUE_COBRA="el push toca $t, que referencia $vieja"; break
                        fi
                    done <<TOC2
$TOCADO
TOC2
                    [ "$COBRA" -eq 1 ] && break
                done < "$LISTA_COMPLETA"
            fi
        fi
    fi
fi

if [ "$VIEJAS" -gt 0 ] && [ "$COBRA" -eq 0 ]; then
    echo "check-screenshot-freshness: ⚠ $VIEJAS captura(s) van más de $TOLERANCIA día(s) por detrás"
    echo "                            (la peor, $PEOR), y este push NO toca ninguna de ellas, ni la"
    echo "                            UI que las fecha, ni nada que las referencie. Se informa y no"
    echo "                            se cobra: el trabajo es real y tiene dueño, pero no es de este push."
    echo "                            La lista completa: $LISTA_COMPLETA"
    echo "check-screenshot-freshness: OK (con aviso) — acotado al diff del push contra $TRUNK_SHOT."
    exit 0
fi

if [ "$VIEJAS" -gt 0 ]; then
    echo "check-screenshot-freshness: · la lista COMPLETA de las $VIEJAS "\
         "(la salida de arriba recorta a 12) esta en: $LISTA_COMPLETA" >&2
    echo "check-screenshot-freshness: ⛔ el material público muestra un producto que ya no existe:" >&2
    echo "                            $VIEJAS captura(s) van más de $TOLERANCIA día(s) por detrás (la peor, $PEOR)." >&2
    echo "                            Re-capturar (scripts/docs-captures.sh), o declarar en" >&2
    echo "                            design/screenshot-pins.txt por qué esa imagen sigue siendo cierta." >&2
    exit 1
fi

# ⚠ AQUÍ HABÍA UN AVISO DE «MARGEN FLOJO» —«la peor va 0 días por detrás contra un margen de 7,
#   bájalo»— copiado del trinquete de conteo, y era **consejo equivocado**. Un techo de deuda se
#   aprieta a lo ya conseguido porque la deuda no debe volver a crecer; pero esto **no es una deuda,
#   es una POLÍTICA**: «las capturas pueden ir hasta una semana por detrás». Justo después de
#   refrescar, la peor va siempre 0 días por detrás, así que el aviso pedía bajar el margen a 0 —y
#   con 0 el gate enrojece con el siguiente commit de UI, que es el vigía-que-siempre-dispara del
#   que veníamos. El margen se cambia cuando cambie la política, no cuando el árbol esté al día.
echo "check-screenshot-freshness: OK — ninguna captura va más de $TOLERANCIA día(s) por detrás de la interfaz."
exit 0
