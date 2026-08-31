// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { overviewApi, workflowApi } = vi.hoisted(() => ({
  overviewApi: {
    schedules: vi.fn(),
    subscriptions: vi.fn(),
    routes: vi.fn(),
    eventTypes: vi.fn(),
    matchTypes: vi.fn(),
  },
  workflowApi: {
    list: vi.fn(),
    create: vi.fn(),
    detail: vi.fn(),
    patch: vi.fn(),
    updateSteps: vi.fn(),
    revisions: vi.fn(),
    restore: vi.fn(),
    dryRun: vi.fn(),
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
    schedules: vi.fn(),
    routes: vi.fn(),
  },
}))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  automationsApi: overviewApi,
}))
vi.mock('./workflows/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./workflows/api')>()),
  workflowsApi: workflowApi,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children?: ReactNode }) => <a href="#">{children}</a>,
}))

import { AutomationsView } from './automations-view'
import './i18n'
import './workflows/i18n'

const workflow = {
  id: 'workflow-1',
  name: 'Existing workflow',
  description: 'Existing composition control',
  enabled: true,
  version: 1,
  step_count: 0,
  steps: [],
  plan_hash: 'plan-1',
  owner_actor: 'admin',
  created_at: '2026-08-26T00:00:00Z',
  updated_at: '2026-08-26T00:00:00Z',
}

function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <AutomationsView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  overviewApi.schedules.mockResolvedValue({ items: [] })
  overviewApi.subscriptions.mockResolvedValue({ items: [] })
  overviewApi.routes.mockResolvedValue({ items: [] })
  overviewApi.eventTypes.mockResolvedValue({ event_types: [] })
  overviewApi.matchTypes.mockResolvedValue({ match_types: [] })
  workflowApi.list.mockResolvedValue({ items: [workflow], has_more: false })
  workflowApi.create.mockResolvedValue({
    ...workflow,
    id: 'workflow-created',
    name: 'Composition workflow',
  })
  workflowApi.detail.mockResolvedValue({
    ...workflow,
    id: 'workflow-created',
    name: 'Composition workflow',
  })
  workflowApi.schedules.mockResolvedValue({ items: [], has_more: false })
  workflowApi.routes.mockResolvedValue({ items: [], has_more: false })
  workflowApi.runs.mockResolvedValue({ items: [], has_more: false })
})

describe('AutomationsView composition', () => {
  it('mounts the workflow rail, creates through it, and opens the resulting editor', async () => {
    const user = userEvent.setup()
    show()

    const tab = screen.getByRole('tab', { name: /workflows/i })
    expect(
      tab,
      'Rendered: the real AutomationsView must expose the Workflows tab',
    ).toBeVisible()
    await user.click(tab)

    const createButton = await screen.findByRole('button', {
      name: /new workflow/i,
    })
    expect(
      createButton,
      'Rendered: the clicked parent tab must mount the real workflow create control',
    ).toBeEnabled()
    await user.click(createButton)

    const dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/^name/i),
      'Composition workflow',
    )
    await user.click(
      within(dialog).getByRole('button', { name: /create workflow/i }),
    )

    await waitFor(() =>
      expect(
        workflowApi.create,
        'Fired: the parent composition must dispatch workflowsApi.create',
      ).toHaveBeenCalledWith(
        { name: 'Composition workflow', description: undefined },
        expect.anything(),
      ),
    )
    expect(
      await screen.findByText(/back to workflows/i),
      'Effect: a successful create must move the real parent composition into the editor',
    ).toBeVisible()
    expect(
      screen.getByRole('heading', { name: 'Composition workflow' }),
      'Effect: the editor must paint the workflow returned by the create handler',
    ).toBeVisible()
  })
})

describe('P11 — un contador a cero no es una alarma', () => {
  /**
   * ⛔ EL DEFECTO, de la revisión de capturas del 2026-08-31: el chip «stalled»
   * llevaba `variant: 'danger'` INCONDICIONAL, así que «0 stalled» —que es la BUENA
   * noticia— se pintaba de rojo. Un contador en rojo dice «atiéndeme»; a cero no hay
   * nada que atender, y una alarma que suena sin causa entrena a ignorar las que sí
   * la tienen.
   *
   * EL MUTANTE: devolver el `variant: 'danger'` fijo. Falla aquí.
   */
  it('sin horarios estancados, el chip no se pinta como peligro', async () => {
    overviewApi.schedules.mockResolvedValue({ items: [] })
    show()
    const chip = await screen.findByText(/0 stalled/i)
    const clases = chip.className + ' ' + (chip.parentElement?.className ?? '')
    expect(
      /danger/.test(clases),
      `«0 stalled» se pinta como alarma: ${clases}`,
    ).toBe(false)
  })

  it('con uno estancado, SÍ se pinta como peligro', async () => {
    overviewApi.schedules.mockResolvedValue({
      items: [{ id: 's1', health: 'stalled' }],
    })
    show()
    const chip = await screen.findByText(/1 stalled/i)
    const clases = chip.className + ' ' + (chip.parentElement?.className ?? '')
    expect(
      /danger/.test(clases),
      'un horario estancado debe seguir avisando en rojo',
    ).toBe(true)
  })
})
