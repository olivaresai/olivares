#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-badge-vacio-filas.sh — trinquete de F-04: un `<ListTruncationBadge>` que convive con un
# `<EmptyState>` tiene que decir cuántas filas hay.
#
# ⛔ QUÉ CLASE CIERRA. `ListTruncationBadge` se pinta con `has_more === true && !error`. Con
#    `{items: [], has_more: true}` eso saca el aviso ENCIMA del estado vacío: «no hay nada» y
#    «cargadas 0, hay más» a la vez, un mensaje que se contradice solo. La prop `filas` lo corta,
#    pero es OPCIONAL a propósito —el componente tiene ~84 montajes y no todos pasan un sobre con
#    `items`, así que exigirla dentro apagaría avisos ajenos en silencio—. Este gate es lo que
#    convierte esa opcionalidad en una deuda que sólo puede bajar.
#
# ⛔ EL CONJUNTO NO SON «LOS QUE NO PASAN filas». Un aviso sin estado vacío al lado no puede
#    superponerse a nada. El riesgo es montar el badge SIN `filas` **y** pintar un `<EmptyState>`
#    en el mismo componente. Medido el 2026-08-30: 84 montajes, 8 con `filas`, 76 sin, y **68** en
#    el conjunto de riesgo. Confundir 76 con 68 mandaría a alguien a tocar ocho ficheros que no lo
#    necesitan.
#
# ⛔ LA BASELINE VA POR FICHERO Y CUENTA, NO POR `fichero:línea`. Un número de línea se desplaza
#    con cualquier edición de más arriba, así que una baseline anclada a líneas se vuelve roja sin
#    que nadie haya empeorado nada — y un gate que miente se desactiva. Por fichero es estable
#    frente a reordenaciones y sigue siendo exacto para lo que importa: cuántos quedan dónde.
set -u -o pipefail

# ⛔⛔ NI `BADGE_SRC` NI `BADGE_BASELINE`: eran la MISMA palanca con otro nombre. Con
#     `BADGE_BASELINE` apuntando a una copia externa, el mismo 68→70 que da rc 1 pasaba a rc 0
#     `PARCIAL` aunque git existiera — porque la ruta relativa de esa baseline no esta en el
#     merge-base y la mitad monotona se declara no comprobable. Retire `BADGE_RAIZ` por esto
#     mismo y deje dos hermanas suyas vivas: lo vio the reviewer al releer la v5.
#
#     La regla que saco: al retirar una palanca, se buscan TODAS las de su clase, no la que te
#     senalaron. Aqui las rutas salen SIEMPRE de la raiz del guion, y la bateria ejercita los
#     caminos ejecutando una COPIA sobre un arbol de mentira con el layout por defecto.
RAIZ="$(cd "$(dirname "$0")/.." && pwd)"
FUENTE="$RAIZ/web/src"
BASE="$RAIZ/web/badge-vacio-filas.baseline"

if [ ! -d "$FUENTE" ]; then
	echo "check-badge-vacio-filas: NO PUDE MIRAR: no existe $FUENTE" >&2
	exit 2
fi
if [ ! -f "$BASE" ]; then
	echo "check-badge-vacio-filas: NO PUDE MIRAR: falta la baseline $BASE" >&2
	exit 2
fi

