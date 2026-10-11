// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDE PANE HAS TABS: Context, Changes, Files, then whatever a build registers.
import { act, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import userEvent from '@testing-library/user-event'
import { GitBranch } from 'lucide-react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import type { SessionPanelExtension } from '@/features/panels'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { SessionContextPane } from './session-context-pane'
import type { SessionResolution } from './use-session-resolution'
import { sessionsApi } from './api'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: { timelineById: vi.fn(), timeline: vi.fn() },
}))

const ops = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  getWorkspace: vi.fn(),
  listProfiles: vi.fn(),
  runChanges: vi.fn(),
  gitStatus: vi.fn(),
  previewPorts: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...ops },
  }
})

// The browser has its own tests; here it only has to be the thing the Files tab shows.
vi.mock('@/features/agentops/workspace-browser', () => ({
  WorkspaceBrowser: ({
    workspace,
  }: {
    workspace: { workspace_ref: string }
  }) => {
    const [draft, setDraft] = useState('')
    return (
      <div data-testid="files-browser">
        {workspace.workspace_ref}
        <input
          aria-label="File draft"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
      </div>
    )
  },
}))
vi.mock('./session-changes', () => ({
  SessionChanges: () => <div data-testid="changes-block" />,
}))
vi.mock('./session-branch-changes', () => ({
  SessionBranchChanges: () => null,
}))
vi.mock('@/features/gitpublish/session-publish', () => ({
  SessionPublish: () => null,
}))

const registered = vi.hoisted(() => ({
  panels: [] as SessionPanelExtension[] | undefined,
}))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      get sessionPanels() {
        return registered.panels
      },
    },
  }
})

const LIVE: LiveDTO = {
  session_ref: 'sess-a',
  live_ref: 'lr-a',
  attribution: 'managed',
  cc_state: 'active',
  input_tokens: 0,
  output_tokens: 0,
  cost_micro_usd: 0,
  event_count: 0,
  tool_call_count: 0,
  first_event_at: '2026-09-18T09:00:00Z',
  last_event_at: '2026-09-18T09:00:15Z',
  duration_seconds: 15,
  run_ref: 'run-1',
  provider_profile_ref: 'ppf_team',
}

const RUN: RunDTO = {
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
  workspace_ref: 'ws-main',
  live_ref: 'lr-a',
  provider_profile_ref: 'ppf_team',
}

function resolution(runs: RunDTO[] = [RUN]): SessionResolution {
  return {
    target: { liveRef: 'lr-a' },
    session: mergeSessions([LIVE], runs)[0]!,
    live: LIVE,
    runs,
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
  }
}

function renderPane(
  props: Partial<Parameters<typeof SessionContextPane>[0]> = {},
) {
  return renderIntel(
    <SessionContextPane
      resolution={resolution()}
      evidence="checks"
      onExpandEvidence={() => {}}
      {...props}
    />,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  registered.panels = []
  auth.can = () => true
  ops.listWorkspaces.mockResolvedValue({
    items: [
      {
        workspace_ref: 'ws-main',
        name: 'main',
        root_path: '/work',
        mount_mode: 'ro',
        max_read_bytes: 0,
        dlp_mode: 'off',
        state: 'active',
      },
    ],
    has_more: false,
  })
  ops.getWorkspace.mockResolvedValue({
    workspace_ref: 'ws-main',
    name: 'main',
    root_path: '/work',
    mount_mode: 'ro',
    max_read_bytes: 0,
    dlp_mode: 'off',
    state: 'active',
  })
  ops.listProfiles.mockResolvedValue({ items: [], has_more: false })
  vi.mocked(sessionsApi.timelineById).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(sessionsApi.timeline).mockResolvedValue({
    items: [],
    has_more: false,
  })
})

const tabs = () =>
  within(screen.getByRole('radiogroup', { name: 'Panels' }))
    .getAllByRole('radio')
    .map((r) => r.textContent)

