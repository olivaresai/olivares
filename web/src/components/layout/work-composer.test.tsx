// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, createTestQueryClient } from '@/test/intel'
import {
  STASH_MAX_BYTES,
  clearSessionDrafts,
  readSessionDraft,
  sessionDraftKey,
  stashDraft,
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
  listRuns: vi.fn(),
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  resume: vi.fn(),
  getRun: vi.fn(),
  profileModels: vi.fn(),
  activity: vi.fn(),
  interrupt: vi.fn(),
  queue: vi.fn(),
  queueAdd: vi.fn(),
  uploadAttachment: vi.fn(),
  attachmentImage: vi.fn(),
  inputWithAttachments: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
const approvals = vi.hoisted(() => ({
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  decide: vi.fn(),
}))
vi.mock('@/features/governance/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/governance/api')>()),
  governanceApi: approvals,
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

// jsdom cannot record, so the composer draws no Dictate there; a test that needs the
// microphone gives the browser one.
const mic = vi.hoisted(() => ({
  canCapture: vi.fn(() => false),
  startCapture: vi.fn(),
}))
vi.mock('@/features/dictation/capture', () => mic)

import { buttonVariants } from '@/components/ui/button'
import { stubViewportWidth } from '@/test/viewport'
import { ApiError } from '@/lib/api/errors'
import { sentTurnsOf, useSentTurns } from '@/features/sessions/sent-turns'
import {
  CLIENT_SETTING_DEFAULTS,
  useClientSettings,
} from '@/features/settings/preferences'
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
  approvals.getApproval.mockRejectedValue(new Error('request unreadable'))
  approvals.listDecisions.mockResolvedValue({ items: [] })
  sessionStorage.clear()
  auth.principal = { kind: 'user', actor: 'user:alice', user_id: 'alice' }
  auth.activeTenant = 'tnt-demo'
  auth.grants = []
  auth.can = () => true
  api.listRuns.mockResolvedValue({ items: [], has_more: false })
  api.listProfiles.mockResolvedValue({ items: [PROFILE], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [WORKSPACE], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
  api.activity.mockResolvedValue(ACTIVITY)
  // Before the queue existed the call threw; a mock answering nothing would be read as data.
  api.queue.mockRejectedValue(new Error('no queue'))
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

/** The browser's shared storage is full; the tab's own still takes the unsent draft. */
function fillSharedStorage() {
  const write = Storage.prototype.setItem
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation((name, value) => {
    if (name.startsWith('olivares.stash.'))
      throw new DOMException('Storage full', 'QuotaExceededError')
    write.call(sessionStorage, name, value)
  })
}

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

  // the regression: the message the person sent appeared only when the tool took the turn,
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
    expect(field()).toHaveAttribute('readonly')
    expect(screen.getByTestId('composer-send')).toBeDisabled()
    await act(async () => finish({ accepted: true }))
    await waitFor(() => expect(field()).not.toHaveAttribute('readonly'))
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
    await waitFor(() => expect(field()).not.toHaveAttribute('readonly'))
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
  it('leaves a waiting request to the conversation, and keeps the field for a reply', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, pending_approval_ref: 'apr_7' },
          group: 'attention',
        }}
      />,
    )
    // The request is answered in the conversation above (the regression design): the composer
    // sends nobody out of Sessions, and a typed reply can still redirect the agent.
    expect(
      await screen.findByRole('textbox', { name: 'Session turn' }),
    ).toBeEnabled()
    expect(screen.queryByRole('link', { name: 'Needs you' })).toBeNull()
    expect(screen.queryByTestId('composer-approval')).toBeNull()
  })

  it('a launch held for its approval says so in the field and links nowhere', async () => {
    renderIntel(
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
      />,
    )
    await screen.findByTestId('composer-send')
    expect(screen.queryByRole('link', { name: 'Needs you' })).toBeNull()
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

/** The classes a size of the shared button adds, whatever values the tokens give it. */
function sizeClasses(size: 'sm' | 'icon') {
  const base = buttonVariants({ variant: 'ghost', size: 'base' }).split(' ')
  return buttonVariants({ variant: 'ghost', size })
    .split(' ')
    .filter((c) => !base.includes(c))
}

describe('WorkComposer layout and disclosure', () => {
  it('is a raised card of the panel radius with a boundary anyone can see and its focus ring on it, in the transcript column', async () => {
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
        'rounded-panel',
        'border',
        'border-ctl-border',
        'bg-surface',
        'hover:border-text-2',
        'focus-within:border-focus',
        'focus-within:-outline-offset-2',
      ]),
    )
    // The column of the conversation and the composer, as the drawing has it.
    expect(box.parentElement!.className).toContain('max-w-[780px]')
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
      'Message calc-review… @ files · / commands',
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
    // One picker for the model and its effort, as the drawing has it.
    expect(chip).toHaveTextContent(/^Stub Model · High$/)
    expect(screen.queryByTestId('composer-effort')).toBeNull()
    expect(chip.tagName).toBe('BUTTON')
    expect(chip).toHaveAccessibleName('Model: Stub Model, effort High')
  })

  // the regression: "claude-opus-5-5" was cut to "clau…" in the composer. It now says
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
    expect(chip.textContent).toBe('Claude Opus 5.5 · High')
    expect(chip.getAttribute('title')).toContain('claude-opus-5-5')
  })

  it('Send is the shared round icon button, 34 px and 44 px on a phone: orange only with text, a quiet ghost otherwise', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const send = await screen.findByTestId('composer-send')
    expect(send).toHaveAccessibleName('Send')
    expect(send.className).toContain('rounded-full')
    expect(send).toHaveClass('size-[var(--density-control)]', 'max-md:size-11')
    // Its size is the shared icon button's, on a phone too: it sets none of its own.
    const sized = (c: string) => /^(max-[^:]+:)?(size|h|w)-/.test(c)
    expect(send.className.split(/\s+/).filter(sized).sort()).toEqual(
      sizeClasses('icon').filter(sized).sort(),
    )
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
  it('focuses an opened session so typing and Enter send without clicking', async () => {
    const user = userEvent.setup()
    const composers = (run = RUNNING) => (
      <>
        <WorkComposer key={run.run_ref} attached={{ run, group: 'active' }} />
        <WorkComposer
          inputId={`${COMPOSER_INPUT_ID}-beside`}
          attached={{
            run: { ...RUNNING, run_ref: 'run_beside' },
            group: 'active',
          }}
        />
      </>
    )
    const view = renderIntel(composers())
    const field = () => document.getElementById(COMPOSER_INPUT_ID)!
    expect(field()).toHaveFocus()
    await user.keyboard('First turn{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        RUNNING.run_ref,
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'First turn' },
        }),
        undefined,
      ),
    )
    const beside = document.getElementById(`${COMPOSER_INPUT_ID}-beside`)!
    await user.type(beside, 'Beside draft')
    view.rerender(composers({ ...RUNNING, pending_approval_ref: 'apr_focus' }))
    expect(beside).toHaveFocus()
    await user.keyboard(' while approval waits')
    expect(beside).toHaveValue('Beside draft while approval waits')
    view.rerender(composers())
    expect(beside).toHaveFocus()
    await user.keyboard(' and clears')
    expect(beside).toHaveValue('Beside draft while approval waits and clears')
    view.rerender(composers({ ...RUNNING, run_ref: 'run_second' }))
    expect(field()).toHaveFocus()
    await user.keyboard('Second turn{Enter}')
    await waitFor(() =>
      expect(api.input).toHaveBeenCalledWith(
        'run_second',
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'Second turn' },
        }),
        undefined,
      ),
    )
  })

  it('keeps focus in the field while a message is sent, so the next keystrokes land', async () => {
    let finish!: (value: { accepted: boolean }) => void
    api.input.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = document.getElementById(COMPOSER_INPUT_ID)!
    expect(field).toHaveFocus()
    await user.keyboard('First turn{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    // The pending message stays in the field, locked but not disabled.
    expect(field).toHaveFocus()
    expect(field).toHaveAttribute('readonly')
    expect(field).toBeEnabled()
    await act(async () => finish({ accepted: true }))
    await waitFor(() => expect(field).toHaveValue(''))
    expect(field).not.toHaveAttribute('readonly')
    await user.keyboard('Next turn')
    expect(field).toHaveValue('Next turn')
  })

  it('keeps focus in the field when an approval card arrives while the person types', async () => {
    const user = userEvent.setup()
    const view = renderIntel(
      <WorkComposer attached={{ run: RUNNING, group: 'active' }} />,
    )
    expect(document.getElementById(COMPOSER_INPUT_ID)).toHaveFocus()
    await user.keyboard('borra')
    view.rerender(
      <WorkComposer
        attached={{
          run: { ...RUNNING, pending_approval_ref: 'apr_typing' },
          group: 'active',
        }}
      />,
    )
    const field = document.getElementById(COMPOSER_INPUT_ID)!
    expect(field).toHaveFocus()
    await user.keyboard('dor')
    expect(field).toHaveValue('borrador')
  })

  it('does not take focus from another control when an approval card arrives', async () => {
    const user = userEvent.setup()
    const view = renderIntel(
      <>
        <button type="button">Elsewhere</button>
        <WorkComposer attached={{ run: RUNNING, group: 'active' }} />
      </>,
    )
    await user.click(screen.getByRole('button', { name: 'Elsewhere' }))
    view.rerender(
      <>
        <button type="button">Elsewhere</button>
        <WorkComposer
          attached={{
            run: { ...RUNNING, pending_approval_ref: 'apr_elsewhere' },
            group: 'active',
          }}
        />
      </>,
    )
    expect(screen.getByRole('button', { name: 'Elsewhere' })).toHaveFocus()
  })

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
      'Ask anything, @ for context, / for commands and skills',
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

//: the message bar's "Advanced" opened a developer's raw stdin line to every user, and
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
      await user.click(screen.getByRole('option', { name: 'High' }))
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
        attached={{
          ...ATTACHED,
          run: { ...ATTACHED.run, workspace_ref: 'ws-main', fast_mode: true },
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-tool-model'))
    expect(await screen.findByLabelText('Fast mode')).toHaveTextContent('On')
    expect(screen.queryByTestId('composer-new-with-choice')).toBeNull()
    await user.click(screen.getByLabelText('Effort'))
    await user.click(screen.getByRole('option', { name: 'High' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      workspaceRef: 'ws-main',
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

  // the regression: a session records the exact id its catalog selector launched on, which
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

  // The catalog names the exact id each selector launches on, so a session recorded under
  // that id is matched to its selector and takes the selector's Fast mode support.
  it('offers Fast On for a session recorded under the exact id its catalog selector launches on', async () => {
    const user = userEvent.setup()
    api.profileModels.mockResolvedValue({
      state: 'fresh',
      models: [
        {
          id: 'default',
          launch_model: 'claude-opus-5-5',
          supports_fast_mode: true,
        },
      ],
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
    expect(screen.getByRole('option', { name: 'On' })).not.toHaveAttribute(
      'aria-disabled',
      'true',
    )
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

  it('holds the longest model and effort of the catalog whole, with no cap of its own', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          ...ATTACHED,
          run: { ...ATTACHED.run, model_ref: 'haiku-5-5' },
        }}
      />,
    )
    const chip = await screen.findByTestId('composer-tool-model')
    await waitFor(() =>
      expect(chip).toHaveTextContent(/^Claude Haiku 5\.5 · Medium$/),
    )
    // As wide as its words need within the bar: the pickers' shared 192 px cap
    // cut «Claude Sonnet 5.5 · Medium» on a desktop.
    expect(chip.className.split(/\s+/)).toContain('max-w-none')
    expect(chip.className.split(/\s+/)).not.toContain('max-w-48')
  })

  it("lists the tool's models and the session's effort levels from the message bar", async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={ATTACHED} />)
    const chip = await screen.findByTestId('composer-tool-model')
    await waitFor(() =>
      expect(chip).toHaveTextContent(/^Claude Opus 5\.5 · Medium$/),
    )
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
        'This session keeps Claude Code · Claude Opus 5.5 · Medium. A different choice starts a new session.',
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
    await user.click(await screen.findByRole('option', { name: 'High' }))
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
    await user.click(await screen.findByRole('option', { name: 'High' }))
    await user.click(screen.getByTestId('composer-new-with-choice'))
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      model: '',
      effort: 'high',
    })
  })
})

