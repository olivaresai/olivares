// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
//
// El veredicto por actor: lo que el MOTOR decide para un par (actor, fuente).
//
// ⛔ LA CELDA QUE MANDA ES LA DE `baseline`. El motor corre el resolvedor con un principal a
// cero y devuelve `baseline: true` «so the console can label it honestly». Una pantalla de
// autorización que pinte ese veredicto sin decir que es la LÍNEA BASE afirma de más: el
// actor real puede tener más permiso del que se ve. Por eso la ausencia del aviso es un
// fallo, no un detalle de estilo.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, navigate } = vi.hoisted(() => ({
  api: {
    listBindings: vi.fn(),
    listPostureRequests: vi.fn(),
    listGuardPostures: vi.fn(),
    listWorkspaces: vi.fn(),
    listAgentGroups: vi.fn(),
    listGroups: vi.fn(),
    rbacCatalog: vi.fn(),
    listRoles: vi.fn(),
    listAgents: vi.fn(),
    listResources: vi.fn(),
    resolvePreview: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
  navigate: vi.fn(),
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { BindingsTab } from './bindings-tab'

const emptyList = { items: [], has_more: false }

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.listBindings.mockResolvedValue(emptyList)
  api.listPostureRequests.mockResolvedValue(emptyList)
  api.listGuardPostures.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue(emptyList)
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue(emptyList)
  api.listRoles.mockResolvedValue(emptyList)
  api.listAgents.mockResolvedValue(emptyList)
  api.listResources.mockResolvedValue(emptyList)
  api.rbacCatalog.mockResolvedValue({ permissions: [], roles: [] })
})

async function abrirFuente(user: ReturnType<typeof userEvent.setup>) {
  wrap(<BindingsTab />)
  await user.type(screen.getByLabelText(/source reference/i), 'kb-prod')
  await screen.findByText(/resolution preview/i)
}

describe('el veredicto por actor', () => {
  it('no le pregunta al motor hasta que hay referencia de actor', async () => {
    const user = userEvent.setup()
    await abrirFuente(user)
    // FIRES IF: alguien quita el `enabled` de la consulta y la dispara vacía —
    // el motor contesta 400 «actor_ref is required» y el operador ve un error suyo.
    expect(api.resolvePreview).not.toHaveBeenCalled()
  })

  it('manda los cuatro parámetros que el motor exige', async () => {
    const user = userEvent.setup()
    api.resolvePreview.mockResolvedValue({
      allowed: true,
      reason: 'bound by workspace',
      bound: true,
      baseline: true,
    })
    await abrirFuente(user)
    await user.type(screen.getByLabelText(/actor reference/i), 'agt-7')
    await waitFor(() => expect(api.resolvePreview).toHaveBeenCalled())
    expect(api.resolvePreview).toHaveBeenCalledWith({
      source_type: 'mcp',
      source_ref: 'kb-prod',
      actor_kind: 'agent',
      actor_ref: 'agt-7',
    })
  })

  it('pinta el veredicto y su razón', async () => {
    const user = userEvent.setup()
    api.resolvePreview.mockResolvedValue({
      allowed: false,
      reason: 'forbidden by folder binding',
      bound: false,
      baseline: true,
    })
    await abrirFuente(user)
    await user.type(screen.getByLabelText(/actor reference/i), 'agt-7')
    expect(
      await screen.findByText(/forbidden by folder binding/i),
    ).not.toBeNull()
    expect(await screen.findByText(/^denied$/i)).not.toBeNull()
  })

  it('⛔ DICE que es LÍNEA BASE cuando el motor lo dice', async () => {
    const user = userEvent.setup()
    api.resolvePreview.mockResolvedValue({
      allowed: true,
      reason: 'bound by workspace',
      bound: true,
      baseline: true,
    })
    await abrirFuente(user)
    await user.type(screen.getByLabelText(/actor reference/i), 'agt-7')
    const nota = await screen.findByRole('note')
    // FIRES IF: alguien pinta el veredicto sin el aviso — la pantalla diría «permitido»
    // sobre un actor cuyas propias concesiones NO se han simulado.
    expect(nota.textContent ?? '').toMatch(/baseline/i)
  })

  it('y NO lo dice cuando el motor no lo marca', async () => {
    const user = userEvent.setup()
    api.resolvePreview.mockResolvedValue({
      allowed: true,
      reason: 'bound by workspace',
      bound: true,
      baseline: false,
    })
    await abrirFuente(user)
    await user.type(screen.getByLabelText(/actor reference/i), 'agt-7')
    await screen.findByText(/bound by workspace/i)
    // El aviso es CONDICIONAL, no decorativo: si se pintara siempre, esta celda lo caza.
    expect(screen.queryByRole('note')).toBeNull()
  })
})
