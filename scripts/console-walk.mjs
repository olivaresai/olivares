// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// console-walk.mjs — drive the embedded console with a REAL browser and report what a
// real operator would hit: console errors, uncaught page errors, and requests the engine
// answered 4xx/5xx or that never completed.
//
// WHY THIS EXISTS. `task test:web` runs the console against mocks; `web:check` proves the
// committed bundle matches its sources. Neither opens a browser, so neither can see a
// TYPED API CLIENT CALLING AN ENDPOINT THE ENGINE NEVER REGISTERS — the console asks, the
// engine answers 404, and every unit test stays green because the mock answered. The first
// run of this walk found exactly that, twice, on one screen.
//
// WHAT IT IS NOT. It is not a screenshot test and it does not assert layout. It reports
// three facts per screen and lets a human read them.
//
// THE SSE CAVEAT, because it cost a false finding on the first run. Screens that open a
// server-sent-events stream never reach `networkidle` — the connection is meant to stay
// open. That is a property of the product working, not a defect.
//
// It used to be handled with a hand-written `STREAMING` set, and by 2026-08-07 that set had
// gone stale exactly the way this file warns hand-written lists do: /sessions streams through
// the same hook as /health, was not in the set, and got reported as a 40s navigation timeout
// on a working feature. Screens are now classified by ASKING THE CONNECTION — a pending
// request whose response says `content-type: text/event-stream` is a stream; anything else
// still pending when the clock runs out is a genuine stall and IS a finding. See the note
// above classifyPending() for what was measured.
//
// Usage:
//   OLIVARES_WALK_BASE=https://127.0.0.1:8443 \
//   OLIVARES_WALK_TOKEN=olst_... \
//   node scripts/console-walk.mjs
//
// Env:
//   OLIVARES_WALK_BASE     console origin                     (default https://127.0.0.1:8443)
//   OLIVARES_WALK_TOKEN    one-time setup token; when set, the walk creates the first admin
//   OLIVARES_WALK_EMAIL    admin email to create/use          (default admin@example.invalid)
//   OLIVARES_WALK_PASS     admin password
//   OLIVARES_WALK_OUT      artifact directory                 (default ./console-walk-out)
//   OLIVARES_WALK_CHROMIUM explicit browser executable; otherwise Playwright's own is used
//   OLIVARES_WALK_PW       path to the playwright module when it is not resolvable normally
//
// ⛔ ESTE INSTRUMENTO NO TIENE LLAMANTE, Y SE DECLARA MANUAL EN VEZ DE FINGIRLO (C15-P9).
// Medido el 2026-08-16 sobre `origin/main`: CERO referencias a `console:walk` en `.github/` y en
// `.githooks/`. Es decir, el único instrumento del repositorio que abre un navegador REAL contra
// un motor vivo se ejecuta si alguien se acuerda — que es la misma clase de defecto que este
// fichero denuncia para otras cosas: verde en local y en ningún otro sitio.
//
// LO QUE COSTARÍA CABLEARLO, medido para que la decisión no sea una pregunta abierta:
//   · Su sitio natural es `.github/workflows/drills-nightly.yml`, NO `mainline-ci`: ese job YA
//     arranca el motor (`task smoke:quickstart`), sus pasos no se abortan entre sí a propósito
//     —«un incidente de DR no esconde uno de PSIRT»— y la cadencia nocturna es la que ese mismo
//     fichero eligió por escrito para no gastar un runner en cada push.
//   · Lo que le falta a ese job: `actions/setup-node`, las dependencias de `web/` y el navegador
//     de Playwright. Ningún workflow del repositorio menciona hoy playwright ni chromium, así
//     que el navegador NO está y hay que instalarlo — no es un paso de una línea.
//   · Y las dos variables que este fichero ya documenta arriba: OLIVARES_WALK_BASE contra el
//     motor que el drill dejó en pie, y OLIVARES_WALK_TOKEN en el primer arranque.
//
// ⛔ NO lo cablea esta sesión, y el motivo no es prioridad: `.github/` y `Taskfile.yml` no son
// del carril de producto. La propuesta va al carril de integración con esta medida; mientras no aterrice,
// el walk es manual Y ESTÁ DICHO, que es lo contrario de un gate que nadie corre y todos citan.
//
// FINDING PLAYWRIGHT, because the default could never work and the failure looked like a
// verdict. This script lives at the repo root, whose node_modules holds commitlint and
// nothing else; the browser deps live in web/. `playwright` is only a TRANSITIVE dep of
// @playwright/test, and pnpm does not link transitive deps into web/node_modules — so the
// old bare `import('playwright')` resolved NOWHERE on a correctly installed tree and the
// walk exited 2 ("could not look"). That is the one answer this tool must never give by
// accident: an empty walk reads like a clean one to anyone skimming. We therefore resolve
// against web/'s real dependency graph, preferring the DECLARED direct dependency
// (@playwright/test re-exports chromium) over the undeclared transitive one, and report
// every location we tried when all of them fail.
//
// THE CJS INTEROP, which is why this is not a one-line import. Resolving through
// createRequire yields a FILE PATH, and importing a file path bypasses the package's
// `exports` map, so Node's named-export detection comes back empty: `m.chromium` is
// undefined while `m.default.chromium` is the real object. Reading only the named export
// produced "Cannot read properties of undefined (reading 'launch')" — a browser-launch
// error for what was actually a module-shape problem. Both shapes are accepted below.
//
// Exit codes follow the engine's own convention: 0 clean, 1 defects found, 2 could not look.
// NOTE: `task` collapses every failing exit code to 201, so these three answers only survive
// as themselves when the task is invoked as `task -x console:walk`. Measured 2026-08-07.

