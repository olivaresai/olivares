#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-community-cockpit-strings.sh — the battery for
# scripts/check-community-cockpit-strings.sh.
#
# TWO KINDS OF CASE, and the split is deliberate.
#
#  · SYNTHETIC (always): throwaway files standing in for a binary's string table. They
#    exercise the parser, the allowlist, the positive control and the three answers in
#    milliseconds, and each red case is checked by its MESSAGE as well as its rc — a red
#    case that only compares rc passes without ever reaching its guard, which is a
#    failure this house has measured.
#
#  · REAL (default on, OLIVARES_COCKPIT_STRINGS_REAL=0 to skip): builds the actual
#    community binary, injects a private literal by MUTATION into a copy of it and
#    requires red, then requires green on the unmutated one. Without it the battery
#    would only prove the parser works on files somebody wrote for it.
#
# Three answers: 0 all cases held · 1 a case did not · 2 could not run.
set -uo pipefail
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT, y no es higiene: `GIT_DIR` se HEREDA. git lo exporta a todo hook
# `pre-push`, así que este guion —que corre DENTRO del gancho y además crea repos/directorios
# desechables con `mktemp -d`— vería el `.git` de OTRO árbol y mediría el binario o el árbol
# equivocado. Lo cazó `lint:git-env` rechazando mi push: *«pairs 'mktemp -d' with git, does not
# source lib/git-env.sh, and does not refuse a poisoned GIT_DIR»*.
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacía falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "$(basename "$0"): FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}"
[ -n "$ROOT" ] || { echo "battery: COULD NOT RUN — not inside a git work tree" >&2; exit 2; }
cd "$ROOT" || exit 2
GATE="$ROOT/scripts/check-community-cockpit-strings.sh"
[ -x "$GATE" ] || { echo "battery: COULD NOT RUN — $GATE is not executable" >&2; exit 2; }

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
WORK="$(mktemp -d 2>/dev/null)" || { echo "battery: COULD NOT RUN — no scratch dir (TMPDIR=${TMPDIR:-unset})" >&2; exit 2; }
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0; ran=0
# check <name> <expected-rc> <expected-substring-in-output> <file>
check() {
	local name="$1" want="$2" needle="$3" file="$4" out rc
	ran=$((ran + 1))
	out="$(bash "$GATE" "$file" 2>&1)"; rc=$?
	if [ "$rc" != "$want" ]; then
		echo "  ✗ $name: rc=$rc, esperado $want"; echo "$out" | sed 's/^/      /'
		fail=$((fail + 1)); return
	fi
	# The message is checked too, so a case cannot pass by failing for another reason.
	#
	# ⛔ SIN TUBERÍA, y no es estilo. Bajo `set -o pipefail`, `printf … | grep -qF` devuelve
	# **141 CUANDO ENCUENTRA**: grep sale al primer casamiento, cierra su extremo, el productor
	# recibe SIGPIPE y pipefail propaga ese 141 — la comprobación falla justo cuando acierta, de
	# forma intermitente y sólo con salidas grandes. Lo cobra `lint:sigpipe-booleans`, y me lo
	# cobró: esta batería subía la deuda de 0 a 2. `case` sobre la variable no abre tubería.
	if [ -n "$needle" ] && case "$out" in *"$needle"*) false ;; *) true ;; esac; then
		echo "  ✗ $name: rc correcto ($rc) pero el mensaje no menciona «$needle» — la guarda que"
		echo "     este caso ejerce puede no haberse ejecutado."
		echo "$out" | sed 's/^/      /'
		fail=$((fail + 1)); return
	fi
	echo "  ✓ $name"
	pass=$((pass + 1))
}

# The five allowed literals, as a binary that carries exactly the placeholder would.
clean="$WORK/clean.bin"
{
	printf 'unrelated string\n'
	# ⛔ LOS OCHO, no una muestra: desde que REQUIRED es «todo literal permitido», un
	# fixture con dos de ellos ya no acredita que el placeholder esté presente — que es
	# justo el hueco que el contraste señaló en el control positivo del gate.
	command grep -vE '^[[:space:]]*(#|$)' "$ROOT/scripts/community-session-cockpit-strings.allow"
	printf 'some/other/package/path\n'
} > "$clean"

