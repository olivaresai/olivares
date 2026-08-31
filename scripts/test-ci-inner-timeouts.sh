#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Batería de check-ci-inner-timeouts.sh — hermética: workflows de fixture, sin red ni repositorio real.
set -u -o pipefail
RAIZ=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SUT="${SUT:-$RAIZ/scripts/check-ci-inner-timeouts.sh}"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/cit.XXXXXX"); trap 'rm -rf "$TMP"' EXIT
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'ok   %-58s rc=%s\n' "$1" "$3"
         else FAIL=$((FAIL+1)); printf 'FAIL %-58s esperaba %s, dio %s\n' "$1" "$2" "$3"; fi; }
corre(){ OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$1" OLIVARES_INNER_MIN="${2:-1}" \
         bash "$SUT" > "$TMP/out" 2>&1; echo $?; }
wf(){ mkdir -p "$TMP/$1"; cat > "$TMP/$1/w.yml"; printf '%s' "$TMP/$1"; }

# 1 · EL PASO REAL DE mainline-ci.yml:2839 — debe CAER (60 < 80 + 1)
D=$(wf real <<'Y'
on: {push: {branches: [main]}}
jobs:
  secrets:
    runs-on: ubuntu-latest
    steps:
      - name: gitleaks (sweep over every ref)
        id: gitleaks
        timeout-minutes: 60
        run: |
          timeout --signal=TERM --kill-after=60s 80m task lint:secrets
Y
)
check "(1) el paso REAL de gitleaks CAE" 1 "$(corre "$D")"
check "(1b) y nombra su techo y su reloj" 0 "$( grep -q 'techo del paso 60 min' "$TMP/out" && grep -q '80 min' "$TMP/out"; echo $? )"
check "(1c) y cuenta el kill-after, no lo confunde con el reloj" 0 "$( grep -q 'kill-after 1 min' "$TMP/out"; echo $? )"

# 2 · EL MISMO PASO CORREGIDO — debe PASAR
D=$(wf sano <<'Y'
on: {push: {branches: [main]}}
jobs:
  secrets:
    runs-on: ubuntu-latest
    steps:
      - name: gitleaks (sweep over every ref)
        id: gitleaks
        timeout-minutes: 90
        run: |
          timeout --signal=TERM --kill-after=60s 80m task lint:secrets
Y
)
check "(2) el mismo paso con techo 90 PASA" 0 "$(corre "$D")"

# 3 · la segunda forma: el reloj INTERNO de otro comando
D=$(wf goflag <<'Y'
on: {push: {branches: [main]}}
jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: e2e
        run: go test ./... -timeout 45m
Y
)
check "(3) '-timeout 45m' bajo un techo de JOB de 30 CAE" 1 "$(corre "$D")"

# 4 · NO-DESCARGA · un reloj que SÍ cabe no debe encenderse
D=$(wf cabe <<'Y'
on: {push: {branches: [main]}}
jobs:
  e2e:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    steps:
      - name: e2e
        run: kubectl rollout status deploy/x --timeout=180s
Y
)
check "(4) un reloj que CABE no se enciende" 0 "$(corre "$D")"

# 5 · NO-DESCARGA · sin reloj interior no hay nada que juzgar
D=$(wf sinreloj <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - name: nada
        run: echo hola
Y
)
check "(5) sin reloj interior sale limpio" 0 "$(corre "$D")"

# 6 · el techo del PASO manda sobre el del JOB
D=$(wf paso <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    timeout-minutes: 300
    steps:
      - name: corto
        timeout-minutes: 5
        run: go test ./... -timeout 45m
Y
)
check "(6) el techo del PASO manda sobre el del JOB" 1 "$(corre "$D")"

# 7-9 · FAIL-CLOSED
check "(7) directorio inexistente -> 2" 2 "$( OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR=/no/existe bash "$SUT" >/dev/null 2>&1; echo $? )"
mkdir -p "$TMP/vacio"
check "(8) menos workflows que el mínimo -> 2" 2 "$( OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$TMP/vacio" OLIVARES_INNER_MIN=1 bash "$SUT" >/dev/null 2>&1; echo $? )"
mkdir -p "$TMP/roto"; printf 'jobs: [esto: no\n  es: yaml\n' > "$TMP/roto/w.yml"
check "(9) YAML ilegible -> 2, no 0" 2 "$( OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$TMP/roto" OLIVARES_INNER_MIN=1 bash "$SUT" >/dev/null 2>&1; echo $? )"
check "(10) argumento posicional -> 2" 2 "$( OLIVARES_ROOT="$RAIZ" bash "$SUT" loquesea >/dev/null 2>&1; echo $? )"

