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
# Usage: scripts/web-e2e-demo.sh [Playwright arguments]
# Requires: go, pnpm, Playwright chromium (`pnpm --dir web exec playwright install chromium`).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
. "$ROOT/scripts/lib/select-console-modules.sh"
. "$ROOT/scripts/lib/session-conversation-fixture.sh"
PORT="${E2E_PORT:-8466}"
GRPC_PORT="$((PORT + 1))"
# ⛔ EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL (the reviewer, A-02). Estaba DESPUES en los cuatro, y
#    un fallo entre el `mktemp` y el `trap` —medido con rc 7— dejaba los cuatro temporales
#    vivos, sin dueño y sin nadie que los borrara. La funcion de limpieza tolera que sus
#    variables aun no existan (todas se leen con `${VAR:-}`), asi que armarla temprano no
#    cuesta nada y cierra la ventana entera.
cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  [ -n "${PID:-}" ] && wait "$PID" 2>/dev/null || true
  rm -rf "${DATA:-}" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

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
  echo "$(basename "$0"): ⛔ CANNOT START: no temporary directory allows EXECUTION." >&2
  echo "   The engine extracts its connector plugins to \$TMPDIR and LAUNCHES them" >&2
  echo "   (cmd/olivares/boot.go), but none of the candidate directories here allows execve —" >&2
  echo "   typically because /tmp is mounted noexec. Starting anyway would silently leave the" >&2
  echo "   connector plane unavailable." >&2
  echo "   Remedy: export OLIVARES_EXEC_TMPDIR pointing to a directory that permits execution." >&2
  exit 2
}

port_is_open() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
  echo "ERROR: port $PORT already serves an Olivares health endpoint" >&2
  exit 1
fi

if [ -n "${OLIVARES_BIN:-}" ]; then
  [ -x "$BIN" ] || { echo 'ERROR: OLIVARES_BIN must name an executable release candidate' >&2; exit 1; }
  echo '==> Using the supplied release candidate'
else
  echo "==> Building web bundle + olivares binary"
  pnpm --dir "$ROOT/web" install --frozen-lockfile >/dev/null
  pnpm --dir "$ROOT/web" run build
  build_olivares_bin "$BIN"
fi

# Recheck both listeners after the long build. The early health probe fails fast
# for a stale engine; this TCP check also catches a non-HTTP or gRPC occupant.
if port_is_open "$PORT" || port_is_open "$GRPC_PORT"; then
  echo "ERROR: port $PORT or $GRPC_PORT is already occupied" >&2
  exit 1
fi

prepare_conversation_fixture

# A new installation runs the standard modules and the ones that hold seeded data
# (cmd/olivares/moduleprofile_settings.go); every other module answers with its calm
# "Turn on" page, which has no grid, no verb and no dashboard section to drive. These specs
# exist to drive the console screens over live data, so the script states what the engine
# runs: this node's own module profile, in the file the engine reads at boot, selecting every
# module the catalog offers. A module this build does not have is ignored by the engine.
python3 -I - "$ROOT/core/modulespec/modules.json" "$DATA/module-profile.json" <<'PY'
import datetime
import json
import os
import sys

catalog, target = sys.argv[1:3]
with open(catalog) as f:
    selected = sorted(m["namespace"] for m in json.load(f) if m.get("selectable"))
# An empty selection is authoritative to the engine: it would boot with no modules at all.
if not selected:
    sys.exit(f"no selectable module in {catalog}")
doc = {
    "version": "olivares.module.profile.v2",
    "selected": selected,
    "updated_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
}
with open(os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as f:
    json.dump(doc, f, indent=2)
    f.write("\n")
PY

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
  LOGIN="$(printf '%s' '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' | curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
    -H 'Content-Type: application/json' \
    --data-binary @- || true)"
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
  WHOAMI="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "http://127.0.0.1:$PORT/v1/auth/whoami" \
    -H @- || true)"
  TENANT="$(python3 -c 'import sys,json;print(json.load(sys.stdin)["grants"][0]["tenant"])' \
    <<<"$WHOAMI" 2>/dev/null || true)"
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
seed_conversation_fixture

