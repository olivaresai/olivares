// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ⌘K VERB OF THIS VIEW, AND WHY IT IS THE CLEAREST CASE OF THE THREE (A1, spec04 §1).
//
// The registry gates this page on `orchestration:graph:read` and its verb writes with
// `orchestration:schedule:write` — a DIFFERENT RESOURCE, not a stronger verb on the same
// one. So no rule that derived a write permission from the page's could ever have been
// right here: a principal who may read the graph and holds no schedule permission at all
// was still offered "New schedule", and landed on a tab where nothing happened.
//
// The view's own dialogs were already `canWrite`-gated; what changes is that the queued
// command is observed by the view ROOT, checked in the identity that queued it, and — if
// accepted — selects the tab that holds the form. Ordinary navigation still lands on the
// graph, which is why every refusal cell below asserts that tab is still the active one.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authState = vi.hoisted(() => ({
  held: new Set<string>(),
  activeTenant: 't1' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => authState.held.has(p),
    activeTenant: authState.activeTenant,
  }),
}))

const api = vi.hoisted(() => ({
  graph: vi.fn(),
  flows: vi.fn(),
  schedules: vi.fn(),
  scheduleDecisions: vi.fn(),
  decisions: vi.fn(),
  timeline: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    orchestrationApi: { ...actual.orchestrationApi, ...api },
  }
})

import '@/features/_intel'
import { queryKeys } from '@/lib/api/query'
import { liveCapabilityContext } from '@/lib/auth/capabilities'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { graphFixture, flowsFixture } from './fixtures'
import { OrchestrationView } from './orchestration-view'
import './i18n'

const EMPTY = { items: [], has_more: false }
const SCHEDULES = {
  items: [
    {
      id: 'sched-0007',
      name: 'nightly cleanup',
      subject_kind: 'agent' as const,
      subject_ref: 'cleanup-bot',
      trigger_kind: 'cron',
      trigger_expr: '0 0 * * *',
      status: 'active',
    },
  ],
  has_more: false,
}

const PRINCIPAL = {
  kind: 'user',
  user_id: 'u-1',
  actor: 'u-1',
  display_name: 'Ada',
  superadmin: false,
  grants: [],
}

function client() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, PRINCIPAL)
  return qc
}

/** Select "New schedule" in the palette, bound to the identity live in this client. */
function selectInPalette(qc: QueryClient) {
  useCommandStore
    .getState()
    .setPendingAction(
      'orchestration',
      'createSchedule',
      liveCapabilityContext(qc),
    )
  expect(useCommandStore.getState().pendingAction).not.toBeNull()
}

/**
 * Arrive, and nothing else.
 *
 * ⛔ THIS HELPER USED TO CLICK THE SCHEDULES TAB, and that click hid the defect the
 *    independent review found: the verb landed on the Graph tab, whose sibling holds the
 *    form, and the test clicked its way to the surface the operator never reached.
 *    Selecting the tab is now the VIEW's job when it accepts the command, so the cells
 *    below assert it instead of performing it.
 */
function arrive(qc: QueryClient) {
  const view = render(
    <QueryClientProvider client={qc}>
      <OrchestrationView />
    </QueryClientProvider>,
  )
  return Object.assign(userEvent.setup(), {
    /** Re-render with whatever `can` answers now — the auth stand-in is not reactive. */
    again: () =>
      view.rerender(
        <QueryClientProvider client={qc}>
          <OrchestrationView />
        </QueryClientProvider>,
      ),
  })
}

/** The tab that is showing right now. */
async function activeTab() {
  const tabs = await screen.findAllByRole('tab')
  return tabs.find((t) => t.getAttribute('data-state') === 'active')
    ?.textContent
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.held = new Set([
    'orchestration:graph:read',
    'orchestration:schedule:read',
    'orchestration:schedule:write',
  ])
  authState.activeTenant = 't1'
  useTenantStore.setState({ activeTenant: 't1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  useCommandStore.setState({ pendingAction: null, open: false, opener: null })
  api.graph.mockResolvedValue(graphFixture)
  api.flows.mockResolvedValue({ items: flowsFixture, has_more: false })
  api.timeline.mockResolvedValue(EMPTY)
  api.schedules.mockResolvedValue(SCHEDULES)
  api.scheduleDecisions.mockResolvedValue(EMPTY)
  api.decisions.mockResolvedValue(EMPTY)
})

describe('the "New schedule" palette verb', () => {
  it('selects the Schedules tab and opens the form for a principal who may write', async () => {
    const qc = client()
    selectInPalette(qc)
    arrive(qc)
    expect(
      await screen.findByRole('dialog', { name: /new schedule/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('acts on a verb chosen while the view is already on screen', async () => {
    // The correction's case: no remount happens when the palette navigates to the page
    // the operator is already on, so the view root must observe the command instead.
    const qc = client()
    arrive(qc)
    expect(
      await screen.findByRole('tab', { name: 'Schedules' }),
    ).toBeInTheDocument()
    expect(await activeTab()).toBe('Communication graph')

    await act(async () => {
      selectInPalette(qc)
    })

    expect(
      await screen.findByRole('dialog', { name: /new schedule/i }),
    ).toBeInTheDocument()
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('opens nothing for a principal who may only read the graph', async () => {
    // THE EXACT PRINCIPAL OF THE DEFECT: the page permission, and no schedule permission
    // of any tier. The old palette offered them this verb.
    authState.held = new Set(['orchestration:graph:read'])
    const qc = client()
    selectInPalette(qc)
    arrive(qc)
    expect(
      await screen.findByRole('tab', { name: 'Schedules' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // The refusal does not move the operator either.
    expect(await activeTab()).toBe('Communication graph')
    // FIRES IF: a refused command is left in the store for the next arrival.
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('opens nothing after a credential refresh, and retires the command', async () => {
    const qc = client()
    selectInPalette(qc)
    // A REAL movement of the live generation, not an edited copy of the queued context:
    // a refresh keeps the display identity and still produces a new authority.
    useSessionStore.setState({ credentialGeneration: 1 })
    arrive(qc)
    expect(
      await screen.findByRole('tab', { name: 'Schedules' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(await activeTab()).toBe('Communication graph')
    expect(useCommandStore.getState().pendingAction).toBeNull()
  })

  it('closes the create dialog when the write grant goes, and does not reopen it', async () => {
    const qc = client()
    selectInPalette(qc)
    const user = arrive(qc)
    expect(
      await screen.findByRole('dialog', { name: /new schedule/i }),
    ).toBeInTheDocument()

    authState.held.delete('orchestration:schedule:write')
    user.again()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )

    // FIRES IF: the dialog is only hidden behind the render gate. The grant coming back
    // would reopen the form with no operator intent anywhere near it.
    authState.held.add('orchestration:schedule:write')
    user.again()
    expect(
      await screen.findByRole('tab', { name: 'Schedules' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('opens nothing when no verb was queued at all', async () => {
    const qc = client()
    const user = arrive(qc)
    expect(await activeTab()).toBe('Communication graph')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // CONTROL: the operator's own route to the same form still works, which is what
    // "preserve every existing in-view workflow" has to mean.
    await user.click(await screen.findByRole('tab', { name: 'Schedules' }))
    await user.click(
      await screen.findByRole('button', { name: /new schedule/i }),
    )
    expect(
      await screen.findByRole('dialog', { name: /new schedule/i }),
    ).toBeInTheDocument()
  })
})
