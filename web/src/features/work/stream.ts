// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState } from 'react'
import { subscribeStream, type StreamStatus } from '@/features/shared'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { WORK_STREAM_PATH } from './api'
import type { WorkStreamEvent } from './types'

/**
 * useWorkStream — the DURABLE resume of GET /v1/m/sessions/work-stream.
 *
 * The contract calls this stream durable and means it literally: it "se reanuda por
 * cursor o Last-Event-ID y no depende del broker volátil". The engine's SSE frames
 * carry `id: <event_id>` (work_api.go:787) and it resumes from
 * `?cursor=` or the `Last-Event-ID` header, rejecting the pair when they disagree
 * (:692-698). It follows features/agentops/attach.ts, with three differences that come
 * from this stream being durable rather than a ring buffer:
 *
 *  1. THE CURSOR IS AN OPAQUE EVENT ID, not a sequence number. attach.ts can compare
 *     `seq < cursor` to dedupe; here there is no ordering to compare, so the cursor is
 *     simply the id of the last event delivered and the server resumes strictly after
 *     it (`OpGt` on colEventID, work_api.go:772).
 *  2. A RESUME THAT STARTS FROM ZERO IS A DEFECT, not a fresh start. Reconnecting with
 *     no cursor replays from the beginning of the workspace's history — the operator
 *     sees a flood of old events presented as new. Reconnecting having DROPPED the
 *     cursor loses everything in between and says nothing. So the cursor lives in a ref
 *     that survives reconnection, and is only ever advanced, never reset by the
 *     transport.
 *  3. THE SERVER CAN SAY "I COULD NOT LOOK" MID-STREAM. On a store failure it emits
 *     `event: olivares.error` with verdict NO_HE_PODIDO_MIRAR and closes
 *     (work_api.go:746-761). That is NOT a transport error and must NOT be retried into
 *     silence: it is surfaced through onUnavailable so the UI can say the live view is
 *     no longer trustworthy. Treating it as a disconnect would show a reconnecting
 *     spinner over a stream the server has told us it cannot serve.
 */
export interface UseWorkStreamOptions {
  enabled?: boolean
  /** Resume point from a previous mount, if the caller persisted one. */
  initialCursor?: string | null
  onEvent: (event: WorkStreamEvent) => void
  /** The server reported NO_HE_PODIDO_MIRAR on the stream itself. */
  onUnavailable?: (code: string) => void
}

export interface UseWorkStreamResult {
  status: StreamStatus
  /** The last event id delivered — the exact point a resume continues from. Exposed so
   * a caller can persist it across mounts and so a test can assert the resume. */
  cursor: string | null
  /** Set once the server answered NO_HE_PODIDO_MIRAR; cleared on a successful reopen. */
  unavailableCode: string | null
}

export function useWorkStream({
  enabled = true,
  initialCursor = null,
  onEvent,
  onUnavailable,
}: UseWorkStreamOptions): UseWorkStreamResult {
  const token = useSessionStore((s) => s.token)
  const tenant = useTenantStore((s) => s.activeTenant)
  const [status, setStatus] = useState<StreamStatus>('closed')
  const [cursor, setCursor] = useState<string | null>(initialCursor)
  const [unavailableCode, setUnavailableCode] = useState<string | null>(null)

  const cbRef = useRef({ onEvent, onUnavailable })
  useEffect(() => {
    cbRef.current = { onEvent, onUnavailable }
  }, [onEvent, onUnavailable])

  // The resume point OUTLIVES each connection: it is deliberately not state, so a
  // re-render cannot roll it back and a reconnect cannot start over.
  const cursorRef = useRef<string | null>(initialCursor)

  const active = enabled && !!token

  useEffect(() => {
    if (!active) return
    const controller = new AbortController()
    let cancelled = false
    let attempt = 0

    const handle = (msg: { event: string; data: string }) => {
      const cb = cbRef.current
      if (msg.event === 'olivares.error') {
        let code = 'observation_unavailable'
        try {
          const parsed = JSON.parse(msg.data) as {
            code?: string
            verdict?: string
          }
          if (parsed.code) code = parsed.code
        } catch {
          // A malformed sentinel is still a sentinel: the server was trying to tell us
          // it could not look. Falling back to the generic code keeps that visible
          // rather than dropping the frame and reconnecting into a false calm.
        }
        setUnavailableCode(code)
        cb.onUnavailable?.(code)
        return
      }
      // Heartbeats are comments and never reach here. Every other frame is a work
      // event; a malformed one is skipped WITHOUT advancing the cursor, so the next
      // resume asks for it again instead of stepping over it silently.
      let parsed: WorkStreamEvent
      try {
        parsed = JSON.parse(msg.data) as WorkStreamEvent
      } catch {
        return
      }
      if (typeof parsed.event_id !== 'string' || parsed.event_id === '') return
      cursorRef.current = parsed.event_id
      setCursor(parsed.event_id)
      cb.onEvent(parsed)
    }

    const run = async () => {
      while (!cancelled) {
        setStatus('connecting')
        try {
          await subscribeStream({
            path: WORK_STREAM_PATH,
            token,
            tenant,
            signal: controller.signal,
            // Resume from where we actually are. Sending the key with an empty value
            // is not the same as omitting it for every endpoint, so an absent cursor
            // omits the param entirely and asks for the stream from its start —
            // which is correct ONLY on a genuine first connect.
            query: cursorRef.current
              ? { cursor: cursorRef.current }
              : undefined,
            onOpen: () => {
              attempt = 0
              setStatus('open')
              setUnavailableCode(null)
            },
            onMessage: handle,
          })
          if (cancelled) break
          setStatus('connecting')
        } catch (err) {
          if (cancelled || (err as Error).name === 'AbortError') return
          setStatus('error')
        }
        if (cancelled) break
        const delay = Math.min(1000 * 2 ** attempt, 15_000)
        attempt += 1
        await new Promise((r) => setTimeout(r, delay))
      }
      if (!cancelled) setStatus('closed')
    }
    void run()

    return () => {
      cancelled = true
      controller.abort()
    }
  }, [active, token, tenant])

  return {
    status: active ? status : 'closed',
    cursor,
    unavailableCode,
  }
}
