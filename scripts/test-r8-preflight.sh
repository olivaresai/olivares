#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-r8-preflight.sh — la batería de r8-preflight.sh.
#
# ⛔ LA ASERCIÓN QUE JUSTIFICA LA BATERÍA ES «NO CAPTURA». Todo lo demás —disco, binario,
#    Playwright— son comprobaciones que fallan ruidosamente si me equivoco. La que puede fallar en
#    SILENCIO y arruinar la mañana del día D es que este guion, que existe para PREPARAR, acabe
#    disparando la captura: media hora de motores y navegadores contra un árbol que igual no es el
#    del corte. Se comprueba por CONDUCTA (no deja artefactos) y por FUENTE (ninguna línea
#    ejecutable lo invoca), porque las dos pueden mentir por separado.
set -u

AQUI="$(cd "$(dirname "$0")" && pwd)"
GUION="$AQUI/r8-preflight.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pasan=0
fallan=0
ok() { pasan=$((pasan + 1)); printf 'ok   %-52s %s\n' "$1" "${2:-}"; }
malo() {
	fallan=$((fallan + 1))
	printf 'FALLO %-51s %s\n' "$1" "${2:-}"
}
comprueba() { if [ "$2" = "$3" ]; then ok "$1" "rc=$3"; else malo "$1" "esperaba $2, dio $3"; fi; }

corre() { # <args...> -> rc, salida en $WORK/out.txt
	R8_DIR="$WORK/r8" bash "$GUION" "$@" >"$WORK/out.txt" 2>&1
	printf '%s' "$?"
}

echo "== r8-preflight =="

# 1 · sin argumento no adivina: pide el SHA
comprueba "sin <sha> => 2 y dice el uso" 2 "$(corre)"
grep -q 'uso:' "$WORK/out.txt" && ok "y nombra el uso" || malo "no nombra el uso"

# 2 · ⛔ UN REF QUE NO EXISTE ES «NO HE PODIDO MIRAR» (2), NO «falta algo» (1).
#    La diferencia manda: un 1 dice «prepara esto» y un 2 dice «tu entrada está mal». Confundirlos
#    haría que alguien preparase un árbol para un corte inexistente.
comprueba "ref inexistente => 2, no 1" 2 "$(corre 'no-existe-este-ref-jamas')"

# 3 · con un ref real pero sin worktree preparado: falta algo (1), y NOMBRA qué
rc="$(corre HEAD)"
comprueba "ref real sin preparar => 1" 1 "$rc"
grep -q 'worktree' "$WORK/out.txt" && ok "y nombra el worktree que falta" || malo "no nombra el worktree"
grep -q 'bin/olivares' "$WORK/out.txt" && ok "y nombra el binario que falta" || malo "no nombra el binario"

# 4 · ⛔ NO CAPTURA — POR CONDUCTA. Ni crea el directorio de salida ni deja un solo PNG.
if [ -e "$WORK/r8" ]; then
	malo "no toca el worktree que no existe" "creó $WORK/r8"
else
	ok "no crea el worktree por su cuenta" "lo NOMBRA, no lo hace"
fi
if find "$WORK" -name '*.png' -o -name 'manifest.json' 2>/dev/null | grep -q .; then
	malo "NO CAPTURA (conducta)" "aparecieron artefactos de captura"
else
	ok "NO CAPTURA (conducta)" "cero PNG, cero manifest"
fi

# 5 · ⛔ NO CAPTURA — POR FUENTE. Ninguna línea EJECUTABLE invoca docs-captures.sh: sólo aparece
#    dentro de un `echo` (la instrucción que se le da al lector) o de un comentario. Se comprueba
#    aparte de la conducta porque hoy no captura por falta de precondiciones, y el día que estén
#    todas cumplidas la conducta ya no distinguiría.
inv="$(grep -nE 'docs-captures\.sh' "$GUION" | grep -vE '^\s*[0-9]+:\s*#' | grep -vE 'echo' | grep -vcE '^\s*[0-9]+:#' || true)"
if [ "${inv:-0}" -eq 0 ]; then
	ok "NO CAPTURA (fuente)" "ninguna línea ejecutable lo invoca"
else
	malo "NO CAPTURA (fuente)" "$inv línea(s) lo invocan"
fi

