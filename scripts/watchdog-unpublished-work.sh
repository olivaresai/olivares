#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# watchdog-unpublished-work.sh — a repository gate, the companion to check-unpublished-work.sh.
# This is a watchdog, not a push gate. lint:unpublished-work was introduced and retired
# on 2026-08-10 after two workers measured the problem over four hours: branches
# belong to a clone shared by three containers and many sessions, but a push belongs
# to one. Blocking it charged the pusher for another session's unresolved work.
# One worker published feature mid-work to reduce the count, but the live session
# kept committing and it rose again. With many active sessions it cannot reach zero;
# a guard whose only remedy is --no-verify defeats its own policy.
# The hook says a watchdog should wake the owner without blocking others. Never block.
#
# Age makes the census actionable: some unpublished work always exists while sessions
# work. Under 45 minutes is normal in-flight work, with no notification; 45 minutes
# to four hours merits an owner reminder; over four hours risks forgotten work (nine
# reports on 2026-08-12). Attribute owners by worktree: all three workers share the
# same noreply signing identity, so commit author cannot distinguish them.
# Exit: 0 no forgotten work · 1 forgotten work · 2 could not check. Without known
# remotes, every commit would look unpublished; do not report that as a finding.
set -euo pipefail

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "watchdog-unpublished-work: ⛔ COULD NOT LOOK: not inside a repository." >&2; exit 2; }
cd "$RAIZ" || { echo "watchdog-unpublished-work: ⛔ COULD NOT LOOK: cannot enter '$RAIZ'." >&2; exit 2; }

STALE="${OLIVARES_WATCHDOG_STALE_SECS:-2700}"      # 45 min
OLVID="${OLIVARES_WATCHDOG_FORGOTTEN_SECS:-14400}" # 4 h
case "$STALE$OLVID" in *[!0-9]*) echo "watchdog-unpublished-work: ⛔ COULD NOT LOOK: thresholds are not numeric." >&2; exit 2;; esac
if [ "$OLVID" -le "$STALE" ]; then
	# Un umbral de olvido por debajo del de aviso haría inalcanzable el tramo intermedio y el
	# watchdog gritaría por todo. Se corrige y SE DICE, igual que hace el mutex con WAIT/STALE.
	echo "watchdog-unpublished-work: ⚠ FORGOTTEN ($OLVID) <= STALE ($STALE); using FORGOTTEN=$((STALE + 3600))." >&2
	OLVID=$((STALE + 3600))
fi

if [ "${OLIVARES_WATCHDOG_NO_FETCH:-0}" != "1" ]; then
	# ⛔ `--prune` NO ES OPCIONAL, y sin él este watchdog tiene un FALSO VERDE medido.
	#
	# El respaldo se decide con `rev-list HEAD --not --remotes`, que lee refs LOCALES de
	# seguimiento. Cuando una rama se borra en el servidor —lo normal al mergear un PR— la ref
	# `origin/<rama>` SOBREVIVE aquí, y sus commits siguen contando como «respaldados» aunque el
	# servidor ya no los tenga. Reproducido el 2026-08-21 en un fixture de cuatro comandos: rama
	# publicada, borrada en el remoto, `ls-remote` sin el SHA ⇒ irrecuperable, y `--not --remotes`
	# contestando CERO SIN PUBLICAR. Es exactamente la clase que este watchdog existe para avisar,
	# y era la única que no podía ver.
	#
	# ⚠ Y NO es el `prune` que este repositorio prohíbe. Aquél es `git prune`/`git gc`, que borra
	# OBJETOS y se llevaría el trabajo en vuelo de otros carriles (417 commits el 2026-08-20).
	# `git fetch --prune` borra sólo REFS DE SEGUIMIENTO muertas: ningún objeto desaparece, y el
	# freno del `gc.log` sigue impidiendo la recolección automática. El efecto es que aparecen MÁS
	# avisos, nunca menos — falla hacia el lado ruidoso, que es el único aceptable en un aviso.
	git fetch -q --prune origin 2>/dev/null || true