import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'

const BASE = process.env.OLIVARES_WALK_BASE || 'https://127.0.0.1:8443'
const TOKEN = process.env.OLIVARES_WALK_TOKEN || ''
const EMAIL = process.env.OLIVARES_WALK_EMAIL || 'admin@example.invalid'
const PASS = process.env.OLIVARES_WALK_PASS || 'Olivares-Local-Walk-2026!x'
const OUT = process.env.OLIVARES_WALK_OUT || 'console-walk-out'
const EXEC = process.env.OLIVARES_WALK_CHROMIUM || ''
// Empty by default ON PURPOSE: a non-empty default would shadow the resolution chain below
// and reinstate the bare `playwright` import that could never resolve from this directory.
const PW = process.env.OLIVARES_WALK_PW || ''

// How long we are willing to wait for the network to go quiet before asking WHY it has not.
// Short on purpose: a streaming screen will never reach idle, so this is the cost we pay per
// streaming screen, not a timeout budget for slow ones.
const IDLE_MS = Number(process.env.OLIVARES_WALK_IDLE_MS || 8000)

// Every place we are willing to look, in order. An explicit OLIVARES_WALK_PW always wins;
// otherwise the DECLARED direct dependency comes before the undeclared transitive one.
const webRequire = createRequire(new URL('../web/package.json', import.meta.url))
const candidates = PW
  ? [{ how: 'OLIVARES_WALK_PW', spec: PW }]
  : [
      { how: 'web/ @playwright/test', spec: '@playwright/test', via: webRequire },
      { how: 'web/ playwright', spec: 'playwright', via: webRequire },
      { how: 'bare playwright', spec: 'playwright' },
    ]

let chromium
const tried = []
for (const c of candidates) {
  let target = c.spec
  try {
    // A resolver hands back a filesystem path; import() needs a file: URL for it.
    if (c.via) target = pathToFileURL(c.via.resolve(c.spec)).href
    else if (c.spec.startsWith('/') || c.spec.startsWith('.')) target = pathToFileURL(c.spec).href
    const m = await import(target)
    // Named first, then default: importing a resolved PATH skips the exports map, and the
    // named export is then undefined even though the module loaded perfectly well.
    const got = m.chromium ?? m.default?.chromium
    if (!got) throw new Error('module loaded but exports no `chromium` (named or default)')
    chromium = got
    console.log(`console-walk: playwright loaded via ${c.how} (${target})`)
    break
  } catch (e) {
    tried.push(`  ${c.how}: ${target} -> ${e && e.message}`)
  }
}
if (!chromium) {
  console.error('console-walk: cannot load playwright — nothing was measured, which is NOT a clean run.')
  for (const t of tried) console.error(t)
  console.error('  fix: `pnpm --dir web install`, or set OLIVARES_WALK_PW to the module entry point.')
  process.exit(2)
}

