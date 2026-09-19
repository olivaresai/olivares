#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-console-walk.sh — prove the browser walk tells the three answers apart, against a
# DECOY console rather than the real one.
#
# WHY THIS EXISTS. console-walk.mjs is the only thing in this repository that opens a
# browser, so every claim of the form "the console works" rests on it — and on 2026-08-07
# it was measured wrong in two ways at once, both of which made it QUIETER than the truth:
#
#   1. It could not resolve playwright at all and exited 2, "could not look". An empty walk
#      reads like a clean one to anyone skimming a log.
#   2. Its finding predicate read only (pageErr | badReq | conErr). A screen whose navigation
#      TIMED OUT printed `nav=Timeout 40000ms exceeded` and was then counted as CLEAN, exit
#      code and all. A walk could report "0 with findings" over a screen that never loaded.
#
# Neither is visible to a unit test of the console, because neither is about the console.
# So this drives the real script against a purpose-built decoy server whose four screens are
# each a known answer, and asserts the walk says what the decoy is.
#
# THE DECOY IS THE POINT. Reproducing is mutating: pointing this at the live engine would
# make the test's verdict depend on the engine's current defects, and today the real console
# has two (a 404 and a 501) that would make a correct walk exit 1 forever. The decoy owns its
# own port and a throwaway output directory, touches no repository state, and needs no engine.
#
# Answers: 0 the walk classifies all four screens correctly / 1 it does not / 2 could not look.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

TMP="$(mktemp -d)"
cleanup() {
  local pid
  for pid in "${SRV_PID:-}" "${SRV3_PID:-}"; do
    [ -z "${pid}" ] || kill "${pid}" 2>/dev/null || true
  done
  if [ -s "${TMP}/workflow-engine.pid" ]; then
    kill "$(cat "${TMP}/workflow-engine.pid")" 2>/dev/null || true
  fi
  rm -rf "${TMP}"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

PW_DIR="$(node --input-type=module - <<'JS'
import { createRequire } from 'node:module'
import { dirname } from 'node:path'
import { pathToFileURL } from 'node:url'
const require = createRequire(pathToFileURL(`${process.cwd()}/web/package.json`))
console.log(dirname(require.resolve('@playwright/test/package.json')))
JS
)" || fail "install the declared web dependencies before testing console-walk"

# Replay the nightly's real run block under the runner's bash -e semantics. The
# disposable engine only supplies a setup token; result 2 comes from the real
# loader in a fresh checkout without dependencies, as in the failed nightly.
mkdir -p "${TMP}/layout/scripts" "${TMP}/layout/web" "${TMP}/bin"
cp scripts/console-walk.mjs "${TMP}/layout/scripts/console-walk.mjs"
cp web/package.json "${TMP}/layout/web/package.json"
awk '
  /^      - name: console walk against a live engine / { step = 1; next }
  step && /^      - / { exit }
  step && /^        run: \|/ { body = 1; next }
  step && body { sub(/^          /, ""); print }
' .github/workflows/drills-nightly.yml > "${TMP}/workflow.sh"
grep -q 'task -x console:walk' "${TMP}/workflow.sh" \
  || fail "could not extract the nightly console walk run block"

cat > "${TMP}/bin/go" <<'SH'
#!/usr/bin/env bash
set -eu
cat > "$RUNNER_TEMP/olivares" <<'ENGINE'
#!/usr/bin/env bash
echo "$$" > "$WALK_TEST_TMP/workflow-engine.pid"
echo 'olst_TESTONLY'
exec sleep 30
ENGINE
chmod +x "$RUNNER_TEMP/olivares"
SH
cat > "${TMP}/bin/task" <<'SH'
#!/usr/bin/env bash
if [ "$WALK_TEST_RC" = 2 ]; then
  exec env -u OLIVARES_WALK_PW node "$WALK_TEST_TMP/layout/scripts/console-walk.mjs"