# 12 · FRONTERA · aquí el kill-after DECIDE, y por eso mata al mutante que lo quita
D=$(wf frontera-cae <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: justo por debajo
        timeout-minutes: 11
        run: timeout --kill-after=2m 10m task algo
Y
)
check "(12a) FRONTERA techo=interior+kill-1 CAE" 1 "$(corre "$D")"
D=$(wf frontera-pasa <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: justo encima
        timeout-minutes: 13
        run: timeout --kill-after=2m 10m task algo
Y
)
check "(12b) FRONTERA techo=interior+kill+margen PASA" 0 "$(corre "$D")"

# 13 · COBERTURA · bandera con valor SEPARADO (el defecto que the reviewer encontró)
D=$(wf separada <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: signal separado
        timeout-minutes: 5
        run: timeout --signal TERM 10m task algo
Y
)
check "(13) 'timeout --signal TERM 10m' (valor separado) CAE" 1 "$(corre "$D")"

# 14 · FAIL-OPEN CERRADO · un workflow de solo `uses:` no es un verde
D=$(wf solouses <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
Y
)
check "(14) workflow SIN ningún run: -> 2, no 0" 2 "$(corre "$D")"

# 16-18 · LA DIMENSION QUE FALTABA: el reloj no siempre es un literal del `run:`
D=$(wf var <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    env: {INNER_TIMEOUT: 10m}
    steps:
      - name: reloj por variable
        timeout-minutes: 5
        run: timeout "$INNER_TIMEOUT" task algo
Y
)
check "(16) reloj en \$VAR con env estático se RESUELVE y CAE" 1 "$(corre "$D")"
D=$(wf goflags <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    env: {GOFLAGS: -timeout=45m}
    timeout-minutes: 30
    steps:
      - name: goflags
        run: go test ./...
Y
)
check "(17) reloj escondido en GOFLAGS del env CAE" 1 "$(corre "$D")"
D=$(wf expr <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: expresion
        timeout-minutes: 5
        run: timeout ${{ vars.T }} task algo
Y
)
check "(18) una expresión \${{ }} en la duración -> 2, NUNCA limpio" 2 "$(corre "$D")"

# 19 · NO-DESCARGA · variables en el shell que NO tocan el reloj no deben dar 2
D=$(wf varsuelta <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: literal con variables alrededor
        timeout-minutes: 90
        run: |
          timeout --kill-after=60s 80m task algo | tee "$RUNNER_TEMP/x.log"
          rc=$?
Y
)
check "(19) variables FUERA de la duración no dan 2" 0 "$(corre "$D")"

# 20-24 · EL RELOJ QUE VIVE EN EL TASKFILE (el punto ciego que este gate declaraba)
cat > "$TMP/Taskfile.yml" <<'TF'
version: '3'
tasks:
  test:race-hot:modules:
    cmds:
      - bash scripts/go-work-each.sh go test -race -count=1 -timeout 150m ./...
  con-dep:
    deps: [test:race-hot:modules]
    cmds: [echo hola]
  con-var:
    # ⛔ ENTRECOMILLADO: `{{.T}}` sin comillas abre un mapa de flujo en YAML y **el Taskfile entero
    # deja de parsear**, asi que los SEIS casos salian 2 por un fallo del banco, no del sujeto.
    cmds: ["go test -timeout {{.T}} ./..."]
