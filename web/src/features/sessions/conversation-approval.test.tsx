// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import type { ApprovalDTO } from '@/features/governance/types'
import { ConversationApproval } from './conversation-approval'
import './i18n'
import '@/features/governance/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 't1',
  principal: { actor: 'user:reviewer', kind: 'user' },
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  getPolicy: vi.fn(),
  decide: vi.fn(),
}))
vi.mock('@/features/governance/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/governance/api')>()
  return { ...actual, governanceApi: api }
})

// What the transcripts' viewports show of every card: the observers' last report.
const reports: ((ratio: number) => void)[] = []
const observed = (ratio: number) => reports.forEach((report) => report(ratio))
class FakeObserver {
  constructor(cb: IntersectionObserverCallback) {
    reports.push((ratio) =>
      cb(
        [{ intersectionRatio: ratio } as IntersectionObserverEntry],
        this as unknown as IntersectionObserver,
      ),
    )
  }
  observe() {}
  unobserve() {}
  disconnect() {}
}

const RUN: RunDTO = {
  run_ref: 'run-a',
  name: 'Reconcile invoice export',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'codex',
  pending_approval_ref: 'apr-1',
  reaches: {
    folders: [{ path: '/srv/payments-api', access: 'write' }],
    hosts: [],
  },
}

const APPROVAL: ApprovalDTO = {
  id: 'apr-1',
  action: 'codex.tool',
  subject_kind: 'codex.tool',
  requested_by: 'session:osn_a',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  review: { tool: 'Command', text: 'make migrate' },
  reason: 'The staging schema needs the reference column first.',
  policy_ref: 'pol-1',
  escalated: false,
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  api.getApproval.mockResolvedValue(APPROVAL)
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
  api.getPolicy.mockResolvedValue({ id: 'pol-1', name: 'Migrations need two' })
  auth.can = () => true
  reports.length = 0
  vi.stubGlobal('IntersectionObserver', FakeObserver)
})
afterEach(() => vi.unstubAllGlobals())