// ⛔ UN 501 NO ES UN 404, Y ESTE WALK LOS CONTABA IGUAL. Medido el 2026-08-13 sobre `main`:
// 55 pantallas, un solo «hallazgo», y era `GET /v1/m/reporting/schedules -> 501` — la ruta
// EXISTE (modules/reporting/enterprise.go:94) y la consola la GATEA a propósito
// (features/reporting/api.ts:66 `isEnterprisePending = status === 501`, y la vista pinta un
// aviso honesto). Es decir: comportamiento correcto reportado como defecto. Un instrumento que
// no distingue «gateado» de «roto» gasta una tarde de alguien cada vez que acierta.
//
// La lista NO se escribe a mano —envejecería el día que una feature gane o pierda su puerta—:
// se DERIVA del propio repositorio. Un fichero `web/src/features/*/api.ts` que comprueba
// `status === 501` está declarando que sabe convivir con esa respuesta; su `const BASE` dice
// sobre qué prefijo. Lo que no se pueda derivar se NOMBRA, en vez de darse por bueno.
function derivarPuertas501() {
  // ⛔ ENDPOINTS, NO PREFIJOS. La primera versión derivaba el `const BASE` y gateaba TODO el
  //    módulo: un 501 inesperado en `/v1/m/reporting/<cualquier-otra-cosa>` desaparecía de
  //    `badReq` Y del error de consola. Lo cazó el contraste `sol max` el 2026-08-14, y es el
  //    mismo defecto que yo había buscado con el mutante «se traga cualquier 501» — pero un
  //    escalón más abajo: «se traga cualquier 501 DEL PREFIJO». Una puerta que cubre más de lo
  //    que su feature declara es un silenciador con mejor educación.
  //    Ahora se extraen las RUTAS concretas que ese api.ts construye sobre su BASE
  //    (`${BASE}/schedules` → `/v1/m/reporting/schedules`), y sólo ésas se gatean.
  const rutas = []
  const sinBase = []
  const sinRutas = []
  const raiz = 'web/src/features'
  let dirs = []
  try {
    dirs = readdirSync(raiz, { withFileTypes: true }).filter((d) => d.isDirectory())
  } catch {
    return { rutas, sinBase, sinRutas, leido: false }
  }
  for (const d of dirs) {
    const f = `${raiz}/${d.name}/api.ts`
    let src
    try {
      src = readFileSync(f, 'utf8')
    } catch {
      continue
    }
    if (!/status\s*===\s*501/.test(src)) continue
    // ⛔ CUALQUIER constante de ruta literal, no una llamada `BASE`. La primera versión buscaba
    //    `const BASE = '…'` y sólo eso, así que `features/identity/api.ts` —que llama a las suyas
    //    `SCIM` e `IDENTITY`, y son igual de literales— caía en `sinBase` y **sus 501 legítimos
    //    seguían contando como hallazgo**. Medido el 2026-08-18: el walk daba rc=1 por dos
    //    `501 /v1/auth/piv/status`, que son la frontera «PIV no configurado» y no un defecto.
    //
    //    El contrato de esta derivación es «una constante de ruta LITERAL», no «una constante que
    //    se llame BASE». Pedir un nombre concreto obliga a renombrar el código de producto para
    //    contentar a un script, que es la cola meneando al perro.
    const consts = new Map()
    for (const c of src.matchAll(/const\s+([A-Z][A-Z0-9_]*)\s*=\s*'(\/v1\/[^']*)'/g)) {
      consts.set(c[1], c[2])
    }
    if (consts.size === 0) {
      sinBase.push(f)
      continue
    }
    // Segmentos LITERALES colgando de cada constante. Se corta en la primera interpolación: de
    // `${BASE}/schedules/${id}/runs` sólo se toma `/schedules`, que es lo que se puede
    // afirmar sin resolver variables.
    const encontradas = new Set()
    for (const [nombre, base] of consts) {
      const re = new RegExp('\\$\\{' + nombre + '\\}((?:\\/[A-Za-z0-9._-]+)+)', 'g')
      for (const t of src.matchAll(re)) {
        const seg = t[1].split('/').filter(Boolean)[0]
        if (seg) encontradas.add(`${base}/${seg}`)
      }
    }
    if (encontradas.size === 0) sinRutas.push(f)
    else for (const r of encontradas) rutas.push(r)
  }
  return { rutas, sinBase, sinRutas, leido: true }
}
const PUERTAS = derivarPuertas501()
const esGateado = (x) =>
  x.status === 501 &&
  PUERTAS.rutas.some((r) => {
    try {
      const ruta = new URL(x.url).pathname
      // Exacta, o un hijo de ESA ruta — nunca un hermano bajo el mismo módulo.
      return ruta === r || ruta.startsWith(r + '/')
    } catch {
      return false
    }
  })

