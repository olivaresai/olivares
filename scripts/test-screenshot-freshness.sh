#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Batería de check-screenshot-freshness.sh.
#
# ⛔ POR QUÉ EXISTE, y lo escribo el mismo día que le encontré al gate su defecto peor: su patrón de
#    selección elegía **exactamente 10 ficheros y los 10 eran archivo histórico**, así que daba 10
#    hallazgos falsos y cubría CERO material vivo. Nada lo veía porque nada comprobaba QUÉ examina.
#
#    Un gate de este tipo no se prueba contando hallazgos —el número cuadra por casualidad—: se
#    prueba afirmando **qué entra en el examen** y **qué veredicto sale**. El caso 7 es el que habría
#    cazado aquel defecto y por eso está escrito como caso propio.
#
# ⚠ SIN PUERTA TRASERA: el señuelo se monta como un repo git de verdad y se le pasa al gate por
#   `OLIVARES_CLONE`, que es el anclaje que el gate ya documenta para sí mismo — no un parámetro de
#   prueba añadido para esto.
set -uo pipefail

# ⛔ AISLAMIENTO DEL ENTORNO GIT, Y ESTA LÍNEA NO LA PUSE YO: la exigió `lint:git-env` al empujar,
#    y tenía razón. Esta batería empareja `mktemp -d` con `git init`, y git EXPORTA `GIT_DIR` a sus
#    hooks desde cualquier worktree ENLAZADO — es decir, desde toda sesión en paralelo de este repo.
#    Sin sanear, mis repos señuelo se habrían conducido contra el repo VIVO. Ya pasó el 2026-08-06 y
#    dejó la rama del PR #526 apuntando a un commit de fixture.
#
#    Fail-closed a propósito: un saneador que no se puede cargar es «no he podido aislar», nunca
#    «no hacía falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
LC_ALL=C; export LC_ALL

AQUI="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd -P)"
GATE="$AQUI/check-screenshot-freshness.sh"
FALLOS=0
CASOS=0

# Fechas fijas: el señuelo no puede depender del reloj de quien lo corre.
T_VIEJO='2026-01-01T00:00:00Z'
T_UI='2026-06-01T00:00:00Z'
T_NUEVO='2026-07-01T00:00:00Z'

commitear() { # <fecha> <mensaje>
    GIT_AUTHOR_DATE="$1" GIT_COMMITTER_DATE="$1" \
        git -c user.email=t@t -c user.name=T commit -q -m "$2" --no-verify
}

señuelo() { # imprime la raíz de un repo señuelo nuevo
    local d; d="$(mktemp -d)"
    git -C "$d" init -q
    mkdir -p "$d/web/src/features" "$d/docs-site/public/console" "$d/design"
    # 1) capturas VIEJAS, antes de que la UI se moviera
    printf 'png' > "$d/docs-site/public/console/vista-light.png"
    git -C "$d" add -A && (cd "$d" && commitear "$T_VIEJO" "capturas viejas")
    # 2) la UI se mueve
    printf 'export const X = 1\n' > "$d/web/src/features/x.tsx"
    git -C "$d" add -A && (cd "$d" && commitear "$T_UI" "la UI cambia")
    printf '%s' "$d"
}

# Señuelo con un PAR CONGELADO: activo `…/2026-06/` + documentación `…/docs/2026-06/` que lo cita
# **relativa**, que es como lo cita el árbol real. Un señuelo cuyo consumidor citase la ruta con el
# prefijo del repo mediría una cita que en producción no existe.
señuelo_congelado() {
    local d; d="$(mktemp -d)"
    git -C "$d" init -q
    mkdir -p "$d/web/src/features" "$d/docs-site/src/assets/console/2026-06" \
             "$d/docs-site/src/content/docs/2026-06" "$d/design"
    printf 'png' > "$d/docs-site/src/assets/console/2026-06/vista-light.png"
    printf 'img: ../../../../../assets/console/2026-06/vista-light.png\n' \
        > "$d/docs-site/src/content/docs/2026-06/x.mdx"
    git -C "$d" add -A && (cd "$d" && commitear "$T_VIEJO" "par congelado")
    printf 'export const X = 1\n' > "$d/web/src/features/x.tsx"
    git -C "$d" add -A && (cd "$d" && commitear "$T_UI" "la UI cambia")
    printf '%s' "$d"
}

corre() { # <raiz> -> imprime "rc<TAB>salida"
    local out rc
    out="$(OLIVARES_CLONE="$1" bash "$GATE" 2>&1)"; rc=$?
    printf '%s\t%s' "$rc" "$out"
}

