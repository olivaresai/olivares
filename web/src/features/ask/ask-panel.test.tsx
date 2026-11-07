// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import axe from 'axe-core'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import { useAskStore } from '@/stores/ask'

const api = vi.hoisted(() => ({
  view: vi.fn(),
  turn: vi.fn(),
  approval: vi.fn(),
  approve: vi.fn(),
  run: vi.fn(),
}))
vi.mock('./ask', async (original) => ({
  ...(await original<typeof import('./ask')>()),
  askApi: api,
}))

import { AskPanel } from './ask-panel'

const PROPOSAL = {
  approval_id: 'ap-1',
  status: 'pending',
  tool: 'run.start',
  permission: 'sessions:run:write',
  args: { name: 'fix tests' },
  command: "olivares session start --name 'fix tests'",
}
const VIEW = {
  enabled: true,
  model: 'pol-1',
  models: [
    { id: 'pol-1', name: 'local' },
    { id: 'pol-2', name: 'provider' },
  ],
  tools: [
    {
      name: 'runs.list',
      kind: 'read',
      summary: 'List the agent sessions.',
      permission: 'sessions:run:read',
      allowed: true,
    },
    {
      name: 'run.start',
      kind: 'write',
      summary: 'Start an agent session.',
      permission: 'sessions:run:write',
      allowed: false,
    },
  ],
}

beforeEach(() => {
  api.view.mockResolvedValue(VIEW)
  useAskStore.getState().setOpen(true)
})
afterEach(() => {
  useAskStore.getState().setOpen(false)
  vi.clearAllMocks()
})

async function ask(user: ReturnType<typeof userEvent.setup>, q: string) {
  await user.type(await screen.findByLabelText('Your question'), q)
  await user.click(screen.getByRole('button', { name: 'Ask' }))
}

