// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createHash, randomBytes } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { expect, test } from '@playwright/test'
import {
  encabezadoDeDialogo,
  encabezadoDelDialogoAbierto,
  encabezadoDePagina,
  esperarEncabezado,
  exigirInstanteLimpio,
  exigirMarcadores,
} from './capture-target'
import { esperarExportacionDePostura } from './posture-export-terminal'
import { EXTENSION_ROUTES } from '../src/features/extensions'

// Capture the running console in English, with seeded data and declared response
// fixtures, in both themes. The runner supplies the engine and tenant; PNGs and
// per-image evidence go under playwright-report/docs.

const demoTenant = process.env.DEMO_TENANT ?? ''

// Index English locale keys with and without namespaces to identify untranslated text.
const CLAVES_I18N: string[] = (() => {
  const salida: string[] = []
  const recorre = (o: unknown, pre: string[]) => {
    if (o && typeof o === 'object' && !Array.isArray(o)) {
      for (const [k, v] of Object.entries(o as Record<string, unknown>)) {
        recorre(v, [...pre, k])
      }
    } else {
      salida.push(pre.join('.'))
    }
  }
  const dirs = [
    ...(fs.existsSync('src/features')
      ? fs
          .readdirSync('src/features')
          .map((f) => `src/features/${f}/i18n/en.json`)
      : []),
    ...(fs.existsSync('src/lib/i18n/locales/en')
      ? fs
          .readdirSync('src/lib/i18n/locales/en')
          .map((f) => `src/lib/i18n/locales/en/${f}`)
      : []),
  ]
  for (const f of dirs) {
    if (!fs.existsSync(f) || !f.endsWith('.json')) continue
    try {
      const d = JSON.parse(fs.readFileSync(f, 'utf8'))
      const ns = f.includes('/i18n/')
        ? f.split('/')[2]
        : path.basename(f, '.json')
      recorre(d, [])
      recorre(d, [ns])
    } catch {
      // Warn when a locale cannot be read: the resulting key index is incomplete.
      console.warn(
        `[i18n] no pude leer ${f} — la sonda de claves crudas mide de menos`,
      )
    }
  }
  return salida
})()
const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// The exact Infrastructure link in the Primary sidebar identifies the global account
// shell without requiring a collapsed child link.
const navReady = (page: import('@playwright/test').Page) =>
  page
    .getByRole('complementary', { name: 'Primary' })
    .getByRole('link', { name: 'Infrastructure', exact: true })

// One entry per view the docs reference. `settle` gives slow views (graph
// layout, charts) extra time after networkidle before the shot. `live` views
// poll continuously, so networkidle never fires — they settle on a timer only.
// Each heading identifies the requested view, including redirects. A title change
// requires reviewing the corresponding capture.
// Prepare workspace selection through the interface after login; tenant changes can
// clear a preloaded selection.
// Wait for loading before counting data rows; skeleton rows are also table rows.
async function esperaTablasCargadas(page: import('@playwright/test').Page) {
  // DataTable may expose a grid role, so find its table element. Allow table-free
  // views, then wait for aria-busy to clear. Views without tables must wait for their
  // own loaded element.
  await page
    .locator('table')
    .first()
    .waitFor({ timeout: 5_000 })
    .catch(() => {})
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0, {
    timeout: 20_000,
  })
}

// Choose the communications workspace and wait for its read to settle before capturing.
async function eligeWorkspaceK3YEspera(page: import('@playwright/test').Page) {
  await eligeWorkspaceConcretoK3(page)
  await esperaTablasCargadas(page)
}

// Communications requires a concrete workspace. Treat an absent switcher or choice as
// missing seed data.
async function eligeWorkspaceConcretoK3(page: import('@playwright/test').Page) {
  const conmutador = page.getByRole('button', { name: 'All workspaces' })
  try {
    await conmutador.waitFor({ timeout: 8_000 })
    await conmutador.click()
  } catch {
    throw new Error(
      'docs-captures: no se pudo abrir el conmutador de workspaces para /communications*: el ' +
        'producto solo lo monta con MAS DE UNO (workspace-switcher.tsx:44). Es SEMBRADO, no un ' +
        'selector roto.',
    )
  }
  const alguno = page
    .getByRole('menuitem')
    .filter({ hasNotText: 'All workspaces' })
    .first()
  try {
    await alguno.waitFor({ timeout: 8_000 })
  } catch {
    throw new Error(
      'docs-captures: el conmutador no ofrece ningun workspace concreto, asi que /communications* ' +
        'solo puede enseñar «Select a workspace». Es SEMBRADO, no un selector roto.',
    )
  }
  await alguno.click()
}

// Provision the required tenant role through users and memberships, then sign in
// normally. Handoffs uses editor and administration uses admin. Generate a password per
// run and keep it out of captures, evidence and logs.
async function provisionaMiembroK3(
  page: import('@playwright/test').Page,
  rol: 'editor' | 'admin',
) {
  const api = page.request
  const entrada = await api.post('/v1/auth/login', {
    data: { email: DEMO_EMAIL, password: DEMO_PASSWORD },
  })
  expect(
    entrada.status(),
    'docs-captures: el superadmin demo no pudo entrar para provisionar el actor de handoffs',
  ).toBe(200)
  const admin = ((await entrada.json()) as { token: string }).token
  const cabeceras = {
    Authorization: `Bearer ${admin}`,
    'X-Olivares-Tenant': demoTenant,
    'Content-Type': 'application/json',
  }
  const email = `docs-capture-k3-${rol}-${randomBytes(6).toString('hex')}@olivares.local`
  const clave = randomBytes(24).toString('base64url')
  const creado = await api.post('/v1/users', {
    headers: cabeceras,
    data: JSON.stringify({ email, password: clave }),
  })
  expect(
    creado.status(),
    'docs-captures: POST /v1/users del actor editor',
  ).toBe(201)
  const usuario = ((await creado.json()) as { id: string }).id
  const alta = await api.post('/v1/memberships', {
    headers: cabeceras,
    data: JSON.stringify({
      user_id: usuario,
      tenant: demoTenant,
      role: rol,
    }),
  })
  expect(
    alta.status(),
    'docs-captures: POST /v1/memberships del actor editor',
  ).toBe(201)
  return {
    email,
    clave,
    procedencia: {
      kind: 'tenant-member',
      role: rol,
      tenant: demoTenant,
      user_id: usuario,
      basis:
        'provisioned through POST /v1/users and POST /v1/memberships with the demo superadmin ' +
        'login, then signed in through the normal login form',
    },
  }
}