fi
if [ "$WALK_TEST_RC" = driver ]; then
  [ -f "$WALK_TEST_TMP/browser-install" ] || exit 72
  exec env -u OLIVARES_WALK_PW OLIVARES_WALK_CHROMIUM="$WALK_TEST_TMP/no-browser" \
    OLIVARES_WALK_OUT="$WALK_TEST_TMP/driver-out" \
    node "$WALK_TEST_TMP/layout/scripts/console-walk.mjs"
fi
exit "$WALK_TEST_RC"
SH
# Browser installation is a separate boundary. This replay must not download
# anything. The install fixture exposes the real dependency only when the
# workflow provisions the locked web graph; removing that command must fail.
cat > "${TMP}/bin/pnpm" <<'SH'
#!/usr/bin/env bash
set -eu
if [ "$WALK_TEST_RC" = driver ]; then
  case "$*" in
    '--dir web install --frozen-lockfile')
      mkdir -p "$WALK_TEST_TMP/layout/web/node_modules/@playwright"
      ln -s "$WALK_TEST_PW_DIR" "$WALK_TEST_TMP/layout/web/node_modules/@playwright/test"
      ;;
    '--dir web exec playwright install --with-deps chromium')
      touch "$WALK_TEST_TMP/browser-install"
      ;;
    *) exit 72 ;;
  esac
fi
SH
printf '#!/usr/bin/env bash\nexit 0\n' > "${TMP}/bin/npx"
# A regression must never run the old process-pattern cleanup on this host.
printf '#!/usr/bin/env bash\nexit 73\n' > "${TMP}/bin/pkill"
chmod +x "${TMP}/bin/"*

for expected in 2 1 0 driver; do
  mkdir -p "${TMP}/workflow-${expected}"
  : > "${TMP}/workflow-${expected}/output"
  set +e
  PATH="${TMP}/bin:${PATH}" WALK_TEST_TMP="${TMP}" WALK_TEST_RC="${expected}" \
    WALK_TEST_PW_DIR="${PW_DIR}" \
    RUNNER_TEMP="${TMP}/workflow-${expected}" \
    GITHUB_OUTPUT="${TMP}/workflow-${expected}/output" \
    bash -e "${TMP}/workflow.sh" > "${TMP}/workflow-${expected}/log" 2>&1
  workflow_rc=$?
  set -e
  # Keep failed test attempts bounded too: the old unguarded pipeline leaves
  # its engine alive when bash -e exits before the reporting code.
  engine_alive=0
  if [ -s "${TMP}/workflow-engine.pid" ]; then
    engine_pid="$(cat "${TMP}/workflow-engine.pid")"
    if kill -0 "${engine_pid}" 2>/dev/null; then
      engine_alive=1
      kill "${engine_pid}" 2>/dev/null || true
    fi
    rm "${TMP}/workflow-engine.pid"
  fi
  [ "${workflow_rc}" = 0 ] \
    || { cat "${TMP}/workflow-${expected}/log" >&2; fail "nightly exited ${workflow_rc} before reporting walk result ${expected}"; }
  [ "${engine_alive}" = 0 ] || fail "nightly left its engine alive after walk result ${expected}"
  failed=true
  [ "${expected}" != 0 ] || failed=false
  grep -qx "failed=${failed}" "${TMP}/workflow-${expected}/output" \
    || fail "nightly did not classify walk result ${expected} as failed=${failed}"
  if [ "${expected}" = 2 ]; then
    grep -q 'console-walk: cannot load playwright' "${TMP}/workflow-${expected}/log" \
      || fail "the workflow's exit-2 test did not reach the real loader refusal"
  fi
  if [ "${expected}" = driver ]; then
    grep -q 'playwright loaded via web/ @playwright/test' "${TMP}/workflow-${expected}/log" \
      || { cat "${TMP}/workflow-${expected}/log" >&2; fail "nightly did not provision its declared web driver"; }
    grep -q 'no browser could be launched' "${TMP}/workflow-${expected}/log" \
      || fail "the provisioned driver did not reach browser startup"
  fi
