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
[ -x "$SUT" ] || { echo "test-ci-step-guard: ⛔ NO HE PODIDO MIRAR: no ejecutable $SUT" >&2; exit 2; }

TMP="$(mktemp -d "${TMPDIR:-/tmp}/stepguard.XXXXXX")" || exit 2
[ -d "$TMP" ] || exit 2
trap 'rm -rf "$TMP"' EXIT

pasan=0; fallan=0

ok()   { printf '  ok    %-58s %s\n' "$1" "${2:-}"; pasan=$((pasan+1)); }
malo() { printf '  FALLO %-58s %s\n' "$1" "${2:-}" >&2; fallan=$((fallan+1)); }

# comprueba <titulo> <rc-esperado> <dir> [cadena-que-debe-aparecer]
comprueba() {
	local titulo="$1" esperado="$2" dir="$3" cadena="${4:-}"
	local salida rc
	salida="$("$SUT" "$dir" 2>&1)"
	rc=$?
	if [ "$rc" -ne "$esperado" ]; then
		malo "$titulo" "rc=$rc, esperaba $esperado"
		return
	fi
	# SIN TUBERIA, y no es estilo: `printf … | grep -q` bajo `set -o pipefail` devuelve **141
	# CUANDO ENCUENTRA** — grep sale al primer casamiento, cierra su extremo, printf recibe
	# SIGPIPE y pipefail propaga ese 141. La comprobacion falla justo cuando acierta, y de forma
	# intermitente. Lo cazo `lint:sigpipe-booleans` en el push de este mismo commit.
	if [ -n "$cadena" ]; then
		case "$salida" in
		*"$cadena"*) ;;
		*)
			malo "$titulo" "rc correcto pero no dice «$cadena»"
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
comprueba "un job de 90 min sin guarda de paso es HALLAZGO" 1 "$D" "«lento»"

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
comprueba "el MISMO job con una guarda de paso queda limpio" 0 "$D" "CLEAN"

# ── CONTROL NEGATIVO: el fixture limpio tiene que salir limpio, o mide el fixture ─────────────
if [ "$("$SUT" "$D" >/dev/null 2>&1; echo $?)" = "0" ]; then
	ok "control negativo: el fixture limpio NO se acusa a sí mismo" "rc=0"
else
	malo "control negativo" "el fixture limpio sale rojo: la batería mediría el fixture"
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
comprueba "EXACTAMENTE en el umbral (30) NO es hallazgo" 0 "$D" "CLEAN"

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
comprueba "UN MINUTO por encima del umbral (31) SI es hallazgo" 1 "$D" "«pasado»"

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
comprueba "un job de 5 min sin guarda NO es hallazgo" 0 "$D" "CLEAN"

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
salida="$("$SUT" "$D" 2>&1)"; rc=$?
nombra_los_dos=false
case "$salida" in
*'«uno»'*)
	case "$salida" in
	*'«dos»'*) nombra_los_dos=true ;;
	esac
	;;
esac
if [ "$rc" = 1 ] && [ "$nombra_los_dos" = true ]; then
	ok "dos jobs que incumplen se NOMBRAN los dos" "rc=1"
else
	malo "dos jobs que incumplen" "rc=$rc y no nombra a los dos"
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
	ok "con el umbral en 60, un job de 40 deja de ser hallazgo" "rc=0"
else
	malo "umbral configurable" "el umbral no se honra"
fi

# ── LAS TRES RESPUESTAS: el tercer caso es un CODIGO, no una frase ────────────────────────────
comprueba "un directorio que no existe es NO HE PODIDO MIRAR" 2 "$TMP/no-existe" "NO HE PODIDO MIRAR"

D="$(nuevo_dir vacio)"
comprueba "un directorio SIN workflows es NO HE PODIDO MIRAR" 2 "$D" "NO HE PODIDO MIRAR"

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
	printf '  skip  %-58s %s\n' "un fichero ilegible es NO HE PODIDO MIRAR" "(root lo lee igual)"
else
	comprueba "un fichero ILEGIBLE es NO HE PODIDO MIRAR, no limpio" 2 "$D" "NO HE PODIDO MIRAR"
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
	ok "un umbral no numerico es NO HE PODIDO MIRAR" "rc=2"
else
	malo "umbral no numerico" "no responde 2"
fi

# ── EL ARBOL DE VERDAD: este repositorio tiene que estar limpio ───────────────────────────────
if [ -d "$RAIZ/.github/workflows" ]; then
	comprueba "el arbol real de .github/workflows esta limpio" 0 "$RAIZ/.github/workflows" "CLEAN"
fi

printf '\ncheck-ci-step-guard selftest: %d pasan, %d fallan\n' "$pasan" "$fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
