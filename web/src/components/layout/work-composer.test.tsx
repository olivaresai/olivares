// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, createTestQueryClient } from '@/test/intel'
import {
  clearSessionDrafts,
  sessionDraftKey,
  writeSessionDraft,
} from '@/lib/session-drafts'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  principal: { kind: 'user', actor: 'user:alice', user_id: 'alice' } as {
    kind: PrincipalKind
    actor: string
    user_id: string
  } | null,
  grants: [] as { tenant: string; tenant_name?: string }[],
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  getRun: vi.fn(),
  profileModels: vi.fn(),
  activity: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
const templates = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/features/workspace-templates/api', async (importOriginal) => ({
  ...(await importOriginal<
    typeof import('@/features/workspace-templates/api')
  >()),
  templatesApi: templates,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { ApiError } from '@/lib/api/errors'
import { sentTurnsOf, useSentTurns } from '@/features/sessions/sent-turns'
import { ScopeLine, WorkComposer } from './work-composer'
import { COMPOSER_INPUT_ID } from './work-composer-id'
import type { RunDTO } from '@/features/agentops/types'
import type { PrincipalKind } from '@/lib/api/types'

const PROFILE = {
  profile_ref: 'ppf_team',
  driver: 'claude',
  environment_ref: 'env-prod',
  display_name: 'Team account',
  state: 'active' as const,
  local_environment: true,
  operable: true,
}

const WORKSPACE = {
  workspace_ref: 'ws-main',
  name: 'main',
  state: 'active',
  mount_mode: 'ro',
  dlp_mode: 'off',
}

/** A session whose tool reported no context window: the composer draws no ring. */
const ACTIVITY = {
  driver: 'claude',
  transcript: 'read',
  context_tokens: 0,
  compactions: [],
  todos: [],
  agents: [],
  tasks: [],
  files: [],
  skills: [],
  mcp_servers: [],
}

