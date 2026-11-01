// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { randomBytes, randomUUID } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { expect, test } from '@playwright/test'
import { Aal3Policy } from './aal3-policy'
import { journeyNowLink } from './browser-session'
import {
  encabezadoDeDialogo,
  encabezadoDelDialogoAbierto,
  encabezadoDePagina,
} from './capture-target'
import { tomar } from './docs-capture-helpers'
import { esperarExportacionDePostura } from './posture-export-terminal'
import { EXTENSION_ROUTES } from '../src/features/extension-routes'
import { rutasExtraDeCaptura } from '../src/features/capture-routes'
import census from '../src/features/route-census.json' with { type: 'json' }
import type { ProtocolBindingSpec } from '../src/features/protocol-bindings/types'
import { newCanonicalId } from '../src/features/communications/intent'

// Capture the running console in English, with seeded data and declared response
// fixtures, in both themes. The runner supplies the engine and tenant; PNGs and
// per-image evidence go under playwright-report/docs.

const demoTenant = process.env.DEMO_TENANT ?? ''

const DEMO_EMAIL = 'demo@olivares.local'
const DEMO_PASSWORD = 'olivares-demo-estate'

// The left navigation is present after account adoption.
const navReady = journeyNowLink

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

// Provision a new account and its first tenant membership atomically, then sign in
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
  // A neutral local part: the account is on screen in the member lists and the audit trail.
  const email = `${rol}.${randomBytes(4).toString('hex')}@olivares.local`
  const clave = randomBytes(24).toString('base64url')
  const creado = await api.post('/v1/users', {
    headers: cabeceras,
    data: JSON.stringify({
      email,
      password: clave,
      tenant: demoTenant,
      role: rol,
    }),
  })
  expect(
    creado.status(),
    'docs-captures: POST /v1/users del actor editor',
  ).toBe(201)
  const nuevo = (await creado.json()) as {
    id: string
    membership: { user_id: string; tenant: string; role: string }
  }
  const usuario = nuevo.id
  expect(nuevo.membership).toMatchObject({
    user_id: usuario,
    tenant: demoTenant,
    role: rol,
  })
  if (rol === 'admin') await grantChannelAdministration(api, cabeceras, usuario)
  if (rol === 'editor') await seedMemberHandoff(api, cabeceras, usuario)
  const listado = await api.get('/v1/members', { headers: cabeceras })
  expect(listado.status()).toBe(200)
  const padron = (await listado.json()) as {
    items: { user_id: string; role: string }[]
  }
  expect(padron.items).toContainEqual(
    expect.objectContaining({ user_id: usuario, role: rol }),
  )
  return {
    email,
    clave,
    procedencia: {
      kind: 'tenant-member',
      role: rol,
      tenant: demoTenant,
      user_id: usuario,
      basis:
        'provisioned atomically through POST /v1/users with the demo superadmin ' +
        'login, then signed in through the normal login form',
    },
  }
}

