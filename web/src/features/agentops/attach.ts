// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState } from 'react'
import { subscribeStream, type StreamStatus } from '@/features/shared'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { runAttachPath } from './api'
import type { AttachFrame, AttachLag, AttachNotice } from './types'

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
 * reconnects with capped backoff. Callbacks are held by ref so updating them does not
 * restart the stream.
 */
export interface UseRunAttachOptions {
  runRef: string | null
  enabled?: boolean
  onFrame: (frame: AttachFrame) => void
  onLag?: (lag: AttachLag) => void
  onNotice?: (notice: AttachNotice) => void
  onEnd?: () => void
}

export interface UseRunAttachResult {
  status: StreamStatus
  /** True once the server signalled the I/O stream ended (process exited + drained). */
  ended: boolean
}

export function useRunAttach({
  runRef,
  enabled = true,
  onFrame,
  onLag,
  onNotice,
  onEnd,
}: UseRunAttachOptions): UseRunAttachResult {
  const token = useSessionStore((s) => s.token)
  const tenant = useTenantStore((s) => s.activeTenant)
  const [status, setStatus] = useState<StreamStatus>('closed')
  const [ended, setEnded] = useState(false)

  // Stable callback refs so the stream effect doesn't restart on every render.
  const cbRef = useRef({ onFrame, onLag, onNotice, onEnd })
  useEffect(() => {
    cbRef.current = { onFrame, onLag, onNotice, onEnd }
  }, [onFrame, onLag, onNotice, onEnd])

  const active = enabled && !!runRef && !!token

  useEffect(() => {
    if (!active || !runRef) return
    const controller = new AbortController()
    let cancelled = false
    let attempt = 0
    let endReceived = false
    // The replay cursor: 0 ⇒ replay the ring's buffered tail on first connect; then the
    // next expected seq, so a reconnect resumes from exactly after the last frame seen.
    let cursor = 0

    const handle = (msg: { event: string; data: string }) => {
      const cb = cbRef.current
      switch (msg.event) {
        case 'output': {
          let f: AttachFrame
          try {
            f = JSON.parse(msg.data) as AttachFrame
          } catch {
            return // a malformed frame must never crash the console
          }
          if (typeof f.seq !== 'number' || f.seq < cursor) return // dedupe replays
          cursor = f.seq + 1
          cb.onFrame(f)
          break
        }
        case 'lag': {
          try {
            const lag = JSON.parse(msg.data) as AttachLag
            // Resume past the gap: the server told us where the stream continues.
            if (typeof lag.next_seq === 'number') cursor = lag.next_seq
            cb.onLag?.(lag)
          } catch {
            /* ignore a malformed sentinel */
          }
          break
        }
        case 'notice': {
          try {
            cb.onNotice?.(JSON.parse(msg.data) as AttachNotice)
          } catch {
            /* ignore */
          }
          break
        }
        case 'end': {
          endReceived = true
          cb.onEnd?.()
          break
        }
      }
    }

    const run = async () => {
      setEnded(false) // reset on (re)subscribe — inside the async runner, not the effect body
      while (!cancelled && !endReceived) {
        setStatus('connecting')
        try {
          await subscribeStream({
            path: runAttachPath(runRef),
            token,
            tenant,
            signal: controller.signal,
            query: { from: String(cursor) },
            onOpen: () => {
              attempt = 0
              setStatus('open')
            },
            onMessage: handle,
          })
          // Server closed the stream. If it ended cleanly, stop; otherwise reconnect.
          if (cancelled || endReceived) break
          setStatus('connecting')
        } catch (err) {
          if (cancelled || (err as Error).name === 'AbortError') return
          setStatus('error')
        }
        if (cancelled || endReceived) break
        const delay = Math.min(1000 * 2 ** attempt, 15_000)
        attempt += 1
        await new Promise((r) => setTimeout(r, delay))
      }
      if (!cancelled) {
        setStatus('closed')
        if (endReceived) setEnded(true)
      }
    }
    void run()

    return () => {
      cancelled = true
      controller.abort()
    }
  }, [active, runRef, token, tenant])

  return { status: active ? status : 'closed', ended }
}
