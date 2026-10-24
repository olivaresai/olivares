// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePreferencesStore } from '@/stores/preferences'
import { tickingDurationSeconds, useSecondClock } from './use-second-clock'

// The engine's `duration_seconds` is the span between the first and last event it has
// seen (modules/sessions/dto.go): between polls it advances only when a new event
// lands. The pure function adds the seconds an ACTIVE session has also been working
// since its last event — exactly what the next poll will certify — and never reads
// below the engine's value.
describe('tickingDurationSeconds', () => {
  const NOW = Date.parse('2026-10-18T12:00:00Z')

  it('advances the engine value by the time since its last event', () => {
    const last = new Date(NOW - 10_000).toISOString()
    expect(tickingDurationSeconds(50, last, NOW)).toBe(60)
  })

  it('keeps the engine value when the row has no last event', () => {
    expect(tickingDurationSeconds(50, null, NOW)).toBe(50)
    expect(tickingDurationSeconds(50, undefined, NOW)).toBe(50)
  })

  it('keeps the engine value when the last event is unreadable or in the future', () => {
    expect(tickingDurationSeconds(50, 'not-a-time', NOW)).toBe(50)
    expect(
      tickingDurationSeconds(50, new Date(NOW + 5_000).toISOString(), NOW),
    ).toBe(50)
  })

  it('counts whole seconds only', () => {
    const last = new Date(NOW - 9_999).toISOString()
    expect(tickingDurationSeconds(50, last, NOW)).toBe(59)
  })

  it('keeps a missing engine value missing', () => {
    const last = new Date(NOW - 1_000).toISOString()
    expect(tickingDurationSeconds(null, last, NOW)).toBeNull()
    expect(tickingDurationSeconds(undefined, last, NOW)).toBeUndefined()
  })

  it('caps the extrapolation at the engine active window', () => {
    // deriveCC (modules/sessions/live.go) calls a session active for at most
    // 2 minutes past its last event; a stale snapshot must not keep counting
    // past what the engine would certify.
    const last = new Date(NOW - 10 * 60_000).toISOString()
    expect(tickingDurationSeconds(50, last, NOW)).toBe(170)
  })
})

describe('useSecondClock', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    usePreferencesStore.setState({ reduceMotion: false })
  })
  afterEach(() => {
    usePreferencesStore.setState({ reduceMotion: false })
    window.history.replaceState(null, '', '/')
    // The per-test matchMedia spies must not leak: a leftover "reduce" answer
    // would freeze the next test's clock before its first tick.
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('ticks once a second', () => {
    const { result } = renderHook(() => useSecondClock())
    const seen = new Set([result.current])
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    seen.add(result.current)
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    seen.add(result.current)
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    seen.add(result.current)
    expect(seen.size).toBe(4)
  })

  it('clears its interval on unmount', () => {
    const { unmount } = renderHook(() => useSecondClock())
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('freezes under the ?still capture switch', () => {
    window.history.replaceState(null, '', '/?still')
    const { result } = renderHook(() => useSecondClock())
    const frozen = result.current
    expect(vi.getTimerCount()).toBe(0)
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(frozen)
  })

  it('stops under the console Reduce motion setting', () => {
    const { result } = renderHook(() => useSecondClock())
    act(() => {
      usePreferencesStore.setState({ reduceMotion: true })
    })
    const frozen = result.current
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(frozen)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops under the system reduced-motion preference', () => {
    vi.spyOn(window, 'matchMedia').mockReturnValue({
      matches: true,
      media: '(prefers-reduced-motion: reduce)',
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList)
    const { result } = renderHook(() => useSecondClock())
    const frozen = result.current
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(frozen)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('ticks exactly on the second, not before', () => {
    const { result } = renderHook(() => useSecondClock())
    const first = result.current
    act(() => {
      vi.advanceTimersByTime(999)
    })
    expect(result.current).toBe(first)
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(result.current).not.toBe(first)
  })

  it('stops and resumes when the system preference flips while mounted', () => {
    const state = { matches: false }
    const listeners = new Set<(event: { matches: boolean }) => void>()
    vi.spyOn(window, 'matchMedia').mockReturnValue({
      get matches() {
        return state.matches
      },
      media: '(prefers-reduced-motion: reduce)',
      onchange: null,
      addEventListener: (
        _type: string,
        fn: (event: { matches: boolean }) => void,
      ) => {
        listeners.add(fn)
      },
      removeEventListener: (
        _type: string,
        fn: (event: { matches: boolean }) => void,
      ) => {
        listeners.delete(fn)
      },
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList)
    const { result } = renderHook(() => useSecondClock())
    expect(vi.getTimerCount()).toBe(1)
    act(() => {
      state.matches = true
      listeners.forEach((fn) => fn({ matches: true }))
    })
    expect(vi.getTimerCount()).toBe(0)
    act(() => {
      state.matches = false
      listeners.forEach((fn) => fn({ matches: false }))
    })
    expect(vi.getTimerCount()).toBe(1)
    const resumed = result.current
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).not.toBe(resumed)
  })

  it('does not start when disabled, and starts fresh when enabled', () => {
    const { result, rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) => useSecondClock(enabled),
      { initialProps: { enabled: false } },
    )
    expect(vi.getTimerCount()).toBe(0)
    const frozen = result.current
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(result.current).toBe(frozen)
    rerender({ enabled: true })
    expect(vi.getTimerCount()).toBe(1)
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).not.toBe(frozen)
  })
})
