#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# UI tramo of the E2E: drive the EMBEDDED web against the REAL binary serving
# REAL seeded data (no /v1 mocks). Builds the web bundle + binary, boots
# `serve --insecure --seed-demo` (which loads a synthetic estate through the real
# event bus), resolves the demo tenant id, and runs the Playwright demo and focused
# console-functional specs against live backend data.
#
# Usage: scripts/web-e2e-demo.sh
# Requires: go, pnpm, Playwright chromium (`pnpm --dir web exec playwright install chromium`).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
PORT="${E2E_PORT:-8466}"
GRPC_PORT="$((PORT + 1))"
# ⛔ EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL (the reviewer, A-02). Estaba DESPUES en los cuatro, y
#    un fallo entre el `mktemp` y el `trap` —medido con rc 7— dejaba los cuatro temporales
#    vivos, sin dueño y sin nadie que los borrara. La funcion de limpieza tolera que sus
#    variables aun no existan (todas se leen con `${VAR:-}`), asi que armarla temprano no
#    cuesta nada y cierra la ventana entera.
cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  rm -rf "$DATA" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

DATA="$(mktemp -d)"
BIN="$ROOT/bin/olivares"


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

port_is_open() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
  echo "ERROR: port $PORT already serves an Olivares health endpoint" >&2
  exit 1
fi

echo "==> Building web bundle + olivares binary"
pnpm --dir "$ROOT/web" install --frozen-lockfile >/dev/null
pnpm --dir "$ROOT/web" run build
build_olivares_bin "$BIN"

# Recheck both listeners after the long build. The early health probe fails fast
# for a stale engine; this TCP check also catches a non-HTTP or gRPC occupant.
if port_is_open "$PORT" || port_is_open "$GRPC_PORT"; then
  echo "ERROR: port $PORT or $GRPC_PORT is already occupied" >&2
  exit 1
fi

echo "==> Booting engine (insecure, demo-seeded) on 127.0.0.1:$PORT"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure --seed-demo --listen "127.0.0.1:$PORT" \
  --grpc-listen "127.0.0.1:$GRPC_PORT" --data-dir "$DATA" >"$DATA/engine.log" 2>&1 &
PID=$!

echo "==> Waiting for the engine to accept connections"
READY=0
for _ in $(seq 1 40); do
  if ! kill -0 "$PID" 2>/dev/null; then
    break
  fi
  if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 &&
    kill -0 "$PID" 2>/dev/null; then
    READY=1
    break
  fi
  sleep 0.5
done
if [ "$READY" -ne 1 ] || ! kill -0 "$PID" 2>/dev/null; then
  echo "ERROR: engine did not become healthy" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi

# Resolve the demo tenant id (the spec seeds it as the active tenant).
TOKEN=""
for _ in $(seq 1 40); do
  if ! kill -0 "$PID" 2>/dev/null; then
    break
  fi
  LOGIN="$(curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' || true)"
  TOKEN="$(python3 -c 'import sys,json;print(json.load(sys.stdin).get("token", ""))' \
    <<<"$LOGIN" 2>/dev/null || true)"
  if [ -n "$TOKEN" ]; then
    break
  fi
  sleep 0.5
done
if [ -z "$TOKEN" ]; then
  echo "ERROR: demo login did not become ready" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi

TENANT=""
for _ in $(seq 1 40); do
  if ! kill -0 "$PID" 2>/dev/null; then
    break
  fi
  ORGS="$(curl -sf "http://127.0.0.1:$PORT/v1/system/orgs" \
    -H "Authorization: Bearer $TOKEN" || true)"
  TENANT="$(python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin).get("items", []) if o.get("slug")=="demo"]' \
    <<<"$ORGS" 2>/dev/null || true)"
  if [ -n "$TENANT" ]; then
    break
  fi
  sleep 0.5
done
if [ -z "$TENANT" ]; then
  echo "ERROR: could not resolve the demo tenant" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi
echo "==> Demo tenant: $TENANT"

if ! kill -0 "$PID" 2>/dev/null; then
  echo "ERROR: engine exited before Playwright" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi

echo "==> Running Playwright demo and console-functional specs against live seeded data"
cd "$ROOT/web"
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT" DEMO_TENANT="$TENANT" \
  pnpm exec playwright test e2e/demo-graph.spec.ts e2e/console-func-l4.spec.ts
