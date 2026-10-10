// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  isSuperadmin: false,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const listOrgs = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...real,
    systemApi: { ...real.systemApi, listOrgs },
  }
})

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  getRun: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { ScopeLine, WorkComposer } from './work-composer'
import type { RunDTO } from '@/features/agentops/types'

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

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 'tnt-demo'
  auth.isSuperadmin = false
  auth.can = () => true
  listOrgs.mockResolvedValue({ items: [], has_more: false })
  api.listProfiles.mockResolvedValue({ items: [PROFILE], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [WORKSPACE], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
})

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

describe('WorkComposer permissions', () => {
  it('opens the pending approval from Waiting for you', async () => {
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
    const link = await screen.findByRole('link', { name: 'Waiting for you' })
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
    const link = await screen.findByRole('link', { name: 'Waiting for you' })
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
    expect(screen.queryByText('Waiting for you')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Waiting for you' })).toBeNull()
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
    expect(screen.queryByText('Waiting for you')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Waiting for you' })).toBeNull()
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
      expect(screen.queryByText('Waiting for you')).toBeNull()
      expect(screen.queryByRole('link', { name: 'Waiting for you' })).toBeNull()
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
    auth.isSuperadmin = true
    listOrgs.mockResolvedValue({
      items: [
        {
          id: 'org-1',
          tenant_id: tenant,
          name: 'Golden Path',
          slug: 'golden-path',
          status: 'active',
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
      has_more: false,
    })
    renderIntel(<ScopeLine workspace={null} environment={null} />)
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('Golden Path'))
    expect(line.textContent).not.toMatch(/01a0b95a/)
  })

  it('falls back to a short identifier when the organization name is not known', async () => {
    const tenant = '01a0b95a-ee26-724d-9db8-1f5423e25db3'
    auth.activeTenant = tenant
    auth.isSuperadmin = true
    listOrgs.mockResolvedValue({ items: [], has_more: false })
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
    const line = screen.getByTestId('work-scope-line')
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
    expect(screen.queryByText('Running')).toBeNull()
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
    expect(field).toHaveAttribute('placeholder', 'Message calc-review…')
    expect(field.className).toContain('max-h-48')
    expect(field.className).toContain('resize-none')
  })

  it('names the tool and model of the session as a read-only tag', async () => {
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            provider_driver: 'codex',
            model_ref: 'stub-model',
          },
          group: 'active',
        }}
      />,
    )
    const tag = await screen.findByTestId('composer-tool-model')
    expect(tag).toHaveTextContent('Codex · stub-model')
    expect(tag.tagName).toBe('SPAN')
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
      'Send a sentence…',
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
        attached={{ run: { ...RUNNING, state: 'stopped' }, group: 'settled' }}
      />,
    )
    const send = await screen.findByTestId('composer-send')
    expect(send).toBeDisabled()
    expect(send).toHaveAccessibleDescription(
      'Start or resume the session to send.',
    )
    const field = screen.getByTestId('launcher-input')
    expect(field).toBeDisabled()
    expect(field).toHaveAttribute(
      'placeholder',
      'Start or resume the session to send.',
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
        attached={{ run: { ...RUNNING, state: 'stopped' }, group: 'settled' }}
      />,
    )
    const wireSend = await screen.findByTestId('composer-wire-send')
    expect(wireSend).toBeDisabled()
    expect(wireSend).toHaveAccessibleDescription(
      'Start or resume the session to send.',
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