caso() { # <nombre> <rc esperado> <raiz> [texto que debe aparecer]
    local r; r="$(corre "$3")"
    local rc="${r%%$'\t'*}" out="${r#*$'\t'}"
    if [ "$rc" != "$2" ]; then
        echo "  FALLO $1: rc=$rc, esperaba $2"; echo "$out" | sed 's/^/        /' | head -4
        CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)); return
    fi
    # ⛔ `case`, NO `printf … | grep -q`. Con `pipefail`, `grep -q` sale en cuanto encuentra, el
    #    productor muere con SIGPIPE y la TUBERÍA devuelve 141 **en el caso de éxito**. Lo cazó
    #    `lint:sigpipe-booleans` al empujar. El texto ya está en una variable: la tubería sobraba.
    if [ -n "${4:-}" ]; then
        case "$out" in
            *"$4"*) : ;;
            *)  echo "  FALLO $1: rc correcto pero no dijo «$4»"
                printf '%s\n' "$out" | head -4 | sed 's/^/        /'
                CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)); return ;;
        esac
    fi
    CASOS=$((CASOS+1)); echo "  ok    $1"
}

# 1) Una captura anterior al último cambio de la UI es un hallazgo.
D="$(señuelo)"
caso "una captura anterior a la UI sale 1 y se nombra" 1 "$D" "vista-light.png"

# 7) EL CASO QUE HABRÍA CAZADO EL DEFECTO REAL: `docs-site/public/console/` tiene que ENTRAR en el
#    examen. Se afirma sobre el recuento de examinadas, no sobre el veredicto, porque el veredicto
#    puede salir bien por casualidad mientras el fichero ni se mira.
R="$(corre "$D")"
case "${R#*$'\t'}" in
    *"· 1 examinadas"*)
        CASOS=$((CASOS+1)); echo "  ok    docs-site/public/console ENTRA en el examen (el punto ciego que costó el héroe)" ;;
    *)  echo "  FALLO docs-site/public/console no entró en el examen"; CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)) ;;
esac

# ⛔⛔ LOS CASOS DEL PIN, y existen porque la primera versión del registro ERA una lista de
#    excepciones. Un panel adversarial interno lo demostró el 2026-08-18 con tres líneas de razón
#    libre —«pendiente», «ver ticket», «TODO»— que silenciaban las 38 capturas obsoletas del árbol
#    real y dejaban el gate en rc=0. La cabecera del registro prohibía justo eso, en mayúsculas.
#
#    Lo que sigue prueba el mecanismo que lo sustituye, y **el orden importa**: primero que lo
#    LEGÍTIMO sigue pasando (un endurecimiento que rompe lo verdadero se desactiva entero), y
#    después, una por una, cada forma de la mentira.
D="$(señuelo_congelado)"
PIN="$D/design/screenshot-pins.txt"
ACT='docs-site/src/assets/console/2026-06/'
DOC='docs-site/src/content/docs/2026-06'

# 2) El par legítimo: activo congelado + consumidor congelado que LO USA.
printf '%s\tsnapshot-de-version\t%s\tLa doc de 2026-06 describe ese producto.\n' "$ACT" "$DOC" > "$PIN"
caso "un snapshot con consumidor congelado que lo usa pasa" 0 "$D" "1 con antigüedad declarada"

# 2-bis) …y se IMPRIME. Un mecanismo que silencia material público sin decir qué silencia se
#        convierte en el sitio donde se esconden cosas.
caso "el pin honrado se imprime con su clase y su anclaje" 0 "$D" "· fijada $ACT [snapshot-de-version"

# 3) ⭐ EL ABUSO DEL PANEL, TEXTUAL. La línea que silenciaba el árbol entero ya no PARSEA.
printf 'docs-site/public/console/\tpendiente\n' > "$PIN"
caso "la línea del panel («<ruta><TAB>pendiente») sale 2" 2 "$D" "no trae los cuatro campos"

# 3-bis) ⭐ Y con los cuatro campos tampoco: una ruta sin marca de versión NO ES EXPRESABLE como
#        snapshot. Es la pata que cierra el agujero — no se detecta la mentira, es que no se puede
#        escribir.
printf 'docs-site/public/console/\tsnapshot-de-version\t%s\tpendiente de re-capturar\n' "$DOC" > "$PIN"
caso "una ruta sin marca de versión no se puede fijar" 2 "$D" "no lleva marca de versión"

# 3-ter) Versión que no casa: el activo es de 2026-06 y el consumidor declarado de 2025-01.
mkdir -p "$D/docs-site/src/content/docs/2025-01"
printf 'nada\n' > "$D/docs-site/src/content/docs/2025-01/x.mdx"
printf '%s\tsnapshot-de-version\tdocs-site/src/content/docs/2025-01\tconsumidor de otra versión\n' "$ACT" > "$PIN"
caso "un consumidor de OTRA versión sale 2" 2 "$D" "es de '2025-01'"

# 3-quater) El consumidor es de la versión correcta pero NO USA ninguna de esas imágenes.
printf 'texto sin imágenes\n' > "$D/docs-site/src/content/docs/2026-06/x.mdx"
git -C "$D" add -A && (cd "$D" && commitear "$T_VIEJO" "consumidor sin imágenes")
printf '%s\tsnapshot-de-version\t%s\tdice ser su consumidor\n' "$ACT" "$DOC" > "$PIN"
caso "un consumidor que no usa las imágenes sale 2" 2 "$D" "no usa ninguna imagen"
printf 'img: ../../../../../assets/console/2026-06/vista-light.png\n' > "$D/docs-site/src/content/docs/2026-06/x.mdx"
git -C "$D" add -A && (cd "$D" && commitear "$T_VIEJO" "consumidor restaurado")