# ⛔⛔ «SOLO PUEDE BAJAR» ERA PROSA, NO UN CONTROL — y lo era en la primera linea del propio
#     fichero de baseline. El check leia UNICAMENTE la baseline PRESENTE, asi que un commit que
#     subiera el riesgo en el JSX **y** subiera la baseline a la vez salia VERDE: la baseline nueva
#     se autorizaba a si misma. Lo encontro the reviewer (asiento 01:12Z) con el mutante exacto.
#
#     El trinquete de verdad compara con la baseline de ANTES: la del merge-base con `origin/main`.
#     Una subida se rechaza por ruta y por total AUNQUE la baseline nueva la declare.
#
# ⛔ Y SI NO HAY GIT, SE DICE. El banco ejecuta esto desde un `git archive` sin `.git`, donde el
#    merge-base no existe. Ahi la mitad monotona NO se puede comprobar, y callarselo seria vender
#    un trinquete que en ese entorno es solo un contador. Se declara PARCIAL y se sigue.
# ⛔⛔ `BADGE_RAIZ` RETIRADO, y la razon es mejor que su arreglo. La version anterior sustituia
#     la raiz ANTES de preguntar a git, asi que fijar `BADGE_RAIZ=<subdir>` movia la pregunta a
#     donde git no contesta y el MISMO arbol pasaba de rc 1 a rc 0 `PARCIAL`: el override SI
#     debilitaba el gate, justo lo contrario de lo que el comentario prometia cuatro lineas mas
#     arriba. Lo midio the reviewer.
#
#     Reordenarlo lo arreglaba, pero dejaba la palanca puesta — y una palanca que no debe usarse
#     nunca es mejor no tenerla. Se retira: la raiz es SIEMPRE la del guion, y como la bateria
#     ejecuta una COPIA desde un directorio sin `.git`, ejercita el camino de respaldo sin
#     necesitar override ninguno. `BADGE_ANTERIOR` solo se mira cuando ahi git no contesta, o sea
#     jamas dentro del repositorio.
#
#     Un comentario que promete una propiedad que el codigo no tiene es peor que no tenerla: se
#     lee como garantia y nadie vuelve a comprobarla. Tercera de esta clase esta noche, con el
#     «SOLO PUEDE BAJAR» de la baseline y la frase del renombrado.
ANTERIOR=""
MONOTONO="si"
if command -v git >/dev/null 2>&1 && git -C "$RAIZ" rev-parse --git-dir >/dev/null 2>&1; then
	_base_ref="$(git -C "$RAIZ" merge-base origin/main HEAD 2>/dev/null || true)"
	if [ -n "$_base_ref" ]; then
		_rel="${BASE#"$RAIZ"/}"
		ANTERIOR="$(git -C "$RAIZ" show "$_base_ref:$_rel" 2>/dev/null || true)"
		[ -n "$ANTERIOR" ] || MONOTONO="sin-baseline-en-la-base"
	else
		MONOTONO="sin-merge-base"
	fi
elif [ -n "${BADGE_ANTERIOR:-}" ] && [ -f "$BADGE_ANTERIOR" ]; then
	ANTERIOR="$(cat "$BADGE_ANTERIOR")"
	MONOTONO="si"
else
	MONOTONO="sin-git"
fi

ANTERIOR="$ANTERIOR" python3 - "$FUENTE" "$BASE" "$MONOTONO" <<'PY'
import pathlib, sys, collections, os

fuente, base = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
monotono = sys.argv[3]
anterior_txt = os.environ.get('ANTERIOR', '')

actual = collections.Counter()
detalle = collections.defaultdict(list)
montajes = con_filas = 0
for f in sorted(fuente.rglob('*.tsx')):
    if '.test.' in f.name:
        continue
    s = f.read_text()
    tiene_vacio = '<EmptyState' in s
    i = s.find('<ListTruncationBadge')
    while i >= 0:
        j = s.find('/>', i) + 2
        if j <= 1:
            break
        montajes += 1
        if 'filas=' in s[i:j]:
            con_filas += 1
        elif tiene_vacio:
            rel = str(f.relative_to(fuente.parent.parent))
            actual[rel] += 1
            detalle[rel].append(s[:i].count('\n') + 1)
        i = s.find('<ListTruncationBadge', j)

esperado = collections.Counter()
for linea in base.read_text().splitlines():
    linea = linea.strip()
    if not linea or linea.startswith('#'):
        continue
    ruta, _, n = linea.rpartition('\t')
    esperado[ruta] = int(n)

print(f"check-badge-vacio-filas: {montajes} montajes · {con_filas} con `filas` · "
      f"{montajes - con_filas} sin · {sum(actual.values())} en riesgo (baseline {sum(esperado.values())})")

subidas, nuevos, bajadas = [], [], []
for ruta, n in sorted(actual.items()):
    e = esperado.get(ruta)
    if e is None:
        nuevos.append((ruta, n))
    elif n > e:
        subidas.append((ruta, e, n))
    elif n < e:
        bajadas.append((ruta, e, n))
idos = [(r, e) for r, e in sorted(esperado.items()) if r not in actual]

rc = 0
for ruta, n in nuevos:
    rc = 1
    print(f"  ⛔ NUEVO: {ruta} monta {n} aviso(s) sin `filas` junto a un <EmptyState>")
    for l in detalle[ruta]:
        print(f"       {ruta}:{l}")
    print("     Remedio: pasa `filas={…length ?? 0}` al <ListTruncationBadge>, o mueve el aviso")
    print("     dentro de la rama no vacía. Con `{items: [], has_more: true}` se superponen.")
for ruta, e, n in subidas:
    rc = 1
    print(f"  ⛔ SUBE: {ruta} pasa de {e} a {n}")
    for l in detalle[ruta]:
        print(f"       {ruta}:{l}")

