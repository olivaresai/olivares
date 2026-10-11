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

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

AQUI="$(cd "$(dirname "$0")" && pwd)"
GUION="$AQUI/r8-preflight.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass_count=0
fail_count=0
ok() { pass_count=$((pass_count + 1)); printf 'ok   %-52s %s\n' "$1" "${2:-}"; }
malo() {
	fail_count=$((fail_count + 1))
	printf 'FAIL %-51s %s\n' "$1" "${2:-}"
}
comprueba() { if [ "$2" = "$3" ]; then ok "$1" "rc=$3"; else malo "$1" "expected $2, got $3"; fi; }

corre() { # <args...> -> rc, salida en $WORK/out.txt
	R8_DIR="$WORK/r8" bash "$GUION" "$@" >"$WORK/out.txt" 2>&1
	printf '%s' "$?"
}

echo "== r8-preflight =="

# 1 · sin argumento no adivina: pide el SHA
comprueba "without <sha> => 2 and reports usage" 2 "$(corre)"
grep -q 'usage:' "$WORK/out.txt" && ok "and reports usage" || malo "does not report usage"

# 2 · ⛔ UN REF QUE NO EXISTE ES «NO HE PODIDO MIRAR» (2), NO «falta algo» (1).
#    La diferencia manda: un 1 dice «prepara esto» y un 2 dice «tu entrada está mal». Confundirlos
#    haría que alguien preparase un árbol para un corte inexistente.
comprueba "missing ref => 2, not 1" 2 "$(corre 'no-existe-este-ref-jamas')"

# 3 · con un ref real pero sin worktree preparado: falta algo (1), y NOMBRA qué
rc="$(corre HEAD)"
comprueba "unprepared real ref => 1" 1 "$rc"
grep -q 'worktree' "$WORK/out.txt" && ok "and names the missing worktree" || malo "does not name the worktree"
grep -q 'bin/olivares' "$WORK/out.txt" && ok "and names the missing binary" || malo "does not name the binary"

# 4 · ⛔ NO CAPTURA — POR CONDUCTA. Ni crea el directorio de salida ni deja un solo PNG.
if [ -e "$WORK/r8" ]; then
	malo "does not touch the nonexistent worktree" "created $WORK/r8"
else
	ok "does not create the worktree itself" "NAMES it without creating it"
fi
if find "$WORK" -name '*.png' -o -name 'manifest.json' 2>/dev/null | grep -q .; then
	malo "DOES NOT CAPTURE (behavior)" "capture artifacts appeared"
else
	ok "DOES NOT CAPTURE (behavior)" "zero PNGs, zero manifests"
fi

# 5 · ⛔ NO CAPTURA — POR FUENTE. Ninguna línea EJECUTABLE invoca docs-captures.sh: sólo aparece
#    dentro de un `echo` (la instrucción que se le da al lector) o de un comentario. Se comprueba
#    aparte de la conducta porque hoy no captura por falta de precondiciones, y el día que estén
#    todas cumplidas la conducta ya no distinguiría.
inv="$(grep -nE 'docs-captures\.sh' "$GUION" | grep -vE '^\s*[0-9]+:\s*#' | grep -vE 'echo' | grep -vcE '^\s*[0-9]+:#' || true)"
if [ "${inv:-0}" -eq 0 ]; then
	ok "DOES NOT CAPTURE (source)" "no executable line invokes it"
else
	malo "DOES NOT CAPTURE (source)" "$inv line(s) invoke it"
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
grep -qE 'OK.*worktree at revision.*detached' "$WORK/o6.txt" &&
	ok "detached worktree => green" || malo "detached worktree did not pass"

git -C "$_raiz" worktree add -q --no-checkout -b tmp-bateria-desprendido "$WORK/wt-conrama" "$_sha" 2>/dev/null
R8_DIR="$WORK/wt-conrama" bash "$GUION" "$_sha" >"$WORK/o7.txt" 2>&1
grep -qE 'FAIL.*detached worktree' "$WORK/o7.txt" &&
	ok "worktree WITH BRANCH => red" || malo "worktree with a branch did NOT fail"