# 3-quinquies) Clase desconocida: el conjunto es CERRADO. No saber qué es algo no autoriza a fijarlo.
printf '%s\tpor-ahora\t%s\tuna clase inventada\n' "$ACT" "$DOC" > "$PIN"
caso "una clase desconocida sale 2" 2 "$D" "clase desconocida"

# 3-sexies) ⭐ Y LA SEGUNDA CLASE TIENE QUE CERRAR LA MISMA PUERTA, o el agujero sólo se ha movido:
#           la ruta del panel declarada como evidencia fechada tampoco pasa, porque su ruta no lleva
#           la fecha. Las DOS clases rechazan una ruta sin marca temporal, que es lo que hace que la
#           línea de la refutación no sea expresable en ninguna de ellas.
printf 'docs-site/public/console/\tevidencia-fechada\t2026-06-30\tevidencia de aquel día\n' > "$PIN"
caso "la ruta del panel tampoco pasa como evidencia fechada" 2 "$D" "no lleva esa fecha"

# 3-sexies-bis) Un anclaje que no es una fecha ISO.
printf 'design/seo/30062026/\tevidencia-fechada\tjunio de 2026\tprosa en vez de fecha\n' > "$PIN"
caso "un anclaje que no es fecha ISO sale 2" 2 "$D" "debe ser una fecha ISO"

# 3-septies) …y la forma DDMMAAAA sí se reconoce (control: la regla no es más estrecha que la verdad).
mkdir -p "$D/design/seo/30062026"
printf 'png' > "$D/design/seo/30062026/screenshot-search.png"
git -C "$D" add -A && (cd "$D" && commitear "$T_VIEJO" "evidencia seo")
printf '%s\tsnapshot-de-version\t%s\tok\ndesign/seo/30062026/\tevidencia-fechada\t2026-06-30\tSu valor es su fecha.\n' "$ACT" "$DOC" > "$PIN"
caso "la fecha en forma DDMMAAAA se reconoce" 0 "$D" "2 con antigüedad declarada"

# 4) Una declaración que ya no cubre nada se pudre: 2, no verde.
printf '%s\tsnapshot-de-version\t%s\tya se re-capturó\n' "${ACT}inexistente/" "$DOC" > "$PIN"
caso "una declaración que no cubre nada sale 2" 2 "$D" "no cubre ninguna captura"
rm -rf "$D"

# ⛔ EL MARGEN DE RETRASO. Sustituyó primero a un `|| true` y después a un techo de CONTEO que era
#    la medida equivocada: tras refrescar las 8 del reel, las 30 restantes salían todas a «0 días»
#    —el commit de UI aterriza después de la captura, siempre— así que un techo por número volvía a
#    su máximo con cada cambio de UI y enrojecía por mecánica, no por descuido. Lo que distingue lo
#    sano de lo podrido es CUÁNTO va por detrás una captura.
#
#    El señuelo tiene la captura 151 días por detrás (T_VIEJO a T_UI), así que sirve para las dos
#    direcciones sin fabricar nada.
D="$(señuelo)"
r="$(OLIVARES_CLONE="$D" OLIVARES_SCREENSHOT_MAX_LAG_DAYS=200 bash "$GATE" 2>&1; echo "rc=$?")"
case "$r" in
    *"rc=0"*) CASOS=$((CASOS+1)); echo "  ok    una captura DENTRO del margen no es un hallazgo" ;;
    *) echo "  FALLO el margen no absorbió un retraso menor que él"; CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)) ;;
esac
# …y la dirección que SÍ tiene que disparar: el mismo retraso contra un margen menor.
r="$(OLIVARES_CLONE="$D" OLIVARES_SCREENSHOT_MAX_LAG_DAYS=100 bash "$GATE" 2>&1; echo "rc=$?")"
case "$r" in
    *"rc=1"*) CASOS=$((CASOS+1)); echo "  ok    el mismo retraso contra un margen menor enrojece" ;;
    *) echo "  FALLO un retraso por encima del margen no enrojeció"; CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)) ;;
esac
# …y el informe dice CUÁNTO va la peor, que es el número con el que se decide.
case "$r" in
    *"peor: 151"*) CASOS=$((CASOS+1)); echo "  ok    el informe nombra el retraso de la peor" ;;
    *) echo "  FALLO el informe no dijo cuánto va la peor"; CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1))
       printf '%s\n' "$r" | head -3 | sed 's/^/        /' ;;
esac
# …y una tolerancia que no es un número es «no he podido mirar», nunca «sin margen».
r="$(OLIVARES_CLONE="$D" OLIVARES_SCREENSHOT_MAX_LAG_DAYS=mucho bash "$GATE" 2>&1; echo "rc=$?")"
case "$r" in
    *"rc=2"*) CASOS=$((CASOS+1)); echo "  ok    una tolerancia no numérica sale 2" ;;
    *) echo "  FALLO una tolerancia no numérica no salió 2"; CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)) ;;
