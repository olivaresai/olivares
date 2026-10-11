// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Work view said people record work items and gave a person no
// way to record one. A handoff starts from an item, so the console could not start one.
// These tests drive the person's path: New item, the form, the engine's plan, apply,
// and the new item open.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor, within } from '@/test/intel'
import type { Plan } from './types'
import '@/features/_intel'
import './i18n'

const auth = vi.hoisted(() => ({
  denied: new Set<string>(),
  kind: 'user' as 'user' | 'token',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => !auth.denied.has(permission),
    confinedWorkspace: null,
    principal: {
      kind: auth.kind,
      user_id:
        auth.kind === 'user' ? '01a111d4-08b1-4c3e-9a51-6f1d2c3b4a59' : '',
      actor: 'user:01a111d4-08b1-4c3e-9a51-6f1d2c3b4a59',
      display_name: 'Owner',
      superadmin: false,
      grants: [],
    },
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouterState: () => '',
  useRouter: () => undefined,
  useNavigate: () => vi.fn(),
}))
vi.mock('./stream', () => ({
  useWorkStream: () => ({ status: 'connected' as const }),
}))
vi.mock('@/lib/hooks/use-url-state', async () => {
  const react = await import('react')
  return {
    useUrlState: () => {
      const [state, setState] = react.useState<
        Record<string, string | undefined>
      >({})
      const patch = react.useCallback(
        (p: Record<string, string | undefined>) =>
          setState((prev) => {
            const next = { ...prev }
            for (const [k, v] of Object.entries(p)) {
              if (v === undefined || v === '') delete next[k]
              else next[k] = v
            }
            return next
          }),
        [],
      )
      return [state, patch]
    },
  }
})

const consola = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listWorkspaces: vi.fn(),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...consola } }
})

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
  planWork: vi.fn(),
  applyWork: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

const { WorkView } = await import('./work-view')
const { useWorkspaceStore } = await import('@/stores/workspace')

const OWNER = '01a111d4-08b1-4c3e-9a51-6f1d2c3b4a59'
const WORKSPACE = '0192f2c0-bbbb-7000-8000-0000000000b1'
const TEAM = '0192f2c0-bbbb-7000-8000-0000000000b2'
const CREATED = '0192f2c0-aaaa-7000-8000-00000000aa01'

const plan: Plan = {
  verdict: 'LIMPIO',
  code: 'planned',
  observed_at: '2026-10-06T22:00:00Z',
  checks: [],
  plan_hash: 'plan-hash-1',
  command: 'item.create',
  row_effects: ['sessions.work_item:insert'],
  event_type: 'work.item.created',
  audit_action: 'sessions.work.item.create',
  permission: 'sessions:work:write',
  external_calls: [],
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.denied = new Set()
  auth.kind = 'user'
  useWorkspaceStore.getState().clear()
  consola.listMembers.mockResolvedValue({ items: [], has_more: false })
  consola.listWorkspaces.mockResolvedValue({
    items: [
      {
        id: WORKSPACE,
        tenant_id: 't1',
        name: 'Default',
        slug: 'default',
        status: 'active',
        is_default: true,
        created_at: '2026-10-06T00:00:00Z',
        updated_at: '2026-10-06T00:00:00Z',
        version: 1,
      },
    ],
    has_more: false,
  })
  api.listWorkItems.mockResolvedValue({ items: [], has_more: false })
  api.getWorkItem.mockReturnValue(new Promise(() => {}))
  api.planWork.mockResolvedValue(plan)
  api.applyWork.mockResolvedValue({
    result: {
      verdict: 'LIMPIO',
      code: 'applied',
      command_id: 'cmd-1',
      result_kind: 'sessions.work_item',
      result_id: CREATED,
      status: 'draft',
      event_id: 'event-1',
      event_seq: 1,
      owner_epoch: 1,
      plan_hash: 'plan-hash-1',
      audit_seq: 7,
    },
    etag: '"v1"',
    replayed: false,
  })
})

const workspace = (id: string, name: string, isDefault: boolean) => ({
  id,
  tenant_id: 't1',
  name,
  slug: name.toLowerCase(),
  status: 'active',
  is_default: isDefault,
  created_at: '2026-10-06T00:00:00Z',
  updated_at: '2026-10-06T00:00:00Z',
  version: 1,
})

async function openForm(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText('No work items yet')
  await user.click(
    within(emptyState()).getByRole('button', { name: 'New item' }),
  )
  return screen.findByRole('dialog', { name: 'New work item' })
}

async function fill(
  user: ReturnType<typeof userEvent.setup>,
  form: HTMLElement,
) {
  await user.type(within(form).getByLabelText(/^Title/), 'Review the notes')
  await user.type(within(form).getByLabelText(/^Brief/), 'Check each entry.')
  await user.type(within(form).getByLabelText(/^Done when/), 'All checked.')
}

const emptyState = () =>
  screen
    .getByText('No work items yet')
    .closest('[data-slot="empty-state"]') as HTMLElement