beforeEach(() => {
  vi.clearAllMocks()
  clearSessionDrafts()
  sessionStorage.clear()
  auth.principal = { kind: 'user', actor: 'user:alice', user_id: 'alice' }
  auth.activeTenant = 'tnt-demo'
  auth.grants = []
  auth.can = () => true
  api.listProfiles.mockResolvedValue({ items: [PROFILE], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [WORKSPACE], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
  api.activity.mockResolvedValue(ACTIVITY)
  api.profileModels.mockResolvedValue({
    profile_ref: 'ppf_team',
    driver: 'claude',
    state: 'fresh',
    default_model: '',
    free_text: true,
    checked_at: '2026-10-12T10:00:00Z',
    refresh_interval_seconds: 120,
    models: [
      {
        id: 'opus-5-5',
        display_name: 'Claude Opus 5.5',
        is_default: true,
        effort_levels: ['low', 'medium', 'high'],
      },
      {
        id: 'haiku-5-5',
        display_name: 'Claude Haiku 5.5',
        effort_levels: ['low', 'high'],
      },
    ],
  })
})

afterEach(() => vi.restoreAllMocks())

const RUNNING: RunDTO = {
  run_ref: 'run_live',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
}

describe('WorkComposer tab drafts', () => {
  const composer = (run = RUNNING) => (
    <WorkComposer attached={{ run, group: 'active' }} />
  )
  const field = () => screen.getByRole('textbox', { name: 'Session turn' })
  const draftKeys = () =>
    Object.keys(sessionStorage).filter((key) =>
      key.startsWith('olivares.draft.'),
    )

  // dev#2787: the message the person sent appeared only when the tool took the turn,
  // seconds later, so they could not tell it had been sent.
  it('hands the conversation the message as soon as the engine takes it', async () => {
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'Look up the readme')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(sentTurnsOf(RUNNING.run_ref)).toEqual([
        { text: 'Look up the readme' },
      ]),
    )
  })

  it('places the message after what the session showed when it was sent, even if its echo comes first', async () => {
    useSentTurns.setState({
      byRun: {},
      seen: { [RUNNING.run_ref]: 2 },
      said: { [RUNNING.run_ref]: { continue: 1 } },
    })
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    // The tool's echo renders before the engine's answer arrives.
    act(() => useSentTurns.getState().see(RUNNING.run_ref, 3, { continue: 2 }))
    await act(async () => finish({ accepted: true }))
    expect(sentTurnsOf(RUNNING.run_ref)).toEqual([
      { text: 'continue', after: 2, shown: 1 },
    ])
  })

  it('notes no slash command: the tool answers a command, it does not show it', async () => {
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), '/compact')
    await user.keyboard('{Escape}')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(sentTurnsOf(RUNNING.run_ref)).toBeUndefined()
  })

  it('keeps the words and notes nothing when the engine refuses the message', async () => {
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
    api.input.mockRejectedValueOnce(
      new ApiError(409, 'conflict', 'the session is not accepting input'),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'Look up the readme')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(toasts.error).toHaveBeenCalled())
    expect(String(toasts.error.mock.calls.at(-1)?.[0])).toContain(
      'the session is not accepting input',
    )
    expect(field()).toHaveValue('Look up the readme')
    expect(sentTurnsOf(RUNNING.run_ref)).toBeUndefined()
  })

  it('clears the current field when a send completes after an unedited session round trip', async () => {
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'This message is being sent')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    view.rerender(composer({ ...RUNNING, run_ref: 'run_other' }))
    view.rerender(composer())
    expect(field()).toHaveValue('This message is being sent')
    await act(async () => finish({ accepted: true }))
    expect(field()).toHaveValue('')
    expect(draftKeys()).toHaveLength(0)
  })

  it('keeps editing and sending available and explains when tab storage refuses a write', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('Storage full', 'QuotaExceededError')
    })
    await user.type(field(), 'Unsaved message')
    expect(field()).toHaveValue('Unsaved message')
    expect(screen.getByRole('status', { name: 'Draft' })).toHaveTextContent(
      'Draft could not be saved in this tab.',
    )
    expect(screen.getByTestId('composer-send')).toBeEnabled()
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(field()).toHaveValue(''))
    expect(api.input).toHaveBeenCalledOnce()
  })

  it('clears a draft whose send completes while another session is open', async () => {
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Sent from the closed session')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    view.rerender(composer({ ...RUNNING, run_ref: 'run_other' }))
    await act(async () => finish({ accepted: true }))
    await user.type(field(), 'The open session is unaffected')
    view.rerender(composer())
    expect(field()).toHaveValue('')
    expect(draftKeys()).toHaveLength(1)
  })

  it('keeps a storage-refused draft for this tab across a session switch', async () => {
    const user = userEvent.setup()
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    const other = { ...RUNNING, run_ref: 'run_other' }
    const view = renderIntel(composer())
    setItem.mockImplementation(() => {
      throw new DOMException('Storage full', 'QuotaExceededError')
    })
    await user.type(field(), 'Only this tab keeps it')
    view.rerender(composer(other))
    await user.type(field(), 'Other session text')
    view.rerender(composer())
    expect(field()).toHaveValue('Only this tab keeps it')
    view.unmount()
    const restored = renderIntel(composer())
    expect(field()).toHaveValue('Only this tab keeps it')
    // A tab whose storage recovers persists again and stops warning.
    setItem.mockRestore()
    await user.clear(field())
    await user.type(field(), 'Saved once more')
    expect(screen.queryByRole('status', { name: 'Draft' })).toBeNull()
    restored.unmount()
    renderIntel(composer())
    expect(field()).toHaveValue('Saved once more')
  })

  it('keeps an in-flight send blocking its draft across a session round trip', async () => {
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Already going out')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    view.rerender(composer({ ...RUNNING, run_ref: 'run_other' }))
    view.rerender(composer())
    // The remounted field must not offer the pending message as a new draft.
    expect(field()).toBeDisabled()
    expect(screen.getByTestId('composer-send')).toBeDisabled()
    await act(async () => finish({ accepted: true }))
    await waitFor(() => expect(field()).toBeEnabled())
    expect(api.input).toHaveBeenCalledOnce()
    expect(field()).toHaveValue('')
  })

  it('keeps two session drafts separate on switching and remounting', async () => {
    const user = userEvent.setup()
    const other = { ...RUNNING, run_ref: 'run_other' }
    const view = renderIntel(composer())
    await user.type(field(), 'First unsent message')
    expect(api.inputText).not.toHaveBeenCalled()
    expect(api.input).not.toHaveBeenCalled()
    view.rerender(composer(other))
    expect(field()).toHaveValue('')
    await user.type(field(), 'Second unsent message')
    view.rerender(composer())
    expect(field()).toHaveValue('First unsent message')
    view.unmount()
    const remounted = renderIntel(composer())
    expect(field()).toHaveValue('First unsent message')
    remounted.rerender(composer(other))
    expect(field()).toHaveValue('Second unsent message')
    expect(draftKeys()).toHaveLength(2)
    expect(localStorage.getItem(draftKeys()[0])).toBeNull()
  })

  it('removes the stored draft after sending, preserving the other session', async () => {
    const user = userEvent.setup()
    const view = renderIntel(composer({ ...RUNNING, run_ref: 'run_other' }))
    await user.type(field(), 'Other draft')
    view.rerender(composer())
    await user.type(field(), 'Send this message')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(field()).toHaveValue(''))
    expect(api.input).toHaveBeenCalledWith(
      'run_live',
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'Send this message' },
      }),
      undefined,
    )
    expect(draftKeys()).toHaveLength(1)
    view.unmount()
    renderIntel(composer())
    expect(field()).toHaveValue('')
  })

  it('keeps a rejected message available after remount', async () => {
    api.input.mockRejectedValueOnce(new Error('Send rejected'))
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Keep this message')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(toasts.error).toHaveBeenCalled())
    expect(field()).toHaveValue('Keep this message')
    view.unmount()
    renderIntel(composer())
    expect(field()).toHaveValue('Keep this message')
  })

  it('drops a draft stored 25 hours ago on read', async () => {
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Expired draft')
    const [key] = draftKeys()
    expect(key).toBeDefined()
    sessionStorage.setItem(
      key,
      JSON.stringify({
        text: 'Expired draft',
        updatedAt: Date.now() - 25 * 60 * 60 * 1000,
      }),
    )
    view.unmount()
    renderIntel(composer())
    expect(field()).toHaveValue('')
    expect(sessionStorage.getItem(key)).toBeNull()
  })

  it('isolates tenants and principals even when the session address is identical', async () => {
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Alice in the first tenant')
    auth.activeTenant = 'tnt-other'
    view.rerender(composer())
    expect(field()).toHaveValue('')
    await user.type(field(), 'Alice in the other tenant')
    auth.principal = { kind: 'user', actor: 'user:bob', user_id: 'bob' }
    view.rerender(composer())
    expect(field()).toHaveValue('')
    await user.type(field(), 'Bob in the other tenant')
    auth.principal = { kind: 'user', actor: 'user:alice', user_id: 'alice' }
    view.rerender(composer())
    expect(field()).toHaveValue('Alice in the other tenant')
    auth.activeTenant = 'tnt-demo'
    view.rerender(composer())
    expect(field()).toHaveValue('Alice in the first tenant')
    expect(draftKeys()).toHaveLength(3)
    expect(
      draftKeys().every((key) => key.includes('alice') || key.includes('bob')),
    ).toBe(true)
    expect(
      draftKeys().every(
        (key) => key.includes('tnt-demo') || key.includes('tnt-other'),
      ),
    ).toBe(true)
  })

  it.each<[string, Partial<NonNullable<typeof auth.principal>>]>([
    ['kind', { kind: 'token' }],
    ['actor', { actor: 'user:alice-2' }],
    ['user_id', { user_id: 'alice-2' }],
  ])(
    'does not share a draft with a principal that differs only in %s',
    async (_field, change) => {
      const user = userEvent.setup()
      const view = renderIntel(composer())
      await user.type(field(), 'Alice only')
      auth.principal = { ...auth.principal!, ...change }
      view.rerender(composer())
      expect(field()).toHaveValue('')
    },
  )

  it('keeps a newer edit made while an earlier send is in flight', async () => {
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'Going out now')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    // The field is locked while sending; a dictation result still lands in the draft.
    act(() => {
      writeSessionDraft(
        sessionDraftKey('tnt-demo', auth.principal, 'run_live'),
        'Dictated afterwards',
      )
    })
    await act(async () => finish({ accepted: true }))
    await waitFor(() => expect(field()).toBeEnabled())
    expect(field()).toHaveValue('Dictated afterwards')
  })

  it('does not restore or persist without a resolved principal', async () => {
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.type(field(), 'Private draft')
    auth.principal = null
    view.rerender(composer())
    expect(field()).toHaveValue('')
    await user.type(field(), 'Unresolved identity')
    expect(draftKeys()).toHaveLength(1)
    auth.principal = { kind: 'user', actor: 'user:alice', user_id: 'alice' }
    view.rerender(composer())
    expect(field()).toHaveValue('Private draft')
  })
})

