// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

/**
 * C-11 — el refresh manual respeta CADA permiso de lectura, y desaparece sin fuente legible.
 *
 * ⛔ EL DEFECTO. Las dos consultas ya estaban guardadas con `enabled: canLiveRead` / `canRunRead`,
 *    así que la carga automática respetaba el permiso. **El botón no**: llamaba a `refetch()` de
 *    las dos sin mirar nada. Y `refetch` es una ORDEN EXPLÍCITA — pide al motor una lectura que
 *    este operador no tiene derecho a hacer, y la respuesta es un 403 que la pantalla no enseña.
 *    La guarda de `enabled` protege la carga inicial y NO ésta: son dos caminos, y sólo uno estaba
 *    cubierto.
 *
 * ⛔ Y EL BOTÓN SE PINTABA SIEMPRE, incluso sin ninguna fuente legible: un control que no puede
 *    hacer nada, ofrecido a quien no puede usarlo. Ahora desaparece.
 *
 * ⛔ POR QUÉ UN CASO POR COMBINACIÓN. Con un solo caso de «sin permisos», una implementación que
 *    refrescara SIEMPRE las dos —o NINGUNA— pasaría. Cada caso concede una lectura y comprueba que
 *    se refresca ESA y no la otra.
 */

const { api, agentOps, authState } = vi.hoisted(() => ({
  api: { live: vi.fn() },
  agentOps: { listRuns: vi.fn() },
  authState: {
    activeTenant: 'tenant-a' as string | null,
    can: ((_: string) => false) as (p: string) => boolean,
  },
}))

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

function botonRefresh() {
  return screen
    .queryAllByRole('button')
    .find((b) => /refresh|actualizar|refrescar/i.test(b.textContent ?? ''))
}

beforeEach(() => {
  vi.clearAllMocks()
  api.live.mockResolvedValue({ items: [], has_more: false })
  agentOps.listRuns.mockResolvedValue({ items: [], has_more: false })
  authState.can = () => false
})

describe('C-11 · el refresh respeta cada permiso de lectura', () => {
  it('SIN ninguna lectura, el boton NO se pinta', () => {
    conPermisos()
    montar()
    expect(botonRefresh()).toBeUndefined()
  })

  it('con `live:read` refresca SOLO live, nunca runs', async () => {
    conPermisos('sessions:live:read')
    montar()
    const b = botonRefresh()
    expect(b, 'con una fuente legible el boton debe existir').toBeTruthy()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(b!)
    expect(api.live.mock.calls.length).toBeGreaterThan(liveAntes)
    expect(agentOps.listRuns.mock.calls.length).toBe(runsAntes)
  })

  it('con `run:read` refresca SOLO runs, nunca live', async () => {
    conPermisos('sessions:run:read')
    montar()
    const b = botonRefresh()
    expect(b).toBeTruthy()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(b!)
    expect(agentOps.listRuns.mock.calls.length).toBeGreaterThan(runsAntes)
    expect(api.live.mock.calls.length).toBe(liveAntes)
  })

  it('CONTROL: con las dos lecturas refresca las dos', async () => {
    // Calibra los dos de arriba: si el refrescador no llamara a nadie, «no llama a la otra»
    // pasaria por la razon equivocada.
    conPermisos('sessions:live:read', 'sessions:run:read')
    montar()
    const b = botonRefresh()
    expect(b).toBeTruthy()
    const runsAntes = agentOps.listRuns.mock.calls.length
    const liveAntes = api.live.mock.calls.length
    await userEvent.click(b!)
    expect(api.live.mock.calls.length).toBeGreaterThan(liveAntes)
    expect(agentOps.listRuns.mock.calls.length).toBeGreaterThan(runsAntes)
  })
})
