// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { governanceApi } from '@/features/governance/api'
import { ApiError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'
import { gitpublishApi } from './api'
import { GitPublicationView } from './gitpublish-view'
import type {
  HeldApproval,
  PublicationIntent,
  PublicationTarget,
} from './types'

const auth = vi.hoisted(() => ({
  can: (_permission: string): boolean => true,
  principal: undefined as
    { aal: number; step_up_satisfied?: boolean } | undefined,
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-1',
    can: auth.can,
    principal: auth.principal,
  }),
}))

const SHA = 'a'.repeat(40)
const TREE = 'b'.repeat(40)

const listed: PublicationTarget = {
  id: 'tg-1',
  workspace_id: 'ws-1',
  host: 'github',
  push_prefix: 'agents/',
  merge_bases: ['main'],
  version: 2,
}
const administered: PublicationTarget = {
  ...listed,
  credential_binding_id: 'cb-github',
  repository_binding_id: 'rb-app',
}

function intent(over: Partial<PublicationIntent> = {}): PublicationIntent {
  return {
    id: 'in-1',
    target_id: 'tg-1',
    target_version: 2,
    effect: 'push',
    operation_id: 'op-1',
    attempt: 1,
    state: 'uncertain',
    receipt: 'none',
    requested: { ref: 'refs/heads/agents/fix', commit: SHA, tree: TREE },
    observed: { present: false, merged: false, source: 'dispatcher' },
    acknowledged: { acknowledged: false },
    ...over,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.can = () => true
  auth.principal = undefined
  useWorkspaceStore.getState().setActiveWorkspace('ws-1', 'Agents')
  vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [listed] })
  vi.spyOn(gitpublishApi, 'target').mockResolvedValue(administered)
  vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [] })
  vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue({
    items: [],
    has_more: false,
  })
  useModulesStore.getState().setOff([])
})

async function openTarget() {
  renderIntel(<GitPublicationView />)
  await userEvent.click(
    await screen.findByRole('button', { name: 'Open target tg-1' }),
  )
  return screen.findByRole('dialog', { name: 'Target tg-1' })
}

/** The sheet's list row of one action, so its authority note is read beside its own label. */
function actionRow(sheet: HTMLElement, label: string): HTMLElement {
  const row = within(sheet)
    .getAllByRole('listitem')
    .find((li) => li.textContent?.startsWith(label))
  if (!row) throw new Error(`no action row starts with ${label}`)
  return row
}

/** The branch names a row shows, each a PathText: whole on hover, cut in its middle. */
function branchesOf(row: HTMLElement): (string | null)[] {
  const codes = row.querySelectorAll('[data-slot="path-text"] code')
  return Array.from(codes, (code) => code.getAttribute('title'))
}

async function pasteInto(field: HTMLElement, value: string) {
  await userEvent.click(field)
  await userEvent.paste(value)
}

async function requestPush() {
  const sheet = await openTarget()
  await userEvent.click(within(sheet).getByRole('button', { name: 'Push' }))
  const dialog = await screen.findByRole('dialog', { name: 'Push a commit' })
  await pasteInto(
    within(dialog).getByLabelText(/Branch ref/),
    'refs/heads/agents/fix',
  )
  await pasteInto(within(dialog).getByLabelText(/^Commit/), SHA)
  await pasteInto(within(dialog).getByLabelText(/^Tree/), TREE)
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Request push' }),
  )
  return dialog
}