# Enable the optional shared readers exercised by the browser journey.
enable_console_modules "http://127.0.0.1:$PORT" accessmap catalog compliance evals finops health inventory knowledge models notify observability orchestration recording redteam reporting security sessions

# Only authenticated 200 and the published 501 refusal describe a capability.
capability_status() {
  local status
  status="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -o "$DATA/capability.json" -w '%{http_code}' \
    "http://127.0.0.1:$PORT$1" -H @- -H "X-Olivares-Tenant: $TENANT")" || return 1
  case "$status" in 200|501) printf '%s' "$status" ;; *) echo "ERROR: $1 returned HTTP $status; expected 200 or 501" >&2; return 1 ;; esac
}
GRAPH_STATUS="$(capability_status '/v1/m/accessmap/graph')"
IDENTITY_STATUS="$(capability_status '/v1/m/governance/identities')"
BUILD_EDITION="$(printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sf "http://127.0.0.1:$PORT/v1/server-info" -H @- \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["edition"])')"
case "$BUILD_EDITION" in community|enterprise|business) ;; *) echo "ERROR: unknown build edition: $BUILD_EDITION" >&2; exit 1 ;; esac
if [ "$BUILD_EDITION" = community ] && { [ "$GRAPH_STATUS" != 200 ] || [ "$IDENTITY_STATUS" != 200 ]; }; then
  echo 'ERROR: Community must serve both the access graph and the identity inventory' >&2; exit 1
fi
echo "==> Build: $BUILD_EDITION; graph HTTP $GRAPH_STATUS; identity inventory HTTP $IDENTITY_STATUS"


if ! kill -0 "$PID" 2>/dev/null; then
  echo "ERROR: engine exited before Playwright" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi

echo "==> Running Playwright demo and console-functional specs against live seeded data"
cd "$ROOT/web"
# console-keyboard-routes.spec.ts runs HERE and not in scripts/web-e2e.sh, and the reason is
# measured rather than stylistic: that script puts every spec which does not fill `#token`
# on one shared engine that nothing sets up, so a spec signing in as demo@olivares.local
# finds no login form, signs nobody in, and then grades the login page as if it were the
# ten routes. The spec carries the DEMO_TENANT guard so that case is a visible SKIP; this
# line is what makes it actually RUN.
# first-screens.spec.ts joins them for the same reason and with the same
# guard: it signs in as the demo operator, and on the shared engine of scripts/web-e2e.sh
# it would grade the login page in nine languages and call it green.
# session-work-surface.spec.ts likewise: it opens a COLD deep link in a second
# browser context carrying the signed-in state, which only means anything against an
# engine that actually has sessions in it.
# console-wide-standard.spec.ts for the same reason again, and with one of its own:
# it MEASURES the rail in nine languages (`scrollWidth > clientWidth`) and the computed
# font size of a painted element, and both of those need a real console with a real
# estate behind it — on the shared engine it would measure the login page nine times.
SPECS=(e2e/demo-graph.spec.ts e2e/console-func-l4.spec.ts e2e/console-keyboard-routes.spec.ts
  e2e/first-screens.spec.ts e2e/session-work-surface.spec.ts e2e/console-wide-standard.spec.ts)
# The access graph and the identity inventory are Community: the public specs above cover them.
# A non-Community build that serves them must bring its private suites; an installed one is always run.
if [ "$BUILD_EDITION" != community ]; then
  if [ "$GRAPH_STATUS" = 200 ]; then
    [ -f e2e/demo-graph-business.spec.ts ] || { echo 'ERROR: available Business graph requires its private E2E suite' >&2; exit 1; }
  fi
  if [ "$IDENTITY_STATUS" = 200 ]; then
    [ -f e2e/console-func-l4-business.spec.ts ] || { echo 'ERROR: available Business identity inventory requires its private E2E suite' >&2; exit 1; }
  fi
fi
for private in e2e/demo-graph-business.spec.ts e2e/console-func-l4-business.spec.ts; do
  [ -f "$private" ] && SPECS+=("$private")
done
# Playwright sends Secure browser-session cookies over the trusted localhost origin.
PLAYWRIGHT_BASE_URL="http://localhost:$PORT" DEMO_TENANT="$TENANT" \
  pnpm exec playwright test "${SPECS[@]}" "$@"
