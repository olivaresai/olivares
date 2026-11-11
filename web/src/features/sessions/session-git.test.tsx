// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { gitpublishApi } from '@/features/gitpublish/api'
import { SessionPublish } from '@/features/gitpublish/session-publish'
import type {
  PublicationIntent,
  PublicationTarget,
} from '@/features/gitpublish/types'
import { governanceApi } from '@/features/governance/api'
import type { ApprovalDTO } from '@/features/governance/types'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { chordHint } from '@/lib/keybindings/model'
import { useModulesStore } from '@/stores/modules'
import { SessionGit } from './session-git'
import { pushableHead, type GitStatus } from './session-push'

let writable = true
let activeTenant = 't1'
// The permissions a test withholds from an otherwise writable person.
let withheld: string[] = []
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant,
    can: (permission: string) => writable && !withheld.includes(permission),
  }),
}))
// Whether the Git publishing page opens for this person.
let publishingOpens = true
vi.mock('@/features/navigation/authorization', () => ({
  useViewAccess: () => ({ navigable: () => publishingOpens }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { gitStatus: vi.fn(), gitAction: vi.fn(), getRun: vi.fn() },
}))
const run = {
  run_ref: 'run_1',
  state: 'stopped',
  workspace_path: '/repo',
} as RunDTO
const status = {
  branch: 'main',
  branches: ['main', 'topic'],
  writable: true,
  files: [{ path: 'README.md', index: ' ', worktree: 'M' }],
  truncated: false,
}
function show(value: RunDTO = run) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <SessionGit run={value} />
    </QueryClientProvider>,
  )
  return { ...view, client }
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.restoreAllMocks()
  writable = true
  withheld = []
  publishingOpens = true
  activeTenant = 't1'
  useModulesStore.getState().setOff([])
  vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(status)
  vi.mocked(agentOpsApi.gitAction).mockResolvedValue({ ok: true })
  vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [] })
  vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [] })
})
describe('Session Git', () => {
  it('shows status and stages the selected file', async () => {
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'stage',
        {
          paths: ['README.md'],
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    expect(await screen.findByLabelText('Branch')).toHaveValue('main')
  })
  it('offers Initialize Git for a folder that is not a repository, in the panel and as the header button', async () => {
    const { ApiError } = await import('@/lib/api/errors')
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new ApiError(
        404,
        'not_found',
        'this session folder is not a Git repository',
      ),
    )
    const user = userEvent.setup()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={run} variant="init" />
      </QueryClientProvider>,
    )
    await user.click(
      await screen.findByRole('button', { name: 'Initialize Git' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'init',
        {},
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })
  it('shows no Initialize Git where the folder is a repository', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={run} variant="init" />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(agentOpsApi.gitStatus).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: 'Initialize Git' }),
    ).not.toBeInTheDocument()
  })
  it('unstages without discarding the working file', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Unstage README.md' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'unstage',
        {
          paths: ['README.md'],
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
  })
  it('commits staged changes with a message and clears it after success', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    const message = await screen.findByLabelText('Commit message')
    expect(screen.getByRole('button', { name: 'Commit' })).toBeDisabled()
    await user.type(message, 'Explain this change')
    await user.click(screen.getByRole('button', { name: 'Commit' }))
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'commit',
        {
          message: 'Explain this change',
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    await waitFor(() => expect(message).toHaveValue(''))
  })
  it('switches to a local branch and creates a new branch', async () => {
    const user = userEvent.setup()
    show()
    await user.selectOptions(await screen.findByLabelText('Branch'), 'topic')
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'branch',
        {
          name: 'topic',
          create: false,
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
    await user.click(screen.getByRole('button', { name: 'New branch' }))
    await user.type(screen.getByLabelText('Branch name'), 'session/topic')
    await user.click(screen.getByRole('button', { name: 'Create branch' }))
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'branch',
        {
          name: 'session/topic',
          create: true,
        },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      ),
    )
  })
  it('keeps failures visible and preserves an unsubmitted commit message', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    vi.mocked(agentOpsApi.gitAction).mockRejectedValue(
      new Error('Git refused the commit'),
    )
    const user = userEvent.setup()
    show()
    await user.type(
      await screen.findByLabelText('Commit message'),
      'Keep this message',
    )
    await user.click(screen.getByRole('button', { name: 'Commit' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Git refused the commit',
    )
    expect(screen.getByLabelText('Commit message')).toHaveValue(
      'Keep this message',
    )
  })
  it('disables writes for read-only sessions and for readers', async () => {
    writable = false
    show()
    expect(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    ).toBeDisabled()
    expect(screen.getByLabelText('Branch')).toBeDisabled()
    // The panel's read-only sentence is Commit's reason, said once.
    expect(
      screen.getByRole('button', { name: 'Commit' }),
    ).toHaveAccessibleDescription(
      'Git changes are unavailable with this session’s permissions.',
    )
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
  })
  it('shows unavailable and status errors explicitly', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new Error('Git status unavailable'),
    )
    show()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Git status unavailable',
    )
  })
  it('reads the new index after staging rather than painting an optimistic result', async () => {
    vi.mocked(agentOpsApi.gitStatus)
      .mockResolvedValueOnce(status)
      .mockResolvedValue({
        ...status,
        files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
      })
    const user = userEvent.setup()
    show()
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    expect(
      await screen.findByRole('button', { name: 'Unstage README.md' }),
    ).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Stage README.md' }),
    ).not.toBeInTheDocument()
  })
  it('asks for the Git list again when a turn ends', async () => {
    const { client, rerender } = show({ ...run, state: 'running' })
    await screen.findByLabelText('Branch')
    expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(1)
    // The engine shows a session that answered its turn and waits for the person as idle.
    rerender(
      <QueryClientProvider client={client}>
        <SessionGit run={{ ...run, state: 'idle' }} />
      </QueryClientProvider>,
    )
    // Well inside the 5 s poll: only the end of the turn can ask.
    await waitFor(() => expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(2))
  })
  it('marks the uncommitted changes to be read again when a turn ends', async () => {
    const { client, rerender } = show({ ...run, state: 'running' })
    await screen.findByLabelText('Branch')
    const diff = agentOpsKeys.runWorktreeDiff('t1', run.run_ref)
    client.setQueryData(diff, { files: [{ path: 'README.md' }] })
    rerender(
      <QueryClientProvider client={client}>
        <SessionGit run={{ ...run, state: 'idle' }} />
      </QueryClientProvider>,
    )
    // A list read before the turn ended may hold files the turn has since committed.
    await waitFor(() =>
      expect(client.getQueryState(diff)?.isInvalidated).toBe(true),
    )
  })
  it('with nothing staged, Commit is disabled and says why', async () => {
    vi.mocked(agentOpsApi.gitStatus)
      .mockResolvedValueOnce(status)
      .mockResolvedValue({
        ...status,
        files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
      })
    const user = userEvent.setup()
    show()
    const commit = () => screen.getByRole('button', { name: 'Commit' })
    await screen.findByRole('button', { name: 'Stage README.md' })
    expect(commit()).toBeDisabled()
    expect(commit()).toHaveAccessibleDescription(
      'Nothing is staged yet. Stage a file to commit it.',
    )
    await user.click(screen.getByRole('button', { name: 'Stage README.md' }))
    await waitFor(() =>
      expect(commit()).toHaveAccessibleDescription('Write a commit message.'),
    )
    await user.type(screen.getByLabelText('Commit message'), 'Explain it')
    expect(commit()).toBeEnabled()
    expect(commit()).not.toHaveAccessibleDescription()
  })
  it('honors the server folder policy even for a console writer', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      writable: false,
    })
    show()
    expect(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    ).toBeDisabled()
  })
  it('keeps a focused draft usable during background status refresh', async () => {
    const user = userEvent.setup()
    const { client } = show()
    const input = await screen.findByLabelText('Commit message')
    await user.type(input, 'Keep ')
    vi.mocked(agentOpsApi.gitStatus).mockImplementation(
      () => new Promise(() => {}),
    )
    await act(async () => {
      void client.invalidateQueries({
        predicate: (q) => q.queryKey.includes('git'),
      })
    })
    expect(input).toBeEnabled()
    expect(input).toHaveFocus()
    await user.type(input, 'typing')
    expect(input).toHaveValue('Keep typing')
  })
  it.each(['run', 'tenant'] as const)(
    'retires drafts at the %s boundary',
    async (boundary) => {
      const user = userEvent.setup()
      const { client, rerender } = show()
      await user.type(
        await screen.findByLabelText('Commit message'),
        'Private draft',
      )
      if (boundary === 'tenant') activeTenant = 't2'
      rerender(
        <QueryClientProvider client={client}>
          <SessionGit
            run={boundary === 'run' ? { ...run, run_ref: 'run_2' } : run}
          />
        </QueryClientProvider>,
      )
      expect(await screen.findByLabelText('Commit message')).toHaveValue('')
    },
  )
  it.each(['active', 'ended'] as const)(
    'uses the current %s work lease at dispatch',
    async (state) => {
      const stamped = {
        ...run,
        work_item_id: 'wi',
        work_lease_fence: 6,
        work_lease_state: 'active' as const,
      }
      vi.mocked(agentOpsApi.getRun).mockResolvedValue({
        ...stamped,
        work_lease_state: state,
      })
      const user = userEvent.setup()
      show(stamped)
      await user.click(
        await screen.findByRole('button', { name: 'Stage README.md' }),
      )
      await waitFor(() =>
        expect(agentOpsApi.getRun).toHaveBeenCalledWith(
          'run_1',
          expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
        ),
      )
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'stage',
        state === 'active'
          ? { paths: ['README.md'], work_lease_fence: 6 }
          : { paths: ['README.md'] },
        expect.objectContaining({ tenant: 't1', sessionEffects: 'none' }),
      )
    },
  )
  it('abandons a pending fenced action when its owner unmounts', async () => {
    let answer!: (value: RunDTO) => void
    vi.mocked(agentOpsApi.getRun).mockImplementation(
      () =>
        new Promise((resolve) => {
          answer = resolve
        }),
    )
    const user = userEvent.setup()
    const { unmount } = show({
      ...run,
      work_item_id: 'wi',
      work_lease_fence: 6,
    })
    await user.click(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    )
    await waitFor(() => expect(agentOpsApi.getRun).toHaveBeenCalled())
    unmount()
    await act(async () =>
      answer({
        ...run,
        work_item_id: 'wi',
        work_lease_fence: 6,
        work_lease_state: 'active',
      }),
    )
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
  })
})

