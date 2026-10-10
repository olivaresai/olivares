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

echo "check-step-env-closure — the misplaced definition, and only that"

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
check "consumed in one step and declared ONLY in another: red" "the defect signature" $?

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
check "declared in ITS own step: green" "control" $?

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
check "declared in WORKFLOW env: green" "broad scope" $?

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
check "declared in JOB env: green" "job scope" $?

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
check "exported to GITHUB_ENV by an earlier step: green" "actual persistence" $?

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
check "shell variable that was NEVER env: not flagged" "does not parse shell and recognizes that limit" $?

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
check "definition lives in ANOTHER job: also red" "scope does not cross jobs" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: YAML comments
        # The hook honors ${FOO_BAR:-/tmp}.
        env:
          # Even an indented YAML comment mentions ${FOO_BAR}.
          OTHER_VAR: x
        run: |
          echo ready
        # A comment after the scalar also mentions ${FOO_BAR}.
YML
[ "$rc" -eq 0 ]
check "YAML comments are not shell consumers" "before and after run" $?

for scalar in '|' '>-' '|2' '>+2'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: trailing YAML comments
        run: $scalar
          echo ready
         # Less indented than scalar content: \${FOO_BAR}.
            # Later YAML comments may be more indented: \${FOO_BAR}.
YML
[ "$rc" -eq 0 ]
check "trailing YAML comments after a $scalar scalar" "content indentation" $?
done

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
        # Describes ${FOO_BAR} before its real consumer.
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:11: 'FOO_BAR'" "$WORK/e"
check "a real consumer after a YAML comment still fails" "original line number" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: descriptive export
        # echo "FOO_BAR=x" >> "$GITHUB_ENV"
        run: echo ready
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 1 ] && grep -q "FOO_BAR" "$WORK/e"
check "a YAML comment cannot export a definition" "missing env still fails" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: inline shell string
        run: echo "# ${FOO_BAR}"
YML
[ "$rc" -eq 1 ] && grep -q "FOO_BAR" "$WORK/e"
check "a quoted hash in inline run remains a consumer" "shell string" $?

for header in 'run: |' 'run: >' 'run: |-' 'run: >+' 'run: |2-' 'run: >2+' \
  '"run": |' "'run': |" 'run: &script |' 'run: !!str |' 'run : |'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: shell data
        $header # Shell content follows.
          cat <<EOF
          # \${FOO_BAR}
          EOF
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:12: 'FOO_BAR'" "$WORK/e"
check "hash in $header remains a consumer" "scalar content" $?
done

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: single-quoted YAML shell string
        run: 'echo "
          # ${FOO_BAR}"'
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:11: 'FOO_BAR'" "$WORK/e"
check "hash in a multiline single-quoted scalar is data" "shell expansion" $?

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: double-quoted YAML shell string
        run: "echo \"
          # ${FOO_BAR}\""
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:11: 'FOO_BAR'" "$WORK/e"
check "hash in a multiline double-quoted scalar is data" "escaped quotes" $?

for mapping in 'run:' 'run: # Shell content follows.'; do
for header in '|' '>' "'echo" '"echo'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: next-line scalar
        $mapping
          $header
            # \${FOO_BAR}
YML
case "$header" in
  "'echo") printf "            '" >> "$WORK/w/f.yml" ;;
  '"echo') printf '            "' >> "$WORK/w/f.yml" ;;
esac
OLIVARES_WORKFLOWS_DIR="$WORK/w" bash "$GATE" >"$WORK/o" 2>"$WORK/e"; rc=$?
[ "$rc" -eq 1 ] && grep -q "f.yml:12: 'FOO_BAR'" "$WORK/e"
check "hash in a next-line $header scalar remains data" "$mapping" $?
done
done

for kind in block quoted; do
case "$kind" in
  block) value=$'|\n            echo ready' ;;
  quoted) value="'echo ready'" ;;
esac
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: comment before value
        run:
          # Describes \${FOO_BAR} before the scalar begins.
          $value
YML
[ "$rc" -eq 0 ]
check "a YAML comment before a next-line $kind value" "not scalar data" $?
done


# Scalar properties may precede the value on separate lines.
for properties in $'&script\n          ' $'\n          !!str ' \
  $'&script\n          !!str ' $'!!str\n          &script ' \
  $'\n          &script\n          !!str '; do