TF
corretf(){ OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$1" OLIVARES_INNER_MIN=1            OLIVARES_INNER_TASKFILE="$TMP/Taskfile.yml" bash "${2:-$SUT}" > "$TMP/out" 2>&1; echo $?; }

D=$(wf tf-cabe <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: race (./modules, on Postgres)
        timeout-minutes: 210
        run: task test:race-hot:modules
Y
)
check "(20) el caso REAL: 150m bajo techo 210 CABE" 0 "$(corretf "$D")"
D2=$(wf tf-no-cabe <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: race (./modules, on Postgres)
        timeout-minutes: 140
        run: task test:race-hot:modules
Y
)
check "(21) con el techo a 140 CAE" 1 "$(corretf "$D2")"
check "(21b) y NOMBRA la tarea donde vive el reloj" 0 "$( grep -q 'dentro de .task test:race-hot:modules' "$TMP/out"; echo $? )"

D3=$(wf tf-dep <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: por deps
        timeout-minutes: 140
        run: task con-dep
Y
)
check "(22) el reloj llega por 'deps:' y CAE igual" 1 "$(corretf "$D3")"

D4=$(wf tf-falta <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: tarea inexistente
        timeout-minutes: 10
        run: task no-existe-jamas
Y
)
check "(23) una tarea que el Taskfile NO tiene -> 2" 2 "$(corretf "$D4")"

D5=$(wf tf-var <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: reloj con plantilla
        timeout-minutes: 10
        run: task con-var
Y
)
check "(24) un {{.VAR}} en la duración -> 2, nunca limpio" 2 "$(corretf "$D5")"

# ⛔ NO-DESCARGA · `task` en PROSA de un comentario no debe disparar nada. Mi primera version casaba
# «task no esta en PATH» y dio 24 falsos «no pude mirar» sobre el arbol real.
D6=$(wf tf-prosa <<'Y'
on: {push: {branches: [main]}}
jobs:
  x:
    runs-on: ubuntu-latest
    steps:
      - name: prosa
        timeout-minutes: 10
        run: |
          # command -v task || { echo "task no esta en PATH"; exit 1; }
          echo hola
Y
)
check "(25) 'task' en un COMENTARIO no dispara nada" 0 "$(corretf "$D6")"

# 11 · MUTANTE · quitar la comparación
cat > "$TMP/mut.py" <<PYEOF
import sys
o = open(sys.argv[1]).read()
v = "                if float(techo) < exige:"
n = "                if False:"
assert o.count(v) == 1, "el patron del mutante no casa"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mut.py" "$SUT" "$TMP/m.sh" || { echo "MUTANTE NO FABRICADO"; exit 1; }
chmod +x "$TMP/m.sh"
check "(11a) el mutante REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m.sh" && echo 1 || echo 0 )"
D=$(wf mut <<'Y'
on: {push: {branches: [main]}}
jobs:
  secrets:
    runs-on: ubuntu-latest
    steps:
      - name: gitleaks
        timeout-minutes: 60
        run: timeout --kill-after=60s 80m task lint:secrets
Y
)
check "(11b) MUTANTE 'sin comparación' es CAZADO" 0 \
  "$( OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$D" OLIVARES_INNER_MIN=1 bash "$TMP/m.sh" >/dev/null 2>&1
      m=$?; [ "$m" -eq 0 ] && echo 0 || echo "el mutante siguió dando $m: caso invalido" )"

# 15 · MUTANTE · quitar el `+ K` (el kill-after deja de contar). Muere en la FRONTERA de (12a):
# sin K la exigencia baja de 13 a 11 y el techo 11 pasa a caber, o sea el hallazgo desaparece.
# Con gitleaks NO moriria —60 < 80 ya cae sin kill-after—, que es justo lo que the reviewer senalo:
# un mutante solo acredita si existe un caso donde su dimension DECIDE.
cat > "$TMP/mut2.py" <<PYEOF
import sys
o = open(sys.argv[1]).read()
v = "                exige = D + K + margen"
n = "                exige = D + margen"
assert o.count(v) == 1, "el patron del mutante no casa"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mut2.py" "$SUT" "$TMP/m2.sh" || { echo "MUTANTE NO FABRICADO"; exit 1; }
check "(15a) el mutante del kill-after REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m2.sh" && echo 1 || echo 0 )"
check "(15b) MUTANTE 'sin kill-after' es CAZADO en la FRONTERA" 0 \
  "$( OLIVARES_ROOT="$RAIZ" OLIVARES_INNER_WFDIR="$TMP/frontera-cae" OLIVARES_INNER_MIN=1 bash "$TMP/m2.sh" >/dev/null 2>&1
      m=$?; [ "$m" -eq 0 ] && echo 0 || echo "el mutante siguió dando $m: caso invalido" )"

echo
echo "check-ci-inner-timeouts selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