const VIEWS: {
  id: string
  path: string
  settle?: number
  live?: boolean
  heading?: RegExp
  prepara?: (page: import('@playwright/test').Page) => Promise<void>
  // The post-navigation hook may return a data witness for the capture.
  // check-capture-view-keys.py validates hook names because the app tsconfig excludes
  // e2e files.
  despues?: (
    page: import('@playwright/test').Page,
  ) => Promise<void | Record<string, unknown>>
  // Use a provisioned tenant member when scoped access excludes the demo global
  // account.
  actor?: 'k3-tenant-editor' | 'k3-tenant-admin'
  // Modal captures temporarily close the dialog to change the theme.
  modal?: boolean
  // A capture can name a target surface instead of the page heading.
  objetivo?: (
    page: import('@playwright/test').Page,
  ) => import('@playwright/test').Locator
  // Crop the fixed-size viewport and record the crop rectangle.
  recorte?: (
    page: import('@playwright/test').Page,
  ) => Promise<{ x: number; y: number; width: number; height: number }>
}[] = [
  ...EXTENSION_ROUTES.map(({ id, path, heading }) => ({
    id,
    path,
    heading: new RegExp(`^${heading.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`),
  })),
  // Expose the requested dialog, tab or menu state. Install response interceptions
  // before navigation.
  {
    id: 'work-decisions',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    live: true,
    // Open Decisions after the view mounts; it requires sessions:decision:read.
    despues: async (page) => {
      await page.getByRole('tab', { name: /^decisions$/i }).click()
    },
  },
  {
    id: 'work-decisions-paginated',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    live: true,
    // Load another page to show that earlier decisions remain in the list.
    despues: async (page) => {
      await page.getByRole('tab', { name: /^decisions$/i }).click()
      // Wait for loading before checking pagination.
      await esperaTablasCargadas(page)
      // Wait for Load more itself; an immediate count cannot distinguish slow loading
      // from absent seed data.
      const mas = page.getByRole('button', { name: /^load more$/i }).first()
      try {
        await mas.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: no hay boton «Load more» en las decisiones de /work tras esperarlo ' +
            '8 s: el estate no tiene una segunda pagina. Es SEMBRADO (B14), no un selector roto.',
        )
      }
      await mas.click()
    },
  },
  {
    id: 'work-apply-refused',
    path: '/work',
    heading: /^Work$/,
    settle: 800,
    live: true,
    // Exercise the engine error envelope, including error.message, with a deterministic
    // refusal.
    prepara: async (page) => {
      await page.route('**/v1/m/sessions/work-items/**', async (route) => {
        if (route.request().method() !== 'POST') return route.fallback()
        await route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: JSON.stringify({
            verdict: 'RECHAZADO',
            code: 'lease_held_elsewhere',
            error: {
              message:
                'the lease is held by session sesion-7 until 2026-08-30T09:00:00Z; take it over or wait',
            },
          }),
        })
      })
    },
  },
  {
    id: 'templates-readonly',
    path: '/workspace-templates',
    heading: /^Workspace Templates$/,
    settle: 800,
    // Remove only template write/admin permissions so read access remains.
    prepara: async (page) => {
      await page.route('**/v1/auth/whoami', async (route) => {
        const res = await route.fetch()
        const cuerpo = await res.json()
        const fuera = new Set([
          'sessions:template:write',
          'sessions:template:admin',
        ])
        if (Array.isArray(cuerpo?.permissions)) {
          cuerpo.permissions = cuerpo.permissions.filter(
            (p: string) => !fuera.has(p),
          )
        }
        await route.fulfill({ response: res, json: cuerpo })
      })
    },
  },
  {
    id: 'workflow-runs-error',
    path: '/automations',
    heading: /^Automations$/,
    settle: 800,
    // A history request failure must show a retryable error rather than an empty
    // history.
    prepara: async (page) => {
      await page.route('**/v1/m/orchestration/**/runs**', async (route) => {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: { message: 'upstream unavailable' } }),
        })
      })
    },
  },
  {
    id: 'list-truncated',
    path: '/console',
    heading: /^Administration$/,
    settle: 800,
    // Exercise the partial-list notice with has_more in the response.
    prepara: async (page) => {
      await page.route('**/v1/members**', async (route) => {
        const res = await route.fetch()
        const cuerpo = await res.json()
        cuerpo.has_more = true
        await route.fulfill({ response: res, json: cuerpo })
      })
    },
  },
  {
    id: 'access-map',
    path: '/access-map',
    settle: 1500,
    heading: /^Access map$/,
  },
  { id: 'inventory', path: '/inventory', heading: /^Inventory$/ },
  {
    id: 'communications-protocol-bindings',
    path: '/communications/protocol-bindings',
    heading: /^Protocol bindings$/,
    // Choose a concrete workspace after the view mounts; All workspaces cannot supply
    // this scope.
    despues: async (page) => {
      // The switcher requires multiple workspaces; its absence is a seed-data
      // precondition.
      const conmutador = page.getByRole('button', { name: 'All workspaces' })
      // Keep both waiting and clicking within the same error boundary.
      try {
        await conmutador.waitFor({ timeout: 8_000 })
        await conmutador.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo abrir el conmutador de workspaces: el producto solo lo monta ' +
            'con MAS DE UNO (workspace-switcher.tsx:44). Con cero o uno no existe, y esta vista ' +
            'no puede elegir ambito. Es SEMBRADO, no un selector roto.',
        )
      }
      // Choose a concrete workspace by excluding All workspaces, rather than by
      // position.
      const alguno = page
        .getByRole('menuitem')
        .filter({ hasNotText: 'All workspaces' })
        .first()
      try {
        await alguno.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: el conmutador no ofrece ningun workspace concreto, asi que ' +
            '/communications/protocol-bindings solo puede enseñar «Select a workspace». Es ' +
            'SEMBRADO, no un selector roto.',
        )
      }
      await alguno.click()
    },
  },
  // Require each communications route to show its own heading rather than login or an
  // authorization error.
  {
    id: 'communications',
    path: '/communications',
    heading: /^Communications$/,
    despues: eligeWorkspaceK3YEspera,
  },
  {
    id: 'communications-inbox',
    path: '/communications/inbox',
    heading: /^Communications inbox$/,
    despues: eligeWorkspaceK3YEspera,
  },
  {
    id: 'communications-new',
    path: '/communications/new',
    heading: /^New channel$/,
    despues: eligeWorkspaceConcretoK3,
  },
  // Choose a workspace and require the Handoffs tab to be selected.
  {
    id: 'communications-handoffs',
    path: '/communications/handoffs',
    heading: /^Handoffs$/,
    actor: 'k3-tenant-editor',
    despues: async (page) => {
      // Arm the first collection-read witness before choosing a workspace. Missing,
      // denied or unavailable evidence cannot qualify a capture.
      const lectura = page.waitForResponse(
        (r) =>
          r.request().method() === 'GET' &&
          new URL(r.url()).pathname === '/v1/m/sessions/inbox/handoffs',
        { timeout: 20_000 },
      )
      await eligeWorkspaceConcretoK3(page)
      const respuesta = await lectura.catch(() => {
        throw new Error(
          'docs-captures: /communications/handoffs no pidio su coleccion tras elegir workspace; ' +
            'sin lectura no hay testigo y sin testigo no se fotografia.',
        )
      })
      const pedido = new URL(respuesta.url())
      const workspaceId = pedido.searchParams.get('workspace_id') ?? ''
      if (respuesta.status() !== 200) {
        throw new Error(
          `docs-captures: GET /v1/m/sessions/inbox/handoffs contesto ${respuesta.status()} a este ` +
            `actor (workspace ${workspaceId}). Ni un 503 ni un 403 son permiso ni lista vacia.`,
        )
      }
      const cuerpo = (await respuesta.json()) as {
        items?: unknown
        has_more?: unknown
      }
      if (
        !Array.isArray(cuerpo.items) ||
        typeof cuerpo.has_more !== 'boolean'
      ) {
        throw new Error(
          'docs-captures: la coleccion de handoffs contesto 200 sin la forma {items, has_more}.',
        )
      }
      // Resolve the visible workspace name with the collection request principal and
      // headers.
      const propias = await respuesta.request().allHeaders()
      const espacios = await page.request.get('/v1/workspaces', {
        headers: {
          authorization: propias['authorization'] ?? '',
          'x-olivares-tenant': propias['x-olivares-tenant'] ?? '',
        },
      })
      expect(
        espacios.status(),
        'docs-captures: /v1/workspaces con el mismo actor, para nombrar el workspace leido',
      ).toBe(200)
      const lista = (await espacios.json()) as
        | { id: string; name: string }[]
        | { items?: { id: string; name: string }[] }
      const todos = Array.isArray(lista) ? lista : (lista.items ?? [])
      const nombre = todos.find((w) => w.id === workspaceId)?.name ?? ''
      if (!nombre) {
        throw new Error(
          `docs-captures: el workspace ${workspaceId} de la lectura no existe para este actor.`,
        )
      }
      await expect(
        page.locator('main').getByText(nombre, { exact: true }).first(),
        `docs-captures: la puerta no enseña el workspace ${nombre} para el que leyo su coleccion`,
      ).toBeVisible()
      const testigo = {
        read: 'GET /v1/m/sessions/inbox/handoffs',
        status: 200,
        workspace_id: workspaceId,
        workspace: nombre,
        state: pedido.searchParams.get('state'),
        limit: pedido.searchParams.get('limit'),
        items: cuerpo.items.length,
        has_more: cuerpo.has_more,
      }
      const pestanna = page.getByRole('tab', { name: 'Handoffs' })
      try {
        await pestanna.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: /communications/handoffs no monta la pestaña «Handoffs». Se ofrece ' +
            'bajo sessions:delivery:read, asi que esto es SEMBRADO (el actor no tiene el permiso) ' +
            'o la entrada del registro dejo de abrirla.',
        )
      }
      const seleccionada = await pestanna.getAttribute('aria-selected')
      if (seleccionada !== 'true') {
        throw new Error(
          'docs-captures: la puerta /communications/handoffs abrio otra pestaña ' +
            `(aria-selected=${seleccionada}). La captura enseñaria otra pantalla con este id.`,
        )
      }
      // The selected tab must finish loading; its heading alone does not prove a
      // successful collection read.
      try {
        await esperaTablasCargadas(page)
      } catch {
        throw new Error(
          'docs-captures: /communications/handoffs no termino de cargar (la tabla sigue aria-busy). ' +
            'Si el motor contesta 503 a /v1/m/sessions/inbox/handoffs, K3 no esta activo y la ' +
            'foto seria el esqueleto de la tabla.',
        )
      }
      // Role locators skip hidden alerts and avoid selector strings that Tailwind might
      // treat as classes.
      const errores = await page
        .locator('[data-slot="data-table"]')
        .getByRole('alert')
        .count()
      if (errores > 0) {
        throw new Error(
          'docs-captures: /communications/handoffs pinta un ERROR en su coleccion (role="alert"). ' +
            'Con K3 sin activar el motor contesta 503: la captura enseñaria el fallo, no la puerta.',
        )
      }
      return testigo
    },
  },
  // The administration capability view requires a tenant admin. A global account cannot
  // submit its scoped access question.
  {
    id: 'communications-administration',
    path: '/communications/administration',
    actor: 'k3-tenant-admin',
    heading: /^Channel administration$/,
    despues: eligeWorkspaceConcretoK3,
  },
  // These routes are mounted outside the feature registry.
  { id: 'settings', path: '/settings', heading: /^Settings$/ },
  {
    id: 'status-page',
    path: '/status-page',
    heading: /^Olivares AI — Status$/,
  },
  {
    id: 'sessions',
    path: '/sessions',
    live: true,
    settle: 2500,
    heading: /^Sessions$/,
    // Filter to launched sessions, whose run records supply the metrics shown in the
    // guide.
    despues: async (page) => {
      // The source filter is in the Table tab.
      await page.getByTestId('sessions-list-menu').click()
      await page.getByRole('menuitem', { name: 'Show as table' }).click()
      await page.getByRole('combobox', { name: 'All sources' }).click()
      await page.getByRole('option', { name: 'Launched', exact: true }).click()
      // Blur keyboard focus so the capture does not retain the selector focus ring.
      await page.evaluate(() =>
        (document.activeElement as HTMLElement | null)?.blur(),
      )
    },
  },
  { id: 'capabilities', path: '/capabilities', heading: /^MCP & skills$/ },
  { id: 'skills', path: '/skills', heading: /^Skills catalog$/ },
  {
    id: 'identity',
    path: '/identity',
    settle: 1500,
    heading: /^Identity & NHI$/,
  },
  { id: 'finops', path: '/finops', settle: 1000, heading: /^Cost & FinOps$/ },
  { id: 'stored-budgets', path: '/stored-budgets', heading: /^Budgets$/ },
  { id: 'security', path: '/security', heading: /^Security & forensics$/ },
  {
    id: 'dashboards',
    path: '/dashboards',
    settle: 1000,
    heading: /^Executive overview$/,
  },
  { id: 'killswitch', path: '/killswitch', heading: /^Kill switch$/ },
  {
    id: 'claude-policy',
    path: '/claude-policy',
    heading: /^Claude Code governance$/,
  },
  { id: 'models', path: '/models', heading: /^Models & providers$/ },
  {
    id: 'observability',
    path: '/observability',
    heading: /^Observability & interop$/,
    settle: 800,
    // Frame the live bus counters; absent per-standard health values remain unavailable
    // rather than zero.
    despues: async (page) => {
      // Align the section heading explicitly; partial visibility does not trigger
      // scrollIntoViewIfNeeded.
      const anclaObs = page.getByRole('heading', {
        name: 'Live counters by bus source',
      })
      await anclaObs.evaluate((el) => el.scrollIntoView(true))
      // At the scroll limit, the last section cannot always align with the top of the
      // container.
    },
  },
  // Capture the counters section with the same viewport and a declared crop, including
  // sixteen pixels of vertical padding.
  {
    id: 'observability-counters',
    path: '/observability',
    heading: /^Observability & interop$/,
    settle: 800,
    despues: async (page) => {
      await page
        .getByRole('heading', { name: 'Live counters by bus source' })
        .evaluate((el) => el.scrollIntoView(true))
    },
    recorte: async (page) => {
      const titulo = await page
        .getByRole('heading', { name: 'Live counters by bus source' })
        .boundingBox()
      const nota = await page
        .getByText(/Counters are process-global/)
        .boundingBox()
      if (!titulo || !nota) {
        throw new Error(
          'observability-counters: no encuentro el titulo o la nota final — el recorte ' +
            'no se puede calcular y NO se guarda una imagen a ciegas',
        )
      }
      const y = Math.max(0, Math.round(titulo.y - 16))
      const abajo = Math.round(nota.y + nota.height + 16)
      return {
        x: 0,
        y,
        width: page.viewportSize()?.width ?? 1440,
        height: Math.max(1, abajo - y),
      }
    },
  },
  {
    id: 'knowledge',
    path: '/knowledge',
    settle: 1500,
    heading: /^Data, knowledge & context$/,
  },
  { id: 'catalog', path: '/catalog', settle: 1000, heading: /^Catalog$/ },
  {
    id: 'health',
    path: '/health',
    live: true,
    settle: 2500,
    heading: /^Health & SLA$/,
  },
  { id: 'audit', path: '/audit', settle: 1000, heading: /^Audit ledger$/ },
  {
    id: 'compliance',
    path: '/compliance',
    settle: 1000,
    heading: /^Compliance$/,
  },
  {
    id: 'workspace',
    path: '/workspace',
    settle: 1500,
    // The selected workspace name is the page heading.
    heading: /^Billing$/,
    // The workspace switcher requires multiple workspaces; the demo supplies Billing.
    prepara: async (page) => {
      await page.getByRole('button', { name: /All workspaces/i }).click()
      await page.getByRole('menuitem', { name: /Billing/i }).click()
    },
  },
  { id: 'evals', path: '/evals', settle: 1000, heading: /^Evals$/ },
  {
    id: 'backups',
    path: '/backups',
    settle: 1000,
    heading: /^Backup & Restore$/,
  },
  // Parameterized and first-boot routes use their own setup below.
  { id: 'home', path: '/', settle: 1000, heading: /^Overview$/ },
  // Area directory headings match their registry labels.
  {
    id: 'areas-infrastructure',
    path: '/areas/infrastructure',
    heading: /^Infrastructure$/,
  },
  { id: 'areas-ai', path: '/areas/ai', heading: /^AI$/ },
  {
    id: 'areas-data-context',
    path: '/areas/data-context',
    heading: /^Data & context$/,
  },
  {
    id: 'areas-work-communications',
    path: '/areas/work-communications',
    heading: /^Work & communications$/,
  },
  {
    id: 'areas-automation',
    path: '/areas/automation',
    heading: /^Automation$/,
  },
  {
    id: 'areas-security-identity',
    path: '/areas/security-identity',
    heading: /^Security & identity$/,
  },
  {
    id: 'areas-deployment',
    path: '/areas/deployment',
    heading: /^Deployment$/,
  },
  {
    id: 'areas-observation',
    path: '/areas/observation',
    heading: /^Observability & evidence$/,
  },
  {
    id: 'areas-system',
    path: '/areas/system',
    heading: /^System & settings$/,
  },
  {
    id: 'onboarding',
    path: '/onboarding',
    settle: 1000,
    heading: /^Get started$/,
  },
  {
    id: 'console',
    path: '/console',
    settle: 1000,
    heading: /^Administration$/,
  },
  {
    id: 'sourceDiff',
    path: '/console/sources/diff',
    settle: 1000,
    heading: /^Source diff$/,
  },
  {
    id: 'permissions',
    path: '/permissions',
    settle: 1000,
    heading: /^Permissions$/,
  },
  {
    id: 'routine-policies',
    path: '/routine-policies',
    settle: 1000,
    heading: /^Routine policies$/,
  },
  {
    id: 'agentcore-export',
    path: '/agentcore-export',
    settle: 1000,
    heading: /^AgentCore export$/,
  },
  {
    id: 'deploy',
    path: '/deploy',
    settle: 1000,
    heading: /^Deployment & integration$/,
  },
  {
    id: 'git-publication',
    path: '/git-publication',
    settle: 1000,
    heading: /^Git publication$/,
  },
  { id: 'work', path: '/work', settle: 1000, live: true, heading: /^Work$/ },
  { id: 'estate', path: '/estate', heading: /^Estate$/ },
  {
    id: 'agentops',
    path: '/agentops',
    settle: 1000,
    live: true,
    heading: /^Claude Code$/,
  },
  // Capture the clean-install AI tools state and its next action.
  { id: 'agent-tools', path: '/agent-tools', heading: /^AI tools$/ },
  { id: 'mcpServers', path: '/mcp-servers', heading: /^MCP servers$/ },
  {
    id: 'providers',
    path: '/providers',
    settle: 1000,
    heading: /^Providers$/,
  },
  // Each administration route opens its corresponding tab.
  {
    id: 'provider-profiles',
    path: '/provider-profiles',
    settle: 1000,
    heading: /^Provider profiles$/,
  },
  {
    id: 'provider-bindings',
    path: '/provider-bindings',
    settle: 1000,
    heading: /^Source bindings$/,
  },
  {
    id: 'provider-accounts',
    path: '/provider-accounts',
    settle: 1000,
    heading: /^Provider accounts$/,
  },
  {
    id: 'agent-artifacts',
    path: '/agent-artifacts',
    settle: 1000,
    heading: /^Agent Artifacts$/,
  },
  {
    id: 'workspace-templates',
    path: '/workspace-templates',
    settle: 1000,
    heading: /^Workspace Templates$/,
  },
  {
    id: 'eventing',
    path: '/eventing',
    settle: 1000,
    heading: /^Webhooks & event subscriptions$/,
  },
  {
    id: 'automations',
    path: '/automations',
    settle: 1000,
    heading: /^Automations$/,
  },
  {
    id: 'inference-proxy',
    path: '/inference-proxy',
    settle: 1000,
    heading: /^Inference proxy$/,
  },
  { id: 'alerting', path: '/alerting', settle: 1000, heading: /^Alerting$/ },
  {
    id: 'model-operations',
    path: '/model-operations',
    settle: 1000,
    heading: /^Model Operations$/,
  },
  {
    id: 'adoption',
    path: '/adoption',
    settle: 1000,
    heading: /^Claude Code Adoption$/,
    // Use the telemetry trend for seeded metrics; the demo does not populate
    // organization analytics.
    despues: async (page) => {
      // The lens selector is inside the Trend tab.
      const pestana = page.getByRole('tab', { name: /^Trend$/ })
      try {
        await pestana.waitFor({ timeout: 8_000 })
        await pestana.click()
      } catch {
        throw new Error(
          'docs-captures: no se encontro la pestana `Trend` en /adoption ' +
            '(adoption-view.tsx:95). El selector de lente vive dentro de ella.',
        )
      }
      const selector = page.getByRole('combobox', { name: 'Lens' })
      try {
        await selector.waitFor({ timeout: 8_000 })
        await selector.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo abrir el selector de lente de /adoption (aria-label `Lens`, ' +
            'adoption-view.tsx:236). Si el control cambio de nombre, la captura seguiria saliendo ' +
            'con la lente `analytics`, que esta vacia POR DISENO y se lee como falta de sembrado.',
        )
      }
      // Choose the live telemetry lens by its label.
      await page.getByRole('option', { name: /live telemetry/i }).click()
    },
  },
  {
    // Overview shows both lenses by default. Organization analytics stays empty in this
    // demo.
    id: 'adoption-overview',
    path: '/adoption',
    settle: 1000,
    heading: /^Claude Code Adoption$/,
    // Frame the telemetry acceptance card rather than the empty organization analytics
    // card.
    despues: async (page) => {
      const viva = page.getByText(/Per session \(live telemetry\)/i).first()
      try {
        await viva.waitFor({ timeout: 8_000 })
        // Align the card at the top even when its heading is already partly visible.
        await viva.evaluate((el) => el.scrollIntoView({ block: 'start' }))
      } catch {
        throw new Error(
          'docs-captures: no encuentro la tarjeta `Per session (live telemetry)` en /adoption ' +
            '(adoption-view.tsx:109). Sin ella la foto encuadra la tarjeta `analytics`, que esta ' +
            'a cero POR DISENO, y se lee como que el producto no mide la aceptacion.',
        )
      }
    },
  },
  {
    id: 'recordings',
    path: '/recordings',
    settle: 1000,
    heading: /^Recordings$/,
  },
  ...(EXTENSION_ROUTES.some((route) => route.id === 'postureExport')
    ? [
        {
          id: 'posture-export',
          path: '/posture-export',
          settle: 1000,
          heading: /^Posture export$/,
          // Run a posture export before capturing its result.
          despues: async (page: import('@playwright/test').Page) => {
            const boton = page.getByRole('button', { name: 'Export posture' })
            try {
              await boton.waitFor({ timeout: 8_000 })
              await boton.click()
            } catch {
              throw new Error(
                'docs-captures: no se encontro el boton `Export posture` en /posture-export ' +
                  '(i18n `export.action`). Sin el, la captura sale con los filtros vacios y no ensena ' +
                  'lo unico que esta vista tiene que ensenar.',
              )
            }
            // Require the successful export terminal; a failure or missing terminal cannot
            // qualify the capture.
            await esperarExportacionDePostura(page)
          },
        },
      ]
    : []),
  {
    id: 'orchestration',
    path: '/orchestration',
    settle: 1000,
    heading: /^Orchestration & A2A$/,
  },
  { id: 'voice', path: '/voice', settle: 1000, heading: /^Voice & realtime$/ },
  {
    id: 'sandbox',
    path: '/sandbox',
    settle: 1000,
    heading: /^Testing sandbox$/,
  },
  { id: 'red-team', path: '/red-team', settle: 1000, heading: /^Red-team$/ },
  {
    id: 'team-costs',
    path: '/team-costs',
    settle: 1000,
    heading: /^Team Costs$/,
  },
  { id: 'reporting', path: '/reporting', settle: 1000, heading: /^Reports$/ },
  {
    id: 'platforms',
    path: '/platforms',
    settle: 1000,
    heading: /^Platforms & lifecycle$/,
  },
  {
    id: 'rate-limits',
    path: '/rate-limits',
    settle: 1000,
    heading: /^Rate Limits$/,
  },
  {
    id: 'attestation',
    path: '/attestation',
    settle: 1000,
    heading: /^Supply-chain attestation$/,
  },
  {
    id: 'api-playground',
    path: '/api-playground',
    settle: 1000,
    heading: /^API Playground$/,
    // Send a request before opening History; the tab lists requests made in this
    // session.
    despues: async (page) => {
      // Select the seeded agents endpoint before Send can mount.
      const filtro = page.getByPlaceholder('Filter endpoints…')
      try {
        await filtro.waitFor({ timeout: 8_000 })
        await filtro.fill('/v1/agents')
      } catch {
        throw new Error(
          'docs-captures: no se encontro el filtro de endpoints en /api-playground ' +
            '(i18n `filterPlaceholder`). Sin el, elegir un endpoint entre 765 es a ciegas.',
        )
      }
      // Match the collection endpoint exactly so its detail route cannot make the
      // locator ambiguous.
      const endpoint = page.getByRole('button', {
        name: /^GET\s+\/v1\/agents$/,
      })
      try {
        await endpoint.waitFor({ timeout: 8_000 })
        await endpoint.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo elegir `GET /v1/agents` en el arbol de endpoints ' +
            '(endpoint-tree.tsx:120). Sin seleccion no hay panel de peticion y no hay boton Send.',
        )
      }
      const enviar = page.getByRole('button', { name: 'Send' })
      try {
        await enviar.waitFor({ timeout: 8_000 })
        await enviar.click()
      } catch {
        throw new Error(
          'docs-captures: no se pudo enviar una peticion en /api-playground (boton `Send`, i18n ' +
            '`send`). Sin una peticion no hay historial: la pestana `History` saldria vacia y la ' +
            'captura no ensenaria la funcion que documenta.',
        )
      }
      const pestana = page.getByRole('tab', { name: /^History/ })
      try {
        await pestana.waitFor({ timeout: 8_000 })
        await pestana.click()
      } catch {
        throw new Error(
          'docs-captures: no se encontro la pestana `History` en /api-playground ' +
            '(api-playground-view.tsx:185).',
        )
      }
    },
  },
  {
    id: 'logs',
    path: '/logs',
    settle: 1000,
    live: true,
    heading: /^Log Viewer$/,
  },
  {
    // Require the view heading rather than inferring identity from the output filename.
    id: 'tenants',
    path: '/tenants',
    heading: /^Tenants$/,
  },
  {
    id: 'residency',
    path: '/residency',
    settle: 1000,
    heading: /^Data residency$/,
  },

  // The guide connector list is the Connectors tab. Its named example connectors are
  // seeded before navigation.
  {
    id: 'guias-connectors',
    path: '/console?tab=connectors',
    heading: /^Administration$/,
    settle: 800,
  },
  // Under an AAL3 policy, a password session shows the elevated-authentication notice
  // rather than privileged connector configuration.
  {
    id: 'guias-config-step-up',
    path: '/console?tab=connectors',
    settle: 800,
    modal: true,
    // The target is the assurance panel heading inside Edit connector.
    objetivo: (page) =>
      encabezadoDeDialogo(page, 'Step-up authentication required'),
    despues: async (page) => {
      await page
        .getByRole('row', { name: /claude-code-prod/ })
        .getByRole('button', { name: /^edit$/i })
        .click()
      await page.getByRole('dialog').waitFor({ state: 'visible' })
    },
  },
]

