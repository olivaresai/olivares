// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREAD HEADER IS ONE 48 px LINE. The state as a dot, the name, the folder tag, the
// tool and model, the usage; on the right Interrupt (only while a turn runs), Stop, Context
// and a `⋯` menu. The badges and the working folder's path are the Context pane's.
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { useClientSettings } from '@/features/settings/preferences'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { SessionNarrative } from './session-narrative'
import { SessionOverview } from './session-overview'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  principal: { user_id: 'user-a' },
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: {
    timelineById: vi
      .fn()
      .mockResolvedValue({ items: [], has_more: false, cursor: '' }),
    timeline: vi
      .fn()
      .mockResolvedValue({ items: [], has_more: false, cursor: '' }),
  },
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

const ops = vi.hoisted(() => ({
  interrupt: vi.fn(),
  stop: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...ops },
  }
})
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: () => ({
    status: 'open',
    ended: false,
    ioUnavailable: null,
    retry: () => {},
  }),
}))

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-a',
    live_ref: 'lr-a',
    attribution: 'managed',
    cc_state: 'active',
    input_tokens: 12,
    output_tokens: 18,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 1,
    first_event_at: '2026-10-01T18:23:40Z',
    last_event_at: '2026-10-01T18:24:00Z',
    duration_seconds: 8,
    provider: 'codex',
    provider_profile_ref: 'ppf_codex',
    run_ref: 'run-1',
    posture: 'enforced',
    engine: 'codex',
    ...over,
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'calc-review',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 3,
    pep_provisioned: true,
    record_io: true,
    critical: false,
    provider_profile_ref: 'ppf_codex',
    provider_driver: 'codex',
    model_ref: 'stub-model',
    workspace_ref: 'ws-demo',
    workspace_path: '/home/olv/work/W320/job-5cfa2f96/projects/demo',
    tool_mode: 'untrusted · dangerFullAccess',
    input_tokens: 12,
    output_tokens: 18,
    live_ref: 'lr-a',
    ...over,
  }
}

function resolutionFor(r: RunDTO): SessionResolution {
  const l = live()
  return {
    target: { liveRef: 'lr-a' },
    session: mergeSessions([l], [r])[0]!,
    live: l,
    runs: [r],
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
  }
}

function renderThread(r: RunDTO = run(), props = {}) {
  const onOpenDetail = vi.fn()
  const onOpenContext = vi.fn()
  const resolution = resolutionFor(r)
  renderIntel(
    <>
      <SessionNarrative
        resolution={resolution}
        pinned={false}
        onTogglePin={null}
        onOpenDetail={onOpenDetail}
        evidence="checks"
        onExpandEvidence={() => {}}
        onOpenContext={onOpenContext}
        {...props}
      />
      <div data-testid="context-side">
        <SessionOverview
          resolution={resolution}
          evidence="checks"
          onExpandEvidence={() => {}}
        />
      </div>
    </>,
  )
  return { onOpenDetail, onOpenContext }
}

const header = () =>
  document.querySelector('[data-slot="thread-header"]') as HTMLElement

