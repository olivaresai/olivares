// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SC sweep on 09b (Root, URGENT): a reviewer could approve without seeing what will run.
// The queue showed claude.tool.use, a #plan/singleuse reference and session:osn_…; the
// Approve dialog only audit wording. These cases hold the review to what the engine
// stored (`review`, PEP), and never to a cut-up `reason`.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApprovalDTO } from './types'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const auth = vi.hoisted(() => ({
  activeTenant: 't1',
  can: (_permission: string): boolean => true,
  principal: { actor: 'user:reviewer', kind: 'user' },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
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

import { agentOpsApi } from '@/features/agentops/api'
import { sessionsApi } from '@/features/sessions/api'
import GovernanceView from './governance-view'
import {
  approvalCommandUnknown,
  approvalExpectsCommand,
} from './approval-preview'
import { ApprovalPreview, AskedByName } from './approval-preview'
import { consoleApi } from '@/features/console/api'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

/** The queue beside the open request, and the open request itself. */
const queue = () => screen.getByRole('list', { name: 'Approval queue' })
const panel = () => screen.getByRole('complementary', { name: 'Request' })
const rowOf = (node: HTMLElement) =>
  node.closest('[data-approval-row]') as HTMLElement

/** The queue's read and the engine's read of each request it lists. */
function serve(items: ApprovalDTO[]) {
  api.listApprovals.mockResolvedValue({ items, has_more: false })
  api.getApproval.mockImplementation((id: string) =>
    Promise.resolve(items.find((a) => a.id === id)),
  )
}

// The shape of the 09b hook request (SC 086), with PEP's review field.
const hookRequest: ApprovalDTO = {
  id: 'apr-1',
  action: 'claude.tool.use',
  subject_kind: 'claude.tool',
  subject_ref:
    '01a0fc45-0859-79a3-b4ce-6e2872ecc03d#plan=690930c13aa15c5d:singleuse-32c58af284bea260',
  requested_by: 'session:osn_01a0fc45',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  reason:
    'governed actuation approval opened by the control-plane approval bridge. action=claude.tool.use subject=claude.tool:x | Claude Code requests Bash\ncommand: rm -rf build',
  escalated: false,
  review: {
    tool: 'Bash',
    text: 'rm -rf build && npm test -- --token [secret NPM_TOKEN]',
  },
}

beforeEach(() => {
  auth.can = () => true
  // The sessions and runs reads are spied per test: no call carries over.
  vi.restoreAllMocks()
  for (const fn of Object.values(api)) fn.mockReset()
  for (const fn of [
    'listPolicies',
    'listIdentities',
    'listGroups',
    'listBindings',
  ] as const)
    api[fn].mockResolvedValue({ items: [], has_more: false })
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
  vi.spyOn(sessionsApi, 'live').mockResolvedValue({
    items: [
      {
        session_ref: 'osn_01a0fc45',
        run_ref: 'run-1',
        live_ref: 'lr-1',
        cc_state: 'active',
      } as never,
    ],
    has_more: false,
  })
  // The waiting run names this request (pending_approval_ref), as the rail's Needs you does.
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [
      {
        run_ref: 'run-1',
        name: 'Fix the build',
        workspace_ref: 'ws-1',
        state: 'running',
        pending_approval_ref: 'apr-1',
      } as never,
    ],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'getRun').mockResolvedValue({
    run_ref: 'run-1',
    name: 'Fix the build',
    workspace_ref: 'ws-1',
  } as never)
  vi.spyOn(agentOpsApi, 'getWorkspace').mockResolvedValue({
    workspace_ref: 'ws-1',
    root_path: '/srv/app',
  } as never)
})

