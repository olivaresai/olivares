// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api/errors'
import type { FrameDTO, SessionDTO } from '@/features/recordings/types'
import type {
  KeyedFrame,
  KeyedTimelineEntry,
  TimelineEntry,
  UnifiedResponse,
  VerifyResult,
} from './types'

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

const routerPathname = vi.hoisted(() => ({ value: '/session-viewer/sess-1' }))
vi.mock('@tanstack/react-router', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  useRouterState: (opts: any) =>
    opts.select({ location: { pathname: routerPathname.value } }),
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Link: ({ to, children, ...props }: any) => (
    <a href={String(to)} {...props}>
      {children}
    </a>
  ),
}))

const api = vi.hoisted(() => ({
  unified: vi.fn(),
  verify: vi.fn(),
  seal: vi.fn(),
  summarize: vi.fn(),
  exportJSON: vi.fn(),
  exportSummary: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, viewerApi: api }
})

import { FilesPanel } from './files-panel'
import { RedactionToggle } from './redaction-toggle'
import { SessionViewerPage } from './session-viewer-page'
import { ToolsPanel } from './tools-panel'
import { UnifiedTimeline } from './unified-timeline'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

const session: SessionDTO = {
  id: 'sess-1',
  subject: 'user:alice',
  subject_kind: 'user',
  subject_user: 'u-1',
  cred: 'cred-1',
  status: 'active',
  opened_at: '2026-06-10T08:00:00Z',
  last_at: '2026-06-10T08:02:00Z',
  frames_written: 3,
  frames_reserved: 3,
  gap: false,
  tip_hash: 'ab'.repeat(32),
  open_seq: 41,
  anchor_seq: 71,
  seal_seq: 0,
}

const frames: FrameDTO[] = [
  {
    idx: 1,
    at: '2026-06-10T08:00:01Z',
    actor: 'user:alice',
    actor_kind: 'user',
    namespace: 'governance',
    method: 'POST',
    pattern: '/approvals/{id}/decisions',
    perm: 'governance:approval:admin',
    http_status: 200,
    outcome: 'allowed',
    dur_ms: 42,
    prev_hash: '0'.repeat(64),
    hash: 'a1'.repeat(32),
    anchor_seq: 57,
  },
  {
    idx: 2,
    at: '2026-06-10T08:00:05Z',
    actor: 'user:alice',
    actor_kind: 'user',
    namespace: 'identity',
    method: 'DELETE',
    pattern: '/users/{id}',
    perm: 'identity:user:admin',
    http_status: 403,
    outcome: 'denied',
    dur_ms: 7,
    prev_hash: 'a1'.repeat(32),
    hash: 'b2'.repeat(32),
  },
  {
    idx: 3,
    at: '2026-06-10T08:00:09Z',
    actor: 'user:alice',
    actor_kind: 'user',
    namespace: 'catalog',
    method: 'PUT',
    pattern: '/entries/{id}',
    perm: 'catalog:entry:admin',
    http_status: 200,
    outcome: 'allowed',
    dur_ms: 18,
    prev_hash: 'b2'.repeat(32),
    hash: 'c3'.repeat(32),
  },
]

const timeline: TimelineEntry[] = [
  {
    at: '2026-06-10T08:00:00Z',
    kind: 'tool',
    tool_ref: 'Read',
    resource_ref: '/src/main.go',
    title: 'Read /src/main.go',
  },
  {
    at: '2026-06-10T08:00:02Z',
    kind: 'tool',
    tool_ref: 'Write',
    resource_ref: '/src/output.go',
    title: 'Write /src/output.go',
  },
  {
    at: '2026-06-10T08:00:04Z',
    kind: 'mcp',
    tool_ref: 'mcp__server__list_files',
    title: 'MCP invocation',
  },
]

const passiveVerify: VerifyResult = {
  ok: true,
  frames_checked: 3,
  written: 3,
  reserved: 3,
  gap: false,
  tip_match: true,
  anchors_ok: true,
  anchors_checked: 2,
  anchored_through: 2,
}

const unifiedResponse: UnifiedResponse = {
  schema: 'olivares.recording/v1',
  semconv: '1.41.1',
  session,
  live: null,
  frames: { items: frames, has_more: false },
  timeline: { items: timeline, has_more: false, available: true },
  ledger: [],
  ledger_truncated: false,
  verify: passiveVerify,
}

