// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, authState } = vi.hoisted(() => ({
  api: {
    detail: vi.fn(),
    schedules: vi.fn(),
    routes: vi.fn(),
    updateSteps: vi.fn(),
    patch: vi.fn(),
    revisions: vi.fn(),
    restore: vi.fn(),
    dryRun: vi.fn(),
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
  },
  authState: {
    activeTenant: 'tenant-a' as string | null,
    can: (_permission: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  workflowsApi: api,
}))

import { WorkflowEditor } from './editor'

const detail = {
  id: 'workflow-1',
  name: 'Graph editor',
  enabled: true,
  version: 1,
  step_count: 2,
  plan_hash: 'hash-1',
  owner_actor: 'admin',
  created_at: '2026-07-01T00:00:00Z',
  steps: [
    {
      ref: 'alpha',
      kind: 'eventing-emit' as const,
      config: { label: 'start' },
      depends_on: [],
    },
    {
      ref: 'beta',
      kind: 'wait' as const,
      config: { seconds: 5 },
      depends_on: [],
    },
  ],
}

function renderEditor() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkflowEditor
        workflowId="workflow-1"
        canWrite
        canAdmin
        onBack={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.detail.mockResolvedValue(detail)
  api.schedules.mockResolvedValue({ items: [], has_more: false })
  api.routes.mockResolvedValue({ items: [], has_more: false })
  api.runs.mockResolvedValue({ items: [], has_more: false })
  api.updateSteps.mockImplementation(
    async (_id: string, steps: typeof detail.steps) => ({
      ...detail,
      version: 2,
      steps,
    }),
  )
})

describe('WorkflowEditor keyboard list', () => {
  it('propagates dependency edits and saves the complete graph', async () => {
    const user = userEvent.setup()
    renderEditor()
    await user.click(await screen.findByRole('button', { name: 'List' }))
    await user.click(
      screen.getByRole('checkbox', { name: 'Make beta depend on alpha' }),
    )
    expect(
      screen.getByRole('checkbox', { name: 'Make beta depend on alpha' }),
    ).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save graph' }))
    await waitFor(() =>
      expect(api.updateSteps).toHaveBeenCalledWith('workflow-1', [
        detail.steps[0],
        { ...detail.steps[1], depends_on: ['alpha'] },
      ]),
    )
  })

  it('marks a live cycle and disables saving', async () => {
    const user = userEvent.setup()
    renderEditor()
    await user.click(await screen.findByRole('button', { name: 'List' }))
    await user.click(
      screen.getByRole('checkbox', { name: 'Make alpha depend on beta' }),
    )
    await user.click(
      screen.getByRole('checkbox', { name: 'Make beta depend on alpha' }),
    )
    expect(
      await screen.findAllByText('This node is part of a dependency cycle.'),
    ).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Save graph' })).toBeDisabled()
  })
})

describe('WorkflowEditor — los selectores dicen cuándo están incompletos', () => {
  // ⛔ Testigo de VISTA MONTADA. El de transporte (`api.test.ts`) prueba que el método manda el
  // techo; éste prueba que el aviso es ALCANZABLE desde el editor. Aquí el recorte no es una cifra
  // mal: es un selector al que le faltan opciones que existen, y el operador no puede elegir lo
  // que no ve.
  it('con has_more en schedules, el aviso SALE', async () => {
    api.schedules.mockResolvedValue({ items: [], has_more: true })
    renderEditor()
    expect(
      await screen.findByText(/schedules are not listed|Faltan horarios/i),
    ).toBeVisible()
  })

  it('con has_more en routes, el aviso SALE', async () => {
    api.routes.mockResolvedValue({ items: [], has_more: true })
    renderEditor()
    expect(
      await screen.findByText(/routes are not listed|Faltan rutas/i),
    ).toBeVisible()
  })

  it('sin has_more no sale ninguno: un aviso que sale siempre no declara nada', async () => {
    renderEditor()
    await waitFor(() => expect(api.schedules).toHaveBeenCalled())
    expect(
      screen.queryByText(/are not listed|Faltan (horarios|rutas)/i),
    ).toBeNull()
  })
})
