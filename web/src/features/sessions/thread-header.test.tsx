// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREAD HEADER IS ONE 48 px BAR, A WINDOW TITLE: the name over one quiet line of its
// facts (folder, branch, tool, model); on the right the state in words, the cost, Interrupt
// (only while a turn runs), Stop, Context and a `⋯` menu. The badges and the working
// folder's path are the Context pane's.
import {
  act,
  cleanup,
  fireEvent,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import {
  AccountMarksContext,
  type AccountMark,
} from '@/features/agent-tools/account-marks'
import type { ProviderAccountDTO, RunDTO } from '@/features/agentops/types'
import { viewById } from '@/features/navigation/model'
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
// SessionMeter draws its cost and policy links with the router's Link; this render has no
// RouterProvider, so the fake router stands in for it (as the other console tests do).
vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)
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
  resume: vi.fn(),
  snooze: vi.fn(),
  unsnooze: vi.fn(),
  markDone: vi.fn(),
  unmarkDone: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...ops },
  }
})
const attach = vi.hoisted(() => ({
  onFrame: null as
    ((f: { seq: number; stream: string; line: string }) => void) | null,
}))
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: { seq: number; stream: string; line: string }) => void
  }) => {
    attach.onFrame = opts.onFrame
    return {
      status: 'open',
      ended: false,
      ioUnavailable: null,
      retry: () => {},
    }
  },
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

/** The accounts the console knows, by profile reference: what the badge reads. */
const accountMarks = new Map<string, AccountMark>()

function renderThread(r: RunDTO = run(), props = {}) {
  const onOpenDetail = vi.fn()
  const onOpenContext = vi.fn()
  const resolution = resolutionFor(r)
  renderIntel(
    <AccountMarksContext.Provider value={accountMarks}>
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
    </AccountMarksContext.Provider>,
  )
  return { onOpenDetail, onOpenContext }
}

const header = () =>
  document.querySelector('[data-slot="thread-header"]') as HTMLElement

/** Whether `b` comes after `a` in the page. */
const follows = (a: Node, b: Node) =>
  !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)

