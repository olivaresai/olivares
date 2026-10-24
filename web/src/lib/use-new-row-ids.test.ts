// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNewRowIds } from './use-new-row-ids'

// A ledger that already has rows must not animate them all on load: only the rows
// that arrived after the list's first paint slide in, the marker clears once the
// slide has finished (so a virtualized row remounting later does not replay it),
// and rows that were there all along are never marked.
describe('useNewRowIds', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('marks nothing on the first render', () => {
    const { result } = renderHook(() => useNewRowIds(['a', 'b']))
    expect(result.current.size).toBe(0)
  })

  it('marks only the ids that arrived after the first render', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a', 'b'] as readonly string[] } },
    )
    rerender({ ids: ['c', 'd', 'a', 'b'] })
    expect(result.current).toEqual(new Set(['c', 'd']))
    // A re-render with the same content (a new array identity) keeps the marks.
    rerender({ ids: ['c', 'd', 'a', 'b'] })
    expect(result.current).toEqual(new Set(['c', 'd']))
  })

  it('clears the marks once the slide has had time to finish', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a'] as readonly string[] } },
    )
    rerender({ ids: ['b', 'a'] })
    expect(result.current).toEqual(new Set(['b']))
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current.size).toBe(0)
  })

  it('marks an id again when it left the list and came back', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['b', 'a'] as readonly string[] } },
    )
    rerender({ ids: ['a'] })
    expect(result.current.size).toBe(0)
    rerender({ ids: ['b', 'a'] })
    expect(result.current).toEqual(new Set(['b']))
  })

  it('never marks rows that were already there, whatever the order', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a', 'b'] as readonly string[] } },
    )
    rerender({ ids: ['b', 'a'] })
    expect(result.current.size).toBe(0)
  })

  it('seeds instead of marking when the list starts empty and fills (first page)', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: [] as readonly string[] } },
    )
    rerender({ ids: ['a', 'b'] })
    expect(result.current.size).toBe(0)
  })

  it('keeps judging after a transient empty list: a genuinely new row still slides in', () => {
    // A refresh mid-flight shows an empty list for one render; it must not
    // re-seed the feed, or a row the engine recorded after the refresh would
    // arrive unmarked (the audit view's real "new event" path).
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a', 'b'] as readonly string[] } },
    )
    rerender({ ids: [] })
    rerender({ ids: ['c', 'a'] })
    expect(result.current).toEqual(new Set(['c']))
  })

  it('clears the marks after same-content re-renders with a new array identity', () => {
    // The audit view rebuilds the id list on every render; the clear timer must
    // survive those re-renders, or a marked row keeps its class forever and a
    // virtualized row replays the slide whenever it re-mounts.
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a'] as readonly string[] } },
    )
    rerender({ ids: ['b', 'a'] })
    expect(result.current).toEqual(new Set(['b']))
    rerender({ ids: ['b', 'a'] })
    act(() => {
      vi.advanceTimersByTime(1100)
    })
    expect(result.current.size).toBe(0)
  })

  it('never mistakes two lists that join around an id containing the separator', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a\u0000b', 'z'] as readonly string[] } },
    )
    rerender({ ids: ['a', 'b', 'z'] })
    expect(result.current).toEqual(new Set(['a', 'b']))
  })

  it('reseeds when the view changes: rows a filter or a fold toggle reveals are not arrivals', () => {
    const { result, rerender } = renderHook(
      ({ ids, scope }: { ids: readonly string[]; scope: string }) =>
        useNewRowIds(ids, scope),
      {
        initialProps: { ids: ['a', 'b'] as readonly string[], scope: 'folded' },
      },
    )
    // Show internal records: a newer internal row appears ABOVE the rows on
    // screen, in the same render as the new view.
    rerender({ ids: ['x', 'a', 'b'], scope: 'all' })
    expect(result.current.size).toBe(0)
    // A filter that keeps some rows and brings others above them.
    rerender({ ids: ['p', 'a'], scope: 'action=agent.' })
    expect(result.current.size).toBe(0)
    // In the new view, a row that genuinely arrives still slides in.
    rerender({ ids: ['n', 'p', 'a'], scope: 'action=agent.' })
    expect(result.current).toEqual(new Set(['n']))
  })

  it('never marks the older rows Load more appends below the feed', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['c', 'b'] as readonly string[] } },
    )
    rerender({ ids: ['c', 'b', 'a'] })
    expect(result.current.size).toBe(0)
  })

  it('seeds again when a list shares no row with the last one', () => {
    const { result, rerender } = renderHook(
      ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
      { initialProps: { ids: ['a'] as readonly string[] } },
    )
    rerender({ ids: ['x', 'y'] })
    expect(result.current.size).toBe(0)
    rerender({ ids: ['n', 'x', 'y'] })
    expect(result.current).toEqual(new Set(['n']))
  })

  it('marks nothing under the ?still capture switch', () => {
    window.history.replaceState(null, '', '/audit?still')
    try {
      const { result, rerender } = renderHook(
        ({ ids }: { ids: readonly string[] }) => useNewRowIds(ids),
        { initialProps: { ids: ['a'] as readonly string[] } },
      )
      rerender({ ids: ['b', 'a'] })
      expect(result.current.size).toBe(0)
    } finally {
      window.history.replaceState(null, '', '/')
    }
  })
})
