// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDE PANE HAS TABS: Changes, Files, Preview, Details, Activity (Claude Code and
// Codex), then whatever a build registers.
import { act, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import userEvent from '@testing-library/user-event'
import { GitBranch } from 'lucide-react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import type { SessionPanelExtension } from '@/features/panels'
import { useClientSettings } from '@/features/settings/preferences'
import { ApiError } from '@/lib/api/errors'
import { preferenceStorage } from '@/lib/preference-storage'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { SessionContextPane } from './session-context-pane'
import type { SessionResolution } from './use-session-resolution'
import { sessionsApi } from './api'
import { useTerminalDrawer } from './terminal-drawer'
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
  getProfile: vi.fn(),
  getAccount: vi.fn(),
  listAccounts: vi.fn(),
  runChanges: vi.fn(),
  changes: vi.fn(),
  gitStatus: vi.fn(),
  previewPorts: vi.fn(),
  activity: vi.fn(),
  listFiles: vi.fn(),
  readFile: vi.fn(),
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
// The folder's terminal: a script's Run types its command there.
const terminal = vi.hoisted(() => ({ run: vi.fn(), folder: '' }))
vi.mock('./session-terminal', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./session-terminal')>()),
  useTerminalGroup: (folder: string) => {
    terminal.folder = folder
    return { tenant: 'tnt-demo', run: terminal.run }
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

// A file at the top of the session's folder, as the engine lists it.
const topFile = (name: string) => ({
  name,
  path: name,
  type: 'file',
  size: 40,
  mode: '-rw-r--r--',
  mtime: '2026-11-10T00:00:00Z',
})

// The profile a quick start resolves for a key: not an account, so it has no name.
const UNNAMED_PROFILE = {
  profile_ref: 'ppf_team',
  driver: 'claude-code',
  state: 'active',
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
  ops.listFiles.mockResolvedValue({ path: '', entries: [], has_more: false })
  useClientSettings.setState({ projectScripts: {} })
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
  ops.changes.mockResolvedValue({ folder: 'main', files: [], truncated: false })
  ops.getProfile.mockResolvedValue(UNNAMED_PROFILE)
  ops.getAccount.mockResolvedValue({ usage: [] })
  ops.listAccounts.mockResolvedValue({ items: [], has_more: false })
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
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
    // Context is the one in front: the scope and the references, not the changes.
    expect(screen.getByTestId('context-details')).toBeInTheDocument()
    expect(screen.queryByTestId('changes-block')).toBeNull()
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })

  //: an address that names no evidence block leaves `evidence` undefined; the
  // pane used to read that as "no overview" and dropped the session's work from it.
  it('shows the overview when the address names no evidence block', async () => {
    renderPane({ evidence: undefined })
    expect(await screen.findByTestId('session-overview')).toBeInTheDocument()
  })

  it('says once what the header and composer already say: tool, model, access and usage wait under Session, and no explainer stands in for an unselected line', async () => {
    renderPane({
      panel: 'context',
      resolution: resolution([
        { ...RUN, tool_mode: 'plan', model_ref: 'claude-opus-5-5' },
      ]),
    })
    const facts = await screen.findByTestId('session-facts')
    // The default view: what the conversation does not show.
    expect(within(facts).getByText('State')).toBeVisible()
    // The rest is one click away, closed.
    const more = screen.getByTestId('session-facts-more') as HTMLDetailsElement
    expect(more.open).toBe(false)
    expect(within(more).getByText('Model')).not.toBeVisible()
    // The composer's access chip says the mode; Details does not say it again.
    expect(screen.queryByTestId('narrative-mode')).toBeNull()
    // Nothing selected: no paragraph about what selecting would show.
    expect(screen.queryByText('Selected line')).toBeNull()
  })

  it("shows the same five tabs when a build's extension object has no sessionPanels at all", async () => {
    registered.panels = undefined
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
  })

  it('shows a registered panel after the pane’s own, and renders it for the session on screen', async () => {
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
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details', 'Files'])
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
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
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
    await user.click(screen.getByRole('radio', { name: 'Details' }))
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

  it('a folderless Files link opens Details', async () => {
    renderPane({
      panel: 'files',
      resolution: resolution([{ ...RUN, workspace_ref: undefined }]),
    })
    expect(await screen.findByTestId('context-details')).toBeInTheDocument()
    expect(screen.queryByTestId('files-browser')).toBeNull()
  })

  it('reads an unknown panel in the address as Context', async () => {
    renderPane({ panel: 'nonsense' })
    await waitFor(() =>
      expect(screen.getByTestId('context-details')).toBeInTheDocument(),
    )
    expect(screen.getByRole('radio', { name: 'Details' })).toHaveAttribute(
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

  it.each(['claude', 'codex'])(
    'opens a %s session on Activity, with Details one click away',
    async (driver) => {
      const user = userEvent.setup()
      const onPanel = vi.fn()
      ops.activity.mockResolvedValue({
        driver,
        transcript: 'read',
        context_tokens: 1200,
        compactions: [],
        agents: [],
        tasks: [],
        files: [],
        todos: [],
        skills: [],
        mcp_servers: [],
      })
      const claudeRun = resolution([{ ...RUN, provider_driver: driver }])
      const view = renderPane({ resolution: claudeRun, onPanel })
      await screen.findByTestId('session-context')
      expect(tabs()).toEqual([
        'Changes',
        'Files',
        'Preview',
        'Details',
        'Activity',
      ])
      // What the tool is doing is in front, read from the engine.
      expect(screen.getByRole('radio', { name: 'Activity' })).toHaveAttribute(
        'aria-checked',
        'true',
      )
      expect(await screen.findByTestId('session-activity')).toBeInTheDocument()
      expect(ops.activity).toHaveBeenCalledWith('run-1', expect.anything())
      await user.click(screen.getByRole('radio', { name: 'Details' }))
      expect(onPanel).toHaveBeenLastCalledWith('context')
      // The address holds the tab in front.
      view.rerender(
        <SessionContextPane
          resolution={claudeRun}
          evidence="checks"
          onExpandEvidence={() => {}}
          onPanel={onPanel}
          panel="context"
        />,
      )
      expect(await screen.findByTestId('context-scope')).toBeInTheDocument()
      expect(screen.queryByTestId('session-activity')).toBeNull()
    },
  )

  it('offers no Activity for another tool, whose record is not read that way yet', async () => {
    renderPane({
      resolution: resolution([{ ...RUN, provider_driver: 'opencode' }]),
    })
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
    expect(ops.activity).not.toHaveBeenCalled()
  })

  it('Details lists the scripts the folder declares and runs one in the session terminal', async () => {
    const user = userEvent.setup()
    ops.listFiles.mockResolvedValue({
      path: '',
      entries: [topFile('package.json'), topFile('README.md')],
      has_more: false,
    })
    ops.readFile.mockResolvedValue({
      path: 'package.json',
      size: 40,
      encoding: 'utf-8',
      content: JSON.stringify({ scripts: { dev: 'vite' } }),
    })
    renderPane({ onPanel: () => {}, panel: 'context' })
    const scripts = await screen.findByTestId('context-scripts')
    expect(await within(scripts).findByText("npm run -- 'dev'")).toBeVisible()
    // No Taskfile and no makefile at the top: none is asked for, so none answers 404.
    expect(ops.readFile.mock.calls.map((call) => call[1])).toEqual([
      'package.json',
    ])
    await user.click(within(scripts).getByRole('button', { name: 'Run dev' }))
    expect(terminal.run).toHaveBeenCalledTimes(1)
    expect(terminal.run.mock.calls[0][0]).toBe("npm run -- 'dev'")
    expect(useTerminalDrawer.getState().open['run-1']).toBe(true)
  })

  it('Details keeps a script a person adds to the folder, and refuses one with hidden characters', async () => {
    const user = userEvent.setup()
    renderPane({ onPanel: () => {}, panel: 'context' })
    const scripts = await screen.findByTestId('context-scripts')
    expect(
      await within(scripts).findByText(/declares no scripts/),
    ).toBeVisible()
    await user.click(
      within(scripts).getByRole('button', { name: 'Add script' }),
    )
    await user.type(within(scripts).getByLabelText('Name'), 'lint')
    const command = within(scripts).getByLabelText('Command')
    await user.type(command, 'pnpm lint\u200b')
    expect(within(scripts).getByRole('alert')).toHaveTextContent(
      /hidden characters/,
    )
    expect(within(scripts).getByRole('button', { name: 'Add' })).toBeDisabled()
    await user.clear(command)
    await user.type(command, 'pnpm lint')
    await user.click(within(scripts).getByRole('button', { name: 'Add' }))
    expect(
      within(scripts).getByRole('button', { name: 'Run lint' }),
    ).toBeVisible()
    expect(useClientSettings.getState().projectScripts['ws-main']).toEqual([
      { name: 'lint', command: 'pnpm lint' },
    ])
    await user.click(
      within(scripts).getByRole('button', { name: 'Remove lint' }),
    )
    expect(
      within(scripts).queryByRole('button', { name: 'Run lint' }),
    ).toBeNull()
  })

  it('Details adds a script while the settings fit the size the server keeps, and past it says to remove one', async () => {
    const user = userEvent.setup()
    // The console keeps the settings under 16000 bytes; the server takes 16384.
    const bound = 16_000
    const saved = () => {
      const value = preferenceStorage.getItem('olivares.settings') ?? ''
      return new TextEncoder().encode(value).length
    }
    const lint = { name: 'lint', command: 'pnpm lint' }
    // One long script already kept: exactly `lint` still fits after it.
    useClientSettings.setState({
      projectScripts: { 'ws-main': [{ name: 'big', command: '' }] },
    })
    const room = bound - saved() - (JSON.stringify(lint).length + 1)
    const big = { name: 'big', command: 'x'.repeat(room) }
    useClientSettings.setState({ projectScripts: { 'ws-main': [big] } })
    renderPane({ onPanel: () => {}, panel: 'context' })
    const scripts = await screen.findByTestId('context-scripts')
    await user.click(
      await within(scripts).findByRole('button', { name: 'Add script' }),
    )
    await user.type(within(scripts).getByLabelText('Name'), 'lint')
    const command = within(scripts).getByLabelText('Command')
    await user.type(command, 'pnpm lint2')
    expect(within(scripts).getByRole('alert')).toHaveTextContent(
      'No more scripts fit: remove one.',
    )
    expect(within(scripts).getByRole('button', { name: 'Add' })).toBeDisabled()
    await user.type(command, '{Backspace}')
    expect(within(scripts).queryByRole('alert')).toBeNull()
    await user.click(within(scripts).getByRole('button', { name: 'Add' }))
    expect(useClientSettings.getState().projectScripts['ws-main']).toEqual([
      big,
      lint,
    ])
    expect(saved()).toBe(bound)
    // Full: the form says so at once, until a script is removed.
    await user.click(
      within(scripts).getByRole('button', { name: 'Add script' }),
    )
    expect(within(scripts).getByRole('alert')).toHaveTextContent(
      'No more scripts fit: remove one.',
    )
    await user.click(
      within(scripts).getByRole('button', { name: 'Remove big' }),
    )
    expect(within(scripts).queryByRole('alert')).toBeNull()
  })

  it('Details keeps a script added on a project for its next session, which runs it in its own worktree', async () => {
    const user = userEvent.setup()
    // The first session works in the project's folder and adds a script.
    const first = renderPane({ onPanel: () => {}, panel: 'context' })
    let scripts = await screen.findByTestId('context-scripts')
    await user.click(
      within(scripts).getByRole('button', { name: 'Add script' }),
    )
    await user.type(within(scripts).getByLabelText('Name'), 'lint')
    await user.type(within(scripts).getByLabelText('Command'), 'pnpm lint')
    await user.click(within(scripts).getByRole('button', { name: 'Add' }))
    first.unmount()
    // The next works in its own worktree, a folder registered on its own.
    ops.listWorkspaces.mockResolvedValue({
      items: [
        {
          workspace_ref: 'ws-worktree',
          root_path: '/worktrees/b',
          state: 'active',
          mount_mode: 'rw',
        },
      ],
      has_more: false,
    })
    const second: RunDTO = {
      ...RUN,
      run_ref: 'run-2',
      live_ref: 'lr-b',
      workspace_path: '/worktrees/b',
      worktree_branch: 'session/b',
    }
    const live: LiveDTO = {
      ...LIVE,
      session_ref: 'sess-b',
      live_ref: 'lr-b',
      run_ref: 'run-2',
    }
    renderPane({
      onPanel: () => {},
      panel: 'context',
      resolution: {
        ...resolution([second]),
        target: { liveRef: 'lr-b' },
        live,
        session: mergeSessions([live], [second])[0]!,
      },
    })
    scripts = await screen.findByTestId('context-scripts')
    await user.click(
      await within(scripts).findByRole('button', { name: 'Run lint' }),
    )
    expect(terminal.folder).toBe('ws-worktree')
    expect(terminal.run.mock.calls[0][0]).toBe('pnpm lint')
    expect(useTerminalDrawer.getState().open['run-2']).toBe(true)
    // What it adds is the project's too, under the one folder.
    await user.click(
      within(scripts).getByRole('button', { name: 'Add script' }),
    )
    await user.type(within(scripts).getByLabelText('Name'), 'test')
    await user.type(within(scripts).getByLabelText('Command'), 'pnpm test')
    await user.click(within(scripts).getByRole('button', { name: 'Add' }))
    expect(useClientSettings.getState().projectScripts).toEqual({
      'ws-main': [
        { name: 'lint', command: 'pnpm lint' },
        { name: 'test', command: 'pnpm test' },
      ],
    })
  })

  it('Details lists the agents the session spawned, each with its state and one-line result, and their total', async () => {
    ops.activity.mockResolvedValue({
      driver: 'claude',
      transcript: 'read',
      context_tokens: 0,
      compactions: [],
      agents: [
        {
          tool_use_id: 't1',
          description: 'Find the callers',
          state: 'completed',
          tokens: 1200,
          summary: 'Three callers, all in core.',
        },
        { tool_use_id: 't2', description: 'Write the test', state: 'running' },
      ],
      tasks: [],
      files: [],
      todos: [],
      skills: [],
      mcp_servers: [],
    })
    renderPane({
      resolution: resolution([{ ...RUN, provider_driver: 'claude' }]),
      onPanel: () => {},
      panel: 'context',
    })
    const agents = await screen.findByTestId('context-subagents')
    const rows = within(agents).getAllByTestId('context-subagent')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('Find the callers')
    expect(rows[0]).toHaveTextContent('Three callers, all in core.')
    expect(rows[1]).toHaveTextContent('Write the test')
    expect(
      within(agents).getByText('1 finished · Σ 1,200 tokens'),
    ).toBeVisible()
  })

  it('Details says when the agents could not be read, and Retry reads them again', async () => {
    const user = userEvent.setup()
    const read = {
      driver: 'claude',
      transcript: 'read',
      context_tokens: 0,
      compactions: [],
      agents: [
        {
          tool_use_id: 't1',
          description: 'Find the callers',
          state: 'completed',
        },
      ],
      tasks: [],
      files: [],
      todos: [],
      skills: [],
      mcp_servers: [],
    }
    ops.activity.mockRejectedValueOnce(new ApiError(503, 'unavailable', 'x'))
    ops.activity.mockResolvedValueOnce(read)
    renderPane({
      resolution: resolution([{ ...RUN, provider_driver: 'claude' }]),
      onPanel: () => {},
      panel: 'context',
    })
    const group = await screen.findByTestId('context-subagents')
    expect(within(group).getByRole('alert')).toHaveTextContent(
      'The activity could not be read.',
    )
    await user.click(within(group).getByRole('button', { name: 'Retry' }))
    expect(await screen.findAllByTestId('context-subagent')).toHaveLength(1)
    const agents = screen.getByTestId('context-subagents')
    expect(within(agents).queryByRole('alert')).toBeNull()
  })

  it('Details leaves the agents out when the engine does not show the activity', async () => {
    const user = userEvent.setup()
    const refusal = new ApiError(409, 'conflict', 'it was given vault secrets')
    // From a failed read, so the refusal is seen to take the group away.
    ops.activity.mockRejectedValueOnce(new ApiError(503, 'unavailable', 'x'))
    ops.activity.mockRejectedValueOnce(refusal)
    renderPane({
      resolution: resolution([{ ...RUN, provider_driver: 'claude' }]),
      onPanel: () => {},
      panel: 'context',
    })
    const group = await screen.findByTestId('context-subagents')
    await user.click(within(group).getByRole('button', { name: 'Retry' }))
    // The Activity tab says why; Details shows neither the group nor an error.
    await waitFor(() =>
      expect(screen.queryByTestId('context-subagents')).toBeNull(),
    )
    expect(ops.activity).toHaveBeenCalledTimes(2)
  })

  it('ignores a registrant that takes the id of Context, Activity, Changes, Files, Preview or the retired Terminal tab', async () => {
    registered.panels = [
      'context',
      'activity',
      'changes',
      'files',
      'preview',
      'terminal',
    ].map((id) => ({
      id,
      labelKey: 'sessions:context.tabFiles',
      icon: GitBranch,
      Component: () => <p>takeover</p>,
    }))
    renderPane()
    await screen.findByTestId('session-context')
    expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
    expect(screen.queryByText('takeover')).toBeNull()
  })
})

// The pane is 280 to 640 px wide (surface-resize.ts). At its default width the five tabs
// ran into one line of text, each cut under the next. jsdom lays nothing out, so the
// widths a browser measures are given here: a tab is 8 px a letter plus 16 px of padding,
// More is 28 px.
describe('the side pane tabs at the pane’s own width', () => {
  function layout(room: number) {
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(
      function (this: HTMLElement) {
        if (this.getAttribute('role') === 'radio')
          return 8 * (this.textContent ?? '').length + 16
        if (this.getAttribute('aria-label') === 'More panels') return 28
        return 0
      },
    )
    vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(
      function (this: Element) {
        return this.getAttribute('data-slot') === 'pane-tabs' ? room : 0
      },
    )
  }

  it('shows the tabs that fit and puts the rest under More, never squeezed', async () => {
    layout(240)
    try {
      const user = userEvent.setup()
      renderPane()
      await screen.findByTestId('session-context')
      // 72 + 56 + 72 px of tabs and 28 px of More fit in 240; Preview would not.
      expect(tabs()).toEqual(['Changes', 'Files', 'Details'])
      await user.click(screen.getByRole('button', { name: 'More panels' }))
      const hidden = screen.getAllByRole('menuitemradio')
      expect(hidden.map((i) => i.textContent)).toEqual(['Preview'])
      await user.click(hidden[0]!)
      // The tab in front always shows, in the strip.
      expect(tabs()).toContain('Preview')
      expect(screen.getByRole('radio', { name: 'Preview' })).toBeChecked()
    } finally {
      vi.restoreAllMocks()
    }
  })

  it('shows every tab and no More where they all fit', async () => {
    layout(640)
    try {
      renderPane()
      await screen.findByTestId('session-context')
      expect(tabs()).toEqual(['Changes', 'Files', 'Preview', 'Details'])
      expect(screen.queryByRole('button', { name: 'More panels' })).toBeNull()
    } finally {
      vi.restoreAllMocks()
    }
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

  it('links a session started from an issue back to its page', async () => {
    renderPane({
      resolution: resolution([
        { ...RUN, source_url: 'https://github.com/acme/widgets/issues/17' },
      ]),
    })
    const link = await screen.findByRole('link', { name: 'acme/widgets#17' })
    expect(link).toHaveAttribute(
      'href',
      'https://github.com/acme/widgets/issues/17',
    )
    expect(link).toHaveAttribute('target', '_blank')
    expect(link.getAttribute('rel')).toContain('noopener')
  })

  it('shows no link for a session started any other way, or for a page that is not GitHub’s', async () => {
    const view = renderPane()
    await screen.findByRole('radio', { name: 'Details' })
    expect(screen.queryByRole('link', { name: /#\d+$/ })).toBeNull()
    view.unmount()
    renderPane({
      resolution: resolution([
        { ...RUN, source_url: 'javascript:alert(document.cookie)' },
      ]),
    })
    await screen.findByRole('radio', { name: 'Details' })
    expect(
      screen
        .queryAllByRole('link')
        .filter((a) => a.getAttribute('href')?.startsWith('javascript:')),
    ).toEqual([])
    expect(screen.queryByText(/javascript:/)).toBeNull()
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
    expect(screen.getByRole('radio', { name: 'Details' })).toHaveAttribute(
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

// The account read is for a NAMED profile: an unnamed one is no account, and the engine
// answers its ref 404, which the browser logs as a console error on the page.
describe('the account usage read', () => {
  const settle = () => act(() => new Promise((r) => setTimeout(r, 100)))

  it('is not made for a new unnamed profile that a cached profile list does not hold yet', async () => {
    // The list was read before this session's launch resolved its profile.
    ops.listProfiles.mockResolvedValue({ items: [], has_more: false })
    renderPane()
    await screen.findByTestId('session-facts')
    await settle()
    expect(ops.getAccount).not.toHaveBeenCalled()
  })

  it('is made for a named profile', async () => {
    ops.getProfile.mockResolvedValue({
      ...UNNAMED_PROFILE,
      account_name: 'team',
    })
    renderPane()
    await waitFor(() =>
      expect(ops.getAccount).toHaveBeenCalledWith(
        'ppf_team',
        expect.anything(),
      ),
    )
  })

  it('is not made for an unnamed profile when the profile cannot be read', async () => {
    // The account list holds named profiles only: the ref is not in it.
    auth.can = (p) => p !== 'sessions:profile:read'
    renderPane()
    await screen.findByTestId('session-facts')
    await settle()
    expect(ops.getAccount).not.toHaveBeenCalled()
    expect(ops.getProfile).not.toHaveBeenCalled()
  })

  it('is made for a named account when the profile cannot be read', async () => {
    auth.can = (p) => p !== 'sessions:profile:read'
    ops.listAccounts.mockResolvedValue({
      items: [{ account_ref: 'ppf_team', name: 'team', state: 'active' }],
      has_more: false,
    })
    renderPane()
    await waitFor(() =>
      expect(ops.getAccount).toHaveBeenCalledWith(
        'ppf_team',
        expect.anything(),
      ),
    )
    expect(ops.getProfile).not.toHaveBeenCalled()
  })

  it('is made for a named account when the profile read fails', async () => {
    ops.getProfile.mockRejectedValue(new Error('boom'))
    ops.listAccounts.mockResolvedValue({
      items: [{ account_ref: 'ppf_team', name: 'team', state: 'active' }],
      has_more: false,
    })
    renderPane()
    await waitFor(() =>
      expect(ops.getAccount).toHaveBeenCalledWith(
        'ppf_team',
        expect.anything(),
      ),
    )
  })
})
