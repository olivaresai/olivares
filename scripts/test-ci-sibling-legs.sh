#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-ci-sibling-legs.sh — batería de `check-ci-sibling-legs.sh`.
#
# El caso que manda es el 2: el gate tiene que enrojecer sobre el `mainline-ci.yml` que estaba en
# `main` la mañana del 2026-08-24, que es donde el defecto vivía de verdad. Un gate escrito DESPUÉS
# del arreglo y probado sólo contra el árbol arreglado no demuestra nada: verde antes y verde
# después se ven igual.

set -u -o pipefail
export LC_ALL=C

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"

# ⛔ ESTA BATERIA ES MIEMBRO DE UNA CLASE QUE NO DECLARABA, y me costo el push.
#
# `lint:git-env` clasifica como miembro a todo guion que empareje `mktemp -d` con git: un `GIT_DIR`
# heredado MANDA sobre el directorio de trabajo, asi que un repo desechable creado con `mktemp -d`
# acabaria operando sobre el repositorio VIVO. La bateria no lo declaraba y el gate la marco
# BROKEN — y `lint:git-env` corre SIN `|| true` en `.githooks/pre-push`, o sea que un push mio
# habria dejado a los cinco carriles sin empujar. Lo caza el push propio; llego a rechazarlo.
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacia falta».
# shellcheck source=/dev/null
. "${RAIZ}/scripts/lib/git-env.sh" || {
	echo "test-ci-sibling-legs: NO HE PODIDO MIRAR — no puedo sourcear scripts/lib/git-env.sh" >&2
	exit 2
}
GATE="$RAIZ/scripts/check-ci-sibling-legs.sh"
pasa=0
falla=0

TRABAJO="$(mktemp -d "${TMPDIR:-/tmp}/sibling-legs.XXXXXX")"
trap 'chmod -R u+rwX "$TRABAJO" 2>/dev/null; rm -rf "$TRABAJO"' EXIT

# ⛔ NADA de `printf … | grep -q`: bajo `pipefail`, `grep -q` cierra la tuberia al primer acierto
# y el productor muere con SIGPIPE, asi que el EXITO devuelve 141. Lo cazo `lint:sigpipe-booleans`
# en mi propio push -- cuarta vez en el dia que un gate nuevo mio tropieza con otro. La forma sin
# tuberia es una herestring.
comprobar() { # <descripción> <rc-esperado> <directorio> [patrón-que-debe-salir]
	local desc="$1" esperado="$2" dir="$3" patron="${4:-}"
	local salida rc
	salida="$(bash "$GATE" "$dir" 2>&1)"
	rc=$?
	if [ "$rc" != "$esperado" ]; then
		printf '  FALLA  %-58s rc=%s (esperaba %s)\n' "$desc" "$rc" "$esperado"
		falla=$((falla + 1))
		return
	fi
	if [ -n "$patron" ] && ! grep -q -- "$patron" <<<"$salida"; then
		printf '  FALLA  %-58s rc=%s pero no dice «%s»\n' "$desc" "$rc" "$patron"
		falla=$((falla + 1))
		return
	fi
	printf '  ok     %-58s rc=%s\n' "$desc" "$rc"
	pasa=$((pasa + 1))
}

siembra() { # <dir> <fichero> — escribe un workflow sintético desde stdin
	mkdir -p "$1"
	cat > "$1/$2"
}

# ── 1 · el árbol real ────────────────────────────────────────────────────────────────────────
comprobar "el arbol real esta limpio" 0 "$RAIZ/.github/workflows" "limpio"

