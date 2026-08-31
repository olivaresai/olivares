// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import type { NoticeResponse, SessionDTO } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => window.location.search,
  useNavigate: () => navigate,
}))

// The menu is exercised through its contract (featureId + onApply), not its
// dropdown: what this view owns is re-sanitising what comes back.
const savedViews = vi.hoisted(() => ({ featureId: '' }))
vi.mock('@/features/saved-views', () => ({
  SavedViewsMenu: ({
    featureId,
    onApply,
  }: {
    featureId: string
    onApply: (params: Record<string, string>) => void
  }) => {
    savedViews.featureId = featureId
    return (
      <button
        type="button"
        onClick={() =>
          onApply({
            status: 'sealed',
            seal_reason: 'not-a-reason',
            opened_after: 'not-a-date',
            unknown: 'ignored',
          })
        }
      >
        Apply saved recordings view
      </button>
    )
  },
}))

const api = vi.hoisted(() => ({
  notice: vi.fn(),
  acknowledge: vi.fn(),
  listSessions: vi.fn(),
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, recordingApi: api }
})

import { RecordingNotice } from './recording-notice'
import { RecordingsView } from './recordings-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

const sealedSession: SessionDTO = {
  id: 'rec-sealed',
  subject: 'user:alice',
  subject_kind: 'user',
  subject_user: 'u-1',
  cred: 'sess-1',
  status: 'sealed',
  opened_at: '2026-06-09T10:00:00Z',
  sealed_at: '2026-06-09T11:00:00Z',
  seal_reason: 'idle',
  frames_written: 42,
  frames_reserved: 42,
  gap: false,
  tip_hash: 'ab'.repeat(32),
}

const gapSession: SessionDTO = {
  id: 'rec-gap',
  subject: 'token:ci',
  subject_kind: 'token',
  cred: 'tok-1',
  status: 'active',
  opened_at: '2026-06-10T08:00:00Z',
  frames_written: 7,
  frames_reserved: 9,
  gap: true,
  breakglass_grant: 'bg-7',
}

const quietNotice: NoticeResponse = {
  recorded_namespaces: ['governance', 'identity'],
  breakglass_always: true,
  consent_mode: 'notice',
  consent_required: false,
  acknowledged: false,
  schema: 'olivares.recording/v1',
  semconv: '1.41.1',
}

const SEARCH_AUDIT_CONTRACT =
  'RECORDINGS_SEARCH_AUDIT_CONTRACT: typing must not issue an audited read before submit'
const SEARCH_SUBJECT_CONTRACT =
  'RECORDINGS_SEARCH_SUBJECT_CONTRACT: submit must use subject_contains'
const SEARCH_GRANT_CONTRACT =
  'RECORDINGS_SEARCH_GRANT_CONTRACT: grant search must use the exact grant predicate'
const SEARCH_URL_CONTRACT =
  'RECORDINGS_SEARCH_URL_CONTRACT: URL search must hydrate and query the same predicate'
const SEARCH_CLEAR_CONTRACT =
  'RECORDINGS_SEARCH_CLEAR_CONTRACT: clear must remove both search predicates'
const SEARCH_RESPONSES_CONTRACT =
  'RECORDINGS_SEARCH_RESPONSES_CONTRACT: populated, empty, and error/retry responses must remain distinct'

beforeEach(() => {
  window.history.replaceState(null, '', '/recordings')
  savedViews.featureId = ''
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  toast.info.mockReset()
  navigate.mockReset()
  // Defaults so background queries never reject.
  api.notice.mockResolvedValue(quietNotice)
  api.listSessions.mockResolvedValue({ items: [], has_more: false })
  // RecordingsView now mounts the policy panel, which reads GET /config.
  api.getConfig.mockResolvedValue({
    namespaces: ['governance', 'identity'],
    breakglass_always: true,
    consent: 'notice',
    idle_seconds: 900,
    retention_days: 365,
    retention_enforced: false,
    ai_summaries: false,
  })
})
afterEach(() => {
  window.history.replaceState(null, '', '/')
  vi.clearAllMocks()
})

