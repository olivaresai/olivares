// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { formatDateTime, formatShortDateTime } from '@/lib/format'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApprovalDTO, BindingDTO, PolicyDTO } from './types'
import { ApiError } from '@/lib/api/errors'

vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: { ...actual.PANEL_EXTENSIONS, governanceTabs: [] },
  }
})

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
// Exercise the Community composition even when the tests are assembled into a paid tree.
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      capabilitiesTabs: [],
      complianceTabs: [],
      reportingCards: [],
      licenseCards: [],
      governanceTabs: [],
    },
  }
})

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
  listBreakGlass: vi.fn(),
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
import { GroupMembersSheet } from './group-members'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

/** The queue's rows (each one a button that opens the request beside the queue). */
const queueRows = () =>
  screen
    .getAllByRole('button')
    .filter((b) => b.hasAttribute('data-approval-row'))

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

// A request a person raised shows its own reason in the row and no "does not include what
// will run" note (#2269): the reason is the row's sentinel.
const ROW_REASON = 'Ship the hotfix'

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
  api.listBreakGlass.mockResolvedValue({ items: [], has_more: false })
  api.listPolicies.mockResolvedValue({ items: [], has_more: false })
  api.listIdentities.mockResolvedValue({ items: [], has_more: false })
  api.listGroups.mockResolvedValue({ items: [], has_more: false })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.getApproval.mockResolvedValue(pendingApproval)
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
})
afterEach(() => {
  vi.clearAllMocks()
  window.history.replaceState({}, '', '/')
})