// The composer: model, effort and access are three pickers, and the field teaches
// what can be typed.
describe('WorkComposer — three pickers and a field that teaches', () => {
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

  it('keeps first-turn choices anchored and shows known launch access', async () => {
    const user = userEvent.setup()
    const { rerender } = renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, effort: 'high', permission_mode: '' },
          group: 'active',
        }}
      />,
    )
    expect(screen.getByTestId('composer-tool-model')).toHaveTextContent(
      /^Claude Code default · High$/,
    )
    expect(screen.queryByTestId('composer-access')).toBeNull()
    expect(screen.queryByText('Access not reported')).toBeNull()
    await user.click(screen.getByTestId('composer-tool-model'))
    expect(await screen.findByTestId('work-scope-line')).toBeVisible()
    await user.keyboard('{Escape}')

    for (const provider_driver of ['gemini', 'grok', 'opencode', 'cursor']) {
      rerender(
        <WorkComposer
          attached={{ run: { ...RUNNING, provider_driver }, group: 'active' }}
        />,
      )
      expect(screen.getByTestId('composer-access')).toHaveTextContent(
        /^Supervised$/,
      )
      await user.click(screen.getByTestId('composer-access'))
      expect(
        await screen.findByTestId('composer-access-panel'),
      ).toHaveTextContent(
        'Access changes on your next turn in this conversation.',
      )
      await user.keyboard('{Escape}')
    }
    rerender(<WorkComposer attached={attached} />)
    expect(await screen.findByTestId('composer-tool-model')).toHaveTextContent(
      /^Claude Opus 5\.5 · High$/,
    )
    expect(screen.getByTestId('composer-access')).toHaveTextContent(
      /^Supervised$/,
    )
  })

  it('says the model and its effort on one picker, and the access on its own', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={attached} />)
    const model = await screen.findByTestId('composer-tool-model')
    expect(model).toHaveTextContent(/^Claude Opus 5\.5 · High$/)
    expect(model).toHaveAccessibleName('Model: Claude Opus 5.5, effort High')
    expect(screen.queryByTestId('composer-effort')).toBeNull()
    expect(screen.getByTestId('composer-access')).toHaveTextContent(
      /^Supervised$/,
    )
    for (const id of ['composer-tool-model', 'composer-access'])
      expect(screen.getByTestId(id).tagName).toBe('BUTTON')
    // One menu: the models and, inside it, the effort of the chosen model.
    await user.click(model)
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
    expect(screen.getByLabelText('Effort')).toBeEnabled()
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
      'Full',
    ],
    [{ tool_mode: 'auto', permission_mode: 'auto' }, 'Auto'],
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

  it('cuts the access words with an ellipsis, never the model’s, each picker keeping its chevron, in a narrow column', async () => {
    renderIntel(<WorkComposer attached={attached} />)
    for (const id of ['composer-tool-model', 'composer-access']) {
      const picker = await screen.findByTestId(id)
      // Every picker ends in its chevron; the access has its shield in front.
      expect(picker.lastElementChild?.tagName.toLowerCase()).toBe('svg')
    }
    const access = screen.getByTestId('composer-access')
    expect(access.querySelector('span')?.className.split(/\s+/)).toContain(
      'truncate',
    )
    // The model's words show whole or not at all: where the row leaves less room
    // than they need, they wrap below the tool's mark, out of sight, the effort
    // first, and the mark stands alone. Never «…».
    const model = screen.getByTestId('composer-tool-model')
    const name = within(model).getByText('Claude Opus 5.5')
    const line = name.parentElement as HTMLElement
    expect(line.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['h-5', 'flex-wrap', 'overflow-hidden']),
    )
    expect(line.firstElementChild).toBe(within(model).getByTestId('tool-mark'))
    for (const words of [name, within(model).getByText('· High')])
      expect(words.className.split(/\s+/)).toContain('whitespace-nowrap')
    expect(model.querySelector('.truncate')).toBeNull()
    expect(model).toHaveAccessibleName('Model: Claude Opus 5.5, effort High')
  })

  it('applies access to the next turn in the same session', async () => {
    const user = userEvent.setup()
    api.resume.mockResolvedValue({ ...RUNNING, permission_mode: 'plan' })
    renderIntel(<WorkComposer attached={attached} />)
    await user.click(await screen.findByTestId('composer-access'))
    await user.click(
      within(await screen.findByTestId('composer-access-panel')).getByRole(
        'button',
        { name: 'Read only' },
      ),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Session turn' }),
      'Continue here',
    )
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.resume).toHaveBeenCalledWith(RUNNING.run_ref, 'plan'),
    )
    expect(api.input).toHaveBeenCalledWith(
      RUNNING.run_ref,
      expect.stringContaining('Continue here'),
      undefined,
    )
    expect(api.listRuns).not.toHaveBeenCalled()
  })

  //: a new session with different access keeps the current session's folder, not My files.
  it('keeps the current folder when a new session with different access starts', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          ...attached,
          run: { ...attached.run, workspace_ref: 'ws-main' },
        }}
      />,
    )
    await user.click(await screen.findByTestId('composer-access'))
    const panel = await screen.findByTestId('composer-access-panel')
    await user.click(
      within(panel).getByRole('button', {
        name: 'New session with different access…',
      }),
    )
    const { useNewSessionDialog } =
      await import('@/features/first-hour/new-session-store')
    expect(useNewSessionDialog.getState().advanced).toMatchObject({
      workspaceRef: 'ws-main',
    })
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

  // the regression: the engine reads activity only from Claude Code and Codex records. For
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
      'Message calc-review… @ files · / commands',
    )
  })

  // The drawing at 390 px: one row. The access picker is its shield alone, the pickers
  // drop their chevrons, and the model's words go whole, the effort first, where the
  // row has no room for them, its mark staying.
  it('keeps the footer to one row on a phone, the access picker its shield alone with its name', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={attached} />)
    const model = await screen.findByTestId('composer-tool-model')
    expect(model.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['max-[760px]:basis-0', 'max-[760px]:grow']),
    )
    // The safety for a long locale: the row may still wrap, never cut a word.
    expect(
      (model.parentElement as HTMLElement).className.split(/\s+/),
    ).toContain('flex-wrap')
    // The drawing shows the effort's word on a phone too, while there is room; the
    // picker is never narrower than its mark, and its name still says the words.
    const effort = within(model).getByText('· High')
    expect(effort.className).not.toMatch(/max-\[760px\]:hidden/)
    expect(model.className.split(/\s+/)).toContain('max-[760px]:min-w-11')
    expect(model).toHaveAccessibleName('Model: Claude Opus 5.5, effort High')
    const access = screen.getByTestId('composer-access')
    expect(access).toHaveAccessibleName('Access: Supervised')
    const [shield, words] = Array.from(access.children)
    expect(shield.tagName.toLowerCase()).toBe('svg')
    expect(shield.getAttribute('class') ?? '').not.toMatch(/max-\[760px\]:/)
    expect(words).toHaveTextContent('Supervised')
    expect(words.className.split(/\s+/)).toContain('max-[760px]:hidden')
    for (const picker of [model, access])
      expect(picker.lastElementChild?.getAttribute('class')).toContain(
        'max-[760px]:hidden',
      )
    // The effort is chosen in the model's menu.
    await user.click(model)
    expect(await screen.findByLabelText('Effort')).toBeInTheDocument()
  })

  it('lays the bar out as attach, the pickers, then send at the end, the pickers 44 px high on a phone', async () => {
    renderIntel(<WorkComposer attached={attached} />)
    const bar = [
      screen.getByRole('button', { name: 'Attach a file' }),
      await screen.findByTestId('composer-tool-model'),
      screen.getByTestId('composer-access'),
      screen.getByTestId('composer-send'),
    ]
    bar.reduce((before, after) => {
      expect(before.compareDocumentPosition(after)).toBe(
        Node.DOCUMENT_POSITION_FOLLOWING,
      )
      return after
    })
    for (const picker of bar.slice(1, 3))
      expect(picker.className.split(/\s+/)).toEqual(
        expect.arrayContaining(['h-7.5', 'px-2.25', 'max-[760px]:min-h-11']),
      )
    // Attach is a flat picker of the shared small size, as the drawing has it.
    const attach = bar[0].className.split(/\s+/)
    expect(attach).toEqual(expect.arrayContaining(sizeClasses('sm')))
    expect(attach).not.toContain('rounded-full')
  })
})