const SHA = 'a'.repeat(40)
const TREE = 'b'.repeat(40)
const target: PublicationTarget = {
  id: 'tg-1',
  workspace_id: 'ws-1',
  host: 'github',
  push_prefix: 'agent/',
  merge_bases: ['main', 'release'],
  version: 2,
}
// A session in the target's workspace whose folder is clean and has a commit.
const bound = { ...run, name: 'export button', authz_workspace_id: 'ws-1' }
const committed = { ...status, files: [], head: { commit: SHA, tree: TREE } }
const PR_URL = 'https://github.com/acme/widgets/pull/7'

function intent(over: Partial<PublicationIntent> = {}): PublicationIntent {
  return {
    id: 'in-1',
    target_id: 'tg-1',
    target_version: 2,
    effect: 'push',
    operation_id: 'console:op',
    attempt: 1,
    state: 'applied',
    receipt: 'caused',
    requested: { ref: 'refs/heads/agent/main', commit: SHA, tree: TREE },
    observed: { present: true, sha: SHA, merged: false },
    acknowledged: { acknowledged: true, status: 200 },
    ...over,
  }
}
const opened = intent({
  id: 'in-2',
  effect: 'pull_request',
  requested: { head_ref: 'agent/main', base: 'main', commit: SHA },
  observed: { present: true, number: 7, merged: false, url: PR_URL },
})