# 5 bis · ⛔ EL WORKTREE, DESPRENDIDO — y se comprueba en las DOS direcciones, porque un control
#    que sólo ve el caso bueno no distingue nada. La comparación `HEAD == SHA` da IDÉNTICO para un
#    worktree desprendido y para uno EN UNA RAMA a esa misma altura, y son estados distintos: la
#    corrida de capturas ensucia el bundle, así que el que tiene rama se lleva un push muerto en
#    `web-bundle-freshness`. Los dos worktrees se crean con `--no-checkout`: no hace falta el árbol
#    para preguntar por HEAD, y así la batería sigue costando segundos.
_raiz="$(cd "$AQUI/.." && pwd)"
_sha="$(git -C "$_raiz" rev-parse HEAD)"

git -C "$_raiz" worktree add -q --detach --no-checkout "$WORK/wt-desprendido" "$_sha" 2>/dev/null
R8_DIR="$WORK/wt-desprendido" bash "$GUION" "$_sha" >"$WORK/o6.txt" 2>&1
grep -qE 'OK.*worktree en el corte.*desprendido' "$WORK/o6.txt" &&
	ok "worktree desprendido => verde" || malo "el desprendido no salió verde"

git -C "$_raiz" worktree add -q --no-checkout -b tmp-bateria-desprendido "$WORK/wt-conrama" "$_sha" 2>/dev/null
R8_DIR="$WORK/wt-conrama" bash "$GUION" "$_sha" >"$WORK/o7.txt" 2>&1
grep -qE 'FALTA.*worktree desprendido' "$WORK/o7.txt" &&
	ok "worktree CON RAMA => rojo" || malo "el que tiene rama NO salió rojo"
grep -q 'tmp-bateria-desprendido' "$WORK/o7.txt" &&
	ok "y nombra la rama culpable" || malo "no nombra la rama"
grep -q 'checkout --detach' "$WORK/o7.txt" &&
	ok "y da el remedio literal" || malo "no da el remedio"

git -C "$_raiz" worktree remove --force "$WORK/wt-desprendido" 2>/dev/null || true
git -C "$_raiz" worktree remove --force "$WORK/wt-conrama" 2>/dev/null || true
git -C "$_raiz" branch -qD tmp-bateria-desprendido 2>/dev/null || true

# 6 · TMPDIR: el control en las DOS direcciones. Un `noexec` tiene que salir rojo, y uno normal
#    verde — si sólo probara el verde no distinguiría la comprobación de una que siempre pasa.
# ⛔ EL «TMPDIR NORMAL» NO PUEDE SALIR DE `$WORK`. `$WORK` es un `mktemp -d`, o sea que hereda el
#    TMPDIR de QUIEN LLAMA — y en estas cajas eso suele ser `/tmp`, que está montado `noexec`. Con
#    esa herencia, el caso «normal» era INALCANZABLE: la batería salía roja en la única dirección
#    que descarta que la comprobación siempre pase, y lo hacía por el entorno del que la corría,
#    no por el guion. Un caso que no puede llegar a verde no mide nada. Se ancla, entonces, a un
#    directorio de un sistema con exec, junto al repo.
_exec_padre="$(cd "$AQUI/../.." && pwd)"
_tmpok="$(mktemp -d "$_exec_padre/.bateria-tmpok-XXXXXX" 2>/dev/null || true)"
if [ -z "$_tmpok" ]; then
	malo "TMPDIR normal" "NO PUEDO MIRAR: no pude crear un directorio en $_exec_padre"
elif ! printf '#!/bin/sh\nexit 0\n' >"$_tmpok/p" || ! chmod +x "$_tmpok/p" || ! "$_tmpok/p"; then
	# La premisa del caso es falsa aquí: decirlo, no dictaminar.
	ok "TMPDIR normal" "OMITIDO: $_exec_padre tampoco permite ejecutar"
else
	TMPDIR="$_tmpok" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o1.txt" 2>&1
	grep -qE 'OK.*TMPDIR ejecutable' "$WORK/o1.txt" && ok "TMPDIR normal => verde" || malo "TMPDIR normal salió rojo"
