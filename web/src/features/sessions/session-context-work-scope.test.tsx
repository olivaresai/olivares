// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { act, screen, within, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'
import { createTestQueryClient, renderIntel } from '@/test/intel'
const auth = vi.hoisted(() => ({ read: true }))
beforeEach(() => {
  auth.read = true
  vi.resetAllMocks()
  vi.mocked(consoleApi.getWorkspaceByID).mockResolvedValue({
    id: 'default-workspace',
    name: 'Default',
  } as never)
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-fixture',
    principal: { user_id: 'human-fixture' },
    can: (p: string) =>
      auth.read && ['sessions:run:read', 'tenant:read'].includes(p),
  }),
}))
vi.mock('@/components/layout/tenant-label', () => ({
  useTenantLabel: () => ({
    tenant: 'tenant-fixture',
    name: 'Fixture organization',
  }),
}))
vi.mock('@/features/console/api', async (original) => ({
  ...(await original<typeof import('@/features/console/api')>()),
  consoleApi: { getWorkspaceByID: vi.fn() },
}))
import { consoleApi } from '@/features/console/api'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { SessionContextPane } from './session-context-pane'
import { mergeSessions } from './provenance'
import type { RunDTO } from '@/features/agentops/types'
import type { SessionResolution } from './use-session-resolution'
const run: RunDTO = {
  run_ref: 'run-fixture',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'idle',
  last_event_seq: 0,
  pep_provisioned: false,
  record_io: false,
  critical: false,
  process_state: 'running',
  work_scope: {
    role: 'orchestrator',
    workspace_id: '01a0ef7c-f25c-72f1-9cf8-3b5b2c8d6ab0',
    capabilities: ['work.read'],
    grant_id: '01a0ef7c-f25a-7f2b-a336-aed3df18206f',
  },
}
function mount(
  value: RunDTO | null,
  queryClient?: QueryClient,
  panel?: string,
) {
  const session = mergeSessions(
    [],
    [value ?? { ...run, work_scope: undefined }],
  )[0]
  const resolution: SessionResolution = {
    target: { runRef: 'run-fixture' },
    session: { ...session, runs: value ? [value] : [] },
    live: undefined,
    runs: value ? [value] : [],
    related: [],
    streamStatus: 'open',
    operateUnknown: !value,
    observeUnknown: true,
    loading: false,
    grants: {
      liveRead: false,
      runRead: !!value,
      runWrite: false,
      runAdmin: false,
    },
  }
  return renderIntel(
    <SessionContextPane resolution={resolution} panel={panel} />,
    {
      queryClient,
    },
  )
}
it('shows the authorized launch role/workspace separately from process and activity', () => {
  mount(run)
  const section = screen.getByTestId('context-work-scope')
  expect(within(section).getByText('Orchestrator')).toBeInTheDocument()
  expect(
    within(section).getByTitle(run.work_scope!.workspace_id),
  ).toBeInTheDocument()
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Running',
  )
  expect(screen.getByTestId('context-activity-state')).toHaveTextContent('Idle')
  expect(within(section).getByText('Read work')).toBeInTheDocument()
})
it("keeps the tool's own mode words in Details, not in the permission chip", () => {
  mount({
    ...run,
    provider_driver: 'codex',
    tool_mode: 'untrusted · dangerFullAccess',
  })
  expect(screen.getByTestId('context-tool-mode')).toHaveTextContent(
    'untrusted · dangerFullAccess',
  )
  expect(screen.getByTestId('context-details')).toContainElement(
    screen.getByTestId('context-tool-mode'),
  )
})

it('shows no tool mode row before the tool has reported one', () => {
  mount(run)
  expect(screen.queryByTestId('context-tool-mode')).toBeNull()
})
it('shows the branch changes of a session in its own worktree', () => {
  // What a session changed is the Changes tab's.
  mount({ ...run, worktree_branch: 'olivares/ab12cd34' }, undefined, 'changes')
  expect(screen.getByTestId('session-branch-changes')).toBeInTheDocument()
})

it('shows no branch changes for a session that works in its folder', () => {
  mount(run, undefined, 'changes')
  expect(screen.queryByTestId('session-branch-changes')).toBeNull()
  expect(screen.getByTestId('session-changes')).toBeInTheDocument()
})

it('does not infer a legacy run role from its current profile or selected workspace', () => {
  mount({
    ...run,
    process_state: undefined,
    work_scope: undefined,
    provider_profile_ref: 'ppf_current',
  })
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Not recorded',
  )
})
it('retains the recorded grant after stop without claiming that it is active authority', () => {
  mount({ ...run, state: 'stopped', process_state: 'stopped' })
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Orchestrator',
  )
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Stopped',
  )
  expect(
    screen.getByText(
      'Recorded for the last launch. Every action checks current authorization.',
    ),
  ).toBeInTheDocument()
})
it('withholds run scope when the operated half is not authorized', () => {
  mount(null)
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
})

it('hides a previously read run grant immediately when run-read permission is withdrawn', () => {
  auth.read = false
  mount(run)
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
})