done
echo "OK: nightly provisions the declared driver, reports walk results 0/1/2, and reaps its engine."

# The browser leg reuses that pnpm layout: no root playwright and no direct
# web/playwright link. Only @playwright/test is visible to the copied real loader.

# ---------------------------------------------------------------------------
# The decoy console. Four screens, four known answers.
#   /clean     nothing pending                      -> clean
#   /sse       a held-open text/event-stream        -> STREAMING, and NOT a finding
#   /stall     a held-open NON-stream response      -> a finding (this is the hole from #2)
#   /notfound  a fetch the server answers 404       -> a finding
# The distinction between /sse and /stall is the whole test: both leave a request pending
# forever, and only the content-type tells them apart.
# ---------------------------------------------------------------------------
cat > "${TMP}/decoy.mjs" <<'JS'
import { createServer } from 'node:http'
// Which screens this instance advertises. Leg 3 starts a second instance carrying ONLY the
// stall, so that a stall is the sole reason the run is not clean.
const ROUTES = (process.env.DECOY_ROUTES || 'clean,sse,stall,notfound,gated501,roto501,vecino501').split(',')
const page = (body) => `<!doctype html><html><body>
<nav>${ROUTES.map((r) => `<a href="/${r}">${r}</a>`).join('')}</nav>
${body}</body></html>`
const srv = createServer((req, res) => {
  const u = req.url.split('?')[0]
  if (u === '/api/stream') {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
    res.write('data: hello\n\n')
    return // held open on purpose, never ended
  }
  if (u === '/api/hang') {
    res.writeHead(200, { 'content-type': 'application/json' })
    return // held open on purpose, and NOT a stream
  }
  if (u === '/api/missing') {
    res.writeHead(404, { 'content-type': 'application/json' })
    return res.end('{"error":"nope"}')
  }
  // Los dos 501, que es la distinción que este señuelo vino a probar. La gateada usa el
  // prefijo REAL de una feature que declara su puerta (features/reporting/api.ts comprueba
  // `status === 501`); la rota usa un prefijo que no gatea nadie.
  // El tercero es el que el contraste `sol max` señaló el 2026-08-14: MISMO módulo que una
  // puerta declarada, RUTA distinta. Gatearlo sería tragarse un 501 inesperado.
  if (
    u === '/v1/m/reporting/schedules' ||
    u === '/v1/m/nadie-gatea-esto/x' ||
    u === '/v1/m/reporting/otra-cosa'
  ) {
    res.writeHead(501, { 'content-type': 'application/json' })
    return res.end('{"error":"not implemented"}')
  }
  const scripts = {
    '/sse': `<script>fetch('/api/stream')</script>`,
    '/stall': `<script>fetch('/api/hang')</script>`,
    '/notfound': `<script>fetch('/api/missing')</script>`,
    '/gated501': `<script>fetch('/v1/m/reporting/schedules')</script>`,
    '/roto501': `<script>fetch('/v1/m/nadie-gatea-esto/x')</script>`,
    '/vecino501': `<script>fetch('/v1/m/reporting/otra-cosa')</script>`,
  }
  res.writeHead(200, { 'content-type': 'text/html' })
  res.end(page(scripts[u] ?? '<p>clean</p>'))
})
srv.listen(0, '127.0.0.1', () => console.log(srv.address().port))
JS

node "${TMP}/decoy.mjs" > "${TMP}/port" 2>"${TMP}/decoy.err" &
SRV_PID=$!
for _ in $(seq 1 50); do [ -s "${TMP}/port" ] && break; sleep 0.1; done
PORT="$(cat "${TMP}/port" 2>/dev/null || true)"
[ -n "${PORT}" ] || { cat "${TMP}/decoy.err" >&2; fail "decoy server never reported a port"; }