describe('WorkComposer permissions', () => {
  it('opens the pending approval from Needs you', async () => {
    const user = userEvent.setup()
    const root = createRootRoute({ component: Outlet })
    const session = createRoute({
      getParentRoute: () => root,
      path: '/',
      component: () => (
        <WorkComposer
          attached={{
            run: { ...RUNNING, pending_approval_ref: 'apr_7' },
            group: 'attention',
          }}
        />
      ),
    })
    const approvals = createRoute({
      getParentRoute: () => root,
      path: '/permissions',
      component: () => <p>Approval details</p>,
    })
    const router = createRouter({
      routeTree: root.addChildren([session, approvals]),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
    renderIntel(<RouterProvider router={router} />)
    const link = await screen.findByRole('link', { name: 'Needs you' })
    expect(link).toHaveAttribute(
      'href',
      '/permissions?tab=approvals&approval=apr_7',
    )
    await user.click(link)
    expect(await screen.findByText('Approval details')).toBeInTheDocument()
    expect(router.state.location.search).toEqual({
      tab: 'approvals',
      approval: 'apr_7',
    })
  })

  it('a launch held for its approval says so and opens that approval', async () => {
    const root = createRootRoute({ component: Outlet })
    const session = createRoute({
      getParentRoute: () => root,
      path: '/',
      component: () => (
        <WorkComposer
          attached={{
            run: {
              ...RUNNING,
              state: 'waiting_approval',
              critical: true,
              approval_ref: 'apr_launch',
            },
            group: 'attention',
          }}
        />
      ),
    })
    const router = createRouter({
      routeTree: root.addChildren([session]),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
    renderIntel(<RouterProvider router={router} />)
    const link = await screen.findByRole('link', { name: 'Needs you' })
    expect(link).toHaveAttribute(
      'href',
      '/permissions?tab=approvals&approval=apr_launch',
    )
    const send = screen.getByTestId('composer-send')
    expect(send).toBeDisabled()
    expect(send).toHaveAccessibleDescription(
      'The launch waits for an approval; Send works once it is approved.',
    )
    expect(
      screen.getByTestId('composer-wire-send'),
    ).toHaveAccessibleDescription(
      'The launch waits for an approval; Send works once it is approved.',
    )
  })

  it('draws no status line and no approval link for a held launch without governance:approval:read', async () => {
    auth.can = (p) => p !== 'governance:approval:read'
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            state: 'waiting_approval',
            approval_ref: 'apr_launch',
          },
          group: 'attention',
        }}
      />,
    )
    // No status line under the field: without the read permission there is no approval to
    // open, so the composer says nothing of it.
    await screen.findByTestId('composer-send')
    expect(screen.queryByText('Needs you')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Needs you' })).toBeNull()
  })

  it('a running session whose launch was approved does not link the old approval', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, critical: true, approval_ref: 'apr_launch' },
          group: 'attention',
        }}
      />,
    )
    await screen.findByTestId('composer-send')
    expect(screen.queryByText('Needs you')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Needs you' })).toBeNull()
  })

  it.each(['governance:approval:read', 'governance:identity:read'])(
    'draws no status line and no approval link without %s',
    async (permission) => {
      auth.can = (p) => p !== permission
      renderIntel(
        <WorkComposer
          attached={{
            run: { ...RUNNING, pending_approval_ref: 'apr_7' },
            group: 'attention',
          }}
        />,
      )
      await screen.findByTestId('composer-send')
      expect(screen.queryByText('Needs you')).toBeNull()
      expect(screen.queryByRole('link', { name: 'Needs you' })).toBeNull()
    },
  )

  it('a read-only operator attached to a run reads the run\u2019s real scope, not "none"', async () => {
    auth.can = (p: string) => p !== 'sessions:run:write'
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            workspace_ref: 'ws-main',
            provider_environment_ref: 'xenv_prod',
          },
          group: 'active',
        }}
      />,
    )
    const line = await screen.findByTestId('work-scope-line')
    expect(line).toHaveTextContent('ws-main')
    expect(line).toHaveTextContent('xenv_prod')
    expect(line).not.toHaveTextContent(/No workspace/i)
    expect(line).not.toHaveTextContent(/No environment/i)
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('composer-send')).toBeNull()
    expect(screen.queryByTestId('composer-advanced')).toBeNull()
  })
})

describe('ScopeLine — the scope of the next action', () => {
  it('names a known organization with the name the switcher uses, not its id', async () => {
    const tenant = '01a0b95a-ee26-724d-9db8-1f5423e25db3'
    auth.activeTenant = tenant
    // Community has no estate inventory: the name is the one the principal's own grant carries.
    auth.grants = [{ tenant, tenant_name: 'Golden Path' }]
    renderIntel(<ScopeLine workspace={null} environment={null} />)
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('Golden Path'))
    expect(line.textContent).not.toMatch(/01a0b95a/)
  })

  it('falls back to a short identifier when the organization name is not known', async () => {
    const tenant = '01a0b95a-ee26-724d-9db8-1f5423e25db3'
    auth.activeTenant = tenant
    renderIntel(<ScopeLine workspace={null} environment={null} />)
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('01a0b95a…'))
    expect(line.textContent).not.toContain(tenant)
  })

  it('does not paint a raw environment reference when the profile is not this node', async () => {
    api.listProfiles.mockResolvedValue({
      items: [
        {
          ...PROFILE,
          local_environment: false,
          environment_ref: 'xenv_01a0b95a-ec91-7014-875a-5732',
        },
      ],
      has_more: false,
    })
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await userEvent.click(await screen.findByTestId('composer-tool-model'))
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('another environment'))
    expect(line).not.toHaveTextContent('xenv_01a0b95a-ec91-7014-875a-5732')
    expect(line.getAttribute('title')).toContain(
      'xenv_01a0b95a-ec91-7014-875a-5732',
    )
  })

  it('names the organization, and says nothing about a workspace when none is chosen', async () => {
    renderIntel(<ScopeLine workspace={null} environment={null} />)
    const line = await screen.findByTestId('work-scope-line')
    expect(line).toHaveTextContent('tnt-demo')
    expect(line).not.toHaveTextContent(/workspace/i)
    expect(line).toHaveTextContent('Not declared')
  })

  it('names the attached profile environment and keeps its reference on title', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await userEvent.click(await screen.findByTestId('composer-tool-model'))
    await waitFor(() =>
      expect(screen.getByTestId('work-scope-line')).toHaveTextContent(
        'This node',
      ),
    )
    expect(
      screen.getByTestId('work-scope-line').getAttribute('title'),
    ).toContain('env-prod')
    expect(screen.getByTestId('work-scope-line').textContent).not.toMatch(
      /env-prod/,
    )
  })
})

