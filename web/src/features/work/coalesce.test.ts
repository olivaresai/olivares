// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TEST COUNTS REQUESTS, AND THAT IS THE WHOLE POINT.
//
// The defect was never "the list did not refresh" — it refreshed sixty times. A test
// asserting that the list is up to date would have passed on the defect exactly as
// happily as on the fix. What is asserted here is the NUMBER, and the bound it must
// never exceed.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useCoalescedRefresh, WORK_REFRESH_WINDOW_MS } from './coalesce'

beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useCoalescedRefresh', () => {
  it('refreshes IMMEDIATELY on the first event of a quiet period', () => {
    // The reason the stream is subscribed at all. A pure trailing debounce would make
    // every estate wait the window for a refresh a single event should give it at once.
    const refresh = vi.fn()
    const { result } = renderHook(() => useCoalescedRefresh(refresh))
    act(() => result.current())
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('collapses a sixty-event burst into two refreshes, not sixty', () => {
    // The measured shape: the operator walk recorded 60 aborted reads of
    // `work-items?limit=100` on ONE visit to /work, from a stream replaying a burst on
    // an estate of 5 work items and 51 decisions.
    const refresh = vi.fn()
    const { result } = renderHook(() => useCoalescedRefresh(refresh))
    act(() => {
      for (let i = 0; i < 60; i++) result.current()
    })
    expect(refresh).toHaveBeenCalledTimes(1)
    act(() => {
      vi.advanceTimersByTime(WORK_REFRESH_WINDOW_MS)
    })
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('costs at most two refreshes per window under a stream that never stops', () => {
    // The bound, stated as a bound: a burst that outlives its window must not degrade
    // into one refresh per event at the next one.
    const refresh = vi.fn()
    const { result } = renderHook(() => useCoalescedRefresh(refresh))
    act(() => {
      for (let i = 0; i < 100; i++) result.current()
    })
    // Three windows of continuous traffic.
    for (let w = 0; w < 3; w++) {
      act(() => {
        vi.advanceTimersByTime(WORK_REFRESH_WINDOW_MS)
        for (let i = 0; i < 100; i++) result.current()
      })
    }
    // 1 leading + one trailing per elapsed window. Never 400.
    expect(refresh.mock.calls.length).toBeLessThanOrEqual(4)
    expect(refresh.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('a single event in a later window refreshes at once again', () => {
    // The window closes. The next quiet period behaves like the first one: an operator
    // who returns after a pause is not made to wait for the throttle to reset.
    const refresh = vi.fn()
    const { result } = renderHook(() => useCoalescedRefresh(refresh))
    act(() => result.current())
    act(() => {
      vi.advanceTimersByTime(WORK_REFRESH_WINDOW_MS * 2)
    })
    expect(refresh).toHaveBeenCalledTimes(1)
    act(() => result.current())
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('abandons a pending window on unmount instead of flushing it', () => {
    // A refresh fired into a torn-down tree invalidates a cache nobody is reading, and
    // on a tenant switch it does so under the tenant the operator has just left.
    const refresh = vi.fn()
    const { result, unmount } = renderHook(() => useCoalescedRefresh(refresh))
    act(() => {
      result.current()
      result.current()
    })
    expect(refresh).toHaveBeenCalledTimes(1)
    unmount()
    act(() => {
      vi.advanceTimersByTime(WORK_REFRESH_WINDOW_MS * 3)
    })
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('abandons the pending window when refresh is replaced, firing NEITHER closure', () => {
    // What a tenant switch looks like from here. The trailing edge the window owed was
    // owed to the tenant the operator has just left, so it is not fired under the old
    // closure; and it is not fired under the new one either, because the incoming
    // tenant's list is a new key whose first read is already in flight — invalidating it
    // here would cancel that read, which is the cost this hook exists to remove.
    const first = vi.fn()
    const second = vi.fn()
    const { result, rerender } = renderHook(
      ({ fn }: { fn: () => void }) => useCoalescedRefresh(fn),
      { initialProps: { fn: first } },
    )
    act(() => result.current())
    expect(first).toHaveBeenCalledTimes(1)
    act(() => result.current()) // queues the trailing edge
    rerender({ fn: second })
    act(() => {
      vi.advanceTimersByTime(WORK_REFRESH_WINDOW_MS * 3)
    })
    expect(
      first,
      'the window is not flushed under the tenant just left',
    ).toHaveBeenCalledTimes(1)
    expect(second, 'nor under the one arriving').not.toHaveBeenCalled()
  })

  it('reaches the LATEST refresh on the next event, never the replaced one', () => {
    // The window was abandoned, so the next event opens a fresh one — and it must reach
    // the closure the view holds NOW. This is what the ref buys: the returned callback
    // keeps one identity while the refresh behind it moves.
    const first = vi.fn()
    const second = vi.fn()
    const { result, rerender } = renderHook(
      ({ fn }: { fn: () => void }) => useCoalescedRefresh(fn),
      { initialProps: { fn: first } },
    )
    act(() => result.current())
    expect(first).toHaveBeenCalledTimes(1)
    rerender({ fn: second })
    act(() => result.current())
    expect(second).toHaveBeenCalledTimes(1)
    expect(first).toHaveBeenCalledTimes(1)
  })

  it('keeps a stable identity, so handing it to the stream never re-subscribes', () => {
    const { result, rerender } = renderHook(
      ({ fn }: { fn: () => void }) => useCoalescedRefresh(fn),
      { initialProps: { fn: vi.fn() } },
    )
    const first = result.current
    rerender({ fn: vi.fn() })
    expect(result.current).toBe(first)
  })
})
