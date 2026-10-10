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
vi.mock('@/features/extensions', () => ({
  PANEL_EXTENSIONS: {
    capabilitiesTabs: [],
    complianceTabs: [],
    reportingCards: [],
    licenseCards: [],
    governanceTabs: [],
  },
}))

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

const NO_REVIEW = 'This request does not include what will run.'

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
    expect(await screen.findAllByText(NO_REVIEW)).toHaveLength(2)
    expect(screen.queryByText('deploy.release')).toBeNull()
    expect(screen.queryByText('data.purge')).toBeNull()
    // requester rendered as an audit-actor handle, never an email.
    expect(screen.getByText('user:u-7')).toBeInTheDocument()
    expect(screen.getByText('token:t-3')).toBeInTheDocument()
    const requesterDetails = screen.getByText('user:u-7').closest('details')!
    expect(requesterDetails).not.toHaveAttribute('open')
    expect(screen.getByText('A member')).toBeVisible()
    const requesterSummary = within(requesterDetails).getByText('Details')
    await userEvent.click(requesterSummary)
    expect(requesterDetails).toHaveAttribute('open')
    expect(screen.queryByRole('dialog')).toBeNull()
    requesterSummary.focus()
    await userEvent.keyboard('{Enter}')
    expect(screen.queryByRole('dialog')).toBeNull()
    await userEvent.keyboard(' ')
    expect(screen.queryByRole('dialog')).toBeNull()
    // escalated request carries the warning badge.
    expect(screen.getByText('Escalated')).toBeInTheDocument()
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
    await screen.findByText(NO_REVIEW)
    expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
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
    await screen.findByText(NO_REVIEW)
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

    await screen.findByText(NO_REVIEW)
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
    await screen.findByText(NO_REVIEW)
    await user.click(screen.getByRole('button', { name: /^approve$/i }))
    const dialog = await screen.findByRole('dialog')
    const reads = api.listApprovals.mock.calls.length
    await user.click(within(dialog).getByRole('button', { name: /^approve$/i }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
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

  it.each([
    ['complete', 1, [], 'success'],
    ['partial', 2, [{ provider: 'vault', reason: 'unreachable' }], 'warning'],
    ['no providers', 0, [], 'warning'],
  ] as const)(
    'reports a %s resync with the matching toast intent',
    async (_label, providers_configured, providers_failed, intent) => {
      api.listIdentities.mockResolvedValue({ items: [], has_more: false })
      api.syncRoster.mockResolvedValue({
        sources: 1,
        providers_configured,
        providers_failed,
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
      await waitFor(() => expect(toast[intent]).toHaveBeenCalledTimes(1))
      expect(
        toast[intent === 'success' ? 'warning' : 'success'],
      ).not.toHaveBeenCalled()
    },
  )
})

describe('GovernanceView — ?tab= deep link (console remake slice 4)', () => {
  afterEach(() => {
    window.history.replaceState({}, '', '/')
    authState.can = () => true
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

// Root, 09b capture review: Approve used the destructive red. Red is for Reject.
describe('the decision dialog styles', () => {
  it('Approve is the primary action; Reject stays red', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [pendingApproval],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText(NO_REVIEW)
    await user.click(screen.getByRole('button', { name: /^approve$/i }))
    let dialog = await screen.findByRole('dialog')
    const approve = within(dialog).getByRole('button', { name: /^approve$/i })
    expect(approve.className).toMatch(/bg-accent/)
    expect(approve.className).not.toMatch(/danger/)
    await user.click(within(dialog).getByRole('button', { name: /^cancel$/i }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await user.click(screen.getByRole('button', { name: /^reject$/i }))
    dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByRole('button', { name: /^reject$/i }).className,
    ).toMatch(/danger/)
  })
})