describe('A person records a work item from the Work view (#505)', () => {
  it('offers New item where the empty state says people record items', async () => {
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    expect(
      within(emptyState()).getByRole('button', { name: 'New item' }),
    ).toBeInTheDocument()
    // Starting a session stays offered beside it, as the quieter door.
    expect(
      within(emptyState())
        .getByRole('link', { name: 'Start a session' })
        .getAttribute('href'),
    ).toBe('/sessions')
  })

  it('without sessions:work:write it offers no New item', async () => {
    auth.denied = new Set(['sessions:work:write'])
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    expect(screen.queryByRole('button', { name: 'New item' })).toBeNull()
  })

  it('a token principal, which cannot own an item, gets no New item', async () => {
    auth.kind = 'token'
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    expect(screen.queryByRole('button', { name: 'New item' })).toBeNull()
    expect(
      within(emptyState()).getByRole('link', { name: 'Start a session' }),
    ).toBeInTheDocument()
  })

  it('plans the item.create the engine accepts, applies it under the same key and opens the item', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    await user.click(
      within(emptyState()).getByRole('button', { name: 'New item' }),
    )

    const form = await screen.findByRole('dialog', { name: 'New work item' })
    await user.type(
      within(form).getByLabelText(/^Title/),
      'Review the release notes',
    )
    await user.type(
      within(form).getByLabelText(/^Brief/),
      'Check every entry against the merged PRs.',
    )
    await user.type(
      within(form).getByLabelText(/^Done when/),
      'Every entry names its PR.',
    )
    // The single workspace is chosen for the person; nothing else is asked.
    await waitFor(() =>
      expect(
        within(form).getByRole('button', { name: 'Continue' }),
      ).toBeEnabled(),
    )
    await user.click(within(form).getByRole('button', { name: 'Continue' }))

    await waitFor(() => expect(api.planWork).toHaveBeenCalledTimes(1))
    const planned = api.planWork.mock.calls[0][0]
    expect(planned.command).toBe('item.create')
    expect(planned.method).toBe('POST')
    expect(planned.path).toBe('/v1/m/sessions/work-items')
    expect(planned.tenant).toBe('t1')
    expect(planned.key).toMatch(/^[0-9a-f-]{36}$/)
    expect(planned.body).toEqual({
      workspace_id: WORKSPACE,
      title: 'Review the release notes',
      work_kind: 'task',
      brief_md: 'Check every entry against the merged PRs.',
      priority: 'p2',
      owner_kind: 'user',
      owner_ref: OWNER,
      provenance_kind: 'human',
      provenance_ref: 'console',
      acceptance: [
        {
          criterion_key: 'done',
          statement: 'Every entry names its PR.',
          required: true,
        },
      ],
    })

    const apply = await screen.findByRole('dialog', { name: 'New work item' })
    await user.click(within(apply).getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(api.applyWork).toHaveBeenCalledTimes(1))
    expect(api.applyWork.mock.calls[0][0].key).toBe(planned.key)

    await within(apply).findByText('Applied')
    // The footer's Close and the dialog's own close control both end the dialog.
    await user.click(within(apply).getAllByRole('button', { name: 'Close' })[0])
    await waitFor(() =>
      expect(api.getWorkItem).toHaveBeenCalledWith(
        CREATED,
        expect.anything(),
        expect.anything(),
      ),
    )
  })

  it('cannot continue until the engine-required text is filled', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    await user.click(
      within(emptyState()).getByRole('button', { name: 'New item' }),
    )
    const form = await screen.findByRole('dialog', { name: 'New work item' })
    await user.type(within(form).getByLabelText(/^Title/), 'Only a title')
    expect(
      within(form).getByRole('button', { name: 'Continue' }),
    ).toBeDisabled()
    expect(api.planWork).not.toHaveBeenCalled()
  })

  it('records the item in the topbar workspace over the tenant default', async () => {
    consola.listWorkspaces.mockResolvedValue({
      items: [
        workspace(WORKSPACE, 'Default', true),
        workspace(TEAM, 'Team', false),
      ],
      has_more: false,
    })
    useWorkspaceStore.getState().setActiveWorkspace(TEAM, 'Team')
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    const form = await openForm(user)
    await fill(user, form)
    await user.click(within(form).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(api.planWork).toHaveBeenCalledTimes(1))
    expect(api.planWork.mock.calls[0][0].body.workspace_id).toBe(TEAM)
  })

  it('says the workspace list could not be read, never that there is none', async () => {
    consola.listWorkspaces.mockRejectedValue(new Error('boom'))
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    const form = await openForm(user)
    expect(
      await within(form).findByText(/workspace list could not be read/),
    ).toBeInTheDocument()
    expect(within(form).queryByText(/No workspace is available/)).toBeNull()
    await fill(user, form)
    expect(
      within(form).getByRole('button', { name: 'Continue' }),
    ).toBeDisabled()
  })

  it('keeps the typed draft when the engine refuses the plan', async () => {
    api.planWork.mockRejectedValue(new Error('refused'))
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    const form = await openForm(user)
    await fill(user, form)
    await waitFor(() =>
      expect(
        within(form).getByRole('button', { name: 'Continue' }),
      ).toBeEnabled(),
    )
    await user.click(within(form).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(api.planWork).toHaveBeenCalledTimes(1))
    const flow = await screen.findByRole('dialog', { name: 'New work item' })
    await user.click(within(flow).getAllByRole('button', { name: 'Cancel' })[0])
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(api.applyWork).not.toHaveBeenCalled()

    await user.click(
      within(emptyState()).getByRole('button', { name: 'New item' }),
    )
    const again = await screen.findByRole('dialog', { name: 'New work item' })
    expect(within(again).getByLabelText(/^Title/)).toHaveValue(
      'Review the notes',
    )
  })
})
