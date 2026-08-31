// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApprovalDTO, BindingDTO, PolicyDTO } from './types'

// --- mocks -------------------------------------------------------------------

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
  principal: null as { actor: string; kind: string } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listIdentities: vi.fn(),
  listGroups: vi.fn(),
  listGroupMembers: vi.fn(),
  syncRoster: vi.fn(),
  listBindings: vi.fn(),
  bindAgentIdentity: vi.fn(),
  unbindAgentIdentity: vi.fn(),
  listPolicies: vi.fn(),
  getPolicy: vi.fn(),
  createPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  deletePolicy: vi.fn(),
  listApprovals: vi.fn(),
  createApproval: vi.fn(),
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  decide: vi.fn(),
  cancelApproval: vi.fn(),
  sweepApprovals: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import GovernanceView from './governance-view'
import { PolicyEditorDialog } from './policy-editor'
import { IdentitiesView } from './identities-view'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const pendingApproval: ApprovalDTO = {
  id: 'a1',
  action: 'deploy.release',
  subject_kind: 'deployment',
  subject_ref: 'rel-42',
  requested_by: 'user:u-7',
  status: 'pending',
  required_approvals: 2,
  approve_count: 0,
  reject_count: 0,
  reason: 'Ship the hotfix',
  escalated: false,
}

const escalatedApproval: ApprovalDTO = {
  ...pendingApproval,
  id: 'a2',
  action: 'data.purge',
  subject_ref: 'tbl-9',
  requested_by: 'token:t-3',
  escalated: true,
  approve_count: 1,
}

