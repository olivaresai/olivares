// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkDecision, WorkPage } from './types'
import './i18n'

const listDecisions = vi.fn()

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-l7',
    can: (permission: string) => permission === 'sessions:decision:read',
  }),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    listDecisions: (...args: unknown[]) => listDecisions(...args),
  }
})

vi.mock('./apply-flow', () => ({ ApplyFlow: () => null }))

import { DecisionsPanel } from './decisions-panel'

function decision(id: string): WorkDecision {
  return {
    id,
    workspace_id: 'ws-l7',
    work_item_id: 'item-l7',
    decision_key: `key-${id}`,
    decision_seq: Number(id.slice(-1)),
    subject_kind: 'work_item',
    subject_ref: 'item-l7',
    operation: 'allow',
    statement_md: `decision ${id}`,
    rationale_md: 'l7 pagination witness',
    decided_by_kind: 'user',
    decided_by_ref: 'operator-l7',
    authority_ref: 'authority-l7',
    effective_at: '2026-08-27T00:00:00Z',
    decision_hash: `hash-${id}`,
    state: 'effective',
  }
}

function page(
  items: WorkDecision[],
  hasMore: boolean,
  nextCursor?: string,
): WorkPage<WorkDecision> {
  return { items, has_more: hasMore, next_cursor: nextCursor ?? '' }
}

describe('DecisionsPanel cursor pagination', () => {
  beforeEach(() => listDecisions.mockReset())

  it('appends the next page instead of replacing the decisions already read', async () => {
    listDecisions.mockImplementation((params?: { cursor?: string }) =>
      params?.cursor === 'cursor-2'
        ? Promise.resolve(page([decision('d2')], false))
        : Promise.resolve(page([decision('d1')], true, 'cursor-2')),
    )
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    render(
      <QueryClientProvider client={client}>
        <DecisionsPanel />
      </QueryClientProvider>,
    )

    expect(await screen.findByText('decision d1')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /load more/i }))

    expect(await screen.findByText('decision d2')).toBeInTheDocument()
    expect(screen.getByText('decision d1')).toBeInTheDocument()
    await waitFor(() =>
      expect(listDecisions).toHaveBeenLastCalledWith(
        expect.objectContaining({ cursor: 'cursor-2' }),
        expect.objectContaining({ tenant: 'tenant-l7' }),
        expect.anything(),
      ),
    )
  })
})