describe('publication targets', () => {
  it.each(['git', undefined] as const)(
    'offers only push when the target host is %s',
    async (host) => {
      vi.spyOn(gitpublishApi, 'target').mockResolvedValue({
        ...administered,
        host,
      })
      const sheet = await openTarget()
      expect(
        await within(sheet).findByRole('button', { name: 'Push' }),
      ).toBeInTheDocument()
      expect(
        within(sheet).queryByRole('button', { name: 'Open pull request' }),
      ).not.toBeInTheDocument()
      expect(
        within(sheet).queryByRole('button', { name: 'Merge' }),
      ).not.toBeInTheDocument()
      // Only the push note: the pull request note belongs to an action this host does
      // not offer, and the administration note asks for admin access.
      expect(within(sheet).getAllByText('Requires write access')).toHaveLength(
        1,
      )
    },
  )

  it.each(['github', 'gitlab'] as const)(
    'keeps pull request and merge actions for %s',
    async (host) => {
      vi.spyOn(gitpublishApi, 'target').mockResolvedValue({
        ...administered,
        host,
      })
      const sheet = await openTarget()
      expect(
        await within(sheet).findByRole('button', { name: 'Push' }),
      ).toBeInTheDocument()
      expect(
        within(sheet).getByRole('button', { name: 'Open pull request' }),
      ).toBeInTheDocument()
      expect(
        within(sheet).getByRole('button', { name: 'Merge' }),
      ).toBeInTheDocument()
    },
  )
  it('lists each target with its push prefix and merge bases, without its bindings', async () => {
    renderIntel(<GitPublicationView />)
    const row = (await screen.findByText('agents/')).closest('tr')!
    expect(within(row).getByText('main')).toBeInTheDocument()
    expect(screen.queryByText('cb-github')).not.toBeInTheDocument()
  })

  it('shows the target’s bindings and the authority each action requires', async () => {
    // A configured policy (totp or passkey) this password session does not meet.
    auth.principal = { aal: 1, step_up_satisfied: false }
    const sheet = await openTarget()
    expect(await within(sheet).findByText('cb-github')).toBeInTheDocument()
    expect(within(sheet).getByText('rb-app')).toBeInTheDocument()
    expect(actionRow(sheet, 'Push')).toHaveTextContent('Requires write access')
    expect(actionRow(sheet, 'Merge')).toHaveTextContent(
      'Requires admin access and the extra check for administrative actions',
    )
    expect(within(sheet).queryByText(/gitpublish:/)).not.toBeInTheDocument()
  })

  it('asks for no extra check when this session already meets the deployment’s policy', async () => {
    // The default policy (none): a password session satisfies the step-up, as the engine says.
    auth.principal = { aal: 1, step_up_satisfied: true }
    const sheet = await openTarget()
    await within(sheet).findByText('cb-github')
    expect(actionRow(sheet, 'Merge')).toHaveTextContent(
      /^MergeRequires admin access(?!.*extra check)/,
    )
    expect(
      within(sheet).queryByText(/AAL3|extra check/),
    ).not.toBeInTheDocument()
  })

  it('asks for no extra check from a session an older engine reports at AAL3', async () => {
    // An engine without step_up_satisfied: a hardware step-up meets any policy.
    auth.principal = { aal: 3 }
    const sheet = await openTarget()
    await within(sheet).findByText('cb-github')
    expect(actionRow(sheet, 'Merge')).toHaveTextContent(
      /^MergeRequires admin access(?!.*extra check)/,
    )
  })

  it('says the bindings are withheld when the engine does not return them', async () => {
    vi.spyOn(gitpublishApi, 'target').mockResolvedValue(listed)
    const sheet = await openTarget()
    expect(
      await within(sheet).findAllByText('Shown to target administrators only'),
    ).toHaveLength(2)
  })

  it('offers no administration without target administration', async () => {
    auth.can = (p) => p === 'gitpublish:target:read'
    const sheet = await openTarget()
    await within(sheet).findByText('agents/')
    expect(
      screen.queryByRole('button', { name: 'New target' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Edit' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Delete' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Push' }),
    ).not.toBeInTheDocument()
  })
})

describe('a governed publication request', () => {
  it('sends one operation and answers an uncertain outcome with reconcile, never a retry', async () => {
    const push = vi
      .spyOn(gitpublishApi, 'push')
      .mockResolvedValue(intent({ state: 'uncertain' }))
    const reconcile = vi
      .spyOn(gitpublishApi, 'reconcile')
      .mockResolvedValue(
        intent({ state: 'adopted', receipt: 'existing_effect' }),
      )
    const dialog = await requestPush()

    expect(
      await within(dialog).findByText('The outcome on the host is uncertain'),
    ).toBeInTheDocument()
    expect(push).toHaveBeenCalledTimes(1)
    const [targetId, body, request] = push.mock.calls[0]!
    expect(targetId).toBe('tg-1')
    expect(body).toMatchObject({
      ref: 'refs/heads/agents/fix',
      commit: SHA,
      tree: TREE,
      expected_old: '',
      acknowledge_intent: '',
    })
    expect(body.operation_id).toMatch(/^[A-Za-z0-9._:-]{1,128}$/)
    // A push that names no session run sends the request it always sent.
    expect(body).not.toHaveProperty('session_run')
    expect(request).toEqual({ tenant: 'tenant-1' })

    // The request cannot be sent again from here, and nothing offers to.
    expect(
      within(dialog).queryByRole('button', { name: 'Request push' }),
    ).not.toBeInTheDocument()
    expect(
      within(dialog).queryByRole('button', { name: /retry|try again|resend/i }),
    ).not.toBeInTheDocument()

    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Reconcile' }),
    )
    await waitFor(() =>
      expect(reconcile).toHaveBeenCalledWith('in-1', { tenant: 'tenant-1' }),
    )
    expect(push).toHaveBeenCalledTimes(1)
  })

  it('sends the session run an operator names, so the engine fetches the commit from it', async () => {
    const run = '0b3f6c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f'
    const push = vi
      .spyOn(gitpublishApi, 'push')
      .mockResolvedValue(intent({ state: 'applied', receipt: 'caused' }))
    const sheet = await openTarget()
    await userEvent.click(within(sheet).getByRole('button', { name: 'Push' }))
    const dialog = await screen.findByRole('dialog', { name: 'Push a commit' })
    await pasteInto(
      within(dialog).getByLabelText(/Branch ref/),
      'refs/heads/agents/fix',
    )
    await pasteInto(within(dialog).getByLabelText(/^Commit/), SHA)
    await pasteInto(within(dialog).getByLabelText(/^Tree/), TREE)
    await pasteInto(within(dialog).getByLabelText(/^Session run/), run)
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Request push' }),
    )
    await within(dialog).findByText('The request is settled')
    expect(push.mock.calls[0]![1]).toMatchObject({ session_run: run })
  })

  it('shows the refusal the engine returns, with its code and the intent it names', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(
        409,
        'internal',
        'refused',
        undefined,
        {},
        {
          error: 'unresolved_intent',
          intent_id: 'in-9',
        },
      ),
    )
    const dialog = await requestPush()
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent(
      'An unresolved intent already holds this branch, pull request or merge.',
    )
    expect(alert).toHaveTextContent('unresolved_intent')
    expect(
      within(alert).getByRole('button', { name: 'Open intent in-9' }),
    ).toBeInTheDocument()
  })

  it('says what to do about a detected credential here, where there is no Ship', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(403, 'secret_detected', 'secret_detected'),
    )
    const dialog = await requestPush()
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent(
      'The commit contains what looks like a credential, so nothing was sent. Remove it from the commit, then make the request again.',
    )
    expect(alert).not.toHaveTextContent('Ship')
  })

  it('reports a lost answer as unknown and sends the operator to the intents, not to resend', async () => {
    const { NetworkError } = await import('@/lib/api/errors')
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new NetworkError('offline'),
    )
    const dialog = await requestPush()
    expect(
      await within(dialog).findByText('The engine’s answer did not arrive'),
    ).toBeInTheDocument()
    expect(
      within(dialog).queryByRole('button', { name: 'Request push' }),
    ).not.toBeInTheDocument()
  })
})

