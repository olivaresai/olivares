// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { clearSessionDrafts } from '@/lib/session-drafts'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  principal: { kind: 'user', actor: 'user:alice', user_id: 'alice' } as {
    kind: PrincipalKind
    actor: string
    user_id: string
  } | null,
  grants: [] as { tenant: string; tenant_name?: string }[],
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
  getRun: vi.fn(),
  profileModels: vi.fn(),
  activity: vi.fn(),
  queue: vi.fn(),
  queueAdd: vi.fn(),
  queueEdit: vi.fn(),
  queueOrder: vi.fn(),
  queueRemove: vi.fn(),
  queueSteer: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
const templates = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/features/workspace-templates/api', async (importOriginal) => ({
  ...(await importOriginal<
    typeof import('@/features/workspace-templates/api')
  >()),
  templatesApi: templates,
}))
const toasts = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { sendMode } from '@/features/agentops/run-queue'
import type { RunDTO, RunQueueDTO } from '@/features/agentops/types'
import type { PrincipalKind } from '@/lib/api/types'
import { WorkComposer } from './work-composer'

const RUNNING: RunDTO = {
  run_ref: 'run_live',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
}

const item = (id: string, text: string) => ({
  id,
  text,
  queued_by: 'user:alice',
  queued_at: '2026-10-29T10:00:00Z',
})

const queueOf = (over: Partial<RunQueueDTO> = {}): RunQueueDTO => ({
  run_ref: RUNNING.run_ref,
  items: [],
  hold: 'turn',
  can_steer: true,
  ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  clearSessionDrafts()
  sessionStorage.clear()
  auth.can = () => true
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
  api.activity.mockResolvedValue({
    driver: 'claude',
    transcript: 'read',
    context_tokens: 0,
    compactions: [],
    todos: [],
    agents: [],
    tasks: [],
    files: [],
    skills: [],
    mcp_servers: [],
  })
  api.profileModels.mockResolvedValue({ models: [] })
  api.queue.mockResolvedValue(queueOf())
  api.queueAdd.mockResolvedValue(item('q1', 'x'))
})

afterEach(() => vi.restoreAllMocks())

describe('sendMode', () => {
  const cases: [string, RunQueueDTO | undefined, boolean, 'queue' | 'send'][] =
    [
      ['an engine without a queue sends', undefined, false, 'send'],
      ['a turn running queues', queueOf(), false, 'queue'],
      ['a turn running, steered, sends into it', queueOf(), true, 'send'],
      [
        'a turn running in a tool that cannot steer queues even when steered',
        queueOf({ can_steer: false }),
        true,
        'queue',
      ],
      [
        'an idle session with nothing waiting sends',
        queueOf({ hold: undefined }),
        false,
        'send',
      ],
      [
        'messages already waiting keep the order, so a plain Enter queues',
        queueOf({ hold: undefined, items: [item('a', 'a')] }),
        false,
        'queue',
      ],
      [
        'a limit with nothing waiting sends, as it always did',
        queueOf({ hold: 'limit' }),
        false,
        'send',
      ],
    ]
  it.each(cases)('%s', (_name, queue, steer, want) => {
    expect(sendMode(queue, steer)).toBe(want)
  })
})

