// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-02 (W3C ARIA APG `treegrid`). jsdom has no layout engine, so these
// tests assert the DETERMINISTIC accessibility invariants and keyboard semantics
// that do NOT need layout: the `treegrid` role + accessible name + true
// `aria-rowcount`; that collapsed subtrees are not in the DOM and `aria-expanded`
// flips on toggle; that the twistie click and Right/Left expand/collapse; that the
// roving-tabindex active cell moves across visible rows; that Enter activates; and
// that `aria-level` reflects the node depth.
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { TreeGrid, type TreeGridColumn, type TreeGridNode } from './tree-grid'

interface Resource {
  name: string
  kind: string
}

const columns: TreeGridColumn<Resource>[] = [
  { key: 'name', header: 'Name', cell: (n) => n.name },
  { key: 'kind', header: 'Kind', cell: (n) => n.kind },
]

// Two roots; the first has two children, the first of which has one grandchild.
const nodes: TreeGridNode<Resource>[] = [
  {
    id: 'root-1',
    level: 0,
    data: { name: 'root-1', kind: 'folder' },
    children: [
      {
        id: 'child-1a',
        level: 1,
        data: { name: 'child-1a', kind: 'folder' },
        children: [
          {
            id: 'grandchild-1a1',
            level: 2,
            data: { name: 'grandchild-1a1', kind: 'leaf' },
          },
        ],
      },
      {
        id: 'child-1b',
        level: 1,
        data: { name: 'child-1b', kind: 'leaf' },
      },
    ],
  },
  {
    id: 'root-2',
    level: 0,
    data: { name: 'root-2', kind: 'leaf' },
  },
]

const visibleRows = () =>
  within(screen.getByRole('treegrid'))
    .getAllByRole('row')
    .filter((r) => r.getAttribute('aria-rowindex') !== '1')

const rowByName = (name: string) =>
  screen.getByText(name).closest('[role="row"]') as HTMLElement

describe('TreeGrid — APG treegrid structure', () => {
  it('renders a labelled treegrid with the visible aria-rowcount and colcount', () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const grid = screen.getByRole('treegrid')
    expect(grid).toHaveAttribute('aria-label', 'Resources')
    expect(grid).toHaveAttribute('aria-colcount', '2')
    // 2 visible roots (collapsed) + 1 header row.
    expect(grid).toHaveAttribute('aria-rowcount', '3')
    expect(grid).toHaveAttribute('tabindex', '0')
  })

  it('exposes column headers and 1-based gridcell colindex', () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const headers = screen.getAllByRole('columnheader')
    expect(headers.map((h) => h.textContent)).toEqual(['Name', 'Kind'])
    expect(headers[0]).toHaveAttribute('aria-colindex', '1')
    const cells = within(visibleRows()[0]).getAllByRole('gridcell')
    expect(cells[0]).toHaveAttribute('aria-colindex', '1')
    expect(cells[1]).toHaveAttribute('aria-colindex', '2')
  })

  it('puts aria-expanded only on rows that have children', () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    // root-1 has children → expanded=false while collapsed.
    expect(rowByName('root-1')).toHaveAttribute('aria-expanded', 'false')
    // root-2 is a leaf → no aria-expanded attribute at all.
    expect(rowByName('root-2')).not.toHaveAttribute('aria-expanded')
  })

  it('does not render collapsed children in the DOM', () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    expect(visibleRows().length).toBe(2)
    expect(screen.queryByText('child-1a')).not.toBeInTheDocument()
  })

  it('seeds expansion from defaultExpandedIds', () => {
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        defaultExpandedIds={['root-1']}
      />,
    )
    expect(screen.getByText('child-1a')).toBeInTheDocument()
    expect(screen.getByText('child-1b')).toBeInTheDocument()
    // Grandchild is still collapsed (child-1a not expanded).
    expect(screen.queryByText('grandchild-1a1')).not.toBeInTheDocument()
    expect(rowByName('root-1')).toHaveAttribute('aria-expanded', 'true')
  })

  it('reports aria-level reflecting node depth (1-based)', () => {
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        defaultExpandedIds={['root-1', 'child-1a']}
      />,
    )
    expect(rowByName('root-1')).toHaveAttribute('aria-level', '1')
    expect(rowByName('child-1a')).toHaveAttribute('aria-level', '2')
    expect(rowByName('grandchild-1a1')).toHaveAttribute('aria-level', '3')
  })

  it('reports aria-setsize / aria-posinset among siblings', () => {
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        defaultExpandedIds={['root-1']}
      />,
    )
    expect(rowByName('root-1')).toHaveAttribute('aria-posinset', '1')
    expect(rowByName('root-1')).toHaveAttribute('aria-setsize', '2')
    expect(rowByName('child-1a')).toHaveAttribute('aria-posinset', '1')
    expect(rowByName('child-1a')).toHaveAttribute('aria-setsize', '2')
    expect(rowByName('child-1b')).toHaveAttribute('aria-posinset', '2')
  })
})

