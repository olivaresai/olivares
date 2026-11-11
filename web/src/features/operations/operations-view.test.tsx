// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Operations: the host, its filesystems, the live sessions and the usage, on one page. Each
// section reads only what the principal may read, every chart has a table and a sentence,
// and a read that cannot answer says so instead of drawing nothing.
import type { ComponentType, ReactNode } from 'react'
import { waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FALLBACK } from '@/components/charts/chart-theme'
import { ApiError } from '@/lib/api/errors'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import '@/features/_intel'
import './i18n'

const GiB = 1024 ** 3

const state = vi.hoisted(() => ({
  perms: new Set<string>(),
  recent: {
    sessionsQ: { isError: false },
    liveAnswer: { has_more: false } as { has_more: boolean } | undefined,
    recentSessions: [] as unknown[] | undefined,
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => state.perms.has(permission),
  }),
}))
vi.mock('@/features/home/use-recent-sessions', () => ({
  useRecentSessions: () => state.recent,
}))
// recharts sizes its surface from a ResizeObserver that jsdom never fires, so a chart
// draws nothing; give each chart a fixed size so the host charts mount and their fills can be read.
vi.mock('recharts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('recharts')>()
  const sized = (Chart: ComponentType<Record<string, unknown>>) =>
    function SizedChart(props: Record<string, unknown>) {
      return <Chart width={800} height={300} {...props} />
    }
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children?: ReactNode }) => (
      <div style={{ width: 800, height: 300 }}>{children}</div>
    ),
    AreaChart: sized(
      actual.AreaChart as ComponentType<Record<string, unknown>>,
    ),
    LineChart: sized(
      actual.LineChart as ComponentType<Record<string, unknown>>,
    ),
    BarChart: sized(actual.BarChart as ComponentType<Record<string, unknown>>),
  }
})

const hostMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    operationsApi: { host: (...a: unknown[]) => hostMock(...a) },
  }
})

const usageMock = vi.fn()
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      usage: (...a: unknown[]) => usageMock(...a),
    },
  }
})
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: {
      ...actual.agentOpsApi,
      listProfiles: () => Promise.resolve({ items: [] }),
    },
  }
})

const statusMock = vi.fn()
vi.mock('@/features/health/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/health/api')>()
  return {
    ...actual,
    healthApi: {
      ...actual.healthApi,
      publicStatus: (...a: unknown[]) => statusMock(...a),
    },
  }
})

const { OperationsView } = await import('./operations-view')

const sample = (at: string, cpu: number, memoryGiB: number) => ({
  at,
  cpu_percent: cpu,
  load_1: 0.4,
  memory_used_bytes: memoryGiB * GiB,
  swap_used_bytes: 0,
})

const HOST = {
  collected_at: '2026-11-03T10:00:00Z',
  load: { one: 0.42, five: 0.5, fifteen: 0.4 },
  cpu_percent: 12,
  memory: {
    total_bytes: 16 * GiB,
    used_bytes: 6 * GiB,
    available_bytes: 10 * GiB,
  },
  swap: { total_bytes: 0, used_bytes: 0 },
  filesystems: [
    {
      mount: '/',
      device: '/dev/sda1',
      type: 'ext4',
      total_bytes: 100 * GiB,
      used_bytes: 80 * GiB,
    },
  ],
  session_folders: {
    root: '/var/lib/olivares/sessions',
    measured_at: '2026-11-03T10:00:00Z',
    total_bytes: 3 * GiB,
    folders: [
      { name: 'run_alpha', bytes: 2 * GiB },
      { name: 'run_beta', bytes: 1 * GiB },
    ],
    truncated: false,
  },
  history: [
    sample('2026-11-03T09:58:00Z', 10, 5),
    sample('2026-11-03T09:59:00Z', 30, 6),
    sample('2026-11-03T10:00:00Z', 12, 6),
  ],
}

// A merged session as the Sessions page builds it: its run says the tool and whom it acts for.
const session = (
  key: string,
  driver: string,
  runState: string,
  actsFor: { kind: string; user_id?: string; name?: string },
) => ({
  key,
  runs: [
    {
      run_ref: key,
      state: runState,
      provider_driver: driver,
      acts_for: actsFor,
    },
  ],
  provenance: 'launched',
  control: 'full',
  lastActivityMs: 0,
})