// The drawing at 390 px: attach, the model, the access, dictate, then More actions
// and Send at the end. Context, setting a draft aside and Plan share the menu.
describe('WorkComposer — one row on a phone', () => {
  beforeEach(() => stubViewportWidth(390))
  afterEach(() => {
    stubViewportWidth(1440)
    mic.canCapture.mockReturnValue(false)
  })
  const field = () => screen.getByRole('textbox', { name: 'Session turn' })

  it('holds Context and setting a draft aside in one menu before Send, each by keyboard', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const more = await screen.findByRole('button', { name: 'More actions' })
    expect(screen.queryByRole('button', { name: 'Context' })).toBeNull()
    expect(screen.queryByTestId('composer-stash')).toBeNull()
    expect(screen.queryByTestId('composer-more-dot')).toBeNull()
    expect(
      more.compareDocumentPosition(screen.getByTestId('composer-send')),
    ).toBe(Node.DOCUMENT_POSITION_FOLLOWING)

    // An empty field with nothing set aside has nothing to set aside or bring back.
    more.focus()
    await user.keyboard('{Enter}')
    expect(
      await screen.findByRole('menuitem', { name: /^Set this draft aside/ }),
    ).toHaveAttribute('aria-disabled', 'true')
    await user.keyboard('{Escape}')

    // Set aside, from the menu by keyboard, its chord beside its name.
    await user.type(field(), 'half a thought')
    more.focus()
    await user.keyboard('{Enter}')
    const menu = await screen.findByRole('menu')
    const setAside = within(menu).getByRole('menuitem', {
      name: /^Set this draft aside/,
    })
    expect(setAside).toHaveTextContent('Ctrl+S')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(field()).toHaveValue('')
    // Something set aside: the button says so, and a dot shows it.
    expect(
      await screen.findByRole('button', {
        name: 'More actions, with a draft set aside',
      }),
    ).toBe(more)
    expect(within(more).getByTestId('composer-more-dot')).toBeInTheDocument()
    // In the accent's ink, which holds 3:1 on the card; the plain accent does not.
    expect(
      within(more).getByTestId('composer-more-dot').className.split(/\s+/),
    ).toContain('bg-accent-text')

    // Brought back the same way.
    more.focus()
    await user.keyboard('{Enter}')
    expect(
      await screen.findByRole('menuitem', {
        name: /^Bring back the draft set aside/,
      }),
    ).toHaveTextContent('Ctrl+S')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(field()).toHaveValue('half a thought')

    // Context opens its panel from the menu; Escape comes back to the button.
    more.focus()
    await user.keyboard('{Enter}')
    expect(
      await screen.findByRole('menuitem', { name: 'Context and compacting…' }),
    ).toBeVisible()
    await user.keyboard('{Enter}')
    const panel = await screen.findByRole('dialog', { name: 'Context' })
    await waitFor(() =>
      expect(panel).toContainElement(document.activeElement as HTMLElement),
    )
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Context' })).toBeNull(),
    )
    await waitFor(() => expect(more).toHaveFocus())
  })

  it('shows how full the context is beside Context in the menu', async () => {
    api.activity.mockResolvedValue({
      context_tokens: 20000,
      context_window: 200000,
      context_percent: 10,
    })
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.click(
      await screen.findByRole('button', { name: 'More actions' }),
    )
    const context = await screen.findByRole('menuitem', {
      name: /^Context and compacting…/,
    })
    expect(await within(context).findByText('10%')).toBeVisible()
  })

  it('lays the row out as the drawing: attach, the model, the access, dictate, then More actions and Send', async () => {
    // A browser that can record, as the drawing's phone is: Dictate is in the row.
    mic.canCapture.mockReturnValue(true)
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const row = [
      screen.getByRole('button', { name: 'Attach a file' }),
      await screen.findByTestId('composer-tool-model'),
      screen.getByTestId('composer-access'),
      screen.getByTestId('composer-dictate'),
      screen.getByRole('button', { name: 'More actions' }),
      screen.getByTestId('composer-send'),
    ]
    row.reduce((before, after) => {
      expect(before.compareDocumentPosition(after)).toBe(
        Node.DOCUMENT_POSITION_FOLLOWING,
      )
      return after
    })
    // Plan off changes nothing Send does: it waits in the menu.
    expect(screen.queryByRole('button', { name: 'Plan' })).toBeNull()
  })

  it('holds Plan in More actions while it is off, and shows it pressed in the row while it is on', async () => {
    api.resume.mockResolvedValue({ ...RUNNING, permission_mode: 'plan' })
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const more = await screen.findByRole('button', { name: 'More actions' })
    await user.click(more)
    const off = await screen.findByRole('menuitemcheckbox', { name: 'Plan' })
    expect(off).toHaveAttribute('aria-checked', 'false')
    await user.click(off)

    // On, it changes what Send does, so the row shows it, pressed, before the menu.
    const plan = await screen.findByRole('button', { name: 'Plan' })
    expect(plan).toHaveAttribute('aria-pressed', 'true')
    expect(plan.compareDocumentPosition(more)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
    await user.click(more)
    expect(
      await screen.findByRole('menuitemcheckbox', { name: 'Plan' }),
    ).toHaveAttribute('aria-checked', 'true')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())

    // A message sent now goes under the plan mode.
    await user.type(field(), 'add a flag{Enter}')
    await waitFor(() =>
      expect(api.resume).toHaveBeenCalledWith('run_live', 'plan'),
    )
  })

  it('turns Plan off from the row and gives focus to More actions, where Plan waits', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const more = await screen.findByRole('button', { name: 'More actions' })
    await user.click(more)
    await user.click(
      await screen.findByRole('menuitemcheckbox', { name: 'Plan' }),
    )
    const plan = await screen.findByRole('button', { name: 'Plan' })
    plan.focus()
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('button', { name: 'Plan' })).toBeNull()
    expect(more).toHaveFocus()
    await user.click(more)
    expect(
      await screen.findByRole('menuitemcheckbox', { name: 'Plan' }),
    ).toHaveAttribute('aria-checked', 'false')
  })
})