describe('TreeGrid — expand / collapse', () => {
  it('expands a row when the twistie is clicked (children appear, aria-expanded true)', () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const twistie = within(rowByName('root-1')).getByRole('button', {
      hidden: true,
    })
    fireEvent.click(twistie)
    expect(rowByName('root-1')).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('child-1a')).toBeInTheDocument()
    expect(screen.getByRole('treegrid')).toHaveAttribute('aria-rowcount', '5')
  })

  it('ArrowRight expands a collapsed row, ArrowLeft collapses it', async () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const grid = screen.getByRole('treegrid')
    // Enter the grid at the first row's first cell.
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1'),
    )
    // Right on the collapsed root expands it.
    fireEvent.keyDown(grid, { key: 'ArrowRight' })
    await waitFor(() =>
      expect(rowByName('root-1')).toHaveAttribute('aria-expanded', 'true'),
    )
    expect(screen.getByText('child-1a')).toBeInTheDocument()
    // Left on the expanded root collapses it again.
    fireEvent.keyDown(grid, { key: 'ArrowLeft' })
    await waitFor(() =>
      expect(rowByName('root-1')).toHaveAttribute('aria-expanded', 'false'),
    )
    expect(screen.queryByText('child-1a')).not.toBeInTheDocument()
  })

  it('ArrowLeft on a child row moves focus to its parent row', async () => {
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        defaultExpandedIds={['root-1']}
      />,
    )
    const grid = screen.getByRole('treegrid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // enter at root-1
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // move to child-1a
    await waitFor(() =>
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-level'),
      ).toBe('2'),
    )
    // child-1a is a leaf in terms of expansion state (collapsed) → Left goes to parent.
    fireEvent.keyDown(grid, { key: 'ArrowLeft' })
    await waitFor(() =>
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-level'),
      ).toBe('1'),
    )
  })
})

describe('TreeGrid — keyboard navigation', () => {
  it('ArrowDown / ArrowUp move the active cell across visible rows', async () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const grid = screen.getByRole('treegrid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-rowindex'),
      ).toBe('2'),
    )
    fireEvent.keyDown(grid, { key: 'ArrowDown' })
    await waitFor(() =>
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-rowindex'),
      ).toBe('3'),
    )
    fireEvent.keyDown(grid, { key: 'ArrowUp' })
    await waitFor(() =>
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-rowindex'),
      ).toBe('2'),
    )
  })

  it('ArrowRight from the first cell of a leaf row moves between cells; Home/End jump to row ends', async () => {
    render(<TreeGrid label="Resources" columns={columns} nodes={nodes} />)
    const grid = screen.getByRole('treegrid')
    // Move to root-2 (a leaf) so Right does not expand.
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // root-1
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // root-2
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1'),
    )
    fireEvent.keyDown(grid, { key: 'ArrowRight' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '2'),
    )
    fireEvent.keyDown(grid, { key: 'Home' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1'),
    )
    fireEvent.keyDown(grid, { key: 'End' })
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('aria-colindex', '2'),
    )
  })

  it('Ctrl+Home / Ctrl+End jump to the first / last visible row', async () => {
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        defaultExpandedIds={['root-1']}
      />,
    )
    const grid = screen.getByRole('treegrid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // enter at row 2
    fireEvent.keyDown(grid, { key: 'End', ctrlKey: true })
    await waitFor(() => {
      // 4 visible rows (root-1, child-1a, child-1b, root-2) → last is rowindex 5.
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-rowindex'),
      ).toBe('5')
      expect(document.activeElement).toHaveAttribute('aria-colindex', '2')
    })
    fireEvent.keyDown(grid, { key: 'Home', ctrlKey: true })
    await waitFor(() => {
      expect(
        document.activeElement
          ?.closest('[role="row"]')
          ?.getAttribute('aria-rowindex'),
      ).toBe('2')
      expect(document.activeElement).toHaveAttribute('aria-colindex', '1')
    })
  })

  it('Enter on the active row calls onActivate with the node', async () => {
    const onActivate = vi.fn()
    render(
      <TreeGrid
        label="Resources"
        columns={columns}
        nodes={nodes}
        onActivate={onActivate}
      />,
    )
    const grid = screen.getByRole('treegrid')
    fireEvent.keyDown(grid, { key: 'ArrowDown' }) // root-1
    await waitFor(() =>
      expect(document.activeElement).toHaveAttribute('role', 'gridcell'),
    )
    fireEvent.keyDown(grid, { key: 'Enter' })
    expect(onActivate).toHaveBeenCalledTimes(1)
    expect(onActivate).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'root-1' }),
    )
  })
})