describe('RecordingsView — recording session list', () => {
  it('lists sessions with kind/status badges, GAP and break-glass flags', async () => {
    api.listSessions.mockResolvedValue({
      items: [sealedSession, gapSession],
      has_more: false,
    })
    wrap(<RecordingsView />)

    expect(await screen.findByText('user:alice')).toBeInTheDocument()
    const sealedRow = screen.getByText('user:alice').closest('tr')!
    expect(within(sealedRow).getByText('Sealed')).toBeInTheDocument()
    expect(within(sealedRow).getByText('idle')).toBeInTheDocument()
    expect(within(sealedRow).queryByText('GAP')).toBeNull()

    const gapRow = screen.getByText('token:ci').closest('tr')!
    expect(within(gapRow).getByText('Active')).toBeInTheDocument()
    // The evident gap is a red badge; the break-glass origin its own badge.
    expect(within(gapRow).getByText('GAP')).toBeInTheDocument()
    expect(within(gapRow).getByText('Break-glass')).toBeInTheDocument()

    await userEvent.click(sealedRow)
    expect(navigate).toHaveBeenCalledWith({
      to: '/session-viewer/rec-sealed',
    })
  })

  it('submits subject or grant search explicitly and never reads while typing', async () => {
    api.listSessions.mockReset()
    api.listSessions.mockImplementation(
      async (params?: { subject_contains?: string; grant?: string }) => {
        if (params?.grant === 'bg-7') {
          return { items: [gapSession], has_more: false }
        }
        if (params?.subject_contains === 'alice') {
          return { items: [sealedSession], has_more: false }
        }
        return { items: [], has_more: false }
      },
    )
    const user = userEvent.setup()
    wrap(<RecordingsView />)
    const search = await screen.findByRole('textbox', {
      name: 'Recording search',
    })
    await waitFor(() => expect(api.listSessions).toHaveBeenCalledTimes(1))

    await user.type(search, 'alice')
    await new Promise((resolve) => window.setTimeout(resolve, 350))
    expect(api.listSessions.mock.calls.length, SEARCH_AUDIT_CONTRACT).toBe(1)
    await user.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() =>
      expect(
        api.listSessions,
        SEARCH_SUBJECT_CONTRACT,
      ).toHaveBeenLastCalledWith(
        expect.objectContaining({
          subject_contains: 'alice',
          grant: undefined,
          limit: 50,
        }),
      ),
    )
    expect(await screen.findByText('user:alice')).toBeInTheDocument()

    const readsAfterSubject = api.listSessions.mock.calls.length
    await user.click(screen.getByLabelText('Search field'))
    await user.click(await screen.findByRole('option', { name: 'Grant ID' }))
    await user.clear(search)
    await user.type(search, 'bg-7')
    await new Promise((resolve) => window.setTimeout(resolve, 350))
    expect(api.listSessions.mock.calls.length, SEARCH_AUDIT_CONTRACT).toBe(
      readsAfterSubject,
    )
    await user.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() =>
      expect(api.listSessions, SEARCH_GRANT_CONTRACT).toHaveBeenLastCalledWith(
        expect.objectContaining({
          subject_contains: undefined,
          grant: 'bg-7',
          limit: 50,
        }),
      ),
    )
    // The loaded-page filter uses the same search value, so its flags accessor
    // must keep carrying the real grant identifier rather than only
    // "breakglass" if client-side search is ever restored.
    expect(await screen.findByText('token:ci')).toBeInTheDocument()
  })

  it('keeps populated, empty, and failed reads distinct and retries the failure', async () => {
    let fail = true
    api.listSessions.mockImplementation(
      async (params?: { subject_contains?: string }) => {
        if (params?.subject_contains === 'none') {
          return { items: [], has_more: false }
        }
        if (params?.subject_contains === 'fail' && fail) {
          throw new Error('recording read failed')
        }
        if (params?.subject_contains === 'fail') {
          return { items: [gapSession], has_more: false }
        }
        return { items: [sealedSession], has_more: false }
      },
    )
    const user = userEvent.setup()
    wrap(<RecordingsView />)

    await waitFor(() => {
      const populated = screen.queryByText('user:alice')
      if (populated === null) throw new Error(SEARCH_RESPONSES_CONTRACT)
      expect(populated).toBeInTheDocument()
    })
    const search = screen.getByRole('textbox', { name: 'Recording search' })

    await user.type(search, 'none')
    await user.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => {
      const empty = screen.queryByText('No recording sessions yet')
      if (empty === null) throw new Error(SEARCH_RESPONSES_CONTRACT)
      expect(empty).toBeInTheDocument()
    })

    await user.clear(search)
    await user.type(search, 'fail')
    await user.click(screen.getByRole('button', { name: /^search$/i }))
    await waitFor(() => {
      const failed = screen.queryByText('Something went wrong')
      if (failed === null) throw new Error(SEARCH_RESPONSES_CONTRACT)
      expect(failed).toBeInTheDocument()
    })

    fail = false
    await user.click(screen.getByRole('button', { name: /^retry$/i }))
    await waitFor(() => {
      const retried = screen.queryByText('token:ci')
      if (retried === null) throw new Error(SEARCH_RESPONSES_CONTRACT)
      expect(retried).toBeInTheDocument()
    })
  })
})