grep -q 'tmp-bateria-desprendido' "$WORK/o7.txt" &&
	ok "and names the offending branch" || malo "does not name the branch"
grep -q 'checkout --detach' "$WORK/o7.txt" &&
	ok "and gives the exact remedy" || malo "does not give the remedy"

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
	malo "TMPDIR normal" "CANNOT INSPECT: could not create a directory in $_exec_padre"
elif ! printf '#!/bin/sh\nexit 0\n' >"$_tmpok/p" || ! chmod +x "$_tmpok/p" || ! "$_tmpok/p"; then
	# La premisa del caso es falsa aquí: decirlo, no dictaminar.
	ok "TMPDIR normal" "SKIPPED: $_exec_padre also forbids execution"
else
	TMPDIR="$_tmpok" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o1.txt" 2>&1
	grep -qE 'OK.*executable TMPDIR' "$WORK/o1.txt" && ok "normal TMPDIR => green" || malo "normal TMPDIR failed"
fi
rm -rf "$_tmpok"
# An unwritable tree returns could-not-check rc 2, not missing-content rc 1.
# The latter directs callers to prepare potentially correct files; the former asks
# them to fix the environment. This matches the existing nonexistent-SHA rule;
# the reviewer measured the missing unwritable case.
TMPDIR="/proc/no-escribible" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o2.txt" 2>&1
rc_tmp="$?"
grep -qE 'UNKNOWN.*executable TMPDIR' "$WORK/o2.txt" && ok "unwritable TMPDIR => «UNKNOWN»" || malo "did not mark it as uninspected"
[ "$rc_tmp" = "2" ] && ok "and the script exits 2, not 1" "rc=2" || malo "returned rc=$rc_tmp, not 2"
grep -q 'COULD NOT RUN' "$WORK/o2.txt" && ok "and reports it in the summary" || malo "the summary does not distinguish it"

# 6-bis · Task build residue in both directions: thirteen ignored connectors in
# cmd/olivares/firstparty/bins. a repository gate moved the census to the index and cleaned
# domain-test residue. Keep this preflight hygiene guard to avoid retaining
# ~249 MB in the capture tree after building the binary.
mkdir -p "$WORK/r8/cmd/olivares/firstparty/bins"
: >"$WORK/r8/cmd/olivares/firstparty/bins/PLACEHOLDER"
R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o4.txt" 2>&1
grep -qE 'OK.*no build residue' "$WORK/o4.txt" && ok "bins/ with only PLACEHOLDER => green" || malo "clean bins/ failed"

: >"$WORK/r8/cmd/olivares/firstparty/bins/kafka-source"
R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o5.txt" 2>&1
grep -qE 'FAIL.*build residue' "$WORK/o5.txt" && ok "one leftover => red" || malo "the leftover did not fail"
grep -q 'PLACEHOLDER -delete' "$WORK/o5.txt" && ok "and gives the exact remedy" || malo "does not give the remedy"
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
grep -qE 'OK.*internal-state captures' "$WORK/o8.txt" && ok "all 6 shots present => green" || malo "with all 6 shots, failed"

# y ahora quitando UNA sola: el control tiene que cortar y NOMBRARLA.
grep -v "list-truncated" "$_specdir/docs-captures.spec.ts" >"$_specdir/x" && mv "$_specdir/x" "$_specdir/docs-captures.spec.ts"
R8_DIR="$WORK/spec" bash "$GUION" HEAD >"$WORK/o9.txt" 2>&1
grep -qE 'FAIL.*internal-state captures' "$WORK/o9.txt" && ok "ONE missing => red" || malo "one missing did not block"
grep -q 'missing: list-truncated' "$WORK/o9.txt" && ok "and names the missing shot" || malo "does not name the missing shot"
grep -q '2134' "$WORK/o9.txt" && ok "and reports which PR provides it" || malo "does not report its source"