describe('WorkComposer layout and disclosure', () => {
  it('is a raised card of 14 px radius and a 1 px line, in the transcript column', async () => {
    renderIntel(
      <WorkComposer
        frame="docked"
        attached={{ run: RUNNING, group: 'active', title: 'calc-review' }}
      />,
    )
    const box = await screen.findByTestId('work-composer')
    expect(box).toHaveAttribute('data-attached', 'true')
    const cls = box.className.split(/\s+/)
    expect(cls).toEqual(
      expect.arrayContaining([
        'rounded-[14px]',
        'border',
        'border-line',
        'bg-raised',
      ]),
    )
    expect(box.parentElement!.className).toContain('max-w-[760px]')
    expect(box.getAttribute('title') ?? '').toMatch(/Organization|Workspace/)
    expect(screen.getByTestId('composer-advanced')).toBeInTheDocument()
    expect(screen.getByTestId('composer-send')).toBeInTheDocument()
    // No status line under the field, and no dashed Send.
    expect(screen.queryByTestId('composer-session-state')).toBeNull()
    expect(screen.queryByText('Working')).toBeNull()
    expect(screen.getByTestId('composer-send').className).not.toMatch(
      /(^|\s)border-dashed/,
    )
  })

  it('is a multi-line field of two to eight rows, addressed to the session', async () => {
    renderIntel(
      <WorkComposer
        attached={{ run: RUNNING, group: 'active', title: 'calc-review' }}
      />,
    )
    const field = await screen.findByTestId('launcher-input')
    expect(field.tagName).toBe('TEXTAREA')
    expect(field).toHaveAttribute('rows', '2')
    expect(field).toHaveAttribute(
      'placeholder',
      'Ask anything, / for commands and skills',
    )
    expect(field.className).toContain('max-h-48')
    expect(field.className).toContain('resize-none')
  })

  it('names the tool, model and effort of the session on the control that changes them', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            provider_driver: 'codex',
            model_ref: 'stub-model',
            effort: 'high',
          },
          group: 'active',
        }}
      />,
    )
    const chip = await screen.findByTestId('composer-tool-model')
    // Three pickers (dev#3162): the model on its own, the effort on the next.
    expect(chip).toHaveTextContent(/^Stub Model$/)
    expect(screen.getByTestId('composer-effort')).toHaveTextContent(/^High$/)
    expect(chip.tagName).toBe('BUTTON')
    expect(chip).toHaveAccessibleName('Model: Stub Model')
  })

  // dev#3120: "claude-opus-5-5" was cut to "clau…" in the composer. It now says
  // "Claude Opus 5.5", and the id stays one hover away.
  it('says the model and the effort in words, with the id on the hover', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            provider_driver: 'claude',
            model_ref: 'claude-opus-5-5',
            effort: 'high',
          },
          group: 'active',
        }}
      />,
    )
    const chip = await screen.findByTestId('composer-tool-model')
    expect(chip.textContent).toBe('Claude Opus 5.5')
    expect(chip.getAttribute('title')).toContain('claude-opus-5-5')
  })

  it('Send is a 32 px round icon button: orange only with text, a quiet ghost otherwise', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const send = await screen.findByTestId('composer-send')
    expect(send).toHaveAccessibleName('Send')
    expect(send.className).toContain('rounded-full')
    expect(send.className).toContain('size-8')
    expect(send.className).not.toContain('bg-accent ')
    expect(send).toBeDisabled()
    await user.type(screen.getByTestId('launcher-input'), 'hello')
    expect(send).toBeEnabled()
    expect(send.className).toContain('bg-accent')
    expect(send.className).toContain('text-on-accent')
  })

  it('Enter sends and Shift+Enter breaks a line', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = await screen.findByTestId('launcher-input')
    await user.type(field, 'one{Shift>}{Enter}{/Shift}two')
    expect(field).toHaveValue('one\ntwo')
    expect(api.input).not.toHaveBeenCalled()
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(String(api.input.mock.calls[0][1])).toContain('one\\ntwo')
  })

  it('Advanced is a native details/summary; Escape closes it and returns focus to the summary', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const summary = await screen.findByTestId('composer-advanced')
    expect(summary.tagName).toBe('SUMMARY')
    const disclosure = summary.closest('details')
    expect(disclosure).not.toBeNull()
    expect(summary).not.toHaveAttribute('aria-expanded')
    await user.click(summary)
    expect(disclosure).toHaveAttribute('open')
    expect(screen.getByTestId('composer-wire-input')).toBeVisible()
    await user.keyboard('{Escape}')
    expect(disclosure).not.toHaveAttribute('open')
    expect(summary).toHaveFocus()
  })
})