describe('RecordingsView — deep-linkable filter state', () => {
  it('hydrates a subject search from the URL and clears both predicates', async () => {
    window.history.replaceState(null, '', '/recordings?subject_contains=alice')
    api.listSessions.mockImplementation(
      async (params?: { subject_contains?: string }) => ({
        items: params?.subject_contains === 'alice' ? [sealedSession] : [],
        has_more: false,
      }),
    )
    const user = userEvent.setup()
    wrap(<RecordingsView />)

    await waitFor(() =>
      expect(api.listSessions, SEARCH_URL_CONTRACT).toHaveBeenCalledWith(
        expect.objectContaining({
          subject_contains: 'alice',
          grant: undefined,
        }),
      ),
    )
    expect(
      screen.getByRole('textbox', { name: 'Recording search' }),
      SEARCH_URL_CONTRACT,
    ).toHaveValue('alice')
    expect(screen.getByLabelText('Search field')).toHaveTextContent(
      'Subject contains',
    )

    await user.click(screen.getByRole('button', { name: /^clear$/i }))
    await waitFor(() =>
      expect(api.listSessions, SEARCH_CLEAR_CONTRACT).toHaveBeenLastCalledWith(
        expect.objectContaining({
          subject_contains: undefined,
          grant: undefined,
        }),
      ),
    )
    expect(
      screen.getByRole('textbox', { name: 'Recording search' }),
      SEARCH_CLEAR_CONTRACT,
    ).toHaveValue('')
    const clearPatch = navigate.mock.calls.at(-1)?.[0]
    expect(
      clearPatch.search({ subject_contains: 'alice', grant: 'stale' }),
      SEARCH_CLEAR_CONTRACT,
    ).toEqual({ subject_contains: undefined, grant: undefined })
  })

  it('refuses a hidden second search predicate instead of applying silent AND', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?subject_contains=alice&grant=bg-7',
    )
    wrap(<RecordingsView />)

    await waitFor(() =>
      expect(api.listSessions, SEARCH_URL_CONTRACT).toHaveBeenCalledWith(
        expect.objectContaining({
          subject_contains: 'alice',
          grant: undefined,
        }),
      ),
    )
    await waitFor(() =>
      expect(
        screen.queryByTestId('url-state-notice'),
        SEARCH_URL_CONTRACT,
      ).toBeInTheDocument(),
    )
    const notice = screen.getByTestId('url-state-notice')
    expect(notice, SEARCH_URL_CONTRACT).toHaveTextContent('grant')
    expect(
      screen.getByRole('textbox', { name: 'Recording search' }),
      SEARCH_URL_CONTRACT,
    ).toHaveValue('alice')
  })

  it('round-trips every filter through the URL and patches with replace', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?status=sealed&seal_reason=idle' +
        '&opened_after=2026-06-01T00%3A00%3A00.000Z' +
        '&opened_before=2026-06-30T00%3A00%3A00.000Z',
    )
    wrap(<RecordingsView />)

    await waitFor(() =>
      expect(api.listSessions).toHaveBeenCalledWith(
        expect.objectContaining({
          status: 'sealed',
          seal_reason: 'idle',
          opened_after: '2026-06-01T00:00:00.000Z',
          opened_before: '2026-06-30T00:00:00.000Z',
          limit: 50,
        }),
      ),
    )
    // The controls show what the link carried; nothing was refused.
    expect(screen.getByLabelText('Filter by status')).toHaveTextContent(
      'Sealed',
    )
    expect(screen.getByLabelText('Filter by seal reason')).toHaveTextContent(
      'Idle',
    )
    expect(screen.getByLabelText('Opened after')).toHaveValue('2026-06-01')
    expect(screen.getByLabelText('Opened before')).toHaveValue('2026-06-30')
    expect(screen.queryByTestId('url-state-notice')).toBeNull()

    fireEvent.change(screen.getByLabelText('Opened before'), {
      target: { value: '2026-07-15' },
    })

    const call = navigate.mock.calls.at(-1)?.[0]
    expect(call.replace).toBe(true)
    // Merged over the current search: params this view does not own survive,
    // and the untouched owned keys are not restated.
    expect(call.search({ tab: 'x', status: 'sealed' })).toEqual({
      tab: 'x',
      status: 'sealed',
      opened_before: '2026-07-15T00:00:00.000Z',
    })
    expect(screen.getByLabelText('Opened before')).toHaveValue('2026-07-15')
  })

  it('falls back to the default for a refused value AND says so', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?status=bogus&seal_reason=idle&opened_after=not-a-date' +
        '&subject_contains=%20%20',
    )
    wrap(<RecordingsView />)

    await waitFor(() =>
      expect(api.listSessions).toHaveBeenCalledWith(
        expect.objectContaining({
          status: undefined,
          seal_reason: 'idle',
          opened_after: undefined,
          subject_contains: undefined,
          limit: 50,
        }),
      ),
    )
    const notice = await screen.findByTestId('url-state-notice')
    expect(notice).toHaveTextContent('status')
    expect(notice).toHaveTextContent('opened_after')
    expect(notice, SEARCH_URL_CONTRACT).toHaveTextContent('subject_contains')
    expect(screen.getByLabelText('Filter by status')).toHaveTextContent(
      'All statuses',
    )
  })

  it('re-sanitises a saved view before it reaches the request', async () => {
    wrap(<RecordingsView />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Apply saved recordings view',
      }),
    )

    expect(savedViews.featureId).toBe('recordings')
    await waitFor(() =>
      expect(api.listSessions).toHaveBeenLastCalledWith(
        expect.objectContaining({
          status: 'sealed',
          seal_reason: undefined,
          opened_after: undefined,
          limit: 50,
        }),
      ),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ replace: true }),
    )
    // A stored view is server data: what it lost must be disclosed too.
    const notice = screen.getByTestId('url-state-notice')
    expect(notice).toHaveTextContent('seal_reason')
    expect(notice).toHaveTextContent('opened_after')
  })
})