/** The sentence that says the branch is pushed: whole in its paragraph, and its branch a
 * PathText, whole on hover, as every branch the console shows. */
async function findPushed(sentence: string) {
  const said = await screen.findByText(
    (_, node) => node?.tagName === 'P' && node.textContent === sentence,
  )
  const branch = said.querySelector('[data-slot="path-text"] code')
  expect(branch).toHaveAttribute('title', 'agent/main')
  return said
}

describe('Pushing and opening a pull request from the Changes panel', () => {
  beforeEach(() => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(committed)
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [target] })
  })

  it('offers Push for a target bound to the run’s workspace while a commit is ahead', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [{ ...target, id: 'tg-2', workspace_id: 'ws-2' }, target],
    })
    show(bound)
    expect(await screen.findByRole('button', { name: 'Push' })).toBeEnabled()
    expect(gitpublishApi.intents).toHaveBeenCalledWith('tg-1')
    // One target in the run's workspace: Push needs no word on where it goes.
    expect(screen.queryByText(/^To GitHub/)).toBeNull()
  })

  it('serves the first target of the run’s workspace, names it, and leaves the others whole to the publish block', async () => {
    const other = { ...target, id: 'tg-2', push_prefix: 'other/' }
    const OTHER_URL = 'https://github.com/acme/widgets/pull/9'
    const theirs = intent({
      id: 'in-9',
      target_id: 'tg-2',
      effect: 'pull_request',
      requested: { head_ref: 'other/main', base: 'main', commit: SHA },
      observed: { present: true, number: 9, merged: false, url: OTHER_URL },
    })
    const MERGED_URL = 'https://github.com/acme/widgets/pull/5'
    const earlier = intent({
      id: 'in-5',
      effect: 'pull_request',
      requested: {
        head_ref: 'agent/main',
        base: 'main',
        commit: 'c'.repeat(40),
      },
      observed: { present: true, number: 5, merged: true, url: MERGED_URL },
    })
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [target, other],
    })
    vi.spyOn(gitpublishApi, 'intents').mockImplementation(async (id) => ({
      items: id === 'tg-1' ? [opened, earlier] : [theirs],
    }))
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(intent())
    const user = userEvent.setup()
    const session = { ...bound, worktree_branch: 'main' }
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    // As the Changes pane mounts them: the Git panel, then the publish block.
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={session} />
        <SessionPublish run={session} gitPanel />
      </QueryClientProvider>,
    )
    // The panel says where Push goes when the workspace has several targets,
    // and Push carries it as its description.
    expect(
      await screen.findByText('To GitHub, under agent/'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Push' }),
    ).toHaveAccessibleDescription('To GitHub, under agent/')
    // On the row the panel serves, the block drops only Publish, which the
    // panel's Push does: its pull requests stay links, a merged one too.
    expect(
      await screen.findByRole('button', { name: 'Ship to agent/' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Publish to agent/' }),
    ).toBeNull()
    expect(
      await screen.findByRole('link', { name: 'Pull request #5' }),
    ).toHaveAttribute('href', MERGED_URL)
    expect(
      screen.getByRole('link', { name: 'Pull request #7' }),
    ).toHaveAttribute('href', PR_URL)
    // The block lists it once; the panel links it too, as the act it saw done,
    // so the pane links it twice.
    expect(
      screen.getByRole('link', { name: 'Pull request #7 was opened.' }),
    ).toHaveAttribute('href', PR_URL)
    expect(
      screen.getAllByRole('link', { name: 'Pull request #7' }),
    ).toHaveLength(1)
    // Every other target keeps its whole row: Ship, Publish, pull requests.
    expect(
      screen.getByRole('button', { name: 'Ship to other/' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Publish to other/' }),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('link', { name: 'Pull request #9' }),
    ).toHaveAttribute('href', OTHER_URL)

    await user.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(push).toHaveBeenCalled())
    expect(push.mock.calls[0]![0]).toBe('tg-1')
  })

  it('offers no Push once the remote has the commit, and says it is pushed', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    show(bound)
    expect(
      await findPushed('Pushed agent/main to GitHub at commit aaaaaaa.'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Push' }),
    ).not.toBeInTheDocument()
  })

  it('pushes the folder’s commit through the module and says the branch and the remote', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [{ ...target, host: 'git' }],
    })
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    const said = await findPushed(
      'Pushed agent/main to the approved repository at commit aaaaaaa.',
    )
    expect(said.closest('[role="status"]')).not.toBeNull()
    const [targetId, body, request] = push.mock.calls[0]!
    expect(targetId).toBe('tg-1')
    expect(body).toMatchObject({
      ref: 'refs/heads/agent/main',
      expected_old: '',
      commit: SHA,
      tree: TREE,
      session_run: 'run_1',
    })
    expect(body.operation_id).toMatch(/^console:/)
    expect(request).toEqual({ tenant: 't1' })
  })

  it('leases the push on the commit the remote was last seen holding', async () => {
    const seen = (commit: string, at: string) =>
      intent({
        requested: { ref: 'refs/heads/agent/main', commit, tree: TREE },
        observed: { present: true, sha: commit, merged: false, at },
      })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [
        seen('d'.repeat(40), '2026-11-10T09:00:00Z'),
        seen('c'.repeat(40), '2026-11-10T10:00:00Z'),
      ],
    })
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    await waitFor(() => expect(push).toHaveBeenCalled())
    expect(push.mock.calls[0]![1]).toMatchObject({
      expected_old: 'c'.repeat(40),
      commit: SHA,
    })
  })

  it('then offers to open a pull request on a GitHub target and links it', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    const open = vi.spyOn(gitpublishApi, 'openPullRequest')
    open.mockResolvedValue(opened)
    const user = userEvent.setup()
    show(bound)
    const submit = await screen.findByRole('button', {
      name: 'Open pull request',
    })
    // The title starts as the session's name; the target's first base is preselected.
    expect(screen.getByLabelText(/^Title/)).toHaveValue('export button')
    expect(screen.getByLabelText('Open as a draft')).not.toBeChecked()
    await user.type(screen.getByLabelText(/^Description/), 'Adds the export')
    await user.click(submit)
    const link = await screen.findByRole('link', {
      name: 'Pull request #7 was opened.',
    })
    expect(link).toHaveAttribute('href', PR_URL)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(open.mock.calls[0]![1]).toMatchObject({
      head_ref: 'agent/main',
      base: 'main',
      commit: SHA,
      title: 'export button',
      body: 'Adds the export',
      draft: false,
    })
  })

  it('never offers a pull request on a plain Git target', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [{ ...target, host: 'git' }],
    })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    show(bound)
    await findPushed(
      'Pushed agent/main to the approved repository at commit aaaaaaa.',
    )
    expect(
      screen.queryByRole('button', { name: 'Open pull request' }),
    ).not.toBeInTheDocument()
  })

  it('shows a push the engine has not settled as waiting, with no pressable Push', async () => {
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(
      intent({ state: 'dispatching', answer: 'uncertain', receipt: 'none' }),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Push' })).toBeDisabled(),
    )
    expect(
      screen.getByRole('button', { name: 'Push' }),
    ).toHaveAccessibleDescription(/Do not send it again/)
    expect(
      screen.getByRole('link', { name: 'Open Git publication' }),
    ).toHaveAttribute('href', '/git-publication')
  })

  it('says a refusal in the module’s words, not as a bare code', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(409, 'unresolved_intent', 'unresolved_intent'),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'An unresolved intent already holds this branch, pull request or merge.',
    )
  })

  it('says a branch changed outside the panel and opens Git publication from there', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(409, 'stale_lease', 'stale_lease'),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The branch changed on the repository outside this panel, so nothing was pushed.',
    )
    expect(
      screen.getByRole('link', { name: 'Open Git publication' }),
    ).toHaveAttribute('href', '/git-publication')
  })

  it('says a detected credential in its own words, with no toast that the role may not push', async () => {
    const warned = vi.spyOn(toast, 'warning').mockReturnValue('test-toast')
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(403, 'secret_detected', 'secret_detected'),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    const alert = await screen.findByRole('alert')
    // What was found is not shown; what to do is: remove it, or ship it for review.
    expect(alert).toHaveTextContent(
      'The commit contains what looks like a credential, so Push sent nothing.',
    )
    expect(alert).toHaveTextContent('use Ship')
    expect(warned).not.toHaveBeenCalled()
  })

  it('names Open pull request, not a push, when a detected credential refuses it', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    vi.spyOn(gitpublishApi, 'openPullRequest').mockRejectedValue(
      new ApiError(403, 'secret_detected', 'secret_detected'),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(
      await screen.findByRole('button', { name: 'Open pull request' }),
    )
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(
      'The commit contains what looks like a credential, so Open pull request sent nothing.',
    )
    expect(alert).not.toHaveTextContent('pushed')
  })

  it('says only the person who started the session, or a person who administers sessions, can publish it', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(422, 'session_source_refused', 'session_source_refused'),
    )
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Only the person who started this session, or a person who administers sessions, can publish its work.',
    )
    // A refusal of who publishes: no door beside it, nothing to press again.
    expect(
      screen.queryByRole('link', { name: 'Open Git publication' }),
    ).toBeNull()
  })

  it('sends the same operation id again after a network failure', async () => {
    const push = vi
      .spyOn(gitpublishApi, 'push')
      .mockRejectedValueOnce(new NetworkError('offline'))
      .mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The engine’s answer did not arrive',
    )
    await user.click(screen.getByRole('button', { name: 'Push' }))
    await findPushed('Pushed agent/main to GitHub at commit aaaaaaa.')
    expect(push).toHaveBeenCalledTimes(2)
    expect(push.mock.calls[1]![1].operation_id).toBe(
      push.mock.calls[0]![1].operation_id,
    )
  })

  it('makes a new operation id for a pull request whose words changed', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    const open = vi
      .spyOn(gitpublishApi, 'openPullRequest')
      .mockRejectedValueOnce(new NetworkError('offline'))
      .mockResolvedValue(opened)
    const user = userEvent.setup()
    show(bound)
    await user.click(
      await screen.findByRole('button', { name: 'Open pull request' }),
    )
    await screen.findByRole('alert')
    await user.type(screen.getByLabelText(/^Title/), ' v2')
    await user.click(screen.getByRole('button', { name: 'Open pull request' }))
    await screen.findByRole('link', { name: 'Pull request #7 was opened.' })
    expect(open.mock.calls[1]![1].operation_id).not.toBe(
      open.mock.calls[0]![1].operation_id,
    )
  })

  it('says a folder has no approved repository, with the door for whoever may open it', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [] })
    show(bound)
    expect(
      await screen.findByText(
        'This folder has no approved repository to push to.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Open Git publication' }),
    ).toHaveAttribute('href', '/git-publication')
    expect(
      screen.queryByRole('button', { name: 'Push' }),
    ).not.toBeInTheDocument()
  })

  it('shows no door where the Git publishing page does not open', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [] })
    publishingOpens = false
    show(bound)
    await screen.findByText(
      'This folder has no approved repository to push to.',
    )
    expect(
      screen.queryByRole('link', { name: 'Open Git publication' }),
    ).not.toBeInTheDocument()
  })

  it('shows what is pushed but no control to a person who may not push, and says why', async () => {
    withheld = ['gitpublish:push:write', 'gitpublish:pull_request:write']
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [intent(), opened],
    })
    show(bound)
    expect(
      await screen.findByText(
        'Your role can see what is pushed from this folder, but not push it.',
      ),
    ).toBeInTheDocument()
    // The sentence waits for the intents read, which starts once the targets answer.
    expect(
      await findPushed('Pushed agent/main to GitHub at commit aaaaaaa.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Pull request #7 was opened.' }),
    ).toHaveAttribute('href', PR_URL)
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
    expect(
      screen.queryByRole('button', { name: 'Open pull request' }),
    ).toBeNull()
  })

  it('offers no Push and posts nothing while the folder’s status names no HEAD', async () => {
    // Without the engine's `head`, the publish block under the diff pushes, as before.
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({ ...status, files: [] })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [intent(), opened],
    })
    const push = vi.spyOn(gitpublishApi, 'push')
    const open = vi.spyOn(gitpublishApi, 'openPullRequest')
    show(bound)
    expect(await screen.findByLabelText('Branch')).toHaveValue('main')
    expect(screen.getByRole('button', { name: 'Commit' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
    expect(screen.queryByRole('link', { name: /Pull request #7/ })).toBeNull()
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
    expect(push).not.toHaveBeenCalled()
    expect(open).not.toHaveBeenCalled()
  })

  it('takes a HEAD of any other shape as no HEAD: no Push, nothing read', async () => {
    const odd = { ...committed, head: { commit: 'HEAD', tree: TREE } }
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(odd)
    const push = vi.spyOn(gitpublishApi, 'push')
    show(bound)
    expect(await screen.findByLabelText('Branch')).toHaveValue('main')
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
    expect(push).not.toHaveBeenCalled()
  })

  it('pushes the 64-character names of a SHA-256 repository', async () => {
    const long = { commit: 'd'.repeat(64), tree: 'e'.repeat(64) }
    const sha256 = { ...committed, head: long }
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(sha256)
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    await waitFor(() => expect(push).toHaveBeenCalled())
    expect(push.mock.calls[0]![1]).toMatchObject(long)
  })

  it('says so to a person who cannot see the folder’s approved repository', async () => {
    withheld = ['gitpublish:target:read']
    show(bound)
    expect(
      await screen.findByText(
        'Your role cannot see the approved repository of this folder.',
      ),
    ).toBeInTheDocument()
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
  })

  it('keeps a race lane silent about pushing: a lane is merged, not published', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [] })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={bound} variant="lane" />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('button', { name: 'Commit' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('This folder has no approved repository to push to.'),
    ).toBeNull()
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
  })

  it('offers a new pull request when the branch’s last one was merged, and says so', async () => {
    const older = 'c'.repeat(40)
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [
        intent(),
        intent({
          id: 'in-3',
          effect: 'pull_request',
          requested: { head_ref: 'agent/main', base: 'main', commit: older },
          observed: { present: true, number: 6, merged: false, url: PR_URL },
        }),
        intent({
          id: 'in-4',
          effect: 'merge',
          requested: { number: 6, expected_head: older },
          observed: { present: true, number: 6, merged: true },
        }),
      ],
    })
    show(bound)
    expect(
      await screen.findByText('Pull request #6 was merged.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Open pull request' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Pull request #6' })).toBeNull()
  })

  it('offers no new pull request for a commit whose pull request was merged', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [
        intent(),
        { ...opened, observed: { ...opened.observed, merged: true } },
      ],
    })
    show(bound)
    expect(
      await screen.findByText('Pull request #7 was merged.'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Open pull request' }),
    ).toBeNull()
  })

  it('ties a missing title to the Title field and moves focus there', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    const open = vi.spyOn(gitpublishApi, 'openPullRequest')
    const user = userEvent.setup()
    show(bound)
    const title = await screen.findByLabelText(/^Title/)
    await user.clear(title)
    await user.click(screen.getByRole('button', { name: 'Open pull request' }))
    expect(title).toHaveAttribute('aria-invalid', 'true')
    expect(title).toHaveAccessibleDescription(
      'Write a title for the pull request.',
    )
    expect(title).toHaveFocus()
    expect(open).not.toHaveBeenCalled()
  })
})

