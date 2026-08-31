#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# watchdog-unpublished-work.sh — a repository gate. La otra mitad de check-unpublished-work.sh.
#
# ⛔ POR QUÉ NO ES UN GATE, y esto es el hallazgo entero de la fila.
#
# `lint:unpublished-work` fue un ratchet del carril rápido y se RETIRÓ el mismo día que aterrizó
# (2026-08-10), medido por dos carriles en cuatro horas. Su premisa era global: contaba ramas que
# sólo existen en el clon, pero el clon lo comparten tres contenedores y N sesiones, mientras que
# un push es de UNA. Cobraba al que empujaba por trabajo ajeno que no podía resolver.
# Otro carril se quedó bloqueado por `feature`, una sesión VIVA; publicó su punta a medias
# para bajar el contador y volvió a subir, porque seguía commiteando. Con N sesiones el
# contador NO puede llegar a cero, y el único remedio que queda es `--no-verify`: una regla cuyo
# único remedio es lo que ella misma prohíbe.
#
# El hook lo dice con todas las letras: «a watchdog is the right home for it — it wakes its owner
# without blocking anyone». Esto es eso. **No bloquea nunca a nadie.**
#
# ⛔ Y LO QUE LE FALTABA AL CENSO NO ERA EL SITIO: ERA LA EDAD.
# `check-unpublished-work.sh` responde «¿hay trabajo sin publicar?» y la respuesta honesta es
# SIEMPRE que sí, porque en cualquier instante hay carriles a medio commit. Un número que siempre
# dice lo mismo es un número sobre el que nadie actúa — el patrón que este proyecto ya tiene
# nombrado. Lo accionable no es que exista, es **cuánto lleva ahí**:
#
#   EN VUELO    < 45 min   alguien está trabajando. No se toca, no se avisa. Es lo normal.
#   SIN PUBLICAR  45 min–4 h   merece un aviso a su dueño: probablemente se olvidó el push.
#   OLVIDADO    > 4 h      es la clase que se pierde. Nueve informes vivían así el 2026-08-12.
#
# El dueño se nombra por su WORKTREE, que es la única atribución que este repositorio tiene y que
# no depende de la identidad del commit (los tres carriles firman con la MISMA dirección noreply,
# así que el autor no distingue a nadie).
#
# Salidas: 0 = nada olvidado (aunque haya trabajo en vuelo) · 1 = hay OLVIDADO · 2 = NO HE PODIDO
# MIRAR. La tercera no es cosmética: sin remotos conocidos, «no está en ningún remoto» es cierto
# para TODO, y ese falso positivo total sería peor que no mirar.
set -euo pipefail

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "watchdog-unpublished-work: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$RAIZ" || { echo "watchdog-unpublished-work: ⛔ NO HE PODIDO MIRAR: no puedo entrar en '$RAIZ'." >&2; exit 2; }

STALE="${OLIVARES_WATCHDOG_STALE_SECS:-2700}"      # 45 min
OLVID="${OLIVARES_WATCHDOG_FORGOTTEN_SECS:-14400}" # 4 h
case "$STALE$OLVID" in *[!0-9]*) echo "watchdog-unpublished-work: ⛔ NO HE PODIDO MIRAR: umbrales no numéricos." >&2; exit 2;; esac
if [ "$OLVID" -le "$STALE" ]; then
	# Un umbral de olvido por debajo del de aviso haría inalcanzable el tramo intermedio y el
	# watchdog gritaría por todo. Se corrige y SE DICE, igual que hace el mutex con WAIT/STALE.
	echo "watchdog-unpublished-work: ⚠ FORGOTTEN ($OLVID) <= STALE ($STALE); derivo FORGOTTEN=$((STALE + 3600))." >&2
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
	echo "watchdog-unpublished-work: ⛔ NO HE PODIDO MIRAR: cero refs remotas conocidas." >&2
	echo "                           Con eso, TODO sale «sin publicar» y el aviso no valdría nada." >&2
	exit 2
fi

ahora="$(date +%s)"
olvidados=0; avisos=0; en_vuelo=0
echo "watchdog-unpublished-work: $n_rem ref(s) remota(s) conocidas — la sonda mide."

while IFS= read -r d; do
	[ -d "$d" ] || continue
	h="$(git -C "$d" rev-parse HEAD 2>/dev/null)" || continue
	n="$(git -C "$d" rev-list --count HEAD --not --remotes 2>/dev/null)" || continue
	[ "${n:-0}" -gt 0 ] || continue
	rama="$(git -C "$d" symbolic-ref --short -q HEAD 2>/dev/null || echo '(HEAD suelta)')"
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
		[ "${ya:-0}" -gt 0 ] && equiv="  [${ya}/${n} ya aplicado(s) arriba por contenido]"
	fi
	if   [ "$edad" -ge "$OLVID" ]; then
		printf '  ⛔ OLVIDADO    %-34s %2d commit(s), el más viejo hace %-8s %s%s\n' "$rama" "$n" "$hm" "$d" "$equiv"
		olvidados=$((olvidados + 1))
	elif [ "$edad" -ge "$STALE" ]; then
		printf '  ⚠  SIN PUBLICAR %-33s %2d commit(s), el más viejo hace %-8s %s%s\n' "$rama" "$n" "$hm" "$d" "$equiv"
		avisos=$((avisos + 1))
	else
		en_vuelo=$((en_vuelo + 1))
	fi
done < <(git worktree list --porcelain 2>/dev/null | awk '/^worktree /{print $2}')

echo "watchdog-unpublished-work: olvidados=$olvidados avisos=$avisos en-vuelo=$en_vuelo"
if [ "$olvidados" -gt 0 ]; then
	echo "watchdog-unpublished-work: ⛔ $olvidados con más de $((OLVID / 3600)) h sin publicar." >&2
	echo "                           NO bloquea a nadie: publica desde ESE worktree, no desde otro." >&2
	exit 1
fi
echo "watchdog-unpublished-work: ✔ nada olvidado."
exit 0