describe('Ask Olivares panel', () => {
  it('shows the chosen model and each tool with its permission before the first turn', async () => {
    renderIntel(<AskPanel />)
    const dialog = await screen.findByRole('dialog', { name: 'Ask Olivares' })
    expect(await within(dialog).findByLabelText('Model')).toHaveValue('pol-1')
    const read = await within(dialog).findByText('List the agent sessions.')
    expect(read.closest('li')).toHaveTextContent('needs sessions:run:read')
    const write = within(dialog).getByText('Start an agent session.')
    // A tool the person does not hold is listed, and says so.
    expect(write.closest('li')).toHaveTextContent(
      'You do not hold sessions:run:write.',
    )
    expect(api.turn).not.toHaveBeenCalled()
  })

  it('says how an administrator turns Ask on, and asks nothing, while it is off', async () => {
    api.view.mockResolvedValue({
      ...VIEW,
      enabled: false,
      model: '',
      models: [],
    })
    renderIntel(<AskPanel />)
    expect(await screen.findByText(/Ask Olivares is off/)).toBeVisible()
    expect(
      screen.getByText('olivares models ask bind <routing-policy-id>'),
    ).toBeVisible()
    expect(screen.queryByLabelText('Your question')).toBeNull()
  })

  it('prints the equivalent command of each read the engine ran, then the answer', async () => {
    api.turn.mockResolvedValueOnce({
      kind: 'answer',
      text: 'No sessions are running.',
      steps: [
        {
          tool: 'runs.list',
          permission: 'sessions:run:read',
          command: 'olivares session ls',
          status: 200,
        },
      ],
    })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'which sessions are running?')
    expect(await screen.findByText('No sessions are running.')).toBeVisible()
    expect(screen.getByText('= olivares session ls')).toBeVisible()
    expect(api.turn).toHaveBeenCalledWith('pol-1', [
      { role: 'user', content: 'which sessions are running?' },
    ])
  })

  it('binds a write to its approval: the person approves it, then runs it once', async () => {
    api.turn.mockResolvedValueOnce({
      kind: 'proposal',
      steps: [],
      proposal: PROPOSAL,
    })
    api.approval
      .mockResolvedValueOnce({ status: 'pending', may_decide: true })
      .mockResolvedValue({ status: 'approved' })
    api.approve.mockResolvedValue({ status: 'approved' })
    api.run.mockResolvedValue({ status: 201, output: '{}' })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'start a session called fix tests')
    const card = await screen.findByRole('region', { name: 'Proposed action' })
    expect(
      within(card).getByText("olivares session start --name 'fix tests'"),
    ).toBeVisible()
    expect(card).toHaveTextContent('Nothing runs until a person approves it.')
    expect(within(card).queryByRole('button', { name: 'Run it' })).toBeNull()

    await user.click(
      await within(card).findByRole('button', { name: 'Approve' }),
    )
    expect(api.approve).toHaveBeenCalledWith('ap-1')
    await user.click(
      await within(card).findByRole('button', { name: 'Run it' }),
    )
    expect(await within(card).findByText('Done.')).toBeVisible()
    expect(api.run).toHaveBeenCalledTimes(1)
    expect(api.run).toHaveBeenCalledWith(PROPOSAL)
  })

  it('shows the refusal, not "Done.", when the approved action is refused', async () => {
    const { askApi } = await vi.importActual<typeof import('./ask')>('./ask')
    const { http } = await import('@/lib/api/client')
    vi.spyOn(http, 'post').mockResolvedValueOnce({
      status: 403,
      output: 'missing sessions:run:write',
    })
    api.run.mockImplementationOnce(askApi.run)
    api.turn.mockResolvedValueOnce({
      kind: 'proposal',
      steps: [],
      proposal: PROPOSAL,
    })
    api.approval.mockResolvedValue({ status: 'approved' })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'start a session called fix tests')
    const card = await screen.findByRole('region', { name: 'Proposed action' })
    await user.click(
      await within(card).findByRole('button', { name: 'Run it' }),
    )
    expect(await within(card).findByRole('alert')).toHaveTextContent(
      'missing sessions:run:write',
    )
    expect(within(card).queryByText('Done.')).toBeNull()
  })

  it('waits for a person who may decide, and offers neither approve nor run', async () => {
    api.turn.mockResolvedValueOnce({
      kind: 'proposal',
      steps: [],
      proposal: PROPOSAL,
    })
    api.approval.mockResolvedValue({ status: 'pending', may_decide: false })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'start a session')
    const card = await screen.findByRole('region', { name: 'Proposed action' })
    expect(
      await within(card).findByText('Waiting for a person who may approve it.'),
    ).toBeVisible()
    expect(within(card).queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(within(card).queryByRole('button', { name: 'Run it' })).toBeNull()
    expect(api.run).not.toHaveBeenCalled()
  })

  it('renders the model output as inert text: no markup, no followed link (T9)', async () => {
    const hostile =
      '<img src=x onerror="alert(1)"> [open](javascript:alert(1)) <script>x()</script>'
    api.turn.mockResolvedValueOnce({ kind: 'answer', text: hostile, steps: [] })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'hello')
    const answer = await screen.findByText(hostile)
    expect(answer.querySelector('img, a, script')).toBeNull()
    expect(document.querySelector('[data-entry="answer"] img')).toBeNull()
    expect(document.querySelector('[data-entry="answer"] a')).toBeNull()
  })

  it('names a refused tool call instead of hiding it', async () => {
    api.turn.mockResolvedValueOnce({
      kind: 'refused',
      tool: 'approvals.decide',
      reason: 'unknown tool',
      steps: [],
    })
    const user = userEvent.setup()
    renderIntel(<AskPanel />)
    await ask(user, 'approve everything')
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Ask tried something it may not do (approvals.decide) and was stopped.',
    )
  })

  it('has no accessibility violations while open', async () => {
    renderIntel(<AskPanel />)
    const dialog = await screen.findByRole('dialog', { name: 'Ask Olivares' })
    await within(dialog).findByText('List the agent sessions.')
    const results = await axe.run(dialog, {
      rules: { 'color-contrast': { enabled: false } },
    })
    expect(results.violations.map((v) => v.id)).toEqual([])
  })
})