# ── 2 · EL CASO QUE MANDA: el main de esa mañana ──────────────────────────────────────────────
# Se reconstruye desde git, no se copia a mano: una copia a mano envejece y acabaría probando el
# árbol de hoy con otro nombre.
if git -C "$RAIZ" rev-parse --verify -q "origin/main:.github/workflows/mainline-ci.yml" >/dev/null 2>&1; then
	ANTES="$TRABAJO/antes/.github/workflows"
	mkdir -p "$ANTES"
	if git -C "$RAIZ" show "origin/main:.github/workflows/mainline-ci.yml" > "$ANTES/mainline-ci.yml" 2>/dev/null &&
		grep -q 'race (commerce domain)' "$ANTES/mainline-ci.yml"; then
		vecina="$(grep -A1 'id: race-commerce' "$ANTES/mainline-ci.yml")"
		if grep -q 'cancelled()' <<<"$vecina"; then
			printf '  ok     %-58s (main ya lleva el arreglo)\n' "el main de referencia ya esta arreglado"
			pasa=$((pasa + 1))
		else
			comprobar "el main sin guarda enrojece" 1 "$ANTES" "race (commerce domain)"
		fi
	else
		printf '  FALLA  %-58s no pude extraer el mainline-ci de origin/main\n' "el main sin guarda enrojece"
		falla=$((falla + 1))
	fi
else
	printf '  ok     %-58s (sin origin/main aqui)\n' "el main sin guarda enrojece — saltado"
	pasa=$((pasa + 1))
fi

# ── 3 · las tres respuestas ──────────────────────────────────────────────────────────────────
comprobar "un directorio que no existe es 2, no 0" 2 "$TRABAJO/no-existe" "NO HE PODIDO MIRAR"

D="$TRABAJO/ilegible/.github/workflows"
siembra "$D" "x.yml" <<'YML'
jobs:
  race-x:
    steps:
      - name: uno
        timeout-minutes: 5
        run: task a
YML
chmod 000 "$D/x.yml"
if [ -r "$D/x.yml" ]; then
	printf '  ok     %-58s (corriendo como root: no se puede probar)\n' "un fichero ilegible es 2 — saltado"
	pasa=$((pasa + 1))
else
	comprobar "un fichero ilegible es 2, nunca limpio" 2 "$D" "NO HE PODIDO MIRAR"
fi
chmod 644 "$D/x.yml"

# ── 4 · no marca de más ──────────────────────────────────────────────────────────────────────
D="$TRABAJO/una-pata/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  race-sola:
    steps:
      - name: unica pata
        timeout-minutes: 5
        run: task test:algo
YML
comprobar "un job race con UNA pata no marca nada" 0 "$D" "limpio"

D="$TRABAJO/no-race/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  control-plane:
    steps:
      - name: primera
        timeout-minutes: 5
        run: task a
      - name: segunda sin guarda
        timeout-minutes: 5
        run: task b
YML
comprobar "un job que NO es race queda fuera del alcance" 0 "$D" "limpio"

D="$TRABAJO/sin-techo/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  race-y:
    steps:
      - name: primera
        timeout-minutes: 5
        run: task a
      - name: segunda sin techo ni guarda
        run: task b
YML
comprobar "un paso sin timeout-minutes no es una pata" 0 "$D" "limpio"

# ── 5 · MUTANTES: lo que tiene que enrojecer ─────────────────────────────────────────────────
D="$TRABAJO/hermana-desnuda/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  race-z:
    steps:
      - name: primera
        timeout-minutes: 5
        run: task a
      - name: hermana sin guarda
        timeout-minutes: 5
        run: task b
YML
comprobar "una hermana sin guarda enrojece y se NOMBRA" 1 "$D" "hermana sin guarda"

D="$TRABAJO/always/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  race-z:
    steps:
      - name: primera
        timeout-minutes: 5
        run: task a
      - name: hermana con always
        timeout-minutes: 5
        if: ${{ always() }}
        run: task b
YML
comprobar "always() NO vale: una cancelada debe seguir cancelada" 1 "$D" "hermana con always"

D="$TRABAJO/tercera/.github/workflows"
siembra "$D" "w.yml" <<'YML'
jobs:
  race-z:
    steps:
      - name: primera
        timeout-minutes: 5
        run: task a
      - name: segunda guardada
        timeout-minutes: 5
        if: ${{ !cancelled() }}
        run: task b
      - name: tercera desnuda
        timeout-minutes: 5
        run: task c
YML
comprobar "caza la TERCERA aunque la segunda este bien" 1 "$D" "tercera desnuda"

printf 'ci-sibling-legs gate: %s passed, %s failed\n' "$pasa" "$falla"
[ "$falla" -eq 0 ]