const ANA = { kind: 'user', user_id: 'u1', name: 'Ana' }

// What `GET /status` answers: each component the engine reports and its state.
const statusOf = (states: Record<string, string>) => ({
  status: 'operational',
  components: Object.entries(states).map(([name, status]) => ({
    name,
    status,
  })),
  timestamp: '2026-11-03T10:00:00Z',
})

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date('2026-11-03T10:00:00Z'))
  state.perms = new Set()
  state.recent = {
    sessionsQ: { isError: false },
    liveAnswer: { has_more: false },
    recentSessions: [],
  }
  hostMock.mockReset().mockResolvedValue(HOST)
  usageMock.mockReset().mockResolvedValue({ group_by: 'model', items: [] })
  statusMock
    .mockReset()
    .mockResolvedValue(statusOf({ api: 'operational', store: 'operational' }))
})

afterEach(() => vi.useRealTimers())

describe('Operations: the components', () => {
  it('says everything is working when every component works', async () => {
    state.perms = new Set(['health:status:read'])
    renderIntel(<OperationsView />)

    expect(await screen.findByText('Everything is working.')).toHaveAttribute(
      'role',
      'status',
    )
  })

  it('says what is configured works, names what is not configured, and lists each state word', async () => {
    state.perms = new Set(['health:status:read'])
    statusMock.mockResolvedValue(
      statusOf({
        api: 'operational',
        store: 'operational',
        knowledge: 'not_configured',
      }),
    )
    renderIntel(<OperationsView />)

    expect(
      await screen.findByText(
        'Everything that is configured is working. Not configured: Knowledge retrieval.',
      ),
    ).toHaveAttribute('role', 'status')
    const list = screen.getByRole('list', { name: 'Components' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent(/Database.*Operational/)
    expect(rows[2]).toHaveTextContent(/Knowledge retrieval.*Not configured/)
  })

  it('names each component that is not working, and leaves out an optional one not set up', async () => {
    state.perms = new Set(['health:status:read'])
    statusMock.mockResolvedValue(
      statusOf({
        api: 'operational',
        store: 'outage',
        connectors: 'degraded',
        knowledge: 'not_configured',
      }),
    )
    renderIntel(<OperationsView />)

    expect(
      await screen.findByText('Needs attention: Database, Connectors.'),
    ).toHaveAttribute('role', 'status')
    expect(screen.queryByText('Everything is working.')).toBeNull()
    const list = screen.getByRole('list', { name: 'Components' })
    expect(within(list).getByText('Outage')).toBeInTheDocument()
    expect(within(list).getByText('Degraded')).toBeInTheDocument()
  })

  it('names a component in a state this build cannot name, rather than calling it fine', async () => {
    state.perms = new Set(['health:status:read'])
    statusMock.mockResolvedValue(
      statusOf({ api: 'operational', store: 'weird' }),
    )
    renderIntel(<OperationsView />)

    expect(
      await screen.findByText('Needs attention: Database.'),
    ).toHaveAttribute('role', 'status')
    expect(screen.queryByText(/is working/)).toBeNull()
  })

  it('shows the failure with Retry when the status cannot be read, never that all is well', async () => {
    state.perms = new Set(['health:status:read'])
    statusMock.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    renderIntel(<OperationsView />)

    expect(
      await screen.findByRole('button', { name: /retry/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText('Everything is working.')).toBeNull()
    expect(screen.queryByRole('list', { name: 'Components' })).toBeNull()
  })

  it('does not read the components without the health read', async () => {
    state.perms = new Set(['sessions:run:read'])
    renderIntel(<OperationsView />)
    await vi.waitFor(() => expect(usageMock).toHaveBeenCalled())
    expect(statusMock).not.toHaveBeenCalled()
    expect(screen.queryByRole('heading', { name: 'Components' })).toBeNull()
  })
})

describe('Operations: the host', () => {
  it('charts CPU and memory with a sentence each, and the disks and folders as tables', async () => {
    state.perms = new Set(['system:admin'])
    const user = userEvent.setup()
    renderIntel(<OperationsView />)

    expect(
      await screen.findByRole('img', { name: /CPU.*peak 30%/ }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('img', { name: /Memory.*6 GiB of 16 GiB/ }),
    ).toBeInTheDocument()

    const disks = screen.getByRole('figure', { name: 'Filesystems' })
    await user.click(within(disks).getByRole('button', { name: /table/i }))
    expect(
      within(disks).getByRole('row', { name: /\/dev\/sda1.*80%/ }),
    ).toBeInTheDocument()

    const folders = screen.getByRole('figure', { name: 'Session folders' })
    await user.click(within(folders).getByRole('button', { name: /table/i }))
    expect(
      within(folders).getByRole('row', { name: /run_alpha.*2 GiB/ }),
    ).toBeInTheDocument()
  })

  it('fills the CPU and memory areas with the neutral data color, not the brand orange', async () => {
    state.perms = new Set(['system:admin'])
    renderIntel(<OperationsView />)

    await screen.findByRole('img', { name: /CPU.*peak 30%/ })
    const cpuFill = document
      .querySelector('#grad-cpu_percent stop')
      ?.getAttribute('stop-color')
    expect(cpuFill).toBe(FALLBACK.slate)
    expect(cpuFill).not.toBe(FALLBACK.accent)
  })

  it('distinguishes memory and swap with separate non-orange data colors', async () => {
    state.perms = new Set(['system:admin'])
    renderIntel(<OperationsView />)

    const memory = await screen.findByRole('figure', { name: 'Memory' })
    const memoryColor = memory
      .querySelector('#grad-memory_used_bytes stop')
      ?.getAttribute('stop-color')
    const swapColor = memory
      .querySelector('#grad-swap_used_bytes stop')
      ?.getAttribute('stop-color')
    expect(memoryColor).toBe(FALLBACK.slate)
    expect(swapColor).toBe(FALLBACK.teal)
    expect(swapColor).not.toBe(memoryColor)
    expect(swapColor).not.toBe(FALLBACK.accent)
  })

  it('fills the filesystem bars with the neutral data color, not the brand orange', async () => {
    state.perms = new Set(['system:admin'])
    renderIntel(<OperationsView />)

    const disks = await screen.findByRole('figure', { name: 'Filesystems' })
    const barFills = await waitFor(() => {
      const fills = Array.from(
        disks.querySelectorAll('.recharts-bar-rectangle path'),
      ).map((bar) => bar.getAttribute('fill'))
      expect(fills.length).toBeGreaterThan(0)
      return fills
    })
    expect(barFills.every((fill) => fill === FALLBACK.slate)).toBe(true)
  })

  it('says a folder size is a minimum when the measurement stopped at its bound', async () => {
    state.perms = new Set(['system:admin'])
    hostMock.mockResolvedValue({
      ...HOST,
      session_folders: { ...HOST.session_folders, truncated: true },
    })
    renderIntel(<OperationsView />)
    expect(await screen.findByText(/sizes are a minimum/i)).toBeInTheDocument()
  })

  it('says the host cannot be read where the engine answers 501, and still shows the rest', async () => {
    state.perms = new Set(['system:admin', 'sessions:run:read'])
    hostMock.mockRejectedValue(new ApiError(501, 'not_implemented', 'no'))
    renderIntel(<OperationsView />)

    expect(
      await screen.findByText(/not available on this host/i),
    ).toBeInTheDocument()
    await vi.waitFor(() => expect(usageMock).toHaveBeenCalled())
  })

  it('does not ask for the host unless the person administers the system', async () => {
    state.perms = new Set(['sessions:run:read'])
    renderIntel(<OperationsView />)
    await vi.waitFor(() => expect(usageMock).toHaveBeenCalled())
    expect(hostMock).not.toHaveBeenCalled()
    expect(screen.queryByRole('heading', { name: 'Host' })).toBeNull()
  })
})

describe('Operations: live sessions', () => {
  it('counts the live sessions by tool and by person, and leaves out the ended ones', async () => {
    state.perms = new Set(['sessions:live:read'])
    state.recent.recentSessions = [
      session('a', 'claude', 'running', ANA),
      session('b', 'claude', 'idle', ANA),
      session('c', 'codex', 'running', { kind: 'automation' }),
      session('d', 'claude', 'stopped', {
        kind: 'user',
        user_id: 'u2',
        name: 'Bo',
      }),
    ]
    const user = userEvent.setup()
    renderIntel(<OperationsView />)

    expect(
      await screen.findByRole('img', { name: /3 live sessions by tool/ }),
    ).toBeInTheDocument()

    const byTool = screen.getByRole('figure', { name: 'Live sessions by tool' })
    await user.click(within(byTool).getByRole('button', { name: /table/i }))
    expect(
      within(byTool).getByRole('row', { name: /Claude Code.*2/ }),
    ).toBeInTheDocument()
    expect(
      within(byTool).getByRole('row', { name: /Codex.*1/ }),
    ).toBeInTheDocument()

    const byPerson = screen.getByRole('figure', {
      name: 'Live sessions by person',
    })
    await user.click(within(byPerson).getByRole('button', { name: /table/i }))
    expect(
      within(byPerson).getByRole('row', { name: /Ana.*2/ }),
    ).toBeInTheDocument()
    expect(
      within(byPerson).getByRole('row', { name: /Automation.*1/ }),
    ).toBeInTheDocument()
    expect(within(byPerson).queryByRole('row', { name: /Bo/ })).toBeNull()
  })

  it('says no session is live rather than drawing an empty chart', async () => {
    state.perms = new Set(['sessions:live:read'])
    renderIntel(<OperationsView />)
    expect(await screen.findByText(/No live sessions/i)).toBeInTheDocument()
  })

  it('says the count is a floor when the live page was cut', async () => {
    state.perms = new Set(['sessions:live:read'])
    state.recent.liveAnswer = { has_more: true }
    state.recent.recentSessions = [session('a', 'claude', 'running', ANA)]
    renderIntel(<OperationsView />)
    expect(await screen.findByText(/more are open/i)).toBeInTheDocument()
  })
})

describe('Operations: the range', () => {
  it('asks for usage over the last 24 hours, then 30 days, from one control', async () => {
    state.perms = new Set(['sessions:run:read'])
    const user = userEvent.setup()
    renderIntel(<OperationsView />)

    await vi.waitFor(() =>
      expect(usageMock).toHaveBeenCalledWith(
        expect.objectContaining({ since: '2026-11-02T10:00:00Z' }),
      ),
    )
    // Usage no longer carries a second period control of its own.
    expect(screen.queryByRole('radiogroup', { name: 'Period' })).toBeNull()

    await user.click(screen.getByRole('radio', { name: '30 days' }))
    await vi.waitFor(() =>
      expect(usageMock).toHaveBeenCalledWith(
        expect.objectContaining({ since: '2026-10-05T00:00:00Z' }),
      ),
    )
  })

  it('says the host keeps 24 hours when a longer range is chosen', async () => {
    state.perms = new Set(['system:admin'])
    const user = userEvent.setup()
    renderIntel(<OperationsView />)
    await screen.findByRole('img', { name: /CPU/ })
    expect(screen.queryByText(/keeps 24 hours/i)).toBeNull()

    await user.click(screen.getByRole('radio', { name: '7 days' }))
    expect(screen.getByText(/keeps 24 hours/i)).toBeInTheDocument()
  })
})

describe('Operations: the usage', () => {
  it('tells the cost only to a person Usage opens for: no $, no cost, no spend for anyone else', async () => {
    usageMock.mockImplementation((q: { bucket?: string }) =>
      Promise.resolve({
        group_by: 'model',
        items: q.bucket
          ? []
          : [
              {
                key: 'claude-opus-5-5',
                runs: 3,
                input_tokens: 1200,
                output_tokens: 300,
                cost_micro_usd: 1_500_000,
                runs_without_tokens: 0,
                runs_without_cost: 0,
              },
            ],
      }),
    )
    state.perms = new Set(['sessions:run:read', 'sessions:account:read'])
    const shown = renderIntel(<OperationsView />)
    const priced = await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(within(priced).getByText('$1.50')).toBeInTheDocument()
    shown.unmount()

    state.perms = new Set(['sessions:run:read'])
    renderIntel(<OperationsView />)
    const opus = await screen.findByRole('row', { name: /claude-opus-5-5/ })
    expect(within(opus).getByText('3')).toBeInTheDocument()
    expect(screen.queryByRole('columnheader', { name: /cost/i })).toBeNull()
    expect(document.body).not.toHaveTextContent(/\$|cost|spend/i)
  })
})
