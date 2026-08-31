// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  listWorkItems: vi.fn(),
  getWorkItem: vi.fn(),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./stream', () => ({
  useWorkStream: () => ({ status: 'connected' as const }),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

import { WorkView } from './work-view'
import './i18n'
import '@/features/_intel'

const item = {
  id: 'work-1',
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
  api.listWorkItems.mockResolvedValue({ items: [item], has_more: false })
  api.getWorkItem.mockResolvedValue({
    snapshot: { item, acceptance: [], dependencies: [] },
    etag: '"v3"',
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
        'work-1',
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
