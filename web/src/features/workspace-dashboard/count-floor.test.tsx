// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fichero PROPIO a propósito: `#1622` edita `workspace-dashboard-view.test.tsx` y no compito con
// él por las mismas líneas. Lo que se pincha aquí es la mitad de PANTALLA del contrato de `#1647`.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { cuentaConSuelo } from './count-floor'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))
const workspaceState = {
  activeWorkspace: 'w1' as string | null,
  activeWorkspaceName: 'Engineering' as string | null,
}
vi.mock('@/stores/workspace', () => ({
  useWorkspaceStore: () => workspaceState,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1' }),
}))

const summaryMock = vi.fn()
const agentsMock = vi.fn()
const groupsMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    workspaceDashboardApi: {
      summary: (...a: unknown[]) => summaryMock(...a),
      agents: (...a: unknown[]) => agentsMock(...a),
      groups: (...a: unknown[]) => groupsMock(...a),
    },
  }
})
const { WorkspaceDashboardView } = await import('./workspace-dashboard-view')

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <WorkspaceDashboardView />
    </QueryClientProvider>,
  )
}
function resumen(extra: Record<string, unknown>) {
  summaryMock.mockReset().mockResolvedValue({
    id: 'w1',
    name: 'Engineering',
    slug: 'eng',
    is_default: true,
    agent_count: 1000,
    session_count: 3,
    resource_count: 11,
    group_count: 2,
    ...extra,
  })
}
beforeEach(() => {
  workspaceState.activeWorkspace = 'w1'
  workspaceState.activeWorkspaceName = 'Engineering'
  resumen({})
  agentsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
  groupsMock.mockReset().mockResolvedValue({ items: [], has_more: false })
})

describe('cuentaConSuelo — la regla, en las dos direcciones', () => {
  const f = (n: number) => String(n)
  it('con capped true dice que es un SUELO', () => {
    expect(cuentaConSuelo(1000, true, f)).toBe('≥ 1000')
  })
  it('con capped false pinta el número EXACTO', () => {
    expect(cuentaConSuelo(1000, false, f)).toBe('1000')
  })
  it('sin el campo (motor anterior a #1647) NO se inventa un suelo', () => {
    expect(cuentaConSuelo(1000, undefined, f)).toBe('1000')
  })
})

describe('WorkspaceDashboardView · el recuento saturado se declara', () => {
  /**
   * EL CONTROL: con `agent_count_capped: true` la pantalla tiene que decir «≥».
   *
   * LA MUTACIÓN que esto caza: que la vista pinte `formatInt(s.agent_count)` a secas. La tarjeta
   * seguiría enseñando «1.000» —con pinta de exacto— sobre un total que en realidad es un suelo,
   * que es justo el defecto que `#1647` arregla en el motor.
   *
   * LA DIRECCIÓN QUE NO DISPARA es el caso siguiente: sin el campo NO puede aparecer el «≥», así
   * que una vista que lo pintara siempre satisface a éste y falla aquél.
   */
  it('con agent_count_capped declara el suelo', async () => {
    resumen({ agent_count_capped: true })
    show()
    await waitFor(() => expect(summaryMock).toHaveBeenCalled())
    expect(await screen.findAllByText(/≥/)).not.toHaveLength(0)
  })

  it('sin el campo NO aparece ningún «≥» inventado', async () => {
    resumen({})
    show()
    await waitFor(() => expect(summaryMock).toHaveBeenCalled())
    await screen.findAllByText(/Engineering/)
    expect(screen.queryAllByText(/≥/)).toHaveLength(0)
  })
})