mkdirSync(OUT, { recursive: true })
const all = { console: [], pageErrors: [], requests: [] }
const findings = []

const launchOpts = { args: ['--no-sandbox', '--disable-dev-shm-usage', '--ignore-certificate-errors'] }
if (EXEC) launchOpts.executablePath = EXEC

let browser
try {
  browser = await chromium.launch(launchOpts)
} catch (e) {
  console.error('console-walk: no browser could be launched — nothing was measured.')
  console.error(`  ${e && e.message}`)
  console.error('  install one (`npx playwright install chromium`) or set OLIVARES_WALK_CHROMIUM.')
  process.exit(2)
}

const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1600, height: 1000 } })
const page = await ctx.newPage()
// Requests that have not settled. A held-open stream lives here for the life of the screen,
// which is what lets us tell "designed to stay open" from "stuck" by evidence instead of by a list.
const inflight = new Set()
page.on('request', (r) => inflight.add(r))
// ⛔ `location` SE CAPTURA, y no es adorno: el texto del navegador para un recurso fallido es
// «Failed to load resource: the server responded with a status of 501 ()» — NO nombra la URL.
// Sin la localización, descontar el ruido de un 501 gateado obliga a descontar TODOS los 501
// de la pantalla, que es un silenciador. Medido el 2026-08-13 sobre el artefacto del walk.
page.on('console', (m) =>
  all.console.push({ type: m.type(), text: m.text(), url: m.location()?.url ?? null }),
)
page.on('pageerror', (e) => all.pageErrors.push({ message: String(e && e.message) }))
page.on('requestfinished', (r) => inflight.delete(r))
page.on('requestfailed', (r) => inflight.delete(r))
page.on('requestfinished', async (r) => {
  try {
    const s = await r.response()
    all.requests.push({ url: r.url(), method: r.method(), status: s ? s.status() : null })
  } catch {
    /* the request died with the navigation; requestfailed covers the real failures */
  }
})
page.on('requestfailed', (r) =>
  all.requests.push({ url: r.url(), method: r.method(), status: 'FAILED', failure: r.failure()?.errorText }),
)

// WHY A SCREEN NEVER WENT QUIET, decided by the wire rather than by a hand-written list.
//
// The old code kept `const STREAMING = new Set(['/health', '/logs'])` and waited for
// `networkidle` on everything else. That is the very "hand-written list that goes stale the
// day a screen is added" this tool refuses to use for ROUTE discovery, and it had already
// gone stale: /sessions holds `/v1/m/sessions/stream` open through the same useLiveStream
// hook as /health, was absent from the set, and so burned 40s and reported
// `nav=Timeout 40000ms exceeded` on a feature that was working perfectly.
//
// So ask the connection what it is. Measured 2026-08-07 against the running engine: the
// pending request reports resourceType `fetch` — NOT `eventsource`, because the console
// streams over fetch rather than the native EventSource — while its RESPONSE HEADERS are
// already available and say `content-type: text/event-stream` with status 200. Headers
// arrive before the body finishes, which is exactly what makes this checkable on a
// connection that never finishes. Verified on all three streaming screens.
//
// Anything else still pending when the clock runs out is a screen that genuinely did not
// settle, and that is a finding.
// WHAT COUNTS AS A FINDING — one definition, used by the per-screen mark, the summary and the
// exit code, so those three can never disagree.
//
// `navError` and `stalled` are in here because they were NOT before, and that was a hole with
// teeth: the old predicate read only (pageErr | badReq | conErr), so a screen whose navigation
// TIMED OUT was printed as `nav=Timeout 40000ms exceeded` and then counted as clean, leaving
// the exit code at 0. A walk could report "53 screens, 0 with findings" over a screen that
// never loaded. An empty walk is never a clean walk, and neither is a stuck one.
const isBad = (f) => !!f.navError || f.stalled.length > 0 || f.pageErr.length > 0 || f.badReq.length > 0 || f.conErr.length > 0

