// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// #2548: the Rules place lists each request with the rule that asked and what it reaches,
// the request that needs this person carries the one orange edge whatever row is
// selected, and approving it is one action that records one decision.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { agentOpsApi } from '@/features/agentops/api'
import type { ApprovalDTO } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
const auth = vi.hoisted(() => ({
  activeTenant: 't1',
  can: (_permission: string): boolean => true,
  principal: { actor: 'user:reviewer', kind: 'user' },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: { ...actual.PANEL_EXTENSIONS, governanceTabs: [] },
  }
})
const api = vi.hoisted(() => ({
  listApprovals: vi.fn(),
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  decide: vi.fn(),
  cancelApproval: vi.fn(),
  sweepApprovals: vi.fn(),
  listPolicies: vi.fn(),
  getPolicy: vi.fn(),
  listIdentities: vi.fn(),
  listGroups: vi.fn(),
  listBindings: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import GovernanceView from './governance-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const migration: ApprovalDTO = {
  id: 'apr-migrate',
  action: 'claude.tool.use',
  subject_kind: 'claude.tool',
  subject_ref: 'Bash',
  session_ref: 'osn_migrate',
  requested_by: 'session:osn_migrate',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  policy_ref: 'pol-migrations',
  escalated: false,
  review: { tool: 'Bash', text: 'pnpm db:migrate --env payments-staging' },
}

const registry: ApprovalDTO = {
  id: 'apr-registry',
  action: 'network.connect',
  subject_kind: 'network_destination',
  subject_ref: 'registry.terraform.io',
  requested_by: 'user:sam',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  reason: 'Terraform needs the provider registry',
  escalated: false,
}

// The reviewer's own request: it waits, but not for them (separation of duties).
const own: ApprovalDTO = {
  ...registry,
  id: 'apr-own',
  subject_ref: 'github.com',
  requested_by: 'user:reviewer',
  reason: 'Open a pull request',
}

const rowOf = (text: string) =>
  screen
    .getAllByRole('button')
    .find(
      (b) =>
        b.hasAttribute('data-approval-row') && b.textContent?.includes(text),
    )!

beforeEach(() => {
  auth.can = () => true
  auth.principal = { actor: 'user:reviewer', kind: 'user' }
  vi.restoreAllMocks()
  for (const fn of Object.values(api)) fn.mockReset()
  for (const t of Object.values(toast)) t.mockReset()
  for (const fn of ['listIdentities', 'listGroups', 'listBindings'] as const)
    api[fn].mockResolvedValue({ items: [], has_more: false })
  api.listPolicies.mockResolvedValue({ items: [], has_more: false })
  api.getPolicy.mockImplementation((id: string) =>
    id === 'pol-migrations'
      ? Promise.resolve({
          id,
          name: 'Database migrations need a second person',
          kind: 'approval',
        })
      : Promise.reject(new Error('not found')),
  )
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
  api.getApproval.mockImplementation((id: string) =>
    Promise.resolve([migration, registry, own].find((a) => a.id === id)),
  )
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [
      {
        run_ref: 'run-1',
        name: 'Payments migration',
        workspace_ref: 'ws-1',
        state: 'running',
        pending_approval_ref: 'apr-migrate',
        reaches: {
          folders: [{ path: '/srv/payments-api', access: 'write' }],
          hosts: ['api.openai.com'],
        },
      } as never,
    ],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'getWorkspace').mockResolvedValue({
    workspace_ref: 'ws-1',
    root_path: '/srv/payments-api',
  } as never)
})

describe('Approvals: the flag stays on the request that needs you', () => {
  it('flags the first request this person may decide, and selecting another row leaves the flag there', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [own, migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('Terraform needs the provider registry')
    const flagged = () =>
      screen
        .getAllByRole('button')
        .filter((b) => b.getAttribute('data-flagged') === 'true')

    // Their own request waits for someone else: the flag is on the migration.
    expect(flagged()).toEqual([rowOf('pnpm db:migrate')])
    // The flagged request is the one open on the right.
    expect(rowOf('pnpm db:migrate')).toHaveAttribute('aria-current', 'true')

    await user.click(rowOf('Terraform needs the provider registry'))

    // Selection moved (a tone step); the orange edge did not follow it.
    expect(rowOf('Terraform needs the provider registry')).toHaveAttribute(
      'aria-current',
      'true',
    )
    expect(rowOf('pnpm db:migrate')).not.toHaveAttribute('aria-current')
    expect(flagged()).toEqual([rowOf('pnpm db:migrate')])
  })

  it('flags nothing for a person who may not decide', async () => {
    auth.can = (p) => p !== 'governance:approval:admin'
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('Terraform needs the provider registry')
    expect(
      screen
        .getAllByRole('button')
        .filter((b) => b.getAttribute('data-flagged') === 'true'),
    ).toEqual([])
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })
})

describe('Approvals: each request says what runs, what it reaches and the rule that asked', () => {
  it('shows the command, the rule and the reach in the row and in the open request', async () => {
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const row = await waitFor(() => {
      const r = rowOf('pnpm db:migrate')
      expect(r).toBeDefined()
      return r
    })
    expect(
      await within(row).findByText(
        'Matched: Database migrations need a second person',
      ),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(row).toHaveTextContent(
        'Reaches: /srv/payments-api (write), api.openai.com',
      ),
    )
    expect(
      row.querySelector(
        '[title="Reaches: /srv/payments-api (write), api.openai.com"]',
      ),
    ).not.toBeNull()
    // A new network destination reaches the host it names; no rule matched it.
    const other = rowOf('Terraform needs the provider registry')
    await waitFor(() =>
      expect(other).toHaveTextContent('Reaches: registry.terraform.io'),
    )
    expect(
      within(other).getByText('Matched: no rule, so the default applies'),
    ).toBeInTheDocument()

    const panel = screen.getByRole('complementary', { name: 'Request' })
    const command = await within(panel).findByText(
      'pnpm db:migrate --env payments-staging',
    )
    expect(command.tagName).toBe('PRE')
    expect(
      await within(panel).findByText(
        'Matched: Database migrations need a second person',
      ),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(panel).toHaveTextContent(
        'Reaches: /srv/payments-api (write), api.openai.com',
      ),
    )
  })

  it('keeps dollar replacement characters literal in the full reach tooltip', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [
        {
          run_ref: 'run-1',
          name: 'Payments migration',
          workspace_ref: 'ws-1',
          state: 'running',
          pending_approval_ref: 'apr-migrate',
          reaches: {
            folders: [{ path: '/srv/a$$b$&c', access: 'write' }],
            hosts: ['api.openai.com'],
          },
        } as never,
      ],
      has_more: false,
    })
    api.listApprovals.mockResolvedValue({
      items: [migration],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const row = await waitFor(() => {
      const found = rowOf('pnpm db:migrate')
      expect(found).toBeDefined()
      return found
    })
    expect(
      await within(row).findByTitle(
        'Reaches: /srv/a$$b$&c (write), api.openai.com',
      ),
    ).toBeInTheDocument()
  })

  it('leads a request a person raised with their words, never with its kind name', async () => {
    api.listApprovals.mockResolvedValue({
      items: [registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findAllByText('Terraform needs the provider registry')
    expect(screen.queryByText('Network connect')).toBeNull()
  })

  it('says the requester cannot approve their own request', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('Terraform needs the provider registry')
    await user.click(rowOf('Terraform needs the provider registry'))
    const panel = screen.getByRole('complementary', { name: 'Request' })
    expect(
      await within(panel).findByText(
        'The person who asked cannot approve their own request.',
      ),
    ).toBeInTheDocument()
  })
})

describe('Approvals: approving the flagged request is one action', () => {
  it('Approve once records one approval with one click, and no dialog opens', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    api.decide.mockResolvedValue({ ...migration, status: 'approved' })
    wrap(<GovernanceView />)
    await screen.findByText('Terraform needs the provider registry')

    await user.click(
      await screen.findByRole('button', { name: 'Approve once' }),
    )

    await waitFor(() => expect(api.decide).toHaveBeenCalledTimes(1))
    expect(api.decide).toHaveBeenCalledWith('apr-migrate', {
      decision: 'approve',
    })
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() =>
      expect(toast.success.mock.calls[0]?.[0]).toBe(
        'Decision recorded — approved',
      ),
    )
    // Still one decision once it has been recorded.
    expect(api.decide).toHaveBeenCalledTimes(1)
  })

  it('A approves the open request once; D opens the denial', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    api.decide.mockResolvedValue({ ...migration, status: 'approved' })
    wrap(<GovernanceView />)
    await screen.findByRole('button', { name: 'Approve once' })

    await user.keyboard('d')
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('button', { name: 'Deny' }),
    ).toBeInTheDocument()
    // A key pressed while the dialog is open belongs to the dialog, even with the focus
    // on a button and not in its note.
    within(dialog).getByRole('button', { name: 'Deny' }).focus()
    await user.keyboard('a')
    expect(api.decide).not.toHaveBeenCalled()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    await user.keyboard('a')
    await waitFor(() => expect(api.decide).toHaveBeenCalledTimes(1))
    expect(api.decide).toHaveBeenCalledWith('apr-migrate', {
      decision: 'approve',
    })
  })

  it('A does nothing while the person types in the search field', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByRole('button', { name: 'Approve once' })
    await user.type(screen.getByRole('searchbox'), 'a')
    expect(api.decide).not.toHaveBeenCalled()
  })
})

