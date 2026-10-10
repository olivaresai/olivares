// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createTestQueryClient,
  renderIntel,
  screen,
  waitFor,
} from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import { configureApiClient } from '@/lib/api/client'
import { useTenantStore } from '@/stores/tenant'
import { signInApi } from './api'
import { FirstHourSteps, StartSessionForm } from './first-hour'
import { readinessOf } from './readiness.fixture'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
    principal: { user_id: 'operator-a' },
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
}))

const RUN_NOTICE = 'Recent sessions: 2 loaded; there are more.'
const FOLDER_NOTICE = 'Registered folders: 1 loaded; there are more.'
const runs = {
  items: [
    { run_ref: 'run_2', state: 'failed', workspace_ref: 'ws_1' },
    { run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' },
  ],
  has_more: false,
}
const folders = {
  items: [
    { workspace_ref: 'ws_1', root_path: '/srv/work/repo', state: 'active' },
  ],
  has_more: false,
}

/** Only the wire is replaced: list reads use the real API client and response envelope. */
function replies(runMore: boolean, folderMore = false, failed?: string) {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const path = new URL(String(input), 'http://localhost').pathname
    if (path !== '/v1/m/sessions/runs' && path !== '/v1/m/sessions/workspaces')
      throw new Error(`Unexpected request: ${path}`)
    const isRun = path.endsWith('/runs')
    return new Response(
      JSON.stringify(
        path === failed
          ? { error: { code: 'unavailable', message: 'List unavailable' } }
          : isRun
            ? { ...runs, has_more: runMore }
            : { ...folders, has_more: folderMore },
      ),
      {
        status: path === failed ? 503 : 200,
        headers: { 'Content-Type': 'application/json' },
      },
    )
  })
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  configureApiClient({ getToken: () => null, getTenant: () => 'tnt-a' })
  vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
    driver,
    installed: true,
    signed_in: true,
  }))
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login' }),
  )
})
afterEach(() => {
  vi.restoreAllMocks()
  configureApiClient({ getToken: () => null, getTenant: () => null })
})

describe('first-hour list truncation', () => {
  it('declares the recent-session window used for setup progress even without a ready tool', async () => {
    replies(true)
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
    renderIntel(<FirstHourSteps />)
    expect(await screen.findByText('2 of 3 done')).toBeInTheDocument()
    expect(screen.getByText(RUN_NOTICE)).toBeInTheDocument()
  })

  it.each([
    [true, false, RUN_NOTICE, FOLDER_NOTICE],
    [false, true, FOLDER_NOTICE, RUN_NOTICE],
  ])(
    'declares only the truncated list in the folder form (%s, %s)',
    async (runMore, folderMore, shown, absent) => {
      replies(runMore, folderMore)
      renderIntel(<StartSessionForm />)
      await screen.findByText('/srv/work/repo')
      expect(screen.getByText(shown)).toBeInTheDocument()
      expect(screen.queryByText(absent)).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    },
  )

  it('declares both truncated responses with their loaded counts', async () => {
    replies(true, true)
    renderIntel(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    expect(screen.getByText(RUN_NOTICE)).toBeInTheDocument()
    expect(screen.getByText(FOLDER_NOTICE)).toBeInTheDocument()
  })

  it.each([
    ['setup', FirstHourSteps],
    ['form', StartSessionForm],
  ] as const)(
    'does not declare complete responses (%s)',
    async (_name, View) => {
      replies(false)
      const qc = createTestQueryClient()
      renderIntel(<View />, { queryClient: qc })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      expect(screen.queryByText(RUN_NOTICE)).not.toBeInTheDocument()
      expect(screen.queryByText(FOLDER_NOTICE)).not.toBeInTheDocument()
    },
  )

  it.each([
    ['setup', FirstHourSteps, '/v1/m/sessions/runs', RUN_NOTICE],
    ['form', StartSessionForm, '/v1/m/sessions/runs', RUN_NOTICE],
    ['form', StartSessionForm, '/v1/m/sessions/workspaces', FOLDER_NOTICE],
  ] as const)(
    'removes the notice when its list refetch fails (%s)',
    async (_name, View, failed, notice) => {
      replies(true, true)
      const qc = createTestQueryClient()
      renderIntel(<View />, { queryClient: qc })
      expect(await screen.findByText(notice)).toBeInTheDocument()
      replies(true, true, failed)
      await qc.refetchQueries()
      await waitFor(() =>
        expect(screen.queryByText(notice)).not.toBeInTheDocument(),
      )
      // A failed refetch keeps its old truncated data; error-aware notice suppression is essential.
      expect(
        qc
          .getQueryCache()
          .getAll()
          .some(
            (q) =>
              q.state.status === 'error' &&
              (q.state.data as { has_more?: boolean })?.has_more === true,
          ),
      ).toBe(true)
    },
  )
})
