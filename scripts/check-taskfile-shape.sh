#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-taskfile-shape.sh — puerta de las TRES RESPUESTAS sobre la forma del Taskfile.
#
# ⛔ POR QUE EXISTE, con la medida que lo motivo (2026-08-21). `scripts/taskfile-shape.py` sabe
#    detectar CADENA, SINCMD y DUPCLAVE, y llevaba desde el 2026-08-20 **sin que lo invocara
#    nada**: ni una tarea del Taskfile, ni una linea del hook. Un detector que nadie corre es una
#    promesa, no un control.
#
# ⛔ Y NO BASTA CON LLAMARLO, porque el reportero **siempre sale 0**: imprime los hallazgos por
#    stdout y termina con exito, incluso cuando imprime `NOPUEDO`. Es decir, cableado a pelo
#    (`python3 ... && ok`) habria sido un gate que nunca dice que no — y `NOPUEDO` saliendo 0 es
#    justo el fail-open que este repositorio prohibe. La conversion a tres respuestas vive AQUI,
#    en la puerta, y no se toca el reportero.
#
# 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
#
# La medida que lo pedia: resolviendo siete conflictos del Taskfile en un solo lote, «conservar
# ambos lados» produjo (a) una tarea RENOMBRADA por #1387 que sobrevivio dos veces, la vieja
# mutilada, y (b) una `desc` duplicada de #1308 que hizo que `task` rechazara el fichero entero.
# Ninguna de las dos se ve leyendo el hunk.
set -uo pipefail
export LC_ALL=C

ROOT="${OLIVARES_ROOT:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)}"
TF="${OLIVARES_TASKFILE:-$ROOT/Taskfile.yml}"
REP="$ROOT/scripts/taskfile-shape.py"

[ -r "$TF" ]  || { echo "check-taskfile-shape: NO HE PODIDO MIRAR — no puedo leer $TF" >&2; exit 2; }
[ -r "$REP" ] || { echo "check-taskfile-shape: NO HE PODIDO MIRAR — falta $REP" >&2; exit 2; }

# ⛔ El reportero EXIGE la ruta como argv[1]. Llamarlo sin ella revienta con IndexError, y con el
#    stderr silenciado eso se lee como «cero hallazgos»: medido, y por eso la ruta va explicita.
salida="$(python3 "$REP" "$TF" 2>&1)"; rc=$?
if [ "$rc" -ne 0 ]; then
	echo "check-taskfile-shape: NO HE PODIDO MIRAR — el lector fallo (rc=$rc):" >&2
	printf '%s\n' "$salida" | tail -3 >&2
	exit 2
fi
if printf '%s\n' "$salida" | command grep -q '^NOPUEDO'; then
	echo "check-taskfile-shape: NO HE PODIDO MIRAR — el lector no supo leer la forma del fichero:" >&2
	printf '%s\n' "$salida" | command grep '^NOPUEDO' >&2
	exit 2
fi
n="$(printf '%s' "$salida" | command grep -c . || true)"
if [ "$n" -ne 0 ]; then
	echo "check-taskfile-shape: FAIL — ${n} tarea(s) con la forma rota:" >&2
	printf '%s\n' "$salida" | sed 's/^/  /' >&2
	exit 1
fi
echo "check-taskfile-shape: CLEAN — $(command grep -cE '^  [A-Za-z0-9:_-]+:$' "$TF") tarea(s), ninguna con la forma rota."
exit 0