it('names the session authorization workspace separately from its temporary folder', async () => {
  mount({
    ...run,
    authz_workspace_id: 'default-workspace',
    workspace_path: '/sessions/run-fixture',
  })
  const scope = screen.getByTestId('context-scope')
  expect(await within(scope).findByText('Default')).toBeInTheDocument()
  expect(
    within(scope).getByText('Temporary folder for this session'),
  ).toBeInTheDocument()
  expect(consoleApi.getWorkspaceByID).toHaveBeenCalledWith(
    'default-workspace',
    { signal: expect.any(AbortSignal) },
  )
  expect(within(scope).queryByText('No workspace')).toBeNull()
})

it('keeps the stored workspace reference visible when its name cannot be read', async () => {
  vi.mocked(consoleApi.getWorkspaceByID).mockRejectedValue(
    new Error('unavailable'),
  )
  const queryClient = createTestQueryClient()
  mount({ ...run, authz_workspace_id: 'default-workspace' }, queryClient)
  await waitFor(() =>
    expect(
      queryClient
        .getQueryCache()
        .findAll()
        .some((q) => q.state.status === 'error'),
    ).toBe(true),
  )
  expect(
    within(screen.getByTestId('context-scope')).getByTitle('default-workspace'),
  ).toBeInTheDocument()
})

it('does not look up or show the workspace of a run the operator cannot read', () => {
  auth.read = false
  mount({ ...run, authz_workspace_id: 'default-workspace' })
  expect(consoleApi.getWorkspaceByID).not.toHaveBeenCalled()
  expect(
    within(screen.getByTestId('context-scope')).queryByTitle(
      'default-workspace',
    ),
  ).toBeNull()
})

it.each([403, 404])(
  'hides a cached workspace name after a %s denial',
  async (status) => {
    const queryClient = createTestQueryClient()
    mount({ ...run, authz_workspace_id: 'default-workspace' }, queryClient)
    const scope = within(screen.getByTestId('context-scope'))
    expect(await scope.findByText('Default')).toBeInTheDocument()
    vi.mocked(consoleApi.getWorkspaceByID).mockRejectedValue(
      new ApiError(status, 'denied', 'Workspace is not readable'),
    )
    await act(async () => {
      await queryClient.refetchQueries({ type: 'active' })
    })
    await waitFor(() => expect(scope.queryByText('Default')).toBeNull())
    expect(scope.getByTitle('default-workspace')).toBeInTheDocument()
  },
)

it('retires the cached name when credentials rotate within the same tenant', async () => {
  const queryClient = createTestQueryClient()
  // Keep inactive entries so cache removal is proved independently of garbage collection.
  queryClient.setDefaultOptions({
    queries: { retry: false, gcTime: Infinity, staleTime: Infinity },
  })
  mount({ ...run, authz_workspace_id: 'default-workspace' }, queryClient)
  const scope = within(screen.getByTestId('context-scope'))
  expect(await scope.findByText('Default')).toBeInTheDocument()
  const oldQuery = queryClient
    .getQueryCache()
    .findAll()
    .find((q) => q.state.data)?.queryKey
  expect(oldQuery).toBeDefined()
  let resolve!: (
    workspace: Awaited<ReturnType<typeof consoleApi.getWorkspaceByID>>,
  ) => void
  vi.mocked(consoleApi.getWorkspaceByID).mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      }),
  )
  act(() =>
    useSessionStore.setState((s) => ({
      credentialGeneration: s.credentialGeneration + 1,
    })),
  )
  expect(scope.queryByText('Default')).toBeNull()
  expect(scope.getByTitle('default-workspace')).toBeInTheDocument()
  expect(queryClient.getQueryState(oldQuery!)).toBeUndefined()
  expect(consoleApi.getWorkspaceByID).toHaveBeenCalledTimes(2)
  await act(async () => {
    resolve({ id: 'default-workspace', name: 'New authorized name' } as never)
  })
  expect(await scope.findByText('New authorized name')).toBeInTheDocument()
  queryClient.clear()
})

it('aborts a pending workspace lookup on credential rotation and ignores its late answer', async () => {
  const queryClient = createTestQueryClient()
  queryClient.setDefaultOptions({ queries: { retry: false, gcTime: Infinity } })
  let signal: AbortSignal | undefined
  let resolve!: (
    workspace: Awaited<ReturnType<typeof consoleApi.getWorkspaceByID>>,
  ) => void
  vi.mocked(consoleApi.getWorkspaceByID).mockImplementationOnce((_id, opts) => {
    signal = opts?.signal
    return new Promise((done) => {
      resolve = done
    })
  })
  mount({ ...run, authz_workspace_id: 'default-workspace' }, queryClient)
  const oldQuery = queryClient
    .getQueryCache()
    .findAll()
    .find((q) => q.state.fetchStatus === 'fetching')!.queryKey
  act(() =>
    useSessionStore.setState((s) => ({
      credentialGeneration: s.credentialGeneration + 1,
    })),
  )
  const scope = within(screen.getByTestId('context-scope'))
  expect(await scope.findByText('Default')).toBeInTheDocument()
  expect(signal?.aborted).toBe(true)
  expect(queryClient.getQueryState(oldQuery)).toBeUndefined()
  await act(async () => {
    resolve({ id: 'default-workspace', name: 'Retired name' } as never)
  })
  expect(scope.queryByText('Retired name')).toBeNull()
  expect(scope.getByText('Default')).toBeInTheDocument()
  expect(queryClient.getQueryState(oldQuery)).toBeUndefined()
  queryClient.clear()
})