describe('a publication intent', () => {
  it('keeps what was requested, what the host showed and what it acknowledged apart', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [intent()],
    })
    vi.spyOn(gitpublishApi, 'intent').mockResolvedValue(intent())
    vi.spyOn(gitpublishApi, 'observations').mockResolvedValue({
      items: [
        {
          attempt: 1,
          source: 'dispatcher',
          result: 'uncertain',
          at: '2026-09-27T10:00:00Z',
        },
      ],
    })
    const sheet = await openTarget()
    await userEvent.click(
      await within(sheet).findByRole('button', { name: 'Open intent in-1' }),
    )
    const detail = await screen.findByRole('dialog', { name: 'Intent in-1' })
    expect(
      within(detail).getByRole('heading', { name: 'Requested' }),
    ).toBeInTheDocument()
    expect(
      within(detail).getByRole('heading', { name: 'Observed on the host' }),
    ).toBeInTheDocument()
    expect(
      within(detail).getByRole('heading', { name: 'Acknowledged by the host' }),
    ).toBeInTheDocument()
    expect(
      await within(detail).findByRole('cell', { name: 'dispatcher' }),
    ).toBeInTheDocument()
    expect(
      within(detail).getByRole('button', { name: 'Reconcile' }),
    ).toBeInTheDocument()
    expect(
      within(detail).getByRole('button', { name: 'Abandon' }),
    ).toBeInTheDocument()
    expect(
      within(detail).queryByRole('button', { name: /retry|try again|resend/i }),
    ).not.toBeInTheDocument()
  })
})