function openMenu() {
  act(() => {
    fireEvent.keyDown(screen.getByTestId('narrative-menu'), { key: 'Enter' })
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(agentOpsApi, 'getWorkspace').mockResolvedValue({
    workspace_ref: 'ws-demo',
    root_path: run().workspace_path!,
  } as Awaited<ReturnType<typeof agentOpsApi.getWorkspace>>)
  accountMarks.clear()
  auth.can = () => true
  useClientSettings.setState({ confirmStop: true })
  ops.interrupt.mockResolvedValue({ accepted: true })
  ops.stop.mockResolvedValue({})
  ops.resume.mockResolvedValue({})
  for (const mark of [ops.snooze, ops.unsnooze, ops.markDone, ops.unmarkDone])
    mark.mockResolvedValue({ run_ref: 'run-1', done: false })
})

describe('thread header — one line', () => {
  it('says the state, the name, the folder and the tool once', async () => {
    renderThread()
    const h = header()
    expect(h.className).toContain('h-12')
    expect(within(h).getByRole('img', { name: 'Working' })).toBeTruthy()
    expect(within(h).getByRole('heading', { level: 2 })).toHaveTextContent(
      'calc-review',
    )
    // The folder by NAME, the whole path on the hover.
    const folder = await within(h).findByTestId('narrative-folder')
    expect(folder).toHaveTextContent('demo')
    expect(folder.textContent).not.toContain('/home/olv')
    // A breadcrumb as T3 draws it: the folder above, then the session's folder.
    expect(folder.textContent).toBe('projects / demo')
    expect(folder).toHaveAttribute(
      'title',
      '/home/olv/work/W320/job-5cfa2f96/projects/demo',
    )
    expect(within(h).getByTestId('thread-tool').textContent).toBe('Codex')
  })

  // A2's window title: line one is the name; line two says where it works and with what,
  // quietly, a dot between the facts. The state is a word beside the actions on it.
  it('heads the thread with its name over one quiet line of facts', async () => {
    renderThread(run({ worktree_branch: 'review/calc' }))
    const h = header()
    const name = within(h).getByRole('heading', { level: 2 })
    const folder = await within(h).findByTestId('narrative-folder')
    const facts = folder.parentElement!
    expect(facts).toHaveAttribute('data-slot', 'thread-facts')
    const title = h.querySelector('[data-slot="thread-title"]')!
    expect(title).toContainElement(name)
    expect(title).toContainElement(facts)
    expect(title.className.split(/\s+/)).toContain('flex-col')
    expect(facts.className).toContain("[&>*+*]:before:content-['·']")
    // In the drawing's order: folder, branch, tool, model.
    const branch = within(facts).getByTestId('thread-branch')
    const tool = within(facts).getByTestId('thread-tool')
    const model = within(facts).getByTestId('thread-model')
    expect(follows(folder, branch)).toBe(true)
    expect(follows(branch, tool)).toBe(true)
    expect(follows(tool, model)).toBe(true)
  })

  it('says the state in words, with its mark, beside the actions', () => {
    renderThread()
    const h = header()
    const state = within(h).getByTestId('rail-row-state')
    expect(state).toHaveTextContent(/^Working/)
    // On the controls' side: after the name, before Stop.
    const name = within(h).getByRole('heading', { level: 2 })
    const stop = within(h).getByRole('button', { name: 'Stop' })
    expect(follows(name, state)).toBe(true)
    expect(follows(state, stop)).toBe(true)
    // A narrow pane keeps the dot alone, named by the word.
    expect(state.parentElement!.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['hidden', '@min-[40rem]/thread:inline-flex']),
    )
    const dot = within(h).getByRole('img', { name: 'Working' })
    expect(dot.className).toContain('@min-[40rem]/thread:hidden')
  })

  it('heads a session that has no name with the title the engine cut, and a name wins', () => {
    renderThread(run({ name: undefined, title: 'Plan the migration' }))
    expect(
      within(header()).getByRole('heading', { level: 2 }),
    ).toHaveTextContent('Plan the migration')
    cleanup()
    renderThread(run({ title: 'Plan the migration' }))
    expect(
      within(header()).getByRole('heading', { level: 2 }),
    ).toHaveTextContent('calc-review')
  })

  //: a launch the person did not name sends no name, and until its first message
  // gives it a title the heading read the tail of its reference in the monospaced face.
  it('heads a session with no name and no title with its tool and folder, not its reference', () => {
    renderThread(run({ name: undefined, title: undefined }))
    const title = within(header()).getByRole('heading', { level: 2 })
    expect(title).toHaveTextContent('Codex · demo')
    expect(title.className).not.toContain('font-mono')
    // The reference stays on the hover.
    expect(title.getAttribute('title')).toContain('sess-a')
  })

  it('names the account the session runs under with its dot and short label, in the header and the Context pane', () => {
    accountMarks.set('ppf_codex', { label: 'Research', accent: 'teal' })
    renderThread()
    for (const place of [header(), screen.getByTestId('context-side')]) {
      const badge = within(place).getByTestId('account-badge')
      expect(badge).toHaveTextContent('Research')
      expect(
        badge.querySelector('[data-provider-accent="teal"]'),
      ).not.toBeNull()
    }
  })

  it('shows no account badge for a profile the console cannot place', () => {
    accountMarks.set('ppf_other', { label: 'Other', accent: 'red' })
    renderThread()
    expect(within(header()).queryByTestId('account-badge')).toBeNull()
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
    expect(within(side).queryByText('Managed by Olivares')).toBeNull()
    const mode = within(side).getByTestId('narrative-mode')
    expect(mode).toHaveTextContent('Supervised')
    expect(mode.textContent).not.toContain('dangerFullAccess')
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

  it.each(['Stop', 'End session'])(
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
    const stop = within(header()).getByRole('button', { name: 'End session' })
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
      name: 'Stop',
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
    expect(within(header()).queryByRole('button', { name: 'Stop' })).toBeNull()
    expect(
      within(header()).getByRole('button', { name: 'End session' }),
    ).toBeTruthy()
  })
})

describe('thread header — Stop keeps the confirm setting', () => {
  it('asks first when the setting says so, and stops on confirm', async () => {
    const user = userEvent.setup()
    renderThread()
    await user.click(
      within(header()).getByRole('button', { name: 'End session' }),
    )
    expect(ops.stop).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: 'End the session' }),
    )
    await waitFor(() =>
      expect(ops.stop).toHaveBeenCalledWith('run-1', undefined),
    )
  })

  it('stops at once when the person turned the question off', async () => {
    const user = userEvent.setup()
    useClientSettings.setState({ confirmStop: false })
    renderThread()
    await user.click(
      within(header()).getByRole('button', { name: 'End session' }),
    )
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

  it('holds Full controls, Copy reference, Open in the editor and In a terminal', async () => {
    const { onOpenDetail } = renderThread()
    expect(within(header()).queryByRole('link', { name: /Open in/ })).toBeNull()
    openMenu()
    const items = await screen.findAllByRole('menuitem')
    expect(items.map((i) => i.textContent)).toEqual(
      expect.arrayContaining([
        'Full controls',
        'Copy reference',
        'Open in VS Code',
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
    expect(dialog).toHaveTextContent('olivares session send run-1')
  })

  it('does not recommend terminal resume for a host isolation refusal', async () => {
    renderThread(
      run({
        state: 'failed',
        reason:
          'the session was not started because its network boundary could not be set up: the host or container must allow unprivileged user and network namespaces and Landlock: operation not permitted',
      }),
    )
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'In a terminal' }))
    })
    const dialog = await screen.findByTestId('narrative-cli')
    expect(dialog).not.toHaveTextContent('olivares session resume')
    expect(dialog).not.toHaveTextContent('olivares session send')
    expect(dialog).toHaveTextContent('olivares session follow run-1')
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

  it('snoozes the session for the person, until a moment they pick', async () => {
    //: the engine keeps the snooze; the menu only names the moment.
    renderThread()
    openMenu()
    expect(
      screen.getByRole('menuitem', { name: 'Snooze for 1 day' }),
    ).toBeTruthy()
    expect(
      screen.getByRole('menuitem', { name: 'Snooze for 1 week' }),
    ).toBeTruthy()
    const before = Date.now()
    act(() => {
      fireEvent.click(
        screen.getByRole('menuitem', { name: 'Snooze for 1 hour' }),
      )
    })
    await waitFor(() => expect(ops.snooze).toHaveBeenCalledTimes(1))
    const [ref, until] = ops.snooze.mock.calls[0] as [string, string]
    expect(ref).toBe('run-1')
    const at = Date.parse(until)
    expect(at - before).toBeGreaterThanOrEqual(3_600_000 - 1_000)
    expect(at - Date.now()).toBeLessThanOrEqual(3_600_000)
  })

  it('snoozes for a week from Snooze for 1 week', async () => {
    renderThread()
    openMenu()
    const before = Date.now()
    act(() => {
      fireEvent.click(
        screen.getByRole('menuitem', { name: 'Snooze for 1 week' }),
      )
    })
    await waitFor(() => expect(ops.snooze).toHaveBeenCalledTimes(1))
    const at = Date.parse((ops.snooze.mock.calls[0] as [string, string])[1])
    expect(at - before).toBeGreaterThanOrEqual(7 * 24 * 3_600_000 - 1_000)
    expect(at - Date.now()).toBeLessThanOrEqual(7 * 24 * 3_600_000)
  })

  it('marks the session done for the person, and offers to undo each mark', async () => {
    renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Mark done' }))
    })
    await waitFor(() => expect(ops.markDone).toHaveBeenCalledWith('run-1'))
  })

  it('offers Unsnooze and Mark not done once the person set them', async () => {
    renderThread(run({ snoozed_until: '2030-01-02T09:00:00Z', done: true }))
    openMenu()
    expect(screen.queryByRole('menuitem', { name: /^Snooze for/ })).toBeNull()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Unsnooze' }))
    })
    await waitFor(() => expect(ops.unsnooze).toHaveBeenCalledWith('run-1'))
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Mark not done' }))
    })
    await waitFor(() => expect(ops.unmarkDone).toHaveBeenCalledWith('run-1'))
  })

  it('offers no mark for a released session: the engine keeps none for it', async () => {
    renderThread(run({ state: 'cleaned' }))
    openMenu()
    await screen.findAllByRole('menuitem')
    expect(screen.queryByRole('menuitem', { name: /^Snooze for/ })).toBeNull()
    expect(screen.queryByRole('menuitem', { name: 'Mark done' })).toBeNull()
  })

  it('brings the Context pane forward from the menu, for a screen with one pane at a time', async () => {
    const { onOpenContext } = renderThread()
    openMenu()
    act(() => {
      fireEvent.click(screen.getByRole('menuitem', { name: 'Details' }))
    })
    expect(onOpenContext).toHaveBeenCalledTimes(1)
  })
})

