// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE BURST OF WORK EVENTS IS ONE READ OF THE LIST.
//
// The list query carries an abort signal, and `invalidateQueries` cancels the fetch in
// flight before it starts the next one. Invalidating on every stream frame therefore
// turned a burst of N events into N-1 aborted reads and one that answered — measured at
// about sixty aborted reads per visit to /work. What is pinned here is the mechanism
// that closes it: the burst is coalesced, the read happens once, after the last event,
// and no read is thrown away.
import { act, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
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
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
// The transport is replaced by a hand the test holds: whatever the view passes as
// `onEvent` is captured, so the test can deliver a burst exactly as the stream would.
const stream = vi.hoisted(() => ({
  onEvent: null as null | ((event: unknown) => void),
}))
vi.mock('./stream', () => ({
  useWorkStream: (opts: { onEvent: (event: unknown) => void }) => {
    stream.onEvent = opts.onEvent
    return { status: 'open' as const, cursor: null, unavailableCode: null }
  },
}))

import { WorkView } from './work-view'
import './i18n'
import '@/features/_intel'
import '@/features/communications/i18n'

const item = {
  id: 'work-1',
  workspace_id: 'workspace-1',
  version: 1,
  created_at: '2026-09-18T00:00:00Z',
  updated_at: '2026-09-18T00:00:00Z',
  work_kind: 'implementation',
  title: 'Burst work item',
  brief_md: '',
  brief_hash: 'brief-hash',
  context_refs: [],
  status: 'active',
  priority: 'p1',
  owner_kind: 'user',
  owner_ref: 'admin',
  owner_epoch: 1,
  provenance_kind: 'human',
  provenance_ref: 'stream-refresh-test',
  acceptance_revision: 0,
  last_event_seq: 1,
  dependency_blocked: false,
  claimable: false,
  leased: false,
  orphaned: false,
}

/** The abort signal of every list read, in call order. */
const signals: (AbortSignal | undefined)[] = []

beforeEach(() => {
  vi.clearAllMocks()
  signals.length = 0
  stream.onEvent = null
  api.listWorkItems.mockImplementation(
    (_params: unknown, _options: unknown, signal?: AbortSignal) => {
      signals.push(signal)
      // Slow enough that the next invalidation finds this read still in flight — the
      // condition under which the old code aborted it.
      return new Promise((resolve) =>
        setTimeout(() => resolve({ items: [item], has_more: false }), 40),
      )
    },
  )
})

describe('WorkView — the stream refreshes the list once per burst', () => {
  it('reads the list once after a burst of sixty events, aborting nothing', async () => {
    renderIntel(<WorkView />)
    await screen.findByRole('button', { name: /burst work item/i })
    expect(api.listWorkItems).toHaveBeenCalledTimes(1)
    expect(stream.onEvent, 'the view subscribed to the stream').not.toBeNull()

    const N = 60
    act(() => {
      for (let i = 0; i < N; i += 1)
        stream.onEvent?.({
          event_id: `evt-${i}`,
          event_type: 'work.item.updated',
          work_item_id: item.id,
        })
    })

    // The burst does refresh the list: the trailing event triggers the read.
    await waitFor(() =>
      expect(
        api.listWorkItems.mock.calls.length,
        'the burst must refresh the list',
      ).toBeGreaterThanOrEqual(2),
    )
    // …and then nothing else happens: one burst, one read.
    await new Promise((r) => setTimeout(r, 400))
    expect(
      api.listWorkItems.mock.calls.length,
      `sixty events must cost one read, not one per event (N=${N})`,
    ).toBe(2)
    expect(
      signals.filter((s) => s?.aborted).length,
      'no list read is started only to be aborted by the next event',
    ).toBe(0)
  })
})