esac
rm -rf "$D"

# 5) Captura POSTERIOR a la UI: verde de verdad.
D="$(señuelo)"
printf 'png nuevo' > "$D/docs-site/public/console/vista-light.png"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "re-capturada")
caso "una captura posterior a la UI sale 0" 0 "$D" "0 por detrás más de"
rm -rf "$D"

# 6) Cero capturas reconocidas: 2. Un conjunto vacío no se aprueba.
D="$(mktemp -d)"
git -C "$D" init -q
mkdir -p "$D/web/src/features"
printf 'export const X = 1\n' > "$D/web/src/features/x.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_UI" "sólo UI")
caso "cero capturas reconocidas sale 2" 2 "$D" "cero capturas"
rm -rf "$D"

# ── a repository gate: el gate se acota al DIFF del push ─────────────────────────────────────────
#
# ⛔ LOS 19 CASOS DE ARRIBA NO PRUEBAN NADA DE ESTO, y decirlo importa: sus señuelos no tienen
#    `origin/main`, así que caen por el deny-closed y COBRAN igual que antes. Que sigan verdes
#    demuestra que no rompí el camino viejo -- no que el nuevo funcione. El camino nuevo necesita
#    un señuelo CON tronco, y por eso existe este bloque.
señuelo_con_tronco() { # imprime la raíz; deja origin/main en el commit de la UI
    local d; d="$(mktemp -d)"
    git -C "$d" init -q
    mkdir -p "$d/web/src/features" "$d/docs-site/public/console" "$d/design" "$d/scripts"
    printf 'png' > "$d/docs-site/public/console/vista-light.png"
    git -C "$d" add -A && (cd "$d" && commitear "$T_VIEJO" "capturas viejas")
    printf 'export const X = 1\n' > "$d/web/src/features/x.tsx"
    git -C "$d" add -A && (cd "$d" && commitear "$T_UI" "la UI cambia")
    # El tronco queda AQUÍ: lo que venga después es «lo que el push añade».
    git -C "$d" update-ref refs/remotes/origin/main HEAD
    printf '%s' "$d"
}

# (20) Dirección DISPARADORA: el push mueve la UI que fecha las capturas ⇒ cobra.
D="$(señuelo_con_tronco)"
printf 'export const Y = 2\n' > "$D/web/src/features/y.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "el push toca la UI")
caso "(GAT-35) un push que toca la UI paga la antigüedad" 1 "$D" "vista-light.png"
rm -rf "$D"

# (21) Dirección NO disparadora, que es la mitad por la que existe esto: el push no toca la
#      captura, ni la UI, ni nada que la referencie ⇒ informa y NO mata.
D="$(señuelo_con_tronco)"
printf '#!/bin/sh\necho ajeno\n' > "$D/scripts/ajeno.sh"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "push ajeno")
caso "(GAT-35) un push ajeno NO paga, y lo dice" 0 "$D" "NO toca ninguna de ellas"
rm -rf "$D"

# (22) La tercera vía de cobro: el push toca un fichero que REFERENCIA la captura. Sin este
#      caso, «ajeno» se confundiría con «no toca la UI» y un doc que enseña una imagen rancia
#      pasaría de largo.
D="$(señuelo_con_tronco)"
printf 'Mira docs-site/public/console/vista-light.png\n' > "$D/docs-site/guia.md"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "un doc que la referencia")
caso "(GAT-35) un push que referencia la captura paga" 1 "$D" "vista-light.png"
rm -rf "$D"

# (23) EL BORDE QUE PROTEGE A `main`: HEAD == origin/main ⇒ el rango es VACÍO. Leerlo como «no
#      toca nada» apagaría el gate justo en el tronco. Vacío = no he podido acotar = se cobra.
D="$(señuelo_con_tronco)"
caso "(GAT-35) un rango vacío COBRA, no absuelve" 1 "$D" "vista-light.png"
rm -rf "$D"

# (24)(25) EL CAMINO DE RESERVA, MEDIDO — lo pidió el contraste de P y tenía razón: los cuatro
#          casos de arriba prueban las vías que RESUELVEN el rango; ninguno afirmaba que el
#          deny-closed corta. Un camino de reserva que nunca se ve cortar es una rama de código
#          que nadie ha ejecutado nunca, y ésas se pudren en silencio.
#
#          Son DOS y no uno porque el rango se puede no resolver por dos motivos distintos, y
#          quien arregle uno debe ver el otro seguir cubierto: no existe el tronco, o existe y no
#          comparte historia con HEAD.
D="$(señuelo_con_tronco)"
git -C "$D" update-ref -d refs/remotes/origin/main
printf '#!/bin/sh\necho ajeno\n' > "$D/scripts/ajeno.sh"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "push ajeno, pero sin tronco")
caso "(GAT-35) sin tronco NO se absuelve: se cobra" 1 "$D" "vista-light.png"
rm -rf "$D"

