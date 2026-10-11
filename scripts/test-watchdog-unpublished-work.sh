#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Batería de watchdog-unpublished-work.sh. Fixtures reales, no simulacros.
#
# ⛔ EL GUARDIÁN DE SANDBOX SE LLAMA DESDE EL SHELL PRINCIPAL Y EXIGE QUE LA RUTA CUELGUE DE $TMP.
# No es ceremonia: el 2026-08-17 una batería mía hizo `git add -A` y `git commit` DENTRO del
# repositorio y truncó README.md a 3 bytes. La primera guarda era decorativa porque el `exit`
# vivía dentro de un `$( )`, donde sólo mata la subshell. Y `cd ""` SALE 0 sin cambiar de
# directorio, así que un fixture con ruta vacía trabaja donde estés. Las dos cosas, aquí, juntas.
set -euo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT. Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO
# —o sea, desde cualquier sesion en paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, los
# repositorios desechables que construye este banco son el repositorio VIVO de quien lo invoque.
# MEDIDO el 2026-08-30 contra un repositorio de destino desechable, con este mismo fichero y sin
# esta linea: el destino recibio COMMITS. Falla cerrado: no poder aislar es «no he podido».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$RAIZ/scripts/watchdog-unpublished-work.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/watchdog-bat.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pasa=0; falla=0
sandbox() { # se INVOCA en el shell principal: un exit aquí mata la batería, que es lo que se quiere
	case "$1" in
		"") echo "⛔ EMPTY fixture path — 'cd \"\"' returns 0 and would operate in the repository." >&2; exit 2;;
		"$TMP"/*) : ;;
		*) echo "⛔ fixture OUTSIDE the sandbox: '$1' is not under '$TMP'." >&2; exit 2;;
	esac
	[ -d "$1" ] || { echo "⛔ nonexistent fixture: '$1'." >&2; exit 2; }
}

comprueba() { # nombre · rc esperado · patrón esperado (vacío = no se mira) · dir
	local n="$1" rc_esp="$2" pat="$3" d="$4"; shift 4
	sandbox "$d"
	local out rc
	# `env` DELANTE: sin él, "$@" pone `VAR=valor` en posición de COMANDO y sale 127
	# «command not found» — que la batería leyó como un fallo del guión y no suyo.
	out="$(cd "$d" && env OLIVARES_WATCHDOG_NO_FETCH=1 "$@" bash "$GUION" 2>&1)" && rc=0 || rc=$?
	local ok=1
	[ "$rc" = "$rc_esp" ] || ok=0
	# ⛔ HERE-STRING, NO TUBERÍA. `printf … | grep -q` devuelve **141 EN ÉXITO** bajo `pipefail`:
	# grep encuentra, cierra su entrada y printf muere de SIGPIPE. Ya me costó dos horas de
	# carril rojo el 2026-08-17 y lo repetí HOY en las dos baterías que escribí, bloqueando el
	# push de los cinco carriles hasta que `lint:sigpipe-booleans` lo nombró.
	if [ -n "$pat" ] && ! grep -q -- "$pat" <<<"$out"; then ok=0; fi
	if [ "$ok" = 1 ]; then printf '  ok   %-52s rc=%s\n' "$n" "$rc"; pasa=$((pasa+1))
	else printf '  FAIL  %-51s rc=%s (expected %s) pat=%s\n' "$n" "$rc" "$rc_esp" "${pat:-<none>}"
	     printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi
}

nuevo_repo() { # $1 = nombre; deja un repo con remoto bare y un commit publicado
	local d="$TMP/$1"
	mkdir -p "$d.remoto" "$d"
	git init -q --bare "$d.remoto"
	git init -q -b main "$d"
	git -C "$d" config user.name t; git -C "$d" config user.email t@t
	git -C "$d" config commit.gpgsign false
	echo base > "$d/f"; git -C "$d" add f; git -C "$d" commit -qm base
	git -C "$d" remote add origin "$d.remoto"; git -C "$d" push -q origin main
	git -C "$d" fetch -q origin
	printf '%s' "$d"
}

commit_con_edad() { # $1 dir · $2 segundos de antigüedad · $3 texto
	# ⛔ FORMATO `@epoch +0000` A PROPÓSITO. Una marca ISO SIN zona la lee git en hora LOCAL, y
	# este contenedor va en Europe/Madrid: el fixture "de hace 1 minuto" salía de hace 2h1m y la
	# batería acusaba al guión de clasificar mal. El desfase era del fixture, no de lo medido.
	local fecha="@$(( $(date +%s) - $2 )) +0000"
	echo "$3" >> "$1/f"; git -C "$1" add f
	GIT_AUTHOR_DATE="$fecha" GIT_COMMITTER_DATE="$fecha" git -C "$1" commit -qm "$3"
}

echo "watchdog-unpublished-work: test suite"

d="$(nuevo_repo limpio)"
comprueba "all work published is clean" 0 "nothing forgotten" "$d"

d="$(nuevo_repo envuelo)"; commit_con_edad "$d" 60 "recien"
comprueba "a commit from 1 minute ago is IN FLIGHT, not a warning" 0 "in-progress=1" "$d"

d="$(nuevo_repo aviso)"; commit_con_edad "$d" 3600 "de hace una hora"
comprueba "one hour unpublished is a WARNING, not a block" 0 "warnings=1" "$d"

d="$(nuevo_repo olvidado)"; commit_con_edad "$d" 90000 "de anteayer"
comprueba "more than 4 h is FORGOTTEN and returns 1" 1 "FORGOTTEN" "$d"

# El caso que motivó el diseño: punta fresca sobre trabajo viejo. Si la edad se tomara de HEAD
# esto saldría EN VUELO y el trabajo de anteayer seguiría invisible.
d="$(nuevo_repo apilado)"; commit_con_edad "$d" 90000 "viejo"; commit_con_edad "$d" 30 "punta fresca"
comprueba "age comes from the OLDEST commit, not the tip" 1 "FORGOTTEN" "$d"

d="$(nuevo_repo sinremoto)"; commit_con_edad "$d" 90000 "x"; git -C "$d" remote remove origin
comprueba "no remote refs is COULD NOT LOOK, not clean" 2 "COULD NOT LOOK" "$d"

d="$(nuevo_repo umbralmalo)"; commit_con_edad "$d" 90000 "x"
comprueba "nonnumeric threshold is COULD NOT LOOK" 2 "COULD NOT LOOK" "$d" OLIVARES_WATCHDOG_STALE_SECS=diez

d="$(nuevo_repo umbralinvertido)"; commit_con_edad "$d" 90000 "x"
comprueba "FORGOTTEN<=STALE is corrected and REPORTED" 1 "using FORGOTTEN" "$d" \
	OLIVARES_WATCHDOG_STALE_SECS=100 OLIVARES_WATCHDOG_FORGOTTEN_SECS=50

# ⛔ LA CLASE QUE FALTABA: una rama BORRADA en el servidor deja aquí su ref de seguimiento, y con
# ella el trabajo parece respaldado cuando ya es irrecuperable. Es lo que le pasó a un carril el
# 2026-08-21 y lo que `--prune` arregla. Van las DOS mitades: el caso y su control negativo, que
# es el que demuestra que la casilla mide el prune y no otra cosa.
d="$(nuevo_repo refmuerta)"
git -C "$d" checkout -q -b rama
commit_con_edad "$d" 90000 "trabajo que solo vive en la rama"
git -C "$d" push -q origin rama
git -C "$d" fetch -q origin
git -C "$d.remoto" update-ref -d refs/heads/rama
# ⚠ EL ORDEN ES PARTE DEL CASO: `--prune` es PERSISTENTE. Si el positivo corriera primero
# dejaría el fixture ya podado y el control negativo mediría el estado del anterior, no el suyo
# — que es exactamente lo que hizo la primera versión de esta casilla.
comprueba "stale ref falsely reports work as backed up and clean (negative control)" 0 "nothing forgotten" "$d"
comprueba "…and with fetch --prune the same branch is FORGOTTEN" 1 "FORGOTTEN" "$d" \
	OLIVARES_WATCHDOG_NO_FETCH=0

# Control positivo del guardián: el propio sandbox tiene que negarse a salir de $TMP.
if ( sandbox "/etc" ) 2>/dev/null; then
	echo "  FAIL the sandbox guard ACCEPTED /etc"; falla=$((falla+1))
else
	echo "  ok   the guard rejects a path outside the sandbox         rc=2"; pasa=$((pasa+1))
fi

echo "watchdog-unpublished-work: $pasa passed, $falla failed"
[ "$falla" -eq 0 ]