async function classifyPending() {
  const pend = []
  for (const r of inflight) {
    let ct = null
    let status = null
    try {
      // r.response() resolves on HEADERS, but a request that never gets any would hang the
      // walk, so never wait on it indefinitely.
      const resp = await Promise.race([r.response(), new Promise((res) => setTimeout(() => res(null), 2000))])
      if (resp) {
        ct = resp.headers()['content-type'] ?? null
        status = resp.status()
      }
    } catch {
      /* the request died with the navigation; it is no longer pending in any meaningful sense */
    }
    pend.push({ url: r.url(), method: r.method(), status, ct, streaming: !!ct && ct.includes('text/event-stream') })
  }
  return pend
}

async function step(name, fn) {
  const b = { c: all.console.length, e: all.pageErrors.length, r: all.requests.length }
  let navError = null
  try {
    await fn()
  } catch (e) {
    navError = String(e && e.message).slice(0, 300)
  }
  // Always navigate to domcontentloaded (above), then ask separately whether the network
  // settled. Splitting the two is what turns "timed out" from a verdict into a question.
  let idle = true
  try {
    await page.waitForLoadState('networkidle', { timeout: IDLE_MS })
  } catch {
    idle = false
  }
  const pending = idle ? [] : await classifyPending()
  const stalled = pending.filter((p) => !p.streaming)
  const streaming = pending.length > 0 && stalled.length === 0
  await page.waitForTimeout(streaming ? 2500 : 1000)
  const conErr = all.console.slice(b.c).filter((x) => x.type === 'error')
  const pageErr = all.pageErrors.slice(b.e)
  const req4xx = all.requests
    .slice(b.r)
    .filter((x) => x.status === 'FAILED' || (typeof x.status === 'number' && x.status >= 400))
  // El 501 gateado sale de `badReq` y va a su propio cubo: se REPORTA, pero no es un defecto.
  const gated = req4xx.filter(esGateado)
  const badReq = req4xx.filter((x) => !esGateado(x))
  // …y con él se va el error de consola que produce ÉL MISMO. No es un `console.error` de la
  // aplicación: es el navegador diciendo «Failed to load resource: … 501», que ninguna consola
  // puede silenciar. Dejarlo contaba dos veces el mismo hecho y mantenía la pantalla en rojo.
  // ⛔ POR URL, no «si hay algún gateado». La primera versión de esta línea descontaba
  // CUALQUIER error de consola con un 501 en cuanto la pantalla tuviera un gateado, así que
  // un 501 roto en la MISMA pantalla se quedaba sin su ruido y podía pasar por limpio. Un
  // silenciador con buena intención sigue siendo un silenciador.
  const rutasGateadas = gated.map((x) => {
    try {
      return new URL(x.url).pathname
    } catch {
      return x.url
    }
  })
  const conErrReal = conErr.filter((x) => {
    const t = x.text || ''
    if (!/Failed to load resource/i.test(t) || !/\b501\b/.test(t)) return true
    // Se descuenta por la LOCALIZACIÓN del mensaje, que es lo único que identifica el recurso.
    // Si el navegador no la diera, el error se CONSERVA: preferimos un hallazgo de más a un
    // silenciador (y el self-test lo prueba con el mutante «silenciador»).
    if (!x.url) return true
    let ruta = x.url
    try {
      ruta = new URL(x.url).pathname
    } catch {
      /* se queda la url tal cual */
    }
    return !rutasGateadas.includes(ruta)
  })
  const s = { name, url: page.url(), streaming, navError, conErr: conErrReal, pageErr, badReq, gated, stalled }
  findings.push(s)
  const mark = isBad(s) ? ' <-- REVIEW' : ''
  console.log(
    `[${name}]${streaming ? ' (streaming)' : ''} ${s.url} nav=${navError ?? 'ok'} gated=${gated.length} conErr=${conErrReal.length} pageErr=${pageErr.length} badReq=${badReq.length} stalled=${stalled.length}${mark}`,
  )
  for (const x of gated.slice(0, 8)) {
    console.log(`      GATEADO ${x.status} ${x.method} ${x.url.replace(BASE, '')} (la consola declara esta puerta)`)
  }
  for (const x of badReq.slice(0, 8)) {
    console.log(`      ${x.status} ${x.method} ${x.url.replace(BASE, '')}${x.failure ? ' :: ' + x.failure : ''}`)
  }
  for (const x of stalled.slice(0, 8)) {
    console.log(`      STALLED ${x.method} ${x.url.replace(BASE, '')} status=${x.status ?? 'none'} ct=${x.ct ?? 'none'}`)
  }
  for (const x of pageErr.slice(0, 4)) console.log(`      PAGEERROR: ${x.message.slice(0, 220)}`)
  return s
}