for kind in literal folded single double; do
case "$kind" in
  literal) value=$'|\n            cat <<EOF\n            # ${FOO_BAR}\n            EOF' ;;
  folded) value=$'>\n            echo "\n            # ${FOO_BAR}"' ;;
  single) value=$'\'echo "\n            # ${FOO_BAR}"\'' ;;
  double) value=$'"echo \\"\n            # ${FOO_BAR}\\""' ;;
esac
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: continued properties
        run: $properties$value
YML
consumer_line=$(grep -n '# ${FOO_BAR}' "$WORK/w/f.yml" | cut -d: -f1)
[ "$rc" -eq 1 ] && grep -q "f.yml:$consumer_line: 'FOO_BAR'" "$WORK/e"
check "hash in $kind after continued properties is data" "multiline properties" $?
done
done

for header in '|' '>-' '|2' '>+2' '&script |' '!!str |' \
  $'&script # Describes ${FOO_BAR}.\n          |' \
  $'# Describes ${FOO_BAR}.\n          !!str |'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: scalar header comments
        run: $header # Describes \${FOO_BAR:-/tmp}.
            echo ready
YML
[ "$rc" -eq 0 ]
check "YAML comments on scalar headers are ignored" "block header" $?
done

for value in "'echo \"# \${FOO_BAR}\"'" '"echo \"# ${FOO_BAR}\""'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: quoted hash with a trailing comment
        run: $value # Describes \${FOO_BAR}.
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:10: 'FOO_BAR'" "$WORK/e"
check "quoted hashes survive trailing YAML comments" "$value" $?
done

for value in "'echo ready'" '"echo ready"'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: quoted scalar with a trailing comment
        run: $value # Describes \${FOO_BAR}.
YML
[ "$rc" -eq 0 ]
check "YAML comments after quoted scalars are ignored" "$value" $?
done

# Plain YAML scalars also end before whitespace followed by a hash.
for value in 'echo ready' '&script echo ready' '!!str echo ready' \
  $'\n          echo ready' $'echo ready\n          and steady'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: plain scalar with a trailing comment
        run: $value # Describes \${FOO_BAR:-/tmp}.
YML
[ "$rc" -eq 0 ]
check "YAML comments after plain scalars are ignored" "$value" $?
done

for value in 'echo "${FOO_BAR}"' 'echo "${FOO_BAR}#fragment"' \
  'echo ready#${FOO_BAR}' $'echo ready #${FOO_BAR}' \
  $'echo ready\n          "${FOO_BAR}#fragment"' \
  "'echo \" # \${FOO_BAR}\"'" '"echo \" # ${FOO_BAR}\""'; do
corre <<YML
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "\${FOO_BAR}"
      - name: real scalar consumer
        run: $value # Describes \${FOO_BAR:-/tmp}.
YML
consumer_line=$(grep -n '\${FOO_BAR}' "$WORK/w/f.yml" | tail -1 | cut -d: -f1)
[ "$rc" -eq 1 ] && grep -q "f.yml:$consumer_line: 'FOO_BAR'" "$WORK/e"
check "real consumers survive trailing YAML comments" "$value" $?
done

corre <<'YML'
name: t
jobs:
  j:
    steps:
      - name: define
        env:
          FOO_BAR: x
        run: echo "${FOO_BAR}"
      - name: descriptive export in a trailing comment
        run: echo ready # echo "FOO_BAR=x" >> "$GITHUB_ENV"
      - name: consume
        run: echo "${FOO_BAR}"
YML
[ "$rc" -eq 1 ] && grep -q "f.yml:12: 'FOO_BAR'" "$WORK/e"
check "a trailing YAML comment cannot export a definition" "missing env still fails" $?

rm -rf "$WORK/w"; mkdir -p "$WORK/w"
OLIVARES_WORKFLOWS_DIR="$WORK/w" bash "$GATE" >"$WORK/o" 2>"$WORK/e" || rc=$?
[ "${rc:-0}" -eq 2 ] && grep -q 'COULD NOT CHECK' "$WORK/e"
check "a directory without workflows returns 2, not 0" "third response" $?

echo ""
echo "check-step-env-closure battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