// Write each image and its evidence separately to avoid concurrent workers overwriting
// a shared manifest. The runner later combines them.
async function tomar(
  page: import('@playwright/test').Page,
  {
    id,
    theme,
    ruta,
    objetivo,
    marcadores,
    settle,
    live,
    recorte,
    actor,
    testigo,
  }: {
    id: string
    theme: string
    ruta: string
    // The declared target identifies the captured surface.
    objetivo: import('@playwright/test').Locator
    // Markers identify state that the heading cannot distinguish.
    marcadores?: import('@playwright/test').Locator[]
    settle?: number
    live?: boolean
    recorte?: (
      page: import('@playwright/test').Page,
    ) => Promise<{ x: number; y: number; width: number; height: number }>
    // Non-secret provenance of the principal, when the cell is not the demo superadmin.
    actor?: Record<string, unknown>
    // What the cell's `despues` proved about the data it photographs (the HTTP 200 read).
    testigo?: Record<string, unknown>
  },
) {
  // Wait for the declared route or modal heading; the shell heading can appear before
  // the view mounts.
  const texto = await esperarEncabezado(page, { id, ruta, objetivo })
  if (marcadores) await exigirMarcadores(id, marcadores)

  if (!live) await page.waitForLoadState('networkidle')
  if (settle) await page.waitForTimeout(settle)

  // Check for errors after loading and settling, immediately before the screenshot.
  await exigirInstanteLimpio(page, id)

  // Record empty-panel counts and main-text size without assigning a cause to empty
  // content.
  const vacios = await page.locator('[data-slot="empty-state"]:visible').count()
  // Check main presence before reading it to avoid waiting on an absent element. Count
  // data rows separately from empty-state rows, and empty tables separately from
  // table-free forms.
  const { filas, tablasVacias } = await page.evaluate(() => {
    const raiz = document.querySelector('main') ?? document.body
    const esDato = (tr: Element) =>
      !tr.querySelector('[data-slot="empty-state"]')
    const filas = Array.from(raiz.querySelectorAll('tbody tr')).filter(
      esDato,
    ).length
    const tablasVacias = Array.from(raiz.querySelectorAll('table')).filter(
      (tabla) => {
        const cabeceras = tabla.querySelectorAll('thead th').length
        const datos = Array.from(tabla.querySelectorAll('tbody tr')).filter(
          esDato,
        ).length
        return cabeceras > 0 && datos === 0
      },
    ).length
    return { filas, tablasVacias }
  })
  // Match untranslated text against known locale keys rather than treating dotted
  // domain names as keys.
  const clavesCrudas = await page.evaluate((conocidas: string[]) => {
    const raiz = document.querySelector('main') ?? document.body
    const texto = (raiz as HTMLElement).innerText ?? ''
    const set = new Set(conocidas)
    // Require both a dot and a known locale key; ordinary translated words can equal
    // undotted keys.
    return texto.split(/\s+/).filter((t) => t.includes('.') && set.has(t))
  }, CLAVES_I18N)

  const hayMain = (await page.locator('main').count()) > 0
  const textoMain = hayMain
    ? (
        (await page
          .locator('main')
          .first()
          .innerText()
          .catch(() => '')) ?? ''
      ).trim().length
    : 0

  // Blur keyboard focus separately from moving the pointer.
  await page.evaluate(() =>
    (document.activeElement as HTMLElement | null)?.blur(),
  )

  const png = `playwright-report/docs/${id}-${theme}.png`
  // A declared crop preserves the viewport layout; record its rectangle with the image.
  const clip = recorte ? await recorte(page) : undefined
  await page.screenshot(clip ? { path: png, clip } : { path: png })
  const sha = createHash('sha256').update(fs.readFileSync(png)).digest('hex')
  fs.writeFileSync(
    path.join('playwright-report', 'docs', `${id}-${theme}.evidence.json`),
    `${JSON.stringify(
      {
        id,
        theme,
        route: ruta,
        h1: texto,
        vacios,
        filas,
        tablas_vacias: tablasVacias,
        hay_main: hayMain,
        texto_main: textoMain,
        claves_crudas: clavesCrudas.length,
        claves_crudas_vistas: clavesCrudas.slice(0, 8),
        // Record the crop rectangle so the saved image can be interpreted.
        recorte_declarado: clip ?? null,
        ...(actor ? { actor } : {}),
        ...(testigo ? { testigo_coleccion: testigo } : {}),
        sha256: sha,
      },
      null,
      2,
    )}\n`,
  )

  // The light and dark images must have different hashes.
  if (theme === 'dark') {
    const claro = path.join(
      'playwright-report',
      'docs',
      `${id}-light.evidence.json`,
    )
    if (fs.existsSync(claro)) {
      const otro = JSON.parse(fs.readFileSync(claro, 'utf8')) as {
        sha256?: string
      }
      expect(
        sha,
        `la toma \`dark\` de \`${id}\` es BYTE A BYTE IGUAL que su \`light\`: el tema no cambio, ` +
          'asi que el par publicado son dos copias de la misma foto. Mira si esa vista tiene ' +
          'conmutador de tema y si sus colores salen de tokens (variables CSS) o son fijos.',
      ).not.toBe(otro.sha256)
    }
  }
}