// The recipient exists only at capture time. Offer real owned work from the
// dataset's tenant administrator before the new member signs in.
async function seedMemberHandoff(
  api: import('@playwright/test').APIRequestContext,
  ownerHeaders: Record<string, string>,
  recipient: string,
) {
  const login = await api.post('/v1/auth/login', {
    data: { email: 'ana.ruiz@olivares.local', password: DEMO_PASSWORD },
  })
  expect(login.status(), 'handoff sender login').toBe(200)
  const headers = {
    Authorization: `Bearer ${(await login.json()).token as string}`,
    'X-Olivares-Tenant': demoTenant,
  }
  const spaces = await api.get('/v1/workspaces', { headers })
  expect(spaces.status()).toBe(200)
  const workspace = ((await spaces.json()) as { items: { id: string }[] })
    .items[0]
  expect(workspace, 'handoff workspace').toBeTruthy()
  const channels = await api.get(
    `/v1/m/sessions/channels?workspace_id=${workspace.id}&limit=100`,
    { headers },
  )
  expect(channels.status()).toBe(200)
  const channel = (
    (await channels.json()) as { items: { id: string; version: number }[] }
  ).items[0]
  expect(channel, 'handoff sender channel').toBeTruthy()
  // The new member needs a current read grant on the carrier's channel. The
  // dataset owner grants only that access, retaining administration itself.
  const granted = await api.post(
    `/v1/m/sessions/channels/${channel.id}/grants`,
    {
      headers: {
        ...ownerHeaders,
        'Idempotency-Key': newCanonicalId(),
        'If-Match': `"v${channel.version}"`,
      },
      data: {
        subject: { kind: 'user', ref: recipient },
        can_read: true,
        can_write: false,
        can_admin: false,
      },
    },
  )
  expect(granted.status(), 'handoff recipient channel read grant').toBe(200)
  const who = await api.get('/v1/auth/whoami', { headers })
  expect(who.status()).toBe(200)
  const input = {
    workspace_id: workspace.id,
    work_kind: 'implementation',
    title: 'Review release readiness',
    brief_md: 'Review the service checks and record the release decision.',
    priority: 'p1',
    owner_kind: 'user',
    owner_ref: ((await who.json()) as { user_id: string }).user_id,
    provenance_kind: 'human',
    provenance_ref: 'release-readiness',
    context_refs: [],
    acceptance: [
      {
        criterion_key: 'release-decision',
        ordinal: 0,
        statement: 'Record the release readiness decision.',
        required: true,
      },
    ],
  }
  const workPath = '/v1/m/sessions/work-items'
  const plan = await api.post(`${workPath}?mode=plan`, { headers, data: input })
  expect(plan.status(), 'handoff work plan').toBe(200)
  const planned = (await plan.json()) as { verdict: string; plan_hash: string }
  expect(planned.verdict).toBe('LIMPIO')
  expect(planned.plan_hash).toMatch(/^[0-9a-f]{64}$/)
  const created = await api.post(`${workPath}?mode=apply`, {
    headers: {
      ...headers,
      'Idempotency-Key': randomUUID(),
      'If-Plan-Hash': planned.plan_hash,
    },
    data: input,
  })
  expect(created.status(), 'handoff work apply').toBe(200)
  const work = (await created.json()) as { result_id: string; verdict: string }
  expect(work.verdict).toBe('LIMPIO')
  expect(work.result_id).toBeTruthy()
  const source = await api.get(`${workPath}/${work.result_id}`, { headers })
  expect(source.status(), 'handoff source work read').toBe(200)
  const etag = source.headers()['etag']
  expect(etag, 'handoff source work version').toMatch(/^"v[1-9][0-9]*"$/)
  const offered = await api.post('/v1/m/sessions/handoffs', {
    headers: {
      ...headers,
      'Idempotency-Key': newCanonicalId(),
      'If-Match': etag,
    },
    data: {
      channel_id: channel.id,
      work_item_id: work.result_id,
      recipient: { kind: 'user', ref: recipient },
      handoff: {
        summary: 'Service checks are ready for review.',
        next_action: 'Record the release decision.',
      },
      ack_deadline: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
    },
  })
  expect(offered.status(), `handoff offer: ${await offered.text()}`).toBe(201)
}