describe('RecordingNotice — AC-8 strip + blocking consent', () => {
  it('renders the quiet strip when the namespace is recorded', async () => {
    wrap(<RecordingNotice namespace="governance" />)
    expect(
      await screen.findByText(/recorded privileged surface/i),
    ).toBeInTheDocument()
    // No consent needed → no blocking dialog.
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('renders nothing when the namespace is not recorded', async () => {
    wrap(<RecordingNotice namespace="finops" />)
    await waitFor(() => expect(api.notice).toHaveBeenCalled())
    expect(screen.queryByText(/recorded privileged surface/i)).toBeNull()
  })

  it('stays quiet (never crashes) when the read returns a malformed body', async () => {
    // A transient/empty read can omit recorded_namespaces (the bare list-empty
    // fallback). The strip is informational — it must degrade to nothing, never
    // crash the recorded surface it is mounted on.
    api.notice.mockResolvedValue({ items: [], has_more: false } as never)
    wrap(<RecordingNotice namespace="governance" />)
    await waitFor(() => expect(api.notice).toHaveBeenCalled())
    expect(screen.queryByText(/recorded privileged surface/i)).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('blocks with the consent dialog and fires the ack mutation on confirm', async () => {
    api.notice.mockResolvedValue({
      ...quietNotice,
      consent_mode: 'required',
      consent_required: true,
    })
    api.acknowledge.mockResolvedValue({
      session_id: 'rs-1',
      acknowledged_at: '2026-06-10T10:00:00Z',
    })
    wrap(<RecordingNotice namespace="governance" />)

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/recording notice \(AC-8\)/i),
    ).toBeInTheDocument()

    await userEvent.click(
      within(dialog).getByRole('button', { name: /acknowledge and continue/i }),
    )
    await waitFor(() => expect(api.acknowledge).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('cancel navigates back instead of acknowledging', async () => {
    api.notice.mockResolvedValue({
      ...quietNotice,
      consent_mode: 'required',
      consent_required: true,
    })
    const back = vi.spyOn(window.history, 'back').mockImplementation(() => {})
    wrap(<RecordingNotice namespace="governance" />)

    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^cancel$/i }),
    )
    expect(back).toHaveBeenCalledTimes(1)
    expect(api.acknowledge).not.toHaveBeenCalled()
    back.mockRestore()
  })
})

describe('RecordingsView day bounds are canonical', () => {
  // The audit blocker this closes: the decoder kept a full RFC3339 instant while
  // the control renders a day, so two links reading "1 June" could query
  // different windows — an evidence link that lies while looking right.
  it('shows and queries the SAME instant for a bound with a time of day', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?opened_after=2026-06-01T14%3A30%3A00Z',
    )
    wrap(<RecordingsView />)

    await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
    const sent = api.listSessions.mock.calls.at(-1)?.[0]
    const shown = (screen.getByLabelText(/opened after/i) as HTMLInputElement)
      .value
    // Whatever the request carries must be the instant the control displays.
    expect(sent.opened_after).toBe(`${shown}T00:00:00.000Z`)
  })

  it('refuses a calendar date that does not exist', async () => {
    window.history.replaceState(null, '', '/recordings?opened_after=2026-02-30')
    wrap(<RecordingsView />)

    // new Date('2026-02-30') is 2 March in JavaScript: answering an impossible
    // day with a different, possible one is worse than refusing it.
    await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
    // The view always names the key; what matters is that no VALUE reaches it,
    // which is how the http client drops it from the query string.
    expect(api.listSessions.mock.calls.at(-1)?.[0].opened_after).toBeUndefined()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /opened_after/,
    )
  })
})