echo "SINTÉTICOS"
check "control positivo: los OCHO literales permitidos y nada más" 0 "OK" "$clean"

mut="$WORK/route.bin"; cp "$clean" "$mut"; printf '/v1/m/session-cockpit/input-sessions\n' >> "$mut"
check "ruta privada completa → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/proto.bin"; cp "$clean" "$mut"; printf 'olivares.cockpit.agent.v1.InputFrame\n' >> "$mut"
check "nombre del proto → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/xterm.bin"; cp "$clean" "$mut"; printf '@xterm/xterm\n' >> "$mut"
check "chunk de xterm → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/symbol.bin"; cp "$clean" "$mut"; printf 'enterprise/sessioncockpit/input.(*AuthorizedInputSink).Write\n' >> "$mut"
check "símbolo del engine privado → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/rename.bin"; cp "$clean" "$mut"; printf 'SessionCockpitAgentClient\n' >> "$mut"
check "renombrado del servicio → rojo (una denylist de nombres no lo vería)" 1 "FUERA DE LA ALLOWLIST" "$mut"

# ⛔ LOS DOS SIGUIENTES LOS ENCONTRÓ EL CONTRASTE ADVERSARIAL (Codex sol, 2026-09-02), no yo,
# y por eso viven aquí: un hallazgo que sólo se arregla vuelve; uno que además se fija en el
# banco no. Los dos eran FALSOS VERDES — la clase silenciosa.
mut="$WORK/chunk.bin"; cp "$clean" "$mut"; printf 'session-cockpit.js\n' >> "$mut"
check "chunk privado 'session-cockpit.js' → rojo (el '.' no puede borrar el marcador)" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/exempt-cross.bin"; cp "$clean" "$mut"
printf 'github.com/olivaresai/olivares/modules/sessioncockpit/github.com/olivaresai/olivares/enterprise/sessioncockpit/input.Write\n' >> "$mut"
check "exención del placeholder que cruza a enterprise/ → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

# ⛔ LOS TRES SIGUIENTES SON LOS FALSOS VERDES QUE EL CONTRASTE ADVERSARIAL PRODUJO
# CONTRA LA VERSIÓN ANTERIOR, con sus cadenas exactas. Dos se curaron; el tercero es un
# LÍMITE y se fija COMO límite, porque un gate que esconde su punto ciego es peor que uno
# que no lo tiene.
mut="$WORK/exempt-tail.bin"; cp "$clean" "$mut"; printf 'enterprise/sessioncockpit.Placeholder\n' >> "$mut"
check "exención sin anclar tragaba enterprise/…Placeholder → rojo" 1 "FUERA DE LA ALLOWLIST" "$mut"

mut="$WORK/sep-unknown.bin"; cp "$clean" "$mut"; printf 'session-cockpit?input-sessions\n' >> "$mut"
check "separador no enumerado ('?') → rojo por la pasada de formas prohibidas" 1 "FUERA DE LA ALLOWLIST" "$mut"

# EL PUNTO CIEGO, declarado y ejercido. `session-cockpitInput` es lo que produce TANTO un
# símbolo privado con ese nombre COMO el literal permitido pegado a un `Input…` ajeno en
# .rodata. Ninguna regla sobre el carácter siguiente los separa: los dos mundos producen
# una letra. Este caso exige VERDE a propósito, para que el límite sea visible en la
# salida del banco y no una creencia; si algún día se cierra, este caso se pone rojo y
# quien lo cierre lo verá aquí.
mut="$WORK/blind.bin"; cp "$clean" "$mut"; printf 'session-cockpitInput\n' >> "$mut"
check "PUNTO CIEGO declarado: adyacencia empaquetada indistinguible → verde" 0 "OK" "$mut"

