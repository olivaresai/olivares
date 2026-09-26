// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderIntel, screen, waitFor } from '@/test/intel'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
  getLease: vi.fn(),
}))
const urlHarness = vi.hoisted(() => ({
  initial: {} as Record<string, string | undefined>,
}))

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
vi.mock('./stream', () => ({
  useWorkStream: () => ({ status: 'connected' as const }),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})
vi.mock('@/lib/hooks/use-url-state', async () => {
  const react = await import('react')
  return {
    useUrlState: () => {
      const [state, setState] = react.useState({ ...urlHarness.initial })
      const patch = react.useCallback(
        (p: Record<string, string | undefined>) => {
          setState((prev: Record<string, string | undefined>) => {
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
import { useWorkspaceStore } from '@/stores/workspace'
import './i18n'
import '@/features/_intel'

const ITEM_ID = '0192f2c0-aaaa-7000-8000-00000000aa01'

const item = {
  id: ITEM_ID,
  workspace_id: 'workspace-1',
  version: 3,
  created_at: '2026-08-26T00:00:00Z',
  updated_at: '2026-08-26T00:00:00Z',
  work_kind: 'implementation',
  title: 'Reload work item',
  brief_md: 'State comes from the API after reload.',
  brief_hash: 'brief-hash',
  context_refs: [],
  status: 'active',
  priority: 'p1',
  owner_kind: 'user',
  owner_ref: 'admin',
  owner_epoch: 1,
  provenance_kind: 'human',
  provenance_ref: 'url-state-test',
  acceptance_revision: 0,
  last_event_seq: 1,
  dependency_blocked: false,
  claimable: false,
  leased: false,
  orphaned: false,
}

beforeEach(() => {
  vi.clearAllMocks()
  urlHarness.initial = {}
  api.listWorkItems.mockResolvedValue({ items: [item], has_more: false })
  api.getWorkItem.mockResolvedValue({
    snapshot: { item, acceptance: [], dependencies: [] },
    etag: '"v3"',
  })
  useWorkspaceStore.setState({
    activeWorkspace: 'workspace-1',
    activeWorkspaceName: 'Billing',
  })
  api.getLease.mockResolvedValue({
    lease: {
      workspace_id: 'workspace-1',
      work_item_id: ITEM_ID,
      fence: 0,
      state: 'vacant',
      renewal_count: 0,
      live: false,
      liveness_verdict: 'LIMPIO',
      liveness_code: 'ok',
    },
    etag: '"v3"',
  })
})

describe('WorkView address state', () => {
  it('opens a listed item by writing its id into the address, then reads the item from the API', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await user.click(
      await screen.findByRole('button', { name: /reload work item/i }),
    )
    await waitFor(() =>
      expect(api.getWorkItem).toHaveBeenCalledWith(
        ITEM_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(
      screen.getByText('State comes from the API after reload.'),
    ).toBeVisible()
  })

  it('re-opens the item and the lease tab from the address without client-only state', async () => {
    urlHarness.initial = { item: ITEM_ID, detail: 'lease' }
    renderIntel(<WorkView />)
    await waitFor(() =>
      expect(api.getWorkItem).toHaveBeenCalledWith(
        ITEM_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toBeVisible()
    await waitFor(() =>
      expect(
        dialog.querySelector('[data-slot="work-lease"]'),
        'the lease tab named in the address is the one shown',
      ).not.toBeNull(),
    )
  })

  it('ignores a non-id item in the address and does not fetch it', async () => {
    urlHarness.initial = { item: 'not-an-id' }
    renderIntel(<WorkView />)
    expect(
      await screen.findByText(
        /address names an item id this screen cannot use/i,
      ),
    ).toBeVisible()
    expect(api.getWorkItem).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