describe('RecordingsView accepts legitimate bounds', () => {
  // The round-5 blocker this closes: the rollover guard compared the STATED
  // calendar date against the UTC day, which conflated two different questions
  // and refused valid input. An offset that crosses UTC midnight is not a
  // rollover — 2026-06-01T00:30:00+02:00 is a real instant on 31 May UTC.
  it('takes a bound whose offset crosses UTC midnight', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?opened_after=2026-06-01T00%3A30%3A00%2B02%3A00',
    )
    wrap(<RecordingsView />)

    await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
    expect(api.listSessions.mock.calls.at(-1)?.[0].opened_after).toBe(
      '2026-05-31T00:00:00.000Z',
    )
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it.each([
    // Every one of these escaped an earlier version of the guard: `new Date`
    // accepts them and normalises the impossible day into a possible one, so
    // opportunistic validation let them through. The grammar refuses them.
    '2026-02-30Z',
    '2026/02/30',
    ' 2026-02-30',
    '0',
    '+010000-01-01T00:00:00Z',
    '1900-02-29',
    // Four digits in, extended year out: these CROSS the boundary through the
    // offset or through 24:00, and the day used to be sliced out of a string
    // whose year is not four characters.
    '9999-12-31T23:00:00-05:00',
    '9999-12-31T24:00:00Z',
    '0000-01-01T00:00:00+05:00',
    // The grammar bounds the fields itself instead of leaving them to the parser.
    '2026-06-01T25:00:00Z',
    '2026-06-01T10:60:00Z',
    '2026-13-01',
    '2026-06-31',
  ])(
    'refuses %s, which the parser would otherwise normalise',
    async (bound) => {
      window.history.replaceState(
        null,
        '',
        `/recordings?opened_after=${encodeURIComponent(bound)}`,
      )
      wrap(<RecordingsView />)

      await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
      expect(
        api.listSessions.mock.calls.at(-1)?.[0].opened_after,
      ).toBeUndefined()
      expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
        /opened_after/,
      )
    },
  )

  it.each([
    ['0099-01-01T00:00:00Z', '0099-01-01T00:00:00.000Z'],
    ['2028-02-29', '2028-02-29T00:00:00.000Z'],
    ['2000-02-29', '2000-02-29T00:00:00.000Z'],
    ['2026-06-01T23:30:00-02:00', '2026-06-02T00:00:00.000Z'],
    // No offset means UTC, not the reader's local time: an evidence link has to
    // mean the same window for whoever opens it. TWO cases, because one is only
    // discriminating on one side of Greenwich — read as local, the first lands
    // on the previous day east of UTC and the second on the next day west of
    // it, so together they fail under any non-UTC process.
    ['2026-06-01T00:30:00', '2026-06-01T00:00:00.000Z'],
    ['2026-06-01T23:30:00', '2026-06-01T00:00:00.000Z'],
  ])('accepts %s as %s', async (bound, expected) => {
    window.history.replaceState(
      null,
      '',
      `/recordings?opened_after=${encodeURIComponent(bound)}`,
    )
    wrap(<RecordingsView />)

    await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
    expect(api.listSessions.mock.calls.at(-1)?.[0].opened_after).toBe(expected)
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('still refuses a day that does not exist, in the full timestamp form', async () => {
    window.history.replaceState(
      null,
      '',
      '/recordings?opened_after=2026-02-30T00%3A00%3A00Z',
    )
    wrap(<RecordingsView />)

    await waitFor(() => expect(api.listSessions).toHaveBeenCalled())
    expect(api.listSessions.mock.calls.at(-1)?.[0].opened_after).toBeUndefined()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /opened_after/,
    )
  })
})