// A push the approval policy holds answers 202 with the shape of a held ship.
const held = {
  state: 'awaiting_approval',
  committed: false,
  approval_id: 'appr-1',
} as unknown as PublicationIntent
const approval = (status: ApprovalDTO['status']) =>
  ({ id: 'appr-1', status }) as ApprovalDTO

describe('A push that waits for another person’s approval', () => {
  beforeEach(() => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(committed)
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [target] })
  })

  it('is shown as a held ship is, with the door to the approval, and Push cannot be pressed meanwhile', async () => {
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('pending'),
    )
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    const title = await screen.findByText(
      'Waiting for another person’s approval',
    )
    expect(title.closest('[role="status"]')).not.toBeNull()
    expect(screen.getByText(/approval appr-1/)).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Open the approval' }),
    ).toHaveAttribute('href', '/permissions?tab=approvals&approval=appr-1')
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Push' })).toBeDisabled(),
    )
    expect(
      screen.getByRole('button', { name: 'Push' }),
    ).toHaveAccessibleDescription(
      'Push is available again once the approval is given.',
    )
  })

  it('pushes again with the same operation id once the approval is given', async () => {
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('approved'),
    )
    const push = vi
      .spyOn(gitpublishApi, 'push')
      .mockResolvedValueOnce(held)
      .mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    await screen.findByText(/approval appr-1/)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Push' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Push' }))
    await findPushed('Pushed agent/main to GitHub at commit aaaaaaa.')
    expect(push).toHaveBeenCalledTimes(2)
    expect(push.mock.calls[1]![1].operation_id).toBe(
      push.mock.calls[0]![1].operation_id,
    )
  })

  it('says the approval was given and that Push sends it', async () => {
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('approved'),
    )
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    const said = await screen.findByText(
      'Another person gave approval appr-1. Press Push again to send it.',
    )
    expect(said.closest('[role="status"]')).not.toBeNull()
    expect(
      screen.queryByText('Waiting for another person’s approval'),
    ).toBeNull()
    expect(screen.getByRole('button', { name: 'Push' })).toBeEnabled()
  })

  it('says a refused approval as an alert, and that Push asks again', async () => {
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('rejected'),
    )
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Another person refused approval appr-1, so nothing went out. Press Push again to ask for a new review.',
    )
    expect(
      screen.queryByText('Waiting for another person’s approval'),
    ).toBeNull()
    expect(screen.getByRole('button', { name: 'Push' })).toBeEnabled()
  })

  it('says an approval that ended undecided, and that Push asks again', async () => {
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('expired'),
    )
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Nobody decided on approval appr-1 before it ended, so nothing went out. Press Push again to ask for a new review.',
    )
    expect(screen.getByRole('button', { name: 'Push' })).toBeEnabled()
  })

  it('holds Open pull request as it holds Push, with the door to the approval', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('pending'),
    )
    vi.spyOn(gitpublishApi, 'openPullRequest').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(
      await screen.findByRole('button', { name: 'Open pull request' }),
    )
    const title = await screen.findByText(
      'Waiting for another person’s approval',
    )
    expect(title.closest('[role="status"]')).not.toBeNull()
    expect(
      screen.getByText(
        'When approval appr-1 is given, press Open pull request again.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Open the approval' }),
    ).toHaveAttribute('href', '/permissions?tab=approvals&approval=appr-1')
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Open pull request' }),
      ).toBeDisabled(),
    )
    expect(
      screen.getByRole('button', { name: 'Open pull request' }),
    ).toHaveAccessibleDescription(
      'Open pull request is available again once the approval is given.',
    )
  })

  it('opens the pull request with the same operation id once the approval is given', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [intent()] })
    vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(
      approval('approved'),
    )
    const open = vi
      .spyOn(gitpublishApi, 'openPullRequest')
      .mockResolvedValueOnce(held)
      .mockResolvedValue(opened)
    const user = userEvent.setup()
    show(bound)
    await user.click(
      await screen.findByRole('button', { name: 'Open pull request' }),
    )
    expect(
      await screen.findByText(
        'Another person gave approval appr-1. Press Open pull request again to send it.',
      ),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Open pull request' }))
    await screen.findByRole('link', { name: 'Pull request #7 was opened.' })
    expect(open.mock.calls[1]![1].operation_id).toBe(
      open.mock.calls[0]![1].operation_id,
    )
  })

  it('leaves Push pressable where this person cannot follow the approval', async () => {
    publishingOpens = false
    const read = vi.spyOn(governanceApi, 'getApproval')
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    expect(
      await screen.findByText(
        'When approval appr-1 is given, press Push again.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Push' })).toBeEnabled()
    expect(screen.queryByRole('link', { name: 'Open the approval' })).toBeNull()
    expect(read).not.toHaveBeenCalled()
  })

  it.each(['rejected', 'expired'] as const)(
    'asks again under the same operation id once the approval is %s',
    async (status) => {
      vi.spyOn(governanceApi, 'getApproval').mockResolvedValue(approval(status))
      const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
      const user = userEvent.setup()
      show(bound)
      await user.click(await screen.findByRole('button', { name: 'Push' }))
      await screen.findByRole('alert')
      await user.click(screen.getByRole('button', { name: 'Push' }))
      await waitFor(() => expect(push).toHaveBeenCalledTimes(2))
      expect(push.mock.calls[1]![1].operation_id).toBe(
        push.mock.calls[0]![1].operation_id,
      )
    },
  )

  it('asks again under the same operation id after the engine refuses the approval', async () => {
    const warned = vi.spyOn(toast, 'warning').mockReturnValue('test-toast')
    const push = vi
      .spyOn(gitpublishApi, 'push')
      .mockRejectedValueOnce(
        new ApiError(403, 'approval_denied', 'approval_denied'),
      )
      .mockResolvedValue(intent())
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    // In the panel's words: it has Push to press again, not the ship's Ship.
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The approval was refused or ended, so nothing went out. Press Push again to ask for a new review.',
    )
    expect(warned).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Push' }))
    await findPushed('Pushed agent/main to GitHub at commit aaaaaaa.')
    expect(push.mock.calls[1]![1].operation_id).toBe(
      push.mock.calls[0]![1].operation_id,
    )
  })

  it('sends the same operation id again while the approval still waits', async () => {
    publishingOpens = false
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(held)
    const user = userEvent.setup()
    show(bound)
    await user.click(await screen.findByRole('button', { name: 'Push' }))
    await screen.findByText('When approval appr-1 is given, press Push again.')
    await user.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(push).toHaveBeenCalledTimes(2))
    expect(push.mock.calls[1]![1].operation_id).toBe(
      push.mock.calls[0]![1].operation_id,
    )
  })
})