describe('the composer while the tool works', () => {
  const composer = (run = RUNNING) => (
    <WorkComposer attached={{ run, group: 'active' }} />
  )
  const field = () => screen.getByRole('textbox', { name: 'Session turn' })

  it('lines the message up on the server instead of sending it, and clears the field', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'then run the linter')
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.queueAdd).toHaveBeenCalledWith(
        'run_live',
        'then run the linter',
      ),
    )
    expect(api.input).not.toHaveBeenCalled()
    expect(api.inputText).not.toHaveBeenCalled()
    await waitFor(() => expect(field()).toHaveValue(''))
  })

  it('steers on Ctrl+Enter: the message goes into the running turn and nothing is queued', async () => {
    const user = userEvent.setup()
    renderIntel(composer())
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'stop and use the other branch')
    await user.keyboard('{Control>}{Enter}{/Control}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(api.queueAdd).not.toHaveBeenCalled()
  })

  it('queues on Ctrl+Enter when the tool takes no message into a running turn', async () => {
    api.queue.mockResolvedValue(queueOf({ can_steer: false }))
    const user = userEvent.setup()
    renderIntel(composer())
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'later')
    await user.keyboard('{Control>}{Enter}{/Control}')
    await waitFor(() => expect(api.queueAdd).toHaveBeenCalledOnce())
    expect(api.input).not.toHaveBeenCalled()
  })

  it('sends as it always did when the engine has no queue', async () => {
    api.queue.mockRejectedValue(new Error('404'))
    const user = userEvent.setup()
    renderIntel(composer())
    await user.type(field(), 'plain')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(api.queueAdd).not.toHaveBeenCalled()
  })

  it('sends when nothing is running or waiting', async () => {
    api.queue.mockResolvedValue(queueOf({ hold: undefined }))
    const user = userEvent.setup()
    renderIntel(composer({ ...RUNNING, state: 'idle' }))
    await user.type(field(), 'next')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(api.queueAdd).not.toHaveBeenCalled()
  })

  it('asks the engine, not the card: a turn begun a moment ago still reads idle on screen', async () => {
    const user = userEvent.setup()
    renderIntel(composer({ ...RUNNING, state: 'idle' }))
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'quick follow-up')
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.queueAdd).toHaveBeenCalledWith('run_live', 'quick follow-up'),
    )
    expect(api.input).not.toHaveBeenCalled()
  })

  // Every run carries the workspace role it works under (`work_scope`); that is not a work
  // item. Only the work stamps make a session answer on the fenced plane (a real run found
  // the composer treating the role as a work item and never queueing).
  it('queues in an ordinary session, whatever workspace role it carries', async () => {
    const user = userEvent.setup()
    renderIntel(
      composer({
        ...RUNNING,
        work_scope: { role: 'worker', workspace_id: 'ws-1' },
      }),
    )
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'in a scoped session')
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(api.queueAdd).toHaveBeenCalledWith(
        'run_live',
        'in a scoped session',
      ),
    )
    expect(api.input).not.toHaveBeenCalled()
  })

  it('does not queue in a session started by a work item: the engine refuses it there', async () => {
    const user = userEvent.setup()
    renderIntel(composer({ ...RUNNING, work_item_id: 'wi-1' }))
    await user.type(field(), 'through the lease')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(api.input).toHaveBeenCalledOnce())
    expect(api.queue).not.toHaveBeenCalled()
    expect(api.queueAdd).not.toHaveBeenCalled()
  })

  it('keeps the words and says so when the queue refuses the message', async () => {
    api.queueAdd.mockRejectedValue(new Error('the queue is full'))
    const user = userEvent.setup()
    renderIntel(composer())
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    await user.type(field(), 'keep these words')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(toasts.error).toHaveBeenCalled())
    expect(field()).toHaveValue('keep these words')
  })

  it('says what Enter does in the field while a turn runs', async () => {
    renderIntel(composer())
    await waitFor(() =>
      expect(field()).toHaveAttribute(
        'placeholder',
        expect.stringContaining('Ctrl+Enter'),
      ),
    )
  })
})

