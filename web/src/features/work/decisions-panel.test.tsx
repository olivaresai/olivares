// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { WorkDecision, WorkPage } from './types'
import './i18n'

const listDecisions = vi.fn()
const listWorkItems = vi.fn()
const listMembers = vi.fn()
const granted = vi.hoisted(() => new Set<string>())

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-l7',
    can: (permission: string) => granted.has(permission),
  }),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    listDecisions: (...args: unknown[]) => listDecisions(...args),
    listWorkItems: (...args: unknown[]) => listWorkItems(...args),
  }
})

vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      listMembers: (...args: unknown[]) => listMembers(...args),
    },
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

beforeEach(() => {
  granted.clear()
  granted.add('sessions:decision:read')
  granted.add('sessions:work:read')
  listDecisions.mockReset()
  listWorkItems.mockReset()
  listMembers.mockReset()
  listWorkItems.mockResolvedValue({
    items: [],
    has_more: false,
    next_cursor: '',
  })
  listMembers.mockResolvedValue({ items: [] })
})

describe('DecisionsPanel cursor pagination', () => {
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

describe('DecisionsPanel names what a decision is about and who decided', () => {
  const ITEM = '01a03ed2-7563-7851-83de-88fe017065fd'
  const PERSON = '01a03ed2-0000-7851-83de-000000000001'

  /** A decision about ITEM, decided by PERSON, recorded under another work item so a lookup by the wrong
   * field cannot pass. */
  const about = (over: Partial<WorkDecision> = {}): WorkDecision => ({
    ...decision('d1'),
    work_item_id: 'another-item',
    subject_kind: 'work.scope',
    subject_ref: ITEM,
    decided_by_kind: 'user',
    decided_by_ref: PERSON,
    ...over,
  })

  function show(items: WorkDecision[]) {
    listDecisions.mockResolvedValue(page(items, false))
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = render(
      <QueryClientProvider client={client}>
        <DecisionsPanel />
      </QueryClientProvider>,
    )
    /** Every read has answered, so a name that is missing is missing for good. */
    const settled = async () => {
      await screen.findByText('decision d1')
      await waitFor(() => expect(client.isFetching()).toBe(0))
    }
    return { ...view, settled }
  }

  /** The line that says what the decision is about and who decided. */
  const meta = () => screen.getByText(/decided by/i)

  it('shows the work item title and the member name, with the engine address on hover', async () => {
    listWorkItems.mockResolvedValue({
      items: [
        { id: ITEM, title: 'Refund idempotency' },
        { id: 'another-item', title: 'Something else' },
      ],
      has_more: false,
      next_cursor: '',
    })
    listMembers.mockResolvedValue({
      items: [{ user_id: PERSON, display_name: 'Rehema Okafor' }],
    })
    const { container, settled } = show([about()])
    await settled()
    expect(meta().textContent).toContain(
      'Scope of “Refund idempotency” · decided by Rehema Okafor',
    )
    expect(container.textContent).not.toContain(ITEM)
    expect(container.textContent).not.toContain(PERSON)
    expect(meta().getAttribute('title')).toContain(
      `work.scope:${ITEM} · decided by user:${PERSON}`,
    )
  })

  it('names only a work.scope subject: another kind with the same reference keeps its address', async () => {
    listWorkItems.mockResolvedValue({
      items: [{ id: ITEM, title: 'Refund idempotency' }],
      has_more: false,
      next_cursor: '',
    })
    listMembers.mockResolvedValue({ items: [] })
    const { container, settled } = show([about({ subject_kind: 'agent' })])
    await settled()
    expect(meta().textContent).toContain(`agent:${ITEM}`)
    expect(container.textContent).not.toContain('Refund idempotency')
  })

  it('keeps the address when nothing can name it, exactly as kind:ref', async () => {
    listWorkItems.mockResolvedValue({
      items: [{ id: 'x', title: 'Unrelated' }],
      has_more: false,
      next_cursor: '',
    })
    listMembers.mockResolvedValue({ items: [] })
    const { settled } = show([
      about({
        subject_ref: 'unknown-item',
        decided_by_kind: 'agent',
        decided_by_ref: 'agent-9',
      }),
    ])
    await settled()
    expect(meta().textContent).toContain(
      'work.scope:unknown-item · decided by agent:agent-9',
    )
  })

  it('keeps the addresses when the item list and the roster cannot be read', async () => {
    listWorkItems.mockRejectedValue(new Error('503'))
    listMembers.mockRejectedValue(new Error('503'))
    const { settled } = show([about()])
    await settled()
    expect(meta().textContent).toContain(
      `work.scope:${ITEM} · decided by user:${PERSON}`,
    )
  })

  it('does not ask for the item list without the permission to read work items', async () => {
    granted.delete('sessions:work:read')
    listMembers.mockResolvedValue({ items: [] })
    const { settled } = show([about()])
    await settled()
    expect(listWorkItems).not.toHaveBeenCalled()
    expect(meta().textContent).toContain(`work.scope:${ITEM}`)
  })

  it('asks for the item list in the tenant it shows, up to the engine page ceiling', async () => {
    listWorkItems.mockResolvedValue({
      items: [],
      has_more: false,
      next_cursor: '',
    })
    listMembers.mockResolvedValue({ items: [] })
    const { settled } = show([about()])
    await settled()
    expect(listWorkItems).toHaveBeenCalledWith(
      { limit: 200 },
      { tenant: 'tenant-l7' },
      expect.anything(),
    )
  })
})