// Use a 1440×1000 viewport at scale factor two. The height keeps navigation visible;
// the higher pixel density preserves small text at the cost of larger PNGs.
test.use({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 2 })

// Require a declared target or heading before capturing a view.
function objetivoDeLaVista(
  page: import('@playwright/test').Page,
  view: (typeof VIEWS)[number],
): import('@playwright/test').Locator {
  if (view.objetivo) return view.objetivo(page)
  if (view.heading) return encabezadoDePagina(page, view.heading)
  throw new Error(
    `${view.id}: la entrada no declara ni \`heading\` ni \`objetivo\``,
  )
}

test.describe('Docs captures over real seeded data', () => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/docs-captures.sh',
  )

  // Capture both themes from one page load; toggle the theme without navigating again.
  for (const view of VIEWS) {
    {
      test(`capture ${view.id}`, async ({ page, request }) => {
        if (view.id === 'orchestration' || view.id === 'automations') {
          const info = await request.get('/v1/server-info')
          expect(info.ok()).toBe(true)
          test.skip(
            (await info.json()).edition === 'Community',
            'Business Identity & Scale view',
          )
        }
        await page.addInitScript(
          ([tenant]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            // The theme store reads a plain string before first paint.
            localStorage.setItem('olivares.theme', 'light')
          },
          [demoTenant],
        )

        // Use an exact sidebar link for the global account and the ordinary member
        // login for scoped captures.
        const actor = view.actor
          ? await provisionaMiembroK3(
              page,
              view.actor === 'k3-tenant-admin' ? 'admin' : 'editor',
            )
          : undefined
        await page.goto('/login')
        await page.locator('#email').fill(actor ? actor.email : DEMO_EMAIL)
        await page
          .locator('#password')
          .fill(actor ? actor.clave : DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        if (actor) {
          // A member login need not mount the global account sidebar; require departure
          // from the login route.
          await page.waitForURL((u) => !u.pathname.startsWith('/login'), {
            timeout: 60_000,
          })
        } else {
          await expect(navReady(page)).toBeVisible({
            timeout: 60_000,
          })
        }

        // Prepare authenticated state before navigation so it is present when the view
        // mounts.
        if (view.prepara) await view.prepara(page)

        await page.goto(view.path)
        // Apply state that requires the mounted view after navigation.
        const testigo = view.despues ? await view.despues(page) : undefined

        for (const theme of ['light', 'dark'] as const) {
          if (theme === 'dark') {
            // Close a blocking modal, change the theme and reopen it without
            // navigating.
            if (view.modal) {
              await page.keyboard.press('Escape')
              await expect(page.getByRole('dialog')).toBeHidden()
            }
            // Use the theme control so store-driven charts update too. Header-free
            // views use the CSS-class fallback.
            const conmutador = page.getByRole('button', {
              name: 'Toggle theme',
            })
            if ((await conmutador.count()) > 0) {
              await conmutador.click()
              await page
                .getByRole('menuitem', { name: 'Dark', exact: true })
                .click()
            } else {
              // This fallback applies to CSS-token views without a theme control. The
              // class and image-hash assertions still verify the change.
              await page
                .locator('html')
                .evaluate((el) => el.classList.add('dark'))
            }
            await expect(page.locator('html')).toHaveClass(/dark/)
            if (view.modal && view.despues) await view.despues(page)
          }
          // Move the pointer away from captured controls to clear hover state.
          await page.mouse.move(0, 0)
          await tomar(page, {
            id: view.id,
            theme,
            ruta: view.path,
            objetivo: objetivoDeLaVista(page, view),
            settle: view.settle,
            live: view.live,
            recorte: view.recorte,
            actor: actor?.procedencia,
            testigo: testigo || undefined,
          })
        }
      })
    }
  }

  // Capture the drift overlay while open in both themes.
  for (const theme of ['light', 'dark'] as const) {
    test(`capture access-map drift overlay — ${theme}`, async ({ page }) => {
      await page.addInitScript(
        ([tenant, th]) => {
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
          )
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        },
        [demoTenant, theme],
      )

      await page.goto('/login')
      await page.locator('#email').fill(DEMO_EMAIL)
      await page.locator('#password').fill(DEMO_PASSWORD)
      await page.getByRole('button', { name: /^sign in$/i }).click()
      await expect(navReady(page)).toBeVisible({
        timeout: 60_000,
      })

      await page.goto('/access-map')
      await expect(page.locator('.react-flow__node').first()).toBeAttached({
        timeout: 60_000,
      })
      await page.getByRole('button', { name: /permitted vs observed/i }).click()
      // Identify the overlay by its own heading while the page heading remains visible.
      await tomar(page, {
        id: 'access-map-drift',
        theme,
        ruta: '/access-map (overlay drift)',
        objetivo: page.getByRole('complementary').getByRole('heading', {
          level: 2,
          name: 'Least-privilege drift',
          exact: true,
        }),
        settle: 1500,
      })
    })
  }

  // Open each console tab before capturing its state.
  const CONSOLE_TABS = [
    { id: 'console-agents', trigger: /^agents$/i },
    { id: 'console-bindings', trigger: /^source bindings$/i },
  ]
  for (const theme of ['light', 'dark'] as const) {
    for (const tab of CONSOLE_TABS) {
      test(`capture ${tab.id} — ${theme}`, async ({ page }) => {
        await page.addInitScript(
          ([tenant, th]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            localStorage.setItem('olivares.theme', th)
          },
          [demoTenant, theme],
        )

        await page.goto('/login')
        await page.locator('#email').fill(DEMO_EMAIL)
        await page.locator('#password').fill(DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        await expect(navReady(page)).toBeVisible({
          timeout: 60_000,
        })

        await page.goto('/console')
        await page.getByRole('tab', { name: tab.trigger }).click()
        // These tabs share a page heading; the selected-tab marker identifies the
        // captured state.
        await tomar(page, {
          id: tab.id,
          theme,
          ruta: `/console (tab ${tab.id})`,
          objetivo: encabezadoDePagina(page, 'Administration'),
          marcadores: [
            page.getByRole('tab', { name: tab.trigger, selected: true }),
          ],
          settle: 1000,
        })
      })
    }
  }

  // These scenes exercise graph interaction, session panels and workspace browsing.
  // Invitation provisioning must satisfy any AAL3 policy; an error page cannot
  // substitute for it.

  // Open a launched session with a run; name missing seed data before attempting a tab
  // that cannot exist.

  async function abreSesionOperada(page: import('@playwright/test').Page) {
    // Launched rows and their Origin values live in the Table tab.
    await page.getByTestId('sessions-list-menu').click()
    await page.getByRole('menuitem', { name: 'Show as table' }).click()
    await esperaTablasCargadas(page)
    const fila = page.getByRole('row').filter({ hasText: 'Launched' }).first()
    try {
      await fila.waitFor({ timeout: 8_000 })
    } catch {
      throw new Error(
        'docs-captures: no hay ninguna sesion `Launched` en /agentops. Las pestañas Live, ' +
          'Governance y Lifecycle solo existen con `run` (sessions/session-card.tsx:285-295), ' +
          'asi que esto es un hueco de SEMBRADO, no un selector roto.',
      )
    }
    await fila.click()
  }

  const ESCENAS_VIDEO: {
    id: string
    escena: number
    ruta: string
    // Live session streams cannot reach networkidle.
    live?: boolean
    prepara: (page: import('@playwright/test').Page) => Promise<void>
    // Resolve dynamic dialog headings asynchronously through their accessible label.
    objetivo: (
      page: import('@playwright/test').Page,
    ) =>
      | import('@playwright/test').Locator
      | Promise<import('@playwright/test').Locator>
    // The selected-tab marker identifies state within the dialog.
    marcadores?: (
      page: import('@playwright/test').Page,
    ) => import('@playwright/test').Locator[]
  }[] = [
    {
      id: 'video-05-edge-honesty',
      escena: 5,
      ruta: '/access-map',
      objetivo: (page) => encabezadoDePagina(page, 'Access map'),
      prepara: async (page) => {
        await page.goto('/access-map')
        // Require a graph edge before capturing the interaction.
        const arista = page.locator('.react-flow__edge').first()
        await expect(arista).toBeAttached({ timeout: 60_000 })
        // A curved SVG edge may miss a bounding-box-center hover. Dispatch the hover
        // events to the edge itself.
        await arista.dispatchEvent('mouseover')
        await arista.dispatchEvent('mouseenter')
      },
    },
    // Choose a launched row with a run: Live, Governance and Lifecycle tabs depend on
    // it.

    {
      id: 'video-07-live',
      live: true,
      escena: 7,
      ruta: '/agentops (session card, tab Live)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await abreSesionOperada(page)
        await page.getByRole('tab', { name: /^live$/i }).click()
      },
      // Resolve the dynamic session heading through the dialog label.
      objetivo: (page) => encabezadoDelDialogoAbierto(page),
      marcadores: (page) => [
        page
          .getByRole('dialog')
          .getByRole('tab', { name: /^live$/i, selected: true }),
      ],
    },
    {
      id: 'video-08-governance',
      live: true,
      escena: 8,
      ruta: '/agentops (session card, tab Governance)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await abreSesionOperada(page)
        await page.getByRole('tab', { name: /^governance$/i }).click()
      },
      // Resolve the dynamic session heading through the dialog label.
      objetivo: (page) => encabezadoDelDialogoAbierto(page),
      marcadores: (page) => [
        page
          .getByRole('dialog')
          .getByRole('tab', { name: /^governance$/i, selected: true }),
      ],
    },
    {
      id: 'video-10-workspace-browser',
      live: true,
      escena: 10,
      ruta: '/agentops (tab Workspaces, Browse files)',
      prepara: async (page) => {
        await page.goto('/agentops')
        await page.getByTestId('sessions-list-menu').click()
        await page.getByRole('menuitem', { name: /^workspaces$/i }).click()
        // Wait for the workspace panel to load before judging whether Browse files is
        // absent.
        await esperaTablasCargadas(page)
        // Choose one Browse files button before waiting; each workspace row supplies
        // one.
        const abrir = page
          .getByRole('button', { name: /^browse files$/i })
          .first()
        try {
          await abrir.waitFor({ timeout: 8_000 })
        } catch {
          throw new Error(
            'docs-captures: la pestaña Workspaces de /agentops no tiene ningun «Browse files». ' +
              'El panel esta vacio (agentops/workspaces-panel.tsx usa `workspaces.empty`), asi ' +
              'que es un hueco de SEMBRADO, no un selector roto.',
          )
        }
        await abrir.click()
      },
      objetivo: (page) => encabezadoDelDialogoAbierto(page),
    },
  ]
  for (const theme of ['light', 'dark'] as const) {
    for (const escena of ESCENAS_VIDEO) {
      test(`capture ${escena.id} (guion escena ${escena.escena}) — ${theme}`, async ({
        page,
      }) => {
        // Keep the normal test timeout; increasing it cannot make a sub-pixel graph
        // target actionable.
        await page.addInitScript(
          ([tenant, th]) => {
            localStorage.setItem(
              'olivares.tenant',
              JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
            )
            localStorage.setItem(
              'olivares.lang',
              JSON.stringify({ state: { lang: 'en' }, version: 0 }),
            )
            localStorage.setItem('olivares.theme', th)
          },
          [demoTenant, theme],
        )

        await page.goto('/login')
        await page.locator('#email').fill(DEMO_EMAIL)
        await page.locator('#password').fill(DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        await expect(navReady(page)).toBeVisible({ timeout: 60_000 })

        await escena.prepara(page)
        await tomar(page, {
          id: escena.id,
          theme,
          ruta: escena.ruta,
          objetivo: await escena.objetivo(page),
          marcadores: escena.marcadores?.(page),
          settle: 1200,
          live: escena.live,
        })
      })
    }
  }
})

