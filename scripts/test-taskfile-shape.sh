#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de check-taskfile-shape.sh.
#
# Un gate solo vale si se demuestra que puede decir que NO, y en las direcciones que importan:
# clave duplicada dentro de una tarea (lo que rompio un lote real), tarea-cadena, y las dos
# formas de «no he podido mirar» — reportero ausente y Taskfile ilegible—, que deben salir 2 y
# NUNCA 0, porque un gate de forma que sale verde sin haber leido nada es peor que no tenerlo.
#
# Trabaja sobre ficheros de mentira en TMPDIR: no toca el arbol.
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
CHECK="$ROOT/scripts/check-taskfile-shape.sh"
[ -r "$CHECK" ] || { echo "test-taskfile-shape: NO HE PODIDO MIRAR — falta $CHECK" >&2; exit 2; }

_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_base" || { echo "test-taskfile-shape: NO HE PODIDO MIRAR — no puedo crear $_base" >&2; exit 2; }
TMP="$(mktemp -d "$_base/tfshape.XXXXXX")" || { echo "test-taskfile-shape: NO HE PODIDO MIRAR — mktemp" >&2; exit 2; }
trap 'rm -rf "$TMP"' EXIT INT TERM

pass=0; fail=0
ok()  { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# Un arbol de mentira con su propio scripts/, para poder quitarle el reportero sin tocar el real.
mkdir -p "$TMP/scripts"
cp "$ROOT/scripts/taskfile-shape.py" "$TMP/scripts/"
cp "$CHECK" "$TMP/scripts/"

limpio() {
	cat > "$1" <<'YAML'
version: '3'

tasks:
  lint:uno:
    desc: una tarea sana.
    cmds:
      - true

  lint:dos:
    desc: otra tarea sana.
    cmds:
      - true
YAML
}

corre() { ( OLIVARES_ROOT="$TMP" OLIVARES_TASKFILE="$1" bash "$TMP/scripts/check-taskfile-shape.sh" >"$TMP/out" 2>&1; echo $? ); }

# --- control positivo: sin el, un guion que siempre saliera 1 pasaria todos los mutantes -------
limpio "$TMP/Taskfile.yml"
rc="$(corre "$TMP/Taskfile.yml")"
[ "$rc" = 0 ] && ok "control: un Taskfile sano sale CLEAN (0)" || bad "el sano salio $rc: $(head -2 "$TMP/out")"

# --- M1: clave duplicada DENTRO de una tarea (el caso real que rompio un lote) -----------------
limpio "$TMP/dup.yml"
python3 - "$TMP/dup.yml" <<'PY'
import sys
p=sys.argv[1]; L=open(p,encoding='utf-8').read().split('\n')
i=next(k for k,l in enumerate(L) if l.strip().startswith('desc: una tarea'))
L.insert(i+1, '    desc: y una segunda descripcion de la MISMA tarea.')
open(p,'w',encoding='utf-8').write('\n'.join(L))
PY
rc="$(corre "$TMP/dup.yml")"
[ "$rc" = 1 ] && ok "mutante: una clave duplicada dentro de una tarea es HALLAZGO (1)" || bad "dup salio $rc: $(head -3 "$TMP/out"|tail -1)"

# --- M2: tarea-cadena (una tarea que solo encadena otras, sin cmds) ---------------------------
limpio "$TMP/cad.yml"
printf '\n  lint:tres:\n    desc: encadena y no ejecuta nada.\n' >> "$TMP/cad.yml"
rc="$(corre "$TMP/cad.yml")"
[ "$rc" = 1 ] && ok "mutante: una tarea sin cmds ni deps es HALLAZGO (1)" || bad "cadena salio $rc: $(head -3 "$TMP/out"|tail -1)"

# --- M3: el reportero AUSENTE -> 2, nunca 0 ---------------------------------------------------
limpio "$TMP/Taskfile.yml"
mv "$TMP/scripts/taskfile-shape.py" "$TMP/scripts/.guardado"
rc="$(corre "$TMP/Taskfile.yml")"
[ "$rc" = 2 ] && ok "sin el reportero: NO HE PODIDO MIRAR (2), nunca 0" || bad "reportero ausente salio $rc"
mv "$TMP/scripts/.guardado" "$TMP/scripts/taskfile-shape.py"

# --- M3b: control negativo del anterior — restaurado, vuelve a salir 0 ------------------------
rc="$(corre "$TMP/Taskfile.yml")"
[ "$rc" = 0 ] && ok "control: restaurado el reportero, vuelve a CLEAN" || bad "tras restaurar esperaba 0 y salio $rc"

# --- M4: el Taskfile ILEGIBLE -> 2 ------------------------------------------------------------
rc="$(corre "$TMP/no-existe.yml")"
[ "$rc" = 2 ] && ok "Taskfile ausente: NO HE PODIDO MIRAR (2)" || bad "Taskfile ausente salio $rc"

# --- M5: el reportero REVIENTA -> 2 (no puede leerse como «cero hallazgos») -------------------
printf 'import sys\nraise SystemExit("boom")\n' > "$TMP/scripts/taskfile-shape.py"
rc="$(corre "$TMP/Taskfile.yml")"
[ "$rc" = 2 ] && ok "si el reportero revienta: 2, no 0 con salida vacia" || bad "reportero roto salio $rc"

# ⛔ M5 dejo el reportero ROTO a proposito. Se restaura AQUI y se COMPRUEBA que la restauracion
#    funciono: sin esto, los casos siguientes salen 2 («no he podido mirar») y se leen como si el
#    detector no supiera ver un duplicado — un mutante que no se aplica acusa al gate de ciego.
cp "$ROOT/scripts/taskfile-shape.py" "$TMP/scripts/taskfile-shape.py"
limpio "$TMP/restaurado.yml"
rc="$(corre "$TMP/restaurado.yml")"
[ "$rc" = 0 ] || { echo "test-taskfile-shape: NO HE PODIDO MIRAR — no pude restaurar el reportero tras M5" >&2; exit 2; }
ok "control: restaurado el reportero, el arbol sano vuelve a CLEAN"

# --- M6: una TAREA definida dos veces -----------------------------------------------------
# ⛔ El caso real que motivo la clase: `yaml.safe_load` la acepta (gana la ultima) y
#    `task --list-all` tambien, asi que dos instrumentos daban CLEAN. Lo vio un parser ESTRICTO,
#    y por un camino que no tenia nada que ver: rehuso el Taskfile entero y dejo otro gate con
#    67 fallos que se leian como un problema de Postgres.
limpio "$TMP/duptarea.yml"
printf '\n  lint:uno:\n    desc: la MISMA tarea otra vez.\n    cmds:\n      - true\n' >> "$TMP/duptarea.yml"
rc="$(corre "$TMP/duptarea.yml")"
[ "$rc" = 1 ] && ok "mutante: una TAREA definida dos veces es HALLAZGO (1)" || bad "tarea duplicada salio $rc, esperaba 1"

# --- M6b: control negativo — dos tareas DISTINTAS no son un duplicado ----------------------
limpio "$TMP/notdup.yml"
printf '\n  lint:tres:\n    desc: una tercera tarea, distinta.\n    cmds:\n      - true\n' >> "$TMP/notdup.yml"
rc="$(corre "$TMP/notdup.yml")"
[ "$rc" = 0 ] && ok "control: dos tareas DISTINTAS no son un duplicado" || bad "tres tareas distintas salio $rc, esperaba 0"

echo "check-taskfile-shape selftest: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
exit 0
