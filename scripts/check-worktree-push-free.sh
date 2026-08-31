#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CENSUS-SUBJECT: external
#   Su sujeto son los PROCESOS de esta caja, no el repositorio: pasar sobre un árbol vacío es
#   CORRECTO, no un verde a ciegas. Por eso se declara aquí y no en una lista del censo.
#
# ⛔ NO SE CABLEA AL `pre-push`, y es una condición de su adjudicación (the planner, 2026-08-25T23:50Z),
#   no un olvido: durante un push SIEMPRE hay un push en vuelo sobre ese worktree — el propio. Un
#   gate que se llamara a sí mismo desde el hook devolvería 1 en todas las corridas legítimas.
#   Se invoca A MANO antes de editar, o desde un arnés de push como su capa 0.
#
# ⛔ Y SE INVOCA CON `bash`, NUNCA SE PRUEBA CON `[ -x ]`. Medido el 2026-08-26: el scratchpad vive
#   bajo /tmp, montado NOEXEC; el bit de ejecución está puesto (`-rwxr-xr-x`) pero `access(X_OK)`
#   falla, así que `[ -x ]` da FALSO y quien lo llame así **desactiva esta guarda en silencio**.
#   Le pasó a su propio arnés en su primer uso real: un aviso por stderr que dentro de un `nohup`
#   no lee nadie. Quien lo integre: `[ -r "$g" ] && bash "$g" …`, y fail-closed si no se puede leer.
# check-worktree-push-free.sh <worktree> — ¿tiene ESE worktree un push en vuelo?
#
#   0 = libre, puedes editar y commitear
#   1 = HAY UN PUSH VIVO: el worktree es de SÓLO LECTURA hasta que declare su ls-remote
#   2 = no he podido mirar
#
# ⛔ EXISTE PORQUE ESCRIBIR LA REGLA NO LA APLICÓ. La rompí TRES veces en una jornada, después de
# escribirla en memoria y de avisar a los otros cuatro carriles de ella. Los dos daños, medidos:
#   · `check-tree-untouched` compara el árbol al empezar y al terminar, ve mi edición y culpa a un
#     gate — «no es tuyo», dice, y sí lo era. El push muere con rc=1 por una causa falsa.
#   · git transfiere el ref RESUELTO EN LA TRANSFERENCIA, no el que el hook gateó, así que el
#     commit que añado encima viaja SIN fast-lints.
#
# Identifica el push por el HOOK (`olivares-prepush.*`) y su cwd, no por `git push` — el hook es
# quien tiene el worktree abierto. Y comprueba el PPID: un hook con padre 1 es HUÉRFANO, su push ya
# murió, y ése no impide editar (pero conviene matarlo: quema un núcleo).
set -uo pipefail
W="${1:-}"
[ -n "$W" ] || { echo "check-worktree-push-free: NO HE PODIDO MIRAR: falta <worktree>" >&2; exit 2; }
[ -d "$W" ] || { echo "check-worktree-push-free: NO HE PODIDO MIRAR: '$W' no es un directorio" >&2; exit 2; }
real="$(cd -- "$W" && pwd -P)" || { echo "check-worktree-push-free: NO HE PODIDO MIRAR: no resuelvo '$W'" >&2; exit 2; }

vivos=0; huerfanos=0
for h in $(ps -eo pid,args --no-headers 2>/dev/null | grep '[o]livares-prepush' | awk '{print $1}'); do
	d="$(readlink "/proc/$h/cwd" 2>/dev/null)" || continue
	[ "$d" = "$real" ] || continue
	pp="$(ps -o ppid= -p "$h" 2>/dev/null | tr -d ' ')"
	if [ "$pp" = "1" ]; then
		huerfanos=$((huerfanos + 1))
		echo "check-worktree-push-free: ⚠ hook HUÉRFANO $h (ppid=1) — su push murió. No te impide editar, pero quema un núcleo: mátalo." >&2
	else
		vivos=$((vivos + 1))
		echo "check-worktree-push-free: ⛔ push VIVO en $real — hook $h, padre $pp." >&2
	fi
done

if [ "$vivos" -gt 0 ]; then
	echo "check-worktree-push-free: NO EDITES. $vivos push(es) en vuelo sobre este worktree." >&2
	echo "               Un commit encima viaja SIN gatear, y tu edición hace que check-tree-untouched culpe a un gate." >&2
	exit 1
fi
echo "check-worktree-push-free: libre ($real)${huerfanos:+ · $huerfanos hook(s) huérfano(s) que convendría matar)}"
exit 0
