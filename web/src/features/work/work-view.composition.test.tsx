// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
}))
const comms = vi.hoisted(() => ({ offerHandoff: vi.fn() }))
// The offer host is real; only the transport under it is replaced, so this file
// measures the ACTUAL composition WorkView → ItemDetailSheet → HandoffOfferHost.
vi.mock('@/features/communications/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...comms }
})
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
vi.mock('@/stores/workspace', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return real
})

const auth = vi.hoisted(() => ({
  can: ((_p: string) => true) as (p: string) => boolean,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.can(p),
    isSuperadmin: false,
    principal: {
      kind: 'user',
      user_id: 'admin',
      actor: 'user:admin',
      display_name: 'Admin',
      superadmin: false,
      grants: [],
    },
  }),
}))
// The stream is replaced by a HANDLE on the view's own `onEvent`, so a case can replay
// a burst through exactly the path the engine's SSE frames take.
const stream = vi.hoisted(() => ({ emit: null as null | (() => void) }))
vi.mock('./stream', () => ({
  useWorkStream: ({ onEvent }: { onEvent: () => void }) => {
    stream.emit = onEvent
    return { status: 'connected' as const }
  },
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})
vi.mock('@/lib/hooks/use-url-state', async () => {
  const react = await import('react')
  return {
    useUrlState: () => {
      const [state, setState] = react.useState<
        Record<string, string | undefined>
      >({})
      const patch = react.useCallback(
        (p: Record<string, string | undefined>) => {
          setState((prev) => {
            const next = { ...prev }
            for (const [k, v] of Object.entries(p)) {
              if (v === undefined || v === '') delete next[k]
              else next[k] = v
            }
            return next
          })
        },
        [],
      )
      return [state, patch]
    },
  }
})

import { WorkView } from './work-view'
import { WORK_REFRESH_WINDOW_MS } from './coalesce'
import { useWorkspaceStore } from '@/stores/workspace'
import './i18n'
import '@/features/_intel'
import '@/features/communications/i18n'

const ITEM_ID = '0192f2c0-aaaa-7000-8000-00000000aa01'

const item = {
  id: ITEM_ID,
  workspace_id: 'workspace-1',
  version: 3,
  created_at: '2026-08-26T00:00:00Z',
  updated_at: '2026-08-26T00:00:00Z',
  work_kind: 'implementation',
  title: 'Composition work item',
  brief_md: 'Opened from the real work cockpit.',
  brief_hash: 'brief-hash',
  context_refs: [],
  status: 'active',
  priority: 'p1',
  owner_kind: 'user',
  owner_ref: 'admin',
  owner_epoch: 1,
  provenance_kind: 'human',
  provenance_ref: 'composition-test',
  acceptance_revision: 0,
  last_event_seq: 1,
  dependency_blocked: false,
  claimable: false,
  leased: false,
  orphaned: false,
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  api.listWorkItems.mockResolvedValue({ items: [item], has_more: false })
  api.getWorkItem.mockResolvedValue({
    snapshot: { item, acceptance: [], dependencies: [] },
    etag: '"v3"',
  })
  useWorkspaceStore.setState({
    activeWorkspace: 'workspace-1',
    activeWorkspaceName: 'Billing',
  })
})

describe('WorkView composition', () => {
  it('opens a listed item through the real cockpit and renders its live detail', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)

    const row = await screen.findByRole('button', {
      name: /composition work item/i,
    })
    expect(
      row,
      'Rendered: the real WorkView must expose the engine-listed work item as an action',
    ).toBeEnabled()
    await user.click(row)

    await waitFor(() =>
      expect(
        api.getWorkItem,
        'Fired: the parent row action must dispatch getWorkItem for the selected id',
        // 83b4685f8 bound tenant scope to every operation: assert the scope,
        // not just the id — an unscoped read is the defect that commit closed.
      ).toHaveBeenCalledWith(
        ITEM_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText('Composition work item'),
      'Effect: the selected engine snapshot must be visible in the parent-mounted detail sheet',
    ).toBeVisible()
    expect(
      within(dialog).getByText('Opened from the real work cockpit.'),
    ).toBeVisible()
    expect(
      within(dialog).getByText('"v3"'),
      'Effect: the detail sheet must paint the ETag returned by the real item handler',
    ).toBeVisible()
  })
})

/* ── K3 I3: the offer entry, mounted for real ─────────────────────────────────── */

