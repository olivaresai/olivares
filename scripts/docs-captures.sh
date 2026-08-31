#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# generate the REAL console captures the public docs embed ("what you'll
# see in the console"). Same posture as scripts/web-e2e-demo.sh: build the web
# bundle + binary, boot `serve --insecure --seed-demo` (synthetic estate through
# the real event bus — no /v1 mocks), then run the docs-captures Playwright
# spec, which logs in for real and screenshots each console view in light and
# dark at a fixed viewport.
#
# Output: web/playwright-report/docs/<view>-<theme>.png
# The docs session curates which captures land in docs-site/src/assets/console/.
#
# Usage: scripts/docs-captures.sh [extra playwright args, e.g. --grep sessions]
# Requires: go, pnpm, Playwright chromium (`pnpm --dir web exec playwright install chromium`).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT, Y AQUÍ NO ES SÓLO HIGIENE: ES LO QUE IMPIDE QUE ESTE GUION
#    CONSTRUYA DENTRO DEL GATE DE OTRO. Medido y cazado en vivo el 2026-08-26.
#
#    `lint:git-env` corre en el hook `pre-push`. No LLAMA a los guiones de su clase: los EJECUTA
#    con `--olivares-git-env-probe` para ver si se aíslan. Este guion está en la clase (empareja
#    `mktemp -d` con git) y no cargaba la librería, así que la sonda lo ejecutaba entero — y este
#    guion reconstruye la consola en SU PROPIO worktree (`ROOT` sale de `dirname "$0"`, y
#    `vite.config.ts` va con `emptyOutDir: true`). Resultado: el gate vaciaba y reescribía
#    `core/internal/webui/dist` del árbol que se estaba empujando, y el push moría al FINAL con
#    «UN GATE MODIFICO EL ARBOL DE TRABAJO» tras pagarse entero. Coste medido: 1 h 48.
#
#    Y sólo se NOTABA cuando el bundle commiteado de esa rama estaba obsoleto: si estaba al día el
#    rebuild salía byte-idéntico y no dejaba rastro. Por eso parecía intermitente y por eso una
#    prueba en un árbol al día lo «refutaba» — un falso negativo.
#
#    Cargar la librería lo corta de raíz por el camino que el repo ya tiene:
#    `check-git-env-isolation.sh:156` da por aislado —SIN EJECUTARLO— a todo guion que la cargue.
#    `scripts/web-e2e.sh` la lleva desde antes y es la prueba de que el patrón funciona.
#    (Salir pronto ante la bandera NO vale: `:143-152` exige que la corrida envenenada falle Y se
#    comporte DISTINTO de la limpia, y dos salidas tempranas idénticas se cargan al miembro.)
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=lib/git-env.sh
. "$_olivares_git_env" || {
  echo "ERROR: cannot source $_olivares_git_env — refusing to run git beside a mktemp sandbox" >&2
  exit 1
}
unset _olivares_git_env
# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
PORT="${E2E_PORT:-8466}"
GRPC_PORT="$((PORT + 1))"
DATA="$(mktemp -d)"
BIN="$ROOT/bin/olivares"
# Disposable host tree for the governed sessions.workspace shown by video scene 10. The harness
# owns both its creation and cleanup; the API seeder only registers it.
WS_ROOT="$(mktemp -d)"

