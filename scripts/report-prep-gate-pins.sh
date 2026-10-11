#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# report-prep-gate-pins.sh — qué PRs abiertas ENROJECERÍAN un gate de preparación al aterrizar.
#
# ⛔ POR QUÉ EXISTE ESTE FICHERO Y NO UN MENSAJE CON LA LISTA.
# El 2026-08-21 se midió que 32 de 56 PRs abiertas enrojecen un `check-*-prep.sh` al aterrizar, y
# se publicó en los buzones. **Esa lista caduca en cada aterrizaje**, y no despacio: un lote puede
# traer A LA VEZ un arreglo y el gate que lo prohíbe — pasó ese mismo día y dejó `main` en rojo con
# un gate que NO EXISTÍA cuando se midió. Una adjudicación contra un mensaje de ayer adjudica un
# árbol que ya no está. ⇒ la lista se RE-DERIVA al sentarse a usarla; por eso es un guion.
#
# ⛔ Y SE MIDE EJECUTANDO, NO LEYENDO. Que una PR TOQUE el fichero que un gate vigila no es que
# DISPARE su aserción: de 33 que lo tocaban, 32 disparaban. La medida pone el blob de la PR en su
# ruta (`git checkout <ref> -- <ruta>`), CORRE el gate y restaura. Sin eso, esto sería un `grep` con
# ínfulas.
#
# ⛔ TRES DEFECTOS DE SONDA QUE ESTE GUION EVITA A PROPÓSITO, y los tres se pagaron ese día:
#
#   1. LA CLASE DEL NOMBRE DE VARIABLE LLEVA DÍGITOS. Las rutas se derivan de asignaciones
#      `VAR="${OLIVARES_...:-<ruta>}"`, y con `[A-Z_]+` se pierde toda variable como
#      `OLIVARES_ALC01S3_WIRE`. Con esa clase corta el bloqueo figuraba como 15 PRs; con
#      `[A-Z0-9_]+` son 32. **Un suelo publicado como recuento.**
#   2. HAY DOS POLARIDADES Y SUMARLAS INFLA. Los mismos guiones llevan `fail "… lost …"` —que salta
#      al RETIRAR algo— junto a `fail "… landed — this HOLD lote does not apply …"`, que salta al
#      ATERRIZAR. **Sólo la segunda bloquea un aterrizaje.** Contando las dos salían 31.
#   3. SIN LÍNEA BASE, «N rojos» no significa nada. Si un gate ya está rojo sobre el árbol limpio,
#      su rojo con la PR puesta no acusa a la PR. La base se mide SIEMPRE y se imprime.
#
# ⚠ LO QUE ESTE INFORME NO PUEDE VER, dicho aquí y no en una nota al pie: cada ruta vigilada se
# prueba AISLADA, no el árbol fusionado entero. Una PR puede traer en su mismo lote la corrección de
# la premisa del gate. Lo que se afirma es **«enrojecería si aterriza tal cual»**, nunca «es
# imposible de aterrizar». Quien planifique con esto planifica con una horquilla, no con un techo.
#
# SALIDAS: 0 = ninguna PR clavada · 1 = hay PRs clavadas · 2 = NO HE PODIDO MIRAR.
# La tercera no es cosmética: sin `gh`, sin red o con la base rota, «cero clavadas» sería un verde
# falso sobre el mayor cuello de botella de la cola.
#
# USO
#   scripts/report-prep-gate-pins.sh              # todas las PRs abiertas
#   scripts/report-prep-gate-pins.sh 995 1023     # sólo esas
#   OLIVARES_PINS_NO_NET=1 …                      # sólo el censo de gates y la línea base
set -euo pipefail
export LC_ALL=C
export GIT_OPTIONAL_LOCKS=0

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "report-prep-gate-pins: ⛔ COULD NOT LOOK: the root is inaccessible." >&2; exit 2; }

cannot_check() { printf 'report-prep-gate-pins: ⛔ COULD NOT LOOK: %s\n' "$1" >&2; exit 2; }

command -v git >/dev/null 2>&1 || cannot_check "git is unavailable"
git rev-parse --git-dir >/dev/null 2>&1 || cannot_check "this is not a Git repository"

# ── FASE 1 — censo derivado. Nada de listas escritas a mano: envejecen hacia el lado ciego.
declare -a GATE_FILE=() GATE_NAME=() GATE_PATH=()
n_prep=0; n_frozen=0; n_block=0
for f in scripts/check-*-prep.sh; do
	[ -f "$f" ] || continue
	n_prep=$((n_prep + 1))
	grep -qE 'does not apply|not landed|lost HOLD|must stay' "$f" 2>/dev/null || continue
	n_frozen=$((n_frozen + 1))
	# POLARIDAD: sólo «no debe haber aterrizado» puede bloquear un aterrizaje.
	grep -qE 'does not apply' "$f" 2>/dev/null || continue
	# RUTAS: la clase LLEVA DÍGITOS. Sin ellos esto mide la mitad y no se queja.
	rutas=$(grep -oE '^[A-Z0-9_]+="[$][{][A-Z0-9_]+:-[^"}]+[}]"' "$f" 2>/dev/null |
		sed -E 's/.*:-//; s/[}]"$//' | grep -E '\.(go|ts|tsx|sql)$' || true)
	[ -n "$rutas" ] || continue
	n_block=$((n_block + 1))
	nombre=$(basename "$f" .sh)
	while IFS= read -r r; do
		[ -n "$r" ] || continue
		GATE_FILE+=("$f"); GATE_NAME+=("${nombre#check-}"); GATE_PATH+=("$r")
	done <<<"$rutas"