# Historias inconexas: el tronco EXISTE y `merge-base` no devuelve nada. Sin este caso, alguien
# que cambiara la comprobación de tronco por un `rev-parse` a secas creería estar cubierto.
#
# ⛔ El tronco huérfano se fabrica con `commit-tree`, SIN tocar el árbol de trabajo. La primera
#    versión hacía `checkout --orphan` y el caso salía verde por el motivo equivocado: ese
#    checkout re-commitea la captura con fecha de HOY, así que dejaba de ser vieja y el gate no
#    tenía nada que juzgar. Un caso que pasa porque su sujeto desapareció no prueba nada.
D="$(señuelo_con_tronco)"
printf '#!/bin/sh\necho ajeno\n' > "$D/scripts/ajeno.sh"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "push ajeno")
ARBOL_VACIO="$(git -C "$D" hash-object -t tree /dev/null)"
SIN_PADRE="$(GIT_AUTHOR_DATE="$T_UI" GIT_COMMITTER_DATE="$T_UI" \
    git -C "$D" -c user.email=t@t -c user.name=T commit-tree "$ARBOL_VACIO" -m "tronco sin parentesco")"
git -C "$D" update-ref refs/remotes/origin/main "$SIN_PADRE"
caso "(GAT-35) sin merge-base tampoco se absuelve" 1 "$D" "vista-light.png"
rm -rf "$D"

# ── a repository gate: la edad se mide contra LA FUENTE QUE RETRATA ──────────────────────────────
#
# ⛔ LOS CASOS DE ARRIBA NO PRUEBAN ESTO. Sus señuelos no tienen un componente cuyo nombre case con
#    la captura, así que caen por el deny-closed y se miden contra `UI_TS` igual que antes. Que
#    sigan verdes dice que no rompí el camino viejo — el nuevo necesita señuelos que RESUELVAN.
señuelo_con_fuente() { # imprime la raíz; la captura retrata `vista-view.tsx` y ambas son viejas
    local d; d="$(mktemp -d)"
    git -C "$d" init -q
    mkdir -p "$d/web/src/features/vista" "$d/web/src/features/otra" \
             "$d/web/src/components/ui" "$d/docs-site/public/console" "$d/design"
    printf "import { Boton } from '@/components/ui/boton'\nexport const V = 1\n" \
        > "$d/web/src/features/vista/vista-view.tsx"
    printf 'export const Boton = 1\n' > "$d/web/src/components/ui/boton.tsx"
    printf 'png' > "$d/docs-site/public/console/vista-light.png"
    git -C "$d" add -A && (cd "$d" && commitear "$T_VIEJO" "la vista, su boton y su captura")
    printf '%s\n' "$d"
}

# (26) LA DIRECCIÓN QUE JUSTIFICA a repository gate: otra pantalla se mueve y la captura NO envejece.
#      Con `UI_TS` global esto es ROJO — es exactamente el falso positivo que costó los pushes.
D="$(señuelo_con_fuente)"
printf 'export const O = 2\n' > "$D/web/src/features/otra/otra-view.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "se mueve OTRA pantalla")
caso "(GAT-36) otra pantalla se mueve y la captura fiel NO envejece" 0 "$D" "por su FUENTE"
rm -rf "$D"

# (27) Y la contraria, o el gate no serviría para nada: su PROPIA fuente se mueve ⇒ rojo.
D="$(señuelo_con_fuente)"
printf "import { Boton } from '@/components/ui/boton'\nexport const V = 2\n" \
    > "$D/web/src/features/vista/vista-view.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "se mueve SU vista")
caso "(GAT-36) su propia fuente se mueve y la captura envejece" 1 "$D" "vista-light.png"
rm -rf "$D"

# (28) EL PUNTO CIEGO SIMÉTRICO que midió y que la derivación por IMPORTS cierra sola:
#      `components/ui/` NO está en `UI_TS`, así que un restyle del botón cambiaría TODAS las
#      capturas y el gate callaría. Siguiendo imports entra por donde debe: por quien lo usa.
D="$(señuelo_con_fuente)"
printf 'export const Boton = 2 // restyle\n' > "$D/web/src/components/ui/boton.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "restyle del boton COMPARTIDO")
caso "(GAT-36) un restyle de un ui/ importado SÍ envejece la captura" 1 "$D" "vista-light.png"
rm -rf "$D"

# (29) DENY-CLOSED: una captura que no resuelve fuente se mide contra UI_TS, como siempre. Lo pidió
#      el contraste de P: un camino de reserva que nunca se ve ejecutar es código muerto.
D="$(señuelo)"   # su captura se llama vista-light.png y NO hay vista.tsx: no resuelve
caso "(GAT-36) sin fuente resoluble se cae a UI_TS y sigue cobrando" 1 "$D" "por UI_TS"
rm -rf "$D"