describe('GovernanceView — approval queue (HITL)', () => {
  it('Community omits the break-glass panel, including a stored deep link', () => {
    window.history.replaceState({}, '', '/permissions?tab=break-glass')
    wrap(<GovernanceView />)
    expect(
      screen.queryByRole('tab', { name: /emergency access/i }),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /approvals/i })).toHaveAttribute(
      'data-state',
      'active',
    )
  })
  // Changed by the SC sweep on 09b (Root): the list leads with what will run; the action
  // and the references moved to the review's Details, so they are not in the row.
  it('lists pending approval requests with what will run, requester and escalation cue', async () => {
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval, escalatedApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await waitFor(() => expect(queueRows()).toHaveLength(2))
    const [first, second] = queueRows()
    for (const row of [first, second]) {
      expect(within(row).getByText(ROW_REASON)).toBeInTheDocument()
      // The engine's action names stay behind Details, never in the row.
      expect(within(row).queryByText('deploy.release')).toBeNull()
      expect(within(row).queryByText('data.purge')).toBeNull()
    }
    // The row names who asks in words; the audit-actor handle (never an email) is in
    // the open request's Details.
    expect(within(first).getByText('A member')).toBeVisible()
    expect(within(second).getByText('An API token')).toBeVisible()
    const panel = screen.getByRole('complementary', { name: 'Request' })
    const details = within(panel).getByText('user:u-7').closest('details')!
    expect(details).not.toHaveAttribute('open')
    // escalated request carries the warning badge.
    expect(within(second).getByText('Escalated')).toBeInTheDocument()
  })

  it('an approval that expired says so with its stored time, never "—" (HU 039)', async () => {
    // A no-expiry policy: no expires_at; the engine stores expired + decided_at (PEP f0c79f29).
    const { expires_at: _, ...noExpiry } = pendingApproval
    api.listApprovals.mockResolvedValue({
      items: [
        {
          ...noExpiry,
          status: 'expired',
          decided_at: '2026-10-02T03:36:00Z',
        },
      ],
      has_more: false,
    })
    wrap(<GovernanceView />)
    // The short time fits the column; the whole date and time is its title.
    const cell = await screen.findByText(
      `Expired ${formatShortDateTime('2026-10-02T03:36:00Z')}`,
    )
    expect(cell).toHaveAttribute(
      'title',
      formatDateTime('2026-10-02T03:36:00Z'),
    )
  })

  it('hides Approve/Reject when the role lacks approval:admin', async () => {
    authState.can = (p) => p !== 'governance:approval:admin'
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await waitFor(() => expect(queueRows()).toHaveLength(1))
    expect(screen.queryByRole('button', { name: /^approve once$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^deny$/i })).toBeNull()
    // The sweep ("Expire and escalate overdue requests", SC item 7 wording) is also admin-only.
    expect(
      screen.queryByRole('button', {
        name: /expire and escalate overdue requests/i,
      }),
    ).toBeNull()
  })

  it('hides Approve/Reject on a request the current operator opened (separation of duties)', async () => {
    // The admin is also the requester (user:u-7) — the engine would 403 a self-decision.
    authState.principal = { actor: 'user:u-7', kind: 'user' }
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await waitFor(() => expect(queueRows()).toHaveLength(1))
    expect(screen.queryByRole('button', { name: /^approve once$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^deny$/i })).toBeNull()
    // Cancel (write tier) is still offered to the requester.
    expect(
      screen.getByRole('button', { name: /^cancel$/i }),
    ).toBeInTheDocument()
  })

  it('approve flow: Approve once → api.decide(approve) → success toast', async () => {
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    api.decide.mockResolvedValue({ ...pendingApproval, approve_count: 1 })
    wrap(<GovernanceView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /^approve once$/i }),
    )

    await waitFor(() => expect(api.decide).toHaveBeenCalledTimes(1))
    expect(api.decide).toHaveBeenCalledWith('a1', { decision: 'approve' })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it('deny flow: Deny → dialog with the note → api.decide(reject, note)', async () => {
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    api.decide.mockResolvedValue({ ...pendingApproval, status: 'rejected' })
    wrap(<GovernanceView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /^deny$/i }),
    )
    // A denial is confirmed and can carry a note.
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText(/^note$/i), 'not now')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^deny$/i }),
    )

    await waitFor(() => expect(api.decide).toHaveBeenCalledTimes(1))
    expect(api.decide).toHaveBeenCalledWith('a1', {
      decision: 'reject',
      note: 'not now',
    })
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
      await screen.findByRole('button', {
        name: /expire and escalate overdue requests/i,
      }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', {
        name: /expire and escalate overdue requests/i,
      }),
    )
    await waitFor(() => expect(api.sweepApprovals).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

// SC 59 item 1: an expired request answered 409 and the dialog stayed open with Approve.
describe('Approve after the request is no longer pending', () => {
  it('closes, says so in the engine words, and reads the queue again', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    api.decide.mockRejectedValue(
      new ApiError(409, 'conflict', 'approval is not pending (expired)'),
    )
    wrap(<GovernanceView />)
    const approve = await screen.findByRole('button', {
      name: /^approve once$/i,
    })
    const reads = api.listApprovals.mock.calls.length
    await user.click(approve)
    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(toast.warning).toHaveBeenCalledWith(
      'This request is no longer waiting for a decision.',
      { description: 'approval is not pending (expired)' },
    )
    await waitFor(() =>
      expect(api.listApprovals.mock.calls.length).toBeGreaterThan(reads),
    )
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

  // AR on 09b: a name-only save dropped min_aal, so a conditional deny became
  // unconditional. The body on the wire must be the stored policy with the new name.
  async function renameAndSave(policy: PolicyDTO) {
    api.updatePolicy.mockResolvedValue({ ...policy, name: 'renamed' })
    wrap(<PolicyEditorDialog open onOpenChange={() => {}} policy={policy} />)
    const name = screen.getByLabelText(/^name/i)
    await userEvent.clear(name)
    await userEvent.type(name, 'renamed')
    await userEvent.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() => expect(api.updatePolicy).toHaveBeenCalledTimes(1))
    const [id, body] = api.updatePolicy.mock.calls[0]
    expect(id).toBe(policy.id)
    return JSON.parse(JSON.stringify(body))
  }

  it('a rename sends the stored abac rule unchanged, min_aal included', async () => {
    const policy: PolicyDTO = {
      id: 'p1',
      name: 'sign-in guard',
      kind: 'abac',
      enabled: true,
      spec: {
        rules: [
          { deny: true, min_aal: 1, permission: 'governance:identity:read' },
          { deny: true, min_aal: 3, principal_kind: 'user', verb: 'admin' },
        ],
      },
    }
    expect(await renameAndSave(policy)).toStrictEqual({
      name: 'renamed',
      kind: 'abac',
      enabled: true,
      spec: policy.spec,
    })
  })

  it('shows the stored minimum sign-in of each rule', () => {
    wrap(
      <PolicyEditorDialog
        open
        onOpenChange={() => {}}
        policy={{
          id: 'p1',
          name: 'sign-in guard',
          kind: 'abac',
          enabled: true,
          spec: { rules: [{ deny: true, min_aal: 2, verb: 'write' }] },
        }}
      />,
    )
    expect(
      screen.getByRole('combobox', { name: /minimum sign-in/i }),
    ).toHaveTextContent('AAL2 (multi-factor)')
  })

  it('a rename sends the stored approval spec unchanged, risk_tier included', async () => {
    const policy: PolicyDTO = {
      id: 'p2',
      name: 'two-eyes',
      kind: 'approval',
      enabled: false,
      spec: {
        match: { action: 'deploy.release', subject_kind: 'deployment' },
        required_approvals: 3,
        expires_in_seconds: 3600,
        escalate_in_seconds: 600,
        risk_tier: 'critical',
      },
    }
    expect(await renameAndSave(policy)).toStrictEqual({
      name: 'renamed',
      kind: 'approval',
      enabled: false,
      spec: policy.spec,
    })
  })

  it('a rename does not invent an approval count the engine left out', async () => {
    // A critical tier with no count: sending 1 would be refused (critical needs 2).
    const policy: PolicyDTO = {
      id: 'p3',
      name: 'critical default',
      kind: 'approval',
      enabled: true,
      spec: { match: {}, risk_tier: 'critical' },
    }
    expect(await renameAndSave(policy)).toStrictEqual({
      name: 'renamed',
      kind: 'approval',
      enabled: true,
      spec: policy.spec,
    })
  })

  it('a rule can require a minimum sign-in, alone or with other conditions', async () => {
    api.createPolicy.mockResolvedValue({
      id: 'p4',
      name: 'step-up',
      kind: 'abac',
      enabled: true,
      spec: { rules: [{ deny: true, min_aal: 3 }] },
    } satisfies PolicyDTO)
    wrap(<PolicyEditorDialog open onOpenChange={() => {}} />)
    await userEvent.type(screen.getByLabelText(/^name/i), 'step-up')
    await userEvent.click(screen.getByRole('button', { name: /add rule/i }))
    const create = screen.getByRole('button', { name: /create policy/i })
    expect(create).toBeDisabled()
    await userEvent.click(
      screen.getByRole('combobox', { name: /minimum sign-in/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'AAL3 (phishing-resistant)' }),
    )
    expect(create).toBeEnabled()
    await userEvent.click(create)
    await waitFor(() => expect(api.createPolicy).toHaveBeenCalledTimes(1))
    expect(
      JSON.parse(JSON.stringify(api.createPolicy.mock.calls[0][0])).spec,
    ).toStrictEqual({ rules: [{ deny: true, min_aal: 3 }] })
  })

  // The form has no risk-tier field, but it keeps the tier of a stored policy and asks
  // for an approval count. Saving a tier or a count other than the action's default is
  // how Community refuses from this form (501 business_required). The dialog says why,
  // in a line that stays.
  it('names the Business limit in the dialog when the server refuses a tier or an approval count', async () => {
    const limit =
      /an approval count other than the action's default needs business/i
    api.updatePolicy.mockRejectedValue(
      new ApiError(
        501,
        'business_required',
        'Approval policies that change the risk tier or the approval count require Olivares Business.',
      ),
    )
    const policy: PolicyDTO = {
      id: 'p9',
      name: 'legacy downgrade',
      kind: 'approval',
      enabled: true,
      spec: { risk_tier: 'high', match: { action: 'deploy.apply' } },
    }
    wrap(<PolicyEditorDialog open onOpenChange={() => {}} policy={policy} />)
    expect(screen.queryByText(limit)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /save changes/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(limit)
    expect(toast.error).not.toHaveBeenCalled()
    // A later save that works clears the line.
    api.updatePolicy.mockResolvedValue(policy)
    await userEvent.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('keeps the generic error toast for any other refusal', async () => {
    api.updatePolicy.mockRejectedValue(
      new ApiError(500, 'internal', 'store unavailable'),
    )
    wrap(
      <PolicyEditorDialog
        open
        onOpenChange={() => {}}
        policy={{
          id: 'p10',
          name: 'two-eyes',
          kind: 'approval',
          enabled: true,
          spec: { required_approvals: 2 },
        }}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: /save changes/i }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('approval counts must be whole numbers in range, or nothing is sent', async () => {
    api.createPolicy.mockResolvedValue({
      id: 'p5',
      name: 'two-eyes',
      kind: 'approval',
      enabled: true,
      spec: { match: {}, required_approvals: 2 },
    } satisfies PolicyDTO)
    wrap(<PolicyEditorDialog open onOpenChange={() => {}} />)
    await userEvent.type(screen.getByLabelText(/^name/i), 'two-eyes')
    await userEvent.click(screen.getByRole('combobox'))
    await userEvent.click(
      await screen.findByRole('option', { name: /approval/i }),
    )
    const create = screen.getByRole('button', { name: /create policy/i })
    const required = screen.getByLabelText(/^required approvals/i)
    // The three values AR saw sent on 09b.
    for (const bad of ['-1', '65', '1.5']) {
      await userEvent.clear(required)
      await userEvent.type(required, bad)
      expect(required).toHaveAccessibleDescription(
        /enter a whole number from 0 to 64/i,
      )
      expect(create).toBeDisabled()
    }
    const expires = screen.getByLabelText(/^expires in/i)
    await userEvent.clear(expires)
    await userEvent.type(expires, '31536001')
    expect(
      screen.getByText('Enter a whole number from 0 to 31536000.'),
    ).toBeInTheDocument()
    expect(create).toBeDisabled()
    await userEvent.clear(expires)
    await userEvent.clear(required)
    await userEvent.type(required, '2')
    expect(screen.queryByRole('alert')).toBeNull()
    await userEvent.click(create)
    await waitFor(() => expect(api.createPolicy).toHaveBeenCalledTimes(1))
    expect(
      JSON.parse(JSON.stringify(api.createPolicy.mock.calls[0][0])).spec,
    ).toStrictEqual({ match: {}, required_approvals: 2 })
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
})

describe('IdentitiesView — cursor pages and an account state nobody reported', () => {
  const identity = (
    ref: string,
    extra: Partial<Record<string, unknown>> = {},
  ) => ({
    id: `id-${ref}`,
    ref,
    kind: 'service_account',
    source: 'dir',
    principal_type: 'nhi',
    disabled: false,
    ...extra,
  })

  it('walks the identities cursor instead of presenting the first page as the whole roster', async () => {
    api.listIdentities.mockImplementation(
      async (params?: { cursor?: string }) =>
        params?.cursor === 'ident-page-2'
          ? { items: [identity('svac_second')], has_more: false }
          : {
              items: [identity('apikey_first')],
              cursor: 'ident-page-2',
              has_more: true,
            },
    )
    wrap(<IdentitiesView />)

    expect(await screen.findByText('apikey_first')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('svac_second')).toBeInTheDocument()
    expect(api.listIdentities).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'ident-page-2' }),
    )
    // The last page is loaded: nothing is left to say about partial search.
    expect(screen.queryByText(/search covers only the rows loaded/i)).toBeNull()
  })

  it('says the search covers only the loaded rows while more pages exist', async () => {
    api.listIdentities.mockResolvedValue({
      items: [identity('apikey_first')],
      cursor: 'ident-page-2',
      has_more: true,
    })
    wrap(<IdentitiesView />)

    expect(await screen.findByText('apikey_first')).toBeInTheDocument()
    expect(
      screen.getByText(/search covers only the rows loaded/i),
    ).toBeInTheDocument()
  })

  it('walks the groups cursor instead of presenting the first page as every group', async () => {
    api.listGroups.mockImplementation(async (params?: { cursor?: string }) =>
      params?.cursor === 'group-page-2'
        ? {
            items: [{ ref: 'grp_second', kind: 'group', source: 'dir' }],
            has_more: false,
          }
        : {
            items: [{ ref: 'grp_first', kind: 'group', source: 'dir' }],
            cursor: 'group-page-2',
            has_more: true,
          },
    )
    wrap(<IdentitiesView />)
    await userEvent.click(screen.getByRole('tab', { name: 'Groups' }))

    expect((await screen.findAllByText('grp_first')).length).toBeGreaterThan(0)
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect((await screen.findAllByText('grp_second')).length).toBeGreaterThan(0)
    expect(api.listGroups).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'group-page-2' }),
    )
  })

  it('shows an account whose state the source never reported as unknown, not enabled', async () => {
    api.listIdentities.mockResolvedValue({
      items: [
        identity('cn=bridge', { disabled: null }),
        identity('cn=on'),
        identity('cn=off', { disabled: true }),
      ],
      has_more: false,
    })
    wrap(<IdentitiesView />)

    const stateOf = async (ref: string) =>
      within((await screen.findByText(ref)).closest('tr')!).getByText(
        /^(enabled|disabled|unknown)$/i,
      ).textContent
    expect(await stateOf('cn=on')).toMatch(/^enabled$/i)
    expect(await stateOf('cn=off')).toMatch(/^disabled$/i)
    expect(await stateOf('cn=bridge')).toMatch(/^unknown$/i)
  })
})

describe('GroupMembersSheet — bounded transitive walk', () => {
  it('distinguishes an unknown group from an existing empty group', async () => {
    api.listGroupMembers.mockResolvedValue({
      items: [],
      has_more: false,
      not_found: true,
    })
    wrap(<GroupMembersSheet open onOpenChange={() => {}} groupRef="grp:typo" />)
    expect(
      await screen.findByText('This group does not exist.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('No members')).toBeNull()
  })

  it('says so when the walk was cut at the depth bound', async () => {
    api.listGroupMembers.mockResolvedValue({
      items: [{ member_ref: 'user:near', member_kind: 'identity' }],
      has_more: false,
      truncated: true,
    })
    wrap(<GroupMembersSheet open onOpenChange={() => {}} groupRef="grp:0" />)

    expect(await screen.findByText('user:near')).toBeInTheDocument()
    expect(screen.getByText(/deeper than the walk limit/i)).toBeInTheDocument()
  })

  it('shows no cut note for a complete walk', async () => {
    api.listGroupMembers.mockResolvedValue({
      items: [{ member_ref: 'user:near', member_kind: 'identity' }],
      has_more: false,
    })
    wrap(<GroupMembersSheet open onOpenChange={() => {}} groupRef="grp:0" />)

    expect(await screen.findByText('user:near')).toBeInTheDocument()
    expect(screen.queryByText(/deeper than the walk limit/i)).toBeNull()
  })
})

describe('GovernanceView — ?tab= deep link (console remake slice 4)', () => {
  afterEach(() => {
    window.history.replaceState({}, '', '/')
    authState.can = () => true
  })

  it('opens the identity inventory from a ?tab=identities link, without a directory resync or agent registration', () => {
    window.history.replaceState({}, '', '/permissions?tab=identities')
    wrap(<GovernanceView />)
    expect(
      screen.getByRole('tab', { name: 'Identities & groups' }),
    ).toHaveAttribute('aria-selected', 'true')
    expect(
      screen.queryByRole('button', { name: /resync roster/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Register agent' }),
    ).not.toBeInTheDocument()
  })

  it('opens the section the link names, and the queue when it names nothing', () => {
    window.history.replaceState({}, '', '/permissions?tab=policies')
    const first = wrap(<GovernanceView />)
    expect(screen.getByRole('tab', { name: 'Policies' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    first.unmount()
    window.history.replaceState({}, '', '/permissions')
    wrap(<GovernanceView />)
    expect(screen.getByRole('tab', { name: 'Approvals' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
  })

  // AR hold07 finding 5: the list said an enabled policy may be inert, while the deny it
  // had just saved answered 403. The engine enforces an enabled policy from its save.
  it('the policies list says an enabled policy applies when saved, not that it may be inert', () => {
    window.history.replaceState({}, '', '/permissions?tab=policies')
    wrap(<GovernanceView />)
    expect(
      screen.getByText(
        'An enabled policy applies as soon as it is saved. Deny rules only take access away; approval thresholds apply to new matching requests.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/inert|not proof of runtime/i)).toBeNull()
  })

  it('never opens the queue by link for a principal who may not read it', () => {
    authState.can = (p: string) => p !== 'governance:approval:read'
    window.history.replaceState({}, '', '/permissions?tab=approvals')
    wrap(<GovernanceView />)
    expect(screen.queryByRole('tab', { name: 'Approvals' })).toBeNull()
    expect(screen.getByRole('tab', { name: 'Policies' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
  })
})

// Root, 09b capture review: Approve used the destructive red. Red is for Deny.
describe('the decision styles', () => {
  it('Approve once is the one orange action; Deny stays red', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const approve = await screen.findByRole('button', {
      name: /^approve once$/i,
    })
    expect(approve.className).toMatch(/bg-accent/)
    expect(approve.className).not.toMatch(/danger/)
    // While a request waits for this person, New request is not a second orange.
    expect(
      screen.getByRole('button', { name: /^new request$/i }).className,
    ).not.toMatch(/bg-accent/)
    await user.click(screen.getByRole('button', { name: /^deny$/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('button', { name: /^deny$/i }).className,
    ).toMatch(/danger/)
  })
})