describe('WorkComposer — attached session input', () => {
  it('keeps two composers and their submitted turns separate', async () => {
    const user = userEvent.setup()
    renderIntel(
      <>
        <WorkComposer
          inputId="first-composer"
          attached={{ run: RUNNING, group: 'active' }}
        />
        <WorkComposer
          inputId="second-composer"
          attached={{
            run: {
              ...RUNNING,
              run_ref: 'run_second',
              provider_driver: 'codex',
            },
            group: 'active',
          }}
        />
      </>,
    )
    const first = document.getElementById('first-composer')!
    const second = document.getElementById('second-composer')!
    expect(first).not.toBeNull()
    expect(second).not.toBeNull()
    await user.type(first, 'First draft')
    await user.type(second, 'Second turn')
    expect(second).toHaveFocus()
    expect(first).not.toHaveFocus()
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.inputText).toHaveBeenCalledWith(
        'run_second',
        'Second turn',
        undefined,
      ),
    )
    expect(api.inputText).toHaveBeenCalledTimes(1)
    expect(first).toHaveValue('First draft')
    await user.click(first)
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_live',
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'First draft' },
        }),
        undefined,
      ),
    )
    expect(api.inputText).toHaveBeenCalledTimes(1)
    expect(api.input).toHaveBeenCalledTimes(1)
  })

  it('gives the field the shared composer id when the caller passes none', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = await screen.findByTestId('launcher-input')
    expect(field).toHaveAttribute('id', COMPOSER_INPUT_ID)
  })

  it('sends a sentence as a user frame on a Claude run', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = await screen.findByTestId('launcher-input')
    await user.type(field, 'Reply with the single word OK')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input).toHaveBeenCalledWith(
      'run_live',
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'Reply with the single word OK' },
      }),
      undefined,
    )
    expect(api.inputText).not.toHaveBeenCalled()
  })

  it('sends a Codex run as {text}', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, provider_driver: 'codex' },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'hello')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.inputText).toHaveBeenCalledTimes(1))
    expect(api.inputText).toHaveBeenCalledWith('run_live', 'hello', undefined)
  })

  it('carries the run work-lease fence on a sentence, as the CLI does', async () => {
    api.getRun.mockResolvedValue({
      ...RUNNING,
      work_item_id: 'work-a',
      work_lease_fence: 7,
      work_lease_state: 'active',
    })
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            provider_driver: 'codex',
            work_item_id: 'work-a',
            work_lease_fence: 7,
            work_owner_epoch: 2,
            work_dispatch_key: 'dispatch-a',
          },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.inputText).toHaveBeenCalledTimes(1))
    expect(api.inputText).toHaveBeenCalledWith('run_live', 'continue', 7)
  })

  it('carries the fence on a wrapped user frame too', async () => {
    api.getRun.mockResolvedValue({
      ...RUNNING,
      work_item_id: 'work-a',
      work_lease_fence: 7,
      work_lease_state: 'active',
    })
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, work_item_id: 'work-a', work_lease_fence: 7 },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input).toHaveBeenCalledWith(
      'run_live',
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'continue' },
      }),
      7,
    )
  })

  it('after the work item is submitted, Send carries no fence', async () => {
    api.getRun.mockResolvedValue({
      ...RUNNING,
      work_item_id: 'work-a',
      work_lease_fence: 7,
      work_lease_state: 'ended',
    })
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, work_item_id: 'work-a', work_lease_fence: 7 },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.getRun).toHaveBeenCalledWith(RUNNING.run_ref)
    expect(api.input.mock.calls[0][2]).toBeUndefined()
  })

  it('sends no fence for a run with no work stamp', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input.mock.calls[0][2]).toBeUndefined()
  })

  it('anchors the advanced panel to the box, so a clipping pane cannot cut it', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const box = await screen.findByTestId('work-composer')
    expect(box.className).toMatch(/\brelative\b/)
    const disclosure = screen
      .getByTestId('composer-advanced')
      .closest('details') as HTMLElement
    expect(disclosure.className).not.toMatch(/\brelative\b/)
    const panel = disclosure.querySelector('div') as HTMLElement
    expect(panel.className).toMatch(/\babsolute\b/)
    expect(panel.className).toMatch(/\binset-x-0\b/)
    expect(panel.className).not.toMatch(/min-w-\[/)
  })

  it('keeps NDJSON behind the advanced disclosure', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await screen.findByTestId('launcher-input')
    expect(screen.getByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Ask anything, / for commands and skills',
    )
    expect(screen.getByTestId('composer-advanced')).toBeInTheDocument()
    expect(
      screen.getByTestId('composer-wire-input').getAttribute('placeholder'),
    ).toMatch(/NDJSON/i)
  })
})

// A disabled Send says why and what unblocks it, in visible text the button
// points at (the release surface guard reads aria-describedby, as these do).
describe('WorkComposer — a disabled Send says why', () => {
  it('asks for a sentence while the box is empty, and the reason leaves once one is typed', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const send = await screen.findByTestId('composer-send')
    expect(send).toBeDisabled()
    expect(send).toHaveAccessibleDescription('Type something to send.')
    await user.type(screen.getByTestId('launcher-input'), 'hello')
    expect(send).toBeEnabled()
    expect(send).not.toHaveAttribute('aria-describedby')
  })

  it('says where the sentence would go when the session is not live, in the field itself', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, state: 'stopped', claude_session_id: 'cs-1' },
          group: 'settled',
        }}
      />,
    )
    const send = await screen.findByTestId('composer-send')
    expect(send).toBeDisabled()
    expect(send).toHaveAccessibleDescription(
      'Resume the session to send a message.',
    )
    const field = screen.getByTestId('launcher-input')
    expect(field).toBeDisabled()
    expect(field).toHaveAttribute(
      'placeholder',
      'Resume the session to send a message.',
    )
  })

  it('names the step the header offers: Start again for a run that never got going', async () => {
    const again = renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, state: 'stopped' },
          group: 'settled',
        }}
      />,
    )
    expect(await screen.findByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Start the session again to send a message.',
    )
    again.unmount()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, state: 'stopped', claude_session_id: 'cs-1' },
          group: 'settled',
        }}
      />,
    )
    expect(await screen.findByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Resume the session to send a message.',
    )
  })

  it('says a session that cannot be resumed has ended, and one starting is starting', async () => {
    const ended = renderIntel(
      <WorkComposer
        attached={{ run: { ...RUNNING, state: 'cleaned' }, group: 'settled' }}
      />,
    )
    expect(await screen.findByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'This session has ended.',
    )
    ended.unmount()
    renderIntel(
      <WorkComposer
        attached={{ run: { ...RUNNING, state: 'pending' }, group: 'active' }}
      />,
    )
    expect(await screen.findByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'The session is starting…',
    )
  })

  it('says the sentence is on its way while it is being sent', async () => {
    const user = userEvent.setup()
    api.input.mockReturnValue(new Promise(() => {}))
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.type(await screen.findByTestId('launcher-input'), 'hello')
    await user.click(screen.getByTestId('composer-send'))
    const send = screen.getByTestId('composer-send')
    await waitFor(() => expect(send).toBeDisabled())
    expect(send).toHaveAccessibleDescription(
      'Sending… Wait for the session to accept it.',
    )
  })

  it('the advanced line Send reads its own box, not the sentence box', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.type(await screen.findByTestId('launcher-input'), 'hello')
    const wireSend = screen.getByTestId('composer-wire-send')
    expect(wireSend).toBeDisabled()
    expect(wireSend).toHaveAccessibleDescription('Type something to send.')
  })

  it('the advanced line Send names the session state when it is not live', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, state: 'stopped', claude_session_id: 'cs-1' },
          group: 'settled',
        }}
      />,
    )
    const wireSend = await screen.findByTestId('composer-wire-send')
    expect(wireSend).toBeDisabled()
    expect(wireSend).toHaveAccessibleDescription(
      'Resume the session to send a message.',
    )
  })

  it('the advanced line Send says a sentence is on its way', async () => {
    const user = userEvent.setup()
    api.input.mockReturnValue(new Promise(() => {}))
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.type(await screen.findByTestId('launcher-input'), 'hello')
    await user.click(screen.getByTestId('composer-send'))
    const wireSend = screen.getByTestId('composer-wire-send')
    await waitFor(() =>
      expect(wireSend).toHaveAccessibleDescription(
        'Sending… Wait for the session to accept it.',
      ),
    )
  })
})