describe('the pull requests opened here', () => {
  const opened = (over: Partial<PublicationIntent> = {}) =>
    intent({
      id: 'in-7',
      effect: 'pull_request',
      state: 'applied',
      receipt: 'caused',
      requested: {
        head_ref: 'agents/export',
        base: 'main',
        commit: SHA,
        title: 'Add the export',
      },
      observed: {
        present: true,
        merged: false,
        number: 7,
        url: 'https://github.com/acme/app/pull/7',
        at: '2026-11-10T10:00:00Z',
      },
      ...over,
    })
  const pushed = (over: Partial<PublicationIntent> = {}) =>
    intent({
      id: 'in-2',
      state: 'applied',
      receipt: 'caused',
      observed: {
        present: true,
        merged: false,
        sha: SHA,
        at: '2026-11-08T10:00:00Z',
      },
      ...over,
    })
  const docs = opened({
    id: 'in-5',
    target_id: 'tg-2',
    requested: {
      head_ref: 'bots/docs',
      base: 'main',
      commit: SHA,
      title: 'Fix the docs',
    },
    observed: {
      present: true,
      merged: true,
      number: 5,
      url: 'https://github.com/acme/app/pull/5',
      at: '2026-11-09T10:00:00Z',
    },
  })

  it('lists them across targets, newest first, with their link and the state seen', async () => {
    const other: PublicationTarget = { ...listed, id: 'tg-2' }
    const plain: PublicationTarget = { ...listed, id: 'tg-3', host: 'git' }
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [listed, other, plain],
    })
    vi.spyOn(gitpublishApi, 'intents').mockImplementation(async (target) => ({
      items:
        target === 'tg-1'
          ? [intent(), opened()]
          : target === 'tg-2'
            ? [docs]
            : [pushed({ target_id: 'tg-3' })],
    }))
    renderIntel(<GitPublicationView />)
    const all = await screen.findByRole('table', { name: 'Publications' })
    // A push to a plain Git remote is a publication, with no pull request.
    expect(within(all).getAllByRole('row').slice(1)).toHaveLength(3)
    expect(within(all).getByText('agents/fix')).toBeVisible()
    await userEvent.click(screen.getByRole('radio', { name: 'Pull requests' }))
    const table = screen.getByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    const link = within(rows[0]!).getByRole('link', {
      name: '#7 Add the export',
    })
    expect(link).toHaveAttribute('href', 'https://github.com/acme/app/pull/7')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    expect(within(rows[0]!).getByText('agents/export')).toBeVisible()
    expect(rows[0]).toHaveTextContent('agents/export → main')
    expect(branchesOf(rows[0]!)).toEqual(['agents/export', 'main'])
    expect(within(rows[0]!).getByText('Opened')).toBeVisible()
    expect(
      within(rows[1]!).getByRole('link', { name: '#5 Fix the docs' }),
    ).toHaveAttribute('href', 'https://github.com/acme/app/pull/5')
    expect(within(rows[1]!).getByText('Merged')).toBeVisible()
    expect(within(table).queryByText('agents/fix')).toBeNull()
  })

  it('names each target by its repository when the engine names it, else by its push prefix', async () => {
    const named: PublicationTarget = { ...listed, repository: 'acme/app' }
    const other: PublicationTarget = { ...listed, id: 'tg-2' }
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [named, other],
    })
    vi.spyOn(gitpublishApi, 'intents').mockImplementation(async (target) => ({
      items: target === 'tg-1' ? [opened()] : [docs],
    }))
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(within(rows[0]!).getByText('acme/app, under agents/')).toBeVisible()
    expect(within(rows[1]!).getByText('agents/')).toBeVisible()
    // A person does not know a target by its internal id.
    expect(within(table).queryByText('tg-1')).toBeNull()
    expect(within(table).queryByText('tg-2')).toBeNull()
  })

  it('says a pull request merged here is merged, though nothing read it again', async () => {
    const merge = intent({
      id: 'in-8',
      effect: 'merge',
      state: 'applied',
      receipt: 'caused',
      requested: { number: 7, method: 'merge' },
      observed: { present: true, merged: true, source: 'dispatcher' },
    })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [opened(), merge],
    })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(1)
    expect(within(rows[0]!).getByText('Merged')).toBeVisible()
    expect(within(rows[0]!).queryByText('Opened')).toBeNull()
  })

  it.each(['newest', 'oldest'] as const)(
    'shows a pull request once, as its newest intent saw it, with the %s listed first',
    async (first) => {
      const again = opened({
        id: 'in-9',
        state: 'adopted',
        receipt: 'existing_effect',
        requested: {
          head_ref: 'agents/export',
          base: 'main',
          commit: SHA,
          title: 'Add the export and its test',
        },
        observed: {
          present: true,
          merged: false,
          number: 7,
          url: 'https://github.com/acme/app/pull/7',
          at: '2026-11-10T12:00:00Z',
        },
      })
      // Either order gives the same row: it is chosen by when, not by order.
      vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
        items: first === 'newest' ? [again, opened()] : [opened(), again],
      })
      renderIntel(<GitPublicationView />)
      const table = await screen.findByRole('table', { name: 'Publications' })
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows).toHaveLength(1)
      expect(
        within(rows[0]!).getByRole('link', {
          name: '#7 Add the export and its test',
        }),
      ).toBeVisible()
    },
  )

  it('says none was published or opened yet, and where to do it', async () => {
    renderIntel(<GitPublicationView />)
    expect(
      await screen.findByText('Nothing was published here yet'),
    ).toBeVisible()
    expect(
      screen.getByText(
        'Publish from a session’s Changes panel, or from a target here.',
      ),
    ).toBeVisible()
    await userEvent.click(screen.getByRole('radio', { name: 'Pull requests' }))
    expect(
      await screen.findByText('No pull request was opened here yet'),
    ).toBeVisible()
    expect(
      screen.getByText(
        'Open one from a session’s Changes panel, or from a target here.',
      ),
    ).toBeVisible()
  })

  it('says a failed read with Retry, never that none was opened', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockRejectedValue(
      new ApiError(500, 'internal', 'internal'),
    )
    renderIntel(<GitPublicationView />)
    expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
    expect(
      screen.queryByText('No pull request was opened here yet'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('Nothing was published here yet'),
    ).not.toBeInTheDocument()
  })
})