# ---------------------------------------------------------------------------
# Leg 1 — classification. IDLE_MS is turned down because the two pending screens are
# SUPPOSED to burn it; that is the cost per streaming screen, not a timeout budget.
# ---------------------------------------------------------------------------
set +e
OLIVARES_WALK_BASE="http://127.0.0.1:${PORT}" \
OLIVARES_WALK_OUT="${TMP}/out" \
OLIVARES_WALK_IDLE_MS=2000 \
OLIVARES_WALK_TOKEN='' \
OLIVARES_WALK_PW='' \
  node "${TMP}/layout/scripts/console-walk.mjs" > "${TMP}/walk.log" 2>&1
RC=$?
set -e

if [ "${RC}" = "2" ]; then
  echo "----- walk output -----" >&2; cat "${TMP}/walk.log" >&2
  fail "the walk could not look (exit 2). That is not a clean run and not a verdict."
fi

grep -q 'playwright loaded via web/ @playwright/test' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "the walk did not load the declared web dependency"; }

grep -q 'route/sse.*(streaming)' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "/sse was not classified STREAMING — a held-open text/event-stream is the feature working, and reporting it as a timeout is the false finding this walk already paid for once."; }

grep -q 'route/stall.*STALLED\|STALLED.*api/hang' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "/stall was not reported STALLED — a screen whose request never settles and is NOT a stream is a real defect, and the old predicate counted it as clean."; }

grep -q '404.*api/missing' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "the 404 on /notfound was not reported"; }

# ⛔ LA DISTINCIÓN QUE ESTE LEG VINO A PROBAR: un 501 en una ruta que la consola DECLARA gatear
# no es un hallazgo; uno en una ruta que nadie gatea, sí. Antes del 2026-08-13 los dos eran lo
# mismo, y el único «hallazgo» de un walk sobre un `main` limpio resultó ser comportamiento
# correcto — 55 pantallas para señalar la puerta comercial de reporting.
grep -q 'GATEADO 501 .*reporting/schedules' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "el 501 GATEADO no se reconocio como tal"; }
grep -q 'route/gated501.*gated=1' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "la pantalla del 501 gateado no lo conto en su propio cubo"; }
# Control en la otra direccion, el que impide que esto sea un silenciador: el 501 SIN puerta
# sigue apareciendo como peticion fallida.
grep -qE '^ +501 GET /v1/m/nadie-gatea-esto/x' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "el 501 SIN puerta debia seguir contando como hallazgo"; }
# ⛔ Y EL CONTROL QUE DE VERDAD IMPIDE EL SILENCIADOR, escrito en negativo sobre la cadena
# exacta. Sin esta linea, una puerta que se tragara TODOS los 501 pasaba este leg entero:
# medido el 2026-08-13 mutando `esGateado` por `x.status === 501` — el mutante SOBREVIVIO.
if grep -q 'GATEADO 501 .*nadie-gatea-esto' "${TMP}/walk.log"; then
  cat "${TMP}/walk.log" >&2
  fail "el 501 SIN puerta se marco como GATEADO: la puerta se traga cualquier 501"
fi
grep -q 'route/roto501.*gated=0' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "la pantalla del 501 sin puerta no debia contar ningun gateado"; }

# ⛔ EL VECINO: mismo modulo que una puerta declarada, RUTA distinta. La primera version de la
# derivacion gateaba el PREFIJO entero y se lo tragaba; lo encontro el contraste sol max.
if grep -q 'GATEADO 501 .*reporting/otra-cosa' "${TMP}/walk.log"; then
  cat "${TMP}/walk.log" >&2
  fail "un 501 en OTRA ruta del mismo modulo se marco como GATEADO: la puerta cubre el prefijo"
fi
grep -qE '^ +501 GET /v1/m/reporting/otra-cosa' "${TMP}/walk.log" \
  || { cat "${TMP}/walk.log" >&2; fail "el 501 vecino debia seguir contando como hallazgo"; }