// #1779: the message bar's "Advanced" opened a developer's raw stdin line to every user, and
// the model and effort a person looked for were nowhere in it.
describe('WorkComposer — model and effort', () => {
  const ATTACHED = {
    run: {
      ...RUNNING,
      model_ref: 'opus-5-5',
      effort: 'medium',
    },
    group: 'active' as const,
  }

  it.each([undefined, 'priority'])(
    'clears a retired service tier (persisted: %s) after catalog failure',
    async (persisted) => {
      const user = userEvent.setup()
      const qc = createTestQueryClient()
      api.profileModels.mockResolvedValue({
        state: 'fresh',
        default_model: 'opus-5-5',
        models: [
          {
            id: 'opus-5-5',
            service_tiers: ['priority'],
            effort_levels: ['medium', 'high'],
          },
        ],
      })
      renderIntel(
        <WorkComposer
          attached={{
            ...ATTACHED,
            run: {
              ...ATTACHED.run,
              provider_driver: 'codex',
              service_tier: persisted,
              ...(persisted
                ? { model_ref: undefined, usage_model_ref: undefined }
                : {}),
            },
          }}
        />,
        { queryClient: qc },
      )
      await user.click(await screen.findByTestId('composer-tool-model'))
      await user.click(await screen.findByLabelText('Service tier'))
      await user.click(screen.getByRole('option', { name: 'priority' }))
      await user.click(screen.getByLabelText('Effort'))
      await user.click(screen.getByRole('option', { name: 'high' }))
      api.profileModels.mockRejectedValue(new Error('Catalog offline'))
      await qc.invalidateQueries({
        predicate: (q) => q.queryKey.includes('models'),
      })
      await waitFor(() =>
        expect(screen.getByTestId('composer-new-with-choice')).toBeDisabled(),
      )
      expect(screen.getByRole('alert')).toHaveTextContent(
        'Speed options changed',
      )
      await user.click(screen.getByRole('button', { name: 'Use tool default' }))
      expect(screen.getByTestId('composer-new-with-choice')).toBeEnabled()
      await user.click(screen.getByTestId('composer-new-with-choice'))
      const { useNewSessionDialog } =
        await import('@/features/first-hour/new-session-store')
      expect(useNewSessionDialog.getState().advanced).not.toHaveProperty(
        'serviceTier',
      )
    },
  )

  it('shows persisted Fast On, preserves it for effort, and allows the native default', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [
        {
          id: 'opus-5-5',
          supports_fast_mode: true,
          effort_levels: ['medium', 'high'],
        },
      ],
    })
    renderIntel(
      <WorkComposer
        attached={{ ...ATTACHED, run: { ...ATTACHED.run, fast_mode: true } }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    expect(await screen.findByLabelText('Fast mode')).toHaveTextContent('On')
    expect(screen.queryByTestId('composer-new-with-choice')).toBeNull()
    await user.click(screen.getByLabelText('Effort'))
    await user.click(screen.getByRole('option', { name: 'high' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      fastMode: true,
      effort: 'high',
    })
    await user.click(screen.getByTestId('composer-tool-model'))
    await user.click(await screen.findByLabelText('Fast mode'))
    await user.click(screen.getByRole('option', { name: 'Model default' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).not.toHaveProperty(
      'fastMode',
    )
  })

  // dev#2786: a session records the exact id its catalog selector launched on, which
  // the catalog does not list. Its own speed is the one the engine checked at launch.
  it('keeps the Fast On of a session recorded under its exact model id', async () => {
    const user = userEvent.setup()
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [{ id: 'default', supports_fast_mode: true }],
    })
    renderIntel(
      <WorkComposer
        attached={{
          ...ATTACHED,
          run: {
            ...ATTACHED.run,
            model_ref: 'claude-opus-5-5',
            fast_mode: true,
          },
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    expect(await screen.findByLabelText('Fast mode')).toHaveTextContent('On')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  // A Claude launch with no model is recorded under the exact id of the tool's default,
  // which the catalog (the tool's selectors) does not list, and so is every session that
  // chose no speed. Off is a valid Claude launch on any model, so the message bar still
  // offers it; On waits for a listed model that reports Fast mode.
  it('offers Fast Off for a session recorded under an exact model id the catalog does not list', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [{ id: 'default', supports_fast_mode: true }],
    })
    renderIntel(
      <WorkComposer
        attached={{
          ...ATTACHED,
          run: { ...ATTACHED.run, model_ref: 'claude-opus-5-5' },
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    await user.click(await screen.findByLabelText('Fast mode'))
    expect(screen.getByRole('option', { name: 'On' })).toHaveAttribute(
      'aria-disabled',
      'true',
    )
    await user.click(screen.getByRole('option', { name: 'Off' }))
    expect(screen.queryByRole('alert')).toBeNull()
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      model: 'claude-opus-5-5',
      fastMode: false,
    })
  })

  it('drops Fast On when another model is picked and hides unsupported speed controls', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [
        { id: 'opus-5-5', supports_fast_mode: true },
        { id: 'haiku-5-5', display_name: 'Claude Haiku 5.5' },
      ],
    })
    renderIntel(<WorkComposer attached={ATTACHED} />)
    await user.click(await screen.findByTestId('composer-tool-model'))
    await user.click(await screen.findByLabelText('Fast mode'))
    await user.click(screen.getByRole('option', { name: 'On' }))
    await user.click(screen.getByText('Claude Haiku 5.5'))
    expect(screen.queryByLabelText('Fast mode')).toBeNull()
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).not.toHaveProperty(
      'fastMode',
    )
  })

  it('does not offer provider-bound Fast On', async () => {
    api.listProfiles.mockResolvedValue({
      items: [{ ...PROFILE, provider_record_ref: 'prv_bound' }],
    })
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [{ id: 'opus-5-5', supports_fast_mode: true }],
    })
    renderIntel(<WorkComposer attached={ATTACHED} />)
    await userEvent.click(await screen.findByTestId('composer-tool-model'))
    await waitFor(() => expect(api.profileModels).toHaveBeenCalled())
    expect(screen.queryByLabelText('Fast mode')).toBeNull()
  })

  it.each([
    { driver: 'claude', label: 'Fast mode', option: 'On', fastMode: true },
    { driver: 'claude', label: 'Fast mode', option: 'Off', fastMode: false },
    {
      driver: 'codex',
      label: 'Service tier',
      option: 'priority',
      serviceTier: 'priority',
    },
  ])('hands the message-bar $label choice to a new session', async (choice) => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    useNewSessionDialog.setState({ advanced: null })
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [
        {
          id: 'opus-5-5',
          supports_fast_mode: true,
          service_tiers: ['priority'],
        },
      ],
    })
    renderIntel(
      <WorkComposer
        attached={{
          ...ATTACHED,
          run: { ...ATTACHED.run, provider_driver: choice.driver },
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    await user.click(await screen.findByLabelText(choice.label))
    await user.click(await screen.findByRole('option', { name: choice.option }))
    await user.click(await screen.findByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      open: true,
      profileRef: 'ppf_team',
      model: 'opus-5-5',
      effort: 'medium',
      ...(choice.driver === 'claude'
        ? { fastMode: choice.fastMode }
        : { serviceTier: choice.serviceTier }),
    })
    expect(api.input).not.toHaveBeenCalled()
    expect(api.inputText).not.toHaveBeenCalled()
  })

  it("lists the tool's models and the session's effort levels from the message bar", async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={ATTACHED} />)
    const chip = await screen.findByTestId('composer-tool-model')
    expect(chip).toHaveTextContent(/^Opus 5\.5$/)
    await user.click(chip)
    expect(await screen.findByText('Claude Haiku 5.5')).toBeInTheDocument()
    expect(api.profileModels).toHaveBeenCalledWith(
      'ppf_team',
      expect.objectContaining({ tenant: 'tnt-demo' }),
    )
    expect(screen.getByLabelText('Effort')).toBeEnabled()
    // Nothing differs from the session yet: no new session is offered.
    expect(screen.queryByTestId('composer-new-with-choice')).toBeNull()
  })

  it('says the session keeps its model, and starts a new session with the other choice preset', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    useNewSessionDialog.setState({ advanced: null })
    renderIntel(<WorkComposer attached={ATTACHED} />)
    await user.click(await screen.findByTestId('composer-tool-model'))
    await user.click(await screen.findByText('Claude Haiku 5.5'))
    expect(
      await screen.findByText(
        'This session keeps Claude Code · Opus 5.5 · medium. A different choice starts a new session.',
      ),
    ).toBeInTheDocument()
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      open: true,
      profileRef: 'ppf_team',
      model: 'haiku-5-5',
      // Haiku takes low and high only: the session's medium is not carried over.
      effort: '',
    })
    // The session itself was not touched.
    expect(api.input).not.toHaveBeenCalled()
    expect(api.inputText).not.toHaveBeenCalled()
  })

  it('shows no raw stdin line and no control named Advanced in a production console', async () => {
    vi.stubEnv('DEV', false)
    try {
      renderIntel(<WorkComposer attached={ATTACHED} />)
      await screen.findByTestId('composer-tool-model')
      expect(screen.queryByTestId('composer-advanced')).toBeNull()
      expect(screen.queryByTestId('composer-wire-input')).toBeNull()
      expect(screen.queryByText(/advanced/i)).toBeNull()
      expect(screen.getByTestId('launcher-input')).toBeVisible()
    } finally {
      vi.unstubAllEnvs()
    }
  })

  it('names the raw line what it is in a development console', async () => {
    renderIntel(<WorkComposer attached={ATTACHED} />)
    expect(await screen.findByTestId('composer-advanced')).toHaveTextContent(
      'Raw line',
    )
  })
  it('reads the catalog only when the control is opened, and not for a read-only operator', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={ATTACHED} />)
    await user.click(await screen.findByTestId('composer-tool-model'))
    await screen.findByText('Claude Haiku 5.5')
    expect(api.profileModels).toHaveBeenCalledTimes(1)
  })

  it('does not read the catalog at all without the right to start work', async () => {
    auth.can = (p) => p !== 'sessions:run:write'
    renderIntel(<WorkComposer attached={ATTACHED} />)
    await screen.findByTestId('work-scope-line')
    expect(screen.queryByTestId('composer-tool-model')).toBeNull()
    expect(api.profileModels).not.toHaveBeenCalled()
  })

  it('forgets a choice that was not started when the control is closed', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={ATTACHED} />)
    const chip = await screen.findByTestId('composer-tool-model')
    await user.click(chip)
    await user.click(await screen.findByText('Claude Haiku 5.5'))
    await screen.findByTestId('composer-new-with-choice')
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByTestId('composer-new-with-choice')).toBeNull(),
    )
    await user.click(chip)
    await screen.findByText('Claude Haiku 5.5')
    expect(screen.queryByTestId('composer-new-with-choice')).toBeNull()
  })

  it('hands over the tool default as no model, and an effort-only change with the model it chose', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    renderIntel(<WorkComposer attached={ATTACHED} />)
    const chip = await screen.findByTestId('composer-tool-model')
    await user.click(chip)
    const list = await screen.findByRole('listbox')
    await user.click(await within(list).findByText('Default (Claude Opus 5.5)'))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      model: '',
      effort: 'medium',
    })
    useNewSessionDialog.setState({ advanced: null })
    await user.click(chip)
    await screen.findByText('Claude Haiku 5.5')
    await user.click(screen.getByLabelText('Effort'))
    await user.click(await screen.findByRole('option', { name: 'high' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      model: 'opus-5-5',
      effort: 'high',
    })
  })

  it('does not hand over as chosen a model the tool only reported', async () => {
    const user = userEvent.setup()
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    useNewSessionDialog.setState({ advanced: null })
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            model_ref: undefined,
            usage_model_ref: 'reported-by-usage',
            effort: 'low',
          },
          group: 'active',
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    await screen.findByText('Claude Haiku 5.5')
    await user.click(screen.getByLabelText('Effort'))
    await user.click(await screen.findByRole('option', { name: 'high' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      model: '',
      effort: 'high',
    })
  })
})