describe('the side pane tabs', () => {
  it('shows Context, Changes, Files and Preview, and nothing else when no build registers a panel', async () => {
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Context', 'Changes', 'Files', 'Preview'])
    // Context is the one in front: the scope and the references, not the changes.
    expect(screen.getByTestId('context-details')).toBeInTheDocument()
    expect(screen.queryByTestId('changes-block')).toBeNull()
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })

  it("shows the same four tabs when a build's extension object has no sessionPanels at all", async () => {
    registered.panels = undefined
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Context', 'Changes', 'Files', 'Preview'])
  })

  it('shows a registered panel after Files, and renders it for the session on screen', async () => {
    const user = userEvent.setup()
    registered.panels = [
      {
        id: 'git',
        labelKey: 'sessions:context.tabFiles',
        icon: GitBranch,
        Component: ({ session }) => (
          <p data-testid="git-panel">git for {session.ref}</p>
        ),
      },
    ]
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Context', 'Changes', 'Files', 'Preview', 'Files'])
    const radios = screen.getAllByRole('radio')
    await user.click(radios[4]!)
    expect(await screen.findByTestId('git-panel')).toHaveTextContent(
      'git for run-1',
    )
    expect(screen.queryByTestId('context-details')).toBeNull()
  })

  it('hides a registered panel from a person without its permission', async () => {
    registered.panels = [
      {
        id: 'term',
        labelKey: 'sessions:context.tabFiles',
        icon: GitBranch,
        permission: 'admin.shell',
        Component: () => <p>terminal</p>,
      },
    ]
    auth.can = (p) => p !== 'admin.shell'
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Context', 'Changes', 'Files', 'Preview'])
  })

  it('Changes shows what the session changed, and moves the scope out of the way', async () => {
    const user = userEvent.setup()
    const onPanel = vi.fn()
    const view = renderPane({ onPanel })
    await user.click(await screen.findByRole('radio', { name: 'Changes' }))
    view.rerender(
      <SessionContextPane
        resolution={resolution()}
        evidence="checks"
        onExpandEvidence={() => {}}
        onPanel={onPanel}
        panel="changes"
      />,
    )
    expect(await screen.findByTestId('changes-block')).toBeInTheDocument()
    expect(screen.queryByTestId('context-details')).toBeNull()
    expect(onPanel).toHaveBeenCalledWith('changes')
  })

  it('Preview shows the app the session serves, and asks for its ports only once opened', async () => {
    const user = userEvent.setup()
    ops.previewPorts.mockResolvedValue({ ports: [5173] })
    renderPane()
    await screen.findByTestId('session-context')
    expect(ops.previewPorts).not.toHaveBeenCalled()
    await user.click(screen.getByRole('radio', { name: 'Preview' }))
    expect(await screen.findByTestId('session-preview')).toBeInTheDocument()
    expect(await screen.findByLabelText('Port')).toHaveValue('5173')
    expect(ops.previewPorts).toHaveBeenCalledWith(
      'run-1',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it('keeps session Git controls in Changes after composing the thread tabs', async () => {
    const user = userEvent.setup()
    ops.gitStatus.mockResolvedValue({
      branch: 'main',
      branches: ['main'],
      writable: true,
      files: [{ path: 'README.md', index: ' ', worktree: 'M' }],
      truncated: false,
    })
    renderPane({
      resolution: resolution([{ ...RUN, workspace_path: '/work' }]),
    })
    expect(screen.queryByRole('heading', { name: 'Git' })).toBeNull()
    expect(ops.gitStatus).not.toHaveBeenCalled()
    await user.click(screen.getByRole('radio', { name: 'Changes' }))
    expect(
      await screen.findByRole('button', { name: 'Stage README.md' }),
    ).toBeEnabled()
    expect(screen.getByLabelText('Branch')).toHaveValue('main')
    expect(ops.gitStatus).toHaveBeenCalledWith(
      'run-1',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    await user.click(screen.getByRole('radio', { name: 'Context' }))
    expect(screen.queryByRole('heading', { name: 'Git' })).toBeNull()
  })

  it('Files opens the workspace browser on the session’s own folder', async () => {
    renderPane({ panel: 'files' })
    expect(await screen.findByTestId('files-browser')).toHaveTextContent(
      'ws-main',
    )
  })

  it('Files shows a loading state while the folders are being read', async () => {
    ops.getWorkspace.mockReturnValue(new Promise(() => {}))
    renderPane({ panel: 'files' })
    const loading = await screen.findByTestId('context-files-loading')
    expect(loading).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByTestId('context-no-files')).toBeNull()
  })

  it('Files says it failed, and Retry asks again', async () => {
    const user = userEvent.setup()
    ops.getWorkspace.mockRejectedValueOnce(new Error('boom'))
    renderPane({ panel: 'files' })
    const failed = await screen.findByTestId('context-files-failed')
    expect(failed).toHaveTextContent('The folder could not be read.')
    expect(screen.queryByTestId('context-no-files')).toBeNull()
    await user.click(within(failed).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByTestId('files-browser')).toBeInTheDocument()
  })

  it('Files says the role cannot browse, apart from there being no folder', async () => {
    auth.can = (p) => p !== 'sessions:workspace:read'
    renderPane({ panel: 'files' })
    expect(
      await screen.findByTestId('context-files-forbidden'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('context-no-files')).toBeNull()
    expect(ops.listWorkspaces).not.toHaveBeenCalled()
    expect(ops.getWorkspace).not.toHaveBeenCalled()
  })

  it('Files says there is no folder to browse for a session without one', async () => {
    renderPane({
      panel: 'files',
      resolution: resolution([{ ...RUN, workspace_ref: undefined }]),
    })
    expect(await screen.findByTestId('context-no-files')).toBeInTheDocument()
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })

  it('reads an unknown panel in the address as Context', async () => {
    renderPane({ panel: 'nonsense' })
    await waitFor(() =>
      expect(screen.getByTestId('context-details')).toBeInTheDocument(),
    )
    expect(screen.getByRole('radio', { name: 'Context' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
  })

  it('mounts a registered panel as a component: one that uses hooks survives switching tabs twice', async () => {
    const user = userEvent.setup()
    const Counter = ({ session }: { session: { ref: string } }) => {
      const [n, setN] = useState(0)
      return (
        <button
          type="button"
          data-testid="counter"
          onClick={() => setN((v) => v + 1)}
        >
          {session.ref} {n}
        </button>
      )
    }
    registered.panels = [
      {
        id: 'counter',
        labelKey: 'sessions:context.tabFiles',
        icon: GitBranch,
        Component: Counter,
      },
    ]
    renderPane()
    await screen.findByTestId('session-context')
    const radios = screen.getAllByRole('radio')
    for (let round = 0; round < 2; round++) {
      await user.click(radios[4]!)
      await user.click(await screen.findByTestId('counter'))
      expect(screen.getByTestId('counter')).toHaveTextContent('run-1 1')
      await user.click(radios[0]!)
      expect(screen.queryByTestId('counter')).toBeNull()
    }
  })

  it('tells a panel only what it needs of the session', async () => {
    const user = userEvent.setup()
    let seen: unknown
    registered.panels = [
      {
        id: 'peek',
        labelKey: 'sessions:context.tabFiles',
        icon: GitBranch,
        Component: ({ session }) => {
          seen = session
          return null
        },
      },
    ]
    renderPane()
    await user.click((await screen.findAllByRole('radio'))[4]!)
    await waitFor(() => expect(seen).toBeDefined())
    expect(seen).toEqual({
      ref: 'run-1',
      workspaceRef: 'ws-main',
      tool: undefined,
      state: 'running',
    })
  })

  it('ignores a registrant that takes the id of Context, Changes, Files or Preview', async () => {
    registered.panels = ['context', 'changes', 'files', 'preview'].map(
      (id) => ({
        id,
        labelKey: 'sessions:context.tabFiles',
        icon: GitBranch,
        Component: () => <p>takeover</p>,
      }),
    )
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Context', 'Changes', 'Files', 'Preview'])
    expect(screen.queryByText('takeover')).toBeNull()
  })
})

describe('Files scope and panel navigation regressions', () => {
  it('resolves the reference directly even when it is absent from the first workspace page', async () => {
    ops.listWorkspaces.mockResolvedValue({
      items: [],
      has_more: true,
      cursor: 'page-2',
    })
    renderPane({ panel: 'files' })
    expect(await screen.findByTestId('files-browser')).toHaveTextContent(
      'ws-main',
    )
    expect(ops.getWorkspace).toHaveBeenCalledWith(
      'ws-main',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  it('browses the registered worktree scope, never the original checkout', async () => {
    ops.listWorkspaces.mockResolvedValue({
      items: [
        {
          workspace_ref: 'ws-worktree',
          root_path: '/worktrees/a',
          state: 'active',
          mount_mode: 'rw',
        },
      ],
      has_more: false,
    })
    renderPane({
      panel: 'files',
      resolution: resolution([
        {
          ...RUN,
          workspace_path: '/worktrees/a',
          worktree_branch: 'session/a',
        },
      ]),
    })
    expect(await screen.findByTestId('files-browser')).toHaveTextContent(
      'ws-worktree',
    )
    expect(ops.listWorkspaces).toHaveBeenCalledWith(
      expect.objectContaining({ root_path: '/worktrees/a', state: 'active' }),
      expect.anything(),
    )
  })

  it('denies browsing when the actual folder has no registered file scope', async () => {
    ops.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
    renderPane({
      panel: 'files',
      resolution: resolution([
        {
          ...RUN,
          workspace_path: '/worktrees/a',
          worktree_branch: 'session/a',
        },
      ]),
    })
    expect(
      await screen.findByTestId('context-files-forbidden'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })

  it('returns to Context when browser navigation removes panel after selecting Files', async () => {
    const user = userEvent.setup()
    const view = renderPane()
    await user.click(await screen.findByRole('radio', { name: 'Files' }))
    await screen.findByTestId('files-browser')
    view.rerender(
      <SessionContextPane
        resolution={resolution([{ ...RUN, run_ref: 'run-b' }])}
        panel="files"
      />,
    )
    await screen.findByTestId('files-browser')
    view.rerender(
      <SessionContextPane resolution={resolution()} onPanel={() => {}} />,
    )
    expect(screen.getByRole('radio', { name: 'Context' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })
})

it('retires the file draft between cached sessions even in the same workspace', async () => {
  const user = userEvent.setup()
  const view = renderPane({ panel: 'files' })
  await user.type(
    await screen.findByRole('textbox', { name: 'File draft' }),
    'unsaved for A',
  )
  view.rerender(
    <SessionContextPane
      resolution={resolution([{ ...RUN, run_ref: 'run-b' }])}
      panel="files"
    />,
  )
  await waitFor(() =>
    expect(screen.getByRole('textbox', { name: 'File draft' })).toHaveValue(''),
  )
})

it('Files Retry recovers a failed original workspace refetch while a worktree scope is cached', async () => {
  const user = userEvent.setup()
  const queryClient = createTestQueryClient()
  ops.listWorkspaces.mockResolvedValue({
    items: [
      {
        workspace_ref: 'ws-worktree',
        root_path: '/worktrees/a',
        state: 'active',
        mount_mode: 'rw',
      },
    ],
    has_more: false,
  })
  renderIntel(
    <SessionContextPane
      resolution={resolution([
        {
          ...RUN,
          workspace_path: '/worktrees/a',
          worktree_branch: 'session/a',
        },
      ])}
      panel="files"
    />,
    { queryClient },
  )
  expect(await screen.findByTestId('files-browser')).toHaveTextContent(
    'ws-worktree',
  )
  ops.getWorkspace.mockRejectedValueOnce(new Error('background lookup failed'))
  await act(async () => {
    await queryClient.refetchQueries({
      predicate: (query) => query.queryKey.includes('workspace'),
    })
  })
  const failed = await screen.findByTestId('context-files-failed')
  await user.click(within(failed).getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(ops.getWorkspace).toHaveBeenCalledTimes(3))
  expect(await screen.findByTestId('files-browser')).toHaveTextContent(
    'ws-worktree',
  )
})
