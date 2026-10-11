// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'
import { gitpublishApi } from './api'
import { SessionPublish } from './session-publish'
import type { PublicationIntent, PublicationTarget } from './types'

const auth = vi.hoisted(() => ({
  can: (_permission: string): boolean => true,
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'tenant-1', can: auth.can }),
}))

const RUN = '0b3f6c1e-5d2a-4c8e-9f10-2a3b4c5d6e7f'
const BRANCH = 'agent/2a3b4c5d6e7f'
const SHA = 'a'.repeat(40)
const TREE = 'b'.repeat(40)

const target: PublicationTarget = {
  id: 'tg-1',
  workspace_id: 'ws-1',
  host: 'github',
  push_prefix: 'agent/',
  merge_bases: ['main'],
  version: 2,
}

const run = {
  run_ref: RUN,
  state: 'idle',
  authz_workspace_id: 'ws-1',
  worktree_branch: BRANCH,
} as RunDTO

function applied(over: Partial<PublicationIntent> = {}): PublicationIntent {
  return {
    id: 'in-1',
    target_id: 'tg-1',
    target_version: 2,
    effect: 'push',
    operation_id: 'op-1',
    attempt: 1,
    state: 'applied',
    receipt: 'caused',
    requested: { ref: `refs/heads/${BRANCH}`, commit: SHA, tree: TREE },
    observed: { present: true, sha: SHA, merged: false },
    acknowledged: { acknowledged: true, status: 200 },
    ...over,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.can = () => true
  useModulesStore.getState().setOff([])
  vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({ items: [target] })
})

async function paste(field: HTMLElement, value: string) {
  await userEvent.click(field)
  await userEvent.paste(value)
}

/** Opens the session's Publish dialog and requests the push of SHA/TREE. */
async function requestPush() {
  renderIntel(<SessionPublish run={run} />)
  await userEvent.click(await screen.findByRole('button', { name: 'Publish' }))
  const dialog = await screen.findByRole('dialog', { name: 'Push a commit' })
  await paste(within(dialog).getByLabelText(/^Commit/), SHA)
  await paste(within(dialog).getByLabelText(/^Tree/), TREE)
  await userEvent.click(
    within(dialog).getByRole('button', { name: 'Request push' }),
  )
  return dialog
}

