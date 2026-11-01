// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor, within } from '@/test/intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
const timelineById = vi.hoisted(() => vi.fn())
vi.mock('./api', async (original) => {
  const real = await original<typeof import('./api')>()
  return { ...real, sessionsApi: { ...real.sessionsApi, timelineById } }
})

import { SessionEvidence } from './session-evidence'

describe('SessionEvidence reads the rows the session wrote on other channels', () => {
  it('counts the PEP hook row’s tool calls as turns and resources', async () => {
    timelineById.mockImplementation(async (ref: string) =>
      ref === 'lr-hook'
        ? {
            items: [
              {
                at: '2026-10-01T18:33:09Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
              {
                at: '2026-10-01T18:33:10Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
            ],
            has_more: false,
          }
        : { items: [], has_more: false },
    )
    renderIntel(
      <SessionEvidence
        liveRef="lr-managed"
        sessionRef="claude-sid"
        echoRefs={['lr-hook', 'lr-usage']}
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await waitFor(() =>
      expect(screen.getByTestId('evidence-toggle-activity')).toHaveTextContent(
        /2$/,
      ),
    )
    expect(screen.getByTestId('evidence-toggle-resources')).toHaveTextContent(
      /1$/,
    )
    expect(timelineById).toHaveBeenCalledWith('lr-hook', expect.anything())
  })

  it('a block with nothing to show is not shown, and no evidence at all when every block is empty (Root review, 09)', async () => {
    timelineById.mockImplementation(async (ref: string) =>
      ref === 'lr-hook'
        ? {
            items: [
              {
                at: '2026-10-02T08:15:09Z',
                kind: 'tool',
                tool_ref: 'Bash',
                resource_ref: 'echo',
              },
            ],
            has_more: false,
          }
        : { items: [], has_more: false },
    )
    const view = renderIntel(
      <SessionEvidence
        liveRef="lr-managed"
        echoRefs={['lr-hook']}
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    expect(
      await screen.findByTestId('evidence-toggle-activity'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('evidence-toggle-checks')).toBeNull()
    expect(screen.queryByText('This session reported no findings.')).toBeNull()
    view.unmount()
    timelineById.mockResolvedValue({ items: [], has_more: false })
    renderIntel(
      <SessionEvidence
        liveRef="lr-empty"
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await waitFor(() =>
      expect(timelineById).toHaveBeenCalledWith('lr-empty', expect.anything()),
    )
    await waitFor(() =>
      expect(screen.queryByTestId('evidence-loading')).toBeNull(),
    )
    expect(screen.queryByTestId('session-evidence')).toBeNull()
  })
})

describe("SessionEvidence puts the session's work on screen (#2267)", () => {
  const gap = (at: string) => ({
    at,
    kind: 'finding',
    title: 'cooperative trace gap',
  })
  const turn = (at: string, tool: string) => ({
    at,
    kind: 'tool',
    tool_ref: tool,
    resource_ref: `src/${tool}.go`,
  })
  const page = {
    items: [
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-15T21:20:00Z'),
      turn('2026-10-15T21:21:00Z', 'Read'),
      turn('2026-10-15T21:22:00Z', 'Write'),
      turn('2026-10-15T21:23:00Z', 'Edit'),
    ],
    has_more: false,
  }

  it('shows three identical checks as one row with a count, and expanding it lists them', async () => {
    timelineById.mockResolvedValue(page)
    const user = userEvent.setup()
    renderIntel(
      <SessionEvidence liveRef="lr-1" expanded="checks" onExpand={() => {}} />,
    )
    await screen.findByTestId('evidence-toggle-checks')
    const checks = document.getElementById('evidence-checks') as HTMLElement
    const row = within(checks).getByRole('button', {
      name: /cooperative trace gap.*×3/,
    })
    // Folded: one visible row, the three entries stay out of the accessibility tree.
    expect(within(checks).getAllByRole('listitem')).toHaveLength(1)
    expect(row).toHaveAttribute('aria-expanded', 'false')
    await user.click(row)
    expect(row).toHaveAttribute('aria-expanded', 'true')
    expect(within(checks).getAllByRole('listitem')).toHaveLength(3 + 1)
  })

  it('opens the turns by default when the session has any, with no block named', async () => {
    timelineById.mockResolvedValue(page)
    renderIntel(<SessionEvidence liveRef="lr-1" onExpand={() => {}} />)
    const toggle = await screen.findByTestId('evidence-toggle-activity')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByTestId('evidence-toggle-checks')).toHaveAttribute(
      'aria-expanded',
      'false',
    )
    expect(screen.getByText('Write')).toBeVisible()
  })

  it('opens the checks by default when the session has no turns', async () => {
    timelineById.mockResolvedValue({
      items: [gap('2026-10-15T21:20:00Z')],
      has_more: false,
    })
    renderIntel(<SessionEvidence liveRef="lr-2" onExpand={() => {}} />)
    expect(await screen.findByTestId('evidence-toggle-checks')).toHaveAttribute(
      'aria-expanded',
      'true',
    )
  })

  it('keeps the block the address names, even when turns exist', async () => {
    timelineById.mockResolvedValue(page)
    renderIntel(
      <SessionEvidence liveRef="lr-1" expanded="checks" onExpand={() => {}} />,
    )
    expect(await screen.findByTestId('evidence-toggle-checks')).toHaveAttribute(
      'aria-expanded',
      'true',
    )
    expect(screen.getByTestId('evidence-toggle-activity')).toHaveAttribute(
      'aria-expanded',
      'false',
    )
  })

  it('says the resource on a folded row, so the same words about two resources stay two readable rows', async () => {
    timelineById.mockResolvedValue({
      items: [
        { ...gap('2026-10-15T21:20:00Z'), resource_ref: 'repo/a' },
        { ...gap('2026-10-15T21:20:00Z'), resource_ref: 'repo/a' },
        { ...gap('2026-10-15T21:20:00Z'), resource_ref: 'repo/b' },
        { ...gap('2026-10-15T21:20:00Z'), resource_ref: 'repo/b' },
      ],
      has_more: false,
    })
    renderIntel(
      <SessionEvidence liveRef="lr-1" expanded="checks" onExpand={() => {}} />,
    )
    await screen.findByTestId('evidence-toggle-checks')
    const checks = document.getElementById('evidence-checks') as HTMLElement
    const rows = within(checks).getAllByRole('button')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('repo/a')
    expect(rows[1]).toHaveTextContent('repo/b')
  })

  it('shows a single check as a plain row: no count, nothing to expand', async () => {
    timelineById.mockResolvedValue({
      items: [gap('2026-10-15T21:20:00Z')],
      has_more: false,
    })
    renderIntel(
      <SessionEvidence liveRef="lr-1" expanded="checks" onExpand={() => {}} />,
    )
    await screen.findByTestId('evidence-toggle-checks')
    const checks = document.getElementById('evidence-checks') as HTMLElement
    expect(within(checks).queryByRole('button')).toBeNull()
    expect(checks).not.toHaveTextContent('×')
  })

  it('folds the entries one call wrote into one turn, and prints no fact twice (#2267 review)', async () => {
    const call = (at: string) => ({
      at,
      kind: 'mcp',
      title: 'create_issue',
      resource_ref: 'github/create_issue',
    })
    timelineById.mockResolvedValue({
      items: [
        call('2026-10-15T21:20:35Z'),
        call('2026-10-15T21:20:34Z'),
        {
          at: '2026-10-15T21:21:00Z',
          kind: 'tool',
          title: 'Task worker-indexer',
          resource_ref: 'worker-indexer',
        },
        {
          at: '2026-10-15T21:22:00Z',
          kind: 'tool',
          title: 'github',
          resource_ref: 'github',
        },
      ],
      has_more: false,
    })
    const user = userEvent.setup()
    renderIntel(<SessionEvidence liveRef="lr-1" onExpand={() => {}} />)
    await screen.findByTestId('evidence-toggle-activity')
    const turns = document.getElementById('evidence-activity') as HTMLElement
    // Three turns, not four entries; the two-entry call carries its count.
    expect(within(turns).getAllByRole('listitem')).toHaveLength(3)
    const folded = within(turns).getByRole('button', {
      name: /create_issue.*×2/,
    })
    // A title that already ends in its resource, or is it, is not printed a second time.
    expect(within(turns).getAllByText(/worker-indexer/)).toHaveLength(1)
    expect(within(turns).getAllByText('github')).toHaveLength(1)
    // Expanding lists the two entries by their time only: the words are said once, above.
    await user.click(folded)
    expect(within(turns).getAllByRole('listitem')).toHaveLength(3 + 2)
    expect(within(turns).getAllByText(/create_issue/)).toHaveLength(1)
    expect(folded).toHaveTextContent('github/create_issue ×2')
    // The two entries are listed by their time, newest group first in the block.
    const entries = within(
      document.getElementById(
        folded.getAttribute('aria-controls') as string,
      ) as HTMLElement,
    ).getAllByRole('listitem')
    expect(entries).toHaveLength(2)
    for (const li of entries) expect(li.textContent?.trim()).not.toBe('')
    const rows = within(turns)
      .getAllByRole('listitem')
      .map((li) => li.textContent ?? '')
    const indexOf = (match: (text: string) => boolean) => rows.findIndex(match)
    const plainRow = indexOf((t) => t.startsWith('github') && !t.includes('/'))
    const taskRow = indexOf((t) => t.startsWith('Task worker-indexer'))
    const callRow = indexOf((t) => t.startsWith('github/create_issue'))
    expect([plainRow, taskRow, callRow].every((i) => i >= 0)).toBe(true)
    expect(plainRow).toBeLessThan(taskRow)
    expect(taskRow).toBeLessThan(callRow)
  })

  it('names a folded row with no words by its kind, never by the raw key', async () => {
    timelineById.mockResolvedValue({
      items: [
        { at: '2026-10-15T21:20:00Z', kind: 'tool' },
        { at: '2026-10-15T21:20:10Z', kind: 'tool' },
      ],
      has_more: false,
    })
    renderIntel(<SessionEvidence liveRef="lr-1" onExpand={() => {}} />)
    await screen.findByTestId('evidence-toggle-activity')
    const turns = document.getElementById('evidence-activity') as HTMLElement
    const row = within(turns).getByRole('button')
    expect(row).toHaveTextContent('×2')
    expect(row).not.toHaveTextContent('kind.')
  })

  it('tells a folded row as a range when its entries are a minute or more apart', async () => {
    timelineById.mockResolvedValue({
      items: [gap('2026-10-15T21:20:00Z'), gap('2026-10-15T21:10:00Z')],
      has_more: false,
    })
    renderIntel(
      <SessionEvidence liveRef="lr-1" expanded="checks" onExpand={() => {}} />,
    )
    await screen.findByTestId('evidence-toggle-checks')
    const checks = document.getElementById('evidence-checks') as HTMLElement
    expect(within(checks).getByRole('button')).toHaveTextContent(' – ')
  })

  it('falls back to the turns when the named block has nothing in it', async () => {
    timelineById.mockResolvedValue({
      items: [turn('2026-10-15T21:21:00Z', 'Read')].map((e) => ({
        ...e,
        resource_ref: undefined,
      })),
      has_more: false,
    })
    renderIntel(
      <SessionEvidence
        liveRef="lr-1"
        expanded="resources"
        onExpand={() => {}}
      />,
    )
    expect(
      await screen.findByTestId('evidence-toggle-activity'),
    ).toHaveAttribute('aria-expanded', 'true')
  })

  it('opens the first block that has rows when there are no turns and no checks', async () => {
    timelineById.mockResolvedValue({
      items: [
        {
          at: '2026-10-15T21:21:00Z',
          kind: 'cost',
          resource_ref: 'openai/gpt',
        },
      ],
      has_more: false,
    })
    renderIntel(<SessionEvidence liveRef="lr-1" onExpand={() => {}} />)
    expect(
      await screen.findByTestId('evidence-toggle-resources'),
    ).toHaveAttribute('aria-expanded', 'true')
  })

  it('waits for the rows other channels wrote before it picks the block, so the pane does not jump', async () => {
    let release: (v: unknown) => void = () => {}
    const late = new Promise((resolve) => {
      release = resolve
    })
    timelineById.mockImplementation(async (ref: string) =>
      ref === 'lr-echo'
        ? late
        : { items: [gap('2026-10-15T21:20:00Z')], has_more: false },
    )
    renderIntel(
      <SessionEvidence
        liveRef="lr-1"
        echoRefs={['lr-echo']}
        onExpand={() => {}}
      />,
    )
    await waitFor(() =>
      expect(timelineById).toHaveBeenCalledWith('lr-echo', expect.anything()),
    )
    // The primary read has answered with a check; the pane must not open on it yet.
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByTestId('evidence-toggle-checks')).toBeNull()
    expect(screen.getByTestId('evidence-loading')).toBeInTheDocument()
    release({
      items: [turn('2026-10-15T21:21:00Z', 'Read')],
      has_more: false,
    })
    expect(
      await screen.findByTestId('evidence-toggle-activity'),
    ).toHaveAttribute('aria-expanded', 'true')
  })
})

describe('SessionEvidence keeps its tool rows on one line (#2796)', () => {
  const call = (i: number) => ({
    at: `2026-10-15T21:2${i}:00Z`,
    kind: 'tool',
    tool_ref: 'Bash',
    resource_ref: 'src/scripts/long-running-check.go',
  })

  it('truncates the row with its whole label on the title, and folds repeated calls once with a count', async () => {
    timelineById.mockResolvedValue({
      items: Array.from({ length: 10 }, (_, i) => call(i)),
      has_more: false,
    })
    renderIntel(
      <SessionEvidence
        liveRef="lr-1"
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await screen.findByTestId('evidence-toggle-activity')
    const turns = document.getElementById('evidence-activity') as HTMLElement
    // Ten repeated calls of the same tool are one row with its count.
    expect(within(turns).getAllByRole('listitem')).toHaveLength(1)
    const row = within(turns).getByRole('button', { name: /Bash .*×10/ })
    // The words and the resource stay on one line each, cut with an ellipsis —
    // never a word broken per letter — and the whole label is on the title.
    const words = row.querySelector('span[title]') as HTMLElement
    expect(words).toHaveClass('truncate')
    expect(words).not.toHaveClass('break-words')
    expect(words).toHaveAttribute(
      'title',
      'Bash · src/scripts/long-running-check.go',
    )
    const resource = row.querySelector('.font-mono') as HTMLElement
    expect(resource).toHaveClass('truncate')
    expect(resource).toHaveAttribute(
      'title',
      'src/scripts/long-running-check.go',
    )
  })

  it('puts a time range under the words, never beside them', async () => {
    // Calls a minute apart span nine minutes: the row tells a range. Beside the words
    // the range is about 200 px of a 320 px pane, and it left "Bash" 0 to 10 px.
    timelineById.mockResolvedValue({
      items: [
        ...Array.from({ length: 10 }, (_, i) => call(i)),
        {
          at: '2026-10-15T21:30:00Z',
          kind: 'tool',
          tool_ref: 'Read',
          resource_ref: 'docs/notes.md',
        },
      ],
      has_more: false,
    })
    renderIntel(
      <SessionEvidence
        liveRef="lr-1"
        expanded="activity"
        onExpand={() => {}}
      />,
    )
    await screen.findByTestId('evidence-toggle-activity')
    const turns = document.getElementById('evidence-activity') as HTMLElement
    const row = within(turns).getByRole('button', { name: /Bash .*×10/ })
    const words = row.querySelector(':scope > span.flex-1') as HTMLElement
    const line = words.querySelector(':scope > span.flex') as HTMLElement
    // The range is a line of the words column, not on the words' line, and not
    // in a cell of the row beside the column.
    expect(words).toHaveTextContent('–')
    expect(line).not.toHaveTextContent('–')
    for (const cell of row.querySelectorAll(':scope > :not(span.flex-1)'))
      expect(cell).not.toHaveTextContent('–')
    // The count never shrinks, and it is caption-sized like the words beside it.
    const count = within(line).getByText('×10')
    expect(count).toHaveClass('shrink-0')
    expect(count.closest('.text-caption')).not.toBeNull()
    // A single call's row is one line too, its resource included.
    const read = within(turns).getByTitle('Read · docs/notes.md')
    expect(read).toHaveClass('truncate')
    expect(read).not.toHaveClass('break-words')
    expect(within(turns).getByTitle('docs/notes.md')).toHaveClass('truncate')
  })
})
