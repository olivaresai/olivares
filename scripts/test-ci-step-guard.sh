#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-ci-step-guard.sh — batería de `check-ci-step-guard.sh`.
#
# Las tres respuestas se prueban por separado, y el LÍMITE se prueba por los dos lados: un gate
# que sólo se prueba con su caso rojo no distingue «caza lo que busca» de «lo caza todo».
#
# ⛔ Y LLEVA CONTROL NEGATIVO. La primera versión de una batería así mide el fixture, no el gate:
# si el fixture limpio saliera rojo, todos los «ok» de abajo serían el mismo rojo repetido.

set -u -o pipefail
export LC_ALL=C

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
SUT="$RAIZ/scripts/check-ci-step-guard.sh"
[ -x "$SUT" ] || { echo "test-ci-step-guard: ⛔ COULD NOT LOOK: $SUT is not executable" >&2; exit 2; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/stepguard.XXXXXX")" || exit 2
[ -d "$TMP" ] || exit 2
trap 'rm -rf "$TMP"' EXIT

pass_count=0; fail_count=0

ok()   { printf '  ok    %-58s %s\n' "$1" "${2:-}"; pass_count=$((pass_count+1)); }
malo() { printf '  FAIL  %-58s %s\n' "$1" "${2:-}" >&2; fail_count=$((fail_count+1)); }

# comprueba <titulo> <rc-esperado> <dir> [cadena-que-debe-aparecer]
comprueba() {
	local titulo="$1" esperado="$2" dir="$3" cadena="${4:-}"
	local output rc
	output="$("$SUT" "$dir" 2>&1)"
	rc=$?
	if [ "$rc" -ne "$esperado" ]; then
		malo "$titulo" "rc=$rc, expected $esperado"
		return
	fi
	# SIN TUBERIA, y no es estilo: `printf … | grep -q` bajo `set -o pipefail` devuelve **141
	# CUANDO ENCUENTRA** — grep sale al primer casamiento, cierra su extremo, printf recibe
	# SIGPIPE y pipefail propaga ese 141. La comprobacion falla justo cuando acierta, y de forma
	# intermitente. Lo cazo `lint:sigpipe-booleans` en el push de este mismo commit.
	if [ -n "$cadena" ]; then
		case "$output" in
		*"$cadena"*) ;;
		*)
			malo "$titulo" "correct rc but does not report «$cadena»"
			return
			;;
		esac
	fi
	ok "$titulo" "rc=$rc"
}

nuevo_dir() { local d="$TMP/$1"; mkdir -p "$d"; printf '%s' "$d"; }

# ── job POR ENCIMA del umbral y SIN guarda de paso: es el hallazgo ────────────────────────────
D="$(nuevo_dir rojo)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  lento:
    runs-on: ubuntu-latest
    timeout-minutes: 90
    steps:
      - name: lo que tarda
        run: sleep 1
YAML
comprueba "a 90-minute job without a step guard is a FINDING" 1 "$D" "«lento»"

# ── el mismo job CON guarda: limpio ──────────────────────────────────────────────────────────
D="$(nuevo_dir verde)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  lento:
    runs-on: ubuntu-latest
    timeout-minutes: 90
    steps:
      - name: lo que tarda
        timeout-minutes: 60
        run: sleep 1
YAML
comprueba "the SAME job with a step guard is clean" 0 "$D" "CLEAN"

# ── CONTROL NEGATIVO: el fixture limpio tiene que salir limpio, o mide el fixture ─────────────
if [ "$("$SUT" "$D" >/dev/null 2>&1; echo $?)" = "0" ]; then
	ok "negative control: the clean fixture does NOT flag itself" "rc=0"
else
	malo "negative control" "clean fixture returns red: test would measure the fixture"
fi

# ── EL LÍMITE, POR LOS DOS LADOS. El umbral es ESTRICTAMENTE MAYOR ────────────────────────────
D="$(nuevo_dir justo)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  justo:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: sin guarda
        run: sleep 1
YAML
comprueba "EXACTLY at the threshold (30) is NOT a finding" 0 "$D" "CLEAN"