cleanup() {
  [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true
  rm -rf "$DATA" "$WS_ROOT" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

# The observed-session/run join is a three-file contract. Check it before paying for the web and Go
# builds so a renamed demo session fails at its source rather than much later as a missing tab.
bash "$ROOT/scripts/test-demo-agent.sh"

echo "==> Building web bundle + olivares binary"
pnpm --dir "$ROOT/web" install --frozen-lockfile >/dev/null 2>&1 || true
pnpm --dir "$ROOT/web" run build
# ⛔ LOS CONECTORES VAN ANTES DEL BINARIO, Y ESTE ARNES SE LOS SALTABA. `build-bin.sh` existe para
#    que nadie escriba su propio `go build` y derive de `task build:bin` — pero la dependencia
#    `build:connectors` vive en el Taskfile, no en la libreria, asi que sourcear la libreria NO
#    la trae. Resultado: el binario del arnes NO ES el artefacto que produce `task build:bin`.
#
#    No es teorico y esta medido el 2026-08-30: al registrar la fuente `claude` para llenar
#    `/adoption`, la recarga la RECHAZA con «the "claude" connector is not embedded in this build
#    (build it with `task build:connectors`, or run it from a collector)», rejected=1, y el
#    receptor OTLP no llega a existir. Es decir, la ausencia de una dependencia de build apagaba
#    en silencio el plano de datos de una vista entera, y la captura salia a cero.
#    Los plugins se `go:embed`ean en `cmd/olivares/firstparty/bins`, asi que TIENEN que existir
#    antes del `go build`: este orden no es preferencia, es la unica que funciona.
bash "$ROOT/scripts/build-connectors.sh"
build_olivares_bin "$BIN"

# A browsable workspace needs a real tree. These are synthetic service files in the disposable
# scratch directory, not paths or content borrowed from the repository being photographed.
mkdir -p "$WS_ROOT/src" "$WS_ROOT/deploy"
printf 'module acme-platform\n\ngo 1.26\n' >"$WS_ROOT/go.mod"
printf '# acme-platform\n\nBilling and entitlement services.\n' >"$WS_ROOT/README.md"
printf 'package main\n\nfunc main() {}\n' >"$WS_ROOT/src/main.go"
printf 'package billing\n' >"$WS_ROOT/src/billing.go"
printf 'replicas: 3\n' >"$WS_ROOT/deploy/values.yaml"

# ⛔ EL MOTOR EXTRAE SUS PLUGINS A `$TMPDIR` Y LOS EJECUTA, ASI QUE `$TMPDIR` TIENE QUE SER
#    EJECUTABLE. Medido en esta caja el 2026-08-30: `/tmp` esta montado **noexec**
#    (`/proc/mounts`: `rw,nosuid,nodev,noexec,...`), y un `.sh` con el bit puesto ahi da
#    «permission denied». El motor lo dijo casi entero por su cuenta —«el binario SI tiene el bit
#    de ejecucion […] es probable que /tmp este montado noexec»— y aun asi el sintoma llegaba
#    disfrazado de «el receptor no levanto».
#
#    No se asume: se PRUEBA. Un `noexec` no se puede deducir de la ruta —hay cajas donde /tmp
#    ejecuta y cajas donde no—, asi que se escribe un guion de una linea y se intenta correr.
#    Si ninguno de los candidatos ejecuta, se dice y se sigue: las otras ~70 vistas no dependen
#    de esto.
#
# ⛔ Y LA SONDA ES LA DE LA LIBRERIA, NO UNA COPIA. Este arnes llevaba su propia version inline y
#    con ella los TRES defectos que `scripts/lib/exec-tmpdir.sh` ya tenia curados: nombre de sonda
#    COMPARTIDO (`.probe-exec`, que con dos arneses a la vez es una loteria: 50 llamadas dieron 3
#    exitos), `run-$$` —que es el PID del shell PADRE y no distingue subshells— y un
#    `|| EXEC_TMP=""` que caia de vuelta al `/tmp` noexec en silencio. Curar la libreria y dejar
#    la copia viva es no curar nada: el arnes que dispara las capturas del acto es JUSTO el que
#    seguia con la version rota. Una convencion sin un testigo que la imponga es una costumbre, y
#    la costumbre no se propaga sola al fichero que no la copio.
. "$ROOT/scripts/lib/exec-tmpdir.sh"
if EXEC_TMP="$(olivares_exec_tmpdir)"; then
  echo "==> Plugin scratch dir (exec-capable, probed): $EXEC_TMP"
else
  # ⛔ AQUI SE DECIA Y SE SEGUIA, Y ESO ERA EL FALLO ORIGINAL CON OTRO TRAJE (the reviewer sobre
  #    `f610a26af`). Vaciar `EXEC_TMP` y arrancar con `${TMPDIR:-/tmp}` devuelve el motor al `/tmp`
  #    **noexec** que esta sonda existe para evitar: el motor extrae sus plugins a `$TMPDIR` y los
  #    LANZA, el `execve` falla, la recarga rechaza la fuente y `/adoption` sale a CERO — que es
  #    exactamente el sintoma que costo una mañana perseguir, y llega DISFRAZADO de «el receptor no
  #    levanto». Y este arnes es el unico que llama a `build-connectors.sh` antes del binario, o
  #    sea el unico donde los plugins SI estan embebidos.
  #
  #    Razone que «las otras ~70 vistas no dependen de esto» y por eso seguia. Es cierto y no basta:
  #    una corrida que produce una vista a cero EN SILENCIO es peor que una que no corre, porque
  #    los PNG se publican y nadie vuelve a mirar. Los cuatro lanzadores rehusan con rc 2 desde la
  #    v3; este se queda solo con la excepcion, y una excepcion sin razon es una costumbre.
  #
  #    El remedio es barato y esta impreso: exportar `OLIVARES_EXEC_TMPDIR` a un dir que ejecute.
  echo "docs-captures: ⛔ NO ARRANCO: ningun directorio temporal EJECUTA (¿/tmp noexec?)." >&2
  echo "   El motor extrae sus plugins de conector a \$TMPDIR y los LANZA, y este arnes los" >&2
  echo "   construye expresamente antes del binario. Arrancar igual dejaria el plano de" >&2
  echo "   conectores muerto SIN decirlo, y /adoption saldria a cero en las capturas." >&2
  echo "   (la libreria ya ha dicho por stderr QUE rutas probo)" >&2
  echo "   Remedio: exporta OLIVARES_EXEC_TMPDIR a un directorio que ejecute." >&2
  exit 2
fi

# ⛔ EL FICHERO DE TOKEN NO ES UNA CREDENCIAL FALSA: ES LO QUE EL MOTOR PIDE PARA NO DENEGAR.
#    Medido contra el motor vivo: `agent session create --transport stream-json` contesta
#    503 «inference credential source is not wired; stream-json launches are deny-closed (set
#    OLIVARES_SESSION_RUNTIME_WIF or OLIVARES_SESSION_RUNTIME_TOKEN_FILE). remote-control
#    launches do not need it» (`modules/sessions/runtime.go:461`). El motor nombra su propio
#    remedio.
#    Y aqui es legitimo porque el binario del runtime YA esta sustituido por `demo-agent.sh`,
#    que no llama a ningun proveedor: el token no se usa contra nadie, solo desbloquea el
#    emisor. Sin esto la tabla de /sessions se queda en UNA fila y la guia enseña un plano
#    vacio. Si algun dia el arnes lanzara un agente REAL, esto tendria que ser una credencial
#    de verdad — queda dicho aqui para que nadie lo herede sin verlo.
printf 'demo-harness-inference-token-not-a-real-credential\n' >"$DATA/demo-inference-token"
chmod 0600 "$DATA/demo-inference-token"

echo "==> Booting engine (insecure, demo-seeded) on 127.0.0.1:$PORT"
# Never launch the operator's real Claude binary from a documentation harness. The override points
# only this disposable engine at the deterministic, zero-network fixture; the real procRunner,
# admission, lifecycle ledger and workspace jail remain in the path.
OLIVARES_SESSION_RUNTIME_CLAUDE_BIN="$ROOT/scripts/demo-agent.sh" \
OLIVARES_SESSION_RUNTIME_TOKEN_FILE="$DATA/demo-inference-token" \
DEMO_SESSION_UNIQUE=1 \
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure --seed-demo --listen "127.0.0.1:$PORT" \
  --grpc-listen "127.0.0.1:$GRPC_PORT" --data-dir "$DATA" >"$DATA/engine.log" 2>&1 &
PID=$!

echo "==> Waiting for the engine to accept connections"
# ⛔ LA ESPERA ERA DE 20 s FIJOS Y NO TENÍA VEREDICTO. `seq 1 40` × `sleep 0.5` agota su presupuesto
#    y el guion **seguía igual**: el login devolvía vacío y el `python` que lo parsea reventaba con
#    un `JSONDecodeError: line 1 column 1` — un traceback que no nombra la causa. Medido el
#    2026-08-29 con `load1` en 40: el motor no llegó a tiempo y la corrida murió así.
#
# ⇒ El presupuesto sube y, sobre todo, **la espera dictamina**: si al agotarse el motor no responde,
#   se aborta diciéndolo y con la cola de SU log, que hasta ahora moría en el `mktemp -d`.
# ⚠ EL PRESUPUESTO SE FIJA POR VARIANZA MEDIDA, NO POR UNA MUESTRA. Medido el 2026-08-29 en esta
#   caja: arranque hasta `/healthz` en **35 s con load1 35,2** — y en una corrida real, con la misma
#   carga, **más de 60 s**. Un presupuesto que «cabe a veces» es la peor propiedad posible para una
#   espera: falla justo cuando la caja está ocupada, que es cuando más caro sale.
#   Cuatro minutos son baratos frente a una corrida de trece; y si de verdad no arranca, el veredicto
#   de abajo lo dice en vez de dejar que el fallo salga a tres pasos de aquí.
ARRIBA=0
for _ in $(seq 1 480); do
  if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then ARRIBA=1; break; fi
  sleep 0.5
done
if [ "$ARRIBA" -ne 1 ]; then
  echo "docs-captures: ⛔ EL MOTOR NO ACEPTÓ CONEXIONES en 240 s (load1 $(cut -d' ' -f1 /proc/loadavg))." >&2
  echo "  No es un fallo de las capturas: no hay contra qué capturar. Cola de su log:" >&2
  tail -20 "$DATA/engine.log" >&2 2>/dev/null || echo "  (sin log)" >&2
  exit 2
fi

TOKEN="$(curl -sf -X POST "http://127.0.0.1:$PORT/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
TENANT="$(curl -sf "http://127.0.0.1:$PORT/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"
if [ -z "$TENANT" ]; then
  echo "ERROR: could not resolve the demo tenant" >&2
  cat "$DATA/engine.log" >&2
  exit 1
fi

# ⛔ THE THREE CONNECTORS THE INTEGRATION GUIDES PHOTOGRAPH. `claude-code-prod`,
#    `codex-enterprise` and `grok-demo` are prose examples in the guides and exist NOWHERE
#    in the tree, so `--seed-demo` never created them and the Connectors tab had no row to
#    shoot. Seeded here, after the tenant resolves, because a source must name its tenant.
#    It runs BEFORE the views so the console is already in the state the guides describe.
OLIVARES_TOKEN="$TOKEN" OTLP_BASE="$((PORT + 30))" OLIVARES_ENGINE_PID="$PID" \
  bash "$ROOT/scripts/seed-guide-connectors.sh" \
  "$BIN" "$DATA" "$TENANT" "$PORT" || {
  echo "docs-captures: ⛔ could not seed the guide connectors; the three guide captures" >&2
  echo "   would photograph an empty Connectors tab and look like the product has none." >&2
  exit 1
}
echo "==> Demo tenant: $TENANT"

# ⛔ SEMBRAR WORK ITEMS, y por que aqui y no en `--seed-demo`. La muestra del 2026-08-26 fotografio
#    `/work` y salio EN BLANCO: la vista funciona y pinta un vacio honesto, pero el sembrador del
#    motor no crea work items. Una captura vacia NIEGA una funcion que existe.
#    En el motor no se puede todavia: `seedDemoEstate` (`boot.go:1915`) corre ANTES de que exista el
#    superadmin demo (`demo.go:346`), asi que ahi no hay dueño elegible. Aqui el TOKEN ya existe.
#    Se siembra por la API del producto, que respeta los invariantes (lease vacante + criterios de
#    aceptacion), no escribiendo el store a mano.
echo "==> Seeding work items through the product API"
python3 "$ROOT/scripts/seed-demo-work.py" \
  "http://127.0.0.1:$PORT" "$TOKEN" "$TENANT" "$WS_ROOT"

# ⛔ LLENAR LA FINCA HASTA UN OBJETIVO, que es distinto de que el payload entre. Medido el
#    2026-08-30 sobre motor virgen y tras la cadena entera de verificacion: 20 superficies tenian
#    contenido y solo SEIS llegaban a cuatro filas; nueve tenian exactamente UNA. Una pantalla con
#    una fila no ensena una tabla: ensena un caso. El objetivo por superficie vive en
#    `docs/launch/objetivos-sembrado.json`, que el guion LEE, y el guion es idempotente -- una
#    segunda corrida crea CERO filas, asi que un arnes que corre en cada captura no acumula.
#
# export-closure: absent-by-design docs/launch/objetivos-sembrado.json — es el catalogo de
# objetivos de sembrado del LANZAMIENTO, y esta curacion retira `docs/launch` ENTERO
# (the export curation script). Aqui es DATO, no una llamada: en el arbol publicado la ruta
# simplemente nunca casa, nada la ejecuta y por tanto no hay llamada que guardar. Declararlo
# `hub-only` seria mentir sobre su clase — `hub-only` exige una guarda de presencia en el sitio
# de la llamada, y aqui NO hay sitio de llamada.
#
# ⛔ RESTAURADA. La puso `abbad2074` y el merge de lote de mi propio `P-ganchos-capturas-v2-0830`
#    (`b11464bb8`) la REVIRTIO: mi claim nacio ANTES de esa cura y no la llevaba, asi que resolver
#    «a favor del claim, que es el que avanza» se llevo por delante seis lineas que el claim no
#    sabia que existian. Es el mecanismo que r26 midio y re-aplico para `b19541bbf` en ESE MISMO
#    lote — en el fichero que no re-comprobo. Hoy no es rojo porque `lint:export-closure` no
#    encuentra referencia viva, pero la proteccion estaba PERDIDA y la siguiente referencia a esta
#    ruta habria salido como fuga sin nada que la ampare.
#
#    No aborta la corrida si falla: sale 1 cuando alguna superficie se queda por debajo, y eso es
#    informacion para quien mire las capturas, no una razon para no tenerlas.
echo "==> Filling the estate to the per-surface target"
python3 "$ROOT/scripts/seed-estate-volume.py" \
  "http://127.0.0.1:$PORT" "$TOKEN" "$TENANT" || {
  echo "    (alguna superficie se queda por debajo de su objetivo; el reparto de arriba dice cual)"
}

# ⛔ UN CHECK DECLARADO Y NUNCA REPORTADO SALE «Unknown · — · 0 ms · —», y eso es la mitad de la
#    tabla en la captura de /health. No es un fallo del producto: «unknown» se INFIERE DEL
#    SILENCIO (cmd_health.go:1097 lo dice al rechazar --state unknown). El seed declaraba los
#    checks y no reportaba ninguno, asi que la foto de la guia enseñaba un panel de salud sin
#    salud. Se les manda una sonda real por la misma API que usaria un operador.
echo "==> Reporting a probe against each declared health check"
python3 - "$PORT" "$TOKEN" "$TENANT" <<'PYHEALTH' || echo "   (health probes skipped; the panel keeps its Unknown rows)" >&2
import json, sys, urllib.request

port, token, tenant = sys.argv[1], sys.argv[2], sys.argv[3]
base = f"http://127.0.0.1:{port}"

# ⛔ `X-Olivares-Tenant` NO ES OPCIONAL en el namespace `/v1/m/`: sin ella el motor contesta
#    400 y la primera version de esta sonda murio asi. Lo dice el sembrador de volumen, que
#    lleva la misma cabecera desde siempre (`seed-estate-volume.py:176`).
def call(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Authorization": f"Bearer {token}",
                                          "X-Olivares-Tenant": tenant,
                                          "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read() or b"{}")

checks = call("GET", "/v1/m/health/checks").get("items", [])
# Deja alguno DEGRADED a proposito: un panel donde todo esta perfecto no ensena que el panel
# distingue estados, y la guia habla de leer el estado, no de celebrarlo.
ok = 0
for n, c in enumerate(checks):
    cid = c.get("id") or c.get("check_id")
    if not cid:
        continue
    estado = "degraded" if n % 5 == 4 else "healthy"
    try:
        call("POST", f"/v1/m/health/checks/{cid}/report",
             {"state": estado, "latency_ms": 40 + (n * 7) % 160})
        ok += 1
    except Exception:
        pass
print(f"   {ok}/{len(checks)} checks reported")
PYHEALTH

# ⛔ SEMBRAR ADOPCION, POR LA MISMA RAZON QUE LOS WORK ITEMS Y CON UNA MEDIDA IGUAL DE CONCRETA.
#    La corrida del 2026-08-30 fotografio `/adoption` y salio a CERO en las cuatro pestanas
#    —«across 0 developer(s)», «0 team(s)», todos los contadores a 0—, y lo vi ABRIENDO EL PNG del
#    fallo, no deduciendolo. La vista funciona; lo que no hay son datos: `--seed-demo` no siembra
#    el plano OTLP, y esta pantalla se alimenta EXCLUSIVAMENTE de el.
#
#    Y no se arregla con un gancho de captura: un `despues` puede abrir la pestana `Trend` y
#    cambiar la lente, pero una grafica sin datos sigue vacia. Hacen falta las dos cosas.
#
#    Las dos precondiciones son de ARRANQUE, no de la vista, y estan medidas: el conector `claude`
#    tiene que estar registrado como fuente ANTES de que su receptor exista, y el receptor solo
#    nace al recargar (`kill -HUP`). La ruta de API `/v1/console/runtime/reload` NO sirve aqui:
#    contesta `step_up_required` (AAL3). Y un conector que YA tiene el puerto cogido no se
#    reconfigura en su sitio: mismo `http_addr` da `rejected=1`.
OTLP_PORT="$((PORT + 30))"
echo "==> Registering the claude connector for the OTLP plane on 127.0.0.1:$OTLP_PORT"
# ⛔ NAMED claude-code-prod, NOT claude-demo. The Claude Code guide photographs the
#    Connectors tab and its prose names this connector, and only ONE source may hold the
#    "olivares.claude" identity — so the OTLP source and the documented source have to be
#    the SAME row. Two of them means the second is rejected on reload and /adoption goes to
#    zero, which is measured, not hypothetical.
if "$BIN" sources set --name claude-code-prod --kind claude --tenant "$TENANT" \
  --actor "docs-captures" --reason "seed the adoption plane for documentation captures" \
  --data-dir "$DATA" \
  --config "http_addr=127.0.0.1:$OTLP_PORT" --config enable_grpc=false \
  --config resource_labels=team,project,cost_center >/dev/null 2>&1; then
  # El SIGHUP es lo que hace nacer el receptor; sin el, el POST de metricas no tiene a quien ir.
  kill -HUP "$PID" 2>/dev/null || true
  OTLP_UP=0
  for _ in $(seq 1 20); do
    if curl -sf --connect-timeout 1 --max-time 2 -o /dev/null \
      -X POST "http://127.0.0.1:$OTLP_PORT/v1/metrics" \
      -H 'Content-Type: application/json' -d '{"resourceMetrics":[]}'; then
      OTLP_UP=1
      break
    fi
    sleep 0.5
  done
  # ⛔ SE LEE EL VEREDICTO DE LA RECARGA, no solo el puerto. La primera version de este bloque
  #    decia «el receptor no levanto» y mandaba a mirar el puerto — cuando la causa real la habia
  #    dicho el motor con nombre y remedio en su propio log. Un diagnostico que no nombra la causa
  #    hace perder la tarde a quien lo lea.
  if [ "$OTLP_UP" != "1" ] && command grep -q 'source rejected on reload' "$DATA/engine.log" 2>/dev/null; then
    echo "    ⛔ la recarga RECHAZO la fuente; el motor dice por que:"
    command grep 'source rejected on reload' "$DATA/engine.log" | tail -1 | sed 's/^/       /'
  fi
  if [ "$OTLP_UP" = "1" ]; then
    echo "==> Seeding the adoption plane through the connector receiver"
    # ⛔ `resource_labels` TIENE QUE IR DESDE EL REGISTRO, no despues: sin el, `/adoption/teams`
    #    devuelve una sola fila «(unassigned)» y la pestana `Teams` sale igual de vacia que antes.
    python3 "$ROOT/scripts/seed-adoption-otlp.py" \
      "http://127.0.0.1:$PORT" "$TOKEN" "$TENANT" \
      --otlp "http://127.0.0.1:$OTLP_PORT/v1/metrics" || {
      echo "    (el sembrado de adopcion fallo; /adoption saldra a cero y se vera en la captura)"
    }
  else
    # ⛔ NO se aborta la corrida entera por esto, y se dice por que: las otras ~70 vistas no
    #    dependen del plano OTLP. Pero SE DICE EN VOZ ALTA, porque una captura de `/adoption` a
    #    cero se lee como «el producto no mide nada» y eso es falso.
    echo "    ⛔ el receptor OTLP no levanto en 127.0.0.1:$OTLP_PORT — /adoption saldra A CERO"
  fi
else
  echo "    ⛔ no pude registrar el conector claude — /adoption saldra A CERO"
fi

# ⛔ EL SEGUNDO MOTOR, Y POR QUE NO BASTA CON ANADIR `/setup` A LA LISTA.
#
# `registry.capture-coverage.test.ts` declaraba `/setup` como excepcion con su medida:
# *«redirige a /login sobre un estate ya instalado (medido); exige un arranque sin sembrar»*, y
# `web/src/app/pages/setup.tsx:68-69` lo dice en el codigo — *«Setup is a one-time door: once an
# admin exists, it is closed»* -> `<Navigate to="/login" />`. El motor de arriba arranca con
# `--seed-demo`, o sea YA INSTALADO. Anadir la ruta a `VIEWS` habria guardado LA PANTALLA DE LOGIN
# etiquetada como el asistente de instalacion, que es exactamente lo que esa declaracion predijo.
#
# ⇒ No se levanta la excepcion: se le quita el MOTIVO. Segundo motor, su propio `--data-dir`, SIN
#   sembrar, y **control positivo antes de fotografiar nada**: `/v1/server-info` tiene que decir
#   `setup_required: true`. Si dice otra cosa, este arnes NO publica una captura de esa ruta.
SETUP_PORT="$((PORT + 20))"
SETUP_DATA="$(mktemp -d)"
cleanup_setup() {
  [ -n "${SETUP_PID:-}" ] && kill "$SETUP_PID" 2>/dev/null || true
  rm -rf "$SETUP_DATA"
}
trap 'cleanup; cleanup_setup' EXIT

echo "==> Booting a SECOND engine, UNSEEDED, on 127.0.0.1:$SETUP_PORT (first-boot wizard)"
"$BIN" serve --insecure --listen "127.0.0.1:$SETUP_PORT" \
  --grpc-listen "127.0.0.1:$((SETUP_PORT + 1))" --data-dir "$SETUP_DATA" >"$SETUP_DATA/engine.log" 2>&1 &
SETUP_PID=$!
# ⛔ EL BUCLE GUARDA SU VEREDICTO Y COMPRUEBA QUE EL PROCESO SIGUE VIVO. Sin lo segundo, un motor
# que muere por colision de puerto deja que OTRO servicio conteste `/healthz` y `/v1/server-info`,
# y el control positivo de abajo estaria interrogando al proceso equivocado con toda su seguridad.
SETUP_UP=0
for _ in $(seq 1 40); do
  kill -0 "$SETUP_PID" 2>/dev/null || break
  if curl -sf --connect-timeout 2 --max-time 5 "http://127.0.0.1:$SETUP_PORT/healthz" >/dev/null 2>&1; then
    SETUP_UP=1
    break
  fi
  sleep 0.5
done
# CONTROL POSITIVO. Un motor que responda `setup_required: false` serviria un redirect a /login y
# la foto saldria igual; el oraculo `heading` la cazaria, pero el fallo se explica mejor aqui.
# ⛔ TRES RESPUESTAS, NUNCA DOS — y aqui faltaba la tercera, dicho por el contraste: *«un fallo de
# arranque, transporte o parseo deja /setup en skip y todavia permite RC 0»*. Un skip silencioso
# convierte «no he podido mirar» en «no hacia falta», que es el defecto mas caro de este repositorio.
# Ahora: si el motor no llego a estar en pie, se DICE y la corrida no puede salir verde por omision.
#
# ⚠ Y una honestidad que el contraste tambien senala y que NO puedo cerrar desde aqui: si el propio
# servidor no consigue leer sus usuarios, `isSetupComplete` cierra el acceso y `/server-info` acaba
# publicando `setup_required: true` (`core/api/middleware.go:218-225`, `handlers_core.go:42-46`).
# Desde el cliente ese `true` es indistinguible de un estate nuevo. Se deja escrito en vez de fingir
# que el control lo separa.
SETUP_REQUIRED=unknown
if [ "$SETUP_UP" = "1" ]; then
  SETUP_REQUIRED="$(curl -sf --connect-timeout 2 --max-time 10 "http://127.0.0.1:$SETUP_PORT/v1/server-info" \
    | python3 -c 'import sys,json;v=json.load(sys.stdin).get("setup_required");print("true" if v is True else ("false" if v is False else "unknown"))' 2>/dev/null || echo unknown)"
else
  echo "==> ⛔ el SEGUNDO motor no llego a aceptar conexiones (o murio): NO HE PODIDO MIRAR" >&2
fi
if [ "$SETUP_REQUIRED" = "true" ]; then
  echo "==> Second engine reports setup_required=true — the wizard is photographable"
  export SETUP_BASE_URL="http://127.0.0.1:$SETUP_PORT"
else
  echo "==> ⚠ Second engine reports setup_required=$SETUP_REQUIRED — NO capturo /setup (no he podido mirar)" >&2
  cat "$SETUP_DATA/engine.log" >&2 || true
fi

# ⛔ EL ID DE SESION SE RESUELVE EN VIVO, NO SE CODIFICA. La otra excepcion declarada era
# `/session-viewer/$id` — *«parametrica: exige sembrar una sesion y navegar a su id»*. Un id
# codificado seria PEOR que no tener captura: el dia que el sembrado cambie de ids, la vista
# serviria su estado de «no encontrada» y la foto saldria igual de verde.
# ⛔ CINCO SESIONES LANZADAS, PARA QUE LA TABLA SEA UNA TABLA (decision de the planner).
#    `/sessions` filtrada a «Launched» enseñaba UNA fila y medio metro de blanco: mejor que la
#    tabla de ceros que habia antes, pero no una tabla. «Launched» significa «al menos un run
#    enlaza con esta sesion» (`provenance.ts:26`), asi que hacen falta RUNS, no filas sueltas.
#
#    Y NO HACE FALTA CREDENCIAL: `agent session create` se autentica con el MISMO bearer que ya
#    usa el resto del sembrado (`--token/--tenant/--server`). Lo pregunto r4 expresamente y la
#    respuesta es que el seed SI puede lanzar mas de una. El binario del runtime ya apunta al
#    agente demo (OLIVARES_SESSION_RUNTIME_CLAUDE_BIN), asi que cada lanzamiento es determinista
#    y sin red.
echo "==> Launching governed sessions so /sessions is a table, not one row"
WS_REF="$(curl -sf "http://127.0.0.1:$PORT/v1/m/sessions/workspaces" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | python3 -c 'import sys,json;i=(json.load(sys.stdin).get("items") or []);print(i[0].get("workspace_ref","") if i else "")' 2>/dev/null || true)"
if [ -n "$WS_REF" ]; then
  LANZADAS=0
  while IFS='|' read -r nombre modelo; do
    [ -n "$nombre" ] || continue
    # ⛔ EL ERROR SE GUARDA, NO SE TIRA. La primera version mandaba stderr a /dev/null y la
    #    corrida dijo «0 session(s) launched» sin una sola pista: tuve que diagnosticarlo a
    #    mano contra el motor vivo. Un fallo silencioso aqui deja /sessions con una fila y
    #    nadie sabe por que.
    # ⛔ `--env-allow` NO ES OPCIONAL AQUI: la sesion NO HEREDA el entorno del motor. El propio
    #    flag lo dice — «host env var NAMES to forward to the session (allowlist; nothing else
    #    is inherited)». Sin esto, `DEMO_SESSION_UNIQUE` se exporta al MOTOR, el motor
    #    lanza el agente con el entorno limpio, el agente no la ve y emite su id fijo: cinco
    #    lanzamientos volvieron a colapsar en UNA fila. Es aislamiento bien hecho del producto,
    #    y hay que pedirle el paso explicitamente.
    if "$BIN" agent session create --name "$nombre" --model "$modelo" \
        --workspace "$WS_REF" --transport stream-json \
        --env-allow DEMO_SESSION_UNIQUE \
        --server "http://127.0.0.1:$PORT" --token "$TOKEN" --tenant "$TENANT" \
        --insecure >/dev/null 2>"$DATA/launch-$LANZADAS.err"; then
      LANZADAS=$((LANZADAS + 1))
    elif [ "$LANZADAS" = "0" ]; then
      echo "   ⛔ launch refused; the engine says:" >&2
      # ⛔ `grep … | head … || tail …` LEE EL RC DE `head`, que siempre es 0, asi que el
      #    respaldo NUNCA corria y un error sin `"message"` salia como una linea vacia. Es la
      #    misma trampa de la tuberia que ya me ha mordido hoy; se separa en dos pasos.
      # ⛔ Y el `|| true` TAMPOCO es higiene: sin el, esta linea mata el arnes entero por
      #    `set -euo pipefail`, y lo mata en la RUTA DE DIAGNOSTICO. Dos formas, medidas: sin
      #    `"message"` en el log, `grep` sale 1; y con MUCHAS coincidencias, `head` cierra la
      #    tuberia y `grep` recibe SIGPIPE (141). `pipefail` propaga los dos. En ambos casos el
      #    respaldo `tail -2` de la linea siguiente NO llega a correr: el arnes muere sin decir
      #    nada justo cuando ya habia algo que contar.
      MSG="$( { grep -oE '"message":"[^"]*"' "$DATA/launch-0.err" 2>/dev/null || true; } | head -1)"
      if [ -n "$MSG" ]; then echo "   $MSG" >&2; else tail -2 "$DATA/launch-0.err" >&2; fi
    fi
  done <<'SESIONES'
billing-migration review|claude-opus-4-8
entitlement audit sweep|claude-opus-4-8
deploy-values reconcile|claude-sonnet-4-5
incident postmortem draft|claude-opus-4-8
dependency upgrade scan|claude-sonnet-4-5
SESIONES
  echo "   $LANZADAS session(s) launched"
else
  echo "   ⚠ no workspace ref: the launched sessions are NOT seeded and /sessions stays at one row" >&2
fi

DEMO_SESSION_ID="$(curl -sf "http://127.0.0.1:$PORT/v1/m/recording/sessions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | python3 -c 'import sys,json;d=json.load(sys.stdin);i=(d.get("items") or []);print(i[0]["id"] if i else "")' 2>/dev/null || echo "")"
if [ -n "$DEMO_SESSION_ID" ]; then
  echo "==> Demo recording session: $DEMO_SESSION_ID"
  export DEMO_SESSION_ID
else
  echo "==> ⚠ el estate sembrado no expone ninguna sesion de grabacion — NO capturo /session-viewer" >&2
fi

# ⛔ LA TANDA SE CIERRA ANTES DE EMPEZAR, y esto lo aporto un contraste externo el 2026-08-20.
# Su frase: *«PUBLICAR=1 no demuestra que publica la tanda verde que acaba de comprobar … el
# directorio de artefactos no se limpia … una captura vieja o parcial puede viajar en una corrida
# verde»*. Es correcto y es la clase de siempre: **el sujeto de la publicacion era «lo que haya en
# el directorio», no «lo que esta corrida produjo»**, y las dos cosas se ven igual cuando todo va
# bien. Con una corrida filtrada (`--grep`) se separan, y ahi la publicacion miente.
rm -rf "$ROOT/web/playwright-report/docs"
mkdir -p "$ROOT/web/playwright-report/docs"

echo "==> Running the docs-captures Playwright spec against live seeded data"
cd "$ROOT/web"
RC=0
PLAYWRIGHT_BASE_URL="http://127.0.0.1:$PORT" DEMO_TENANT="$TENANT" \
  pnpm exec playwright test e2e/docs-captures.spec.ts "$@" || RC=$?

# El manifiesto de procedencia de la tanda. Se funde SIEMPRE, también tras una corrida roja: si
# faltan capturas, es justo cuando hace falta saber cuáles hay y de qué árbol salieron.
echo "==> Fusionando la evidencia de cada toma en manifest.json"
DOCS_DIR="$ROOT/web/playwright-report/docs" python3 - <<'PY' || true
import glob, json, os, subprocess, time

d = os.environ["DOCS_DIR"]
tomas = []
for f in sorted(glob.glob(os.path.join(d, "*.evidence.json"))):
    with open(f, encoding="utf8") as fh:
        tomas.append(json.load(fh))
    os.remove(f)  # el sidecar es un intermedio; la autoridad es el manifiesto


def git(*a):
    try:
        return subprocess.run(["git", *a], capture_output=True, text=True, check=True).stdout.strip()
    except Exception:
        return None


# ⛔ EL `dirty` NO ES ADORNO Y VA JUNTO AL SHA: C10-03 pide «manifiesto con SHA de toma», y un SHA
#    tomado sobre un árbol sucio NO describe lo que se fotografió. Publicar el SHA a secas sería
#    una afirmación de procedencia falsa — el lector no puede reconstruir la imagen desde él.
sucio = git("status", "--porcelain")
por_vista = {}
for t in tomas:
    por_vista.setdefault(t["id"], set()).add(t["theme"])

# «Pares light/dark de la MISMA toma» (C10-03): con un solo manifiesto por tanda, el par lo prueba
# el propio fichero. Lo que hay que decir en voz alta es qué vista NO tiene su par.
descabalados = sorted(k for k, v in por_vista.items() if v != {"light", "dark"})

man = {
    "taken_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    "commit": git("rev-parse", "HEAD"),
    "tree_dirty": bool(sucio),
    "views": len(por_vista),
    "captures": len(tomas),
    "unpaired": descabalados,
    # ⛔ CENSO, NO CONTADOR. Las vistas que salen VACÍAS con el estate sembrado son el objetivo de
    #    C10-02 (sembrado por ruta), y hasta hoy eran «8 placeholders» de un informe del 08-15 que
    #    nadie podía reproducir. Un número no dice CUÁLES, así que envejece sin avisar: se listan.
    #
    # ⚠ Y NO SE ADJUDICA AQUÍ. Una pantalla vacía puede ser correcta —no todo se siembra— o puede
    #    ser un hueco que la documentación pública enseña como si fuese el producto. El arnés no
    #    sabe cuál es cuál; lo que puede hacer, y hace, es no dejar que la pregunta se pierda.
    # Cada vista con su par de cifras, ordenada por la que más se parece a un hueco de sembrado:
    # pocos caracteres en `main` y al menos un panel vacío. NO es una lista de «vistas vacías».
    "empty_panels": [
        {"id": i, "paneles_vacios": v, "texto_main": m}
        for i, v, m in sorted(
            {
                (t["id"], t.get("vacios", 0), t.get("texto_main", 0))
                for t in tomas
                if t.get("vacios", 0) > 0 and t["theme"] == "light"
            },
            key=lambda x: (x[2], -x[1]),
        )
    ],
    # CONTROL, NO HUECO: estas dos salen sin filas porque algo se NIEGA a fabricarlas, y eso es
    # lo que hay que ense~nar. Declaradas para que nadie las lea como deuda de sembrado ni las
    # "arregle" ma~nana inventando una fila. Adjudicado por the planner / the planner, 2026-08-26.
    "empty_by_control": {
        "rate-limits": "the Anthropic Admin-API governance ingest is not wired; the view shows"
                       " the reason rather than a fabricated inventory",
        "console": "AAL3 step-up required to issue an invitation, so the pending-invitations"
                   " panel stays empty; a password-token seeder must not mint accounts",
    },
    # ⛔ AUSENCIA DECLARADA, que NO es lo mismo que vacia. `empty_by_control` de arriba habla de
    #    vistas que SE FOTOGRAFIAN y salen sin filas; esta habla de una ruta que NO se fotografia.
    #    Meterla en la de arriba haria que el manifiesto afirmase una toma que no existe — la misma
    #    clase de autoridad falsa que su retirada viene a cerrar. El CONTROL es el mismo que ya
    #    declara `console`; el HECHO es otro.
    "not_captured_by_control": {
        "accept-invite": "issuing an invitation requires AAL3 step-up (hardware,"
                         " phishing-resistant) and the harness session is AAL1, so a live"
                         " invitation cannot be seeded; the tokenless screen is an ERROR state"
                         " and publishing it as the view would be false authority. Retired"
                         " 2026-08-29 rather than published stale-by-control: a PNG that can"
                         " never be refreshed is a future trap. Restoring it needs AAL3 in the"
                         " harness, not a new selector.",
    },
    # CLAVES i18n CRUDAS: cero es la unica cifra aceptable. check-i18n-usage.mjs no mira las
    # dinamicas, asi que una clave que falte se PUBLICA cruda y ningun gate lo dice.
    "raw_i18n_keys": sum(t.get("claves_crudas", 0) for t in tomas),
    "raw_i18n_keys_by_capture": [
        {"id": t["id"], "theme": t["theme"], "n": t.get("claves_crudas", 0),
         "vistas": t.get("claves_crudas_vistas", [])}
        for t in sorted(tomas, key=lambda t: (t["id"], t["theme"]))
        if t.get("claves_crudas", 0) > 0
    ],
    "take": sorted(tomas, key=lambda t: (t["id"], t["theme"])),
}
with open(os.path.join(d, "manifest.json"), "w", encoding="utf8") as fh:
    json.dump(man, fh, indent=2, ensure_ascii=False)
    fh.write("\n")

if man["raw_i18n_keys"]:
    print("    CLAVES i18n CRUDAS: %d" % man["raw_i18n_keys"])
    for e in man["raw_i18n_keys_by_capture"][:8]:
        print("        %-26s %-6s %d  %s" % (e["id"], e["theme"], e["n"], ", ".join(e["vistas"][:4])))
else:
    print("    claves i18n crudas: 0")

if man["empty_panels"]:
    print(f"    ⚠ {len(man['empty_panels'])} vista(s) con algún panel vacío — candidatas de C10-02, NO «vistas vacías»:")
    for e in man["empty_panels"][:12]:
        print(f"      {e['id']:24} paneles vacíos={e['paneles_vacios']:2}  texto en main={e['texto_main']}")
print(f"    {man['captures']} capturas de {man['views']} vistas · commit {man['commit']}"
      + (" · ⚠ ÁRBOL SUCIO: el sha NO reproduce estas imágenes" if man["tree_dirty"] else ""))
if descabalados:
    print(f"    ⚠ sin par light/dark: {', '.join(descabalados)}")
PY

# ⛔⛔ EL PASO QUE FALTABA, Y ES LA CAUSA DE UNA CLASE ENTERA, NO DE UN CASO.
#
# Este guion escribia en `web/playwright-report/docs/` y ahi se acababa: la curacion hacia
# `docs-site/public/console/` era A MANO. Medido dos veces con el mismo resultado:
#   · C10-03 (08-18): *«el arnes ya cubria las 52; lo que nunca paso fue publicar su salida»*.
#   ·   (08-20): cuatro vistas —`login`, `accept-invite`, `settings`, `status-page`— llevaban
#     dias en `VIEWS` como codigo vivo, capturandose en cada corrida, y **sin un solo PNG
#     publicado**. Son el camino que recorre un cliente NUEVO antes de ver nada mas.
#
# ⇒ «Capturado» tiene que implicar «publicado», o la deuda vuelve. `--publish` copia la tanda.
#
# Y publica CON CONDICIONES, porque una publicacion sin ellas es peor que la curacion manual:
#   1 · corrida VERDE (`RC=0`): una tanda roja tiene tomas que no se comprobaron.
#   2 · arbol LIMPIO: C10-01 exige identidad byte a byte entre destinos y procedencia reconstruible;
#       un SHA tomado sobre un arbol sucio NO reproduce las imagenes, y el manifiesto ya lo dice.
#   3 · el manifiesto viaja con las imagenes: sin el, un PNG publicado no tiene procedencia.
if [ "${PUBLICAR:-0}" = "1" ]; then
  SRC="$ROOT/web/playwright-report/docs"
  if [ "$RC" -ne 0 ]; then
    echo "==> ⛔ NO publico: la corrida salio $RC. Una tanda roja tiene tomas sin comprobar." >&2
    exit "$RC"
  fi
  # ⛔ SIN `| head -1`, y no es estilo: bajo `set -o pipefail` una tuberia cuyo consumidor sale
  # antes devuelve **141 CUANDO HAY SALIDA**, o sea la comprobacion revienta justo cuando el arbol
  # esta sucio — el caso que existe para cazar. Es la clase que `lint:sigpipe-booleans` persigue y
  # que el integrador midio como la mitad de lo que rechaza. Se lee entero y se mira si esta vacio.
  DIRTY="$(cd "$ROOT" && git status --porcelain)"
  if [ -n "$DIRTY" ]; then
    echo "==> ⛔ NO publico: el arbol esta SUCIO, asi que el SHA del manifiesto no reproduce estas" >&2
    echo "       imagenes. Commitea o limpia y vuelve a correr." >&2
    exit 2
  fi
  DEST="$ROOT/docs-site/public/console"
  mkdir -p "$DEST"
  # ⛔ SE PUBLICA POR GLOB DEL DIRECTORIO, NO POR UNA LISTA DE NOMBRES, y el motivo lo dio un gate
  # rechazando este mismo push. `lint:export-closure` cazo dos referencias literales mias a
  # `manifest.json` —origen y destino— con el veredicto exacto: «exists in neither the export nor
  # the hub (dangling reference)». Y cuando las declare `hub-only`, me corrigio otra vez y mejor:
  # «declared hub-only but no such path exists in the hub — the declaration names nothing». Tenia
  # razon las dos veces: el manifiesto lo PRODUCE esta misma corrida, asi que en un arbol en reposo
  # —publicado o no— no esta, y `hub-only` significa «esta en el hub y no en el export», que es otra
  # cosa. Iterar el directorio publica lo que la tanda haya producido y no nombra ningun fichero
  # ausente: es la misma forma que el glob de PNG que ya pasaba el gate, y ademas no se queda
  # obsoleta el dia que el arnes emita un artefacto mas.
  # ⛔ CAPTURAR Y PUBLICAR DEJAN DE SER LO MISMO, y esta lista existe porque el paso de
  # publicacion que anade esta misma rama le CAMBIO EL SIGNIFICADO a una toma que ya existia.
  #
  # `/accept-invite` sin token pinta un aviso en ROJO —«This invitation link is incomplete»— y su
  # entrada en el spec lo declaraba, con razon, como «la pantalla que ve quien abre un enlace de
  # invitacion». Mientras la curacion era MANUAL esa imagen nunca salia del informe. En cuanto
  # publicar es automatico, seria la captura representativa del flujo de invitacion en la
  # documentacion publica — y §1.4 del canon prohibe exactamente eso.
  #
  # ⇒ La toma se conserva (si esa pantalla se rompiera del todo, su oraculo `heading` lo caza) y
  #   se retiene su publicacion. Se quita de aqui cuando el arnes pueda sembrar una invitacion viva
  #   y fotografiar el estado CON token, que es el sembrado por ruta de C10-02.
  #
  # ⚠ Y lo encontro MIRAR EL PNG. Ninguna metrica del manifiesto la marco, porque la pantalla no
  #   esta vacia: esta en estado de ERROR, que es una clase que ninguna de las tres cifras cubre.
  # ⛔ VACIA DESDE EL 2026-08-29, y el mecanismo se CONSERVA a proposito. Llevaba
  #    "accept-invite" porque su toma era la pantalla de error; hoy esa toma no se genera
  #    —se retiro del spec, adjudicado por the planner— asi que no hay nada que retener y la
  #    ruta se declara como ausencia en `not_captured_by_control`. La lista se queda porque
  #    la clase que la motivo —capturar y publicar dejaron de ser lo mismo— no ha muerto.
  no_publicar=""
  n=0
  manifiesto=0
  retenidas=0
  for art in "$SRC"/*; do
    [ -f "$art" ] || continue
    base="$(basename "$art")"
    case "$base" in
    *.evidence.json) continue ;;
    manifest.json) manifiesto=1 ;;
    esac
    id_toma="${base%-light.png}"; id_toma="${id_toma%-dark.png}"
    case " $no_publicar " in
    *" $id_toma "*)
      retenidas=$((retenidas + 1))
      continue
      ;;
    esac
    cp -f "$art" "$DEST/$base"
    n=$((n + 1))
  done
  if [ "$manifiesto" -eq 0 ]; then
    # Sin manifiesto no hay PROCEDENCIA, y una imagen publicada sin procedencia es exactamente lo
    # que C10-01 prohibe: nadie puede reconstruir de que arbol salio. Se rehusa, no se avisa.
    echo "==> ⛔ $n artefacto(s) copiados SIN manifiesto: no hay procedencia. Re-corre el arnes." >&2
    exit 2
  fi
  echo "==> PUBLICADOS $n artefacto(s) en docs-site/public/console/ (manifiesto incluido)"
  [ "$retenidas" -gt 0 ] && echo "==> RETENIDAS $retenidas toma(s) por estado declarado no representativo: $no_publicar" >&2
  echo "==> Comprueba el techo en el MISMO commit:  task lint:screenshot-coverage"
fi

exit $RC