async function seedProtocolDraft(page: import('@playwright/test').Page) {
  const api = page.request
  const login = await api.post('/v1/auth/login', {
    data: { email: DEMO_EMAIL, password: DEMO_PASSWORD },
  })
  expect(login.status()).toBe(200)
  const headers = {
    Authorization: `Bearer ${(await login.json()).token as string}`,
    'X-Olivares-Tenant': demoTenant,
  }
  const spaces = await api.get('/v1/workspaces', { headers })
  expect(spaces.status()).toBe(200)
  const workspace = ((await spaces.json()) as { items: { id: string }[] })
    .items[0]
  expect(workspace, 'protocol draft workspace').toBeTruthy()
  const input = {
    workspace_id: workspace.id,
    binding_key: 'release-readiness',
    generation: 1,
    protocol: 'a2a',
    protocol_version: '1.0.1',
    direction: 'outbound',
    local_kind: 'work_item',
    local_selector: { work_kind: 'implementation' },
    peer_authority: 'https://peer.example.invalid',
    remote_resource_kind: 'agent',
    remote_resource_ref: 'agent:release-review',
    mapping_schema: 'olivares.protocol-binding/v1',
    mapping: [
      {
        source: 'work.title',
        target: 'message.text',
        cardinality: 'one_to_one',
        transform: 'text',
      },
    ],
    known_losses: [],
    rule_refs: ['rule:release-readiness'],
    permission_profile_ref: 'permission:release-readiness',
    currency_policy: 'pinned',
  }
  const endpoint = '/v1/m/sessions/protocol-binding-specs'
  const listed = await api.get(
    `${endpoint}?workspace_id=${workspace.id}&binding_key=${input.binding_key}&generation=1&limit=100`,
    { headers },
  )
  expect(listed.status()).toBe(200)
  const existing = (
    (await listed.json()) as { items: ProtocolBindingSpec[] }
  ).items.find((spec) => spec.generation === input.generation)
  // A retry after apply reads the same generation rather than creating it again.
  let spec = existing
  if (!spec) {
    const plan = await api.post(`${endpoint}?mode=plan`, {
      headers,
      data: input,
    })
    expect(plan.status(), `protocol draft plan: ${await plan.text()}`).toBe(200)
    const planned = (await plan.json()) as { plan_hash: string }
    expect(planned.plan_hash).toMatch(/^[0-9a-f]{64}$/)
    const applied = await api.post(`${endpoint}?mode=apply`, {
      headers: {
        ...headers,
        'Idempotency-Key': randomUUID(),
        'If-Plan-Hash': planned.plan_hash,
      },
      data: input,
    })
    expect(
      applied.status(),
      `protocol draft apply: ${await applied.text()}`,
    ).toBe(201)
    spec = ((await applied.json()) as { spec: ProtocolBindingSpec }).spec
  }
  expect(spec.state).toBe('draft')
  expect(spec.validation.verdict).toBe('UNKNOWN')
  const read = await api.get(`${endpoint}/${spec.id}`, { headers })
  expect(read.status()).toBe(200)
  expect(await read.json()).toMatchObject({
    ...input,
    id: spec.id,
    state: 'draft',
  })
  return {
    spec_id: spec.id,
    workspace_id: workspace.id,
    validation: spec.validation,
    binding_property:
      'A live binding requires reconciliation against a reachable peer; this offline estate authors a draft specification.',
  }
}

// The channel administration view is read as a freshly provisioned tenant administrator, who holds no grant on
// the channels the dataset seeded. The demo owner administers them and grants that person the same, as an
// administrator would when adding someone to the team.
async function grantChannelAdministration(
  api: import('@playwright/test').APIRequestContext,
  cabeceras: Record<string, string>,
  usuario: string,
) {
  const espacios = await api.get('/v1/workspaces', { headers: cabeceras })
  expect(espacios.status(), 'docs-captures: /v1/workspaces as the owner').toBe(
    200,
  )
  const lista = (
    (await espacios.json()) as { items: { id: string; is_default?: boolean }[] }
  ).items
  const espacio = lista.find((w) => w.is_default) ?? lista[0]
  expect(espacio, 'docs-captures: the estate has no workspace').toBeTruthy()
  const canales = await api.get(
    `/v1/m/sessions/channels?workspace_id=${espacio!.id}&limit=100`,
    { headers: cabeceras },
  )
  expect(canales.status(), 'docs-captures: the channel list as the owner').toBe(
    200,
  )
  const items = (
    (await canales.json()) as { items: { id: string; version: number }[] }
  ).items
  expect(
    items.length,
    'docs-captures: no channels are seeded; the demo dataset did not create the communications plane',
  ).toBeGreaterThan(0)
  for (const canal of items) {
    const concedido = await api.post(
      `/v1/m/sessions/channels/${canal.id}/grants`,
      {
        // A grant is written against the channel version it was read at.
        headers: {
          ...cabeceras,
          'Idempotency-Key': randomUUID(),
          'If-Match': `"v${canal.version}"`,
        },
        data: JSON.stringify({
          subject: { kind: 'user', ref: usuario },
          can_read: true,
          can_write: true,
          can_admin: true,
        }),
      },
    )
    expect(
      concedido.status(),
      `docs-captures: granting administration of channel ${canal.id}: ${await concedido.text()}`,
    ).toBeLessThan(300)
  }
}

