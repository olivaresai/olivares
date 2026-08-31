#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# census-blind-verdict.sh — ¿qué contesta cada gate cuando NO PUEDE MIRAR? Por COMPORTAMIENTO.
#
# ⛔ EXISTE PORQUE LA TERCERA RESPUESTA NO TIENE ORTOGRAFÍA CANÓNICA, y por eso ningún censo por
# cadena cuadraba. Medido el 2026-08-15 sobre los `check-*.sh` de `main`:
#
#     sólo «UNVERIFIED»          18        las DOS          1
#     sólo «NO HE PODIDO MIRAR»   6        ninguna         26   ← varias salen 2 por variable
#
# Son la misma respuesta en dos idiomas. El carril de integración contó 11 grepeando una de las dos; yo conté
# 14 leyendo el código fuente; ninguna de las dos cifras era el comportamiento. **Y el número se
# mueve cada vez que alguien aterriza una guarda**, así que una cifra en un documento nace vieja:
# esto es un COMANDO, no un número.
#
# ⛔ Y NO ES UN GATE: es un informe. Un aviso permanente en la vía de push es el patrón que este
# árbol ya midió como inútil —el detector de deriva imprimía su cifra cada noche y nadie la leía—.
# Se corre a mano o desde un job, y su salida se lee.
#
# CÓMO CIEGA, que es lo único que hace válido el censo: copia cada gate a un árbol VACÍO y lo
# EJECUTA. Las dos cosas salieron de equivocarme:
#   · ejecutarlo, no lanzarlo con `sh` — la primera versión corrió todo con `sh` y 36 «FAIL-2» eran
#     dash rechazando `set -o pipefail`, no el veredicto del gate;
#   · el señuelo bajo `$HOME`, porque `/tmp` está montado noexec en el contenedor (execve → 126);
#   · y un `cd` del llamante NO ciega nada: los gates resuelven su raíz desde `$0`.
#
# CLASES:
#   PASA-CIEGO   rc=0 sin sujeto            ⛔ el peligroso: un verde que no midió nada
#   RECHAZA-2    rc=2                       la tercera respuesta, dicha como sea
#   RECHAZA-1    rc=1 nombrando su entrada  negativa legítima (un artefacto ausente ES un defecto)
#   CRUDO        rc≠0 con el error de un ayudante, sin frase propia
#   NO-EJECUTA   126/127                    no se pudo medir: no es un veredicto del gate
#   TIMEOUT      124
#
# ⛔ LÍMITE DECLARADO: un gate cuyo SUJETO no es el repositorio (p. ej. `check-disk-headroom`, que
# mide el disco) pasa en un árbol vacío **con razón**. El censo lo marca `[sujeto-externo]` a
# partir de una lista corta y explícita; no adivina.
set -uo pipefail

# ⛔ AISLAMIENTO DE ENTORNO GIT — tercera vez hoy que `lint:git-env` me lo exige, y las tres tenía
# razón: este guion empareja `mktemp -d` con git (`git ls-files` para el censo, `git init` en el
# self-test), así que un `GIT_DIR` heredado haría que el árbol señuelo operase sobre OTRO
# repositorio. Es el mecanismo del repo, ya auditado sobre 30 miembros.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# ⛔ El override es EXPLÍCITO y con nombre propio: el self-test necesita censar un árbol fabricado,
# y la primera versión ponía `ROOT=` en el entorno del hijo… que lo reasignaba desde `$0` y censaba
# el repositorio real. Cuatro casillas en rojo por medir el árbol equivocado, no por la regla.
ROOT="${OLIVARES_CENSUS_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
# shellcheck source=lib/exec-workdir.sh
# lib/exec-workdir.sh ya PRUEBA que un candidato puede crear y EJECUTAR: no se elige el
# directorio, se demuestra. Antes esto era `mktemp -d "${VAR:-$HOME}/..."`, que bajo
# `set -u` MUERE con «HOME: unbound variable» en los runners sin $HOME — seis de nueve —
# antes siquiera de llegar a su propio respaldo.
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	# Sin la lib el guion esta CIEGO, y eso es 2 — no el error crudo del shell. La bateria
	# lo comprueba copiando este fichero solo a un arbol vacio.
	echo "census-blind-verdict: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
# ⛔ EL SUJETO EXTERNO SE DESCUBRE, NO SE ENUMERA. Antes esta línea era
# `SUJETO_EXTERNO="check-disk-headroom.sh"`: una lista a mano que caduca en silencio el día que
# aparezca otro gate cuyo sujeto no sea el repositorio — saldría como PASA-CIEGO falso y nadie lo
# sabría. Lo dice el carril de integración el 2026-08-15 tras medirlo en el gate del tag, que enumeraba
# `docs/` a mano y publicó 9 tokens obsoletos con su gate en verde: **si un gate enumera miembros,
# ya está caducando**. Ahora cada gate declara su propio sujeto con `CENSUS-SUBJECT: external`.
SUBJECT_MARK="CENSUS-SUBJECT: external"

clasificar() { # <rc> <salida>
	local rc="$1" out="$2"
	case "$rc" in
	0) echo PASA-CIEGO; return ;;
	124) echo TIMEOUT; return ;;
	126 | 127) echo "NO-EJECUTA-$rc"; return ;;
	esac
	if printf '%s' "$out" | grep -qE '^[^:]*: line [0-9]+: |command not found'; then
		echo "CRUDO-$rc"
	else
		echo "RECHAZA-$rc"
	fi
}

