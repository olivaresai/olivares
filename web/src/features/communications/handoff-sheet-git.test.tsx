// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A handoff that names where its work is in git (K4.A2): the branch and the commit are
// shown as inert text, and an owner who may start a session gets one action that hands
// the commit (or the branch, when no commit is named) to the launch. A handoff that
// names neither is the sheet it always was.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({ getHandoffDetail: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})

import { HandoffSheet } from './handoff-sheet'
import {
  handoffDetailOf,
  HANDOFF_DELIVERY_ID,
  renderWithQuery,
  scopeOf,
} from './test-harness'
import type { HandoffGitRefs } from './types'
import './i18n'

const SHA = '0123456789abcdef0123456789abcdef01234567'
const BRANCH = 'olivares/3f9a1c20'

function detailWith(refs: HandoffGitRefs) {
  const detail = handoffDetailOf()
  Object.assign(detail.content, refs)
  return detail
}

function mount(onOpenWorktree?: (from: string) => void) {
  return renderWithQuery(() => (
    <HandoffSheet
      open
      onOpenChange={() => {}}
      deliveryId={HANDOFF_DELIVERY_ID}
      scope={scopeOf()}
      canDeliveryRead
      canRespond
      onRespond={vi.fn()}
      onOpenWorktree={onOpenWorktree}
    />
  ))
}

const openButton = () =>
  screen.queryByRole('button', { name: 'Open in a new session worktree' })

beforeEach(() => {
  vi.clearAllMocks()
})

describe('HandoffSheet — where the handed-over work is in git', () => {
  it('shows the branch and the commit as text and hands the commit to the launch', async () => {
    api.getHandoffDetail.mockResolvedValue(
      detailWith({ branch: BRANCH, sha: SHA }),
    )
    const onOpenWorktree = vi.fn()
    const user = userEvent.setup()
    mount(onOpenWorktree)

    expect(await screen.findByText(SHA)).toBeInTheDocument()
    expect(screen.getByText(BRANCH)).toBeInTheDocument()
    expect(screen.getByText('Branch')).toBeInTheDocument()
    expect(screen.getByText('Commit')).toBeInTheDocument()
    expect(
      screen.getByText(/Starts a session in a worktree of its own/),
    ).toBeInTheDocument()
    await user.click(openButton() as HTMLElement)

    expect(onOpenWorktree).toHaveBeenCalledOnce()
    expect(onOpenWorktree).toHaveBeenCalledWith(SHA)
  })

  it('hands over the branch when the handoff names no commit', async () => {
    api.getHandoffDetail.mockResolvedValue(detailWith({ branch: BRANCH }))
    const onOpenWorktree = vi.fn()
    const user = userEvent.setup()
    mount(onOpenWorktree)

    expect(await screen.findByText(BRANCH)).toBeInTheDocument()
    expect(screen.queryByText('Commit')).not.toBeInTheDocument()
    await user.click(openButton() as HTMLElement)

    expect(onOpenWorktree).toHaveBeenCalledWith(BRANCH)
  })

  it('offers no action to an owner who may not start a session', async () => {
    api.getHandoffDetail.mockResolvedValue(
      detailWith({ branch: BRANCH, sha: SHA }),
    )
    mount(undefined)

    expect(await screen.findByText(SHA)).toBeInTheDocument()
    expect(openButton()).not.toBeInTheDocument()
  })

  it('is the sheet it always was for a handoff that names neither', async () => {
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    mount(vi.fn())

    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalled())
    expect(await screen.findByText('Next action')).toBeInTheDocument()
    expect(screen.queryByText('Branch')).not.toBeInTheDocument()
    expect(screen.queryByText('Commit')).not.toBeInTheDocument()
    expect(openButton()).not.toBeInTheDocument()
  })
})