describe('publishing a session’s commit', () => {
  it('starts from the session’s run and branch', async () => {
    renderIntel(<SessionPublish run={run} />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Publish' }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Push a commit' })
    expect(within(dialog).getByText(RUN)).toBeInTheDocument()
    // Fixed by the session, so not offered as optional.
    expect(
      within(dialog).getByText(/^The session this push comes from/),
    ).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/Branch ref/)).toHaveValue(
      `refs/heads/${BRANCH}`,
    )
  })

  it('starts a branch outside the push prefix under it, where the engine accepts it', async () => {
    renderIntel(
      <SessionPublish
        run={{ ...run, worktree_branch: 'olivares/2a3b4c5d6e7f' }}
      />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Publish' }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Push a commit' })
    expect(within(dialog).getByLabelText(/Branch ref/)).toHaveValue(
      'refs/heads/agent/olivares/2a3b4c5d6e7f',
    )
  })

  it('pushes through the existing route with the session run, never a path', async () => {
    const push = vi.spyOn(gitpublishApi, 'push').mockResolvedValue(applied())
    const dialog = await requestPush()

    expect(
      await within(dialog).findByText('The request is settled'),
    ).toBeInTheDocument()
    expect(push).toHaveBeenCalledTimes(1)
    const [targetId, body, request] = push.mock.calls[0]!
    expect(targetId).toBe('tg-1')
    expect(body).toMatchObject({
      ref: `refs/heads/${BRANCH}`,
      commit: SHA,
      tree: TREE,
      session_run: RUN,
    })
    expect(request).toEqual({ tenant: 'tenant-1' })
  })

  it.each(['github', 'gitlab'] as const)(
    'offers a draft pull request after an applied %s push',
    async (host) => {
      vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
        items: [{ ...target, host }],
      })
      vi.spyOn(gitpublishApi, 'push').mockResolvedValue(applied())
      const open = vi.spyOn(gitpublishApi, 'openPullRequest').mockResolvedValue(
        applied({
          id: 'in-2',
          effect: 'pull_request',
          requested: { head_ref: BRANCH, base: 'main', commit: SHA },
          observed: { present: true, number: 7, head_sha: SHA, merged: false },
        }),
      )
      const pushDialog = await requestPush()
      await userEvent.click(
        await within(pushDialog).findByRole('button', {
          name: 'Open a draft pull request',
        }),
      )

      const prDialog = await screen.findByRole('dialog', {
        name: 'Open a pull request',
      })
      expect(within(prDialog).getByLabelText(/Head branch/)).toHaveValue(BRANCH)
      expect(within(prDialog).getByLabelText(/^Commit/)).toHaveValue(SHA)
      expect(within(prDialog).getByLabelText('Open as a draft')).toBeChecked()
      await paste(within(prDialog).getByLabelText(/^Title/), 'Session change')
      await userEvent.click(
        within(prDialog).getByRole('button', { name: 'Request pull request' }),
      )
      expect(
        await within(prDialog).findByText('The request is settled'),
      ).toBeInTheDocument()
      expect(open.mock.calls[0]![1]).toMatchObject({
        head_ref: BRANCH,
        base: 'main',
        commit: SHA,
        title: 'Session change',
        draft: true,
      })
    },
  )

  it.each(['git', undefined] as const)(
    'offers no draft pull request after an applied push when host is %s',
    async (host) => {
      vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
        items: [{ ...target, host }],
      })
      vi.spyOn(gitpublishApi, 'push').mockResolvedValue(applied())
      const dialog = await requestPush()
      await within(dialog).findByText('The request is settled')
      expect(
        within(dialog).queryByRole('button', {
          name: 'Open a draft pull request',
        }),
      ).not.toBeInTheDocument()
    },
  )

  it('offers no pull request to someone who may not open one', async () => {
    auth.can = (p) => p !== 'gitpublish:pull_request:write'
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(applied())
    const dialog = await requestPush()
    await within(dialog).findByText('The request is settled')
    expect(
      within(dialog).queryByRole('button', {
        name: 'Open a draft pull request',
      }),
    ).not.toBeInTheDocument()
  })

  it('offers no pull request after a push the host did not apply', async () => {
    vi.spyOn(gitpublishApi, 'push').mockResolvedValue(
      applied({ state: 'uncertain', receipt: 'none' }),
    )
    const dialog = await requestPush()
    await within(dialog).findByText('The outcome on the host is uncertain')
    expect(
      within(dialog).queryByRole('button', {
        name: 'Open a draft pull request',
      }),
    ).not.toBeInTheDocument()
  })

  it('names the refusal when the session folder cannot feed the repository', async () => {
    vi.spyOn(gitpublishApi, 'push').mockRejectedValue(
      new ApiError(422, 'session_source_refused', 'refused'),
    )
    const dialog = await requestPush()
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('session_source_refused')
    expect(alert).not.toHaveTextContent(
      'with a code this console does not describe',
    )
  })
})

describe('when there is nothing to publish to', () => {
  it('offers only the targets of the session’s own workspace', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockResolvedValue({
      items: [
        { ...target, id: 'tg-2', workspace_id: 'ws-2', push_prefix: 'other/' },
        target,
      ],
    })
    renderIntel(<SessionPublish run={run} />)
    expect(
      await screen.findByRole('button', { name: 'Publish' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('other/')).not.toBeInTheDocument()
  })

  it('reads nothing when Git publication is off on this installation', () => {
    useModulesStore.getState().setOff(['gitpublish'])
    renderIntel(<SessionPublish run={run} />)
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Publish' }),
    ).not.toBeInTheDocument()
  })

  it('reads nothing without the push permission', () => {
    auth.can = (p) => p !== 'gitpublish:push:write'
    renderIntel(<SessionPublish run={run} />)
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
  })

  it('reads nothing for a cleaned session, whose folder is gone', () => {
    renderIntel(<SessionPublish run={{ ...run, state: 'cleaned' }} />)
    expect(gitpublishApi.targets).not.toHaveBeenCalled()
  })

  it('says a failed read of the targets, instead of hiding the action', async () => {
    vi.spyOn(gitpublishApi, 'targets').mockRejectedValue(
      new ApiError(500, 'internal', 'boom'),
    )
    renderIntel(<SessionPublish run={run} />)
    expect(
      await screen.findByRole('button', { name: /retry/i }),
    ).toBeInTheDocument()
  })
})