# ⛔ `base` es GLOBAL a propósito: como `local` de `censo()`, el `trap … EXIT` se dispara cuando la
# función ya retornó y `set -u` lo mata con «base: unbound variable» — que mi propio censo imprimía
# como si fuera una fila más (50 líneas para 49 gates). Un informe que mete su error entre los datos
# es peor que uno que falla.
base=""
censo() {
	local d rc out clase marca
	base="$(olivares_pick_exec_workdir censo)" || {
		echo "census: NO HE PODIDO MIRAR: no puedo crear el árbol señuelo" >&2
		exit 2
	}
	trap 'rm -rf "$base"' EXIT HUP INT TERM
	cd "$ROOT" || exit 2
	for f in $(git ls-files 'scripts/check-*.sh'); do
		local b; b="$(basename "$f")"
		d="$base/$b.d"
		mkdir -p "$d/scripts"
		cp "$f" "$d/scripts/" && chmod +x "$d/scripts/$b" || { printf '%-34s COPIA\n' "$b"; continue; }
		# ⛔ LA LIBRERÍA VIAJA CON EL SEÑUELO, o el censo se mide a sí mismo. Sin esto, los gates que
		#    sourcean `lib/git-env.sh` morían con «FATAL: cannot source …» y yo los clasificaba CRUDO:
		#    seis filas que no hablaban de su sujeto sino de MI árbol incompleto. Medido el 2026-08-15,
		#    y es la segunda vez en el día que un señuelo sin la librería me devuelve una lectura falsa.
		if [ -d "$ROOT/scripts/lib" ]; then cp -r "$ROOT/scripts/lib" "$d/scripts/" 2>/dev/null || true; fi
		out="$( (cd "$d" && timeout "${OLIVARES_CENSUS_TIMEOUT:-120}" "./scripts/$b" 2>&1) )"
		rc=$?
		rm -rf "$d"
		clase="$(clasificar "$rc" "$out")"
		marca=""
		if grep -qF "$SUBJECT_MARK" "$f"; then marca="  [sujeto-externo declarado: pasar sin sujeto es correcto]"; fi
		printf '%-34s %-12s %s%s\n' "$b" "$clase" "$(printf '%s' "$out" | tail -1 | cut -c1-46)" "$marca"
	done
}

self_test() {
	# ⛔ Controles FABRICADOS, porque un censo que no distingue sus propias clases no mide nada.
	local t ok=0 ko=0 salida
	t="$(olivares_pick_exec_workdir censo)"
	mkdir -p "$t/scripts"
	printf '#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n'                              > "$t/scripts/check-a-pasa.sh"
	printf '#!/usr/bin/env bash\nset -euo pipefail\necho "UNVERIFIED: nada" >&2\nexit 2\n' > "$t/scripts/check-b-dos.sh"
	printf '#!/usr/bin/env bash\nset -euo pipefail\necho "FAIL — falta x" >&2\nexit 1\n'   > "$t/scripts/check-c-uno.sh"
	printf '#!/usr/bin/env bash\nset -euo pipefail\ncd /no/existe\n'                       > "$t/scripts/check-d-crudo.sh"
	# ⛔ Y LA DECLARACIÓN DE SUJETO SE PRUEBA EN LAS DOS DIRECCIONES. Con marca, el censo debe
	#    DECIRLO —para que un verde sin sujeto no se lea como hallazgo—; sin marca, NO debe
	#    inventársela. Sin el segundo caso, «marca los externos» se cumpliría marcándolos todos.
	printf '#!/usr/bin/env bash\n# CENSUS-SUBJECT: external\nset -euo pipefail\nexit 0\n'      > "$t/scripts/check-e-externo.sh"
	( cd "$t" && git init -q -b main . && git add -A ) >/dev/null 2>&1
	salida="$(OLIVARES_CENSUS_ROOT="$t" bash "$0" --run 2>&1)"
	espera() { # <fichero> <clase esperada>
		if printf '%s' "$salida" | grep -qE "^$1 +$2"; then
			ok=$((ok + 1)); printf '  ok    %-40s %s\n' "$1" "$2"
		else
			ko=$((ko + 1)); printf '  FALLO %-40s esperaba %s · dijo: %s\n' "$1" "$2" \
				"$(printf '%s' "$salida" | grep -E "^$1" | head -1)"
		fi
	}
	echo "census-blind-verdict self-test"
	espera check-a-pasa.sh   PASA-CIEGO
	espera check-b-dos.sh    RECHAZA-2
	espera check-c-uno.sh    RECHAZA-1
	espera check-d-crudo.sh  CRUDO-1
	if printf '%s' "$salida" | grep -qE '^check-e-externo\.sh +PASA-CIEGO.*sujeto-externo declarado'; then
		ok=$((ok + 1)); printf '  ok    %-40s %s\n' 'check-e-externo.sh' 'PASA-CIEGO + marca declarada'
	else
		ko=$((ko + 1)); printf '  FALLO %-40s sin la marca: %s\n' 'check-e-externo.sh' \
			"$(printf '%s' "$salida" | grep -E '^check-e-externo' | head -1)"
	fi
	# ⛔ CONTROL NEGATIVO: el que NO la declara no puede recibirla.
	if printf '%s' "$salida" | grep -E '^check-a-pasa\.sh' | grep -q 'sujeto-externo'; then
		ko=$((ko + 1)); printf '  FALLO %-40s recibió una marca que no declara\n' 'check-a-pasa.sh'
	else
		ok=$((ok + 1)); printf '  ok    %-40s %s\n' 'check-a-pasa.sh' 'sin declararla, no la recibe'
	fi
	rm -rf "$t"
	printf 'census-blind-verdict self-test: %d pasan, %d fallan\n' "$ok" "$ko"
	[ "$ko" -eq 0 ]
}

case "${1:-}" in
--selftest) self_test ;;
--run | '') censo ;;
*) echo "uso: $0 [--run|--selftest]" >&2; exit 2 ;;
esac