# ⛔⛔ (30-31) EL MUTANTE QUE DESTAPÓ ESTO, VERSIONADO — antes lo corría una persona a mano.
#
# El contraste de Codex `sol max` del 2026-08-30 mató a repository gate a mano: puso el reloj global de vuelta
# y vio caer los casos 26 y 28. Y su reproche es el que importa: **un banco que no incorpora el
# mutante que lo destapó no protege de la regresión.** Los casos 26-29 prueban que el gate hace lo
# que dice HOY; sólo esto prueba que dejará de estar verde si alguien deshace la derivación por
# fuente — que es la única forma en que este trabajo se pierde en silencio.
#
# La mutación no borra la función: le anula el cuerpo dejando el `RELOJ="$UI_TS"` que ya se fija en
# su primera línea. Es decir, el mutante ES el gate anterior a a repository gate, y sigue ejecutándose entero.
mutante_reloj_global() { # imprime la ruta de un gate con el reloj por FUENTE anulado
    local m; m="$(mktemp)"
    sed 's@^    fuentes_de_captura "$1" || return 0$@    return 0  # MUTANTE-RELOJ-GLOBAL@' \
        "$GATE" > "$m"
    printf '%s\n' "$m"
}

caso_mutante() { # <nombre> <rc esperado CON el mutante> <raiz> <gate mutado> <texto que debe salir>
    local out rc
    out="$(OLIVARES_CLONE="$3" bash "$4" 2>&1)"; rc=$?
    if [ "$rc" != "$2" ]; then
        echo "  FALLO $1: el mutante SOBREVIVIÓ — rc=$rc, esperaba $2"
        printf '%s\n' "$out" | head -4 | sed 's/^/        /'
        CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)); return
    fi
    case "$out" in
        *"$5"*) : ;;
        *)  echo "  FALLO $1: rc correcto pero el mutante no dijo «$5»"
            printf '%s\n' "$out" | head -4 | sed 's/^/        /'
            CASOS=$((CASOS+1)); FALLOS=$((FALLOS+1)); return ;;
    esac
    CASOS=$((CASOS+1)); echo "  ok    $1"
}

MUT="$(mutante_reloj_global)"

# ⛔ CONTROL POSITIVO DE QUE EL MUTANTE SE APLICÓ, y va ANTES de usarlo. Sin él, un `sed` que no
#    casa nada produce una copia IDÉNTICA del gate: los dos casos de abajo saldrían verdes y la
#    batería estaría acreditando una mutación que nunca ocurrió. Es «no he podido mirar», y sale 2.
MUT_MARCAS="$(grep -c 'MUTANTE-RELOJ-GLOBAL' "$MUT" || true)"
if [ "$MUT_MARCAS" != "1" ]; then
    echo "test-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: la mutación del reloj no se" >&2
    echo "        aplicó ($MUT_MARCAS marcas, esperaba 1). Si el gate cambió de forma, re-ancla el sed." >&2
    rm -f "$MUT"; exit 2
fi
if ! bash -n "$MUT" 2>/dev/null; then
    echo "test-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: el mutante no es sintácticamente" >&2
    echo "        válido, así que su rojo mediría el sed y no el gate." >&2
    rm -f "$MUT"; exit 2
fi

# (30) Con el reloj global vuelve el FALSO POSITIVO: otra pantalla se mueve y la captura fiel
#      envejece. Es literalmente el rojo que costó los pushes de.
D="$(señuelo_con_fuente)"
printf 'export const O = 2\n' > "$D/web/src/features/otra/otra-view.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "se mueve OTRA pantalla")
caso_mutante "(GAT-36) el reloj global resucita el falso positivo del caso 26" 1 "$D" "$MUT" "vista-light.png"
rm -rf "$D"

# (31) Y vuelve el FALSO NEGATIVO simétrico: `components/ui/` no está en `UI_TS`, así que el
#      restyle del botón compartido deja de verse. El gate calla sobre una captura que ya miente.
D="$(señuelo_con_fuente)"
printf 'export const Boton = 2 // restyle\n' > "$D/web/src/components/ui/boton.tsx"
git -C "$D" add -A && (cd "$D" && commitear "$T_NUEVO" "restyle del boton COMPARTIDO")
caso_mutante "(GAT-36) el reloj global vuelve a callar ante el restyle del caso 28" 0 "$D" "$MUT" "0 por detrás"
rm -rf "$D"
rm -f "$MUT"