function response(overrides: Partial<UnifiedResponse> = {}): UnifiedResponse {
  return { ...unifiedResponse, ...overrides }
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  for (const fn of Object.values(toast)) fn.mockReset()
  routerPathname.value = '/session-viewer/sess-1'
  api.unified.mockResolvedValue(unifiedResponse)
})
afterEach(() => vi.clearAllMocks())

describe('SessionViewerPage', () => {
  it('appends each cursor lane once, keeps stable keys, and stops at has_more=false', async () => {
    const firstTimeline = { ...timeline[0]!, title: 'First activity' }
    const secondTimeline = { ...timeline[1]!, title: 'Second activity' }

    api.unified.mockImplementation(
      async (_id: string, params: { frame_cursor?: string }) => {
        if (!params.frame_cursor) {
          return response({
            frames: { items: [frames[0]!], cursor: 'frame-2', has_more: true },
            timeline: {
              items: [firstTimeline],
              cursor: 'timeline-2',
              has_more: true,
              available: true,
            },
          })
        }
        if (params.frame_cursor === 'frame-2') {
          return response({
            frames: { items: [frames[1]!], cursor: 'frame-3', has_more: true },
            timeline: {
              items: [secondTimeline],
              has_more: false,
              available: true,
            },
          })
        }
        return response({
          frames: { items: [frames[2]!], has_more: false },
          // Omitted timeline_cursor means the backend returns page one again.
          timeline: {
            items: [firstTimeline],
            cursor: 'timeline-2',
            has_more: true,
            available: true,
          },
        })
      },
    )

    wrap(<SessionViewerPage />)
    expect(await screen.findByText('First activity')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('Second activity')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(
      await screen.findByText('PUT catalog/entries/{id}'),
    ).toBeInTheDocument()

    expect(screen.getAllByText('First activity')).toHaveLength(1)
    expect(screen.getAllByText('Second activity')).toHaveLength(1)
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Load more' }),
      ).not.toBeInTheDocument(),
    )
    expect(api.unified).toHaveBeenCalledTimes(3)
    expect(api.unified.mock.calls[2]?.[1]).toEqual(
      expect.objectContaining({ frame_cursor: 'frame-3' }),
    )
    expect(api.unified.mock.calls[2]?.[1]).not.toHaveProperty('timeline_cursor')

    const renderedKeys = within(screen.getByTestId('unified-timeline'))
      .getAllByRole('listitem')
      .map((row) => row.getAttribute('data-event-key'))
    expect(new Set(renderedKeys).size).toBe(renderedKeys.length)
    expect(renderedKeys).toContain(`evidence:${frames[2]!.hash}`)
    expect(renderedKeys).toContain(
      `activity:cursor:timeline-2-0-${secondTimeline.at}`,
    )
  })

  it('renders ledger rows, chain header fields, and frame chain details', async () => {
    api.unified.mockResolvedValue(
      response({
        ledger: [
          {
            seq: 42,
            occurred_at: '2026-06-10T08:00:00Z',
            actor: 'user:alice',
            action: 'recording.session.open',
            target_kind: 'recording_session',
            target_id: 'sess-1',
          },
        ],
        ledger_truncated: true,
      }),
    )
    wrap(<SessionViewerPage />)

    expect(
      await screen.findByText('recording.session.open'),
    ).toBeInTheDocument()
    expect(screen.getByText('seq 42')).toBeInTheDocument()
    expect(screen.getByText(/ledger view is truncated/i)).toBeInTheDocument()
    expect(screen.getByText('Chain tip').parentElement).toHaveTextContent(
      'tip_hash',
    )
    expect(screen.getByText('Open ledger seq').parentElement).toHaveTextContent(
      '41',
    )
    expect(
      screen.getByText('Latest anchor seq').parentElement,
    ).toHaveTextContent('71')
    expect(screen.getByText('Seal ledger seq').parentElement).toHaveTextContent(
      '—',
    )

    await userEvent.click(
      screen.getByRole('button', {
        name: /POST governance\/approvals\/\{id\}\/decisions/,
      }),
    )
    expect(screen.getByText('Frame hash').parentElement).toHaveTextContent(
      frames[0]!.hash,
    )
    expect(screen.getByText('Previous hash').parentElement).toHaveTextContent(
      frames[0]!.prev_hash,
    )
    expect(screen.getByText('Anchor seq').parentElement).toHaveTextContent('57')
  })

  it('calls fresh verify and renders anchor failures and coverage', async () => {
    api.verify.mockResolvedValue({
      ...passiveVerify,
      ok: false,
      anchors_ok: false,
      reason: 'anchor-mismatch',
      break_at: 0,
      anchored_through: 1,
      anchor_failures: [
        {
          kind: 'periodic',
          seq: 63,
          at_idx: 2,
          reason: 'tip-mismatch',
        },
      ],
    })
    wrap(<SessionViewerPage />)

    await userEvent.click(
      await screen.findByRole('button', { name: 'Verify Chain' }),
    )
    await waitFor(() => expect(api.verify).toHaveBeenCalledWith('sess-1'))
    expect(await screen.findByText('Fresh verification')).toBeInTheDocument()
    expect(
      screen.getByText('periodic anchor · seq 63 · frame 2 · tip-mismatch'),
    ).toBeInTheDocument()
    expect(
      screen.getByText('Verified ledger anchors cover through frame 1.'),
    ).toBeInTheDocument()
  })

  it('confirms seal and renders an honest 409 already-sealed state', async () => {
    api.seal.mockRejectedValue(
      new ApiError(409, 'conflict', 'session is already sealed'),
    )
    wrap(<SessionViewerPage />)

    await userEvent.click(
      await screen.findByRole('button', { name: 'Seal session' }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Seal session' }),
    )

    await waitFor(() => expect(api.seal).toHaveBeenCalledWith('sess-1'))
    expect(
      await screen.findByText('This session is already sealed.'),
    ).toBeInTheDocument()
  })

  it.each([
    [
      501,
      'No summarizer is configured; the recording remains available as raw evidence.',
    ],
    [
      403,
      'AI summaries are disabled for this tenant because the transcript would leave the trust boundary.',
    ],
    [
      409,
      'This session is still active. Seal it before generating a complete summary.',
    ],
  ])(
    'renders the honest summarize %i state inline',
    async (status, message) => {
      api.summarize.mockRejectedValue(
        new ApiError(status, 'internal', 'backend refusal'),
      )
      wrap(<SessionViewerPage />)

      await userEvent.click(
        await screen.findByRole('button', { name: 'Generate summary' }),
      )
      await waitFor(() => expect(api.summarize).toHaveBeenCalledWith('sess-1'))
      expect(await screen.findByText(message)).toBeInTheDocument()
      expect(toast.error).not.toHaveBeenCalled()
    },
  )

  it('renders recordings and live-sessions cross-links with correct hrefs', async () => {
    api.unified.mockResolvedValue(
      response({ live: { session_ref: 'live-session-7' } }),
    )
    wrap(<SessionViewerPage />)

    const back = await screen.findByRole('link', {
      name: 'Back to recordings',
    })
    expect(back).toHaveAttribute('href', '/recordings')
    const live = screen.getByRole('link', { name: 'LIVE' })
    expect(live).toHaveAttribute('href', '/sessions')
    expect(live).toHaveAttribute('title', 'live-session-7')
  })

  it('does not report a noop or failed activity resolver as an empty success', async () => {
    api.unified.mockResolvedValue(
      response({
        timeline: { items: [], has_more: false, available: false },
      }),
    )
    wrap(<SessionViewerPage />)

    expect(
      await screen.findByText(
        /timeline resolver is not wired or did not answer/i,
      ),
    ).toBeInTheDocument()
    // Recording evidence is independent and remains usable during degradation.
    expect(
      screen.getByText('POST governance/approvals/{id}/decisions'),
    ).toBeInTheDocument()
  })

  it('shows not-found for an empty route ID', () => {
    routerPathname.value = '/session-viewer/'
    wrap(<SessionViewerPage />)
    expect(screen.getByText('Recording session not found')).toBeInTheDocument()
  })
})

