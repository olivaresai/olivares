// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE REFRESH PER BURST, AND IT IS A MEASURED DEFECT THAT PUT IT HERE.
//
// ⛔ WHAT WAS MEASURED. The operator walk of 2026-09-18 opened `/work` once against a
//    seeded estate and recorded **60 aborted reads** of
//    `GET /v1/m/sessions/work-items?limit=100` (`net::ERR_ABORTED`) on that single
//    visit. No other screen of the 77 reported a console error at all.
//
// ⛔ THE MECHANISM, AND IT IS NOT A SLOW ENDPOINT. `/work` subscribes to the durable
//    work stream and invalidates the WHOLE work key on EVERY stream event, while the
//    list query carries an abort `signal`. On an estate whose stream replays a burst —
//    that one had 5 work items and 51 seeded decisions — each event re-issues the full
//    list read and cancels the one in flight. The comment above it said the stream
//    *"keeps the list honest without polling"*: it does not poll, and under a burst it
//    does something considerably more expensive than polling would have been.
//
// ⛔ AND "DEBOUNCE IT" WOULD HAVE BEEN THE WRONG SHAPE. A pure trailing debounce makes
//    every estate pay the window, including the quiet one where a single event should
//    refresh the list at once — which is the whole reason the stream exists. So this is
//    a LEADING-plus-TRAILING throttle: the first event of a quiet period fires
//    immediately, and anything that arrives inside the window is collapsed into exactly
//    one more refresh at its end.
//
//    Bound, stated as a bound and not as an average: at most **two** refreshes per
//    window however many events arrive in it, and exactly **one** when a single event
//    does. The 60-request burst becomes 2.
import { useCallback, useEffect, useRef } from 'react'

/**
 * The window, in milliseconds.
 *
 * Long enough that a replayed burst lands inside one window (the measured burst arrived
 * as fast as the transport could deliver it), short enough that the trailing refresh is
 * not something an operator waits for. It is not configurable: a knob here would be a
 * second place to get this wrong, and the right value is a property of the stream, not
 * of a screen.
 */
export const WORK_REFRESH_WINDOW_MS = 400

/**
 * Collapse a burst of stream events into at most two refreshes.
 *
 * Returns a stable callback to hand to `onEvent`. The refresh itself is read from a ref
 * at call time, so a caller whose closure changes — a tenant switch, a new query client
 * — never fires a stale one, and the returned callback never changes identity and so
 * never re-subscribes the stream.
 */
export function useCoalescedRefresh(
  refresh: () => void,
  windowMs: number = WORK_REFRESH_WINDOW_MS,
): () => void {
  const latest = useRef(refresh)
  // In an EFFECT and not during render: `react-hooks/refs` refuses a ref write in the
  // render pass, and it is right to — a ref written during render is not a value React
  // has agreed to. Every caller here reaches the callback from a stream frame, which is
  // after effects have flushed, so the latest closure is always the one that runs.
  useEffect(() => {
    latest.current = refresh
  }, [refresh])
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pending = useRef(false)

  // The window is abandoned, not flushed, when the view unmounts AND when `refresh`
  // itself is replaced — which is what a tenant switch looks like from here, the
  // callback closing over the tenant whose key it would invalidate. A refresh fired
  // into a torn-down tree invalidates a cache nobody is reading; one fired just after a
  // switch invalidates the incoming tenant's list while its first read is still in
  // flight, and `invalidateQueries` cancels that read — the very cost this hook exists
  // to remove. The new tenant's list is a new key and reads fresh on its own.
  //
  // This is why `refresh` must be memoised by its caller. An identity that changes every
  // render reopens the window every render, which degrades the bound back to one refresh
  // per event; `useCallback` over the tenant is the contract, and it fails safe — an
  // extra cancellation costs one immediate refresh, a missed one costs the bound.
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current)
      timer.current = null
      pending.current = false
    },
    [refresh],
  )

  return useCallback(() => {
    if (timer.current !== null) {
      // Inside a window: remember that something happened and let the trailing edge
      // spend the one refresh it owes.
      pending.current = true
      return
    }
    latest.current()
    const close = () => {
      timer.current = null
      if (!pending.current) return
      pending.current = false
      // The trailing refresh opens its own window, so a stream that never stops still
      // costs at most two reads per window rather than one per event.
      latest.current()
      timer.current = setTimeout(close, windowMs)
    }
    timer.current = setTimeout(close, windowMs)
  }, [windowMs])
}
