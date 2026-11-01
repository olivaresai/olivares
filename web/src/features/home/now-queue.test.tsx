// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEEDS YOU on Now: the rail's own classification over the page Now already holds, plus
// the offered handoffs — and no sessions read of its own.
import { act, type ComponentProps, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
  DEFAULT_AUTH,
} from '@/test/intel'
import { governanceApi } from '@/features/governance/api'
import type { ApprovalDTO } from '@/features/governance/types'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import type { LiveDTO } from '@/features/sessions/types'
import { sessionsApi } from '@/features/sessions/api'
import * as communications from '@/features/communications/api'
import { useWorkspaceStore } from '@/stores/workspace'
import { FLAG_ARM_MS, NowQueue, useFlaggedRequest } from './now-queue'
import './i18n'

const auth = vi.hoisted(() => ({
  superadmin: true,
  owner: false,
  actor: 'user:me' as string | undefined,
  tenant: 'demo',
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    principal: {
      actor: auth.actor,
      kind: 'user',
      superadmin: auth.superadmin,
      grants: auth.owner
        ? [
            {
              tenant: DEFAULT_AUTH.activeTenant,
              role: 'owner',
              permissions: [],
            },
          ]
        : [],
    },
    isSuperadmin: auth.superadmin,
    activeTenant: auth.tenant,
    can: (p: string) => auth.can(p),
  }),
}))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => navigate,
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))

/** Estate's own wiring: the page holds the flagged request and hands it to the queue. */
function Queue(props: Omit<ComponentProps<typeof NowQueue>, 'flag'>) {
  const flag = useFlaggedRequest(props.approvals)
  return <NowQueue {...props} flag={flag} />
}

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-coder-7a3f',
    live_ref: '01a0b0b2-e581-71b8-a163-5f73c7ed6aaa',
    attribution: 'legacy',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: '2026-09-17T18:47:59Z',
    last_event_at: '2026-09-17T18:48:14Z',
    duration_seconds: 15,
    ...over,
  }
}

afterEach(() => {
  auth.superadmin = true
  auth.owner = false
  auth.actor = 'user:me'
  auth.tenant = 'demo'
  auth.can = () => true
  useWorkspaceStore.setState({ activeWorkspace: null } as never)
  navigate.mockReset()
  vi.restoreAllMocks()
})