# ⛔⛔ (32) EL CASO QUE PROHÍBE EL LOTE — antes esto vivía SÓLO en prosa, y una prosa no impide nada.
#
# El techo de este gate son ~594 llamadas a `git log` a ~226 ms. La optimización evidente es UNA
# pasada `git log --format=%ct --name-only` construyendo el mapa entero: 359 ms, 370× más barato.
# **Y da veredictos distintos.** Contra 40 ficheros reales del árbol discrepaba en 3 de 40 (repo
# entero) y en 1 de 40 (acotada a `web/src`).
#
# El mecanismo, que es lo que este caso CONGELA: `git log -1 -- <ruta>` puede devolver un commit de
# MERGE, y `--name-only` **no imprime ficheros para un merge**, así que el lote no puede verlo
# nunca. No hace falta un merge raro: basta uno que RESUELVA el fichero —contenido distinto de sus
# dos padres—, que es lo que pasa al resolver un conflicto. El caso vivo que lo destapó fue
# `web/src/components/data/data-table.tsx` → `86a4a8f69`, dos padres, 2,3 h de diferencia.
#
# Si alguien mete el lote, este caso se pone rojo. Ésa es toda su razón de ser.
D="$(mktemp -d)"
git -C "$D" init -q -b main
printf 'v1\n' > "$D/f.txt"; git -C "$D" add f.txt
(cd "$D" && commitear "$T_VIEJO" "base")
git -C "$D" checkout -q -b lado
printf 'lado\n' > "$D/f.txt"; git -C "$D" add f.txt
(cd "$D" && commitear "$T_VIEJO" "el lado toca f.txt")
git -C "$D" checkout -q main
printf 'main\n' > "$D/f.txt"; git -C "$D" add f.txt
(cd "$D" && commitear "$T_VIEJO" "main toca f.txt")
git -C "$D" merge --no-ff lado -m merge >/dev/null 2>&1 || true
printf 'resuelto\n' > "$D/f.txt"; git -C "$D" add f.txt
(cd "$D" && commitear "$T_NUEVO" "merge que RESUELVE f.txt")

_padres="$(git -C "$D" log -1 --format=%p -- f.txt | wc -w)"
_suelta="$(git -C "$D" log -1 --format=%ct -- f.txt)"
_lote="$(git -C "$D" log --format='C%ct' --name-only |
    awk '/^C/{ts=substr($0,2);next} $0=="f.txt" && !v {print ts; v=1}')"

CASOS=$((CASOS + 1))
if [ "$_padres" -ne 2 ]; then
    echo "  FALLO (lote) el señuelo no produjo un MERGE: $_padres padre(s). Sin merge, este caso no mide nada."
    FALLOS=$((FALLOS + 1))
elif [ "$_suelta" = "$_lote" ]; then
    echo "  FALLO (lote) la pasada por lote coincidió con la llamada suelta ($_suelta)."
    echo "        Si git ha cambiado y ya NO pierde los merges, el rechazo del lote hay que RE-MEDIRLO"
    echo "        contra el árbol real antes de adoptarlo — no basta con que este señuelo coincida."
    FALLOS=$((FALLOS + 1))
else
    echo "  ok    (lote) --name-only pierde el merge: suelta=$_suelta lote=$_lote — el lote NO es equivalente"
fi
rm -rf "$D"

# ⛔⛔ (33-34) EL HEAD SE MUEVE DE VERDAD, Y CADA CASO ACREDITA UN EXTREMO DISTINTO.
#
# Historia de estos dos, porque explica su forma. Primero fueron UN caso que mutaba la lectura FINAL
# del sujeto a un literal imposible: acreditaba el COMPARADOR y no el intervalo. Luego fueron dos que
# movían un HEAD real, pero el «tardío» disparaba con el patrón GENÉRICO `log -1 --format=%ct`, que
# casa PRIMERO en el cálculo de `UI_TS` — o sea que los dos medían el mismo extremo temprano y el
# tardío no existía. Lo cazó el contraste, dos veces seguidas.
#
# Por eso ahora cada caso declara DÓNDE tiene que dispararse y la aserción lo COMPRUEBA: el shim deja
# la línea de órdenes que lo activó en `$OLV_TRAZA`, y el caso exige que esa traza contenga lo suyo.
# Un testigo que no prueba dónde disparó no puede llamarse «tardío».
mueve_head_en() { # <patrón> -> imprime el directorio del git interpuesto
    local pat="$1" d
    # ⛔ FUERA DE `/tmp`, QUE ESTÁ NOEXEC. Un shim ahí no se ejecuta y el caso saldría verde por no
    #    haber corrido nunca — el cero más caro que hay. Se comprueba, no se supone.
    d="$(mktemp -d "${TMPDIR:-/workspace/.olivares-tmptest}/shim.XXXXXX")" || return 1
    # ⛔ `case`, NO `printf … | grep -q`. Con `pipefail`, `grep -q` sale al primer casamiento, el
    #    productor muere con SIGPIPE y la tubería devuelve 141 EN EL CASO DE ÉXITO. Está fichado en
    #    este mismo árbol, lo impone `lint:sigpipe-booleans` — y aun así lo escribí aquí, en el
    #    fichero que documenta la trampa dos funciones más arriba.
    cat > "$d/git" <<SHIM
#!/usr/bin/env bash
if [ ! -e "\$OLV_MOVIDO" ]; then
    case " \$* " in
    *'$pat'*)
        : > "\$OLV_MOVIDO"
        printf '%s\n' "\$*" > "\$OLV_TRAZA"
        printf 'x\n' >> "\$OLV_REPO/mueve.txt"
        /usr/bin/git -C "\$OLV_REPO" add mueve.txt >/dev/null 2>&1
        /usr/bin/git -C "\$OLV_REPO" -c user.name=t -c user.email=t@t commit -qm 'otro carril empuja' >/dev/null 2>&1
        ;;
    esac
