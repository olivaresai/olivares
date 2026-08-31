// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ChevronRight } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { cn } from '@/lib/utils'

/**
 * TreeGrid — THE reusable hierarchical grid primitive (ADM-CORE-02) the resource /
 * policy tree views build on, so they never re-implement twistie
 * expansion, indentation, or APG keyboard navigation. It is generic over a node
 * type and contains no business logic: callers supply columns + a nested node tree
 * and decide what each cell renders.
 *
 * Accessibility (W3C ARIA APG `treegrid`): the container is `role="treegrid"` with
 * an accessible name, a single tab stop into the grid (roving tabindex), and
 * `aria-rowcount` carrying the header row plus every VISIBLE (expanded) node. Each
 * visible node is a `role="row"` with `aria-level`, `aria-posinset`/`aria-setsize`
 * among its siblings, and `aria-expanded` ONLY when it has children. Cells are
 * `role="gridcell"` carrying 1-based `aria-colindex`; the header is a `role="row"`
 * of `role="columnheader"` cells. This mirrors the sibling `DataTable` grid so the
 * two primitives are consistent.
 *
 * Keyboard (verbatim APG `treegrid`):
 *  - On a ROW (first cell): Right expands a collapsed row, else moves into the
 *    first cell; Left collapses an expanded row, else moves focus to the parent
 *    row; Down/Up move one VISIBLE row; Enter activates (`onActivate`).
 *  - On a CELL: Right/Left move between cells (Left from the first cell returns to
 *    row behaviour); Down/Up move one cell vertically; Home/End jump to the first/
 *    last cell of the row; Ctrl+Home/End jump to the first/last row.
 * The visible tree is flattened to an ordered list so vertical movement is O(1),
 * and the active `{row,col}` is focused by id + tabindex like `DataTable`.
 */
export interface TreeGridColumn<T> {
  key: string
  header: string
  /** The FIRST column is the tree column: it renders the twistie + indentation. */
  cell: (node: T) => ReactNode
}

export interface TreeGridNode<T> {
  id: string
  data: T
  level: number
  children?: TreeGridNode<T>[]
}

export interface TreeGridProps<T> {
  /** Accessible name applied as `aria-label` on the treegrid (WCAG 4.1.2). */
  label: string
  columns: TreeGridColumn<T>[]
  /** Root nodes; descendants nested via `children`. */
  nodes: TreeGridNode<T>[]
  getRowId?: (node: TreeGridNode<T>) => string
  defaultExpandedIds?: string[]
  /** Enter on a row. */
  onActivate?: (node: TreeGridNode<T>) => void
  density?: 'compact' | 'comfortable'
  className?: string
}

const INDENT_PX = 16

interface FlatNode<T> {
  node: TreeGridNode<T>
  id: string
  level: number
  hasChildren: boolean
  expanded: boolean
  /** 1-based position among its siblings. */
  posInSet: number
  setSize: number
  /** Index of the parent in the flattened list, or -1 for a root. */
  parentIndex: number
}