import { navLeader } from '@/lib/keybindings/model'

describe('Approvals: a bare key never decides what the person is not reading', () => {
  it('D does nothing while the status filter has the focus (its own typeahead)', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByRole('button', { name: 'Approve once' })
    // `d` names no status, so the filter stays as it is and only the guard decides.
    screen.getByRole('combobox', { name: 'Status' }).focus()
    await user.keyboard('d')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(api.decide).not.toHaveBeenCalled()
  })

  it('A does nothing as the second half of a g sequence, whichever listener runs first', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByRole('button', { name: 'Approve once' })
    navLeader.arm(Date.now() + 60_000)
    try {
      await user.keyboard('a')
    } finally {
      navLeader.disarm()
    }
    expect(api.decide).not.toHaveBeenCalled()
  })

  it('keeps the decided request on screen: the next one is not swapped in under the key', async () => {
    const user = userEvent.setup()
    const approved = {
      ...migration,
      status: 'approved' as const,
      approve_count: 1,
    }
    api.listApprovals
      .mockResolvedValueOnce({ items: [migration, registry], has_more: false })
      .mockResolvedValue({ items: [registry], has_more: false })
    api.decide.mockResolvedValue(approved)
    wrap(<GovernanceView />)
    await screen.findByRole('button', { name: 'Approve once' })
    api.getApproval.mockImplementation((id: string) =>
      Promise.resolve(id === migration.id ? approved : registry),
    )

    await user.keyboard('a')
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    // The queue moved on; the panel did not.
    await waitFor(() =>
      expect(
        screen.queryByText('pnpm db:migrate --env payments-staging', {
          selector: '[data-approval-row] *',
        }),
      ).toBeNull(),
    )
    const panel = screen.getByRole('complementary', { name: 'Request' })
    expect(
      await within(panel).findByText('pnpm db:migrate --env payments-staging'),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(
        within(panel).queryByRole('button', { name: 'Approve once' }),
      ).toBeNull(),
    )
    await user.keyboard('a')
    expect(api.decide).toHaveBeenCalledTimes(1)
  })

  it('opens the linked request even when the queue does not list it, never another one', async () => {
    window.history.replaceState(
      {},
      '',
      '/permissions?tab=approvals&approval=apr-gone',
    )
    try {
      api.listApprovals.mockResolvedValue({
        items: [migration, registry],
        has_more: false,
      })
      api.getApproval.mockImplementation((id: string) =>
        Promise.resolve(
          id === 'apr-gone'
            ? {
                ...registry,
                id,
                status: 'expired',
                reason: 'Linked from the bell',
              }
            : migration,
        ),
      )
      wrap(<GovernanceView />)
      const panel = await screen.findByRole('complementary', {
        name: 'Request',
      })
      expect(
        (await within(panel).findAllByText('Linked from the bell')).length,
      ).toBeGreaterThan(0)
      expect(
        within(panel).queryByRole('button', { name: 'Approve once' }),
      ).toBeNull()
      // The flag still marks the request that waits for this person.
      expect(rowOf('pnpm db:migrate')).toHaveAttribute('data-flagged', 'true')
    } finally {
      window.history.replaceState({}, '', '/')
    }
  })

  it('does not offer a second decision on a request this person already approved', async () => {
    api.listApprovals.mockResolvedValue({
      items: [{ ...migration, required_approvals: 2, approve_count: 1 }],
      has_more: false,
    })
    api.listDecisions.mockResolvedValue({
      items: [{ decision: 'approve', decider: 'user:reviewer' }],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const panel = await screen.findByRole('complementary', { name: 'Request' })
    expect(
      await within(panel).findByText(
        'You approved this request. It waits for another approver.',
      ),
    ).toBeInTheDocument()
    expect(
      within(panel).queryByRole('button', { name: 'Approve once' }),
    ).toBeNull()
  })
})