D="$(nuevo_dir pasado)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  pasado:
    runs-on: ubuntu-latest
    timeout-minutes: 31
    steps:
      - name: sin guarda
        run: sleep 1
YAML
comprueba "ONE MINUTE above the threshold (31) IS a finding" 1 "$D" "«pasado»"

# ── un job barato sin guarda no es hallazgo: es el falso positivo que haria ignorar el gate ───
D="$(nuevo_dir barato)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  barato:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - name: sin guarda
        run: sleep 1
YAML
comprueba "a 5-minute job without a guard is NOT a finding" 0 "$D" "CLEAN"

# ── varios jobs: se nombran TODOS los que incumplen, no solo el primero ───────────────────────
D="$(nuevo_dir varios)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  uno:
    runs-on: ubuntu-latest
    timeout-minutes: 90
    steps:
      - name: a
        run: sleep 1
  dos:
    runs-on: ubuntu-latest
    timeout-minutes: 45
    steps:
      - name: b
        run: sleep 1
YAML
output="$("$SUT" "$D" 2>&1)"; rc=$?
nombra_los_dos=false
case "$output" in
*'«uno»'*)
	case "$output" in
	*'«dos»'*) nombra_los_dos=true ;;
	esac
	;;
esac
if [ "$rc" = 1 ] && [ "$nombra_los_dos" = true ]; then
	ok "both noncompliant jobs are NAMED" "rc=1"
else
	malo "two noncompliant jobs" "rc=$rc and does not name both"
fi

# ── el umbral es configurable, y moverlo cambia el veredicto ──────────────────────────────────
D="$(nuevo_dir umbral)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  medio:
    runs-on: ubuntu-latest
    timeout-minutes: 40
    steps:
      - name: sin guarda
        run: sleep 1
YAML
if [ "$(OLIVARES_STEP_GUARD_MIN=60 "$SUT" "$D" >/dev/null 2>&1; echo $?)" = "0" ]; then
	ok "with threshold 60, a 40-minute job is no longer a finding" "rc=0"
else
	malo "configurable threshold" "threshold is not honored"
fi

# ── LAS TRES RESPUESTAS: el tercer caso es un CODIGO, no una frase ────────────────────────────
comprueba "a nonexistent directory is COULD NOT LOOK" 2 "$TMP/no-existe" "COULD NOT CHECK"

D="$(nuevo_dir vacio)"
comprueba "a directory WITHOUT workflows is COULD NOT LOOK" 2 "$D" "COULD NOT CHECK"

D="$(nuevo_dir ilegible)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  x:
    timeout-minutes: 90
    steps:
      - name: a
        run: sleep 1
YAML
chmod 000 "$D/w.yml"
if [ "$(id -u)" = "0" ]; then
	printf '  skip  %-58s %s\n' "an unreadable file is COULD NOT LOOK" "(root can still read it)"
else
	comprueba "an UNREADABLE file is COULD NOT LOOK, not clean" 2 "$D" "COULD NOT CHECK"
fi
chmod 644 "$D/w.yml" 2>/dev/null || true

# ── un umbral que no es un numero no se resuelve a favor del verde ────────────────────────────
D="$(nuevo_dir malumbral)"
cat > "$D/w.yml" <<'YAML'
name: w
on: {workflow_dispatch: {}}
jobs:
  x:
    timeout-minutes: 90
    steps:
      - name: a
        run: sleep 1
YAML
if [ "$(OLIVARES_STEP_GUARD_MIN=cuarenta "$SUT" "$D" >/dev/null 2>&1; echo $?)" = "2" ]; then
	ok "a nonnumeric threshold is COULD NOT LOOK" "rc=2"
else
	malo "nonnumeric threshold" "does not return 2"
fi

# ── EL ARBOL DE VERDAD: este repositorio tiene que estar limpio ───────────────────────────────
if [ -d "$RAIZ/.github/workflows" ]; then
	comprueba "the real .github/workflows tree is clean" 0 "$RAIZ/.github/workflows" "CLEAN"
fi

printf '\ncheck-ci-step-guard selftest: %d passed, %d failed\n' "$pass_count" "$fail_count"
[ "$fail_count" -eq 0 ] || exit 1
exit 0