describe('UnifiedTimeline', () => {
  it('renders keyed activity and evidence lanes', () => {
    const keyedTimeline: KeyedTimelineEntry[] = timeline.map(
      (entry, index) => ({
        key: `source-${index}`,
        entry,
      }),
    )
    const keyedFrames: KeyedFrame[] = frames.map((frame) => ({
      key: frame.hash,
      frame,
    }))
    wrap(
      <UnifiedTimeline
        timeline={keyedTimeline}
        timelineAvailable
        frames={keyedFrames}
        selectedEventId={null}
        onSelectTimeline={() => {}}
        onSelectFrame={() => {}}
      />,
    )

    expect(screen.getByText('Agent Activity')).toBeInTheDocument()
    expect(screen.getByText('Governance Evidence')).toBeInTheDocument()
    expect(screen.getByText('Read /src/main.go')).toBeInTheDocument()
    expect(
      screen.getByText('POST governance/approvals/{id}/decisions'),
    ).toBeInTheDocument()
    expect(screen.getByText('denied')).toBeInTheDocument()
  })

  //SC 1.4.1/1.4.11 regression net. Before this test existed, reverting the
  // five selection sites to `ring-accent-line` (1.34:1 dark / 1.60:1 light, colour
  // only) left BOTH the unit suite and `task at:gate` green: the gate probes the
  // token, never the usage, and --accent-line is still a legitimately waived pair.
  // So the token measurement alone cannot protect this — the usage has to be pinned.
  it('carries selection with a non-colour signal, not just a low-contrast ring', () => {
    const keyedTimeline: KeyedTimelineEntry[] = timeline.map(
      (entry, index) => ({
        key: `source-${index}`,
        entry,
      }),
    )
    const keyedFrames: KeyedFrame[] = frames.map((frame) => ({
      key: frame.hash,
      frame,
    }))
    const { container } = wrap(
      <UnifiedTimeline
        timeline={keyedTimeline}
        timelineAvailable
        frames={keyedFrames}
        selectedEventId="activity:source-0"
        onSelectTimeline={() => {}}
        onSelectFrame={() => {}}
      />,
    )

    // 1. The state reaches the accessibility tree, on exactly the selected row.
    const pressed = container.querySelectorAll('[aria-pressed="true"]')
    expect(pressed).toHaveLength(1)
    const unpressed = container.querySelectorAll('[aria-pressed="false"]')
    expect(unpressed.length).toBeGreaterThan(0)

    // 2. A non-colour signal: the rail is FILLED on the selected row and transparent
    //    on the others, so presence/absence — not hue — distinguishes them.
    expect(pressed[0].querySelector('.bg-accent-strong')).not.toBeNull()
    expect(unpressed[0].querySelector('.bg-accent-strong')).toBeNull()
    expect(unpressed[0].querySelector('.bg-transparent')).not.toBeNull()

    // 3. The ring is the gated >=3:1 token, never the waived hairline again.
    expect(container.innerHTML).not.toContain('ring-accent-line')
    expect(pressed[0].className).toContain('ring-accent-strong')
  })
})

describe('RedactionToggle', () => {
  it('hides and reveals content', async () => {
    wrap(
      <RedactionToggle value="secret-token-abc" permission="recording:read" />,
    )
    expect(screen.getByText('[REDACTED — click to reveal]')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /reveal/i }))
    expect(screen.getByText('secret-token-abc')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /hide/i }))
    expect(screen.getByText('[REDACTED — click to reveal]')).toBeInTheDocument()
  })
})

describe('summary panels', () => {
  it('aggregates tools and builds the file list', () => {
    wrap(
      <>
        <ToolsPanel
          timeline={timeline}
          activeFilter={null}
          onFilterChange={() => {}}
        />
        <FilesPanel timeline={timeline} />
      </>,
    )

    expect(screen.getByText('Tools Used')).toBeInTheDocument()
    expect(screen.getByText('Files Touched')).toBeInTheDocument()
    expect(screen.getAllByText('Read')).toHaveLength(2)
    expect(screen.getAllByText('Write')).toHaveLength(2)
    expect(screen.getByText('main.go')).toBeInTheDocument()
    expect(screen.getByText('output.go')).toBeInTheDocument()
  })
})
