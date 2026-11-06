// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
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
vi.mock('@tanstack/react-router', async () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
  Link: (await import('@/test/fake-router')).fakeRouterModule().Link,
}))

// The pages of the internal reads that feed the form are not the user's business: the
// New session dialog shows what the user chooses, never a warning chip about a list it
// does not show. Only a cut folder list is said, inside the folder picker.
const RUN_NOTICE = /Recent sessions: \d+ loaded/
const FOLDER_NOTICE = /Registered folders: \d+ loaded/
const FOLDER_HINT = 'Some registered folders are outside this list.'
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
  it.each([
    ['setup', FirstHourSteps],
    ['form', StartSessionForm],
  ] as const)(
    'shows no paging chip for cut internal lists (%s)',
    async (_name, View) => {
      replies(true, true)
      const qc = createTestQueryClient()
      renderIntel(<View />, { queryClient: qc })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      expect(screen.queryByText(RUN_NOTICE)).not.toBeInTheDocument()
      expect(screen.queryByText(FOLDER_NOTICE)).not.toBeInTheDocument()
      expect(screen.queryByText(FOLDER_HINT)).not.toBeInTheDocument()
    },
  )

  it.each([
    [true, true],
    [false, false],
  ])(
    'says a cut folder list inside the folder picker only (has_more=%s)',
    async (folderMore, shown) => {
      replies(true, folderMore)
      const user = userEvent.setup()
      renderIntel(<StartSessionForm />)
      await screen.findByRole('combobox', { name: 'Folder' })
      await user.click(screen.getByRole('combobox', { name: 'Folder' }))
      expect(screen.queryByText(FOLDER_HINT) !== null).toBe(shown)
      expect(screen.queryByText(RUN_NOTICE)).not.toBeInTheDocument()
      await user.keyboard('{Escape}')
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    },
  )
})