export function TreeGrid<T>({
  label,
  columns,
  nodes,
  getRowId,
  defaultExpandedIds,
  onActivate,
  density = 'comfortable',
  className,
}: TreeGridProps<T>) {
  const gridId = useId()
  const rowId = useCallback(
    (node: TreeGridNode<T>) => getRowId?.(node) ?? node.id,
    [getRowId],
  )

  const [expanded, setExpanded] = useState<Set<string>>(
    () => new Set(defaultExpandedIds ?? []),
  )

  // Flatten the VISIBLE tree (a node's children are visible only when expanded)
  // into an ordered list — the backbone for vertical movement and rendering.
  const flat = useMemo(() => {
    const out: FlatNode<T>[] = []
    const walk = (siblings: TreeGridNode<T>[], parentIndex: number): void => {
      const setSize = siblings.length
      siblings.forEach((node, i) => {
        const id = rowId(node)
        const hasChildren = !!node.children && node.children.length > 0
        const isExpanded = hasChildren && expanded.has(id)
        const index = out.length
        out.push({
          node,
          id,
          level: node.level,
          hasChildren,
          expanded: isExpanded,
          posInSet: i + 1,
          setSize,
          parentIndex,
        })
        if (isExpanded) walk(node.children!, index)
      })
    }
    walk(nodes, -1)
    return out
  }, [nodes, expanded, rowId])

  const colCount = columns.length
  const rowCount = flat.length

  // --- roving tabindex over the visible grid (matches DataTable) ----------------
  const cellId = useCallback(
    (r: number, c: number) => `${gridId}-cell-${r}-${c}`,
    [gridId],
  )
  const [active, setActive] = useState<{ r: number; c: number } | null>(null)

  // A collapse can shrink the visible set below the active row's index; clamp the
  // active position back inside the grid during render (no effect → no cascading
  // render). This derived value drives both focus and the rendered active cell.
  const safeActive =
    active && rowCount > 0
      ? {
          r: Math.min(active.r, rowCount - 1),
          c: Math.min(active.c, colCount - 1),
        }
      : null

  // Focus the active cell after it commits (no layout needed under jsdom).
  const focusR = safeActive?.r ?? -1
  const focusC = safeActive?.c ?? -1
  useEffect(() => {
    if (focusR < 0) return
    const id = requestAnimationFrame(() => {
      document.getElementById(cellId(focusR, focusC))?.focus()
    })
    return () => cancelAnimationFrame(id)
  }, [focusR, focusC, cellId])

  const setExpandedFor = useCallback((id: string, next: boolean) => {
    setExpanded((prev) => {
      if (prev.has(id) === next) return prev
      const copy = new Set(prev)
      if (next) copy.add(id)
      else copy.delete(id)
      return copy
    })
  }, [])

  const toggle = useCallback(
    (id: string) =>
      setExpanded((prev) => {
        const copy = new Set(prev)
        if (copy.has(id)) copy.delete(id)
        else copy.add(id)
        return copy
      }),
    [],
  )

  const NAV_KEYS = useMemo(
    () => [
      'ArrowDown',
      'ArrowUp',
      'ArrowRight',
      'ArrowLeft',
      'Home',
      'End',
      'Enter',
    ],
    [],
  )

  const onGridKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (rowCount === 0) return
    // First navigation key with nothing active ENTERS the grid at {0,0}.
    if (safeActive === null) {
      if (NAV_KEYS.includes(e.key)) {
        e.preventDefault()
        setActive({ r: 0, c: 0 })
      }
      return
    }
    const last = rowCount - 1
    const lastCol = colCount - 1
    const cur = safeActive
    const row = flat[cur.r]
    const onFirstCell = cur.c === 0
    const move = (r: number, c: number) => {
      e.preventDefault()
      setActive({ r, c })
    }

    switch (e.key) {
      case 'ArrowDown':
        move(Math.min(cur.r + 1, last), cur.c)
        return
      case 'ArrowUp':
        move(Math.max(cur.r - 1, 0), cur.c)
        return
      case 'ArrowRight':
        if (onFirstCell && row.hasChildren && !row.expanded) {
          // Right on a collapsed row expands it (focus stays on the row).
          e.preventDefault()
          setExpandedFor(row.id, true)
          return
        }
        move(cur.r, Math.min(cur.c + 1, lastCol))
        return
      case 'ArrowLeft':
        if (onFirstCell) {
          if (row.hasChildren && row.expanded) {
            // Left on an expanded row collapses it.
            e.preventDefault()
            setExpandedFor(row.id, false)
            return
          }
          // Left on a collapsed/leaf row moves focus to its parent row (if any).
          if (row.parentIndex >= 0) move(row.parentIndex, 0)
          return
        }
        move(cur.r, Math.max(cur.c - 1, 0))
        return
      case 'Home':
        move(e.ctrlKey ? 0 : cur.r, 0)
        return
      case 'End':
        move(e.ctrlKey ? last : cur.r, lastCol)
        return
      case 'Enter':
        e.preventDefault()
        onActivate?.(row.node)
        return
      default:
    }
  }

  const cellPad = density === 'compact' ? 'h-7 px-3' : 'h-8 px-3'
  const headPad = density === 'compact' ? 'px-3 py-1.5' : 'px-3 py-2'

  // The grid takes the single tab stop until a cell is active (roving tabindex).
  const gridTabIndex = safeActive ? -1 : 0

  return (
    <div
      className={cn(
        'overflow-hidden rounded-lg border border-border bg-surface',
        className,
      )}
    >
      <div className="overflow-x-auto">
        <div
          role="treegrid"
          aria-label={label}
          aria-rowcount={rowCount + 1}
          aria-colcount={colCount}
          tabIndex={gridTabIndex}
          onKeyDown={onGridKeyDown}
          className="w-full text-sm outline-none"
        >
          <div role="rowgroup">
            <div
              role="row"
              aria-rowindex={1}
              className="flex border-b border-border-strong"
            >
              {columns.map((col, c) => (
                <div
                  key={col.key}
                  role="columnheader"
                  aria-colindex={c + 1}
                  className={cn(
                    'flex-1 bg-muted text-left text-xs font-medium tracking-wide text-muted-foreground uppercase',
                    headPad,
                  )}
                >
                  {col.header}
                </div>
              ))}
            </div>
          </div>

          <div role="rowgroup">
            {flat.map((fn, r) => (
              <div
                key={fn.id}
                role="row"
                aria-rowindex={r + 2}
                aria-level={fn.level + 1}
                aria-posinset={fn.posInSet}
                aria-setsize={fn.setSize}
                aria-expanded={
                  fn.hasChildren ? (fn.expanded ? true : false) : undefined
                }
                className="flex border-b border-border transition-colors last:border-0 hover:bg-muted"
              >
                {columns.map((col, c) => {
                  const isActive = safeActive?.r === r && safeActive?.c === c
                  const isTreeCol = c === 0
                  return (
                    <div
                      key={col.key}
                      role="gridcell"
                      id={cellId(r, c)}
                      aria-colindex={c + 1}
                      tabIndex={isActive ? 0 : -1}
                      className={cn(
                        'flex flex-1 items-center align-middle outline-none',
                        cellPad,
                        isActive &&
                          'bg-muted ring-2 ring-ring -outline-offset-2 ring-inset',
                      )}
                    >
                      {isTreeCol ? (
                        <span
                          className="flex min-w-0 items-center"
                          style={{ paddingLeft: fn.level * INDENT_PX }}
                        >
                          {fn.hasChildren ? (
                            <button
                              type="button"
                              tabIndex={-1}
                              aria-hidden="true"
                              onClick={(ev) => {
                                ev.stopPropagation()
                                toggle(fn.id)
                                setActive({ r, c: 0 })
                              }}
                              className={cn(
                                'mr-1 inline-flex size-6 shrink-0 items-center justify-center rounded-sm',
                                'text-muted-foreground transition-colors outline-none',
                                'hover:bg-muted hover:text-foreground',
                                'focus-visible:ring-2 focus-visible:ring-ring',
                              )}
                            >
                              <ChevronRight
                                className={cn(
                                  'size-4 transition-transform duration-100',
                                  fn.expanded && 'rotate-90',
                                )}
                              />
                            </button>
                          ) : (
                            <span className="mr-1 inline-block size-6 shrink-0" />
                          )}
                          <span className="min-w-0 truncate">
                            {col.cell(fn.node.data)}
                          </span>
                        </span>
                      ) : (
                        col.cell(fn.node.data)
                      )}
                    </div>
                  )
                })}
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