// Capture login without an authenticated session so it cannot redirect to the app.
test.describe('Docs captures — las patas sin autenticar', () => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/docs-captures.sh',
  )

  const PUBLICAS: { id: string; path: string; heading: RegExp }[] = [
    { id: 'login', path: '/login', heading: /^Sign in$/ },
    // A valid invitation capture requires its token and authorized provisioning. An
    // AAL3 requirement must be satisfied rather than bypassed.
  ]

  for (const theme of ['light', 'dark'] as const) {
    for (const vista of PUBLICAS) {
      test(`capture ${vista.id} — ${theme}`, async ({ page }) => {
        await page.addInitScript((th) => {
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        }, theme)

        await page.goto(vista.path)
        // Require the requested path so a sign-in redirect cannot qualify as this
        // public page.
        expect(
          new URL(page.url()).pathname,
          `${vista.id}: la navegación acabó en otra ruta — ¿había sesión iniciada?`,
        ).toBe(vista.path)
        await tomar(page, {
          id: vista.id,
          theme,
          ruta: vista.path,
          objetivo: encabezadoDePagina(page, vista.heading),
        })
      })
    }
  }
})

// Setup uses a separate unseeded engine whose server-info reports setup_required. Skip
// without SETUP_BASE_URL; require both the final path and heading. Keep the literal
// path for registry coverage.
const VISTA_SETUP = {
  id: 'setup',
  path: '/setup',
  heading: /^First-boot setup$/,
}