describe('Approvals: what will run, before Approve', () => {
  it('the queue leads with the tool, the session, what will run and the folder; references are not in the row', async () => {
    serve([hookRequest])
    wrap(<GovernanceView />)
    const row = rowOf(await screen.findByText('Bash · Fix the build'))
    expect(
      within(row).getByText(
        'rm -rf build && npm test -- --token [secret NPM_TOKEN]',
      ),
    ).toBeInTheDocument()
    expect(await within(row).findByText('/srv/app')).toBeInTheDocument()
    expect(within(row).getByText('The session itself')).toBeInTheDocument()
    expect(within(row).queryByText('claude.tool.use')).toBeNull()
    expect(within(row).queryByText(/#plan=/)).toBeNull()
  })

  it('the open request shows what will run before Approve once, and the Deny dialog shows it above the note, whole, with the references behind Details', async () => {
    const user = userEvent.setup()
    serve([hookRequest])
    wrap(<GovernanceView />)
    await screen.findByText('Bash · Fix the build')
    const open = within(panel()).getByRole('region', { name: 'What will run' })
    expect(
      within(open).getByText(
        'rm -rf build && npm test -- --token [secret NPM_TOKEN]',
      ).tagName,
    ).toBe('PRE')
    expect(
      open.compareDocumentPosition(
        within(panel()).getByRole('button', { name: 'Approve once' }),
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    await user.click(screen.getByRole('button', { name: /^deny$/i }))
    const dialog = await screen.findByRole('dialog')
    const preview = within(dialog).getByRole('region', {
      name: 'What will run',
    })
    const text = within(preview).getByText(
      'rm -rf build && npm test -- --token [secret NPM_TOKEN]',
    )
    expect(text.tagName).toBe('PRE')
    expect(
      await within(preview).findByText('Fix the build'),
    ).toBeInTheDocument()
    expect(within(preview).getByText('/srv/app')).toBeInTheDocument()
    expect(within(preview).getByText('Bash')).toBeInTheDocument()
    // The references are in the dialog, behind Details, and come after the preview.
    const details = within(dialog).getByText('Details').closest('details')!
    expect(details).not.toHaveAttribute('open')
    expect(within(details).getByText('claude.tool.use')).toBeInTheDocument()
    expect(
      preview.compareDocumentPosition(
        within(dialog).getByLabelText(/^note$/i),
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('without the review field it says so in one line, and does not cut the reason up', async () => {
    const { review: _none, ...without } = hookRequest
    void _none
    serve([without])
    wrap(<GovernanceView />)
    await screen.findAllByText('This request does not include what will run.')
    const row = rowOf(
      within(queue()).getByText('This request does not include what will run.'),
    )
    expect(within(row).queryByText(/rm -rf build/)).toBeNull()
    expect(await within(row).findByText('Fix the build')).toBeInTheDocument()
  })

  // HU-R32 (RC10): OpenCode's permission request carries no command and no path, and the
  // review showed the raw struct as if it were the command. The reason is HU's, verbatim.
  it('says the command is not shown for a request with an empty command and no paths', async () => {
    const user = userEvent.setup()
    const { review: _none, ...base } = hookRequest
    void _none
    const blind: ApprovalDTO = {
      ...base,
      action: 'sessions.provider.approval',
      subject_kind: 'sessions.provider',
      reason:
        'Provider permission: driver=opencode method=session/request_permission kind=tool_call_permission\n' +
        'run=01a0fe5c-b4d7-745c-ad0d-0fcf7a7abe98 turn=4\n' +
        'permissions={"Permissions":["once","always","reject"],"CommandLine":"","FilePaths":null}',
    }
    serve([blind])
    wrap(<GovernanceView />)
    await screen.findAllByText('Command not shown for this tool yet')
    const row = rowOf(
      within(queue()).getByText('Command not shown for this tool yet'),
    )
    expect(row.querySelector('[data-slot="approval-review"]')).toBeNull()
    // One line says it, not two (#2269).
    expect(
      within(row).queryByText('This request does not include what will run.'),
    ).toBeNull()
    // Approve once and Deny are unchanged.
    expect(
      within(panel()).getByRole('button', { name: /^approve once$/i }),
    ).toBeEnabled()
    expect(
      within(panel()).getByRole('button', { name: /^deny$/i }),
    ).toBeEnabled()
    await user.click(within(panel()).getByRole('button', { name: /^deny$/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText('Command not shown for this tool yet'),
    ).toBeInTheDocument()
    expect(dialog.querySelector('[data-slot="approval-review"]')).toBeNull()
    expect(
      within(dialog).queryByText(
        'This request does not include what will run.',
      ),
    ).toBeNull()
  })

  // The engine's own shape on RC10: a one-line reason, and a review that lists the
  // permission options (tool "Provider permission"), which is not what will run.
  it('labels the RC10 shape too: options in the review, no command in the scope', async () => {
    const rc10: ApprovalDTO = {
      ...hookRequest,
      action: 'sessions.provider.approval',
      subject_kind: 'session_run',
      reason:
        'Provider permission: driver=opencode method=session/request_permission kind=tool_call_permission run=r1 turn=4 permissions={"Permissions":["once","always","reject"],"CommandLine":"","FilePaths":null}',
      review: { tool: 'Provider permission', text: 'once\nalways\nreject' },
    }
    serve([rc10])
    wrap(<GovernanceView />)
    await screen.findAllByText('Command not shown for this tool yet')
    const row = rowOf(
      within(queue()).getByText('Command not shown for this tool yet'),
    )
    expect(row.querySelector('[data-slot="approval-review"]')).toBeNull()
  })

  // SR4C on 912c4d15: only the structured provider scope decides, never the text of a
  // reviewed command.
  it('decides from the provider scope only, never from a reviewed command', () => {
    const provider = (scope: string, review?: ApprovalDTO['review']) =>
      approvalCommandUnknown({
        ...hookRequest,
        action: 'sessions.provider.approval',
        reason: `Provider permission: driver=opencode method=session/request_permission kind=tool_call_permission run=r1 turn=4 permissions=${scope}`,
        review,
      })
    expect(
      approvalCommandUnknown({
        ...hookRequest,
        review: {
          tool: 'Bash',
          text: `printf '%s\\n' '{"CommandLine":"","FilePaths":null}'`,
        },
      }),
    ).toBe(false)
    expect(
      provider('{"Permissions":["once"],"CommandLine":"","FilePaths":null}'),
    ).toBe(true)
    expect(
      provider('{"Permissions":["once"],"CommandLine":"ls","FilePaths":null}'),
    ).toBe(false)
    expect(
      provider(
        '{"Permissions":["once"],"CommandLine":"","FilePaths":["/srv/a"]}',
      ),
    ).toBe(false)
    expect(
      provider('{"Permissions":["once"],"CommandLine":"","FilePaths":null}', {
        tool: 'Command',
        text: 'ls',
      }),
    ).toBe(false)
    expect(provider('[secret masked]')).toBe(false)
  })

  it('a request from a person names the person, and no session is looked up', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    serve([
      { ...hookRequest, requested_by: 'user:u-7', session_ref: undefined },
    ])
    wrap(<GovernanceView />)
    await screen.findAllByText('A member')
    expect(within(queue()).getByText('A member')).toBeInTheDocument()
    await waitFor(() => expect(sessionsApi.live).not.toHaveBeenCalled())
  })

  // 09b real use: the request's session_ref is the canonical osn_ id, which the live list
  // does not filter on. The waiting run names the request by its exact id instead.
  it('the session and folder come from the run waiting on this request', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    serve([hookRequest])
    wrap(<GovernanceView />)
    expect(await screen.findByText('Bash · Fix the build')).toBeInTheDocument()
    expect(await within(queue()).findByText('/srv/app')).toBeInTheDocument()
  })

  // #502: a launch held for its approval names it in approval_ref, not pending_approval_ref.
  it('the session and folder come from the launch held for this request', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [
        {
          run_ref: 'run-2',
          name: 'Writes a classified folder',
          workspace_ref: 'ws-1',
          state: 'waiting_approval',
          approval_ref: 'apr-1',
        } as never,
      ],
      has_more: false,
    })
    serve([hookRequest])
    wrap(<GovernanceView />)
    expect(
      await screen.findByText('Bash · Writes a classified folder'),
    ).toBeInTheDocument()
    expect(await within(queue()).findByText('/srv/app')).toBeInTheDocument()
  })
})

// Root, 09b capture review: a long folder widened the Request column and pushed the
// actions off the right edge at 1280; at 390 only the Request column was in view. The
// queue's row fills its column and cuts each line to one; the decision is in the panel.
describe('the queue row keeps its lines to one', () => {
  it('cuts the folder to one line with the whole path as its title, and says a tap opens the decision', async () => {
    serve([hookRequest])
    wrap(<GovernanceView />)
    await screen.findByText('Bash · Fix the build')
    const folder = await within(queue()).findByText('/srv/app')
    expect(folder).toHaveAttribute('title', '/srv/app')
    expect(folder.className).toMatch(/truncate/)
    const cell = folder.closest('[data-slot="approval-request"]') as HTMLElement
    expect(cell.className).toMatch(/max-w-full/)
    expect(
      within(cell).getByText('Open to approve or reject').className,
    ).toMatch(/md:hidden/)
  })
})

it('hides cached requester and policy names when their read permissions leave', async () => {
  const request = {
    ...hookRequest,
    action: 'deployment.release',
    subject_kind: 'deployment',
    requested_by: 'user:u-7',
    policy_ref: 'policy-a',
    review: undefined,
  }
  vi.spyOn(consoleApi, 'listMembers').mockResolvedValue({
    items: [
      {
        user_id: 'u-7',
        email: 'ada@example.test',
        display_name: 'Ada',
        status: 'active',
        sso_only: false,
        role: 'admin',
      },
    ],
    has_more: false,
  })
  api.getPolicy.mockResolvedValue({ id: 'policy-a', name: 'Release policy' })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const content = () => (
    <QueryClientProvider client={client}>
      <ApprovalPreview approval={request} />
    </QueryClientProvider>
  )
  const mounted = render(content())
  expect(await screen.findByText('Ada')).toBeVisible()
  expect(await screen.findByText('Matched: Release policy')).toBeVisible()
  // A deployment a person raised runs nothing by itself: no command is expected.
  expect(
    screen.queryByText('This request does not include what will run.'),
  ).toBeNull()
  auth.can = (permission) =>
    !['user:read', 'governance:policy:read'].includes(permission)
  mounted.rerender(content())
  expect(screen.queryByText('Ada')).toBeNull()
  expect(screen.queryByText('Matched: Release policy')).toBeNull()
  expect(screen.getByText('Matched: a rule you cannot read')).toBeVisible()
  expect(screen.getByText('A member')).toBeVisible()
})

it('names the launcher of a new session in both requester displays and withdraws cached names without user read', async () => {
  const request = {
    ...hookRequest,
    action: 'sessions.run.launch',
    subject_kind: 'sessions.run',
    requested_by: 'session:osn_01a0fc45',
    launched_by: 'user:u-7',
    review: undefined,
    reason:
      'A new session that writes to a classified folder: it edits files and asks before commands; tools Read, Edit.',
  }
  const members = vi.spyOn(consoleApi, 'listMembers').mockResolvedValue({
    items: [
      {
        user_id: 'u-7',
        email: 'ada@example.test',
        display_name: 'Ada',
        status: 'active',
        sso_only: false,
        role: 'admin',
      },
    ],
    has_more: false,
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const content = () => (
    <QueryClientProvider client={client}>
      <AskedByName approval={request} />
      <ApprovalPreview approval={request} />
    </QueryClientProvider>
  )
  const mounted = render(content())
  expect(await screen.findAllByText('Ada for a new session')).toHaveLength(2)
  expect(screen.getByText(request.reason)).toBeVisible()
  expect(
    screen.queryByText('This request does not include what will run.'),
  ).toBeNull()
  const reads = members.mock.calls.length
  auth.can = (permission) => permission !== 'user:read'
  mounted.rerender(content())
  expect(screen.queryByText('Ada for a new session')).toBeNull()
  expect(screen.getAllByText('A member for a new session')).toHaveLength(2)
  expect(members).toHaveBeenCalledTimes(reads)
})

// Issue #2269 (permissions capture): the note repeated under every request,
// including the ones a person raised, where no command is expected. It shows once, and
// only on a request the engine stores a review for: a tool call, a provider's
// permission request or a publish.
describe('the "does not include what will run" note', () => {
  const NOTE = 'This request does not include what will run.'
  const { review: _review, ...toolCall } = hookRequest
  void _review
  const providerScope =
    '{"Permissions":["once","reject"],"CommandLine":"npm test","FilePaths":null}'
  const provider: ApprovalDTO = {
    ...toolCall,
    id: 'apr-provider',
    action: 'sessions.provider.approval',
    subject_kind: 'session_run',
    reason: `Provider permission: driver=codex method=session/request_permission kind=tool_call_permission run=r1 turn=4 permissions=${providerScope}`,
  }
  const deployment: ApprovalDTO = {
    ...toolCall,
    id: 'apr-deploy',
    action: 'deploy.promote',
    subject_kind: 'deployment',
    subject_ref: 'rel-42',
    requested_by: 'user:u-7',
    reason: 'Promote the billing connector to production',
  }
  const exportRequest: ApprovalDTO = {
    ...deployment,
    id: 'apr-export',
    action: 'evidence.export',
    subject_kind: 'evidence',
    reason: 'Export the Q3 ledger for the auditors',
  }

  it('expects a command for tool calls, provider requests and publishes only', () => {
    expect(approvalExpectsCommand(toolCall)).toBe(true)
    expect(approvalExpectsCommand(provider)).toBe(true)
    expect(
      approvalExpectsCommand({
        ...deployment,
        action: 'gitpublish.push',
        subject_kind: 'gitpublish.target',
      }),
    ).toBe(true)
    // The engine's other requests that run something when approved.
    for (const [action, subject_kind] of [
      ['mcp.tool.call', 'tool'],
      ['deploy.apply', 'deployment'],
      ['deploy.retire', 'deployment'],
      ['agentcore.export.apply', 'agentcore.policy_engine'],
      ['agentcore.export.apply_weakening', 'agentcore.policy_engine'],
    ])
      expect(
        approvalExpectsCommand({ ...deployment, action, subject_kind }),
        action,
      ).toBe(true)
    expect(approvalExpectsCommand(deployment)).toBe(false)
    expect(approvalExpectsCommand(exportRequest)).toBe(false)
    expect(
      approvalExpectsCommand({
        ...deployment,
        action: 'sessions.run.launch',
        subject_kind: 'sessions.run',
      }),
    ).toBe(false)
  })

  it('shows once, on the rows that expect a command, and not on the ones a person raised', async () => {
    serve([toolCall, provider, deployment, exportRequest])
    wrap(<GovernanceView />)
    await screen.findAllByText(NOTE)
    const notes = within(queue()).getAllByText(NOTE)
    expect(notes).toHaveLength(2)
    for (const note of notes) {
      const row = rowOf(note)
      expect(within(row).getAllByText(NOTE), 'one note per row').toHaveLength(1)
      expect(within(row).queryByText(deployment.reason!)).toBeNull()
      expect(within(row).queryByText(exportRequest.reason!)).toBeNull()
    }
    // The requests a person raised keep their own reason and carry no note.
    for (const request of [deployment, exportRequest]) {
      const row = rowOf(within(queue()).getByText(request.reason!))
      expect(within(row).queryByText(NOTE)).toBeNull()
    }
  })

  it('is absent from the decision dialog of a request a person raised, and shows once on each request that expects a command', async () => {
    const user = userEvent.setup()
    const publish: ApprovalDTO = {
      ...toolCall,
      id: 'apr-publish',
      action: 'gitpublish.push',
      subject_kind: 'gitpublish.target',
      reason: 'Push the release branch',
    }
    serve([deployment, toolCall, provider, publish])
    wrap(<GovernanceView />)
    await screen.findAllByText(deployment.reason!)
    const deny = async (row: HTMLElement) => {
      await user.click(row)
      await user.click(within(panel()).getByRole('button', { name: /^deny$/i }))
      return screen.findByRole('dialog')
    }
    let dialog = await deny(
      rowOf(within(queue()).getByText(deployment.reason!)),
    )
    // The reason leads the preview and repeats behind Details.
    expect(
      within(dialog).getAllByText(deployment.reason!).length,
    ).toBeGreaterThan(0)
    expect(within(dialog).queryByText(NOTE)).toBeNull()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // The tool call, the provider request and the publish each carry one.
    expect(within(queue()).getAllByText(NOTE)).toHaveLength(3)
    for (let i = 0; i < 3; i++) {
      dialog = await deny(rowOf(within(queue()).getAllByText(NOTE)[i]))
      expect(within(dialog).getAllByText(NOTE)).toHaveLength(1)
      await user.keyboard('{Escape}')
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    }
  })
})
