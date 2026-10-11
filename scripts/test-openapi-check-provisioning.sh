#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# a repository gate / openapi:check — test provisioning and the named guard.
# Run 33284926144 (control-plane job 99186359739, SHA 417564d0d), 2026-08-30, failed
# “OpenAPI snapshot + web client codegen drift” with openapi-typescript not found.
# With dependencies installed, task openapi:check returned 0 and all three file diffs
# were zero bytes. An earlier SPDX failure at step 17 skipped steps 18-49, including
# dependency installation at 31; the consumer used a copied vuln-gate if: predicate
# (steps.tools, go-task), so it ran without its provisioner.
# Exercise both fixes: run the real Taskfile guard in disposable trees with and
# without the tool, requiring a named missing dependency; read both workflow if:
# conditions from YAML, not prose; mutate each fix in the copied file and require
# this suite to reject it. Surviving mutants expose missing controls.
# Exit: 0 clean · 1 finding · 2 could not check.
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TF="$ROOT/Taskfile.yml"
WF="$ROOT/.github/workflows/mainline-ci.yml"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/openapi-prov.XXXXXX") || { echo "could not create the workspace"; exit 2; }
trap 'rm -rf "$WORK"' EXIT

pass=0; fail=0
ok(){ printf 'ok    %s\n' "$1"; pass=$((pass+1)); }
no(){ printf 'FAIL  %s\n' "$1"; fail=$((fail+1)); }
cannot(){ printf 'CANNOT INSPECT: %s\n' "$1"; exit 2; }

command -v python3 >/dev/null 2>&1 || cannot "without python3, cannot read YAML"
PEEK="$ROOT/scripts/lib/ci-yaml-peek.py"
[ -r "$PEEK" ] || cannot "missing scripts/lib/ci-yaml-peek.py, required to read these YAML files"
python3 "$ROOT/scripts/test-ci-yaml-peek.py" || cannot "CI reader regression suite failed"
# ⛔ SIN PyYAML A PROPOSITO. Este guion corre como paso de mainline-ci, y ese job vive en un
# runner AUTOALOJADO (hetzner/srv17), no en una imagen de GitHub. Medido el 2026-08-30 sobre el
# arbol entero: de todos los `run:` de todos los workflows, UNO usa python y solo importa
# stdlib. Nada demuestra que PyYAML se alcance alli, y un gate nuevo que lo diera por hecho
# seria la misma clase de dependencia de entorno sin medir que este claim cura un piso arriba.

# ---------------------------------------------------------------- extractores (del YAML)

# Imprime el bloque shell de la guarda de web:codegen. rc 3 = no esta (lo usan los mutantes).
guard_block() {
  python3 "$PEEK" task-cmd "$1" web:codegen 'node_modules/.bin'
}

# Read the condition wherever the step is defined; differing copies are ambiguous.
step_if() {
  python3 "$PEEK" step-field-anyjob "$1" "$2" if
}
step_jobs() {
  python3 "$PEEK" step-jobs "$1" "$2"
}

# Arbol de mentira con los binarios que se le pidan en web/node_modules/.bin.
fake_tree() {
  local d; d=$(mktemp -d "$WORK/tree.XXXXXX") || return 1
  mkdir -p "$d/web/node_modules/.bin" || return 1
  local t
  for t in "$@"; do
    printf '#!/bin/sh\nexit 0\n' > "$d/web/node_modules/.bin/$t" || return 1
    chmod +x "$d/web/node_modules/.bin/$t" || return 1
  done
  printf '%s' "$d"
}

# ---------------------------------------------------------------- 1. la guarda, EJECUTADA

G="$WORK/guard.sh"
if ! guard_block "$TF" > "$G" 2>"$WORK/g.err"; then
  cannot "cannot find the web:codegen guard in Taskfile.yml ($(head -1 "$WORK/g.err"))"
fi
[ -s "$G" ] || cannot "the web:codegen guard was empty"

if bash -n "$G" 2>"$WORK/n.err"; then
  ok "the guard block is valid shell (bash -n)"
else
  no "the guard fails bash -n: $(head -1 "$WORK/n.err")"
fi

run_guard() { ( cd "$1" && bash "$G" ) >"$WORK/out.txt" 2>&1; printf '%s' "$?"; }

T_OK=$(fake_tree openapi-typescript prettier) || cannot "could not stage the complete tree"
rc=$(run_guard "$T_OK")
if [ "$rc" = 0 ]; then ok "with both tools present, the guard allows execution (rc 0)"
else no "with the tools present, the guard still blocks (rc $rc): $(head -1 "$WORK/out.txt")"; fi

T_NONE=$(fake_tree) || cannot "could not stage the empty tree"
rc=$(run_guard "$T_NONE")
if [ "$rc" = 1 ]; then ok "without either tool, the guard blocks fail-closed (rc 1)"
else no "without tools, the guard does not block with rc 1 (rc $rc)"; fi
if command grep -q 'openapi-typescript' "$WORK/out.txt"; then
  ok "and NAMES openapi-typescript (not a downstream 'command not found')"