// The card the adoption overview is framed on: the nearest card around the live telemetry title.
const tarjetaDeTelemetria = (page: import('@playwright/test').Page) =>
  page
    .getByText(/Per session \(live telemetry\)/i)
    .first()
    .locator('xpath=ancestor::div[contains(@class, "rounded-lg")][1]')

// The deployment-wide AAL3 policy the step-up capture raises; see aal3-policy.ts.
let aal3Policy: Aal3Policy | undefined

const A_MANO: {
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
  // Runs after the capture, pass or fail, for a view whose `prepara` changed deployment
  // state that later captures must not inherit.
  limpia?: () => Promise<void>
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
    path: '/identity?tab=people',
    heading: /^Identity & access$/,
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
  {
    id: 'reach',
    path: '/reach',
    settle: 1500,
    heading: /^Reach$/,
  },
  { id: 'inventory', path: '/inventory', heading: /^Inventory$/ },
  {
    id: 'communications-protocol-bindings',
    path: '/communications/protocol-bindings',
    heading: /^Protocol bindings$/,
    // Choose a concrete workspace after the view mounts; All workspaces cannot supply
    // this scope.
    despues: async (page) => {
      const draft = await seedProtocolDraft(page)
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
      await expect(page.locator('main li button').first()).toBeVisible()
      await expect(page.locator('main li button').first()).toContainText(
        'release-readiness',
      )
      return draft
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
      expect(
        cuerpo.items.length,
        'the member inbox must contain a real offered handoff',
      ).toBeGreaterThan(0)
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
  { id: 'settings', path: '/settings', heading: /^General$/ },
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
    // The hook below switches to the table, whose page heading is "Table".
    heading: /^Table$/,
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
  {
    id: 'identity',
    path: '/identity',
    settle: 1500,
    heading: /^Identity & access$/,
  },
  { id: 'vault', path: '/vault', heading: /^Vault$/ },
  { id: 'usage', path: '/usage', heading: /^Usage and cost, by account$/ },
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
  { id: 'models', path: '/models', heading: /^Models$/ },
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
  { id: 'retention', path: '/retention', settle: 1000, heading: /^Retention$/ },
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
  { id: 'updates', path: '/updates', settle: 1000, heading: /^Updates$/ },
  // Parameterized and first-boot routes use their own setup below.
  { id: 'home', path: '/', settle: 1000, heading: /^Now$/ },
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
    heading: /^Approvals and permissions$/,
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
  {
    id: 'work',
    path: '/work',
    settle: 1000,
    live: true,
    heading: /^Work$/,
    // The list stays a skeleton until the live connection opens; a shot before that shows no work.
    despues: async (page) => {
      await expect(page.getByText('Live', { exact: true }).first()).toBeVisible(
        { timeout: 20_000 },
      )
    },
  },
  { id: 'estate', path: '/estate', heading: /^Estate$/ },
  {
    id: 'agentops',
    path: '/agentops',
    settle: 1000,
    live: true,
    // /agentops is a second door into Sessions and mounts that screen.
    heading: /^Sessions$/,
  },
  // Capture the clean-install AI tools state and its next action.
  {
    id: 'agent-tools',
    path: '/agent-tools',
    heading: /^AI tools$/,
    // The tool rows appear only after the host inventory refresh; before that the page body is empty.
    despues: async (page) => {
      await page
        .getByText('Default sign-in')
        .first()
        .waitFor({ timeout: 20_000 })
    },
  },
  // The API key, profile, binding and account pages are doors into Tools (#2552).
  {
    id: 'providers',
    path: '/providers',
    settle: 1000,
    heading: /^AI tools$/,
  },
  {
    id: 'provider-profiles',
    path: '/provider-profiles',
    settle: 1000,
    heading: /^AI tools$/,
  },
  {
    id: 'provider-bindings',
    path: '/provider-bindings',
    settle: 1000,
    heading: /^AI tools$/,
  },
  {
    id: 'provider-accounts',
    path: '/provider-accounts',
    settle: 1000,
    heading: /^AI tools$/,
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
    // Frame the telemetry acceptance card rather than the empty organization analytics card: pinned just under
    // the page header, then cropped to the card, so the image starts at its title and stops before the
    // official-versus-observed comparison below it.
    despues: async (page) => {
      const tarjeta = tarjetaDeTelemetria(page)
      try {
        await tarjeta.waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'docs-captures: no encuentro la tarjeta `Per session (live telemetry)` en /adoption ' +
            '(adoption-view.tsx:109). Sin ella la foto encuadra la tarjeta `analytics`, que esta ' +
            'a cero POR DISENO, y se lee como que el producto no mide la aceptacion.',
        )
      }
      try {
        // The tool table and the token chart render after the tiles; the box is read once they have. The
        // table is found by its Acceptance column, which does not depend on the markup's ARIA roles.
        await tarjeta
          .getByText(/^acceptance$/i)
          .first()
          .waitFor({ timeout: 8_000 })
      } catch {
        throw new Error(
          'adoption-overview: la tarjeta de telemetria viva no mostro su tabla de herramientas: ' +
            'sin ella la tarjeta no esta completa y NO se guarda una imagen a medias',
        )
      }
      await tarjeta.evaluate((el) => {
        // A margin keeps the card clear of the sticky page header whatever its height.
        el.style.scrollMarginTop = '72px'
        el.scrollIntoView({ block: 'start' })
      })
      // The token chart animates in; the box is read after it.
      await page.waitForTimeout(800)
    },
    recorte: async (page) => {
      const caja = await tarjetaDeTelemetria(page).boundingBox()
      const vista = page.viewportSize()
      if (!caja || !vista) {
        throw new Error(
          'adoption-overview: no encuentro la tarjeta de telemetria viva — el recorte no se ' +
            'puede calcular y NO se guarda una imagen a ciegas',
        )
      }
      const y = Math.max(0, Math.round(caja.y - 12))
      const alto = Math.round(caja.height + 24)
      if (y + alto > vista.height) {
        throw new Error(
          `adoption-overview: la tarjeta (${Math.round(caja.height)} px) no cabe bajo la cabecera ` +
            `en una vista de ${vista.height} px: el recorte saldria truncado y NO se guarda`,
        )
      }
      return {
        x: Math.max(0, Math.round(caja.x - 12)),
        y,
        width: Math.round(caja.width + 24),
        height: alto,
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
  // The guide connector list is the Connectors tab. Its named example connectors are
  // seeded before navigation.
  {
    id: 'guias-connectors',
    path: '/console?tab=connectors',
    heading: /^Administration$/,
    settle: 800,
  },
  // Under an AAL3 policy, a password session shows the elevated-authentication notice
  // rather than privileged connector configuration. The demo estate has no such policy
  // and the API cannot raise it from a password session, so `prepara` sets it through
  // the console (aal3-policy.ts) and `limpia` puts it back for the captures after it.
  {
    id: 'guias-config-step-up',
    path: '/console?tab=connectors',
    settle: 800,
    modal: true,
    prepara: async (page) => {
      // The passkey ceremony and the restore after it run inside this test's time.
      test.slow()
      const browser = page.context().browser()
      if (!browser)
        throw new Error('docs-captures: the AAL3 policy needs a browser handle')
      aal3Policy = new Aal3Policy(browser, new URL(page.url()).origin, {
        email: DEMO_EMAIL,
        password: DEMO_PASSWORD,
        tenant: demoTenant,
      })
      await aal3Policy.enable()
    },
    limpia: async () => {
      await aal3Policy?.restore()
      aal3Policy = undefined
    },
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

const VIEWS: (typeof A_MANO)[number][] = [
  ...rutasExtraDeCaptura(EXTENSION_ROUTES, A_MANO).map(
    ({ id, path, heading }) => ({
      id,
      path,
      heading: new RegExp(
        `^${heading.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`,
      ),
    }),
  ),
  ...A_MANO,
]

// Use a 1440×900 viewport at scale factor two: the size every published capture has, so a page and its
// neighbors line up. The pixel density preserves small text at the cost of larger PNGs.
// The zone is pinned so a capture does not show where it was taken (Settings prints the viewer's zone).
test.use({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  trace: 'off',
  timezoneId: 'UTC',
})

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

// The title of a view's test; the afterEach below finds the view by it.
const tituloDeVista = (id: string) => `capture ${id}`

test.describe('Docs captures over real seeded data', () => {
  test.skip(
    !demoTenant,
    'DEMO_TENANT not set — run via scripts/docs-captures.sh',
  )

  // A view that changed deployment state cleans up even when its capture failed, so one
  // red view cannot turn every later capture red.
  // Playwright requires destructuring to reach testInfo, even with no fixtures.
  // eslint-disable-next-line no-empty-pattern
  test.afterEach(async ({}, testInfo) => {
    // lower() can outwait a hook's default 30 s budget; a hook killed mid-restore would
    // leave the policy raised and blame the next view. The budget is lower()'s own waits
    // plus margin.
    testInfo.setTimeout(120_000)
    const view = VIEWS.find((v) => testInfo.title === tituloDeVista(v.id))
    if (view) await view.limpia?.()
    // A renamed test would match no view above and leave the policy raised for the next one.
    expect(
      aal3Policy,
      'docs-captures: a view left the AAL3 policy raised',
    ).toBeUndefined()
  })

  // Capture both themes from one page load; toggle the theme without navigating again.
  for (const view of VIEWS) {
    {
      test(tituloDeVista(view.id), async ({ page, request }) => {
        // The census lists every page docs/editions.md places in Business (business_paths).
        if (census.business_paths.includes(view.path)) {
          const info = await request.get('/v1/server-info')
          expect(info.ok()).toBe(true)
          const edition = (await info.json()).edition
          expect(['community', 'business', 'enterprise']).toContain(edition)
          const reason = 'Business capability on a Community build'
          if (edition === 'community') {
            fs.mkdirSync('playwright-report/docs', { recursive: true })
            fs.writeFileSync(
              path.join(
                'playwright-report',
                'docs',
                `${view.id}.evidence.json`,
              ),
              JSON.stringify({ id: view.id, skipped: reason }),
            )
          }
          test.skip(edition === 'community', `${view.id}: ${reason}`)
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
        const login = page.waitForResponse(
          (response) =>
            response.request().method() === 'POST' &&
            new URL(response.url()).pathname === '/v1/auth/login',
        )
        await page.goto('/login')
        await page.locator('#email').fill(actor ? actor.email : DEMO_EMAIL)
        await page
          .locator('#password')
          .fill(actor ? actor.clave : DEMO_PASSWORD)
        await page.getByRole('button', { name: /^sign in$/i }).click()
        const loginResponse = await login
        expect(loginResponse.status()).toBe(200)

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
            // Use the theme control so store-driven charts update too: the account
            // menu's Theme submenu (#3161). Header-free views use the CSS-class fallback.
            const cuenta = page.getByRole('button', { name: /, Account$/ })
            if ((await cuenta.count()) > 0) {
              await cuenta.click()
              await page
                .getByRole('menuitemradio', { name: 'Dark', exact: true })
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
    test(`capture access-map-drift — ${theme}`, async ({ page }) => {
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

  // These scenes exercise session panels and workspace browsing.
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
