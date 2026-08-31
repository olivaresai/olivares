// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

vi.mock('@/components/ui/code-diff', () => ({
  CodeDiff: ({
    original,
    modified,
  }: {
    original: string
    modified: string
  }) => (
    <div data-testid="code-diff">
      <span data-testid="diff-original">{original}</span>
      <span data-testid="diff-modified">{modified}</span>
    </div>
  ),
}))

import { RevisionsSheet, type RevisionsSheetLabels } from './revisions-sheet'

const labels: RevisionsSheetLabels = {
  title: 'Revision history — Example',
  description: 'Chronological snapshots.',
  empty: 'No revisions recorded',
  loading: 'Loading',
  loadMore: 'Load more',
  compareTitle: 'Snapshot diff',
  selectTwo: 'Select two revisions.',
  selectRevision: (operation, actor) =>
    `Select ${operation} revision by ${actor}`,
  originalLabel: 'Earlier revision',
  modifiedLabel: 'Later revision',
  restore: 'Restore this revision',
  restoreTitle: 'Restore revision?',
  restoreDescription: 'Restore this configuration?',
  restoreConfirm: 'Restore revision',
  restoreSuccess: 'Revision restored',
  operations: {
    create: 'Created',
    update: 'Updated',
    delete: 'Deleted',
    restore: 'Restored',
  },
}

const firstPage = {
  items: [
    {
      id: 'rev-1',
      op: 'create' as const,
      snapshot: { enabled: false },
      actor: 'alice@example.com',
      actor_kind: 'user',
      at: '2026-07-18T10:00:00Z',
    },
    {
      id: 'rev-2',
      op: 'update' as const,
      snapshot: { enabled: true },
      actor: 'bob@example.com',
      actor_kind: 'user',
      at: '2026-07-18T11:00:00Z',
    },
  ],
  cursor: 'cursor-2',
  has_more: true,
}

const secondPage = {
  items: [
    {
      id: 'rev-3',
      op: 'restore' as const,
      snapshot: { enabled: false, retries: 3 },
      actor: 'carol@example.com',
      actor_kind: 'user',
      at: '2026-07-18T12:00:00Z',
    },
  ],
  has_more: false,
}

describe('RevisionsSheet', () => {
  it('paginates chronologically, defaults the diff to adjacent revisions, and confirms restore', async () => {
    const user = userEvent.setup()
    const listRevisions = vi.fn(
      async (params?: { cursor?: string; limit?: number }) =>
        params?.cursor === 'cursor-2' ? secondPage : firstPage,
    )
    const restoreRevision = vi.fn(async (revisionId: string) => ({
      id: 'entity-1',
      revisionId,
    }))

    renderIntel(
      <RevisionsSheet
        open
        onOpenChange={vi.fn()}
        queryKey={['history', 'entity-1']}
        listRevisions={listRevisions}
        restoreRevision={restoreRevision}
        invalidateKeys={[['entities'], ['entities', 'entity-1']]}
        canWrite
        labels={labels}
      />,
    )

    expect(await screen.findByText('Created')).toBeInTheDocument()

    // The sheet drains the remaining pages automatically (the ledger pages
    // oldest-first, so page 1 alone would default the diff to the two OLDEST
    // revisions): page 2 arrives without a manual "Load more".
    await waitFor(() =>
      expect(listRevisions).toHaveBeenCalledWith({
        cursor: 'cursor-2',
        limit: 50,
      }),
    )
    expect(await screen.findByText('Restored')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByTestId('diff-original')).toHaveTextContent(
        '"enabled": true',
      )
      expect(screen.getByTestId('diff-modified')).toHaveTextContent(
        '"retries": 3',
      )
    })

    await user.click(
      screen.getAllByRole('button', { name: 'Restore this revision' })[0],
    )
    const confirm = await screen.findByRole('dialog', {
      name: 'Restore revision?',
    })
    await user.click(
      within(confirm).getByRole('button', { name: 'Restore revision' }),
    )
    await waitFor(() => expect(restoreRevision).toHaveBeenCalledWith('rev-1'))
  })

  it('falls back to a single selection when pairing the oldest revision', async () => {
    const user = userEvent.setup()
    const listRevisions = vi.fn(
      async (params?: { cursor?: string; limit?: number }) =>
        params?.cursor === 'cursor-2' ? secondPage : firstPage,
    )

    renderIntel(
      <RevisionsSheet
        open
        onOpenChange={vi.fn()}
        queryKey={['history', 'entity-toggle']}
        listRevisions={listRevisions}
        restoreRevision={vi.fn(async () => ({ id: 'entity-1' }))}
        invalidateKeys={[['entities']]}
        canWrite
        labels={labels}
      />,
    )

    // Drained: default pair = the two NEWEST revisions.
    expect(await screen.findByText('Restored')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId('diff-modified')).toHaveTextContent(
        '"retries": 3',
      ),
    )

    // Clicking the OLDEST revision while a full pair is selected: it has no
    // predecessor, so the selection becomes that single revision (never the
    // old degenerate {id, id} pair) and the pane asks for a second pick.
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Select Created revision by alice@example.com',
      }),
    )
    expect(await screen.findByText('Select two revisions.')).toBeInTheDocument()
  })

  it('keeps revision history readable while hiding restore without write permission', async () => {
    renderIntel(
      <RevisionsSheet
        open
        onOpenChange={vi.fn()}
        queryKey={['history', 'read-only-entity']}
        listRevisions={vi.fn(async () => ({ ...firstPage, has_more: false }))}
        restoreRevision={vi.fn()}
        invalidateKeys={[]}
        canWrite={false}
        labels={labels}
      />,
    )

    expect(await screen.findByText('Created')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Restore this revision' }),
    ).not.toBeInTheDocument()
  })
})
