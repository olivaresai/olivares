#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-step-env-closure.sh.
#
# Cada caso ROJO va emparejado con el VERDE que prueba que no salta por cualquier cosa — que es la
# mitad que le faltaba a mis dos primeras versiones del detector: acusaban 41 y 33 consumidores
# legítimos por un defecto real, y un gate así se desactiva en vez de usarse.
# ⛔ SIN `-e`: esta bateria EJECUTA fallos a proposito — la mitad de sus casos exigen que el
# portón devuelva rojo. Con `errexit` el guion moria en el primer caso y sólo se veia la cabecera.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="${ROOT}/scripts/check-step-env-closure.sh"
pass=0; fail=0
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT HUP INT TERM

check() {
	if [ "$3" -eq 0 ]; then pass=$((pass+1)); printf '  ok    %-56s %s\n' "$1" "$2"
	else fail=$((fail+1)); printf '  FAIL  %-56s %s\n' "$1" "$2"; fi
}
corre() { rm -rf "$WORK/w"; mkdir -p "$WORK/w"; cat > "$WORK/w/f.yml"; \
	# ⛔ SIN `|| true` AQUI, y esa fue mi tercera trampa de arnés del dia: con el, `$?` valia
	# SIEMPRE cero y los dos casos que exigen rojo pasaban por verdes sin mirar nada.
	OLIVARES_WORKFLOWS_DIR="$WORK/w" bash "$GATE" >"$WORK/o" 2>"$WORK/e"; rc=$?; }

echo "check-step-env-closure — la definicion extraviada, y solo esa"

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -ne 0 ] && grep -q "FOO_BAR" "$WORK/e"
check "consumida en un paso y declarada SOLO en otro: rojo" "la firma del defecto" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: ambos
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 0 ]
check "declarada en SU propio paso: verde" "control" $?

corre <<'YML'
name: t
env:
  FOO_BAR: x
jobs:
  j:
    steps:
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 0 ]
check "declarada en el env: del WORKFLOW: verde" "ambito amplio" $?

corre <<'YML'
name: t
jobs:
  j:
    env:
      FOO_BAR: x
    steps:
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 0 ]
check "declarada en el env: del JOB: verde" "ambito de job" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: exporta
        run: |
          FOO_BAR=x
          echo "FOO_BAR=$FOO_BAR" >> "$GITHUB_ENV"
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 0 ]
check "exportada a GITHUB_ENV por un paso anterior: verde" "persistencia real" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: shell puro
        run: |
          read -r LOPORT HIPORT < /proc/sys/net/ipv4/ip_local_port_range
          echo "${LOPORT}-${HIPORT}"
YML
[ "$rc" -eq 0 ]
check "variable de shell que NUNCA fue env: no se acusa" "no parsea shell, y lo sabe" $?

corre <<'YML'
name: t
jobs:
  a:
    steps:
      - name: define en el job A
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
  b:
    steps:
      - name: consume en el job B
        run: echo "${FOO_BAR}"
YML
[ "$rc" -ne 0 ] && grep -q "FOO_BAR" "$WORK/e"
check "la definicion vive en OTRO job: tambien rojo" "el ambito no cruza jobs" $?

rm -rf "$WORK/w"; mkdir -p "$WORK/w"
OLIVARES_WORKFLOWS_DIR="$WORK/w" bash "$GATE" >"$WORK/o" 2>"$WORK/e" || rc=$?
[ "${rc:-0}" -eq 2 ] && grep -q 'NO HE PODIDO MIRAR' "$WORK/e"
check "un directorio sin workflows da 2, no 0" "tercera respuesta" $?

echo ""
echo "check-step-env-closure battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