fi
rm -rf "$_tmpok"
# ⛔ NO ESCRIBIBLE ES «NO HE PODIDO MIRAR» (rc 2), NO «falta algo» (rc 1). Son acciones
#    distintas: un `FALTA` manda a preparar el árbol —que puede estar perfecto— y un «no sé»
#    manda a arreglar el ENTORNO. Confundirlos hace perder el tiempo en el sitio equivocado, y
#    es la regla que este mismo guion aplica al `<sha>` inexistente desde el primer día: aquí
#    faltaba. Lo midió the reviewer.
TMPDIR="/proc/no-escribible" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o2.txt" 2>&1
rc_tmp="$?"
grep -qE 'NO SE.*TMPDIR ejecutable' "$WORK/o2.txt" && ok "TMPDIR no escribible => «NO SE»" || malo "no lo marcó como no-mirado"
[ "$rc_tmp" = "2" ] && ok "y el guion sale 2, no 1" "rc=2" || malo "salió rc=$rc_tmp, no 2"
grep -q 'NO HE PODIDO HACER' "$WORK/o2.txt" && ok "y lo dice en el resumen" || malo "el resumen no lo distingue"

# 6 bis · ⛔ LOS RESIDUOS DE `task build`, en las dos direcciones. `task build` compila trece
#    conectores en `cmd/olivares/firstparty/bins/` y el censo C03-41 exige que ahi SOLO este
#    PLACEHOLDER; mide el DISCO, asi que ficheros sin trackear lo rompen igual. Como el pre-vuelo
#    manda construir el binario, quien prepare el arbol se queda con los residuos y su siguiente
#    push muere sin relacion aparente. Lo reporto N tras perder un push, y me alcanzo con el mio
#    ya en vuelo.
mkdir -p "$WORK/r8/cmd/olivares/firstparty/bins"
: >"$WORK/r8/cmd/olivares/firstparty/bins/PLACEHOLDER"
R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o4.txt" 2>&1
grep -qE 'OK.*sin residuos de build' "$WORK/o4.txt" && ok "bins/ solo con PLACEHOLDER => verde" || malo "bins/ limpio salio rojo"

: >"$WORK/r8/cmd/olivares/firstparty/bins/kafka-source"
R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o5.txt" 2>&1
grep -qE 'FALTA.*residuos de build' "$WORK/o5.txt" && ok "un residuo => rojo" || malo "el residuo no salio rojo"
grep -q 'PLACEHOLDER -delete' "$WORK/o5.txt" && ok "y da el remedio literal" || malo "no da el remedio"
rm -rf "$WORK/r8"

# 6 ter · ⛔ LAS SEIS TOMAS DE ESTADO INTERNO — precondición 0 del candidato 2 (r4, 2026-08-29).
#    En las dos direcciones, porque el fallo que esto ataja es SILENCIOSO: un árbol sin ellas
#    captura "45 de 45" y sale verde. La spec sintética basta: lo que se comprueba es el
#    predicado, no la spec real, y así la batería no depende de qué haya en el árbol.
_specdir="$WORK/spec/web/e2e"
mkdir -p "$_specdir"
{
	echo "  VIEWS = ["
	for _v in work-decisions work-decisions-paginated work-apply-refused templates-readonly workflow-runs-error list-truncated; do
		printf "    id: '%s',\n" "$_v"
	done
	echo "  ]"
} >"$_specdir/docs-captures.spec.ts"
R8_DIR="$WORK/spec" bash "$GUION" HEAD >"$WORK/o8.txt" 2>&1
grep -qE 'OK.*tomas de estado interno' "$WORK/o8.txt" && ok "las 6 tomas presentes => verde" || malo "con las 6 salió rojo"

# y ahora quitando UNA sola: el control tiene que cortar y NOMBRARLA.
grep -v "list-truncated" "$_specdir/docs-captures.spec.ts" >"$_specdir/x" && mv "$_specdir/x" "$_specdir/docs-captures.spec.ts"
R8_DIR="$WORK/spec" bash "$GUION" HEAD >"$WORK/o9.txt" 2>&1
grep -qE 'FALTA.*tomas de estado interno' "$WORK/o9.txt" && ok "falta UNA => rojo" || malo "faltando una no cortó"
grep -q 'faltan: list-truncated' "$WORK/o9.txt" && ok "y nombra cuál falta" || malo "no nombra la que falta"
grep -q '2134' "$WORK/o9.txt" && ok "y dice qué PR la trae" || malo "no dice de dónde sale"