describe('the publications on these targets', () => {
  const at = (time: string) => `2026-11-10T${time}:00Z`
  // The engine's shapes: an approval's id is a UUIDv7, its times are fixed-width
  // UTC, and its subject is the target's id with the plan bound after it.
  const stamp = (time: string) => `2026-11-10T${time}:00.000000000Z`
  const GIVEN = '019a6d2e-3f10-7c41-9b2a-5e8d0c1f2a3b'
  const WAITING = '019a6d4b-8e21-7f05-a3c6-1d9e2f0b4c5d'
  const PLAN =
    '#plan=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08'
  const fix = intent({
    id: 'in-2',
    state: 'applied',
    receipt: 'caused',
    observed: { present: true, merged: false, sha: SHA, at: at('09:00') },
    session_run: 'run-1',
  })
  const fixPull = intent({
    id: 'in-3',
    effect: 'pull_request',
    state: 'applied',
    receipt: 'caused',
    requested: {
      head_ref: 'agents/fix',
      base: 'main',
      commit: SHA,
      title: 'Fix it',
    },
    observed: { present: true, merged: false, number: 8, at: at('09:01') },
    proposal: { session_run: 'run-1', approval_id: GIVEN },
  })
  const docsPush = intent({
    id: 'in-4',
    state: 'applied',
    receipt: 'caused',
    requested: { ref: 'refs/heads/agents/docs', commit: SHA, tree: TREE },
    observed: { present: true, merged: false, sha: SHA, at: at('08:00') },
  })
  const held = (over: Partial<HeldApproval> = {}): HeldApproval => ({
    id: WAITING,
    action: 'gitpublish.push',
    subject_kind: 'gitpublish.target',
    subject_ref: `tg-1${PLAN}`,
    requested_by: 'user:u-1',
    status: 'pending',
    required_approvals: 1,
    approve_count: 0,
    reject_count: 0,
    escalated: false,
    created_at: stamp('11:00'),
    may_decide: false,
    ...over,
  })
  // An approved grant as the engine sends it: a person approved it, and when.
  const granted = (over: Partial<HeldApproval> = {}) =>
    held({
      status: 'approved',
      approve_count: 1,
      decided_at: stamp('11:05'),
      ...over,
    })
  // The approvals list as the engine answers: the asked action and status only.
  const answerApprovals = (items: HeldApproval[]) =>
    vi
      .spyOn(governanceApi, 'listApprovals')
      .mockImplementation(async (params) => ({
        items: items.filter(
          (a) =>
            a.action === params?.action &&
            (!params?.status || a.status === params?.status),
        ),
        has_more: false,
      }))

  it('lists what went out and what waits, newest first, with its approval and its session', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [docsPush, fix, fixPull],
    })
    // GIVEN was given and its push went out: that push is its row.
    const approvals = answerApprovals([
      held(),
      held({
        id: GIVEN,
        status: 'approved',
        approve_count: 1,
        created_at: stamp('08:58'),
        decided_at: stamp('08:59'),
      }),
    ])
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(3)
    // Only what the list shows is asked for: what waits, and what was approved.
    for (const action of ['gitpublish.push', 'gitpublish.pull_request'])
      for (const status of ['pending', 'approved'])
        expect(approvals).toHaveBeenCalledWith({ action, status, limit: 1000 })
    expect(approvals).toHaveBeenCalledTimes(4)
    // What waits: its target, and its approval with the door to it.
    expect(within(rows[0]!).getByText('agents/')).toBeVisible()
    expect(
      within(rows[0]!).getByRole('link', {
        name: 'Waiting for another person’s approval: the push to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${WAITING}`))
    // A branch, its pull request, the approval given, the session it came from.
    expect(within(rows[1]!).getByText('agents/fix')).toBeVisible()
    expect(rows[1]).toHaveTextContent('agents/fix → main')
    expect(branchesOf(rows[1]!)).toEqual(['agents/fix', 'main'])
    expect(within(rows[1]!).getByText('#8 Fix it')).toBeVisible()
    expect(within(rows[1]!).getByText('Opened')).toBeVisible()
    expect(within(rows[1]!).getByText('Approved')).toBeVisible()
    const session = within(rows[1]!).getByRole('link', {
      name: 'Open the session that published agents/fix to agents/',
    })
    expect(session).toHaveAttribute(
      'href',
      expect.stringContaining('session=run%3Arun-1'),
    )
    expect(session).not.toHaveAttribute('title')
    // Nobody was asked, and no session is named: its cell is empty.
    expect(within(rows[2]!).getByText('agents/docs')).toBeVisible()
    expect(within(rows[2]!).getByText('Not asked')).toBeVisible()
    expect(within(rows[2]!).getAllByRole('cell').at(-1)).toBeEmptyDOMElement()
    // What waits has no pull request yet.
    await userEvent.click(screen.getByRole('radio', { name: 'Pull requests' }))
    const pulls = screen.getByRole('table', { name: 'Publications' })
    expect(within(pulls).getAllByRole('row').slice(1)).toHaveLength(1)
  })

  it('keeps a merged pull request beside the next one opened from its branch', async () => {
    const merged = intent({
      id: 'in-6',
      effect: 'pull_request',
      state: 'applied',
      receipt: 'caused',
      requested: {
        head_ref: 'agents/fix',
        base: 'main',
        commit: SHA,
        title: 'Fix it',
      },
      observed: { present: true, merged: true, number: 3, at: at('07:00') },
    })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({
      items: [merged, fix, fixPull],
    })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    // The push after the merge went into the pull request opened after it.
    expect(within(rows[0]!).getByText('#8 Fix it')).toBeVisible()
    expect(within(rows[0]!).getByText('Opened')).toBeVisible()
    expect(within(rows[0]!).getByRole('link')).toHaveAttribute(
      'href',
      expect.stringContaining('session=run%3Arun-1'),
    )
    expect(within(rows[1]!).getByText('#3 Fix it')).toBeVisible()
    expect(within(rows[1]!).getByText('Merged')).toBeVisible()
    expect(within(rows[1]!).queryByRole('link')).toBeNull()
    await userEvent.click(screen.getByRole('radio', { name: 'Pull requests' }))
    const pulls = screen.getByRole('table', { name: 'Publications' })
    expect(within(pulls).getAllByRole('row').slice(1)).toHaveLength(2)
  })

  it('lists a held pull request, and says what each holds in the Push panel’s words', async () => {
    const approvals = answerApprovals([
      held(),
      held({
        id: GIVEN,
        action: 'gitpublish.pull_request',
        created_at: stamp('10:00'),
      }),
    ])
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(approvals).toHaveBeenCalledWith(
      expect.objectContaining({ action: 'gitpublish.pull_request' }),
    )
    expect(within(rows[0]!).getByText('Push')).toBeVisible()
    expect(within(rows[1]!).getByText('Open pull request')).toBeVisible()
    expect(
      within(rows[1]!).getByRole('link', {
        name: 'Waiting for another person’s approval: the pull request to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${GIVEN}`))
    // A held pull request is a pull request: it is listed with them.
    await userEvent.click(screen.getByRole('radio', { name: 'Pull requests' }))
    const pulls = screen.getByRole('table', { name: 'Publications' })
    const only = within(pulls).getAllByRole('row').slice(1)
    expect(only).toHaveLength(1)
    expect(within(only[0]!).getByText('Open pull request')).toBeVisible()
  })

  it('names each link by what it published or holds, and where', async () => {
    const bots: PublicationTarget = {
      ...listed,
      id: 'tg-2',
      push_prefix: 'bots/',
    }
    const botsPush = intent({
      ...docsPush,
      id: 'in-5',
      target_id: 'tg-2',
      requested: { ref: 'refs/heads/bots/docs', commit: SHA, tree: TREE },
      session_run: 'run-2',
    })
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [listed, bots],
    })
    vi.spyOn(gitpublishApi, 'intents').mockImplementation(async (target) => ({
      items: target === 'tg-1' ? [fix] : [botsPush],
    }))
    answerApprovals([
      held(),
      held({
        id: GIVEN,
        action: 'gitpublish.pull_request',
        subject_ref: `tg-2${PLAN}`,
        created_at: stamp('10:00'),
      }),
    ])
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    // Each link is found by its own name: the visible words, then which one.
    expect(
      within(table).getByRole('link', {
        name: 'Open the session that published agents/fix to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining('session=run%3Arun-1'))
    expect(
      within(table).getByRole('link', {
        name: 'Open the session that published bots/docs to bots/',
      }),
    ).toHaveAttribute('href', expect.stringContaining('session=run%3Arun-2'))
    expect(
      within(table).getByRole('link', {
        name: 'Waiting for another person’s approval: the push to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${WAITING}`))
    expect(
      within(table).getByRole('link', {
        name: 'Waiting for another person’s approval: the pull request to bots/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${GIVEN}`))
    // The visible words stay short.
    expect(within(table).getAllByText('Open the session')).toHaveLength(2)
    expect(
      within(table).getAllByText('Waiting for another person’s approval'),
    ).toHaveLength(2)
  })

  it.each([
    ['pending', 'Waiting for another person’s approval'],
    ['approved', 'Approved, not used'],
  ])(
    'says what a held publication waits on when its approval is %s',
    async (status, words) => {
      answerApprovals([status === 'pending' ? held() : granted()])
      renderIntel(<GitPublicationView />)
      const table = await screen.findByRole('table', { name: 'Publications' })
      expect(
        within(table).getByRole('link', {
          name: `${words}: the push to agents/`,
        }),
      ).toBeVisible()
    },
  )

  it('says nothing waits when the approved push went out and the host refused it', async () => {
    const refused = intent({
      id: 'in-5',
      state: 'rejected',
      receipt: 'none',
      proposal: { session_run: 'run-1', approval_id: WAITING },
    })
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [refused] })
    answerApprovals([granted()])
    renderIntel(<GitPublicationView />)
    expect(
      await screen.findByText('Nothing was published here yet'),
    ).toBeVisible()
    expect(screen.queryByText('Approved, not used')).toBeNull()
  })

  it('lists no approved grant past its expiry, which the engine no longer spends', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-10T12:00:00Z'))
    try {
      answerApprovals([
        granted({ expires_at: stamp('11:30') }),
        granted({
          id: GIVEN,
          created_at: stamp('11:40'),
          decided_at: stamp('11:45'),
          expires_at: stamp('12:30'),
        }),
      ])
      renderIntel(<GitPublicationView />)
      const table = await screen.findByRole('table', { name: 'Publications' })
      const rows = within(table).getAllByRole('row').slice(1)
      expect(rows).toHaveLength(1)
      expect(
        within(rows[0]!).getByRole('link', {
          name: 'Approved, not used: the push to agents/',
        }),
      ).toHaveAttribute('href', expect.stringContaining(`approval=${GIVEN}`))
    } finally {
      vi.useRealTimers()
    }
  })

  it('says nothing waits for an approval already spent, though no intent names it', async () => {
    answerApprovals([granted({ consumed: true })])
    renderIntel(<GitPublicationView />)
    expect(
      await screen.findByText('Nothing was published here yet'),
    ).toBeVisible()
  })

  it('follows the approvals to their last page', async () => {
    const newer = held({ id: GIVEN, created_at: stamp('12:00') })
    const approvals = vi
      .spyOn(governanceApi, 'listApprovals')
      .mockImplementation(async (params) => {
        if (
          params?.action !== 'gitpublish.push' ||
          params?.status !== 'pending'
        )
          return { items: [], has_more: false }
        return params?.cursor === 'page-2'
          ? { items: [newer], has_more: false }
          : { items: [held()], cursor: 'page-2', has_more: true }
      })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(
      within(rows[0]!).getByRole('link', {
        name: 'Waiting for another person’s approval: the push to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${GIVEN}`))
    expect(approvals).toHaveBeenCalledWith({
      action: 'gitpublish.push',
      status: 'pending',
      limit: 1000,
      cursor: 'page-2',
    })
    expect(screen.queryByText(/approvals were read/)).toBeNull()
  })

  it('lists an approval decided between its two reads once, as decided', async () => {
    // One read saw it waiting, and the other, a moment later, approved.
    answerApprovals([held(), granted()])
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(1)
    expect(
      within(rows[0]!).getByRole('link', {
        name: 'Approved, not used: the push to agents/',
      }),
    ).toHaveAttribute('href', expect.stringContaining(`approval=${WAITING}`))
  })

  it('says plainly that it read only the oldest approvals when they go on', async () => {
    let page = 0
    const approvals = vi
      .spyOn(governanceApi, 'listApprovals')
      .mockImplementation(async (params) => {
        if (
          params?.action !== 'gitpublish.push' ||
          params?.status !== 'pending'
        )
          return { items: [], has_more: false }
        page += 1
        const id = `${WAITING.slice(0, -2)}${String(page).padStart(2, '0')}`
        return { items: [held({ id })], cursor: `page-${page}`, has_more: true }
      })
    renderIntel(<GitPublicationView />)
    expect(
      await screen.findByText(
        'Only the oldest 5 approvals were read. Approvals lists them all.',
      ),
    ).toBeVisible()
    // Five pages of one action and state, and one of each other: then it stops.
    expect(approvals).toHaveBeenCalledTimes(8)
  })

  it('says it read only the oldest approvals when spent grants fill the read', async () => {
    // The route cannot leave out what was spent, so spent grants can fill it.
    let page = 0
    const approvals = vi
      .spyOn(governanceApi, 'listApprovals')
      .mockImplementation(async (params) => {
        if (
          params?.action !== 'gitpublish.push' ||
          params?.status !== 'approved'
        )
          return { items: [], has_more: false }
        page += 1
        const id = `${GIVEN.slice(0, -2)}${String(page).padStart(2, '0')}`
        return {
          items: [granted({ id, consumed: true })],
          cursor: `page-${page}`,
          has_more: true,
        }
      })
    renderIntel(<GitPublicationView />)
    expect(
      await screen.findByText(
        'Only the oldest 5 approvals were read. Approvals lists them all.',
      ),
    ).toBeVisible()
    expect(screen.getByText('Nothing was published here yet')).toBeVisible()
    expect(approvals).toHaveBeenCalledTimes(8)
  })

  it('titles the session with the name the console already holds, and reads no session', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [fix] })
    const read = vi.spyOn(agentOpsApi, 'getRun').mockResolvedValue({} as RunDTO)
    const queryClient = createTestQueryClient()
    // The console holds a run it has read; this client drops at once what no
    // view watches (gcTime 0), and the list reads the cache without watching.
    queryClient.setQueryDefaults(agentOpsKeys.run('tenant-1', 'run-1'), {
      gcTime: Infinity,
    })
    queryClient.setQueryData(agentOpsKeys.run('tenant-1', 'run-1'), {
      run_ref: 'run-1',
      name: 'export-fix',
    } as RunDTO)
    renderIntel(<GitPublicationView />, { queryClient })
    expect(
      await screen.findByRole('link', {
        name: 'Open the session that published agents/fix to agents/',
      }),
    ).toHaveAttribute('title', 'export-fix')
    expect(read).not.toHaveBeenCalled()
  })

  it('says a session as plain text to a person who may not open it', async () => {
    auth.can = (permission) => permission !== 'sessions:run:read'
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [fix] })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    expect(within(table).getByText('A session')).toBeVisible()
    expect(within(table).queryByRole('link')).toBeNull()
  })

  it('lists what went out without the approvals, and says who sees what waits', async () => {
    auth.can = (permission) => permission !== 'governance:approval:read'
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [docsPush] })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    expect(within(table).getAllByRole('row').slice(1)).toHaveLength(1)
    expect(
      screen.getByText(
        'What waits for another person’s approval is listed for people who can read approvals.',
      ),
    ).toBeVisible()
    expect(governanceApi.listApprovals).not.toHaveBeenCalled()
  })

  it('reads no approvals while the approvals module is off, and says nothing of them', async () => {
    useModulesStore.getState().setOff(['governance'])
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [docsPush] })
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    expect(within(table).getAllByRole('row').slice(1)).toHaveLength(1)
    expect(governanceApi.listApprovals).not.toHaveBeenCalled()
    expect(screen.queryByText(/listed for people who/)).toBeNull()
  })

  it('shows what it read, and names each target it could not read, with its own Retry', async () => {
    const bots: PublicationTarget = {
      ...listed,
      id: 'tg-2',
      push_prefix: 'bots/',
    }
    const botsPush = intent({
      ...docsPush,
      id: 'in-5',
      target_id: 'tg-2',
      requested: { ref: 'refs/heads/bots/docs', commit: SHA, tree: TREE },
    })
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [listed, bots],
    })
    const intents = vi
      .spyOn(gitpublishApi, 'intents')
      .mockImplementation(async (target) => {
        if (target === 'tg-1') throw new ApiError(500, 'internal', 'internal')
        return { items: [botsPush] }
      })
    const asked = (target: string) =>
      intents.mock.calls.filter(([id]) => id === target).length
    renderIntel(<GitPublicationView />)
    // What was read is listed.
    const table = await screen.findByRole('table', { name: 'Publications' })
    expect(within(table).getAllByRole('row').slice(1)).toHaveLength(1)
    expect(within(table).getByText('bots/docs')).toBeVisible()
    // What could not be read is named, with its own Retry.
    const failed = screen.getByText(
      'What was published to agents/ could not be read',
    )
    expect(failed).toBeVisible()
    const alert = screen.getAllByRole('alert').find((a) => a.contains(failed))!
    expect(asked('tg-1')).toBe(1)
    expect(asked('tg-2')).toBe(1)
    await userEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(asked('tg-1')).toBe(2))
    expect(asked('tg-2')).toBe(1)
  })

  it('lists what went out when what waits could not be read, and says so', async () => {
    vi.spyOn(gitpublishApi, 'intents').mockResolvedValue({ items: [docsPush] })
    vi.spyOn(governanceApi, 'listApprovals').mockRejectedValue(
      new ApiError(500, 'internal', 'internal'),
    )
    renderIntel(<GitPublicationView />)
    const table = await screen.findByRole('table', { name: 'Publications' })
    expect(within(table).getByText('agents/docs')).toBeVisible()
    expect(
      screen.getByText('What waits for approval could not be read'),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeVisible()
  })

  it('says a failed approvals read with Retry, never that nothing was published', async () => {
    vi.spyOn(governanceApi, 'listApprovals').mockRejectedValue(
      new ApiError(500, 'internal', 'internal'),
    )
    renderIntel(<GitPublicationView />)
    expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
    expect(
      screen.queryByText('Nothing was published here yet'),
    ).not.toBeInTheDocument()
  })
})