describe('NowQueue — what needs a person', () => {
  it('lists the sessions that went silent or that nobody claimed, and nothing else', () => {
    const spy = vi.spyOn(sessionsApi, 'live')
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          live({
            session_ref: 's-active',
            live_ref: 'l1',
            cc_state: 'active',
            goal: 'running work',
          }),
          live({
            session_ref: 's-silent',
            live_ref: 'l2',
            cc_state: 'silent_evasion',
            goal: 'gone quiet',
          }),
          live({
            session_ref: 's-unclaimed',
            live_ref: 'l3',
            unclaimed: true,
            goal: 'nobody owns me',
          }),
          live({
            session_ref: 's-ended',
            live_ref: 'l4',
            cc_state: 'ended',
            goal: 'done',
          }),
        ]}
      />,
    )
    const rows = screen.getAllByTestId('now-needs-row')
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('gone quiet'),
      expect.stringContaining('nobody owns me'),
    ])
    for (const r of rows)
      expect(r.getAttribute('href')).toMatch(/^\/sessions\?/)
    expect(
      screen.getByRole('heading', { name: 'Needs you' }),
    ).toBeInTheDocument()
    // It reads no sessions of its own: the page comes from Now.
    expect(spy).not.toHaveBeenCalled()
    expectNoRawI18nKeys(document.body)
  })

  it('says so when nothing waits, instead of drawing an empty list', () => {
    renderIntel(
      <Queue state="ready" sessions={[live({ cc_state: 'active' })]} />,
    )
    expect(screen.queryByTestId('now-needs-row')).toBeNull()
    expect(screen.getByText('Nothing waits for you.')).toBeInTheDocument()
  })

  it('reports a failed sessions read as a failure, never as "nothing waits"', () => {
    renderIntel(<Queue state="unavailable" sessions={undefined} />)
    expect(
      screen.getByText('The sessions could not be read.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
  })

  it('adds a handoff offered to this operator in the selected workspace', async () => {
    auth.superadmin = false
    useWorkspaceStore.setState({ activeWorkspace: 'w1' } as never)
    const inbox = vi
      .spyOn(communications, 'listHandoffInbox')
      .mockResolvedValue({
        items: [
          {
            carrier: { delivery_id: 'd-1' },
            work_item: { id: 'wi-42' },
            handoff: {
              from: { ref: 'agent:reviewer' },
              state: 'offered',
              created_at: '2026-09-17T18:40:00Z',
            },
          },
        ],
      } as never)
    renderIntel(<Queue state="ready" sessions={[]} />)
    const row = await screen.findByTestId('now-needs-row')
    expect(row.getAttribute('href')).toBe(
      '/communications/handoffs?handoff=d-1',
    )
    expect(row.textContent).toContain('agent:reviewer')
    expect(inbox).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: 'w1', state: 'offered' }),
      expect.anything(),
      expect.anything(),
    )
  })

  it('the setup administrator (superadmin and owner here) is offered handoffs too (#503)', async () => {
    auth.owner = true
    useWorkspaceStore.setState({ activeWorkspace: 'w1' } as never)
    const inbox = vi
      .spyOn(communications, 'listHandoffInbox')
      .mockResolvedValue({ items: [] } as never)
    renderIntel(<Queue state="ready" sessions={[]} />)
    await vi.waitFor(() =>
      expect(inbox).toHaveBeenCalledWith(
        expect.objectContaining({ workspace_id: 'w1', state: 'offered' }),
        expect.anything(),
        expect.anything(),
      ),
    )
  })

  it('puts the pending request first, with its progress, and opens the queue from it', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[]}
        approvals={[
          {
            id: 'ap-1',
            action: 'mcp.tools/call',
            subject_kind: 'mcp_tool',
            subject_ref: 'github/create_issue',
            requested_by: 'user:ada',
            status: 'pending',
            required_approvals: 2,
            approve_count: 1,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    const row = screen.getByTestId('now-flagged-request')
    // The request itself opens beside the queue.
    expect(
      within(row).getByRole('link', { name: 'Details' }).getAttribute('href'),
    ).toBe('/permissions?tab=approvals&approval=ap-1')
    // The Approvals list's own words, not the engine's action name in code type.
    expect(row).toHaveTextContent('Mcp tools/call')
    expect(row).not.toHaveTextContent('mcp_tool github/create_issue')
    expect(row).toHaveTextContent('1 of 2')
    // An approval alone is something waiting: never "Nothing waits for you."
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
    expectNoRawI18nKeys(document.body)
  })

  it('says who waits for what in the words the Approvals list uses (HU, binary 12)', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[]}
        approvals={[
          {
            id: 'ap-3',
            action: 'sessions.provider.approval',
            subject_kind: 'session_run',
            subject_ref: '01a1029d-0000-7000-8000-000000000001',
            requested_by: 'user:ada',
            review: {
              tool: 'Command',
              text: "/bin/zsh -lc 'echo lane-b > b.txt'",
            },
            status: 'pending',
            required_approvals: 1,
            approve_count: 0,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    const row = screen.getByTestId('now-flagged-request')
    expect(row).toHaveTextContent('Command')
    expect(row).toHaveTextContent("/bin/zsh -lc 'echo lane-b > b.txt'")
    expect(row).not.toHaveTextContent('sessions.provider.approval')
    expect(row).not.toHaveTextContent('session_run')
    expectNoRawI18nKeys(document.body)
  })

  it('says what is not shown once, on a tool call stored without a review (#2269)', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[]}
        approvals={[
          {
            id: 'ap-4',
            action: 'claude.hook.tool',
            subject_kind: 'claude.tool',
            subject_ref: 'Bash',
            requested_by: 'user:ada',
            status: 'pending',
            required_approvals: 1,
            approve_count: 0,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    expect(screen.getByTestId('now-flagged-request')).toBeInTheDocument()
    expect(
      screen.getAllByText('This request does not include what will run.'),
    ).toHaveLength(1)
  })

  it('serves a role that reads approvals but not live sessions', () => {
    const spy = vi.spyOn(sessionsApi, 'live')
    renderIntel(
      <Queue
        state="ready"
        sessions={undefined}
        sessionsReadable={false}
        approvals={[
          {
            id: 'ap-2',
            action: 'deploy.promote',
            requested_by: 'user:grace',
            status: 'pending',
            required_approvals: 1,
            approve_count: 0,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    expect(screen.getByTestId('now-flagged-request')).toHaveTextContent(
      'Deploy promote',
    )
    // A request a person raised expects no command: no "does not include" note (#2269).
    expect(
      screen.queryByText('This request does not include what will run.'),
    ).toBeNull()
    // Nothing about sessions it may not read: no failure line, no session read.
    expect(screen.queryByText('The sessions could not be read.')).toBeNull()
    expect(spy).not.toHaveBeenCalled()
  })

  it('says a failed approvals read, and never "nothing waits" over it', () => {
    renderIntel(
      <Queue state="ready" sessions={[]} approvalsState="unavailable" />,
    )
    expect(
      screen.getByText('Approvals could not be loaded.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
  })

  const request = (over: Partial<ApprovalDTO>): ApprovalDTO => ({
    id: 'ap-x',
    action: 'claude.hook.tool',
    subject_kind: 'claude.tool',
    requested_by: 'user:marc',
    status: 'pending',
    required_approvals: 1,
    approve_count: 0,
    reject_count: 0,
    escalated: false,
    ...over,
  })
  const MIGRATION = request({
    id: 'ap-migrate',
    review: { tool: 'Command', text: 'pnpm db:migrate --env payments-staging' },
    policy_ref: 'pol-migrations',
  })
  /** The card ignores Approve until it has been on screen for FLAG_ARM_MS. */
  const armed = () =>
    act(() => new Promise((done) => setTimeout(done, FLAG_ARM_MS + 50)))
  const NETWORK = request({
    id: 'ap-network',
    review: { tool: 'Network', text: 'registry.terraform.io' },
  })

  it('flags the first request: what will run, the rule that asked, and Approve in one click', async () => {
    vi.spyOn(governanceApi, 'getPolicy').mockResolvedValue({
      id: 'pol-migrations',
      name: 'Database migrations need a second person',
    } as never)
    const decide = vi
      .spyOn(governanceApi, 'decide')
      .mockResolvedValue({ ...MIGRATION, status: 'approved' })
    const user = userEvent.setup()
    renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION, NETWORK]} />,
    )
    const flagged = screen.getByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('pnpm db:migrate --env payments-staging')
    expect(
      await within(flagged).findByText(
        'Matched: Database migrations need a second person',
      ),
    ).toBeInTheDocument()
    // The others are quiet rows, with no decision on them.
    const rows = screen.getAllByTestId('now-approval-row')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('registry.terraform.io')
    expect(within(rows[0]!).queryByRole('button')).toBeNull()

    await armed()
    await user.click(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    )
    expect(decide).toHaveBeenCalledTimes(1)
    expect(decide).toHaveBeenCalledWith('ap-migrate', { decision: 'approve' })
    // One click: no confirmation stands between the reviewed command and the decision.
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  // the regression: while the decision is sent, Approve once shows it is working.
  it('shows Approve once working while the decision is sent', async () => {
    vi.spyOn(governanceApi, 'getPolicy').mockResolvedValue({
      id: 'pol-migrations',
      name: 'Database migrations need a second person',
    } as never)
    vi.spyOn(governanceApi, 'decide').mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    renderIntel(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    const flagged = screen.getByTestId('now-flagged-request')
    await armed()
    await user.click(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    )
    const working = await within(flagged).findByRole('button', {
      name: 'Approving…',
    })
    expect(working).toBeDisabled()
    expect(working.querySelector('.animate-spin')).not.toBeNull()
  })

  it('Deny asks for a reason first, in the deny dialog of the queue', async () => {
    const decide = vi.spyOn(governanceApi, 'decide')
    const user = userEvent.setup()
    renderIntel(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    await user.click(screen.getByRole('button', { name: 'Deny' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(decide).not.toHaveBeenCalled()
  })

  it('a request this person may not decide is a quiet row, never the flag', () => {
    auth.actor = 'user:marc'
    renderIntel(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    expect(screen.queryByTestId('now-flagged-request')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
    expect(screen.getByTestId('now-approval-row')).toHaveAttribute(
      'href',
      '/permissions?tab=approvals&approval=ap-migrate',
    )
  })

  it('flags the first request this person may decide, not the first in the queue', async () => {
    const own = request({
      id: 'ap-own',
      requested_by: 'user:me',
      review: { tool: 'Command', text: 'make release' },
    })
    const decide = vi
      .spyOn(governanceApi, 'decide')
      .mockResolvedValue({ ...NETWORK, status: 'approved' })
    const user = userEvent.setup()
    renderIntel(
      <Queue state="ready" sessions={[]} approvals={[own, NETWORK]} />,
    )
    const flagged = screen.getByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('registry.terraform.io')
    expect(screen.getByTestId('now-approval-row')).toHaveTextContent(
      'make release',
    )
    await armed()
    await user.click(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    )
    expect(decide).toHaveBeenCalledWith('ap-network', { decision: 'approve' })
  })

  it('a poll never swaps another request in under the hand: the one shown stays and says it left', async () => {
    const decide = vi.spyOn(governanceApi, 'decide')
    const { rerender } = renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION, NETWORK]} />,
    )
    await armed()
    // A colleague decided the migration: the next poll no longer holds it.
    rerender(<Queue state="ready" sessions={[]} approvals={[NETWORK]} />)
    await armed()
    const flagged = screen.getByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('pnpm db:migrate --env payments-staging')
    expect(flagged).toHaveTextContent(
      'This request is no longer waiting for a decision.',
    )
    expect(
      within(flagged).queryByRole('button', { name: 'Approve once' }),
    ).toBeNull()
    expect(screen.getByTestId('now-approval-row')).toHaveTextContent(
      'registry.terraform.io',
    )
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
    // The request itself still opens beside the queue.
    expect(
      within(flagged).getByRole('link', { name: 'Details' }),
    ).toHaveAttribute('href', '/permissions?tab=approvals&approval=ap-migrate')
    expect(decide).not.toHaveBeenCalled()
  })

  it('a request that left the queue moves on only when the person closes it', async () => {
    const user = userEvent.setup()
    const { rerender } = renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION, NETWORK]} />,
    )
    rerender(<Queue state="ready" sessions={[]} approvals={[NETWORK]} />)
    await user.click(
      within(screen.getByTestId('now-flagged-request')).getByRole('button', {
        name: 'Close',
      }),
    )
    const flagged = screen.getByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('registry.terraform.io')
    expect(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    ).toBeInTheDocument()
  })

  it('the last request leaving the queue is never said as "nothing waits" under its card', async () => {
    const user = userEvent.setup()
    const { rerender } = renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION]} />,
    )
    rerender(<Queue state="ready" sessions={[]} approvals={[]} />)
    expect(screen.getByTestId('now-flagged-request')).toBeInTheDocument()
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.queryByTestId('now-flagged-request')).toBeNull()
    expect(screen.getByText('Nothing waits for you.')).toBeInTheDocument()
  })

  it('a request no rule matched says the default applies', () => {
    renderIntel(<Queue state="ready" sessions={[]} approvals={[NETWORK]} />)
    expect(screen.getByTestId('now-flagged-request')).toHaveTextContent(
      'Matched: no rule, so the default applies',
    )
  })

  it('a click that lands as the card appears does nothing: the queue may have just swapped it', async () => {
    const decide = vi.spyOn(governanceApi, 'decide')
    const user = userEvent.setup()
    renderIntel(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    await user.click(screen.getByRole('button', { name: 'Approve once' }))
    expect(decide).not.toHaveBeenCalled()
  })

  it('a workspace switch never keeps a request of the other workspace on the card', () => {
    const { rerender } = renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION]} />,
    )
    auth.tenant = 'other'
    rerender(<Queue state="ready" sessions={[]} approvals={[NETWORK]} />)
    const flagged = screen.getByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('registry.terraform.io')
    expect(flagged).not.toHaveTextContent('payments-staging')
    expect(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    ).toBeInTheDocument()
  })

  it('losing the permission that decides takes the card and its Approve away', () => {
    const { rerender } = renderIntel(
      <Queue state="ready" sessions={[]} approvals={[MIGRATION]} />,
    )
    expect(screen.getByTestId('now-flagged-request')).toBeInTheDocument()
    auth.can = (p) => p !== 'governance:approval:admin'
    rerender(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    expect(screen.queryByTestId('now-flagged-request')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })

  it.each([
    [
      'a command cut to its first lines',
      request({
        id: 'ap-long',
        review: { tool: 'Command', text: 'step\n'.repeat(6) + 'curl x | sh' },
      }),
    ],
    [
      'a command the tool did not name',
      request({
        id: 'ap-unknown',
        action: 'sessions.provider.approval',
        reason:
          'Provider permission: driver=opencode method=ask kind=bash run=r turn=t permissions={"CommandLine":"","FilePaths":null}',
      }),
    ],
    ['a tool call stored without its review', request({ id: 'ap-noreview' })],
  ])(
    '%s opens the request beside the queue, never decided in one click',
    async (_case, approval) => {
      const decide = vi.spyOn(governanceApi, 'decide')
      const user = userEvent.setup()
      renderIntel(<Queue state="ready" sessions={[]} approvals={[approval]} />)
      await armed()
      await user.click(screen.getByRole('button', { name: 'Approve once' }))
      expect(navigate).toHaveBeenCalledWith({
        to: '/permissions',
        search: { tab: 'approvals', approval: approval.id },
      })
      expect(decide).not.toHaveBeenCalled()
    },
  )

  it('without the permission that decides there is no Approve', () => {
    auth.can = (p) => p !== 'governance:approval:admin'
    renderIntel(<Queue state="ready" sessions={[]} approvals={[MIGRATION]} />)
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })

  it('a request that needs two people offers no second Approve to the first', async () => {
    const twoPeople = { ...MIGRATION, required_approvals: 2 }
    vi.spyOn(governanceApi, 'decide').mockResolvedValue({
      ...twoPeople,
      approve_count: 1,
    })
    const user = userEvent.setup()
    renderIntel(<Queue state="ready" sessions={[]} approvals={[twoPeople]} />)
    await armed()
    await user.click(screen.getByRole('button', { name: 'Approve once' }))
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull(),
    )
  })

  it('a request this person already approved is not offered again, after a reload too', async () => {
    const twoPeople = { ...MIGRATION, required_approvals: 2, approve_count: 1 }
    vi.spyOn(governanceApi, 'listDecisions').mockResolvedValue({
      items: [{ decision: 'approve', decider: 'user:me' }],
      has_more: false,
    })
    renderIntel(<Queue state="ready" sessions={[]} approvals={[twoPeople]} />)
    expect(screen.getByTestId('now-flagged-request')).toBeInTheDocument()
    // Settled: the card moves on and the request waits for the others as a quiet row.
    await waitFor(() =>
      expect(screen.queryByTestId('now-flagged-request')).toBeNull(),
    )
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
    expect(screen.getByTestId('now-approval-row')).toHaveTextContent('1 of 2')
  })
})
describe('NowQueue — the person’s marks on a run take a session out (#2901)', () => {
  const needsPerson = {
    code: 'repeated_policy_denials',
    consecutive_denials: 3,
    total_denials: 3,
    reasons: ['Bash'],
  }
  const run = (over: Record<string, unknown>) =>
    ({
      run_ref: 'r',
      state: 'running',
      claude_session_id: 'sess-x',
      needs_person: needsPerson,
      ...over,
    }) as never

  it('leaves out a session the person archived, snoozed, marked done or sent to an approval, and counts what is left', async () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          live({
            session_ref: 'sess-archived',
            live_ref: 'l-ar',
            cc_state: 'silent_evasion',
            goal: 'archived one',
          }),
          live({
            session_ref: 'sess-snoozed',
            live_ref: 'l-s',
            cc_state: 'silent_evasion',
            goal: 'snoozed one',
          }),
          live({
            session_ref: 'sess-done',
            live_ref: 'l-d',
            cc_state: 'silent_evasion',
            goal: 'done one',
          }),
          live({
            session_ref: 'sess-needs',
            live_ref: 'l-n',
            cc_state: 'silent_evasion',
            goal: 'still needs me',
          }),
          live({
            session_ref: 'sess-approval',
            live_ref: 'l-a',
            cc_state: 'active',
            goal: 'waits on approval',
          }),
        ]}
        runs={[
          run({
            run_ref: 'r-ar',
            claude_session_id: 'sess-archived',
            archived_at: '2026-10-20T09:00:00Z',
          }),
          run({
            run_ref: 'r-s',
            claude_session_id: 'sess-snoozed',
            snoozed_until: '2030-01-02T09:00:00Z',
          }),
          run({ run_ref: 'r-d', claude_session_id: 'sess-done', done: true }),
          run({
            run_ref: 'r-a',
            claude_session_id: 'sess-approval',
            state: 'waiting_approval',
            approval_ref: 'ap-9',
            needs_person: undefined,
          }),
          // A run the page does not hold is not a row Now lists today.
          run({
            run_ref: 'r-p',
            claude_session_id: 'sess-not-on-page',
            state: 'pending',
            needs_person: undefined,
          }),
        ]}
        approvals={[
          {
            id: 'ap-9',
            action: 'sessions.provider.approval',
            subject_kind: 'session_run',
            subject_ref: 'r-a',
            requested_by: 'user:ada',
            status: 'pending',
            required_approvals: 1,
            approve_count: 0,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    // The one listed approval is the flagged card, not a second session row.
    await screen.findByTestId('now-flagged-request')
    const rows = screen.getAllByTestId('now-needs-row')
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('still needs me'),
    ])
    expect(screen.queryByText('waits on approval')).toBeNull()
    // The count is what the section lists: one session and one approval.
    expect(
      within(screen.getByTestId('now-needs')).getByText('2'),
    ).toBeInTheDocument()
  })

  it('keeps a session waiting on an approval the queue does not list, so it is not lost', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          live({
            session_ref: 'sess-gone',
            live_ref: 'l-g',
            cc_state: 'active',
            goal: 'approval not listed',
          }),
        ]}
        runs={[
          run({
            run_ref: 'r-g',
            claude_session_id: 'sess-gone',
            state: 'waiting_approval',
            approval_ref: 'ap-not-listed',
            needs_person: undefined,
          }),
        ]}
        approvals={[]}
      />,
    )
    expect(screen.getAllByTestId('now-needs-row')).toHaveLength(1)
    expect(screen.getByTestId('now-needs-row')).toHaveTextContent(
      'approval not listed',
    )
  })

  it('shows a session that needs the person even after eight rows that are not its own', async () => {
    // Eight sessions wait on approvals the queue lists: their rows are the approval rows.
    const waiting = Array.from({ length: 8 }, (_, i) => i + 1)
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          ...waiting.map((i) =>
            live({
              session_ref: `sess-ap-${i}`,
              live_ref: `l-ap-${i}`,
              cc_state: 'active',
            }),
          ),
          live({
            session_ref: 'sess-needs',
            live_ref: 'l-needs',
            cc_state: 'silent_evasion',
            goal: 'still needs me after the cap',
          }),
        ]}
        runs={waiting.map((i) =>
          run({
            run_ref: `r-ap-${i}`,
            claude_session_id: `sess-ap-${i}`,
            state: 'waiting_approval',
            approval_ref: `ap-${i}`,
            needs_person: undefined,
          }),
        )}
        approvals={waiting.map((i) => ({
          id: `ap-${i}`,
          action: 'deploy.promote',
          requested_by: 'user:ada',
          status: 'pending',
          required_approvals: 1,
          approve_count: 0,
          reject_count: 0,
          escalated: false,
        }))}
      />,
    )
    // One approval leads as the flagged card; the other seven are quiet rows.
    await screen.findByTestId('now-flagged-request')
    expect(screen.getAllByTestId('now-approval-row')).toHaveLength(7)
    expect(screen.getByTestId('now-needs-row')).toHaveTextContent(
      'still needs me after the cap',
    )
  })

  it.each([
    [
      'may not read approvals',
      () => {
        auth.can = (p) => p !== 'governance:approval:read'
      },
    ],
    [
      'cannot open the Permissions view',
      () => {
        auth.can = (p) => p !== 'governance:identity:read'
      },
    ],
  ])(
    'opens a waiting session on /sessions for a principal who %s',
    (_, deny) => {
      deny()
      renderIntel(
        <Queue
          state="ready"
          sessions={[
            live({
              session_ref: 'sess-g',
              live_ref: 'l-g',
              cc_state: 'active',
              goal: 'waits on an approval I cannot open',
            }),
          ]}
          runs={[
            run({
              run_ref: 'r-g',
              claude_session_id: 'sess-g',
              state: 'waiting_approval',
              approval_ref: 'ap-1',
              needs_person: undefined,
            }),
          ]}
        />,
      )
      expect(screen.getByTestId('now-needs-row').getAttribute('href')).toMatch(
        /^\/sessions\?/,
      )
    },
  )

  it('keeps a session whose live row is an echo of a run, as the rail and the top bar do', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          live({
            session_ref: 'r-e',
            live_ref: 'l-e',
            cc_state: 'silent_evasion',
            goal: 'echo of a run',
          }),
        ]}
        runs={[run({ run_ref: 'r-e', claude_session_id: 'sess-other' })]}
      />,
    )
    expect(screen.getAllByTestId('now-needs-row')).toHaveLength(1)
    expect(screen.getByTestId('now-needs-row').getAttribute('href')).toMatch(
      /^\/sessions\?/,
    )
    expect(
      within(screen.getByTestId('now-needs')).getByText('1'),
    ).toBeInTheDocument()
  })
})

describe('NowQueue — the state vocabulary', () => {
  it('says each row as the Sessions list does: the mark, the word and its age', () => {
    renderIntel(
      <Queue
        state="ready"
        sessions={[
          live({
            session_ref: 's-silent',
            live_ref: 'l2',
            cc_state: 'silent_evasion',
            goal: 'gone quiet',
          }),
        ]}
      />,
    )
    const row = screen.getByTestId('now-needs-row')
    const status = within(row).getByTestId('now-needs-row-status')
    expect(status).toHaveAttribute('data-status', 'needs-you')
    expect(status).toHaveTextContent(/^Needs you \d+[mhd]$/)
    expect(row).toHaveAccessibleName(/Needs you/)
  })
})