test.describe('Docs captures — el asistente de primer arranque', () => {
  const setupBase = process.env.SETUP_BASE_URL
  test.skip(
    !setupBase,
    'SETUP_BASE_URL not set — run via scripts/docs-captures.sh (needs the UNSEEDED engine)',
  )

  for (const theme of ['light', 'dark'] as const) {
    test(`capture setup — ${theme}`, async ({ page }) => {
      await page.addInitScript((th) => {
        localStorage.setItem(
          'olivares.lang',
          JSON.stringify({ state: { lang: 'en' }, version: 0 }),
        )
        localStorage.setItem('olivares.theme', th)
      }, theme)

      await page.goto(`${setupBase}${VISTA_SETUP.path}`)
      expect(
        new URL(page.url()).pathname,
        'setup: la navegacion acabo en otra ruta — ¿el segundo motor venia ya instalado?',
      ).toBe(VISTA_SETUP.path)
      await tomar(page, {
        id: VISTA_SETUP.id,
        theme,
        ruta: VISTA_SETUP.path,
        objetivo: encabezadoDePagina(page, VISTA_SETUP.heading),
      })
    })
  }
})

// Resolve the recording session ID from the seeded engine and skip without it. The
// viewer heading proves page identity and loading, not the exact backend row. Keep the
// parameterized path literal for registry coverage.
const VISTA_VISOR = { id: 'session-viewer', path: '/session-viewer/$id' }

