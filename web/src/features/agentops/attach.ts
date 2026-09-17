// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useCallback, useEffect, useRef, useState } from 'react'
import { subscribeStream, type StreamStatus } from '@/features/shared'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { runAttachPath } from './api'
import {
  isAttachIOUnavailable,
  type AttachFrame,
  type AttachIOUnavailable,
  type AttachLag,
  type AttachNotice,
} from './types'

/**
 * useRunAttach — the LOSS-FREE, cursor-aware attach to one operated session's bridged
 * I/O (GET /runs/{ref}/attach, SSE). It is deliberately NOT useLiveStream: an operated
 * session's output is SEQUENTIAL, so a dropped frame corrupts the transcript. Instead:
 *
 *  - it tracks a per-run sequence CURSOR and reconnects with `?from=<cursor>`, so a
 *    network blip resumes WITHOUT loss from the last frame seen (the server replays the
 *    ring from the cursor + live frames);
 *  - it dedupes by seq (a replay may re-send the boundary frame) — every `output` is
 *    delivered to `onFrame` exactly once, in order;
 *  - it surfaces the server's `lag` sentinel HONESTLY (onLag): when the ring evicted
 *    frames below the cursor, N frames are gone — never a silent "all good";
 *  - it relays lifecycle `notice` frames (onNotice — e.g. a remote-control
 *    "I/O relayed to Anthropic cloud, not bridged" sentinel) and the terminal `end`.
 *
 * On a clean `end` it stops reconnecting (the process I/O ended); a transport error
 * reconnects with capped backoff. A typed `io_unavailable` notice stops THIS attempt
 * without backoff and without marking the process ended — I/O was never bridged here.
 * A legacy notice without that field is not terminal. Callbacks are held by ref so
 * updating them does not restart the stream; a cancelled attempt never delivers them.
 *
 * The replay cursor belongs to the run+tenant+credential identity, not to one HTTP
 * attempt: an explicit retry or a same-identity state/transport restart resumes
 * with `?from=` and keeps seq dedupe. Only a new identity starts at from=0.
 */
export interface UseRunAttachOptions {
  runRef: string | null
  enabled?: boolean
  /** Restarts the attempt when session state/transport (or equivalent) changes. */
  sessionKey?: string
  onFrame: (frame: AttachFrame) => void
  onLag?: (lag: AttachLag) => void
  onNotice?: (notice: AttachNotice) => void
  onEnd?: () => void
}

export interface UseRunAttachResult {
  status: StreamStatus
  /** True once the server signalled the I/O stream ended (process exited + drained). */
  ended: boolean
  /** Typed I/O-absence for this attempt; null unless a known classification arrived. */
  ioUnavailable: AttachIOUnavailable | null
  /** Start a new attempt on the same identity, keeping the replay cursor. */
  retry: () => void
}

export function useRunAttach({
  runRef,
  enabled = true,
  sessionKey,
  onFrame,
  onLag,
  onNotice,
  onEnd,
}: UseRunAttachOptions): UseRunAttachResult {
  const token = useSessionStore((s) => s.token)
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const tenant = useTenantStore((s) => s.activeTenant)
  const [status, setStatus] = useState<StreamStatus>('closed')
  const [ended, setEnded] = useState(false)
  const [ioUnavailable, setIoUnavailable] =
    useState<AttachIOUnavailable | null>(null)
  const [generation, setGeneration] = useState(0)
  const retry = useCallback(() => {
    setGeneration((g) => g + 1)
  }, [])

  // Stable callback refs so the stream effect doesn't restart on every render.
  const cbRef = useRef({ onFrame, onLag, onNotice, onEnd })
  useEffect(() => {
    cbRef.current = { onFrame, onLag, onNotice, onEnd }
  }, [onFrame, onLag, onNotice, onEnd])

  // Cursor owner is compared privately; the bearer is never placed in React
  // state, a key, or a log. credentialGeneration is the non-secret counter.
  const ownerKey = `${runRef ?? ''}\n${tenant ?? ''}\n${credentialGeneration}`
  const [owner, setOwner] = useState(ownerKey)
  const cursorRef = useRef(0)
  if (owner !== ownerKey) {
    setOwner(ownerKey)
    if (ended) setEnded(false)
    if (ioUnavailable !== null) setIoUnavailable(null)
    if (status !== 'closed') setStatus('closed')
  }
  // Reset the replay cursor after the identity commit, before the stream
  // effect (declaration order). Retry/sessionKey must NOT reset it.
  useEffect(() => {
    cursorRef.current = 0
  }, [ownerKey])

  const active = enabled && !!runRef && !!token

  useEffect(() => {
    if (!active || !runRef) return
    const controller = new AbortController()
    let cancelled = false
    let attempt = 0
    let endReceived = false
    let unavailable: AttachIOUnavailable | null = null

    const handle = (msg: { event: string; data: string }) => {
      if (cancelled) return
      const cb = cbRef.current
      switch (msg.event) {
        case 'output': {
          let f: AttachFrame
          try {
            f = JSON.parse(msg.data) as AttachFrame
          } catch {
            return // a malformed frame must never crash the console
          }
          if (typeof f.seq !== 'number' || f.seq < cursorRef.current) return
          cursorRef.current = f.seq + 1
          if (cancelled) return
          cb.onFrame(f)
          break
        }
        case 'lag': {
          try {
            const lag = JSON.parse(msg.data) as AttachLag
            // Resume past the gap: the server told us where the stream continues.
            if (typeof lag.next_seq === 'number')
              cursorRef.current = lag.next_seq
            if (cancelled) return
            cb.onLag?.(lag)
          } catch {
            /* ignore a malformed sentinel */
          }
          break
        }
        case 'notice': {
          try {
            const notice = JSON.parse(msg.data) as AttachNotice
            if (isAttachIOUnavailable(notice.io_unavailable)) {
              unavailable = notice.io_unavailable
            }
            if (cancelled) return
            cb.onNotice?.(notice)
          } catch {
            /* ignore */
          }
          break
        }
        case 'end': {
          endReceived = true
          if (cancelled) return
          cb.onEnd?.()
          break
        }
      }
    }

    const run = async () => {
      setEnded(false) // reset on (re)subscribe — inside the async runner, not the effect body
      setIoUnavailable(null)
      while (!cancelled && !endReceived && unavailable === null) {
        setStatus('connecting')
        try {
          await subscribeStream({
            path: runAttachPath(runRef),
            token,
            tenant,
            signal: controller.signal,
            query: { from: String(cursorRef.current) },
            onOpen: () => {
              attempt = 0
              setStatus('open')
            },
            onMessage: handle,
          })
          // Server closed the stream. If it ended cleanly or I/O is absent, stop;
          // otherwise reconnect with the cursor.
          if (cancelled || endReceived || unavailable !== null) break
          setStatus('connecting')
        } catch (err) {
          if (cancelled || (err as Error).name === 'AbortError') return
          setStatus('error')
        }
        if (cancelled || endReceived || unavailable !== null) break
        const delay = Math.min(1000 * 2 ** attempt, 15_000)
        attempt += 1
        await new Promise((r) => setTimeout(r, delay))
      }
      if (!cancelled) {
        setStatus('closed')
        if (endReceived) setEnded(true)
        if (unavailable !== null) setIoUnavailable(unavailable)
      }
    }
    void run()

    return () => {
      cancelled = true
      controller.abort()
    }
  }, [active, runRef, token, tenant, sessionKey, generation])

  return {
    status: active ? status : 'closed',
    ended,
    ioUnavailable: active ? ioUnavailable : null,
    retry,
  }
}
