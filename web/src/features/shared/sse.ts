// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState } from 'react'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

/**
 * Server-Sent Events over fetch — the live-operation transport consumes.
 *
 * WHY NOT native EventSource: the engine authenticates with a bearer token and a
 * tenant header (Authorization + X-Olivares-Tenant), and EventSource cannot set
 * request headers. So we read the SSE stream with fetch + a ReadableStream reader,
 * which lets us attach auth, pin the tenant, abort cleanly on unmount, and
 * reconnect with backoff. The frame parser is a pure, testable function; the React
 * hook wraps it with the connection lifecycle. The web adds NO logic (ARCHITECTURE.md) —
 * it renders the same `liveDTO`/`statusDTO` snapshots the modules already push.
 */

/** One decoded SSE frame: an event name (defaults to "message") and its data. */
export interface SSEMessage {
  event: string
  data: string
}

/**
 * createSSEParser returns a function you feed raw text chunks; it invokes
 * `onMessage` once per complete frame (frames are separated by a blank line).
 * It handles CRLF and LF, multi-line `data:` accumulation, and ignores comment
 * lines (`:` — the engine's `: ping`/`: connected` heartbeats keep the socket
 * warm but carry no event). Incomplete trailing data is buffered across chunks.
 */
export function createSSEParser(
  onMessage: (msg: SSEMessage) => void,
): (chunk: string) => void {
  let buffer = ''
  return (chunk: string) => {
    buffer += chunk.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
    let sep: number
    while ((sep = buffer.indexOf('\n\n')) !== -1) {
      const frame = buffer.slice(0, sep)
      buffer = buffer.slice(sep + 2)
      let event = 'message'
      const dataLines: string[] = []
      for (const line of frame.split('\n')) {
        if (line === '' || line.startsWith(':')) continue // comment / heartbeat
        const colon = line.indexOf(':')
        const field = colon === -1 ? line : line.slice(0, colon)
        // Per the spec a single leading space after the colon is stripped.
        let value = colon === -1 ? '' : line.slice(colon + 1)
        if (value.startsWith(' ')) value = value.slice(1)
        if (field === 'event') event = value
        else if (field === 'data') dataLines.push(value)
      }
      if (dataLines.length > 0) onMessage({ event, data: dataLines.join('\n') })
    }
  }
}

export interface SubscribeOptions {
  /** Absolute API path, e.g. `/v1/m/sessions/stream`. */
  path: string
  token: string | null
  tenant: string | null
  signal: AbortSignal
  onMessage: (msg: SSEMessage) => void
  /** Called when the connection opens (HTTP 200 + body readable). */
  onOpen?: () => void
  /** Query params appended to the path. */
  query?: Record<string, string | undefined>
}

/**
 * subscribeStream opens ONE SSE connection and pumps frames into `onMessage`
 * until the body ends or the signal aborts. It throws on a non-OK response or a
 * transport error so the caller can decide whether to reconnect; an abort resolves
 * quietly. It does not retry — that policy lives in the hook.
 */
export async function subscribeStream(opts: SubscribeOptions): Promise<void> {
  const { path, token, tenant, signal, onMessage, onOpen, query } = opts
  const headers = new Headers({ Accept: 'text/event-stream' })
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  const url = new URL(path, window.location.origin)
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined) url.searchParams.set(k, v)
    }
  }

  const res = await fetch(url.pathname + url.search, {
    method: 'GET',
    headers,
    signal,
    credentials: 'same-origin',
  })
  if (!res.ok) {
    const err = new Error(`stream ${res.status}`) as Error & { status?: number }
    err.status = res.status
    throw err
  }
  if (!res.body) throw new Error('stream has no body')
  onOpen?.()

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const feed = createSSEParser(onMessage)
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      feed(decoder.decode(value, { stream: true }))
    }
  } finally {
    reader.releaseLock()
  }
}

export type StreamStatus = 'connecting' | 'open' | 'closed' | 'error'

export interface UseLiveStreamOptions<T> {
  /** Absolute API path of the SSE endpoint. */
  path: string
  /** Parsed-snapshot handler. Receives the JSON-parsed `data` of every frame whose
   * event is in `events` (default: any). Keep it cheap — it runs per frame. */
  onSnapshot: (snapshot: T, event: string) => void
  /** Restrict to these event names (e.g. `['session']`, `['health']`). */
  events?: string[]
  /** Optional query params (e.g. a single `ref`/`subject_ref`). */
  query?: Record<string, string | undefined>
  /** Gate the subscription (default true). When false the stream stays closed. */
  enabled?: boolean
  /**
   * OPTIONAL LIFETIME OWNER — an opaque token naming the context this subscription
   * belongs to. Supply it when "the same URL and the same tenant" is NOT the same
   * subscription: a caller whose acting principal, credential or read episode can
   * change while path, token and tenant stay put.
   *
   * ⛔ WHY A KEY EQUALITY TEST IS NOT ENOUGH, and why this is a token and not a
   *    comparison. A console that returns to a context it has visited before
   *    (principal A → B → A) computes the SAME key again. If the hook only compared
   *    keys, the first A connection — still draining, still holding a callback from a
   *    lifetime that has ended — would look current again. So the token is used to
   *    RETIRE a connection, not to identify it: a change retires whatever was open,
   *    and a retired connection stays retired for good, whatever the new value is.
   *
   * It must stay opaque and non-secret: never a token, a principal id or a secret
   * reference. An opaque counter or epoch is the intended shape. The hook attaches no
   * meaning to it, sends it nowhere and never logs it — it only owns lifetime.
   *
   * Callers that omit it keep exactly their previous behaviour, save that a message
   * arriving after their own connection was already retired is dropped instead of
   * delivered — which was never intended for anyone.
   */
  contextKey?: string | number
}