describe('The folder’s HEAD the Git panel pushes', () => {
  it.each([
    { shape: 'uppercase hex', head: { commit: 'A'.repeat(40), tree: TREE } },
    { shape: '39 characters', head: { commit: 'a'.repeat(39), tree: TREE } },
    { shape: '41 characters', head: { commit: 'a'.repeat(41), tree: TREE } },
    { shape: 'no tree', head: { commit: SHA } },
    { shape: 'a number', head: { commit: 42, tree: TREE } },
    { shape: 'an array', head: { commit: [SHA], tree: TREE } },
  ])('takes a name of $shape as no HEAD', ({ head }) => {
    const odd = { ...committed, head } as unknown as GitStatus
    expect(pushableHead(odd)).toBeUndefined()
  })

  it('takes 40 or 64 lowercase hex names as the HEAD', () => {
    const long = { commit: 'd'.repeat(64), tree: 'e'.repeat(64) }
    expect(pushableHead(committed)).toEqual({ commit: SHA, tree: TREE })
    expect(pushableHead({ ...committed, head: long })).toEqual(long)
  })
})

describe('The repository Push goes to', () => {
  beforeEach(() => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue(committed)
  })

  it.each([
    ['github', 'acme/widgets', 'To acme/widgets on GitHub, under agent/'],
    ['git', '/srv/git/widgets.git', 'To /srv/git/widgets.git, under agent/'],
  ] as const)(
    'names a %s repository the engine names, with one target',
    async (host, repository, where) => {
      vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
        items: [{ ...target, host, repository }],
      })
      show(bound)
      expect(
        await screen.findByRole('button', { name: 'Push' }),
      ).toHaveAccessibleDescription(where)
      expect(screen.getByText(where)).toBeVisible()
    },
  )

  it('names two targets of one host and prefix apart, in the panel and the block', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [
        { ...target, repository: 'acme/widgets' },
        { ...target, id: 'tg-2', repository: 'acme/site' },
      ],
    })
    const session = { ...bound, worktree_branch: 'main' }
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SessionGit run={session} />
        <SessionPublish run={session} gitPanel />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('button', { name: 'Push' }),
    ).toHaveAccessibleDescription('To acme/widgets on GitHub, under agent/')
    expect(
      screen.getByRole('button', {
        name: 'Ship to acme/widgets, under agent/',
      }),
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Ship to acme/site, under agent/' }),
    ).toBeVisible()
    expect(
      screen.getByRole('button', {
        name: 'Publish to acme/site, under agent/',
      }),
    ).toBeVisible()
    expect(screen.getByText('acme/site')).toBeVisible()
  })
})