describe('ConversationApproval — the request, answered where it is read', () => {
  it('sends session scope only after that button is chosen on a supported request', async () => {
    api.getApproval.mockResolvedValue({
      ...APPROVAL,
      session_scope_allowed: true,
    })
    api.decide.mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    wrap(<ConversationApproval run={RUN} />)
    await user.click(
      await screen.findByRole('button', { name: 'Approve for this session' }),
    )
    expect(api.decide).toHaveBeenCalledWith(APPROVAL.id, {
      decision: 'approve',
      scope: 'session',
    })
    expect(screen.getByRole('button', { name: 'Approving…' })).toBeDisabled()
  })

  it('says what runs, why, the rule, the reach and who asks, with Approve once and Deny', async () => {
    wrap(<ConversationApproval run={RUN} />)
    const card = await screen.findByRole('region', { name: 'Approval needed' })
    expect(await within(card).findByText('make migrate')).toBeInTheDocument()
    expect(
      within(card).getByText(
        'The staging schema needs the reference column first.',
      ),
    ).toBeInTheDocument()
    expect(
      await within(card).findByText('Migrations need two'),
    ).toBeInTheDocument()
    expect(
      within(card).getByText('(write)', { exact: false }),
    ).toBeInTheDocument()
    expect(
      within(card).getByText('Codex · The session itself'),
    ).toBeInTheDocument()
    for (const label of ['Why', 'Rule', 'Reach', 'Requested by'])
      expect(within(card).getByText(label)).toBeInTheDocument()
    expect(
      within(card).getByRole('button', { name: 'Approve once' }),
    ).toHaveAttribute('aria-keyshortcuts', 'Alt+A')
    expect(within(card).getByRole('button', { name: 'Deny' })).toBeEnabled()
  })

  it.each(['read', 'write', 'unknown'] as const)(
    'renders a folder with %s rights alongside hosts and incomplete reach',
    async (access) => {
      wrap(
        <ConversationApproval
          run={{
            ...RUN,
            reaches: {
              folders: [{ path: '/srv/payments-api', access }],
              hosts: ['api.example.test'],
              reason: 'unavailable',
            },
          }}
        />,
      )
      const card = await screen.findByRole('region', {
        name: 'Approval needed',
      })
      await within(card).findByText('make migrate')
      const list = within(card).getByRole('list')
      expect(within(list).getAllByRole('listitem')).toHaveLength(3)
      expect(list).toHaveTextContent('/srv/payments-api')
      expect(list).toHaveTextContent('api.example.test')
      expect(list).toHaveTextContent('more that could not be read')
      expect(list).toHaveTextContent(
        access === 'read'
          ? '(read only)'
          : access === 'write'
            ? '(write)'
            : '(access unknown)',
      )
    },
  )

  it('says when the reach is unknown', async () => {
    wrap(<ConversationApproval run={{ ...RUN, reaches: undefined }} />)
    expect(await screen.findByText('Not reported')).toBeInTheDocument()
  })

  it('approves once in place', async () => {
    const user = userEvent.setup()
    api.decide.mockResolvedValue({ ...APPROVAL, status: 'approved' })
    wrap(<ConversationApproval run={RUN} />)
    await user.click(
      await screen.findByRole('button', { name: 'Approve once' }),
    )
    expect(api.decide).toHaveBeenCalledWith('apr-1', { decision: 'approve' })
  })

  it('says a denied request was denied, never that it was approved', async () => {
    const user = userEvent.setup()
    api.decide.mockResolvedValue({ ...APPROVAL, status: 'rejected' })
    wrap(<ConversationApproval run={RUN} />)
    await user.click(await screen.findByRole('button', { name: 'Deny' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Deny this request?',
    })
    api.getApproval.mockResolvedValue({ ...APPROVAL, status: 'rejected' })
    await user.click(within(dialog).getByRole('button', { name: 'Deny' }))
    expect(api.decide).toHaveBeenCalledWith('apr-1', { decision: 'reject' })
    const card = screen.getByRole('region', { name: 'Approval needed' })
    expect(await within(card).findByText('Denied')).toBeInTheDocument()
    expect(within(card).queryByText('Approved')).toBeNull()
    expect(
      within(card).queryByRole('button', { name: 'Approve once' }),
    ).toBeNull()
  })

  it('takes Alt+A only while the whole request is in view and no dialog is open', async () => {
    const user = userEvent.setup()
    api.decide.mockResolvedValue({ ...APPROVAL, status: 'approved' })
    wrap(<ConversationApproval run={RUN} />)
    await screen.findByRole('button', { name: 'Approve once' })
    act(() => observed(0.6))
    await user.keyboard('{Alt>}a{/Alt}')
    expect(api.decide).not.toHaveBeenCalled()
    act(() => observed(1))
    const layer = document.createElement('div')
    layer.setAttribute('role', 'dialog')
    layer.setAttribute('data-state', 'open')
    document.body.append(layer)
    await user.keyboard('{Alt>}a{/Alt}')
    expect(api.decide).not.toHaveBeenCalled()
    layer.remove()
    await user.keyboard('{Alt>}a{/Alt}')
    await waitFor(() =>
      expect(api.decide).toHaveBeenCalledWith('apr-1', {
        decision: 'approve',
      }),
    )
  })

  it('takes Alt+A in the column that has the keys, never in the other one', async () => {
    // Two conversations side by side, both requests fully in view; the person works in
    // the beside column.
    const user = userEvent.setup()
    const beside = { ...RUN, run_ref: 'run-b', pending_approval_ref: 'apr-2' }
    api.getApproval.mockImplementation(async (id: string) => ({
      ...APPROVAL,
      id,
    }))
    api.decide.mockResolvedValue({ ...APPROVAL, status: 'approved' })
    wrap(
      <>
        <section data-focused="false">
          <ConversationApproval run={RUN} />
        </section>
        <section data-focused="true">
          <ConversationApproval run={beside} />
          <textarea aria-label="beside composer" />
        </section>
      </>,
    )
    await waitFor(() =>
      expect(
        screen.getAllByRole('button', { name: 'Approve once' }),
      ).toHaveLength(2),
    )
    act(() => observed(1))
    await user.click(screen.getByRole('textbox', { name: 'beside composer' }))
    await user.keyboard('{Alt>}a{/Alt}')
    await waitFor(() =>
      expect(api.decide).toHaveBeenCalledWith('apr-2', {
        decision: 'approve',
      }),
    )
    expect(api.decide).toHaveBeenCalledTimes(1)
  })

  it('says a person who already approved that it waits for another approver', async () => {
    api.getApproval.mockResolvedValue({ ...APPROVAL, required_approvals: 2 })
    api.listDecisions.mockResolvedValue({
      items: [{ decision: 'approve', decider: 'user:reviewer' }],
      has_more: false,
    })
    wrap(<ConversationApproval run={RUN} />)
    expect(
      await screen.findByText(
        'You approved this request. It waits for another approver.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })

  it('offers no decision to a person the engine would refuse', async () => {
    auth.can = (p: string) => p !== 'governance:approval:admin'
    wrap(<ConversationApproval run={RUN} />)
    await screen.findByRole('region', { name: 'Approval needed' })
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
  })

  it('shows nothing for a run that waits on no request, or to a person who may not read it', () => {
    const { unmount } = wrap(
      <ConversationApproval
        run={{ ...RUN, pending_approval_ref: undefined }}
      />,
    )
    expect(screen.queryByRole('region', { name: 'Approval needed' })).toBeNull()
    unmount()
    auth.can = (p: string) => p !== 'governance:approval:read'
    wrap(<ConversationApproval run={RUN} />)
    expect(screen.queryByRole('region', { name: 'Approval needed' })).toBeNull()
    expect(api.getApproval).not.toHaveBeenCalled()
  })
})

it('does not invent Why from the Claude hook scope when the tool gave no purpose', async () => {
  api.getApproval.mockResolvedValue({
    ...APPROVAL,
    subject_kind: 'claude.tool',
    reason: 'Claude Code requests Bash\ncommand: make migrate',
  })
  wrap(<ConversationApproval run={RUN} />)
  await screen.findByText('make migrate')
  expect(screen.queryByText('Why')).not.toBeInTheDocument()
})
