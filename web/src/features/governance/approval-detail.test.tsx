// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApprovalDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { actor: 'user:reviewer', kind: 'user' },
  }),
}))

const api = vi.hoisted(() => ({
  getApproval: vi.fn(),
  listDecisions: vi.fn(),
  cancelApproval: vi.fn(),
  decide: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import { ApprovalDetailPanel } from './approval-detail'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

/** The queue's row opens the panel; the engine's read of the same request follows. */
function openPanel(approval: ApprovalDTO) {
  api.getApproval.mockResolvedValue(approval)
  return wrap(
    <ApprovalDetailPanel approvalId={approval.id} approval={approval} />,
  )
}

const NOTE = 'This request does not include what will run.'

// 16 KiB with no space in it: the shape that pushed the old sheet past the viewport.
const COMMAND = `curl -fsS https://example.test/${'a'.repeat(16 * 1024)}`

const toolApproval: ApprovalDTO = {
  id: 'a-cmd',
  action: 'claude.hook.tool',
  subject_kind: 'claude.tool',
  subject_ref: 'Bash',
  requested_by: 'user:operator',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  reason: COMMAND,
  escalated: false,
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  api.listDecisions.mockResolvedValue({ items: [], has_more: false })
})

describe('ApprovalDetailPanel — the reason a reviewer approves', () => {
  it('lets the launcher decide a session-proposed request while naming the launcher', async () => {
    openPanel({
      ...toolApproval,
      action: 'sessions.run.launch',
      subject_kind: 'sessions.run',
      requested_by: 'session:osn_new',
      launched_by: 'user:reviewer',
      reason: 'A new session that writes to a classified folder.',
    })
    expect(await screen.findByText('You for a new session')).toBeVisible()
    expect(
      screen.getByRole('button', { name: /^approve once$/i }),
    ).toBeEnabled()
    expect(screen.getByRole('button', { name: /^deny$/i })).toBeEnabled()
    expect(
      screen.queryByText(
        'You cannot approve your own request. Another administrator must decide.',
      ),
    ).toBeNull()
    expect(screen.getByRole('button', { name: /^cancel$/i })).toBeEnabled()
    expect(screen.getByText('session:osn_new')).toBeInTheDocument()
    expect(api.decide).not.toHaveBeenCalled()
    // A launch runs nothing by itself: no command is expected, so no note (#2269).
    expect(screen.queryByText(NOTE)).toBeNull()
  })

  // dev#3268: a disabled primary says why, or shows it is working. While the decision
  // is sent, Approve once is that action in progress.
  it('shows Approve once working while the decision is sent', async () => {
    api.decide.mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    openPanel(toolApproval)
    await user.click(
      await screen.findByRole('button', { name: /^approve once$/i }),
    )
    const working = await screen.findByRole('button', { name: 'Approving…' })
    expect(working).toBeDisabled()
    expect(working.querySelector('.animate-spin')).not.toBeNull()
  })

  it('explains why a requester cannot decide their own pending request', async () => {
    openPanel({
      ...toolApproval,
      requested_by: 'user:reviewer',
    })
    expect(
      await screen.findByText(
        'You cannot approve your own request. Another administrator must decide.',
      ),
    ).toBeVisible()
    expect(screen.queryByRole('button', { name: /^approve once$/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^deny$/i })).toBeNull()
    expect(screen.getByRole('button', { name: /^cancel$/i })).toBeEnabled()
  })

  it('shows a 16 KiB tool command whole, as code that wraps at any character', async () => {
    openPanel(toolApproval)

    const block = await screen.findByText(COMMAND)
    // Whole: no tail cut, so the reviewer never approves an unseen part.
    expect(block.textContent).toHaveLength(COMMAND.length)
    expect(block.tagName).toBe('PRE')
    expect(block).toHaveAttribute('data-slot', 'approval-command')
    // Wraps instead of widening the sheet; long commands scroll inside the block.
    expect(block.className).toMatch(/whitespace-pre-wrap/)
    expect(block.className).toMatch(/break-all/)
    expect(block.className).toMatch(/overflow-y-auto/)
    // A tool call stored without a review says so once.
    expect(screen.getAllByText(NOTE)).toHaveLength(1)
  })

  it('keeps a prose reason as text, and any long value still breaks anywhere', async () => {
    const prose = `Ship the hotfix to ${'x'.repeat(400)}`
    openPanel({
      ...toolApproval,
      subject_kind: 'deployment',
      reason: prose,
    })

    const value = await screen.findByText(prose, { selector: 'dd' })
    expect(value.tagName).toBe('DD')
    expect(value.className).toMatch(/\[overflow-wrap:anywhere\]/)
    expect(screen.queryByText(NOTE)).toBeNull()
  })
})

// HU 047: the requester of an estate re-enable cannot decide it; the panel offered only
// Cancel and Close, with no word on what is needed.
describe('ApprovalDetailPanel — the requester of an estate re-enable', () => {
  it('says two other admin accounts must approve, and where to create them', async () => {
    openPanel({
      ...toolApproval,
      action: 'security.killswitch.reenable',
      subject_kind: 'security.killswitch',
      requested_by: 'user:reviewer',
      required_approvals: 2,
      reason: 'Re-enable after the incident',
    })
    expect(
      await screen.findByText(/Two other admin accounts must approve this/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: 'Open Identity & access' }),
    ).toHaveAttribute('href', '/identity')
    // The command reads as a command (the inline chip), never between backticks.
    const line = document.querySelector('[data-slot="reenable-needs-admins"]')!
    expect(line.textContent).not.toContain('`')
    expect(
      line.querySelector('[data-slot="code-line"][data-inline="true"] code'),
    ).toHaveTextContent('olivares users create')
    expect(screen.queryByRole('button', { name: /^approve once$/i })).toBeNull()
  })

  it('says nothing of the kind to an admin who may decide it', async () => {
    openPanel({
      ...toolApproval,
      action: 'security.killswitch.reenable',
      requested_by: 'user:someone-else',
    })
    expect(
      await screen.findByRole('button', { name: /^approve once$/i }),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/Two other admin accounts must approve this/),
    ).toBeNull()
  })
})