describe('typed approval feedback', () => {
  beforeEach(() => {
    useSentTurns.setState({ byRun: {}, seen: {}, said: {} })
    approvals.getApproval.mockResolvedValue({
      id: 'apr-feedback',
      status: 'pending',
      subject_kind: 'claude.tool',
      requested_by: 'session:osn-a',
    })
    approvals.listDecisions.mockResolvedValue({ items: [] })
    approvals.decide.mockResolvedValue({ status: 'rejected' })
  })
  it('declines the exact pending tool call with the typed reason, without sending another turn', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, pending_approval_ref: 'apr-feedback' },
          group: 'active',
        }}
      />,
    )
    const send = await screen.findByRole('button', {
      name: 'Decline and send this',
    })
    await user.type(
      screen.getByTestId('launcher-input'),
      'use pnpm instead{Enter}',
    )
    await waitFor(() =>
      expect(approvals.decide).toHaveBeenCalledWith('apr-feedback', {
        decision: 'reject',
        note: 'use pnpm instead',
      }),
    )
    expect(api.input).not.toHaveBeenCalled()
    expect(api.inputText).not.toHaveBeenCalled()
    expect(sentTurnsOf(RUNNING.run_ref)).toBeUndefined()
    expect(send).toBeDefined()
  })
  it('keeps the draft when the decision fails', async () => {
    approvals.decide.mockRejectedValue(new Error('unavailable'))
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, pending_approval_ref: 'apr-feedback' },
          group: 'active',
        }}
      />,
    )
    await screen.findByRole('button', { name: 'Decline and send this' })
    await user.type(
      screen.getByTestId('launcher-input'),
      'use pnpm instead{Enter}',
    )
    await waitFor(() => expect(approvals.decide).toHaveBeenCalled())
    expect(screen.getByTestId('launcher-input')).toHaveValue('use pnpm instead')
  })
  it('offers no typed decline to someone who cannot decide', async () => {
    auth.can = (p) => p !== 'governance:approval:admin'
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, pending_approval_ref: 'apr-feedback' },
          group: 'active',
        }}
      />,
    )
    await waitFor(() => expect(approvals.getApproval).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: 'Decline and send this' }),
    ).not.toBeInTheDocument()
    expect(screen.getByTestId('composer-send')).toBeDisabled()
    expect(screen.getByTestId('launcher-input')).toBeEnabled()
  })
})