describe('WorkView composition — the handoff offer entry', () => {
  it('mounts the communications-owned host beside the item sheet and re-reads the item FRESH for the offer', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    // One read for the sheet.
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(1))

    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )

    // A SECOND, INDEPENDENT READ. The sheet already has the item on screen; the
    //    offer does not inherit that body as authority, so the adapter asks again
    //    with the EXPLICIT captured tenant and the host's own abort signal.
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(2))
    expect(api.getWorkItem.mock.calls[1]).toEqual([
      ITEM_ID,
      { tenant: 't1' },
      expect.any(AbortSignal),
    ])

    const offer = await screen.findByRole('dialog', { name: 'Offer handoff' })
    // The projection the adapter returns is what the offer screen shows.
    expect(within(offer).getByText('"v3"')).toBeVisible()
    expect(within(offer).getByText('user:admin')).toBeVisible()
  })

  it('the offer action is INDEPENDENT of the WorkItem transition permission', async () => {
    // A principal who may send on a channel but may NOT transition work.
    auth.can = (p: string) => p === 'sessions:message-send:write'
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    expect(screen.getByRole('button', { name: 'Mark ready' })).toBeDisabled()
    expect(screen.getByText(/sessions:work:write/)).toBeVisible()
    expect(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    ).toBeEnabled()
  })

  it('without message-send:write the offer entry is disabled with the reason, while the transitions remain', async () => {
    auth.can = (p: string) => p !== 'sessions:message-send:write'
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    expect(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    ).toBeDisabled()
    expect(screen.getByText(/sessions:message-send:write/)).toBeVisible()
    // POSITIVE CONTROL: the neighbouring work permission is untouched.
    expect(
      await screen.findByRole('button', { name: 'Mark ready' }),
    ).toBeEnabled()
  })

  it('reopening the SAME item is a NEW editor action: the host reads again rather than reusing the previous draft', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(2))

    // Close the offer panel and ask for the same item again.
    await user.click(
      within(
        await screen.findByRole('dialog', { name: 'Offer handoff' }),
      ).getByRole('button', { name: 'Close' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(3))
    expect(api.getWorkItem.mock.calls[2][0]).toBe(ITEM_ID)
    expect(
      await screen.findByRole('dialog', { name: 'Offer handoff' }),
    ).toBeVisible()
  })
})

describe('I3 correction — offer from the item sheet', () => {
  it('returns focus to the item-sheet control that opened the offer dialog', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    const opener = await screen.findByRole('button', { name: 'Offer handoff' })
    await user.click(opener)
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Offer handoff' }),
      ).toBeNull(),
    )
    // Settled focus, on the real opener inside the still-open item sheet.
    await waitFor(() => expect(document.activeElement).toBe(opener), {
      timeout: 1000,
    })
  })

  it('reopening the same item is a new invocation with its own fresh read', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(2))
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Offer handoff' }),
      ).toBeNull(),
    )

    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    // The validator a confirmation would bind comes from this read, not the last one.
    await waitFor(() => expect(api.getWorkItem).toHaveBeenCalledTimes(3))
  })

  it('states the local tracking limits on the offer panel', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /composition work item/i }),
    )
    await screen.findByRole('dialog')
    await user.click(
      await screen.findByRole('button', { name: 'Offer handoff' }),
    )
    await screen.findByRole('dialog', { name: 'Offer handoff' })
    const limits = document.querySelector(
      '[data-slot="handoff-offer-tracking-limits"]',
    ) as HTMLElement
    expect(limits).toHaveTextContent(/Hiding this panel keeps local tracking/i)
    expect(limits).toHaveTextContent(
      /cannot cancel or roll back a command already sent/i,
    )
  })
})

describe('WorkView — a stream burst costs two list reads, not sixty', () => {
  it('counts the requests, because a test that counted refreshes would have passed on the defect', async () => {
    // ⛔ WHAT WAS MEASURED, and it is the reason this case exists. The operator walk of
    //    2026-09-18 opened `/work` ONCE against a seeded estate and recorded 60 aborted
    //    reads of `GET /v1/m/sessions/work-items?limit=100` — one per stream event,
    //    each cancelling the one before it, because the view invalidated the whole work
    //    key on every event while the list query carried an abort signal.
    //
    // ⛔ AND THE ASSERTION IS A COUNT ON PURPOSE. "the list is up to date" was true on
    //    the defect: it was true sixty times. The only thing that tells the fix from the
    //    defect is how many requests it took.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      renderIntel(<WorkView />)
      await waitFor(() => expect(api.listWorkItems).toHaveBeenCalled())
      const afterFirstRead = api.listWorkItems.mock.calls.length
      expect(stream.emit).not.toBeNull()

      await act(async () => {
        for (let i = 0; i < 60; i++) stream.emit?.()
      })
      await waitFor(() =>
        expect(api.listWorkItems.mock.calls.length).toBeGreaterThan(
          afterFirstRead,
        ),
      )
      // The leading edge fired one read: sixty events so far, one read so far.
      expect(api.listWorkItems.mock.calls.length - afterFirstRead).toBe(1)

      // The window closes and spends the ONE refresh it owes for the other 59.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(WORK_REFRESH_WINDOW_MS)
      })
      await waitFor(() =>
        expect(api.listWorkItems.mock.calls.length - afterFirstRead).toBe(2),
      )
      // Two. Never sixty.
      expect(api.listWorkItems.mock.calls.length - afterFirstRead).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })
})