# The summary and the exit code must agree with the per-screen marks.
grep -qE 'route/sse' <(sed -n '/screen(s), .* with findings/,$p' "${TMP}/walk.log") \
  && { cat "${TMP}/walk.log" >&2; fail "/sse appears in the findings summary; a working stream must never be a finding"; }

[ "${RC}" = "1" ] \
  || { cat "${TMP}/walk.log" >&2; fail "expected exit 1 (findings present: a stall and a 404), got ${RC}"; }

# ---------------------------------------------------------------------------
# Leg 2 — "could not look" stays its own answer, and says where it looked. This is the
# regression guard for the resolution defect: the failure must never be silent.
# ---------------------------------------------------------------------------
set +e
OLIVARES_WALK_BASE="http://127.0.0.1:${PORT}" \
OLIVARES_WALK_OUT="${TMP}/out2" \
OLIVARES_WALK_PW="${TMP}/there-is-no-playwright-here.mjs" \
  node scripts/console-walk.mjs > "${TMP}/walk2.log" 2>&1
RC2=$?
set -e

[ "${RC2}" = "2" ] || { cat "${TMP}/walk2.log" >&2; fail "an unloadable playwright must exit 2 (could not look), got ${RC2}"; }
grep -q 'there-is-no-playwright-here' "${TMP}/walk2.log" \
  || { cat "${TMP}/walk2.log" >&2; fail "the failure must NAME every location it tried; that is what made the original defect unreadable"; }

# ---------------------------------------------------------------------------
# Leg 3 — A STALL ON ITS OWN MUST TURN THE RUN RED.
#
# This leg exists because leg 1 could not see the defect it was written for. Mutation,
# 2026-08-07: restoring the OLD finding predicate — (pageErr | badReq | conErr), with
# navError and stalled left out, which is the bug — left leg 1 GREEN. Its 404 forced exit 1
# on its own, and the STALLED line is printed per-screen whether or not it counts, so both
# assertions passed while the predicate was broken. The mutant survived.
#
# So here the stall is the ONLY thing wrong: no 404, no page error, nothing else. A walk that
# does not count it must exit 0, and this leg fails. That is the assertion that actually
# holds the fix down.
# ---------------------------------------------------------------------------
DECOY_ROUTES=clean,stall node "${TMP}/decoy.mjs" > "${TMP}/port3" 2>"${TMP}/decoy3.err" &
SRV3_PID=$!
for _ in $(seq 1 50); do [ -s "${TMP}/port3" ] && break; sleep 0.1; done
PORT3="$(cat "${TMP}/port3" 2>/dev/null || true)"
[ -n "${PORT3}" ] || { cat "${TMP}/decoy3.err" >&2; fail "stall-only decoy never reported a port"; }

set +e
OLIVARES_WALK_BASE="http://127.0.0.1:${PORT3}" \
OLIVARES_WALK_OUT="${TMP}/out3" \
OLIVARES_WALK_IDLE_MS=2000 \
OLIVARES_WALK_TOKEN='' \
  node scripts/console-walk.mjs > "${TMP}/walk3.log" 2>&1
RC3=$?
set -e

[ "${RC3}" = "2" ] && { cat "${TMP}/walk3.log" >&2; fail "stall-only leg could not look (exit 2)"; }
[ "${RC3}" = "1" ] \
  || { cat "${TMP}/walk3.log" >&2; fail "a stalled screen is the ONLY defect here and the walk exited ${RC3}. A screen whose request never settles must make the run non-clean on its own — otherwise the walk reports '0 with findings' over a screen that never loaded."; }
grep -q 'route/stall' <(sed -n '/screen(s), .* with findings/,$p' "${TMP}/walk3.log") \
  || { cat "${TMP}/walk3.log" >&2; fail "the stalled screen must appear in the findings summary, not only in its own line"; }

echo "OK: console-walk tells its three answers apart — stream vs stall vs 404, and 'could not look' names where it looked."