fi
exec /usr/bin/git "\$@"
SHIM
    chmod +x "$d/git"
    [ -x "$d/git" ] && "$d/git" --version >/dev/null 2>&1 || { rm -rf "$d"; return 1; }
    printf '%s\n' "$d"
}

caso_head_movido() { # <nombre> <patrón> <lo que la TRAZA debe contener>
    local nombre="$1" pat="$2" espera="$3" d shim antes despues out rc traza
    d="$(señuelo)"
    antes="$(git -C "$d" rev-parse HEAD)"
    if ! shim="$(mueve_head_en "$pat")"; then
        echo "test-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: no pude montar un git interpuesto" >&2
        echo "        EJECUTABLE bajo \${TMPDIR:-/workspace/.olivares-tmptest} (¿noexec?)." >&2
        rm -rf "$d"; exit 2
    fi
    out="$(OLV_REPO="$d" OLV_MOVIDO="$shim/hecho" OLV_TRAZA="$shim/traza" PATH="$shim:$PATH" \
           OLIVARES_CLONE="$d" bash "$GATE" 2>&1)"; rc=$?
    despues="$(git -C "$d" rev-parse HEAD)"
    traza="$(cat "$shim/traza" 2>/dev/null || true)"
    CASOS=$((CASOS + 1))
    if [ "$antes" = "$despues" ]; then
        echo "  FALLO $nombre: el HEAD NO se movió (${antes:0:9}); el caso no midió nada."
        echo "        Si el gate dejó de invocar «$pat», re-ancla el disparador."
        FALLOS=$((FALLOS + 1)); rm -rf "$d" "$shim"; return
    fi
    case "$traza" in
    *"$espera"*) : ;;
    *)  echo "  FALLO $nombre: disparó en el sitio EQUIVOCADO — la traza no contiene «$espera»."
        echo "        traza: ${traza:-(vacía)}"
        echo "        Un testigo que no prueba DÓNDE disparó no acredita el extremo que dice."
        FALLOS=$((FALLOS + 1)); rm -rf "$d" "$shim"; return ;;
    esac
    if [ "$rc" != "2" ]; then
        echo "  FALLO $nombre: HEAD ${antes:0:9} -> ${despues:0:9} y el gate salió $rc, esperaba 2."
        printf '%s\n' "$out" | head -3 | sed 's/^/        /'
        FALLOS=$((FALLOS + 1)); rm -rf "$d" "$shim"; return
    fi
    case "$out" in
    *"el árbol se movió durante la medida"*)
        echo "  ok    $nombre: HEAD ${antes:0:9} -> ${despues:0:9}, disparó en «$espera», rc 2 y lo NOMBRA" ;;
    *)  echo "  FALLO $nombre: rc 2 correcto pero por otra razón — no dijo «el árbol se movió»."
        printf '%s\n' "$out" | head -3 | sed 's/^/        /'
        FALLOS=$((FALLOS + 1)) ;;
    esac
    rm -rf "$d" "$shim"
}

# (33) EXTREMO TEMPRANO: al enumerar las capturas (`git ls-files`), que ocurre DESPUÉS de fijar el
#      sujeto y ANTES de donde lo fijaba la versión mala. Es donde el contraste movió el HEAD real y
#      el gate publicó un verde. Acredita el ANCLAJE.
caso_head_movido "(sujeto) HEAD movido al enumerar capturas sale 2" 'ls-files' 'ls-files'

# (34) EXTREMO TARDÍO: midiendo la edad de UNA CAPTURA concreta, ya dentro del bucle principal — o
#      sea, DESPUÉS incluso de donde la versión mala fijaba el sujeto. Acredita la RELECTURA final.
#      El disparador va atado al nombre del PNG a propósito: el patrón genérico `log -1 --format=%ct`
#      casa antes en `UI_TS` y convertía este caso en un duplicado del 33 sin que se notara.
caso_head_movido "(sujeto) HEAD movido midiendo la edad de una captura sale 2" 'vista-light.png' 'vista-light.png'

if [ "$FALLOS" -gt 0 ]; then
    echo "test-screenshot-freshness: $FALLOS caso(s) rojo(s)" >&2
    exit 1
fi
# ⛔ EL RECUENTO NO SE ESCRIBE A MANO. Decía «7 casos» mientras corrían 15: un número en prosa
#    envejece en silencio, y una batería que dice cuántos casos tiene sin contarlos es la misma
#    clase de sonda que este gate existe para no ser.
#
#    Y lleva SUELO: si alguien borra casos, la batería enrojece en vez de felicitarse con menos.
SUELO=34
if [ "$CASOS" -lt "$SUELO" ]; then
    echo "test-screenshot-freshness: ⛔ NO HE PODIDO MIRAR: corrieron $CASOS casos y el suelo es $SUELO." >&2
    echo "                                       Si la batería adelgaza a propósito, baja el suelo EN EL MISMO commit." >&2
    exit 2
fi
echo "test-screenshot-freshness: OK — $CASOS casos"