# ⛔ Bajar SIN actualizar la baseline tambien es rojo: si no, el trinquete no aprieta nunca y el
#    numero se queda congelado mientras el arbol mejora. El remedio es una linea.
for ruta, e, n in bajadas:
    rc = 1
    print(f"  ⛔ BAJA y la baseline no se actualizo: {ruta} {e} → {n}. Ponlo en {base.name}.")
for ruta, e in idos:
    rc = 1
    print(f"  ⛔ RESUELTO y la baseline no se actualizo: {ruta} ({e}). Quita su linea de {base.name}.")

# ⛔ UN RENOMBRADO NO ES UN EMPEORAMIENTO, Y EL MENSAJE NO PUEDE DECIR QUE SI LO ES. Mover un
#    fichero produce un NUEVO y un RESUELTO por una edicion que no cambio una sola linea de JSX, y
#    los dos mensajes acusan de algo que no paso: uno de anadir un defecto, otro de haberlo
#    arreglado. Sigue siendo rojo —la baseline TIENE que reflejar el arbol— pero el rojo dice la
#    verdad y da la edicion exacta. Un gate cuyo mensaje miente se desactiva, que es como mueren.
# ⛔⛔ Y EL AVISO NO PUEDE AFIRMAR QUE NADIE AÑADIO NADA, porque desde aqui NO SE PUEDE SABER.
#     Decia «nadie ha anadido un aviso; se ha movido de sitio» y lo probe con el caso que lo
#     rompe: baseline {A:1, B:1}; arbol con A renombrado a C, B ARREGLADO y D NUEVO. Total 2 = 2,
#     el aviso saltaba, y su frase era FALSA — habia un alta de verdad escondida detras de una
#     coincidencia aritmetica. Un total invariante es compatible con un renombrado Y con
#     «arreglo + alta», y este gate no distingue las dos: lo que puede hacer es DECIRLO.
#     Mi primer negativo («si el total sube, no se disfraza») era demasiado debil y por eso no lo
#     vio: cubria la direccion facil.
if nuevos and idos and sum(actual.values()) == sum(esperado.values()):
    print()
    print("  ⚠ EL TOTAL NO HA CAMBIADO ({}). Eso es compatible con un RENOMBRADO o un reparto de"
          .format(sum(actual.values())))
    print("    ficheros — y TAMBIEN con «uno arreglado + uno nuevo», que si es un empeoramiento.")
    print("    NO puedo distinguirlos desde aqui: mira las dos listas antes de tocar la baseline.")
    print("    Si de verdad es una mudanza, la edicion es:")
    for ruta, e in idos:
        print(f"      - {ruta}\t{e}")
    for ruta, n in nuevos:
        print(f"      + {ruta}\t{n}")

# ⛔ EL TRINQUETE DE VERDAD: contra la baseline de ANTES, no contra la del arbol.
def lee(txt):
    c = collections.Counter()
    for l in txt.splitlines():
        l = l.strip()
        if not l or l.startswith('#'):
            continue
        r, _, n = l.rpartition('\t')
        c[r] = int(n)
    return c

if monotono == 'si':
    antes = lee(anterior_txt)
    subidas_reales = [(r, antes.get(r, 0), n) for r, n in sorted(actual.items()) if n > antes.get(r, 0)]
    if subidas_reales:
        rc = 1
        print()
        print("  ⛔ SUBE EL RIESGO RESPECTO A LA BASE, y la baseline del arbol NO lo autoriza:")
        for r, a, n in subidas_reales:
            print(f"       {r}: {a} → {n}")
            for l in detalle.get(r, []):
                print(f"         {r}:{l}")
        print("     Subir la baseline en el mismo commit NO vale: se compara con la del merge-base")
        print("     con origin/main. Pasa `filas` o mueve el aviso dentro de la rama no vacia.")
    if sum(actual.values()) > sum(antes.values()):
        rc = 1
        print(f"  ⛔ El TOTAL sube respecto a la base: {sum(antes.values())} → {sum(actual.values())}")
else:
    print(f"  ⚠ PARCIAL: no he podido comparar con la baseline de la base ({monotono}).")
    print("    Lo comprobado es que el arbol casa con SU baseline; que esa baseline no haya subido")
    print("    NO esta verificado aqui. Con `.git` disponible, si lo esta.")

if rc == 0:
    print("  OK — el conjunto de riesgo casa con la baseline exactamente.")
sys.exit(rc)
PY