// the regression: two panes beside each other read the same "workspace · Claude Code ·
// claude-opus-5-5 · high" and nothing said which session each was. The header names the
// session, then its tool; the model and the effort are said once, by the composer.
describe('thread header — the session, then its tool', () => {
  it('says the branch, the model and what the session spent', () => {
    renderThread(
      run({ worktree_branch: 'review/calc', cost_micro_usd: 1_840_000 }),
    )
    const h = header()
    expect(within(h).getByTestId('thread-branch')).toHaveTextContent(
      'review/calc',
    )
    // A fact of the quiet line under the name, in the machine face, as drawn.
    expect(within(h).getByTestId('thread-branch').className).toContain(
      'font-mono',
    )
    // The branch is the path text: cut in its middle, its end kept.
    const name = within(h)
      .getByTestId('thread-branch')
      .querySelector('[data-slot="path-text"]')
    expect(name).toHaveTextContent('review/calc')
    // A row beside its icon, never cut at its own end: the path text shrinks and
    // keeps the end of a long branch in view.
    const branch = within(h).getByTestId('thread-branch')
    expect(branch).toHaveClass('flex', 'min-w-0')
    expect(branch).not.toHaveClass('truncate')
    expect(within(h).getByTestId('thread-model')).toHaveTextContent(
      'Stub Model',
    )
    expect(within(h).getByTestId('thread-cost')).toHaveTextContent('$1.84')
  })

  it('says no amount where the Usage place does not open for the person', () => {
    // A member sees no money: the cost shows where Usage opens, by its permission.
    const usage = viewById('usage')!.permission!
    auth.can = (permission) => permission !== usage
    renderThread(run({ cost_micro_usd: 1_840_000 }))
    expect(within(header()).queryByTestId('thread-cost')).toBeNull()
    expect(header()).not.toHaveTextContent('$')
  })

  it('names the session first and its tool after it, with no effort', () => {
    renderThread(run({ effort: 'high' }))
    const h = header()
    const title = within(h).getByRole('heading', { level: 2 })
    const tool = within(h).getByTestId('thread-tool')
    expect(title).toHaveTextContent('calc-review')
    expect(tool.textContent).toBe('Codex')
    expect(
      title.compareDocumentPosition(tool) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(h).not.toHaveTextContent(/\bhigh\b/i)
  })
})

// the regression, measured at 1280 px with two panes and Context open: each pane was 340 px
// wide, the tool and the folder could not shrink, and the session's name was squeezed to
// nothing while the controls were cut. The header measures its own pane, not the window.
describe('thread header — in a narrow pane', () => {
  it('keeps the name on its own line and gives way with the model, then the tool', async () => {
    renderThread(run({ effort: 'high' }))
    const h = header()
    expect(h.className).toContain('@container/thread')
    const tool = within(h).getByTestId('thread-tool')
    expect(tool.className).toMatch(
      /\bmin-w-0\b.*\btruncate\b|\btruncate\b.*\bmin-w-0\b/,
    )
    expect(tool.parentElement!.className).toContain(
      '@min-[26rem]/thread:inline-flex',
    )
    expect(within(h).getByTestId('thread-model').className).toContain(
      '@min-[48rem]/thread:inline',
    )
    // The folder is the first fact: it stays, and is cut before it pushes anything.
    const folder = await within(h).findByTestId('narrative-folder')
    expect(folder.className).not.toContain('hidden')
    expect(folder.className).toContain('truncate')
    // Nothing in the header follows the window's width any more.
    expect(h.innerHTML).not.toMatch(/min-\[(761|1100)px\]/)
  })
})

// the regression: the header said `opus` while every turn ran on claude-opus-4-8.
describe('thread header — the model in use', () => {
  const reply = (model: string, seq: number) =>
    act(() =>
      attach.onFrame?.({
        seq,
        stream: 'stdout',
        line: JSON.stringify({
          type: 'assistant',
          parent_tool_use_id: null,
          message: { model, content: [{ type: 'text', text: `on ${model}` }] },
        }),
      }),
    )

  it('warns with both models, in words, when the turns do not run on the one asked for', () => {
    renderThread(
      run({ provider_driver: 'claude', model_ref: 'claude-opus-5-5' }),
    )
    reply('claude-opus-5-5', 1)
    expect(screen.queryByTestId('thread-model-mismatch')).toBeNull()

    reply('claude-opus-4-8', 2)
    expect(screen.getByTestId('thread-model-mismatch')).toHaveTextContent(
      'Asked for Claude Opus 5.5, running Claude Opus 4.8',
    )
    expect(screen.getByTestId('thread-model-mismatch')).toHaveAttribute(
      'title',
      'Requested: claude-opus-5-5 · In use: claude-opus-4-8',
    )
  })

  it('does not hold a provider-bound name against the model the provider reports', () => {
    renderThread(
      run({
        provider_driver: 'claude',
        provider_auth_source: 'managed_injection',
        model_ref: 'opus',
      }),
    )
    reply('claude-opus-5-5', 1)
    expect(screen.queryByTestId('thread-model-mismatch')).toBeNull()
  })
})

it('explains an idle-policy stop and resumes with one click', async () => {
  renderThread(
    run({
      state: 'stopped',
      reason: 'idle_policy',
      claude_session_id: 'conversation-1',
    }),
  )
  expect(screen.getByText('Stopped to free memory.')).toBeInTheDocument()
  await userEvent.click(
    within(header()).getByRole('button', { name: 'Resume' }),
  )
  await waitFor(() => expect(ops.resume).toHaveBeenCalledWith('run-1'))
})

it.each([[''], ['user'], ['Stopped by Ada']])(
  'does not call a stop with reason %j a stop to free memory',
  (reason) => {
    renderThread(
      run({
        state: 'stopped',
        reason,
        claude_session_id: 'conversation-1',
      }),
    )
    expect(screen.queryByText('Stopped to free memory.')).toBeNull()
  },
)

// The usage of the account the session runs on: beside the tool, from the account list.
describe('thread header — the usage of the session account', () => {
  const account = (five: number, resets_at?: string): ProviderAccountDTO => ({
    account_ref: 'ppf_codex',
    name: 'work-codex',
    driver: 'codex',
    environment_ref: 'xenv_1',
    state: 'active',
    home_mode: 'managed',
    home_generation: 1,
    home_relative: '',
    isolation_level: 'shared',
    auth_source: 'provider_account_home',
    identity: '',
    identity_source: 'none',
    usage: [
      {
        window: 'five_hour',
        label: '5-hour',
        utilization: five,
        observed_at: '2026-10-29T10:00:00Z',
        ...(resets_at ? { resets_at } : {}),
      },
      {
        window: 'seven_day',
        label: 'Weekly',
        utilization: 17,
        observed_at: '2026-10-29T10:00:00Z',
      },
    ],
    created_at: '2026-10-01T10:00:00Z',
    updated_at: '2026-10-01T10:00:00Z',
  })

  // THE TITLE IS THE ONE THING THE HEADER MUST LET A PERSON READ: the account's
  // quota is the status bar's and the tokens are Details', not the header's, and the title
  // takes the room the actions leave and is cut there, with no width of its own to push
  // under them at 390 px. jsdom has no layout, so this pins the arrangement.
  it('keeps the account quota and the raw token totals out of the header and cuts the title where the actions begin', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [account(41)],
      has_more: false,
    })
    renderThread()
    await waitFor(() => expect(agentOpsApi.listAccounts).toHaveBeenCalled())
    // The account read answers after the call: let it render before saying it is absent.
    await act(() => new Promise((resolve) => setTimeout(resolve, 50)))
    // The header names the session, its tool and state; the account's 5-hour and weekly
    // use is the status bar's, the tokens and cost are Details'.
    expect(within(header()).queryByTestId('account-usage')).toBeNull()
    expect(header()).not.toHaveTextContent(/Weekly|5-hour|tokens/)
    expect(within(header()).queryByText(/limit reached/)).toBeNull()
    const title = within(header()).getByRole('heading', { level: 2 })
    const classes = title.className.split(/\s+/)
    expect(classes).toContain('truncate')
    expect(classes.filter((name) => name.startsWith('min-w-'))).toEqual([])
    // Its block shrinks to the room left, so the cut is inside it.
    expect(title.closest('[data-slot="thread-title"]')).toHaveClass(
      'min-w-0',
      'flex-1',
    )
  })

  it('marks the session account at its limit, with when it resets', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [account(100, '2030-01-01T10:00:00Z')],
      has_more: false,
    })
    renderThread()
    expect(await within(header()).findByRole('status')).toHaveTextContent(
      /5-hour limit reached, resets /,
    )
  })

  it('says nothing for a session whose account reported no usage', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [{ ...account(1), usage: undefined }],
      has_more: false,
    })
    renderThread()
    await waitFor(() => expect(agentOpsApi.listAccounts).toHaveBeenCalled())
    expect(within(header()).queryByTestId('account-usage')).toBeNull()
  })
})
