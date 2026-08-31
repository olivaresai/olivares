#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# launch-state-captures.sh — las capturas de ESTADO que `docs/launch/` referencia.
#
# Hermano de `docs-captures.sh` y deliberadamente separado: aquél captura RUTAS y éste ESTADOS, que
# exigen pasos y —tres de ellos— MUTAN el estate sembrado. Compartir corrida haría que una sesión
# enviada o un kill-switch activado cambiasen lo que ven las capturas de rutas.
set -euo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"

PORT="${PORT:-18091}"
GRPC_PORT="${GRPC_PORT:-18092}"
# ⛔ EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL (the reviewer, A-02). Estaba DESPUES en los cuatro, y
#    un fallo entre el `mktemp` y el `trap` —medido con rc 7— dejaba los cuatro temporales
#    vivos, sin dueño y sin nadie que los borrara. La funcion de limpieza tolera que sus
#    variables aun no existan (todas se leen con `${VAR:-}`), asi que armarla temprano no
#    cuesta nada y cierra la ventana entera.
limpia() {
	[ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
	rm -rf ${EXEC_TMP:+"$EXEC_TMP"}
}
trap limpia EXIT

DATA="$(mktemp -d)"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"


# ⛔ ESTO VA DESPUES DEL `trap`, y no es orden estetico (the reviewer, A-02). El scratch se crea
#    aqui; si se creara ANTES de que la limpieza este armada, cualquier fallo en el setup
#    intermedio —otro `mktemp`, un `curl`— lo dejaria colgado sin dueño. La ventana era de
#    18 a 41 lineas segun el guion. Ahora no hay ventana.
# ⛔ EL MOTOR EXTRAE SUS PLUGINS A `$TMPDIR` Y LOS EJECUTA (`cmd/olivares/boot.go:1423`), y en estos
#    contenedores `/tmp` esta montado noexec, asi que el `fork/exec` falla con el bit puesto y la
#    fuente se rechaza en la recarga. Se SONDEA un directorio que ejecute —no se deduce de la ruta—
#    y se le pasa al motor. Si ninguno ejecuta se DICE, porque el silencio aqui se lee como «no
#    hacia falta». Detalle y alcance honesto, en la cabecera de la libreria.
# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
# ⛔ SE REHUSA, NO SE AVISA (the reviewer, A-01). La version anterior imprimia el aviso, vaciaba
#    `EXEC_TMP` y ARRANCABA IGUAL con `/tmp`: es decir, el seguro no aseguraba nada — el motor
#    volvia a extraer sus plugins donde no puede ejecutarlos y la fuente se rechazaba en la
#    recarga, en silencio y deny-closed, que es exactamente el fallo que este guion existe para
#    cerrar. Un control que avisa y sigue no es un control: es un comentario con `echo`.
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): ⛔ NO ARRANCO: ningun directorio temporal EJECUTA." >&2
  echo "   El motor extrae sus plugins de conector a \$TMPDIR y los LANZA" >&2
  echo "   (cmd/olivares/boot.go), y aqui ninguno de los candidatos permite execve —" >&2
  echo "   tipicamente porque /tmp esta montado noexec. Arrancar igual dejaria el plano de" >&2
  echo "   conectores muerto SIN decirlo." >&2
  echo "   Remedio: exporta OLIVARES_EXEC_TMPDIR a un directorio que ejecute." >&2
  exit 2
}

[ -x "$BIN" ] || { echo "launch-state-captures: ⛔ NO HE PODIDO MIRAR: no hay binario en $BIN" >&2; exit 2; }

# ⛔ EL BINARIO EMPOTRA EL BUNDLE, así que uno viejo fotografía una consola vieja — y el manifiesto
#    de la tanda apuntaría al commit de HOY. Una captura que miente sobre su procedencia es peor que
#    no tenerla: los gates de frescura se fían de esa fecha.
#
#    Medido el 2026-08-18: el binario era de las 10:46 y el `dist` estaba commiteado a las 14:21, con
#    un arreglo de consola en medio. El guion capturaba con la interfaz de cuatro horas antes y no
#    decía nada. Su hermano `docs-captures.sh` sí reconstruye; esta diferencia entre dos guiones
#    gemelos no era una decisión, era una omisión.
#
# ⚠ REHÚSA en vez de reconstruir por su cuenta cuando el binario está viejo: compilar Go aquí
#    duplicaría lo que `task build:go` ya hace y escondería un árbol a medio construir. Lo que no
#    puede es seguir adelante callado.
if [ -d "$ROOT/core/internal/webui/dist" ]; then
	dist_ts="$(find "$ROOT/core/internal/webui/dist" -type f -newer "$BIN" -print -quit 2>/dev/null || true)"
	if [ -n "$dist_ts" ]; then
		echo "launch-state-captures: ⛔ NO HE PODIDO MIRAR: el binario es MÁS VIEJO que el bundle empotrado." >&2
		echo "  $BIN se compiló antes que $dist_ts, así que serviría una consola anterior al arreglo" >&2
		echo "  que se quiere fotografiar — y el manifiesto diría que es de hoy." >&2
		# ⚠ `task build:go` NO escribe `bin/olivares` — sale rc=0 y deja el binario intacto, medido.
		#    La que lo produce es `build:bin`, que compila a través de `scripts/lib/build-bin.sh`.
		echo "  Reconstruye:  task build:web && task build:bin" >&2
		exit 2
	fi
fi

echo "==> Motor sembrado en 127.0.0.1:$PORT"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure --seed-demo --listen "127.0.0.1:$PORT" \
  --grpc-listen "127.0.0.1:$GRPC_PORT" --data-dir "$DATA" >"$DATA/engine.log" 2>&1 &
PID=$!
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done

TOKEN="$(curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
TENANT="$(curl -sf "http://127.0.0.1:$PORT/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"
[ -n "$TENANT" ] || { echo "launch-state-captures: ⛔ NO HE PODIDO MIRAR: sin tenant demo" >&2; cat "$DATA/engine.log" >&2; exit 2; }
echo "==> Tenant demo: $TENANT"

cd "$ROOT/web"
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT" DEMO_TENANT="$TENANT" \
  pnpm exec playwright test e2e/launch-states.spec.ts "$@"