await step('root', () => page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 45000 }))

if (TOKEN && (await page.locator('#token').count())) {
  await step('first-admin', async () => {
    await page.fill('#token', TOKEN)
    await page.fill('#setup-email', EMAIL)
    await page.fill('#setup-password', PASS)
    await page.click('button:has-text("Create administrator")')
    await page.waitForLoadState('networkidle', { timeout: 30000 }).catch(() => {})
  })
}
if ((await page.locator('input[type=password]').count()) > 0) {
  await step('login', async () => {
    const em = page.locator('input[type=email]').first()
    if (await em.count()) await em.fill(EMAIL)
    await page.locator('input[type=password]').first().fill(PASS)
    const btn = page.locator('button[type=submit], button:has-text("Sign in"), button:has-text("Log in")').first()
    if (await btn.count()) await btn.click()
    await page.waitForLoadState('networkidle', { timeout: 30000 }).catch(() => {})
  })
}

// AUTHENTICATION IS ASSERTED, NOT ASSUMED — the one thing between this walk and a green that
// means nothing. Measured 2026-08-08: `task console:walk` answered
// rc=0, "4 screen(s), 0 with findings", having walked ONE route of fifty-three.
//
// THE MECHANISM, because the zero-route guard below looks like it already covers this and does
// not. `POST /v1/setup` hashes with argon2id m=64MiB,t=3,p=1 (core/auth/credential.go:112-118)
// — 162-175 ms idle, 1880 ms under contention on this three-container host. When the
// first-admin wait times out, its `.catch(() => {})` swallows it and setup is left HALF DONE;
// the login block above then asks for a password field, finds none because the page is still
// /setup, and SKIPS ITSELF in silence. Discovery below therefore runs against the SIGNED-OUT
// SHELL — which still renders a link or two, so `nav.length` is 1, not 0, and the guard passes.
//
// A route-count floor was the other candidate and is worse: a number that goes stale the day a
// screen is added or a community build ships fewer. The precondition itself is the test.
//
// THE INVERSION THIS CLOSES: in this build a SANE walk exits 1 — the honest 501 from
// /v1/m/reporting/schedules — and the blind one exited 0. The exit code was useless in both
// directions, and of the ten instruments on that list this was the only one that REWARDED the
// failure with a green. Exit 2 here, never 0 and never 1: the console was not found clean, it
// was never reached.
const stillOut = []
if (await page.locator('#token').count()) stillOut.push('the setup token field (#token)')
if (await page.locator('input[type=password]').count()) stillOut.push('a password box')
if (stillOut.length) {
  console.error('console-walk: still UNAUTHENTICATED after the sign-in steps — nothing was measured.')
  console.error(`  the page at ${page.url()} still shows ${stillOut.join(' and ')}.`)
  console.error('  Anything discovered from here is the signed-out shell, not the console. Likely')
  console.error('  causes: a wrong or spent OLIVARES_WALK_TOKEN, or the setup POST timing out under')
  console.error('  load — argon2id costs 64 MiB and one thread, measured at 1880 ms on this host')
  console.error('  under contention. Re-run when the box is quiet, or raise the step timeout.')
  await browser.close()
  process.exit(2)
}

