// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ACTIVE AGENTS CARD IS THE SESSIONS WORKING SET (#2268). The headline used to be
// the inventory catalog's "seen recently" count, so the dashboard could say "3 active
// agents" over a Sessions list that filed 9 under Working. The card now counts the
// sessions the Sessions view files under Working, launched or observed, through the
// same join and the same rule. Each case renders the real container against the real
// join; only the API modules and auth are doubled.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import '@/features/_intel'
import type { ListResponse } from '@/lib/api/types'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from '@/features/sessions/types'
import { agentOpsApi } from '@/features/agentops/api'
import { sessionsApi } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { useWorkspaceStore } from '@/stores/workspace'
import { inventorySummaryFixture } from './fixtures'
import { ExecutiveView } from './executive-view'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const WITH_RUNS = (p: string) =>
  p === 'sessions:live:read' ||
  p === 'sessions:run:read' ||
  p === 'inventory:catalog:read'
const WITHOUT_RUNS = (p: string) =>
  p === 'sessions:live:read' || p === 'inventory:catalog:read'

function liveRow(ref: string, over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: ref,
    live_ref: `lr-${ref}`,
    attribution: 'legacy',
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: '2026-10-15T09:00:00Z',
    last_event_at: '2026-10-15T09:01:00Z',
    duration_seconds: 60,
    ...over,
  }
}

function runRow(ref: string, over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: ref,
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  }
}

const page = <T,>(items: T[]): ListResponse<T> => ({ items, has_more: false })

// Four observed rows say "active", but one of them is a launched session whose run
// answered and waits for its person: the run decides, so the Sessions view lists three
// Working. The inventory catalog says 7 active agents of its own and must not matter.
const LIVE = page([
  liveRow('observed-a'),
  liveRow('observed-b'),
  liveRow('launched-working'),
  liveRow('launched-waiting'),
  liveRow('observed-idle', { cc_state: 'idle' }),
])
const RUNS = page([
  runRow('run-working', {
    state: 'running',
    claude_session_id: 'launched-working',
  }),
  runRow('run-waiting', {
    state: 'idle',
    claude_session_id: 'launched-waiting',
  }),
])

let runs: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  authState.activeTenant = 'demo'
  authState.can = WITH_RUNS
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
  vi.spyOn(sessionsApi, 'live').mockResolvedValue(LIVE)
  vi.spyOn(inventoryApi, 'summary').mockResolvedValue({
    ...inventorySummaryFixture,
    by_kind: {
      ...inventorySummaryFixture.by_kind,
      agent: { total: 12, active: 7, stale: 5 },
    },
  })
  runs = vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(RUNS)
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function card() {
  await waitFor(() =>
    expect(screen.getByText('Active agents')).toBeInTheDocument(),
  )
  return screen.getByText('Active agents').closest('a')!
}

describe('Active agents card — the Working set of the Sessions view', () => {
  it('prints the sessions the Sessions view files under Working, not the catalog count', async () => {
    renderIntel(<ExecutiveView />)
    const tile = await card()

    // Observed a + b, and the launched session whose run is working. Not the launched
    // session that waits for its person, not the idle one, not the catalog's 7.
    expect(within(tile).getByText('3')).toBeInTheDocument()
    expect(within(tile).queryByText('7')).toBeNull()
    expect(runs).toHaveBeenCalled()
    // The caption counts the live sessions (not ended): five of them, three working.
    expect(within(tile).getByText(/^5 live ·/)).toBeInTheDocument()
  })

  it('says the same when the role cannot read runs: it counts the observed rows alone, as the Sessions view lists them', async () => {
    authState.can = WITHOUT_RUNS

    renderIntel(<ExecutiveView />)
    const tile = await card()

    expect(runs).not.toHaveBeenCalled()
    // Every observed row that says active: a, b and both launched rows.
    expect(within(tile).getByText('4')).toBeInTheDocument()
  })

  it('reads both pages at the Sessions view page size, so it joins the rows that view lists', async () => {
    renderIntel(<ExecutiveView />)
    await card()

    expect(sessionsApi.live).toHaveBeenCalledWith({ limit: 200 })
    expect(runs).toHaveBeenCalledWith({ limit: 200 }, { tenant: 'demo' })
  })

  it('is "—" with the Sessions line while the run half is in scope and could not load: a launched session is never counted by its observed row', async () => {
    runs.mockRejectedValue(new ApiError(500, 'server_error', 'boom'))

    renderIntel(<ExecutiveView />)
    const tile = await card()

    // Counting by observed rows would have said 4: the launched session whose run
    // waits for its person reads active there.
    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(within(tile).queryByText('4')).toBeNull()
    expect(within(tile).queryByText('3')).toBeNull()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      "Sessions — couldn't load",
    )
  })

  it('is "—" with the restricted line when the engine refuses the run read', async () => {
    runs.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))

    renderIntel(<ExecutiveView />)
    const tile = await card()

    expect(within(tile).getByText('—')).toBeInTheDocument()
    expect(screen.getByTestId('executive-usage-live-gap')).toHaveTextContent(
      'Sessions — restricted to your role',
    )
  })
})
