// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { toastMock } = vi.hoisted(() => ({
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))
vi.mock('@/components/ui/toaster', () => ({ toast: toastMock }))

const { api, authState } = vi.hoisted(() => ({
  api: {
    listRoutes: vi.fn(),
    getRoute: vi.fn(),
    createRoute: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn(),
    testRoute: vi.fn(),
    routeRevisions: vi.fn(),
    restoreRoute: vi.fn(),
    listDestinations: vi.fn(),
    listMatchTypes: vi.fn(),
    evaluateRoutes: vi.fn(),
    listDeliveries: vi.fn(),
    listOutbox: vi.fn(),
    redeliverOutbox: vi.fn(),
  },
  authState: {
    can: (_p: string): boolean => true,
    activeTenant: 't1' as string | null,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  notifyApi: api,
}))

// AlertingView mounts RecordingNotice (AC-8). Stub only its data seam so the real
// component still renders — a mocked-away component could be deleted from the view
// without a single cell noticing, which is the class of gap this suite exists to close.
const { recordingApiMock } = vi.hoisted(() => ({
  recordingApiMock: { notice: vi.fn(), acknowledge: vi.fn() },
}))
vi.mock('@/features/recordings/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/recordings/api')>()),
  recordingApi: recordingApiMock,
}))

import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { AlertingView } from './alerting-view'

const route = {
  id: 'r1',
  name: 'sec-alerts',
  destination: 'slack-sec',
  enabled: true,
  min_severity: 'high',
}

/** One dead-lettered outbox row, shaped like notify's outboxDTO
 * (modules/notify/outbox_api.go:19-34). */
const deadRow = {
  id: 'ob-1',
  status: 'dead',
  attempts: 3,
  destination: 'slack-sec',
  event_type: 'finding.reported',
  finding_kind: 'security_guardrail',
  severity: 'high',
  last_detail: 'destination down',
  occurred_at: '2026-08-11T09:00:00Z',
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
    qc,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listRoutes.mockResolvedValue({ items: [route], has_more: false })
  api.listDestinations.mockResolvedValue({
    destinations: ['slack-sec', 'pagerduty'],
  })
  api.listMatchTypes.mockResolvedValue({
    match_types: [
      { type: 'finding.reported', description: 'A finding was reported' },
    ],
  })
  api.evaluateRoutes.mockResolvedValue({ items: [], matched_count: 0 })
  api.listDeliveries.mockResolvedValue({
    items: [
      {
        id: 'd1',
        destination: 'slack-sec',
        event_type: 'finding',
        finding_kind: 'anomaly',
        status: 'delivered',
        occurred_at: '2026-07-09T00:00:00Z',
      },
    ],
    has_more: false,
  })
  api.createRoute.mockResolvedValue(route)
  api.getRoute.mockResolvedValue(route)
  api.updateRoute.mockResolvedValue(route)
  api.deleteRoute.mockResolvedValue(undefined)
  api.testRoute.mockResolvedValue({ destination: 'slack-sec', status: 'ok' })
  api.routeRevisions.mockResolvedValue({ items: [], has_more: false })
  api.restoreRoute.mockResolvedValue(route)
  api.listOutbox.mockResolvedValue({ items: [deadRow], has_more: false })
  api.redeliverOutbox.mockResolvedValue({ id: 'ob-1', status: 'queued' })
  recordingApiMock.notice.mockResolvedValue({
    recorded_namespaces: [],
    consent_required: false,
  })
  recordingApiMock.acknowledge.mockResolvedValue({ acknowledged: true })
})

