// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor, within } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { useWorkspaceStore } from '@/stores/workspace'
import { gitpublishApi } from './api'
import { GitPublicationView } from './gitpublish-view'
import type { PublicationIntent, PublicationTarget } from './types'

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
})

async function openTarget() {
  renderIntel(<GitPublicationView />)
  await userEvent.click(
    await screen.findByRole('button', { name: 'Open target tg-1' }),
  )
  return screen.findByRole('dialog', { name: 'Target tg-1' })
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
      expect(
        within(sheet).queryByText('Requires gitpublish:pull_request:write'),
      ).not.toBeInTheDocument()
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
    expect(
      within(sheet).getByText('Requires gitpublish:push:write'),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByText(
        'Requires gitpublish:merge:admin and the extra check for administrative actions',
      ),
    ).toBeInTheDocument()
  })

  it('asks for no extra check when this session already meets the deployment’s policy', async () => {
    // The default policy (none): a password session satisfies the step-up, as the engine says.
    auth.principal = { aal: 1, step_up_satisfied: true }
    const sheet = await openTarget()
    expect(
      await within(sheet).findByText('Requires gitpublish:merge:admin'),
    ).toBeInTheDocument()
    expect(
      within(sheet).queryByText(/AAL3|extra check/),
    ).not.toBeInTheDocument()
  })

  it('asks for no extra check from a session an older engine reports at AAL3', async () => {
    // An engine without step_up_satisfied: a hardware step-up meets any policy.
    auth.principal = { aal: 3 }
    const sheet = await openTarget()
    expect(
      await within(sheet).findByText('Requires gitpublish:merge:admin'),
    ).toBeInTheDocument()
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
