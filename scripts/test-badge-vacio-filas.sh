#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-badge-vacio-filas.sh — la batería del trinquete.
#
# ⛔ LA ASERCIÓN QUE LA JUSTIFICA es la que separa los dos conjuntos: un montaje SIN `filas` pero
#    SIN `<EmptyState>` al lado NO cuenta. Si eso se rompe, el gate pasa de 68 a 76 y manda a
#    alguien a tocar ocho ficheros que no pueden superponer nada — y un gate que pide trabajo
#    inútil se desactiva, que es la forma en que estos mueren.
set -u

AQUI="$(cd "$(dirname "$0")" && pwd)"
GUION="$AQUI/check-badge-vacio-filas.sh"

# ⛔ TODO se ejercita sobre una COPIA del guion en un arbol de mentira con el LAYOUT POR DEFECTO
#    (`<raiz>/web/src`, `<raiz>/web/badge-vacio-filas.baseline`). Antes se inyectaban rutas por
#    variable, y esas variables eran una palanca que apagaba el gate; retiradas del guion real, la
#    bateria no las necesita: basta con poner los ficheros donde el guion los busca.
FALSO=""
GUION_COPIA=""
# ⛔ La mitad monotona se ejercita con una COPIA en un directorio SIN `.git`: asi el camino de
#    respaldo (`BADGE_ANTERIOR`) es alcanzable sin ningun override que pudiera debilitar el gate
#    real. Dentro del repositorio git siempre contesta y ese camino no existe.
GUION_COPIA=""
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pasan=0
fallan=0
ok() { pasan=$((pasan + 1)); printf 'ok   %-56s %s\n' "$1" "${2:-}"; }
malo() {
	fallan=$((fallan + 1))
	printf 'FALLO %-55s %s\n' "$1" "${2:-}"
}

# Árbol sintético: `web/src/…` porque el guion recorta la ruta contra la raíz del repo.
FALSO="$WORK/falso"
SRC="$FALSO/web/src/features"
mkdir -p "$SRC" "$FALSO/scripts"
cp "$GUION" "$FALSO/scripts/"
GUION_COPIA="$FALSO/scripts/check-badge-vacio-filas.sh"
BASE_FALSA="$FALSO/web/badge-vacio-filas.baseline"

pinta() { # <fichero> <con-filas|sin-filas> <con-vacio|sin-vacio>
	local f="$SRC/$1" filas="" vacio=""
	[ "$2" = "con-filas" ] && filas="        filas={items.length}"
	[ "$3" = "con-vacio" ] && vacio="      <EmptyState title=\"nada\" />"
	cat >"$f" <<FIN
export function V() {
  return (
    <div>
      <ListTruncationBadge
        query={q}
        label="x"
        hint="y"
$filas
      />
$vacio
    </div>
  )
}
FIN
}

# Siempre la COPIA, y las rutas por defecto: es como corre de verdad.
corre() { bash "$GUION_COPIA" >"$WORK/out.txt" 2>&1; printf '%s' "$?"; }
base() { cp "$1" "$BASE_FALSA"; }

echo "== check-badge-vacio-filas =="

# 1 · el caso que separa los conjuntos: sin `filas` pero SIN vacío → NO cuenta.
pinta a.tsx sin-filas sin-vacio
: >"$BASE_FALSA"
comprueba_rc="$(corre)"
[ "$comprueba_rc" = "0" ] && ok "sin filas y SIN <EmptyState> => no cuenta" "rc=0" || { malo "contó un montaje que no puede superponerse"; cat "$WORK/out.txt" | head -3; }

# 2 · con `filas` y con vacío → tampoco cuenta.
pinta a.tsx con-filas con-vacio
[ "$(corre)" = "0" ] && ok "con filas => no cuenta" || malo "contó uno que ya pasa filas"

