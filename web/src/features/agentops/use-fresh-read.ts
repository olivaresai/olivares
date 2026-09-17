// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError } from '@/lib/api/errors'

/**
 * useFreshRead — an EXPLICIT read cycle for data whose presence on screen is a
 * decision of the operator and whose authority is the server's answer NOW.
 *
 * Two surfaces need this and neither fits a cached query:
 *   · the admin-only configuration read of a profile (the only read that carries a
 *     path): it starts on a Reveal gesture and ends on Hide, on unmount, or on any
 *     change of permission, identity, tenant or credential — and a response that
 *     lands after any of those is DISCARDED, never painted, never cached;
 *   · the source roster behind "Bind": the protected GET is what decides whether this
 *     principal may administer sources right now, so it is asked when the dialog
 *     opens, under the current permission, and a stale success is no authority.
 *
 * Nothing here touches the QueryCache. The answer lives in this component's state
 * and dies with the cycle; `gcTime: 0` on a query cannot promise that, because a
 * mounted observer keeps its data while disabled.
 *
 * A read that resolves is accepted only if (a) it is still the CURRENT generation,
 * (b) its controller was not aborted and (c) the permission and boundary that
 * allowed it still hold at delivery. `ready` carries the boundary it was read under
 * so a caller can refuse to act on it after the boundary moved.
 *
 * HOW A BOUNDARY MOVE ENDS THE CYCLE, and why it is split in two. The STATE is
 * adjusted during render (the sanctioned "adjust state when a prop changes"
 * pattern: nothing of a previous authority survives even one paint), and the
 * generation is advanced there too so any late response is judged stale. The
 * ABORT of the in-flight request is an effect, because cancelling a fetch is a
 * side effect on an external system, not a state update.
 */
export type FreshReadState<T> =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ready'; data: T; boundary: string }
  | { status: 'forbidden' }
  | { status: 'error'; message: string }

export interface FreshReadOptions<T> {
  /** The request. It MUST honour the signal, or an abort cannot end the cycle. */
  read: (signal: AbortSignal) => Promise<T>
  /** The CURRENT permission for this read. Losing it ends any cycle at once. */
  allowed: boolean
  /** Identity | tenant | credential key (useAuthBoundary). A change ends the cycle. */
  boundary: string
}

export interface FreshRead<T> {
  state: FreshReadState<T>
  /** Begin a cycle now. Without the permission it does not request — it says so. */
  start: () => void
  /** End the cycle: abort what is in flight, forget what arrived. */
  stop: () => void
  /** Whether `state` is a usable answer for THIS boundary and permission. */
  current: boolean
}

export function useFreshRead<T>({
  read,
  allowed,
  boundary,
}: FreshReadOptions<T>): FreshRead<T> {
  const [state, setState] = useState<FreshReadState<T>>({ status: 'idle' })
  // Refs are touched only in handlers, effects and promise callbacks — never
  // during render (react-hooks/refs).
  const generation = useRef(0)
  const controller = useRef<AbortController | null>(null)
  // The LATEST inputs, synchronised after every commit: a response is judged by
  // what holds when it arrives, not by what held when the request left.
  const readRef = useRef(read)
  const allowedRef = useRef(allowed)
  const boundaryRef = useRef(boundary)
  useEffect(() => {
    readRef.current = read
    allowedRef.current = allowed
    boundaryRef.current = boundary
  })

  // Permission lost or boundary moved: the cycle's STATE is over before this render
  // paints (the sanctioned adjust-during-render pattern)…
  const [seen, setSeen] = useState({ allowed, boundary })
  if (seen.allowed !== allowed || seen.boundary !== boundary) {
    setSeen({ allowed, boundary })
    if ((!allowed || seen.boundary !== boundary) && state.status !== 'idle') {
      setState({ status: 'idle' })
    }
  }
  // …and its REQUEST is cancelled right after the commit (an effect: cancelling a
  // fetch is a side effect on the network, not a state update). A response landing
  // in the gap is refused by the delivery checks below.
  useEffect(() => {
    if (!allowed) {
      generation.current += 1
      controller.current?.abort()
      controller.current = null
    }
  }, [allowed])
  useEffect(() => {
    // Runs on mount too, where there is nothing to cancel yet.
    generation.current += 1
    controller.current?.abort()
    controller.current = null
  }, [boundary])
  // Unmount: nothing in flight may repopulate a surface that no longer exists.
  useEffect(
    () => () => {
      generation.current += 1
      controller.current?.abort()
      controller.current = null
    },
    [],
  )

  const stop = useCallback(() => {
    generation.current += 1
    controller.current?.abort()
    controller.current = null
    setState((s) => (s.status === 'idle' ? s : { status: 'idle' }))
  }, [])

  const start = useCallback(() => {
    // Ends whatever was in flight first: two Reveal gestures are one cycle.
    generation.current += 1
    controller.current?.abort()
    if (!allowedRef.current) {
      controller.current = null
      setState({ status: 'forbidden' })
      return
    }
    const gen = generation.current
    const startedUnder = boundaryRef.current
    const ac = new AbortController()
    controller.current = ac
    setState({ status: 'loading' })
    // A read that throws before returning a promise is a failed read, not an
    // exception escaping the gesture that started it.
    let request: Promise<T>
    try {
      request = Promise.resolve(readRef.current(ac.signal))
    } catch (err) {
      request = Promise.reject(
        err instanceof Error ? err : new Error(String(err)),
      )
    }
    void request.then(
      (data) => {
        if (gen !== generation.current || ac.signal.aborted) return
        if (!allowedRef.current || boundaryRef.current !== startedUnder) {
          // Delivered into a cycle whose authority moved: not painted, not kept.
          controller.current = null
          setState({ status: 'idle' })
          return
        }
        controller.current = null
        setState({ status: 'ready', data, boundary: startedUnder })
      },
      (err: unknown) => {
        if (gen !== generation.current || ac.signal.aborted) return
        controller.current = null
        if (err instanceof ApiError && err.isForbidden) {
          setState({ status: 'forbidden' })
          return
        }
        setState({
          status: 'error',
          message: err instanceof Error ? err.message : String(err),
        })
      },
    )
  }, [])

  const current =
    allowed && state.status === 'ready' && state.boundary === boundary

  return { state, start, stop, current }
}