// The composer spec (rows 2 to 5 and 7 of the Composer area): recall, a Stop in the
// composer, a stashed draft, plan first, and the person's choice of send key.
describe('WorkComposer: the composer spec', () => {
  const composer = (run = RUNNING) => (
    <WorkComposer attached={{ run, group: 'active' }} />
  )
  const field = () => screen.getByRole('textbox', { name: 'Session turn' })

  beforeEach(() => {
    useClientSettings.setState({ ...CLIENT_SETTING_DEFAULTS })
  })

  it('recalls the last sent prompt with ArrowUp in the empty composer, and steps back to empty with ArrowDown', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'Look up the readme')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(field()).toHaveValue(''))
    await user.click(field())
    await user.keyboard('{ArrowUp}')
    expect(field()).toHaveValue('Look up the readme')
    await user.keyboard('{ArrowDown}')
    expect(field()).toHaveValue('')
  })

  it('offers Stop in the composer while a turn runs, and not when the session is idle', async () => {
    api.interrupt.mockResolvedValue({})
    const user = userEvent.setup()
    const view = renderIntel(composer())
    await user.click(screen.getByRole('button', { name: 'Interrupt turn' }))
    await waitFor(() =>
      expect(api.interrupt).toHaveBeenCalledWith('run_live', undefined),
    )
    view.rerender(composer({ ...RUNNING, state: 'idle' }))
    expect(
      screen.queryByRole('button', { name: 'Interrupt turn' }),
    ).not.toBeInTheDocument()
  })

  it('stashes the draft with Ctrl+S and restores it with Ctrl+S in the empty composer', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'half written')
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('')
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('half written')
  })

  it('keeps the images with a stashed draft and brings them back with it', async () => {
    api.uploadAttachment.mockResolvedValue({ id: 'att_1' })
    api.inputWithAttachments.mockResolvedValue({ accepted: true })
    const user = userEvent.setup()
    renderIntel(composer())
    const png = new File([new Uint8Array([0x89, 0x50])], 'shot.png', {
      type: 'image/png',
    })
    fireEvent.paste(field(), {
      clipboardData: { files: [png], types: ['Files'] },
    })
    await screen.findByTestId('attachment-tray')
    await user.type(field(), 'see this')
    await user.keyboard('{Control>}s{/Control}')
    await waitFor(() =>
      expect(screen.queryByTestId('attachment-tray')).not.toBeInTheDocument(),
    )
    expect(field()).toHaveValue('')
    await user.keyboard('{Control>}s{/Control}')
    expect(await screen.findByTestId('attachment-tray')).toBeVisible()
    expect(field()).toHaveValue('see this')
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledWith(
        'run_live',
        'see this',
        ['att_1'],
      ),
    )
  })

  // A draft set aside with an image, then the page reloaded and its words and image
  // asked back.
  const pastePng = () =>
    fireEvent.paste(field(), {
      clipboardData: {
        files: [
          new File([new Uint8Array([0x89, 0x50])], 'shot.png', {
            type: 'image/png',
          }),
        ],
        types: ['Files'],
      },
    })
  const storedPng = () =>
    new Blob([new Uint8Array([0x89, 0x50])], { type: 'image/png' })

  it('brings back a draft set aside, words and image, after a reload', async () => {
    api.uploadAttachment.mockResolvedValue({ id: 'att_1' })
    api.attachmentImage.mockResolvedValue(storedPng())
    api.inputWithAttachments.mockResolvedValue({ accepted: true })
    const user = userEvent.setup()
    const view = renderIntel(composer())
    pastePng()
    await screen.findByRole('img', { name: 'shot.png' })
    await user.type(field(), 'see this')
    await user.click(
      screen.getByRole('button', { name: 'Set this draft aside' }),
    )
    expect(field()).toHaveValue('')
    view.unmount()
    // A reload: the page's memory starts again, the tab keeps its storage.
    vi.resetModules()
    const { WorkComposer: Reloaded } = await import('./work-composer')
    renderIntel(<Reloaded attached={{ run: RUNNING, group: 'active' }} />)
    await user.click(
      await screen.findByRole('button', {
        name: 'Bring back the draft set aside',
      }),
    )
    expect(field()).toHaveValue('see this')
    expect(await screen.findByRole('img', { name: 'shot.png' })).toBeVisible()
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() =>
      expect(api.inputWithAttachments).toHaveBeenCalledWith(
        'run_live',
        'see this',
        ['att_1'],
      ),
    )
  })

  it('leaves the file stored with the session when its draft is set aside, and brings it back from there', async () => {
    api.uploadAttachment.mockResolvedValue({ id: 'att_1' })
    api.attachmentImage.mockResolvedValue(storedPng())
    const user = userEvent.setup()
    renderIntel(composer())
    pastePng()
    await screen.findByRole('img', { name: 'shot.png' })
    await user.type(field(), 'see this')
    await user.keyboard('{Control>}s{/Control}')
    await waitFor(() =>
      expect(screen.queryByTestId('attachment-tray')).not.toBeInTheDocument(),
    )
    await user.keyboard('{Control>}s{/Control}')
    // The engine's own copy, by the id it was stored under: nothing set aside removed
    // it, and nothing stores it a second time.
    await waitFor(() =>
      expect(api.attachmentImage).toHaveBeenCalledWith('run_live', 'att_1'),
    )
    expect(await screen.findByRole('img', { name: 'shot.png' })).toBeVisible()
    expect(api.uploadAttachment).toHaveBeenCalledOnce()
  })

  it('says beside the words brought back that a file set aside is no longer stored with the session', async () => {
    api.uploadAttachment.mockResolvedValue({ id: 'att_1' })
    api.attachmentImage.mockRejectedValue(
      new ApiError(404, 'not_found', 'attachment not found'),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    pastePng()
    await screen.findByRole('img', { name: 'shot.png' })
    await user.type(field(), 'see this')
    await user.keyboard('{Control>}s{/Control}')
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('see this')
    expect(
      await screen.findByText(
        'No longer stored with this session, so not brought back: shot.png',
      ),
    ).toBeVisible()
    expect(screen.queryByTestId('attachment-tray')).not.toBeInTheDocument()
  })

  // What happens to a draft set aside is said in one sentence, in a status region that
  // is there from the start.
  const said = () => screen.getByTestId('stash-status')
  const aliceKey = () => sessionDraftKey('tnt-demo', auth.principal, 'run_live')

  // A draft that cannot be set aside is refused aloud, with its true cause.
  const refused = (sentence: string) =>
    expect(screen.getByText(sentence)).toHaveAttribute('role', 'alert')

  it('says so when a draft cannot be set aside, keeps it in the field, and still saves it', async () => {
    fillSharedStorage()
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'keep me')
    await user.click(
      screen.getByRole('button', { name: 'Set this draft aside' }),
    )
    expect(field()).toHaveValue('keep me')
    refused(
      'Could not set this draft aside: this browser has no room left to keep it. Nothing was dropped, and it stays here.',
    )
    vi.mocked(Storage.prototype.setItem).mockRestore()
    await user.type(field(), ' and more')
    expect(readSessionDraft(aliceKey())).toBe('keep me and more')
  })

  it('says a draft is too large to set aside, and names the limit', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    const large = 'z'.repeat(STASH_MAX_BYTES)
    fireEvent.change(field(), { target: { value: large } })
    await user.click(
      screen.getByRole('button', { name: 'Set this draft aside' }),
    )
    expect(field()).toHaveValue(large)
    refused(
      'This draft is too large to set aside: the limit is 512 KiB. It stays here.',
    )
  })

  it('says a browser that keeps nothing between visits cannot set a draft aside', async () => {
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
      throw new DOMException('Storage disabled', 'SecurityError')
    })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'keep me')
    await user.click(
      screen.getByRole('button', { name: 'Set this draft aside' }),
    )
    expect(field()).toHaveValue('keep me')
    refused(
      'Could not set this draft aside: this browser keeps nothing between visits. It stays only in this tab.',
    )
  })

  it('says which draft set aside was dropped to make room', async () => {
    for (let n = 1; n <= 20; n++)
      stashDraft(aliceKey(), { text: `Draft number ${n}`, attachments: [] })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'The newest one')
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('')
    expect(said()).toHaveTextContent(
      'Set aside. Dropped to make room, oldest first: Draft number 1',
    )
  })

  it('says so when another tab already brought the draft set aside back', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'only once')
    await user.keyboard('{Control>}s{/Control}')
    // The other tab took it; its storage event has not reached this one yet.
    for (const name of Object.keys(localStorage))
      if (name.startsWith('olivares.stash.')) localStorage.removeItem(name)
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('')
    expect(said()).toHaveTextContent(
      'The draft set aside is no longer here: another tab brought it back, or a day went by.',
    )
  })

  it('counts the drafts set aside when they change, not at every key', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'first words')
    await user.keyboard('{Control>}s{/Control}')
    // Counted again when one is set aside: the field offers it back.
    expect(
      await screen.findByRole('button', {
        name: 'Bring back the draft set aside',
      }),
    ).toBeVisible()
    const read = vi.spyOn(Storage.prototype, 'getItem')
    await user.type(field(), 'a few more words')
    const asideReads = read.mock.calls.filter(([name]) =>
      name.startsWith('olivares.stash.'),
    )
    expect(asideReads).toEqual([])
  })

  it('says nothing when the chord finds an empty field and nothing set aside', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    const region = await screen.findByTestId('stash-status')
    await user.click(field())
    await user.keyboard('{Control>}s{/Control}')
    expect(field()).toHaveValue('')
    expect(region).toBeEmptyDOMElement()
  })

  it('offers a draft another tab set aside, without a reload', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await screen.findByTestId('stash-status')
    expect(
      screen.queryByRole('button', { name: 'Bring back the draft set aside' }),
    ).not.toBeInTheDocument()
    // Another tab: its own page memory, the browser's shared storage.
    vi.resetModules()
    const otherTab = await import('@/lib/session-drafts')
    otherTab.stashDraft(aliceKey(), {
      text: 'From the other tab',
      attachments: [],
    })
    const stored = Object.keys(localStorage).find((name) =>
      name.startsWith('olivares.stash.'),
    )
    act(() => {
      window.dispatchEvent(new StorageEvent('storage', { key: stored }))
    })
    await user.click(
      await screen.findByRole('button', {
        name: 'Bring back the draft set aside',
      }),
    )
    expect(field()).toHaveValue('From the other tab')
  })

  it('says which files were not brought back once they are read, even if the person typed meanwhile', async () => {
    api.uploadAttachment.mockResolvedValue({ id: 'att_1' })
    let refuse: (error: unknown) => void = () => {}
    const reading = new Promise<Blob>((_, reject) => {
      refuse = reject
    })
    api.attachmentImage.mockReturnValue(reading)
    const user = userEvent.setup()
    renderIntel(composer())
    const region = await screen.findByTestId('stash-status')
    expect(region).toHaveAttribute('role', 'status')
    expect(region).toBeEmptyDOMElement()
    pastePng()
    await screen.findByRole('img', { name: 'shot.png' })
    await user.type(field(), 'see this')
    await user.keyboard('{Control>}s{/Control}')
    await user.keyboard('{Control>}s{/Control}')
    await user.type(field(), ' and that')
    await act(async () => {
      refuse(new ApiError(404, 'not_found', 'attachment not found'))
    })
    // The same region, there all along, now says it.
    expect(said()).toBe(region)
    expect(region).toHaveTextContent(
      'No longer stored with this session, so not brought back: shot.png',
    )
  })

  it('plans first: Plan hands the next message to the tool under its plan mode, and Plan again goes back', async () => {
    api.resume.mockResolvedValue({ ...RUNNING, permission_mode: 'plan' })
    const user = userEvent.setup()
    renderIntel(composer())
    const plan = screen.getByRole('button', { name: 'Plan' })
    expect(plan).toHaveAttribute('aria-pressed', 'false')
    await user.click(plan)
    expect(plan).toHaveAttribute('aria-pressed', 'true')
    await user.type(field(), 'add a flag{Enter}')
    await waitFor(() =>
      expect(api.resume).toHaveBeenCalledWith('run_live', 'plan'),
    )
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
  })

  it('sends with Ctrl+Enter only when the person chose it, and Enter then breaks a line', async () => {
    useClientSettings.setState({ sendKey: 'modEnter' })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'first line{Enter}second')
    expect(field()).toHaveValue('first line\nsecond')
    expect(api.input).not.toHaveBeenCalled()
    await user.keyboard('{Control>}{Enter}{/Control}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
  })

  it('names the keys the person chose in the field while the tool works', async () => {
    api.queue.mockResolvedValue({ items: [], hold: 'turn', can_steer: true })
    useClientSettings.setState({ sendKey: 'modEnter' })
    renderIntel(composer())
    await waitFor(() =>
      expect(field()).toHaveAttribute(
        'placeholder',
        'Ctrl+Enter queues a message for after this turn',
      ),
    )
  })

  it('steers on Enter, and queues on Ctrl+Enter, when the person chose to steer', async () => {
    api.queue.mockResolvedValue({
      items: [],
      hold: 'turn',
      can_steer: true,
    })
    api.queueAdd.mockResolvedValue({ id: 'q1', text: 'later' })
    useClientSettings.setState({ sendWhileRunning: 'steer' })
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'now{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(api.queueAdd).not.toHaveBeenCalled()
    await user.type(field(), 'later{Control>}{Enter}{/Control}')
    await waitFor(() =>
      expect(api.queueAdd).toHaveBeenCalledWith('run_live', 'later'),
    )
  })
})