describe('AlertingView', () => {
  it('forbids without the route read permission', () => {
    authState.can = () => false
    wrap(<AlertingView />)
    expect(api.listRoutes).not.toHaveBeenCalled()
  })

  it('keeps route History available with read permission alone', async () => {
    authState.can = (permission) => permission === 'notify:route:read'
    const user = userEvent.setup()
    wrap(<AlertingView />)

    await user.click(
      await screen.findByRole('button', {
        name: /view revision history for sec-alerts/i,
      }),
    )

    await waitFor(() =>
      expect(api.routeRevisions).toHaveBeenCalledWith('r1', {
        cursor: undefined,
        limit: 50,
      }),
    )
    expect(
      await screen.findByRole('dialog', {
        name: /revision history — sec-alerts/i,
      }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /restore this revision/i }),
    ).not.toBeInTheDocument()
  })

  it('creates a route with the selected destination', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)

    await user.click(await screen.findByRole('button', { name: /new route/i }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'ops-alerts')
    await user.click(
      within(dialog).getByRole('combobox', { name: /destination/i }),
    )
    await user.click(screen.getByRole('option', { name: 'pagerduty' }))
    await user.click(within(dialog).getByText('finding.reported'))
    await user.click(
      within(dialog).getByRole('button', { name: /create route/i }),
    )

    await waitFor(() =>
      expect(api.createRoute).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'ops-alerts',
          destination: 'pagerduty',
          enabled: true,
          match_types: ['finding.reported'],
        }),
      ),
    )
  })

  it('keeps the immutable route name locked while editing mutable fields', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)

    await user.click(
      await screen.findByRole('button', { name: /edit route sec-alerts/i }),
    )
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText(/^name/i)).toBeDisabled()
    const priority = within(dialog).getByLabelText(/^priority/i)
    await user.clear(priority)
    await user.type(priority, '8')
    await user.click(
      within(dialog).getByRole('button', { name: /save route/i }),
    )

    await waitFor(() =>
      expect(api.updateRoute).toHaveBeenCalledWith(
        'r1',
        expect.objectContaining({ name: 'sec-alerts', priority: 8 }),
      ),
    )
  })

  it('keeps the live route catalog dialog inside a laptop viewport', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)

    await user.click(await screen.findByRole('button', { name: /new route/i }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveClass('max-h-[calc(100vh-2rem)]', 'overflow-y-auto')
  })

  it('fires a route test', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(
      await screen.findByRole('button', { name: /test route sec-alerts/i }),
    )
    await waitFor(() => expect(api.testRoute).toHaveBeenCalledWith('r1'))
  })

  // A route test the DESTINATION refused is not a success, even though the request
  // itself completed. Reporting it with a success toast told an operator their
  // route worked while the destination had rejected the payload — the one thing a
  // test button exists to reveal.
  it('reports a refused route test as a failure, not a success', async () => {
    api.testRoute.mockResolvedValue({
      destination: 'slack-sec',
      status: 'rejected',
      detail: 'rejected',
    })
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(
      await screen.findByRole('button', { name: /test route sec-alerts/i }),
    )
    await waitFor(() => expect(toastMock.error).toHaveBeenCalled())
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('reports a delivered route test as a success', async () => {
    api.testRoute.mockResolvedValue({
      destination: 'slack-sec',
      status: 'delivered',
    })
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(
      await screen.findByRole('button', { name: /test route sec-alerts/i }),
    )
    await waitFor(() => expect(toastMock.success).toHaveBeenCalled())
  })

  it('selects routes, updates each current item and invalidates the list', async () => {
    const second = {
      ...route,
      id: 'r2',
      name: 'ops-alerts',
      destination: 'pagerduty',
      match_types: ['finding.reported'],
      throttle_window_seconds: 30,
    }
    api.listRoutes.mockResolvedValue({
      items: [route, second],
      has_more: false,
    })
    api.getRoute.mockImplementation(async (id: string) =>
      id === 'r1' ? route : second,
    )
    api.updateRoute.mockImplementation(
      async (id: string, body: typeof route) => ({ ...body, id }),
    )
    const user = userEvent.setup()
    const { qc } = wrap(<AlertingView />)
    const invalidate = vi.spyOn(qc, 'invalidateQueries')

    await user.click(
      await screen.findByRole('checkbox', {
        name: /select all visible rows/i,
      }),
    )
    await user.click(screen.getByRole('button', { name: /disable selected/i }))

    await waitFor(() => expect(api.updateRoute).toHaveBeenCalledTimes(2))
    expect(api.getRoute.mock.calls.map(([id]) => id)).toEqual(['r1', 'r2'])
    expect(api.updateRoute).toHaveBeenNthCalledWith(
      1,
      'r1',
      expect.objectContaining({
        name: 'sec-alerts',
        destination: 'slack-sec',
        enabled: false,
        min_severity: 'high',
      }),
    )
    expect(api.updateRoute).toHaveBeenNthCalledWith(
      2,
      'r2',
      expect.objectContaining({
        name: 'ops-alerts',
        destination: 'pagerduty',
        enabled: false,
        match_types: ['finding.reported'],
        throttle_window_seconds: 30,
      }),
    )
    expect(invalidate).toHaveBeenCalledTimes(2)
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ['notify', 't1', 'routes'],
    })
  })

  it('deletes a route after confirm', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(
      await screen.findByRole('button', { name: /delete route sec-alerts/i }),
    )
    const confirm = await screen.findByRole('dialog')
    expect(api.deleteRoute).not.toHaveBeenCalled()
    await user.click(
      within(confirm).getByRole('button', { name: /delete route/i }),
    )
    await waitFor(() => expect(api.deleteRoute).toHaveBeenCalledWith('r1'))
  })

  it('lists deliveries and filters by status', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /deliveries/i }))
    // The initial deliveries load has no status filter.
    await waitFor(() =>
      expect(api.listDeliveries).toHaveBeenCalledWith(undefined),
    )
    await screen.findByText('anomaly')

    await user.click(
      screen.getByRole('combobox', { name: /filter by status/i }),
    )
    await user.click(await screen.findByRole('option', { name: /failed/i }))
    await waitFor(() =>
      expect(api.listDeliveries).toHaveBeenCalledWith({ status: 'failed' }),
    )
  })

  //E3c: the delivery ledger paginates on the notify cursor.
  it('walks the delivery cursor with Load more', async () => {
    api.listDeliveries
      .mockResolvedValueOnce({
        items: [
          {
            id: 'd1',
            destination: 'slack-sec',
            event_type: 'finding',
            finding_kind: 'anomaly',
            status: 'delivered',
            occurred_at: '2026-07-09T00:00:00Z',
          },
        ],
        cursor: 'c1',
        has_more: true,
      })
      .mockResolvedValueOnce({
        items: [
          {
            id: 'd2',
            destination: 'pagerduty',
            event_type: 'finding',
            finding_kind: 'drift',
            status: 'delivered',
            occurred_at: '2026-07-09T00:01:00Z',
          },
        ],
        has_more: false,
      })

    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /deliveries/i }))
    await screen.findByText('anomaly')

    await user.click(await screen.findByRole('button', { name: /load more/i }))
    await waitFor(() =>
      expect(api.listDeliveries).toHaveBeenCalledWith(
        expect.objectContaining({ cursor: 'c1' }),
      ),
    )
    // Accumulated, not replaced — and the button disappears at the end.
    expect(await screen.findByText('drift')).toBeInTheDocument()
    expect(screen.getByText('anomaly')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /load more/i }),
    ).not.toBeInTheDocument()
  })
})