function openMenu() {
  act(() => {
    fireEvent.keyDown(screen.getByTestId('narrative-menu'), { key: 'Enter' })
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  useClientSettings.setState({ confirmStop: true })
  ops.interrupt.mockResolvedValue({ accepted: true })
  ops.stop.mockResolvedValue({})
})

describe('thread header — one line', () => {
  it('says the state, the name, the folder, the tool and model and the usage once', () => {
    renderThread()
    const h = header()
    expect(h.className).toContain('h-12')
    expect(within(h).getByRole('img', { name: 'Running' })).toBeTruthy()
    expect(within(h).getByRole('heading', { level: 2 })).toHaveTextContent(
      'calc-review',
    )
    // The folder by NAME, the whole path on the hover.
    const folder = within(h).getByTestId('narrative-folder')
    expect(folder).toHaveTextContent('demo')
    expect(folder.textContent).not.toContain('/home/olv')
    expect(folder).toHaveAttribute(
      'title',
      '/home/olv/work/W320/job-5cfa2f96/projects/demo',
    )
    expect(within(h).getByTestId('thread-tool-model')).toHaveTextContent(
      'Codex · stub-model',
    )
    expect(within(h).getByTestId('narrative-usage')).toHaveTextContent(
      '12 in / 18 out tokens',
    )
  })

  it('does not print the badges, the mode or the working-folder line in the header', () => {
    renderThread()
    const h = header()
    expect(within(h).queryByText('Managed by Olivares')).toBeNull()
    expect(within(h).queryByTestId('narrative-mode')).toBeNull()
    expect(within(h).queryByText(/Working folder/)).toBeNull()
    expect(h.textContent).not.toContain('dangerFullAccess')
    expect(h.textContent).not.toContain('/home/olv')
  })

  it('moves those facts into the Context pane in plain words', () => {
    renderThread()
    const side = screen.getByTestId('context-side')
    expect(within(side).getByText('Managed by Olivares')).toBeInTheDocument()
    expect(within(side).getByTestId('narrative-mode')).toHaveTextContent(
      'Mode: untrusted · dangerFullAccess',
    )
    expect(within(side).getByTestId('conversation-folder')).toHaveTextContent(
      'Working folder: /home/olv/work/W320/job-5cfa2f96/projects/demo',
    )
    // And the session's own page of the thread carries none of them.
    expect(
      within(screen.getByTestId('session-narrative')).queryByTestId(
        'conversation-folder',
      ),
    ).toBeNull()
  })

  it.each(['Interrupt turn', 'Stop'])(
    'names the %s icon button and shows its name as a tooltip on hover',
    async (name) => {
      const user = userEvent.setup()
      renderThread()
      await user.hover(within(header()).getByRole('button', { name }))
      await waitFor(() =>
        expect(
          screen
            .queryAllByRole('tooltip')
            .some((el) => el.textContent?.includes(name)),
        ).toBe(true),
      )
    },
  )

  it("shows an icon button's name as a tooltip on keyboard focus", async () => {
    renderThread()
    act(() => within(header()).getByRole('button', { name: 'Stop' }).focus())
    await waitFor(() =>
      expect(
        screen
          .queryAllByRole('tooltip')
          .some((el) => el.textContent?.includes('Stop')),
      ).toBe(true),
    )
  })

  it('keeps Stop quiet: a ghost icon whose glyph is the danger tone, tinted on hover', () => {
    renderThread()
    const stop = within(header()).getByRole('button', { name: 'Stop' })
    const cls = stop.className.split(/\s+/)
    expect(cls).toContain('text-bad')
    expect(cls).toContain('hover:bg-bad-soft')
    // Not the filled danger fill of the confirm.
    expect(cls).not.toContain('bg-bad-soft')
    expect(cls).not.toContain('bg-danger-solid')
  })

  it("keeps a disabled Interrupt's reason reachable by keyboard", async () => {
    // A work-bound run without a usable fence: Interrupt is shown, and waits for a refresh.
    renderThread(run({ work_item_id: 'work-a', work_lease_fence: 0 }))
    const button = within(header()).getByRole('button', {
      name: 'Interrupt turn',
    })
    expect(button).toBeDisabled()
    const tip = button.closest('[data-slot="disabled-tip"]') as HTMLElement
    expect(tip).not.toBeNull()
    expect(tip).toHaveAttribute('tabindex', '0')
    act(() => tip.focus())
    expect(await screen.findByRole('tooltip')).toHaveTextContent(
      /fence|refresh/i,
    )
  })

  it('offers Interrupt only while a turn runs', () => {
    renderThread(run({ state: 'idle' }))
    expect(
      within(header()).queryByRole('button', { name: 'Interrupt turn' }),
    ).toBeNull()
    expect(within(header()).getByRole('button', { name: 'Stop' })).toBeTruthy()
  })
})

describe('thread header — Stop keeps the confirm setting', () => {
  it('asks first when the setting says so, and stops on confirm', async () => {
    const user = userEvent.setup()
    renderThread()
    await user.click(within(header()).getByRole('button', { name: 'Stop' }))
    expect(ops.stop).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: 'Stop the session' }),
    )
    await waitFor(() =>
      expect(ops.stop).toHaveBeenCalledWith('run-1', undefined),
    )
  })

  it('stops at once when the person turned the question off', async () => {
    const user = userEvent.setup()
    useClientSettings.setState({ confirmStop: false })
    renderThread()
    await user.click(within(header()).getByRole('button', { name: 'Stop' }))
    await waitFor(() =>
      expect(ops.stop).toHaveBeenCalledWith('run-1', undefined),
    )
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('thread header — the ⋯ menu', () => {
  it('toasts a copied reference only once the clipboard took it', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy reference' }))
    })
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    await new Promise((r) => setTimeout(r, 20))
    expect(toasts.success).not.toHaveBeenCalled()
  })

  it('toasts a copied reference when the clipboard took it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy reference' }))
    })
    await waitFor(() => expect(toasts.success).toHaveBeenCalledTimes(1))
  })

  it('holds Full controls, Copy reference and In a terminal', async () => {
    const { onOpenDetail } = renderThread()
    openMenu()
    const items = await screen.findAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual(
      expect.arrayContaining([
        'Full controls',
        'Copy reference',
        'In a terminal',
      ]),
    )
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Full controls' }))
    })
    expect(onOpenDetail).toHaveBeenCalledTimes(1)
  })

  it('opens the terminal commands of the session from In a terminal', async () => {
    renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'In a terminal' }))
    })
    const dialog = await screen.findByTestId('narrative-cli')
    expect(dialog).toHaveTextContent('olivares session follow run-1 -o json')
    expect(dialog).toHaveTextContent('olivares session stop run-1')
  })

  it('copies the session reference', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Copy reference' }))
    })
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    expect(String(writeText.mock.calls[0][0])).toContain('sess-a')
  })

  it('brings the Context pane forward from the menu, for a screen with one pane at a time', async () => {
    const { onOpenContext } = renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Context' }))
    })
    expect(onOpenContext).toHaveBeenCalledTimes(1)
  })
})