else no "blocks without naming openapi-typescript: $(head -2 "$WORK/out.txt")"; fi
if command grep -q 'pnpm --dir web install --frozen-lockfile' "$WORK/out.txt"; then
  ok "and reports HOW to obtain them (the exact installation command)"
else no "does not report how to obtain the tools"; fi
if command grep -qi 'not OpenAPI snapshot drift' "$WORK/out.txt"; then
  ok "and explicitly rejects the false interpretation ('not OpenAPI snapshot drift')"
else no "does not reject the drift interpretation, the measured cost of the defect"; fi

T_HALF=$(fake_tree openapi-typescript) || cannot "could not stage the incomplete tree"
rc=$(run_guard "$T_HALF")
if [ "$rc" = 1 ] && command grep -q 'prettier' "$WORK/out.txt" \
   && ! command grep -q ' openapi-typescript' "$WORK/out.txt"; then
  ok "with openapi-typescript present and prettier missing, names ONLY prettier"
else no "does not distinguish which of the two is missing (rc $rc): $(head -1 "$WORK/out.txt")"; fi

# ---------------------------------------------------------------- 2. las dos `if:` del workflow

IF_PROV=$(step_if "$WF" openapi-web-deps) || cannot "cannot find step openapi-web-deps"
IF_CONS=$(step_if "$WF" openapi-check)   || cannot "cannot find step openapi-check"

case "$IF_PROV" in
  *steps.node.outcome*steps.pnpm.outcome*)
    ok "the provider (openapi-web-deps) names ITS predicate: node and pnpm" ;;
  '') no "the provider still lacks a guard: an unrelated failure skips it again" ;;
  *)  no "the provider guard does not name node+pnpm: $IF_PROV" ;;
esac
case "$IF_PROV" in
  *'!cancelled()'*) ok "uses !cancelled(), not always() (cancellation is not a verdict)" ;;
  *) no "the provider does not use !cancelled(): $IF_PROV" ;;
esac
case "$IF_CONS" in
  *steps.openapi-web-deps.outcome*)
    ok "the consumer (openapi-check) names its provider, not only steps.tools" ;;
  *) no "the consumer does NOT name openapi-web-deps: may run without its tool again ($IF_CONS)" ;;
esac
case "$IF_CONS" in
  *steps.tools.outcome*) ok "retains steps.tools, also required (go-task)" ;;
  *) no "the consumer lost steps.tools during the fix: $IF_CONS" ;;
esac

# Step outputs are visible only within the job that produced them.
J_PROV=$(step_jobs "$WF" openapi-web-deps) || cannot "cannot locate openapi-web-deps jobs"
J_CONS=$(step_jobs "$WF" openapi-check) || cannot "cannot locate openapi-check jobs"
orphan=""
for job in $J_CONS; do
  printf '%s\n' "$J_PROV" | command grep -Fxq -- "$job" || orphan="$orphan $job"
done
if [ -z "$orphan" ]; then
  ok "OpenAPI consumers share their job with openapi-web-deps"
else
  no "OpenAPI consumers lack openapi-web-deps in their own job:$orphan"
fi

# ---------------------------------------------------------------- 3. mutantes

mut_check() { # <etiqueta> <fichero-mutado> <TF|WF>
  local label=$1 f=$2 kind=$3 out
  if [ "$kind" = TF ]; then
    out=$(guard_block "$f" 2>/dev/null); local grc=$?
    if [ $grc -ne 0 ]; then printf 'DETECTED %s (the test no longer finds the guard)\n' "$label"; return 0; fi
    printf '%s' "$out" > "$WORK/mut-guard.sh"
    local rc2; rc2=$( ( cd "$T_NONE" && bash "$WORK/mut-guard.sh" ) >/dev/null 2>&1; printf '%s' "$?" )
    if [ "$rc2" != 1 ]; then printf 'DETECTED %s (the mutated guard stops blocking)\n' "$label"; return 0; fi
    printf 'SURVIVES %s\n' "$label"; return 1
  else
    local p c
    p=$(step_if "$f" openapi-web-deps) || { printf 'COULD NOT LOOK: %s provider\n' "$label" >&2; return 2; }
    c=$(step_if "$f" openapi-check) || { printf 'COULD NOT LOOK: %s consumer\n' "$label" >&2; return 2; }
    case "$p" in *steps.node.outcome*steps.pnpm.outcome*) ;; *) printf 'DETECTED %s (provider)\n' "$label"; return 0;; esac
    case "$c" in *steps.openapi-web-deps.outcome*) ;; *) printf 'DETECTED %s (consumer)\n' "$label"; return 0;; esac
    printf 'SURVIVES %s\n' "$label"; return 1
  fi
}

M_TF="$WORK/mut-Taskfile.yml"; M_WF="$WORK/mut-mainline.yml"

# M1 — se borra la guarda del Taskfile.
python3 - "$TF" "$M_TF" <<'PY'
import sys
s = open(sys.argv[1], encoding='utf-8').read()
i = s.index('      - cmd: |\n          missing=\"\"')
j = s.index('      - pnpm --dir web run codegen', i)
open(sys.argv[2], 'w', encoding='utf-8').write(s[:i] + s[j:])
PY
if mut_check "M1 Taskfile guard removed" "$M_TF" TF; then ok "M1: mutant detected"; else no "M1 SURVIVES"; fi