describe('Approvals: what cannot be read is said, never guessed', () => {
  it('says the rule could not be read when its read fails', async () => {
    api.getPolicy.mockRejectedValue(new Error('boom'))
    api.listApprovals.mockResolvedValue({ items: [migration], has_more: false })
    wrap(<GovernanceView />)
    await waitFor(() => expect(rowOf('pnpm db:migrate')).toBeDefined())
    expect(
      await within(rowOf('pnpm db:migrate')).findByText(
        'Matched: a rule that could not be read',
      ),
    ).toBeInTheDocument()
  })

  it('says a reach the engine could not read in full is incomplete', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [
        {
          run_ref: 'run-1',
          name: 'Payments migration',
          state: 'running',
          pending_approval_ref: 'apr-migrate',
          reaches: {
            folders: [{ path: '/srv/payments-api', access: 'write' }],
            hosts: [],
            reason: 'the provider profile could not be read',
          },
        } as never,
      ],
      has_more: false,
    })
    api.listApprovals.mockResolvedValue({ items: [migration], has_more: false })
    wrap(<GovernanceView />)
    await waitFor(() => expect(rowOf('pnpm db:migrate')).toBeDefined())
    await waitFor(() =>
      expect(rowOf('pnpm db:migrate')).toHaveTextContent(
        'Reaches: /srv/payments-api (write), more that could not be read',
      ),
    )
  })
})

describe('Approvals: the queue around the decision', () => {
  it('filters the rows by the search and says when nothing matches', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [migration, registry],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('Terraform needs the provider registry')
    await user.type(screen.getByRole('searchbox'), 'terraform')
    expect(
      screen
        .getAllByRole('button')
        .filter((b) => b.hasAttribute('data-approval-row')),
    ).toEqual([rowOf('Terraform needs the provider registry')])
    await user.clear(screen.getByRole('searchbox'))
    await user.type(screen.getByRole('searchbox'), 'nothing like it')
    expect(
      screen.getByText('No request matches the search'),
    ).toBeInTheDocument()
  })

  it('cancels the open request from the panel through its confirmation', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({ items: [migration], has_more: false })
    api.cancelApproval.mockResolvedValue({ ...migration, status: 'canceled' })
    wrap(<GovernanceView />)
    const panel = await screen.findByRole('complementary', { name: 'Request' })
    await user.click(within(panel).getByRole('button', { name: 'Cancel' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: 'Cancel request' }),
    )
    await waitFor(() =>
      expect(api.cancelApproval).toHaveBeenCalledWith('apr-migrate'),
    )
  })
})