# 3 · ⛔ EL CASO DE RIESGO: sin `filas` Y con vacío, y NO está en la baseline => rojo con ruta:línea
pinta a.tsx sin-filas con-vacio
[ "$(corre)" = "1" ] && ok "sin filas Y con <EmptyState> => rojo" "rc=1" || malo "el caso de riesgo NO cortó"
grep -q 'NUEVO' "$WORK/out.txt" && ok "y lo llama NUEVO" || malo "no lo marca como nuevo"
grep -qE 'a\.tsx:[0-9]+' "$WORK/out.txt" && ok "y da fichero:línea" || malo "no da la línea"
grep -q 'Remedio' "$WORK/out.txt" && ok "y el remedio literal" || malo "sin remedio"

# 4 · con la baseline al día, ese mismo árbol es verde.
printf 'web/src/features/a.tsx\t1\n' >"$BASE_FALSA"
[ "$(corre)" = "0" ] && ok "en la baseline => verde" || malo "con baseline al día salió rojo"

# 5 · ⛔ SUBIR es rojo (dos montajes donde la baseline dice uno).
cat >"$SRC/a.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
[ "$(corre)" = "1" ] && ok "subir de 1 a 2 => rojo" || malo "una subida no cortó"
grep -q 'SUBE' "$WORK/out.txt" && ok "y lo llama SUBE" || malo "no distingue subida de nuevo"

# 6 · ⛔ BAJAR sin actualizar la baseline TAMBIÉN es rojo: si no, el trinquete no aprieta nunca.
pinta a.tsx con-filas con-vacio
[ "$(corre)" = "1" ] && ok "resolver sin bajar la baseline => rojo" || malo "el trinquete no aprieta"
grep -q 'RESUELTO' "$WORK/out.txt" && ok "y dice que quite la línea" || malo "no dice cómo apretarlo"

