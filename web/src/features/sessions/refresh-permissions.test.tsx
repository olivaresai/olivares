// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'
import './i18n'

/**
 * C-11 — manual reads respect every read permission.
 *
 * Both automatic queries already used `enabled: canLiveRead` / `canRunRead`. The button
 * called both `refetch()` functions unconditionally. Refetch explicitly requests a read
 * the operator may lack, producing a 403 the screen does not display. The enabled guard
 * protects initial loading, not this separate path.
 *
 * The page no longer has a Refresh button because the list updates live. The remaining
 * explicit request is the table error's Retry action, which calls the same function.
 * These cases exercise that action and still measure the permission guard.
 *
 * Test each permission combination. A no-permissions-only case could pass an implementation
 * that refreshes both queries or neither. Each case grants one read and checks that only
 * that query refreshes.
 */

const { api, agentOps, authState } = vi.hoisted(() => ({
  api: { live: vi.fn() },
  agentOps: { listRuns: vi.fn() },
  authState: {
    activeTenant: 'tenant-a' as string | null,
    can: ((_: string) => false) as (p: string) => boolean,
  },
}))

// The view's selection is the URL now, so it reads the location on every render.
// Without a RouterProvider the real hook throws; this is the same in-memory location
// double the other two view tests mount, and it keeps this file about the refresh
// button and nothing else.
vi.mock('@tanstack/react-router', async () => {
  const { fakeRouterModule } = await import('@/test/fake-router')
  return fakeRouterModule()
})

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/sessions/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: api,
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: agentOps,
}))

const { SessionsWorkspaceView } = await import('./sessions-workspace-view')

function conPermisos(...permisos: string[]) {
  const set = new Set(permisos)
  authState.can = (p: string) => set.has(p)
}

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      {/* Sin props: el componente saca el workspace de useWorkspaceFilter (:115), no de fuera.
          Mi primera version le pasaba workspaceId="ws-1" y el componente lo IGNORABA — un prop
          inventado que el test daba por efectivo. */}
      <SessionsWorkspaceView />
    </QueryClientProvider>,
  )
}

/**
 * When permitted reads fail, the table's Retry action is the only explicit retry offered.
 */
async function tablaConError() {
  const trigger = await screen.findByTestId('sessions-list-menu')
  act(() => {
    fireEvent.keyDown(trigger, { key: 'Enter' })
  })
  act(() => {
    fireEvent.click(screen.getByRole('menuitem', { name: 'Show as table' }))
  })
  return screen.findByRole('button', { name: /retry/i })
}

beforeEach(() => {
  vi.clearAllMocks()
  fakeRouter.reset('/sessions')
  api.live.mockResolvedValue({ items: [], has_more: false })
  agentOps.listRuns.mockResolvedValue({ items: [], has_more: false })
  authState.can = () => false
})

describe('C-11 · la lectura manual respeta cada permiso de lectura', () => {
  it('renders no Refresh button, with or without permissions', async () => {
    conPermisos()
    montar()
    expect(screen.queryByRole('button', { name: /refresh/i })).toBeNull()
    expect(screen.queryByText('Live')).toBeNull()
  })

  it('con `live:read` vuelve a pedir SOLO live, nunca runs', async () => {
    conPermisos('sessions:live:read')
    api.live.mockRejectedValue(new Error('boom'))
    montar()
    const retry = await tablaConError()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(retry)
    expect(api.live.mock.calls.length).toBeGreaterThan(liveAntes)
    expect(agentOps.listRuns.mock.calls.length).toBe(runsAntes)
  })

  it('con `run:read` vuelve a pedir SOLO runs, nunca live', async () => {
    conPermisos('sessions:run:read')
    agentOps.listRuns.mockRejectedValue(new Error('boom'))
    montar()
    const retry = await tablaConError()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(retry)
    expect(agentOps.listRuns.mock.calls.length).toBeGreaterThan(runsAntes)
    expect(api.live.mock.calls.length).toBe(liveAntes)
  })

  it('CONTROL: con las dos lecturas vuelve a pedir las dos', async () => {
    // Calibra los dos de arriba: si el reintento no llamara a nadie, «no llama a la otra»
    // pasaria por la razon equivocada.
    conPermisos('sessions:live:read', 'sessions:run:read')
    api.live.mockRejectedValue(new Error('boom'))
    agentOps.listRuns.mockRejectedValue(new Error('boom'))
    montar()
    const retry = await tablaConError()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(retry)
    expect(api.live.mock.calls.length).toBeGreaterThan(liveAntes)
    expect(agentOps.listRuns.mock.calls.length).toBeGreaterThan(runsAntes)
  })
})