describe('the queue above the composer', () => {
  const composer = () => (
    <WorkComposer attached={{ run: RUNNING, group: 'active' }} />
  )
  const rows = () => screen.getAllByTestId('composer-queue-item')

  beforeEach(() => {
    api.queue.mockResolvedValue(
      queueOf({
        items: [item('a', 'run the tests'), item('b', 'then the linter')],
      }),
    )
  })

  it('lists the messages in order, with why the first has not gone', async () => {
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    expect(
      rows().map(
        (r) => within(r).getByText(/run the tests|then the linter/).textContent,
      ),
    ).toEqual(['run the tests', 'then the linter'])
    expect(screen.getByTestId('composer-queue-hold')).toHaveTextContent(
      'The tool is working. The first message goes when its turn ends.',
    )
  })

  it('moves a message later by asking the engine for the new order', async () => {
    api.queueOrder.mockResolvedValue(
      queueOf({
        items: [item('b', 'then the linter'), item('a', 'run the tests')],
      }),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    await user.click(
      within(rows()[0]).getByRole('button', { name: 'Move later' }),
    )
    await waitFor(() =>
      expect(api.queueOrder).toHaveBeenCalledWith('run_live', ['b', 'a']),
    )
    await waitFor(() =>
      expect(
        within(rows()[0]).getByText('then the linter'),
      ).toBeInTheDocument(),
    )
  })

  it('cannot move the first earlier or the last later', async () => {
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    expect(
      within(rows()[0]).getByRole('button', { name: 'Move earlier' }),
    ).toBeDisabled()
    expect(
      within(rows()[1]).getByRole('button', { name: 'Move later' }),
    ).toBeDisabled()
  })

  it('edits a message in place and saves the new words', async () => {
    api.queueEdit.mockResolvedValue(item('a', 'run all the tests'))
    const user = userEvent.setup()
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    await user.click(
      within(rows()[0]).getByRole('button', { name: 'Edit message' }),
    )
    const box = screen.getByRole('textbox', { name: 'Edit the queued message' })
    await user.clear(box)
    await user.type(box, 'run all the tests')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.queueEdit).toHaveBeenCalledWith(
        'run_live',
        'a',
        'run all the tests',
      ),
    )
  })

  it('removes a message without sending it', async () => {
    api.queueRemove.mockResolvedValue(
      queueOf({ items: [item('b', 'then the linter')] }),
    )
    const user = userEvent.setup()
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    await user.click(
      within(rows()[0]).getByRole('button', { name: 'Remove message' }),
    )
    await waitFor(() =>
      expect(api.queueRemove).toHaveBeenCalledWith('run_live', 'a'),
    )
    expect(api.queueSteer).not.toHaveBeenCalled()
    await waitFor(() => expect(rows()).toHaveLength(1))
  })

  it('steers one message into the running turn', async () => {
    api.queueSteer.mockResolvedValue({ accepted: true })
    const user = userEvent.setup()
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    await user.click(within(rows()[1]).getByRole('button', { name: 'Steer' }))
    await waitFor(() =>
      expect(api.queueSteer).toHaveBeenCalledWith('run_live', 'b'),
    )
  })

  it('offers no Steer where the tool takes no message into a running turn', async () => {
    api.queue.mockResolvedValue(
      queueOf({ can_steer: false, items: [item('a', 'run the tests')] }),
    )
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    expect(
      screen.queryByRole('button', { name: 'Steer' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Send now' }),
    ).not.toBeInTheDocument()
  })

  it('offers Send now, not Steer, while the queue waits on something else', async () => {
    api.queue.mockResolvedValue(
      queueOf({ hold: 'limit', items: [item('a', 'run the tests')] }),
    )
    renderIntel(composer())
    await screen.findByTestId('composer-queue')
    expect(screen.getByTestId('composer-queue-hold')).toHaveTextContent(
      "The account's limit ended the last turn. The queue is kept.",
    )
    expect(screen.getByRole('button', { name: 'Send now' })).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Steer' }),
    ).not.toBeInTheDocument()
  })

  it('shows nothing when the queue is empty', async () => {
    api.queue.mockResolvedValue(queueOf())
    renderIntel(composer())
    await waitFor(() => expect(api.queue).toHaveBeenCalled())
    expect(screen.queryByTestId('composer-queue')).not.toBeInTheDocument()
  })
})