describe('A session folder that is not a Git repository', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('says so quietly, with what the pane still shows, and stops asking', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new ApiError(
        404,
        'not_a_git_repository',
        'this session folder is not a Git repository',
      ),
    )
    show({ ...run, state: 'running' })
    expect(
      await screen.findByText(
        'This folder is not a Git repository; Changes still lists the files the session changes.',
      ),
    ).toBeVisible()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Initialize Git' })).toBeVisible()
    // A running session's Git is read every 5 seconds; this answer stops that.
    await vi.advanceTimersByTimeAsync(12_000)
    expect(agentOpsApi.gitStatus).toHaveBeenCalledTimes(1)
  })

  it('keeps today’s words for a 404 the engine does not name', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    show()
    expect(
      await screen.findByText('This folder is not a Git repository.'),
    ).toBeVisible()
    expect(screen.queryByText(/Changes still lists/)).toBeNull()
  })
})

describe('The session folder’s files, staged and not', () => {
  it('lists staged and unstaged files apart, a file changed in both in each', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [
        { path: 'calc.py', index: 'M', worktree: ' ' },
        { path: 'tests/test_calc.py', index: '?', worktree: '?' },
        { path: 'README.md', index: 'M', worktree: 'M' },
      ],
    })
    show()
    const staged = await screen.findByRole('list', { name: 'Staged 2' })
    const unstaged = screen.getByRole('list', { name: 'Unstaged 2' })
    expect(within(staged).getAllByRole('listitem')).toHaveLength(2)
    expect(
      within(staged).getByRole('button', { name: 'Unstage calc.py' }),
    ).toBeVisible()
    expect(
      within(staged).getByRole('button', { name: 'Unstage README.md' }),
    ).toBeVisible()
    expect(within(staged).queryByRole('button', { name: /^Stage / })).toBeNull()
    expect(within(unstaged).getAllByRole('listitem')).toHaveLength(2)
    expect(
      within(unstaged).getByRole('button', {
        name: 'Stage tests/test_calc.py',
      }),
    ).toBeVisible()
    expect(
      within(unstaged).getByRole('button', { name: 'Stage README.md' }),
    ).toBeVisible()
    expect(
      within(unstaged).queryByRole('button', { name: /^Unstage / }),
    ).toBeNull()
  })

  it('shows no empty list: only unstaged files, no Staged list', async () => {
    show()
    expect(
      await screen.findByRole('list', { name: 'Unstaged 1' }),
    ).toBeVisible()
    expect(screen.queryByRole('list', { name: /^Staged/ })).toBeNull()
  })
})