# ⛔ Y LA CIFRA VA CON SU SONDA, que ya ha estado mal DOS veces sobre el mismo fichero:
#    `^    id:` (4 espacios) daba 45, `^\s+id:` daba 51 y los ids DISTINTOS son 71 — las dos
#    primeras sólo ven las entradas escritas como objeto multilínea y se dejan fuera las de una
#    sola línea (`{ id: 'killswitch', path: '/killswitch', … },`). Veinte entradas invisibles.
#    Que la cifra viaje con el nombre de su sonda es lo que permitió verlo.
grep -q "sonda: id:'" "$WORK/o8.txt" && ok "la cifra nombra su sonda" || malo "la cifra va sin sonda"

# 6 quater · ⛔ LA PUERTA DE LA CAJA (#112-bis), en sus CUATRO ramas. La que de verdad importa es
#    la tercera: una sonda RANCIA no puede leerse como «abierta». Una sonda vieja no es una
#    lectura, es una foto de otro momento, y el fallo seria silencioso —la corrida arrancaria
#    contra una caja saturada y las esperas venceran, que es como murieron cuatro mediciones ya
#    documentadas en la spec.
_pf="$WORK/puerta.estado"

echo "PUERTA=ABIERTA HORA=00:00:00Z margen=9000M load1=1.20 umbral=8 cuota=4" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p1.txt" 2>&1
grep -qE 'OK.*puerta de la caja.*ABIERTA' "$WORK/p1.txt" && ok "puerta ABIERTA => verde" || malo "abierta no salió verde"

echo "PUERTA=CERRADA HORA=00:00:00Z margen=800M load1=43.9 umbral=8 cuota=4" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p2.txt" 2>&1
grep -qE 'FALTA.*puerta de la caja.*CERRADA' "$WORK/p2.txt" && ok "puerta CERRADA => rojo" || malo "cerrada no cortó"
grep -q 'load1=43.9' "$WORK/p2.txt" && ok "y cita la medida que la cerró" || malo "no cita la medida"

# ⛔ RANCIA: se toca la fecha a dos minutos atrás. NO puede salir «ABIERTA» aunque el fichero lo diga.
echo "PUERTA=ABIERTA HORA=00:00:00Z margen=9000M load1=1.20" >"$_pf"
touch -d '2 minutes ago' "$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p3.txt" 2>&1
grep -qE 'puerta de la caja.*ABIERTA \(sonda' "$WORK/p3.txt" && malo "⛔ creyó una sonda de 2 minutos" || ok "sonda rancia => NO se cree" "cae a medir aquí"
grep -qE 'puerta de la caja.*(load1|cuota)' "$WORK/p3.txt" && ok "y mide la carga por su cuenta" || malo "no midió nada al caer"
grep -q 'PARCIAL' "$WORK/p3.txt" && ok "y declara que la lectura es PARCIAL" || malo "no dice que le faltan swap y throttle"

# ilegible: ni verde ni silencio
echo "basura sin PUERTA" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p4.txt" 2>&1
grep -qE 'FALTA.*puerta.*no puedo mirar' "$WORK/p4.txt" && ok "sonda ilegible => 'no puedo mirar'" || malo "la basura no cortó"

# ⛔ Y LA CUOTA SE LEE, NO SE SUPONE: el umbral es 2x la cuota del cgroup (4 aquí => 8), no 2x los
#    nucleos visibles (16 => 32), que dejaría pasar una caja el doble de saturada.
grep -q 'cuota \* 2\|_cuota \* 2\|_umbral=\$((_cuota \* 2))' "$GUION" && ok "el umbral es 2x la CUOTA" || malo "el umbral no sale de la cuota"
grep -q 'cgroup/cpu.max' "$GUION" && ok "y la cuota se lee del cgroup" || malo "no lee cpu.max"

# 7 · el umbral de disco es un umbral, no un adorno: con uno imposible, rojo.
R8_MIN_DISCO_G=999999 R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o3.txt" 2>&1
grep -qE 'FALTA.*disco' "$WORK/o3.txt" && ok "umbral de disco corta" || malo "el umbral de disco no corta"

echo
echo "test-r8-preflight: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ]
