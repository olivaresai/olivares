// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ESTATE SHEET (#2549): a running session's real conversation streams into it as the
// tool writes, its header carries every fact on a line of its own, Stop asks first, and
// "Open the session" goes to the full page by the same address.
import axe from 'axe-core'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  act,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { useClientSettings } from '@/features/settings/preferences'
import { mergeSessions } from './provenance'
import type { SessionResolution } from './use-session-resolution'
import './i18n'

const onFrame = vi.hoisted(() => ({
  current: null as
    ((f: { seq: number; stream: string; line: string }) => void) | null,
}))
const stop = vi.hoisted(() => vi.fn())
const resolved = vi.hoisted(() => ({
  current: null as SessionResolution | null,
  asked: [] as unknown[],
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'demo', can: () => true }),
}))
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: { seq: number; stream: string; line: string }) => void
  }) => {
    onFrame.current = opts.onFrame
    return { status: 'open', ended: false, ioUnavailable: null, retry: vi.fn() }
  },
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    stop,
    resume: vi.fn(),
    interrupt: vi.fn(),
    runMessages: vi.fn().mockResolvedValue({ items: [] }),
    runActivity: vi.fn().mockResolvedValue({ items: [] }),
  },
}))
// The resolution is the engine's answer, read once by the sheet: the double hands back
// the session the test built and records which address it was asked for.
vi.mock('./use-session-resolution', () => ({
  useSessionResolution: (target: unknown) => {
    resolved.asked.push(target)
    return resolved.current
  },
}))

import { SessionSheet } from './session-sheet'

const run = {
  run_ref: 'run_1',
  name: 'Refactor cart totals',
  tenant_id: 'demo',
  transport: 'stream-json',
  provider_driver: 'claude',
  state: 'running',
  workspace_path: '/srv/work/storefront-web',
  cost_micro_usd: 1_420_000,
} as unknown as RunDTO

function resolutionOf(r: RunDTO): SessionResolution {
  const session = mergeSessions([], [r])[0]!
  return {
    target: { runRef: r.run_ref },
    session,
    live: undefined,
    runs: [r],
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
  }
}

function push(seq: number, line: unknown) {
  act(() =>
    onFrame.current?.({ seq, stream: 'stdout', line: JSON.stringify(line) }),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  stop.mockResolvedValue({})
  onFrame.current = null
  resolved.current = resolutionOf(run)
  resolved.asked = []
  useClientSettings.setState({ confirmStop: true })
})

const sheet = () => screen.getByTestId('session-sheet')

describe('SessionSheet', () => {
  it('stays closed without an address, and for one that does not parse', () => {
    const { rerender } = renderIntel(
      <SessionSheet address={null} onClose={vi.fn()} />,
    )
    expect(screen.queryByTestId('session-sheet')).toBeNull()
    rerender(<SessionSheet address="nope" onClose={vi.fn()} />)
    expect(screen.queryByTestId('session-sheet')).toBeNull()
  })

  it('streams the running session’s conversation as the tool writes it', async () => {
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    expect(resolved.asked).toContainEqual({ runRef: 'run_1' })
    // The sheet is a dialog named by the session.
    expect(screen.getByRole('dialog', { name: 'Refactor cart totals' })).toBe(
      sheet(),
    )
    const log = within(sheet()).getByRole('log')
    push(1, {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'text', text: 'Reading the cart totals first.' }],
      },
    })
    expect(
      await within(log).findByText('Reading the cart totals first.'),
    ).toBeInTheDocument()
    push(2, {
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: 'toolu_1',
            name: 'Read',
            input: { file_path: 'src/cart/totals.ts' },
          },
        ],
      },
    })
    await waitFor(() =>
      expect(
        within(log)
          .getAllByTestId('conversation-item')
          .some((el) => el.getAttribute('data-kind') === 'tool'),
      ).toBe(true),
    )
  })

  it('is named by the title the engine cut when the session has no name, and a name wins', () => {
    resolved.current = resolutionOf({
      ...run,
      name: undefined,
      title: 'Plan the migration',
    } as RunDTO)
    const first = renderIntel(
      <SessionSheet address="run:run_1" onClose={vi.fn()} />,
    )
    expect(screen.getByRole('dialog', { name: 'Plan the migration' })).toBe(
      sheet(),
    )
    first.unmount()
    resolved.current = resolutionOf({ ...run, title: 'Plan the migration' })
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    expect(screen.getByRole('dialog', { name: 'Refactor cart totals' })).toBe(
      sheet(),
    )
  })

  it('puts the facts on a line of their own under the name', () => {
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    const folder = within(sheet()).getByTestId('narrative-folder')
    const facts = folder.parentElement as HTMLElement
    expect(facts).toHaveAttribute('data-slot', 'thread-facts')
    expect(folder).toHaveTextContent('storefront-web')
    expect(within(facts).getByTestId('thread-tool')).toHaveTextContent(
      'Claude Code',
    )
    // No viewport breakpoint hides a fact in the sheet.
    expect(folder.className).not.toContain('hidden')
    expect(within(facts).getByTestId('thread-tool').className).not.toContain(
      'hidden',
    )
  })

  it('asks before Stop, and stops on the confirmation', async () => {
    const user = userEvent.setup()
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    await user.click(within(sheet()).getByRole('button', { name: 'Stop' }))
    expect(stop).not.toHaveBeenCalled()
    await user.click(
      await screen.findByRole('button', { name: 'Stop the session' }),
    )
    await waitFor(() => expect(stop).toHaveBeenCalledWith('run_1', undefined))
  })

  it('opens the full page by the same address', () => {
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    expect(
      within(sheet()).getByRole('link', { name: 'Open the session' }),
    ).toHaveAttribute('href', '/sessions?session=run%3Arun_1')
  })

  it('closes through the close button', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    renderIntel(<SessionSheet address="run:run_1" onClose={onClose} />)
    await user.click(within(sheet()).getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('says so, and offers no control, when the person may not read the session', () => {
    resolved.current = {
      ...resolutionOf(run),
      grants: {
        liveRead: false,
        runRead: false,
        runWrite: false,
        runAdmin: false,
      },
    }
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    expect(within(sheet()).queryByRole('log')).toBeNull()
    expect(within(sheet()).queryByRole('button', { name: 'Stop' })).toBeNull()
    expect(
      within(sheet()).getByText('Sessions not authorized'),
    ).toBeInTheDocument()
  })

  it('has no axe violations', async () => {
    renderIntel(<SessionSheet address="run:run_1" onClose={vi.fn()} />)
    const results = await axe.run(sheet(), {
      // jsdom computes no colours or layout; the browser captures check those.
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(results.violations.map((v) => v.id)).toEqual([])
  })
})
