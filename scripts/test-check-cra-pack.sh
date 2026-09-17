#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bateria de scripts/check-cra-pack.sh — la guarda de regresion del paquete de preparacion CRA (UE).
#
# ⛔ POR QUE. Es una pata VIVA del gancho y no tenia prueba propia. Y su valor no es documental: el
#    paquete CRA es lo que sostiene una afirmacion de cumplimiento hacia fuera. Un gate que deja de
#    mirarlo imprime CLEAN, que es indistinguible de un paquete correcto.
#
# ⛔ Y TIENE TRES RESPUESTAS, NO DOS, que es lo mas facil de romper al tocarlo: sin `date -d`
#    de GNU la FRESCURA no se puede medir, y eso NO es un fallo del paquete ni un verde. El guion lo
#    anota, SIGUE con lo que si puede mirar, y termina PARCIAL con rc=2.
#
# CONTROL DE VIDA, medido: sustituyendo el sujeto por un `exit 0`, la bateria enrojece
#   (9 filas caen). ⚠ PERO 2 SOBREVIVEN, y no es un defecto que se pueda quitar: son las
#   que aseveran «limpio», y un señuelo que no hace nada tambien sale 0. ⇒ El poder discriminante
#   de esta bateria vive en las filas que esperan 1 o 2; las de rc 0 valen como control positivo
#   —sin ellas los rojos no probarian nada— pero NO distinguen un sujeto vivo de uno muerto.
#
# ONCE FILAS, NUEVE ACREDITADAS POR MUTACION — cada una muere con su mutante y SOLO con el suyo:
#   deja de mirar el documento · no exige la plantilla de 72h · acepta la ausencia de linea de fecha ·
#   acepta una fecha FUTURA · acepta una de mas de 400 dias · convierte el aviso de 180 en FALLO ·
#   deja de exigir UPGRADE-AND-ROLLBACK.md · no exige 'support period' · colapsa el punto ciego en FALLO.
# Las DOS que no se acreditan son CONTROLES POSITIVOS y lo son a proposito: «el paquete completo sale
# limpio» y «el date señuelo sirve sin -d y falla con -d». Sin ellas los nueve rojos no probarian nada
# —podrian estar fallando por la fixture— pero no distinguen un sujeto vivo de uno muerto.
#
# HERMETICA: construye paquetes de usar y tirar y se los pasa por `CRA_PACK_ROOT`. Sin red.
#
# ⛔ LAS FECHAS SE GENERAN RELATIVAS A HOY, nunca literales. Una fecha cableada convierte la bateria
#    en una bomba de relojeria: pasa hoy y falla dentro de 400 dias sin que nadie toque nada.
set -uo pipefail

GATE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/check-cra-pack.sh"
[ -f "$GATE" ] || { echo "FATAL: no encuentro el sujeto en $GATE" >&2; exit 2; }
command -v date >/dev/null 2>&1 && date -u -d '1970-01-01' +%s >/dev/null 2>&1 \
	|| { echo "FATAL: esta bateria necesita GNU date para FABRICAR sus fechas" >&2; exit 2; }
# ⛔ EL TEMPORAL TIENE QUE EJECUTAR, y en estas cajas `/tmp` NO ejecuta (tmpfs con `noexec`).
# Medido el 2026-09-01: con `TMPDIR=/tmp` esta bateria da **9 passed, 1 failed** —su caso del
# senuelo `date` se NIEGA a medir, que es lo correcto— y con un temporal ejecutable da **11/0**.
# Y no es cosmetico: la bateria esta CABLEADA AL GANCHO, asi que con el TMPDIR por defecto
# **ningun carril podia empujar una rama**. Dos veredictos opuestos sobre el mismo arbol eran dos
# ENTORNOS, no dos opiniones: quien llevaba `TMPDIR=/workspace/...` la veia verde y quien no, roja.
#
# Se elige con la libreria de la casa, como ya hacen `test-publish-inbox-shape.sh` y
# `test-exec-tmpdir.sh`, y si NINGUN candidato ejecuta se rehusa con 2: no medido no es verde.
_cra_lib="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/exec-tmpdir.sh"
# shellcheck source=/dev/null
. "$_cra_lib" || { echo "test-check-cra-pack: FATAL: no puedo cargar $_cra_lib" >&2; exit 2; }
if ! _cra_base="$(olivares_exec_tmpdir)"; then
	echo "test-check-cra-pack: NO HE PODIDO MIRAR: ningun directorio temporal EJECUTA" >&2
	exit 2
fi
unset _cra_lib
WORK="$(mktemp -d "$_cra_base/cra-bat.XXXXXX")" || exit 2
trap 'rm -rf "$WORK"' EXIT INT TERM
pass=0; fail=0

pack() { # pack <nombre> <desplazamiento-de-fecha, tal cual para `date -d`>  -> ruta
	# ⛔ EL DESPLAZAMIENTO VA ENTERO, no un numero de dias con el signo puesto fuera. La primera
	#    version hacia `date -d "-$2 days"` y para una fecha FUTURA habia que pasar `-3`, lo que
	#    producia `--3 days`: `date` no lo entendia, la fecha salia la de hoy y el caso del futuro
	#    pasaba en verde SIN ejercer nada. Lo cazo la propia fila, que es para lo que esta.
	local d="$WORK/$1" f; f="$(date -u -d "$2" +%Y-%m-%d)" || return 1
	mkdir -p "$d/docs"
	cat >"$d/docs/CRA-READINESS.md" <<CRA
# CRA
### Template: early warning (≤24h)
### Template: notification (≤72h)
### Template: final report (two distinct triggers)
| Release line | First placed on EU market | End of support |
Last re-verification: $f (contexto entre parentesis, que el sujeto tolera a proposito)
CRA
	printf '# upgrade\n## 3. Security updates — CRA statement\n' >"$d/docs/UPGRADE-AND-ROLLBACK.md"
	printf 'release:\n  header: |\n    Support period: see CRA-READINESS.md\n' >"$d/.goreleaser.yaml"
	printf '%s' "$d"
}