/**
 * useLiveStream subscribes to a module SSE endpoint for the lifetime of the
 * component (and the current tenant/token), parses each frame's JSON, and calls
 * `onSnapshot`. It reconnects with capped backoff on a dropped/errored connection
 * and tears down on unmount or when the tenant changes. Returns the connection
 * status so a view can render an honest "live" indicator (it never fakes "live").
 *
 * A connection is OWNED by the run of the effect that opened it. Everything it does
 * afterwards — a status change, an open, a delivered frame, a reconnection after its
 * backoff — is conditional on that run still being the current one. Callers that pass
 * `contextKey` additionally get a lifetime that ends when their context does, which is
 * what a caller whose acting authority can change without changing the URL needs.
 */
export function useLiveStream<T>({
  path,
  onSnapshot,
  events,
  query,
  enabled = true,
  contextKey,
}: UseLiveStreamOptions<T>): { status: StreamStatus } {
  const token = useSessionStore((s) => s.token)
  const tenant = useTenantStore((s) => s.activeTenant)
  const [status, setStatus] = useState<StreamStatus>('closed')

  // Keep the latest callback without restarting the stream on every render.
  const onSnapshotRef = useRef(onSnapshot)
  useEffect(() => {
    onSnapshotRef.current = onSnapshot
  }, [onSnapshot])
  // …and the context that owns the CURRENT subscription, so a connection can ask
  // whether it is still the one, rather than assume it because its key matches.
  const contextRef = useRef(contextKey)
  useEffect(() => {
    contextRef.current = contextKey
  }, [contextKey])
  const eventsKey = events?.join(',') ?? ''
  const queryKey = query ? JSON.stringify(query) : ''

  useEffect(() => {
    // When disabled or unauthenticated the stream stays closed — the returned
    // status is derived below, so we don't synchronously setState here.
    if (!enabled || !token) return
    const controller = new AbortController()
    // THE CONNECTION'S OWN LIFETIME. `retired` belongs to THIS run of the effect: it
    // is set by this run's cleanup and by nothing else, so it is one-way and per
    // connection. Aborting the request is not enough on its own — a frame already
    // parsed out of a chunk that was read before the abort is still sitting in the
    // call stack, and delivering it would hand the previous lifetime's data to
    // whoever is listening now.
    let retired = false
    const owner = contextKey
    const current = () => !retired && contextRef.current === owner
    let attempt = 0

    const run = async () => {
      while (!retired) {
        if (current()) setStatus('connecting')
        try {
          await subscribeStream({
            path,
            token,
            tenant,
            signal: controller.signal,
            query,
            onOpen: () => {
              if (!current()) return
              attempt = 0
              setStatus('open')
            },
            onMessage: (msg) => {
              // Checked HERE, per message, and not only when the connection was
              // opened: this is the last point before somebody else's data becomes
              // this context's.
              if (!current()) return
              if (events && !events.includes(msg.event)) return
              try {
                onSnapshotRef.current(JSON.parse(msg.data) as T, msg.event)
              } catch {
                // A malformed frame must never crash the view; skip it.
              }
            },
          })
          // Clean end of stream (server closed) — reconnect after a short pause.
          if (!retired) setStatus('connecting')
        } catch (err) {
          if (retired || (err as Error).name === 'AbortError') return
          setStatus('error')
        }
        if (retired) return
        // Capped exponential backoff: 1s, 2s, 4s … max 15s.
        const delay = Math.min(1000 * 2 ** attempt, 15_000)
        attempt += 1
        await new Promise((r) => setTimeout(r, delay))
        // The wait is where a retirement most often lands: re-check before looping,
        // so a retired connection never reopens after its backoff elapses.
        if (retired) return
      }
    }
    void run()

    return () => {
      retired = true
      controller.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- stable primitive keys
  }, [path, token, tenant, enabled, eventsKey, queryKey, contextKey])

  // Derive the closed state instead of setting it in the effect (a disabled or
  // unauthenticated stream reads as closed regardless of the last live status).
  return { status: enabled && token ? status : 'closed' }
}