# 8 · ⛔ UN RENOMBRADO NO PUEDE LEERSE COMO UN EMPEORAMIENTO. Mover un fichero produce un NUEVO y
#    un RESUELTO por una edición que no cambió una línea de JSX. Sigue siendo rojo —la baseline
#    tiene que casar con el árbol— pero el mensaje debe DECIRLO y dar la edición exacta, o acusa
#    de dos cosas que no pasaron y se aprende a ignorarlo.
rm -f "$SRC"/*.tsx
pinta renombrado.tsx sin-filas con-vacio
printf 'web/src/features/original.tsx\t1\n' >"$BASE_FALSA"
rc="$(corre)"
[ "$rc" = "1" ] && ok "renombrado => sigue siendo rojo" "rc=1" || malo "un renombrado no cortó"
grep -q 'RENOMBRADO o un reparto' "$WORK/out.txt" && ok "y lo NOMBRA como posible renombrado" || malo "lo acusa de empeoramiento"
grep -q 'TAMBIEN con' "$WORK/out.txt" && ok "y NO afirma que nadie añadió nada" || malo "⛔ afirma lo que no puede saber"
grep -q 'EL TOTAL NO HA CAMBIADO (1)' "$WORK/out.txt" && ok "y cita el total invariante" || malo "no dice que el total no cambió"
grep -q -- '- web/src/features/original.tsx' "$WORK/out.txt" && ok "y da la línea a quitar" || malo "no da la edición"
grep -q -- '+ web/src/features/renombrado.tsx' "$WORK/out.txt" && ok "y la línea a poner" || malo "no da la línea nueva"

# 8 bis · y un empeoramiento REAL no se disfraza de renombrado: el total sube, así que no hay aviso.
cat >"$SRC/renombrado.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
corre >/dev/null
grep -q 'RENOMBRADO o un reparto' "$WORK/out.txt" && malo "⛔ llamó renombrado a un empeoramiento real" || ok "un empeoramiento real NO se disfraza" "el total sube"

# 8 ter · ⛔⛔ EL CASO QUE ROMPE EL AVISO DE RENOMBRADO, y que mi primer negativo NO cubria: un
#     total invariante tambien sale de «uno ARREGLADO + uno NUEVO». Si el aviso afirmara que nadie
#     añadio nada, un alta real se colaria detras de una coincidencia aritmetica. El gate sigue en
#     rojo; lo que se exige aqui es que NO mienta.
rm -f "$SRC"/*.tsx
pinta C.tsx sin-filas con-vacio      # A renombrado a C
pinta B.tsx con-filas con-vacio      # B arreglado
pinta D.tsx sin-filas con-vacio      # D NUEVO — el empeoramiento escondido
printf 'web/src/features/A.tsx\t1\nweb/src/features/B.tsx\t1\n' >"$BASE_FALSA"
[ "$(corre)" = "1" ] && ok "renombrado + arreglo + alta => rojo" "rc=1" || malo "no cortó"
grep -q 'TAMBIEN con «uno arreglado + uno nuevo»' "$WORK/out.txt" && ok "y NOMBRA la otra lectura" || malo "⛔ el aviso afirma que es una mudanza"
grep -q 'NO puedo distinguirlos' "$WORK/out.txt" && ok "y admite que no distingue" || malo "no admite el limite"
grep -qE '\+ web/src/features/D\.tsx' "$WORK/out.txt" && ok "y el alta real sale en la lista" || malo "el alta no se ve"

# 9 · ⛔⛔ EL TRINQUETE DE VERDAD: no basta con casar con la baseline PRESENTE. Un commit que
#     suba el riesgo en el JSX **y** suba la baseline a la vez salia VERDE — la baseline nueva se
#     autorizaba a si misma, y «SOLO PUEDE BAJAR» era prosa en la cabecera del fichero, no un
#     control. Lo encontro the reviewer (01:12Z) con este mutante exacto. Se compara con la baseline
#     del merge-base con origin/main; aqui se inyecta por fichero porque la bateria es hermetica.
mono() { # <baseline-antes> <baseline-ahora> -> rc
	cp "$2" "$BASE_FALSA"
	BADGE_ANTERIOR="$1" bash "$GUION_COPIA" >"$WORK/m.txt" 2>&1
	printf '%s' "$?"
}
cat >"$SRC/M.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" />
      <ListTruncationBadge query={b} label="x" hint="y" />
      <EmptyState title="nada" />
  </div>)
}
FIN
rm -f "$SRC"/a.tsx "$SRC"/renombrado.tsx "$SRC"/B.tsx "$SRC"/C.tsx "$SRC"/D.tsx
printf 'web/src/features/M.tsx\t1\n' >"$WORK/antes.txt"
printf 'web/src/features/M.tsx\t2\n' >"$WORK/ahora.txt"
[ "$(mono "$WORK/antes.txt" "$WORK/ahora.txt")" = "1" ] &&
	ok "sube JSX + sube baseline => ROJO" "rc=1" || malo "⛔ la baseline se autorizó a sí misma"
grep -q 'SUBE EL RIESGO RESPECTO A LA BASE' "$WORK/m.txt" && ok "y lo dice con esas palabras" || malo "no nombra la causa"
grep -q 'web/src/features/M.tsx: 1 → 2' "$WORK/m.txt" && ok "y nombra la ruta con el salto" || malo "no da la ruta"
grep -q 'El TOTAL sube respecto a la base' "$WORK/m.txt" && ok "y también por total" || malo "no comprueba el total"

# ⛔ EL NEGATIVO: una BAJADA real sigue siendo verde, o el trinquete impediria mejorar.
cat >"$SRC/M.tsx" <<'FIN'
export function V() {
  return (<div>
      <ListTruncationBadge query={a} label="x" hint="y" filas={n} />
      <EmptyState title="nada" />
  </div>)
}
FIN
: >"$WORK/vacia.txt"
[ "$(mono "$WORK/antes.txt" "$WORK/vacia.txt")" = "0" ] &&
	ok "arreglar y vaciar la baseline => VERDE" "el trinquete deja mejorar" || { malo "una bajada real salió roja"; head -4 "$WORK/m.txt"; }

# ⛔ Y SIN forma de leer la base, se DECLARA parcial: no es verde silencioso.
cp "$WORK/vacia.txt" "$BASE_FALSA"
bash "$GUION_COPIA" >"$WORK/m2.txt" 2>&1
grep -q 'PARCIAL' "$WORK/m2.txt" && ok "sin git ni base => lo DECLARA parcial" || malo "se calló que no comprobó la mitad monótona"
grep -q 'NO esta verificado aqui' "$WORK/m2.txt" && ok "y dice QUÉ no verificó" || malo "no dice qué falta"

# 10 · ⛔⛔ LOS OVERRIDES NO PUEDEN DEBILITAR EL GATE, y en la v4 SÍ podían: la raíz se sustituía
#      ANTES de preguntar a git, así que fijar `BADGE_RAIZ=<subdir sin git>` movía la pregunta a
#      donde git no contesta y el MISMO árbol pasaba de rc 1 a rc 0 `PARCIAL`. Lo midió the reviewer.
#      El comentario prometía justo lo contrario cuatro líneas más arriba — una garantía escrita
#      donde no había control. Aquí está el control.
rm -f "$SRC"/*.tsx
pinta OV.tsx sin-filas con-vacio
: >"$BASE_FALSA"                       # baseline vacía ⇒ el montaje es NUEVO ⇒ rojo
rc_normal="$(corre)"
[ "$rc_normal" = "1" ] && ok "sin override, el árbol de prueba es rojo" "rc=1" || malo "el fixture no era rojo"
mkdir -p "$WORK/sin-git"
: >"$WORK/baseline-mentirosa"
BADGE_RAIZ="$WORK/sin-git" BADGE_SRC="$WORK/no-existe" BADGE_BASELINE="$WORK/baseline-mentirosa" \
	bash "$GUION_COPIA" >"$WORK/ov.txt" 2>&1
rc_ov="$?"
[ "$rc_ov" = "1" ] &&
	ok "las TRES variables son INERTES: el gate sigue rojo" "rc=1" ||
	malo "⛔ el override apagó el gate (rc=$rc_ov)"
# ⛔ Se busca el USO EJECUTABLE, no la palabra: el guion documenta en prosa por que se retiro
#    la palanca, y una sonda por token contaria ese comentario como si fuera codigo.
# ⛔ Y LA SONDA BUSCA LAS TRES, no la que me señalaron. Retire `BADGE_RAIZ` y deje vivas a sus
#    dos hermanas: al quitar una palanca se barre su CLASE.
_vivas="$(grep -vE '^[[:space:]]*#' "$GUION" | grep -cE 'BADGE_(RAIZ|SRC|BASELINE)' || true)"
if [ "${_vivas:-0}" -eq 0 ]; then
	ok "ninguna de las tres tiene uso ejecutable" "solo la prosa que lo explica"
else
	malo "⛔ quedan $_vivas uso(s) ejecutable(s) de BADGE_RAIZ/SRC/BASELINE"
fi
grep -q 'NUEVO' "$WORK/ov.txt" && ok "y sigue nombrando el hallazgo" || malo "el override se comió el detalle"

# 7 · «no puedo mirar» ≠ «está limpio»
mv "$FALSO/web/src" "$FALSO/web/src-guardado"
bash "$GUION_COPIA" >/dev/null 2>&1
[ "$?" = "2" ] && ok "fuente ausente => rc=2" || malo "no distingue 'no puedo mirar'"
mv "$FALSO/web/src-guardado" "$FALSO/web/src"
mv "$BASE_FALSA" "$WORK/base-guardada"
bash "$GUION_COPIA" >/dev/null 2>&1
[ "$?" = "2" ] && ok "baseline ausente => rc=2" || malo "sin baseline no dijo que no podía mirar"

echo
echo "test-badge-vacio-filas: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ]