caso() { # caso <nombre> <rc-esperado> <subcadena|-> <dir> [VAR=VAL...]
	local n="$1" want="$2" sub="$3" d="$4"; shift 4
	local out rc=0
	out="$(env "$@" CRA_PACK_ROOT="$d" sh "$GATE" 2>&1)" || rc=$?
	if [ "$rc" -ne "$want" ]; then
		printf 'FAIL  %s: rc=%s, esperaba %s\n' "$n" "$rc" "$want"
		printf '%s\n' "$out" | head -3 | sed 's/^/        /'; fail=$((fail + 1)); return
	fi
	if [ "$sub" != "-" ]; then
		case "$out" in *"$sub"*) : ;; *)
			printf 'FAIL  %s: rc correcto (%s) pero no dijo %s\n' "$n" "$rc" "$sub"
			printf '%s\n' "$out" | head -3 | sed 's/^/        /'; fail=$((fail + 1)); return ;;
		esac
	fi
	printf 'ok    %s (rc %s)\n' "$n" "$rc"; pass=$((pass + 1))
}

# 1 · el paquete completo y fresco sale limpio. Sin este control positivo, los rojos de abajo no
#     prueban nada: podrian estar fallando por como monto la fixture.
caso "un paquete completo y fresco sale limpio" 0 "-" "$(pack completo '-10 days')"

# 2 · falta el documento entero
d="$(pack sin-cra '-10 days')"; rm -f "$d/docs/CRA-READINESS.md"
caso "sin docs/CRA-READINESS.md es FAIL" 1 "missing docs/CRA-READINESS.md" "$d"

# 3 · falta una de las plantillas de notificacion (el corazon del deber de aviso)
d="$(pack sin-72h '-10 days')"; sed -i '/notification (≤72h)/d' "$d/docs/CRA-READINESS.md"
caso "sin la plantilla de notificacion (72h) es FAIL" 1 "notification template heading" "$d"

# 4 · sin linea de re-verificacion NO se puede afirmar frescura
d="$(pack sin-fecha '-10 days')"; sed -i '/^Last re-verification:/d' "$d/docs/CRA-READINESS.md"
caso "sin 'Last re-verification' es FAIL" 1 "missing Last re-verification" "$d"

# 5 · una fecha en el FUTURO no es frescura, es un reloj mal puesto o una copia
caso "una re-verificacion en el FUTURO es FAIL" 1 "in the future" "$(pack futura '+3 days')"

# 6 · rancia de verdad (>400 dias)
caso "una re-verificacion de mas de 400 dias es FAIL" 1 "older than 400 days" "$(pack rancia '-500 days')"

# 7 · ⛔ ENTRE 180 Y 400 DIAS ES AVISO, NO FALLO. Es la distincion que un mutante perezoso borra:
#     convertir el warn en fail bloquea a los cinco carriles por un documento que sigue siendo valido.
caso "entre 180 y 400 dias AVISA pero no falla" 0 "older than 180 days" "$(pack tibia '-200 days')"

# 8 · falta el documento de actualizacion y reversion
d="$(pack sin-upgrade '-10 days')"; rm -f "$d/docs/UPGRADE-AND-ROLLBACK.md"
caso "sin docs/UPGRADE-AND-ROLLBACK.md es FAIL" 1 "missing docs/UPGRADE-AND-ROLLBACK.md" "$d"

# 9 · la cabecera de release tiene que MENCIONAR el periodo de soporte: es la afirmacion que viaja
#     al usuario final en cada release.
d="$(pack sin-soporte '-10 days')"
printf 'release:\n  header: |\n    nada que declarar\n' >"$d/.goreleaser.yaml"
caso "una cabecera de release sin 'support period' es FAIL" 1 "support period" "$d"

# 10 · ⛔ SIN `date -d` DE GNU: PARCIAL con rc=2, ni 0 ni 1. Se inyecta un `date` señuelo que falla
#      con -d y funciona sin el; el CONTROL POSITIVO va primero, porque un señuelo que no sabe hacer
#      nada probaria lo mismo con el sujeto roto o sano.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/date" <<'SH'
#!/bin/sh
for a in "$@"; do case "$a" in -d|-d*) exit 1 ;; esac; done
exec /usr/bin/date "$@"
SH
chmod +x "$WORK/bin/date"
if "$WORK/bin/date" -u +%Y >/dev/null 2>&1 && ! "$WORK/bin/date" -u -d '1970-01-01' +%s >/dev/null 2>&1; then
	printf 'ok    control positivo: el date señuelo sirve SIN -d y falla CON -d\n'; pass=$((pass + 1))
	caso "sin GNU date la frescura no se mira: PARCIAL rc=2, ni 0 ni 1" 2 "NO HE PODIDO MIRAR" \
		"$(pack ciego '-10 days')" "PATH=$WORK/bin:$PATH"
else
	printf 'FAIL  el date señuelo no se comporta como pide el caso: no mido nada\n'; fail=$((fail + 1))
fi

printf '\ntest-check-cra-pack: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