describe('The commit message', () => {
  it('takes several lines, says what is committed, and commits on Ctrl+Enter', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    const message = await screen.findByRole('textbox', {
      name: 'Commit message',
    })
    expect(message).toHaveAccessibleDescription(
      'Only staged changes are committed, using your profile’s name and email.',
    )
    await user.type(message, 'Round amounts{Enter}{Enter}Use Decimal.')
    expect(message).toHaveValue('Round amounts\n\nUse Decimal.')
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
    await user.keyboard('{Control>}{Enter}{/Control}')
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'commit',
        { message: 'Round amounts\n\nUse Decimal.' },
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })

  it('keeps its chord inside Commit while Commit says why it cannot act', async () => {
    show()
    const commit = await screen.findByRole('button', { name: 'Commit' })
    expect(commit).toBeDisabled()
    expect(commit).toHaveAccessibleDescription(
      'Nothing is staged yet. Stage a file to commit it.',
    )
    expect(commit.querySelector('kbd')).toHaveAttribute(
      'data-keys',
      chordHint('Mod+Enter'),
    )
    // The chip found inside Commit above dims with it: a button takes a key into
    // its own ink and fades as a whole while disabled, so the chip has no class.
    expect(commit.querySelector('kbd')?.className).not.toMatch(/disabled:/)
    expect(commit).toHaveTextContent(/^Commit$/)
  })

  it('shows its chord on Commit, and commits on ⌘ with Enter only', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({
      ...status,
      files: [{ path: 'README.md', index: 'M', worktree: ' ' }],
    })
    const user = userEvent.setup()
    show()
    const message = await screen.findByRole('textbox', {
      name: 'Commit message',
    })
    // The field has the keyboard when the chord works, so it names the chord too.
    expect(message).toHaveAttribute(
      'aria-keyshortcuts',
      'Control+Enter Meta+Enter',
    )
    const commit = screen.getByRole('button', { name: 'Commit' })
    expect(commit).toHaveAttribute(
      'aria-keyshortcuts',
      'Control+Enter Meta+Enter',
    )
    // The chord is drawn inside Commit, so the button's name stays its words.
    expect(commit.querySelector('kbd')).toHaveAttribute(
      'data-keys',
      chordHint('Mod+Enter'),
    )
    await user.type(message, 'Round amounts{Enter}')
    expect(agentOpsApi.gitAction).not.toHaveBeenCalled()
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    await waitFor(() =>
      expect(agentOpsApi.gitAction).toHaveBeenCalledWith(
        'run_1',
        'commit',
        { message: 'Round amounts\n' },
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })
})