// The composer: model, effort and access are three pickers, and the field teaches
// what can be typed (dev#3162).
describe('WorkComposer — three pickers and a field that teaches (dev#3162)', () => {
  const attached = {
    run: {
      ...RUNNING,
      provider_driver: 'claude',
      model_ref: 'claude-opus-5-5',
      effort: 'high',
      permission_mode: 'default',
      tool_mode: 'default',
    },
    group: 'active' as const,
    title: 'calc-review',
  }

  it('says the model, the effort and the access each on its own picker', async () => {
    renderIntel(<WorkComposer attached={attached} />)
    expect(await screen.findByTestId('composer-tool-model')).toHaveTextContent(
      /^Claude Opus 5\.5$/,
    )
    expect(screen.getByTestId('composer-effort')).toHaveTextContent(/^High$/)
    expect(screen.getByTestId('composer-access')).toHaveTextContent(
      /^Ask before each action$/,
    )
    for (const id of [
      'composer-tool-model',
      'composer-effort',
      'composer-access',
    ])
      expect(screen.getByTestId(id).tagName).toBe('BUTTON')
  })

  it.each([
    [
      {
        tool_mode: 'dontAsk',
        permission_mode: 'dontAsk',
        template_id: 'tpl_ec',
      },
      'Edit files and run commands',
    ],
    [
      { tool_mode: 'bypassPermissions', permission_mode: 'bypassPermissions' },
      'Runs without asking',
    ],
  ])('words the access %o as New session does: %s', async (over, words) => {
    templates.get.mockResolvedValue({
      id: 'tpl_ec',
      name: 'Edits and commands',
      builtin: true,
    })
    renderIntel(
      <WorkComposer
        attached={{ ...attached, run: { ...attached.run, ...over } }}
      />,
    )
    await waitFor(() =>
      expect(screen.getByTestId('composer-access')).toHaveTextContent(
        new RegExp(`^${words}$`),
      ),
    )
  })

  it('cuts a long picker word with an ellipsis and keeps its chevron, in a narrow column', async () => {
    renderIntel(<WorkComposer attached={attached} />)
    for (const id of [
      'composer-tool-model',
      'composer-effort',
      'composer-access',
    ]) {
      const picker = await screen.findByTestId(id)
      const words = picker.querySelector('span')
      expect(words?.className.split(/\s+/)).toContain('truncate')
      expect(picker.lastElementChild?.tagName.toLowerCase()).toBe('svg')
    }
    // The effort is one short word: the model and the access give way before it.
    expect(screen.getByTestId('composer-effort').className).toContain(
      'shrink-0',
    )
  })

  it("opens the access picker on the session's permission, kept until a new session", async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={attached} />)
    await user.click(await screen.findByTestId('composer-access'))
    expect(
      await screen.findByTestId('composer-access-panel'),
    ).toHaveTextContent(/A different access starts a new session/)
  })

  it('draws how full the context is when the tool reports its window, and nothing otherwise', async () => {
    const first = renderIntel(<WorkComposer attached={attached} />)
    await screen.findByTestId('composer-access')
    await waitFor(() => expect(api.activity).toHaveBeenCalled())
    expect(screen.queryByTestId('composer-context')).toBeNull()
    first.unmount()
    api.activity.mockResolvedValue({
      ...ACTIVITY,
      context_tokens: 90_000,
      context_window: 200_000,
      context_percent: 45,
    })
    renderIntel(<WorkComposer attached={attached} />)
    const ring = await screen.findByTestId('composer-context')
    expect(ring).toHaveAccessibleName('90,000 tokens · 45.0% of 200,000')
  })

  it.each([
    [79, false],
    [80, true],
  ])('warns when the context is %i%% full: %s', async (percent, warns) => {
    api.activity.mockResolvedValue({
      ...ACTIVITY,
      context_tokens: 1000,
      context_window: 200_000,
      context_percent: percent,
    })
    renderIntel(<WorkComposer attached={attached} />)
    const ring = await screen.findByTestId('composer-context')
    expect(ring.className.split(/\s+/).includes('text-warning')).toBe(warns)
  })

  it('draws no ring for a window with no share of it reported', async () => {
    api.activity.mockResolvedValue({
      ...ACTIVITY,
      context_tokens: 1000,
      context_window: 200_000,
      context_percent: null,
    })
    renderIntel(<WorkComposer attached={attached} />)
    await screen.findByTestId('composer-access')
    await waitFor(() => expect(api.activity).toHaveBeenCalled())
    expect(screen.queryByTestId('composer-context')).toBeNull()
  })

  // dev#3267: the engine reads activity only from Claude Code and Codex records. For
  // another tool, and for a legacy session with no provider, it answers 422 or 409,
  // and the browser logs that answer as an error.
  it.each([['opencode'], [undefined]])(
    'asks no activity of a session whose record the engine does not read (%s)',
    async (driver) => {
      renderIntel(
        <WorkComposer
          attached={{
            ...attached,
            run: { ...attached.run, provider_driver: driver },
          }}
        />,
      )
      await screen.findByTestId('composer-access')
      await act(async () => {})
      expect(api.activity).not.toHaveBeenCalled()
      expect(screen.queryByTestId('composer-context')).toBeNull()
    },
  )

  it('teaches what can be typed', async () => {
    renderIntel(<WorkComposer attached={attached} />)
    expect(await screen.findByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Ask anything, / for commands and skills',
    )
  })
})