// The route list is DISCOVERED from the app's own navigation, never typed here: a hand
// written list goes stale the day a screen is added, and reports full coverage while
// missing it.
const nav = await page.evaluate(() => {
  const seen = new Set()
  const out = []
  for (const a of document.querySelectorAll('a[href^="/"]')) {
    const h = a.getAttribute('href')
    if (!h || h.startsWith('//') || seen.has(h)) continue
    seen.add(h)
    out.push(h)
  }
  return out
})
if (nav.length === 0) {
  console.error('console-walk: discovered ZERO routes — the walk measured nothing, which is not a clean run.')
  await browser.close()
  process.exit(2)
}
console.log(`\nconsole-walk: ${nav.length} route(s) discovered from the app's own navigation\n`)

for (const href of nav) {
  await step(`route${href}`, () => page.goto(BASE + href, { waitUntil: 'domcontentloaded', timeout: 40000 }))
  await page.screenshot({ path: `${OUT}/route-${href.replace(/[^a-z0-9]+/gi, '_')}.png`, fullPage: true }).catch(() => {})
}

writeFileSync(`${OUT}/report.json`, JSON.stringify({ base: BASE, findings, all }, null, 2))
const bad = findings.filter(isBad)
const gatedTotal = findings.reduce((n, f) => n + (f.gated?.length ?? 0), 0)
console.log(`\nconsole-walk: ${findings.length} screen(s), ${bad.length} with findings. Artifacts in ${OUT}`)
// Lo gateado se DICE siempre, incluso cuando el walk sale limpio: un 501 silenciado sin
// mencionarlo sería la misma clase de mentira que contarlo como defecto, sólo que en la
// dirección cómoda. Y si la derivación no pudo leer el árbol, o hay puertas cuya base no se
// pudo derivar, se nombra aquí — el walk no presume de saber lo que no ha podido mirar.
if (gatedTotal > 0 || PUERTAS.sinBase.length > 0 || PUERTAS.sinRutas.length > 0 || !PUERTAS.leido) {
  console.log(
    `console-walk: ${gatedTotal} respuesta(s) 501 en rutas que la consola DECLARA gatear ` +
      `(${PUERTAS.rutas.length} RUTA(s) derivadas de web/src/features/*/api.ts) — no son hallazgos.`,
  )
  if (!PUERTAS.leido)
    console.log(
      '  ⚠ NO he podido leer web/src/features: cualquier 501 se ha contado como HALLAZGO, que es el lado seguro.',
    )
  for (const f of PUERTAS.sinBase)
    console.log(`  ⚠ ${f} comprueba 501 pero no declara un BASE literal: sus 501 siguen contando como hallazgo.`)
  for (const f of PUERTAS.sinRutas)
    console.log(`  ⚠ ${f} declara BASE pero no construye rutas literales sobre él: NO se gatea su módulo.`)
}
for (const f of bad) {
  const why = [
    ...f.badReq.map((r) => `${r.status} ${r.url.replace(BASE, '')}`),
    ...f.stalled.map((r) => `STALLED ${r.url.replace(BASE, '')}`),
    ...(f.navError ? [`NAV ${f.navError.split('\n')[0]}`] : []),
    ...(f.pageErr.length ? [`${f.pageErr.length} page error(s)`] : []),
    ...(f.conErr.length && !f.badReq.length ? [`${f.conErr.length} console error(s)`] : []),
  ]
  console.log(`  ${f.name}: ${why.join(', ') || '-'}`)
}
await browser.close()
process.exit(bad.length ? 1 : 0)