# ⛔ Y LA CIFRA VA CON SU SONDA, que ya ha estado mal DOS veces sobre el mismo fichero:
#    `^    id:` (4 espacios) daba 45, `^\s+id:` daba 51 y los ids DISTINTOS son 71 — las dos
#    primeras sólo ven las entradas escritas como objeto multilínea y se dejan fuera las de una
#    sola línea (`{ id: 'killswitch', path: '/killswitch', … },`). Veinte entradas invisibles.
#    Que la cifra viaje con el nombre de su sonda es lo que permitió verlo.
grep -q "probe: id:'" "$WORK/o8.txt" && ok "the figure names its probe" || malo "the figure lacks a probe"

# 6 quater · ⛔ LA PUERTA DE LA CAJA (#112-bis), en sus CUATRO ramas. La que de verdad importa es
#    la tercera: una sonda RANCIA no puede leerse como «abierta». Una sonda vieja no es una
#    lectura, es una foto de otro momento, y el fallo seria silencioso —la corrida arrancaria
#    contra una caja saturada y las esperas venceran, que es como murieron cuatro mediciones ya
#    documentadas en la spec.
_pf="$WORK/puerta.estado"

echo "PUERTA=ABIERTA HORA=00:00:00Z margen=9000M load1=1.20 umbral=8 cuota=4" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p1.txt" 2>&1
grep -qE 'OK.*host gate.*OPEN' "$WORK/p1.txt" && ok "OPEN gate => green" || malo "open gate did not pass"

echo "PUERTA=CERRADA HORA=00:00:00Z margen=800M load1=43.9 umbral=8 cuota=4" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p2.txt" 2>&1
grep -qE 'FAIL.*host gate.*CLOSED' "$WORK/p2.txt" && ok "CLOSED gate => red" || malo "closed gate did not block"
grep -q 'load1=43.9' "$WORK/p2.txt" && ok "and cites the measurement that closed it" || malo "does not cite the measurement"

# ⛔ RANCIA: se toca la fecha a dos minutos atrás. NO puede salir «ABIERTA» aunque el fichero lo diga.
echo "PUERTA=ABIERTA HORA=00:00:00Z margen=9000M load1=1.20" >"$_pf"
touch -d '2 minutes ago' "$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p3.txt" 2>&1
grep -qE 'host gate.*OPEN \(probe' "$WORK/p3.txt" && malo "⛔ trusted a 2-minute-old probe" || ok "stale probe => NOT trusted" "falls back to local measurement"
grep -qE 'host gate.*(load1|quota)' "$WORK/p3.txt" && ok "and measures load itself" || malo "measured nothing on fallback"
grep -q 'PARTIAL' "$WORK/p3.txt" && ok "and states that the reading is PARTIAL" || malo "does not report missing swap and throttle"

# ilegible: ni verde ni silencio
echo "basura sin PUERTA" >"$_pf"
R8_PUERTA="$_pf" R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/p4.txt" 2>&1
grep -qE 'FAIL.*gate.*could not look' "$WORK/p4.txt" && ok "unreadable probe => 'cannot inspect'" || malo "garbage did not block"

# ⛔ Y LA CUOTA SE LEE, NO SE SUPONE: el umbral es 2x la cuota del cgroup (4 aquí => 8), no 2x los
#    nucleos visibles (16 => 32), que dejaría pasar una caja el doble de saturada.
grep -q 'cuota \* 2\|_cuota \* 2\|_umbral=\$((_cuota \* 2))' "$GUION" && ok "the threshold is 2x the QUOTA" || malo "the threshold is not derived from the quota"
grep -q 'cgroup/cpu.max' "$GUION" && ok "and the quota is read from the cgroup" || malo "does not read cpu.max"

# 7 · el umbral de disco es un umbral, no un adorno: con uno imposible, rojo.
R8_MIN_DISCO_G=999999 R8_DIR="$WORK/r8" bash "$GUION" HEAD >"$WORK/o3.txt" 2>&1
grep -qE 'FAIL.*disk' "$WORK/o3.txt" && ok "disk threshold blocks" || malo "disk threshold does not block"

echo
echo "test-r8-preflight: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
