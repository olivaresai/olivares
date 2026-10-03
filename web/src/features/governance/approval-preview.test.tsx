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
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { actor: 'user:reviewer', kind: 'user' },
  }),
}))
const api = vi.hoisted(() => ({
  listApprovals: vi.fn(),
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  decide: vi.fn(),
  cancelApproval: vi.fn(),
  sweepApprovals: vi.fn(),
  listPolicies: vi.fn(),
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
import { approvalCommandUnknown } from './approval-preview'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
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
    api.listApprovals.mockResolvedValue({
      items: [hookRequest],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const row = (await screen.findByText('Bash · Fix the build')).closest('tr')!
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

  it('the Approve dialog shows what will run above the note, whole, and the references behind Details', async () => {
    const user = userEvent.setup()
    api.listApprovals.mockResolvedValue({
      items: [hookRequest],
      has_more: false,
    })
    wrap(<GovernanceView />)
    await screen.findByText('Bash · Fix the build')
    await user.click(screen.getByRole('button', { name: /^approve$/i }))
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
    api.listApprovals.mockResolvedValue({ items: [without], has_more: false })
    wrap(<GovernanceView />)
    const row = (
      await screen.findByText('This request does not include what will run.')
    ).closest('tr')!
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
    api.listApprovals.mockResolvedValue({ items: [blind], has_more: false })
    wrap(<GovernanceView />)
    const note = await screen.findByText('Command not shown for this tool yet')
    const row = note.closest('tr')!
    expect(row.querySelector('[data-slot="approval-review"]')).toBeNull()
    // Approve and Reject are unchanged.
    expect(
      within(row).getByRole('button', { name: /^approve$/i }),
    ).toBeEnabled()
    expect(within(row).getByRole('button', { name: /^reject$/i })).toBeEnabled()
    await user.click(within(row).getByRole('button', { name: /^approve$/i }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText('Command not shown for this tool yet'),
    ).toBeInTheDocument()
    expect(dialog.querySelector('[data-slot="approval-review"]')).toBeNull()
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
    api.listApprovals.mockResolvedValue({ items: [rc10], has_more: false })
    wrap(<GovernanceView />)
    const note = await screen.findByText('Command not shown for this tool yet')
    const row = note.closest('tr')!
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
    api.listApprovals.mockResolvedValue({
      items: [
        { ...hookRequest, requested_by: 'user:u-7', session_ref: undefined },
      ],
      has_more: false,
    })
    wrap(<GovernanceView />)
    expect(await screen.findByText('user:u-7')).toBeInTheDocument()
    await waitFor(() => expect(sessionsApi.live).not.toHaveBeenCalled())
  })

  // 09b real use: the request's session_ref is the canonical osn_ id, which the live list
  // does not filter on. The waiting run names the request by its exact id instead.
  it('the session and folder come from the run waiting on this request', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    api.listApprovals.mockResolvedValue({
      items: [hookRequest],
      has_more: false,
    })
    wrap(<GovernanceView />)
    expect(await screen.findByText('Bash · Fix the build')).toBeInTheDocument()
    expect(await screen.findByText('/srv/app')).toBeInTheDocument()
  })
})

// Root, 09b capture review: a long folder widened the Request column and pushed the
// actions off the right edge at 1280; at 390 only the Request column was in view.
describe('the queue row keeps its actions in view', () => {
  it('cuts the folder to one line with the whole path as its title, and says a tap opens the decision', async () => {
    api.listApprovals.mockResolvedValue({
      items: [hookRequest],
      has_more: false,
    })
    wrap(<GovernanceView />)
    const folder = await screen.findByText('/srv/app')
    expect(folder).toHaveAttribute('title', '/srv/app')
    expect(folder.className).toMatch(/truncate/)
    const cell = folder.closest('[data-slot="approval-request"]') as HTMLElement
    expect(cell.className).toMatch(/max-w-\[20rem\]/)
    expect(
      within(cell).getByText('Open to approve or reject').className,
    ).toMatch(/md:hidden/)
  })
})