test.describe('Docs captures — el visor de sesion (ruta parametrica)', () => {
  const sessionId = process.env.DEMO_SESSION_ID
  test.skip(
    !sessionId || !demoTenant,
    'DEMO_SESSION_ID not set — run via scripts/docs-captures.sh (resuelve el id del estate sembrado)',
  )

  for (const theme of ['light', 'dark'] as const) {
    test(`capture session-viewer — ${theme}`, async ({ page }) => {
      await page.addInitScript(
        ([tenant, th]) => {
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
          )
          localStorage.setItem(
            'olivares.lang',
            JSON.stringify({ state: { lang: 'en' }, version: 0 }),
          )
          localStorage.setItem('olivares.theme', th)
        },
        [demoTenant, theme],
      )
      await page.goto('/login')
      await page.locator('#email').fill(DEMO_EMAIL)
      await page.locator('#password').fill(DEMO_PASSWORD)
      await page.getByRole('button', { name: /^sign in$/i }).click()
      await expect(navReady(page)).toBeVisible({
        timeout: 60_000,
      })

      const ruta = `/session-viewer/${sessionId}`
      await page.goto(ruta)
      expect(
        new URL(page.url()).pathname,
        `session-viewer: la navegacion acabo en ${new URL(page.url()).pathname} — ¿el id ya no existe?`,
      ).toBe(ruta)
      await tomar(page, {
        id: VISTA_VISOR.id,
        theme,
        ruta,
        settle: 1500,
        objetivo: encabezadoDePagina(page, /^Session Recording Viewer$/),
      })
    })
  }
})