done

[ "$n_prep" -gt 0 ] || cannot_check "no scripts/check-*-prep.sh found — the census cannot be empty"
[ "${#GATE_FILE[@]}" -gt 0 ] && : || cannot_check "no gates remain after filtering: the derivation is broken, not the tree"

printf 'report-prep-gate-pins: %s *-prep script(s) · %s with frozen assertions · %s anchor CODE with MUST-NOT-LAND\n' \
	"$n_prep" "$n_frozen" "$n_block"
printf '                       %s gate→watched-path pair(s)\n' "${#GATE_FILE[@]}"

# ── FASE 2 — línea base. Un gate ya rojo no puede acusar a nadie.
declare -A BASE_ROJO=()
rojos_base=0
i=0
while [ "$i" -lt "${#GATE_FILE[@]}" ]; do
	f="${GATE_FILE[$i]}"
	if [ -z "${BASE_ROJO[$f]+x}" ]; then
		if timeout 120 bash "$f" >/dev/null 2>&1; then BASE_ROJO["$f"]=0; else
			BASE_ROJO["$f"]=1; rojos_base=$((rojos_base + 1))
			printf '  ⛔ ALREADY FAILING on the clean tree: %s — failure with a PR applied does NOT implicate that PR\n' "${GATE_NAME[$i]}"
		fi
	fi
	i=$((i + 1))
done
printf 'report-prep-gate-pins: baseline — %s failing gate(s) on the current tree\n' "$rojos_base"

if [ "${OLIVARES_PINS_NO_NET:-0}" = "1" ]; then
	echo "report-prep-gate-pins: OLIVARES_PINS_NO_NET=1 — census and baseline only; the queue is not queried."
	exit 0
fi

# ── FASE 3 — qué PR clava qué gate. Se MIDE, no se infiere.
if [ "$#" -gt 0 ]; then
	PRS=("$@")
else
	command -v gh >/dev/null 2>&1 || cannot_check "gh is unavailable and no PR was passed as an argument"
	mapfile -t PRS < <(gh pr list --state open --limit 300 --json number --jq '.[].number' 2>/dev/null || true)
	[ "${#PRS[@]}" -gt 0 ] || cannot_check "gh returned no open PRs — an empty queue cannot be distinguished from a failed query"
fi
printf 'report-prep-gate-pins: %s PR(s) to measure\n\n' "${#PRS[@]}"

sucio=$(git status --porcelain | grep -c . || true)
[ "${sucio:-0}" -eq 0 ] || cannot_check "the working tree is NOT clean ($sucio entry/entries): this measurement WRITES and restores files, so it cannot run over uncommitted work"

clavadas=0
for n in "${PRS[@]}"; do
	case "$n" in '' | *[!0-9]*) continue ;; esac
	ref="refs/remotes/pins/pr$n"
	git fetch -q origin "+refs/pull/$n/head:$ref" 2>/dev/null || continue
	tocados=$(git diff --name-only "HEAD...$ref" 2>/dev/null || true)
	[ -n "$tocados" ] || continue
	hits=""
	i=0
	while [ "$i" -lt "${#GATE_FILE[@]}" ]; do
		f="${GATE_FILE[$i]}"; g="${GATE_NAME[$i]}"; r="${GATE_PATH[$i]}"
		i=$((i + 1))
		[ "${BASE_ROJO[$f]:-0}" -eq 0 ] || continue
		# Sin tubería que cerrar: `productor | grep -q` devuelve 141 CUANDO ACIERTA bajo
		# `pipefail` (SIGPIPE al productor), y el `||` lo lee como «no está». La sustitución
		# de proceso es la forma que el propio check-sigpipe-booleans.sh documenta.
		grep -qxF -- "$r" <(printf '%s\n' "$tocados") || continue
		git checkout -q "$ref" -- "$r" 2>/dev/null || continue
		timeout 120 bash "$f" >/dev/null 2>&1 || hits="$hits ${g%-prep}"
		# RESTAURAR SIEMPRE, y las dos formas: si la ruta no existe en HEAD, `checkout` no
		# la borra — la PR la AÑADE, y dejarla ahi contamina la medida de la PR siguiente.
		if git cat-file -e "HEAD:$r" 2>/dev/null; then
			git checkout -q HEAD -- "$r" 2>/dev/null || true
		else
			git rm -q --cached --force -- "$r" >/dev/null 2>&1 || true
			rm -f -- "$r" 2>/dev/null || true
		fi
	done
	if [ -n "$hits" ]; then
		clavadas=$((clavadas + 1))
		printf '  PINNED   #%-6s → %s\n' "$n" "$(printf '%s' "$hits" | tr ' ' '\n' | grep . | sort -u | tr '\n' ' ')"
	fi
done

resto=$(git status --porcelain | grep -c . || true)
[ "${resto:-0}" -eq 0 ] || cannot_check "the measurement left $resto entry/entries in the tree: restoration failed and the verdict is unreliable"

printf '\nreport-prep-gate-pins: %s of %s PR(s) would fail a gate if landed AS IS.\n' "$clavadas" "${#PRS[@]}"
echo "                       Measured by EXECUTION with each path ISOLATED: a PR may include a correction to"
echo "                       the gate premise in the same batch. This is a range, not an upper bound."
[ "$clavadas" -eq 0 ]