# The positive control of the gate itself: a binary that never carried the placeholder
# must NOT be reported clean.
# ⛔ THE DECOY'S PROSE MUST NOT CONTAIN THE WORD IT TESTS THE ABSENCE OF, and this line
# is written that way because the first version did: "a binary with no cockpit at all"
# tripped the marker on its own text, so the case failed with a TRACE finding instead of
# the MISSING-REQUIRED finding it exists to exercise. It had the right rc for the wrong
# reason — which is exactly why every red case here is checked by its message too.
empty="$WORK/nothing.bin"; printf 'a binary with no such surface at all\navailability\n' > "$empty"
check "binario sin placeholder → rojo, no verde" 1 "FALTA" "$empty"

check "binario ilegible → 2, nunca 0" 2 "COULD NOT LOOK" "$WORK/does-not-exist.bin"

# An empty allowlist must refuse rather than call everything clean.
#
# ⛔ ESTE CASO EDITABA EL FICHERO RASTREADO Y LO RESTAURABA EN LA LÍNEA SIGUIENTE. Un `kill`, un
# Ctrl-C o dos corridas a la vez podían dejar el gate REAL con la lista vacía — es decir, la pata
# que comprueba integridad alterando la autoridad de entrada de su propio sujeto. Lo señaló el
# contraste adversarial y lo confirma la clase que `lint:git-env` vigila. Ahora se apunta el gate
# a una allowlist DESECHABLE por variable de entorno y el fichero del árbol no se toca.
ran=$((ran + 1))
tmp_allow="$WORK/allow-empty"; printf '# only a comment\n' > "$tmp_allow"
out="$(OLIVARES_COCKPIT_ALLOWLIST="$tmp_allow" bash "$GATE" "$clean" 2>&1)"; rc=$?
# Misma razón que arriba: `case` en vez de tubería, para que un acierto no pueda salir 141.
if [ "$rc" = 2 ] && case "$out" in *"ZERO literals"*) true ;; *) false ;; esac; then
	echo "  ✓ allowlist vacía → 2 (no llama limpio a nadie)"; pass=$((pass + 1))
else
	echo "  ✗ allowlist vacía: rc=$rc"; echo "$out" | sed 's/^/      /'; fail=$((fail + 1))
fi

if [ "${OLIVARES_COCKPIT_STRINGS_REAL:-1}" = "1" ]; then
	echo "REAL (build de community + mutación del binario)"
	if ! command -v go >/dev/null 2>&1; then
		echo "  ! sin toolchain Go: los casos reales NO se ejecutaron (esto NO es un verde)"
		fail=$((fail + 1))
	else
		bin="$WORK/olivares-community"
		# La misma razón que en el gate: un `go build` pelado incrusta la ruta del
		# checkout en cada referencia de fuente, así que este caso real juzgaba DÓNDE
		# está el repositorio. Medido el 2026-09-18 desde un árbol de trabajo cuyo
		# nombre lleva un marcador: ~1950 hallazgos, todos rutas, y el caso «binario
		# community real → verde» en rojo. build_olivares_bin es la definición única.
		if ! ( . "$ROOT/scripts/lib/build-bin.sh" && build_olivares_bin "$bin" ) 2>"$WORK/build.err"; then
			echo "  ! el binario community no construyó: los casos reales NO se ejecutaron"
			sed 's/^/      /' "$WORK/build.err"
			fail=$((fail + 1))
		else
			check "binario community real → verde" 0 "OK" "$bin"
			# MUTATION: append a private literal to a COPY of the real binary. Appending
			# past the ELF image does not change what the program does, and it is exactly
			# what the gate's discovery reads — the string table.
			mutbin="$WORK/olivares-community-mutated"
			cp "$bin" "$mutbin"
			printf '\n/v1/m/session-cockpit/input-sessions\n' >> "$mutbin"
			check "binario community + literal privado → rojo" 1 "FUERA DE LA ALLOWLIST" "$mutbin"
		fi
	fi
else
	echo "REAL: SALTADO por OLIVARES_COCKPIT_STRINGS_REAL=0 — la corrida NO cubre el binario"
fi

echo
echo "casos ejecutados: $ran · verdes: $pass · rojos: $fail"
[ "$ran" -gt 0 ] || { echo "battery: examinó CERO casos — eso no es un verde" >&2; exit 2; }
[ "$fail" -eq 0 ] || exit 1
exit 0