beforeEach(() => {
  authState.can = () => true
  authState.principal = null
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  // Default resolutions so background tab queries never reject.
  api.listApprovals.mockResolvedValue({ items: [], has_more: false })
  api.listPolicies.mockResolvedValue({ items: [], has_more: false })
  api.listIdentities.mockResolvedValue({ items: [], has_more: false })
  api.listGroups.mockResolvedValue({ items: [], has_more: false })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.getApproval.mockResolvedValue(pendingApproval)
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => vi.clearAllMocks())

describe('GovernanceView — approval queue (HITL)', () => {
  it('lists pending approval requests with action, requester and escalation cue', async () => {
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval, escalatedApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    expect(await screen.findByText('deploy.release')).toBeInTheDocument()
    expect(screen.getByText('data.purge')).toBeInTheDocument()
    // requester rendered as an audit-actor handle, never an email.
    expect(screen.getByText('user:u-7')).toBeInTheDocument()
    expect(screen.getByText('token:t-3')).toBeInTheDocument()
    // escalated request carries the warning badge.
    expect(screen.getByText('Escalated')).toBeInTheDocument()
  })

  it('hides Approve/Reject when the role lacks approval:admin', async () => {
    authState.can = (p) => p !== 'governance:approval:admin'
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('deploy.release')
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
    // Run sweep is also admin-only.
    expect(screen.queryByRole('button', { name: /run sweep/i })).toBeNull()
  })

  it('hides Approve/Reject on a request the current operator opened (separation of duties)', async () => {
    // The admin is also the requester (user:u-7) — the engine would 403 a self-decision.
    authState.principal = { actor: 'user:u-7', kind: 'user' }
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('deploy.release')
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
    // Cancel (write tier) is still offered to the requester.
    expect(
      screen.getByRole('button', { name: /^cancel$/i }),
    ).toBeInTheDocument()
  })

  it('approve flow: click → confirm dialog → api.decide(approve, note) → success toast', async () => {
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    api.decide.mockResolvedValue({ ...pendingApproval, approve_count: 1 })
    wrap(<GovernanceView />)

    await screen.findByText('deploy.release')
    await userEvent.click(screen.getByRole('button', { name: /^approve$/i }))

    // The confirm dialog gates the high-risk decision and collects a note.
    const dialog = await screen.findByRole('dialog')
    const note = within(dialog).getByLabelText(/^note$/i)
    await userEvent.type(note, 'looks good')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^approve$/i }),
    )

    await waitFor(() => expect(api.decide).toHaveBeenCalledTimes(1))
    expect(api.decide.mock.calls[0][0]).toBe('a1')
    expect(api.decide.mock.calls[0][1]).toMatchObject({
      decision: 'approve',
      note: 'looks good',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('runs the admin sweep through a confirm dialog', async () => {
    api.listApprovals.mockResolvedValue({ items: [], has_more: false })
    api.sweepApprovals.mockResolvedValue({
      scanned: 3,
      escalated: 1,
      expired: 1,
      more: false,
    })
    wrap(<GovernanceView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /run sweep/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /run sweep/i }),
    )
    await waitFor(() => expect(api.sweepApprovals).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

describe('PolicyEditorDialog — typed spec, no secret value, audited', () => {
  it('shows the audit notice and offers NO secret-value input; gates on a name', async () => {
    wrap(<PolicyEditorDialog open onOpenChange={() => {}} />)
    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()
    // No secret-value field exists anywhere in the governance editor.
    expect(screen.queryByLabelText(/secret value/i)).toBeNull()

    const create = screen.getByRole('button', { name: /create policy/i })
    expect(create).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/^name/i), 'no-prod-delete')
    // ABAC (default) additionally requires at least one deny rule, so a name alone
    // is not enough — adding a rule (deny is implicit, never a user input) enables it.
    expect(create).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: /add rule/i }))
    await userEvent.type(
      screen.getByLabelText(/^permission$/i),
      'governance:policy:admin',
    )
    expect(create).toBeEnabled()
    // "Deny" is shown as implicit and there is no editable deny control.
    expect(screen.getByText(/deny \(implicit\)/i)).toBeInTheDocument()
  })

  it('creates an approval policy (submit → api.createPolicy → success toast → close)', async () => {
    api.createPolicy.mockResolvedValue({
      id: 'p1',
      name: 'two-eyes',
      kind: 'approval',
      enabled: true,
      spec: { required_approvals: 2 },
    } satisfies PolicyDTO)
    const onOpenChange = vi.fn()
    wrap(<PolicyEditorDialog open onOpenChange={onOpenChange} />)

    await userEvent.type(screen.getByLabelText(/^name/i), 'two-eyes')
    // Switch the kind to approval so the threshold form renders (default is abac,
    // which would need at least one deny rule).
    await userEvent.click(screen.getByRole('combobox'))
    await userEvent.click(
      await screen.findByRole('option', { name: /approval/i }),
    )

    await userEvent.click(
      screen.getByRole('button', { name: /create policy/i }),
    )
    await waitFor(() => expect(api.createPolicy).toHaveBeenCalledTimes(1))
    expect(api.createPolicy.mock.calls[0][0]).toMatchObject({
      name: 'two-eyes',
      kind: 'approval',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})

describe('IdentitiesView — roster, bindings RBAC + shared cue', () => {
  it('flags an identity shared across multiple agents and gates unbind on identity:admin', async () => {
    const shared: BindingDTO = {
      agent_id: 'ag-1',
      agent_name: 'planner',
      identity_id: 'id-1',
      identity_ref: 'entity:svc',
      shared: true,
      agent_count: 3,
    }
    authState.can = (p) => p !== 'governance:identity:admin'
    api.listBindings.mockResolvedValue({ items: [shared], has_more: false })
    wrap(<IdentitiesView />)

    // Move to the bindings sub-tab.
    await userEvent.click(screen.getByRole('tab', { name: /agent bindings/i }))
    expect(await screen.findByText('entity:svc')).toBeInTheDocument()
    // The shared-attribution warning is present.
    expect(screen.getByText(/shared/i)).toBeInTheDocument()
    // Unbind is hidden without identity:admin.
    expect(screen.queryByRole('button', { name: /unbind/i })).toBeNull()
  })

  it('shows the resync action and confirms it (identity:admin)', async () => {
    api.listIdentities.mockResolvedValue({ items: [], has_more: false })
    api.syncRoster.mockResolvedValue({
      sources: 1,
      providers_configured: 1,
      identities: 4,
      collections: 2,
      memberships: 6,
    })
    wrap(<IdentitiesView />)
    await userEvent.click(
      await screen.findByRole('button', { name: /resync roster/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^resync$/i }),
    )
    await waitFor(() => expect(api.syncRoster).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})