# M2 — keep the blocking guard, but remove its executable diagnostics.
python3 - "$TF" "$M_TF" <<'PY' || cannot "M2 did not remove exactly one diagnostic block"
import sys, re
s = open(sys.argv[1], encoding='utf-8').read()
s, removed = re.subn(r'\n *echo "::error::web:codegen[^\n]*\n *echo "Install them with:[^\n]*\n *echo "\(this is a toolchain problem[^\n]*', '', s)
if removed != 1:
    raise SystemExit(f'M2 removed {removed} diagnostic blocks, expected exactly one')
open(sys.argv[2], 'w', encoding='utf-8').write(s)
PY
G_KEEP=$G; G="$WORK/mut-guard-mudo.sh"
guard_block "$M_TF" > "$G" 2>/dev/null || cannot "cannot extract the M2 mutated guard"
rc=$(run_guard "$T_NONE")
if [ "$rc" = 1 ] && [ ! -s "$WORK/out.txt" ]; then
  ok "M2: a SILENT blocking guard is distinguishable (fails without naming) — the test requires it above"
else no "M2: cannot distinguish a silent guard from one that names the cause (rc $rc)"; fi
G=$G_KEEP

# M3 — el proveedor se queda sin `if:` (el estado de ayer).
python3 - "$WF" "$M_WF" <<'PY' || cannot "M3 did not remove exactly one guard per provider"
import sys
lines = open(sys.argv[1], encoding='utf-8').read().splitlines(keepends=True)
remove = []
copies = 0
for start, line in enumerate(lines):
    if not line.startswith('      - '):
        continue
    end = start + 1
    while end < len(lines):
        following = lines[end]
        if following.strip() and not following.lstrip().startswith('#'):
            indent = len(following) - len(following.lstrip(' '))
            if indent <= 6:
                break
        end += 1
    block = ['        ' + line[8:], *lines[start + 1:end]]
    if not any(value.strip() == 'id: openapi-web-deps' for value in block):
        continue
    guards = [start + offset for offset, value in enumerate(block) if value.startswith('        if:')]
    if len(guards) != 1:
        raise SystemExit('M3 requires one guard per provider step')
    copies += 1
    remove.extend(guards)
if not copies or len(remove) != copies:
    raise SystemExit('M3 did not find every provider guard')
open(sys.argv[2], 'w', encoding='utf-8').writelines(value for index, value in enumerate(lines) if index not in remove)
print(f'M3 removed {len(remove)} guards from {copies} provider copies')
PY
if mut_check "M3 provider without a guard" "$M_WF" WF; then ok "M3: mutant detected"; else no "M3 SURVIVES"; fi

# M4 — el consumidor vuelve al predicado de vuln-gate (el defecto exacto que se cura).
python3 - "$WF" "$M_WF" <<'PY' || cannot "M4 did not change every consumer guard"
import sys
lines = open(sys.argv[1], encoding='utf-8').read().splitlines(keepends=True)
old = "!cancelled() && steps.tools.outcome == 'success' && steps.openapi-web-deps.outcome == 'success'"
new = "!cancelled() && steps.tools.outcome == 'success'"
copies = 0
for start, line in enumerate(lines):
    if not line.startswith('      - '):
        continue
    end = start + 1
    while end < len(lines):
        following = lines[end]
        if following.strip() and not following.lstrip().startswith('#'):
            if len(following) - len(following.lstrip(' ')) <= 6:
                break
        end += 1
    block = ['        ' + line[8:], *lines[start + 1:end]]
    if not any(value.strip() == 'id: openapi-check' for value in block):
        continue
    guards = [start + offset for offset, value in enumerate(block)
              if value.startswith('        if:') and value.count(old) == 1]
    if len(guards) != 1:
        raise SystemExit('M4 requires the expected guard in each consumer step')
    lines[guards[0]] = lines[guards[0]].replace(old, new)
    copies += 1
if not copies:
    raise SystemExit('M4 found no consumers')
open(sys.argv[2], 'w', encoding='utf-8').writelines(lines)
print(f'M4 changed {copies} consumer guards')
PY
if mut_check "M4 consumer with the vuln-gate predicate" "$M_WF" WF; then ok "M4: mutant detected"; else no "M4 SURVIVES"; fi

# CONTROL — se muta EL GUION, no el sujeto: si la bateria mira un fichero que no tiene el paso,
# tiene que decir NO HE PODIDO MIRAR (rc 3 del extractor), no dar por buena la ausencia.
printf 'jobs:\n  control-plane:\n    steps:\n      - name: nada\n' > "$WORK/vacio.yml"
step_if "$WORK/vacio.yml" openapi-check >/dev/null 2>&1
if [ $? -eq 3 ]; then ok "CONTROL: on a workflow without the step, the extractor reports inability to inspect"
else no "CONTROL: missing step is interpreted as something other than 'cannot inspect'"; fi

printf '\ntest-openapi-check-provisioning: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