fi
n_rem="$(git for-each-ref --format='%(refname)' refs/remotes/ 2>/dev/null | grep -c . || true)"
if [ "${n_rem:-0}" -eq 0 ]; then
	echo "watchdog-unpublished-work: ⛔ COULD NOT LOOK: no known remote refs." >&2
	echo "                           That would mark EVERYTHING as unpublished, making the warning meaningless." >&2
	exit 2
fi

ahora="$(date +%s)"
olvidados=0; avisos=0; en_vuelo=0
echo "watchdog-unpublished-work: $n_rem known remote ref(s) — the probe can measure."

while IFS= read -r d; do
	[ -d "$d" ] || continue
	h="$(git -C "$d" rev-parse HEAD 2>/dev/null)" || continue
	n="$(git -C "$d" rev-list --count HEAD --not --remotes 2>/dev/null)" || continue
	[ "${n:-0}" -gt 0 ] || continue
	rama="$(git -C "$d" symbolic-ref --short -q HEAD 2>/dev/null || echo '(detached HEAD)')"
	# El commit sin publicar MÁS ANTIGUO es el que fija la edad: la punta puede ser de hace un
	# minuto y estar sentada encima de trabajo de ayer, y es ese trabajo el que se pierde.
	viejo="$(git -C "$d" rev-list HEAD --not --remotes 2>/dev/null | tail -1)"
	[ -n "$viejo" ] || continue
	ct="$(git -C "$d" show -s --format=%ct "$viejo" 2>/dev/null)" || continue
	edad=$(( ahora - ct )); [ "$edad" -ge 0 ] || edad=0
	hm="$((edad / 3600))h$(( (edad % 3600) / 60 ))m"
	# ⛔ SEGUNDA SEÑAL, y la aprendí en la PRIMERA corrida de esto: «no está en ningún remoto»
	# es alcanzabilidad por SHA, y NO es «no está publicado». El primer OLVIDADO que encontró
	# este watchdog —373 h en /workspace/.s525-board4— tenía sus 44 líneas ENTERAS en `main`,
	# publicadas por otra vía (rebase o cherry-pick le cambian el SHA y lo dejan huérfano).
	# Avisar de una pérdida que no existe gasta el aviso, que es justo como muere un watchdog.
	#
	# `git cherry` compara por patch-id y marca con `-` lo que ya está arriba. Sólo se usa para
	# REBAJAR: si dice que está aplicado, se dice; si dice que no, NO se sube la alarma, porque
	# un patch-id cambia al resolver un conflicto y este proyecto ya midió que `git cherry`
	# cuenta como pendiente cosas ya hechas. Una señal que sólo puede quitar ruido no puede
	# introducir un falso negativo.
	equiv=""
	if up="$(git -C "$d" rev-parse --verify -q origin/main 2>/dev/null)" && [ -n "$up" ]; then
		ya="$(git -C "$d" cherry origin/main HEAD 2>/dev/null | grep -c '^-' || true)"
		[ "${ya:-0}" -gt 0 ] && equiv="  [${ya}/${n} already applied upstream by content]"
	fi
	if   [ "$edad" -ge "$OLVID" ]; then
		printf '  ⛔ FORGOTTEN   %-34s %2d commit(s), oldest: %-8s ago %s%s\n' "$rama" "$n" "$hm" "$d" "$equiv"
		olvidados=$((olvidados + 1))
	elif [ "$edad" -ge "$STALE" ]; then
		printf '  ⚠  UNPUBLISHED  %-33s %2d commit(s), oldest: %-8s ago %s%s\n' "$rama" "$n" "$hm" "$d" "$equiv"
		avisos=$((avisos + 1))
	else
		en_vuelo=$((en_vuelo + 1))
	fi
done < <(git worktree list --porcelain 2>/dev/null | awk '/^worktree /{print $2}')

echo "watchdog-unpublished-work: forgotten=$olvidados warnings=$avisos in-progress=$en_vuelo"
if [ "$olvidados" -gt 0 ]; then
	echo "watchdog-unpublished-work: ⛔ $olvidados unpublished for more than $((OLVID / 3600)) h." >&2
	echo "                           This does NOT block anyone: publish from THAT worktree." >&2
	exit 1
fi
echo "watchdog-unpublished-work: ✔ nothing forgotten."
exit 0