// --- the dead-letter queue ---------------------------------------------

describe('AlertingView · dead letters', () => {
  // THE WIRING CELL, and it starts at the PARENT on purpose. Measured two days ago on
  // this repository: deleting the tab-URL write in two views produced mutants that
  // COMPILED and left 164 files and 1644 tests green, because every suite mounted the
  // child directly and mocked its way past the seam. A tab nobody can reach is exactly
  // the defect this session came to fix, so the assertion is: mount AlertingView, press
  // the tab a human presses, and check the engine was actually asked.
  it('reaches the DLQ from the parent view and asks the engine for dead letters', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)

    expect(api.listOutbox).not.toHaveBeenCalled()
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    await waitFor(() =>
      expect(api.listOutbox).toHaveBeenCalledWith({ status: 'dead' }),
    )
    expect(await screen.findByText('security_guardrail')).toBeInTheDocument()
    expect(screen.getByText('destination down')).toBeInTheDocument()
  })

  it('requeues after confirming, and reports it as QUEUED — never as delivered', async () => {
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    // ConfirmDialog first: a requeue re-triggers an EXTERNAL delivery, so it is never
    // one click (modules/notify/api.go:44-45 puts it at admin tier for this reason).
    const dialog = await screen.findByRole('dialog')
    expect(api.redeliverOutbox).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole('button', { name: /^requeue notification$/i }),
    )

    await waitFor(() =>
      expect(api.redeliverOutbox).toHaveBeenCalledWith('ob-1'),
    )
    await waitFor(() => expect(toastMock.success).toHaveBeenCalled())
    // The engine answered {"status":"queued"} (modules/notify/outbox_api.go:141): the
    // next pump makes the attempt. Saying "delivered" would report an outcome the
    // engine has not reached — and that is the whole point of the third answer.
    const title = String(toastMock.success.mock.calls[0][0])
    expect(title).toMatch(/requeued/i)
    expect(title).not.toMatch(/delivered/i)
  })

  it('does not offer the action for a row that is still in flight', async () => {
    api.listOutbox.mockResolvedValue({
      items: [{ ...deadRow, status: 'delivering', last_detail: '' }],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    expect(await screen.findByText('security_guardrail')).toBeInTheDocument()
    // The engine refuses a non-terminal row with 409 (outbox_api.go:111-117), so the
    // console does not offer what the engine will not do.
    expect(
      screen.queryByRole('button', { name: /requeue this notification/i }),
    ).not.toBeInTheDocument()
  })

  it('hides the requeue from a reader who lacks the route ADMIN permission', async () => {
    // The list is delivery:read; the requeue is route:admin (modules/notify/api.go:46-47).
    // An operator holding only the reads sees the queue and no button.
    //
    // REDUNDANCY, DECLARED: the cell below subsumes this one for mutation purposes,
    // because a reader holds neither write nor admin and so cannot tell them apart.
    // This one is kept as the plain reader scenario; the load-bearing one is the next.
    authState.can = (permission) =>
      permission === 'notify:route:read' ||
      permission === 'notify:delivery:read'
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    expect(await screen.findByText('security_guardrail')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /requeue this notification/i }),
    ).not.toBeInTheDocument()
  })

  // MEASURED, not anticipated: re-gating the requeue on notify:route:write instead of
  // notify:route:admin was a mutant that COMPILED and survived every other cell in this
  // suite, because no test granted an editor. An editor is the one principal that can
  // tell the two permissions apart, so without this cell the console could offer a
  // button the engine answers with 403 — the "not authorized" toast an operator reads
  // as a broken product.
  it('does not accept route WRITE in place of route ADMIN for the requeue', async () => {
    authState.can = (permission) =>
      permission === 'notify:route:read' ||
      permission === 'notify:delivery:read' ||
      permission === 'notify:route:write'
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    expect(await screen.findByText('security_guardrail')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /requeue this notification/i }),
    ).not.toBeInTheDocument()
  })

  it('refuses the DLQ to a principal without the delivery read permission', async () => {
    authState.can = (permission) => permission === 'notify:route:read'
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    expect(
      await screen.findByText(/permission to view the outbox/i),
    ).toBeInTheDocument()
    expect(api.listOutbox).not.toHaveBeenCalled()
  })

  // "No dead letters" and "I could not read the dead letters" are DIFFERENT answers,
  // and confusing them is the most expensive defect in this repository. On this screen
  // it is worse than average: an empty DLQ is the good news an operator acts on.
  it('does not render an unreadable queue as an empty one', async () => {
    api.listOutbox.mockRejectedValue(
      new ApiError(500, 'internal', 'the store is unavailable'),
    )
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    await waitFor(() => expect(api.listOutbox).toHaveBeenCalled())
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.queryByText(/no dead letters/i)).not.toBeInTheDocument()
  })

  it('says the notification is in flight when the engine answers 409', async () => {
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(
        409,
        'conflict',
        'delivery is in flight; only a terminal (delivered/dead) row can be redelivered',
      ),
    )
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^requeue notification$/i }),
    )

    // The sentence is chosen from the STATUS, and it survives on the screen — a toast
    // is gone in seconds and the operator's next question is "so is it queued?".
    expect(await screen.findByText(/already in flight/i)).toBeInTheDocument()
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('a step-up 403 opens the ceremony and does NOT also accuse the operator', async () => {
    // ⛔ BOTH 403s ARE `isForbidden`. `errors.ts:59` is only the status and
    // `errors.ts:77` is the code, so a step_up_required refusal satisfies BOTH —
    // and `redeliverFailureKind` read only the second one. The operator got the
    // ceremony AND, in the same viewport, a box saying the control plane refused
    // them: if they abandon the ceremony, the accusation is what remains on screen.
    useStepUpStore.setState({ request: null })
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^requeue notification$/i }),
    )

    // POSITIVE ANCHOR FIRST. An absence asserted on its own is satisfied by the
    // first tick, before any box could have rendered — the cell would pass with
    // the defect in place. Waiting for the ceremony proves the refusal ARRIVED.
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(screen.queryByText(/refused the request/i)).not.toBeInTheDocument()
  })

  it('says the notification is gone when the engine answers 404', async () => {
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^requeue notification$/i }),
    )

    // A DIFFERENT sentence from the 409: both are refusals, and telling an operator
    // "already in flight" about a row that no longer exists sends them hunting for it.
    //
    // The wording is deliberately "not in THIS TENANT'S outbox", not "gone": the engine
    // pins the read to the resolved tenant, so a row that exists in another tenant is
    // also a 404 (contrast finding F5). Claiming it was removed would be a fact the
    // console cannot know.
    expect(
      await screen.findByText(/not in this tenant's outbox/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/already in flight/i)).not.toBeInTheDocument()
  })

  // CONTRAST FINDING F2. Reading only the STATUS collapsed every 403 into "you need the
  // notify route admin permission". The engine mints a distinct code for the recording
  // consent boundary precisely so a console can route to the acknowledgement instead
  // (core/api/errors.go:211-215), and telling an admin to obtain a permission they
  // already hold sends them somewhere there is nothing to fix.
  it('names a recording-consent 403 as consent, not as a missing permission', async () => {
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(403, 'recording_consent_required', 'consent required'),
    )
    const user = userEvent.setup()
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^requeue notification$/i }),
    )

    expect(
      await screen.findByText(/recording notice .* has not been acknowledged/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/needs the notify route admin permission/i),
    ).not.toBeInTheDocument()
  })

  // A permission boundary is calm, not red — that is what the design system says and what
  // this box used to contradict. The 409 control below is what makes the assertion
  // load-bearing: without it, painting EVERYTHING calm would pass too.
  it('announces a permission boundary calmly and a refusal as an alert', async () => {
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )
    const user = userEvent.setup()
    const { unmount } = wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /^requeue notification$/i,
      }),
    )
    const calm = await screen.findByText(/refused the request/i)
    expect(calm.closest('[role="status"]')).not.toBeNull()
    expect(calm.closest('[role="alert"]')).toBeNull()
    unmount()

    // CONTROL: a 409 is a refusal of the action, not a boundary, and stays an alert.
    api.redeliverOutbox.mockRejectedValue(
      new ApiError(409, 'conflict', 'in flight'),
    )
    wrap(<AlertingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /requeue this notification to slack-sec/i,
      }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /^requeue notification$/i,
      }),
    )
    const loud = await screen.findByText(/already in flight/i)
    expect(loud.closest('[role="alert"]')).not.toBeNull()
  })

  // The other half of contrast finding F2: naming the consent 403 correctly is useless
  // if the operator has no way to answer it. AlertingView now mounts RecordingNotice,
  // whose blocking dialog is how the acknowledgement is given — without it the engine
  // stays deny-closed and the screen offers no exit.
  it('offers the recording acknowledgement when the tenant requires consent', async () => {
    recordingApiMock.notice.mockResolvedValue({
      recorded_namespaces: ['notify'],
      consent_required: true,
    })
    wrap(<AlertingView />)

    expect(
      await screen.findByRole('button', { name: /acknowledge and continue/i }),
    ).toBeInTheDocument()
  })

  it('stays out of the way when notify is not a recorded namespace', async () => {
    // CONTROL for the cell above: the strip and dialog must not appear for every tenant,
    // or the assertion would pass on a component that always renders.
    wrap(<AlertingView />)
    expect(
      await screen.findByRole('tab', { name: /dead letters/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /acknowledge and continue/i }),
    ).not.toBeInTheDocument()
  })
})
