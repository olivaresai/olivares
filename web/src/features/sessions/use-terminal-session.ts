// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONNECTION OF A BROWSER TERMINAL, with no terminal in it.
//
// The shell lives on the server (modules/sessions/terminal.go); the browser holds an id.
// Output arrives as server-sent events with the offset each chunk starts at, so a dropped
// connection resumes where it stopped, and a page reload finds the same shell while the
// server keeps it (its reconnect window). Input goes out as plain requests, one at a time
// and in order, so what is typed arrives as it was typed.
//
// ⛔ A RECONNECT NEVER STARTS A SHELL. Only `open` does, and only because a person asked.
//    Resuming an id the server no longer holds ends quietly with nothing started.
import { useCallback, useEffect, useRef, useState } from 'react'
import { agentOpsApi } from '@/features/agentops/api'
import { subscribeStream } from '@/features/shared/sse'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

export type TerminalState =
  'idle' | 'starting' | 'live' | 'reconnecting' | 'ended'

export interface TerminalProblem {
  /** `step_up` needs the person to confirm their identity first; the rest is shown as is. */
  kind: 'step_up' | 'refused' | 'busy' | 'failed'
}

const FIRST_RETRY_MS = 500
const RESIZE_DEBOUNCE_MS = 120
const MAX_RETRY_MS = 2000
const DEFAULT_WINDOW_MS = 60_000

const storageKey = (tenant: string | null, ref: string) =>
  `olivares.terminal.${tenant ?? ''}.${ref}`

function remembered(key: string): string | null {
  try {
    return window.sessionStorage.getItem(key)
  } catch {
    return null
  }
}

function remember(key: string, id: string | null) {
  try {
    if (id) window.sessionStorage.setItem(key, id)
    else window.sessionStorage.removeItem(key)
  } catch {
    // A browser without storage loses the reload reconnect, nothing else.
  }
}

function decode(b64: string): Uint8Array {
  const raw = atob(b64)
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  return bytes
}

function problemOf(err: unknown): TerminalProblem {
  if (err instanceof ApiError) {
    if (err.code === 'step_up_required') return { kind: 'step_up' }
    if (err.status === 429) return { kind: 'busy' }
    if (err.status === 403 || err.status === 404) return { kind: 'refused' }
  }
  return { kind: 'failed' }
}

export interface TerminalSession {
  state: TerminalState
  problem: TerminalProblem | null
  exitCode: number | null
  /** Forgets the reason the last start was refused, as the person asks again. */
  dismiss: () => void
  /** This tab opened a shell before a reload, and `resume` may still find it. */
  canResume: boolean
  /** Starts a shell in the folder; the size is the window the terminal already has. */
  open: (rows: number, cols: number) => Promise<void>
  /** Takes the shell this tab opened before a reload, if the server still holds it. */
  resume: () => boolean
  send: (data: string) => void
  resize: (rows: number, cols: number) => void
  /** Ends the shell. */
  close: () => Promise<void>
}

/**
 * useTerminalSession drives one terminal of one folder. `onOutput` receives the bytes the
 * shell prints, in order, each exactly once. Unmounting leaves the shell to the server's
 * reconnect window; `close` ends it.
 */
export function useTerminalSession(
  workspaceRef: string,
  onOutput: (bytes: Uint8Array) => void,
): TerminalSession {
  const token = useSessionStore((s) => s.csrfToken)
  const tenant = useTenantStore((s) => s.activeTenant)
  const key = storageKey(tenant, workspaceRef)
  const [canResume] = useState(() => remembered(key) !== null)
  // A tab that remembers a shell is reconnecting to it from the first paint.
  const [state, setState] = useState<TerminalState>(
    canResume ? 'reconnecting' : 'idle',
  )
  const [problem, setProblem] = useState<TerminalProblem | null>(null)
  const [exitCode, setExitCode] = useState<number | null>(null)

  const output = useRef(onOutput)
  useEffect(() => {
    output.current = onOutput
  }, [onOutput])

  const idRef = useRef<string | null>(null)
  const offset = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const pending = useRef('')
  const sending = useRef(false)
  // How long the server keeps a shell nobody is attached to; the answer to `open` says.
  const windowMs = useRef(DEFAULT_WINDOW_MS)
  // True from `resume` until the stream first opens: a shell the server no longer holds
  // then returns to the idle panel instead of showing an ended terminal.
  const resuming = useRef(false)
  const resizeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  )

  const stop = useCallback(() => {
    controller.current?.abort()
    controller.current = null
  }, [])

  const end = useCallback(
    (code: number | null) => {
      stop()
      idRef.current = null
      remember(key, null)
      pending.current = ''
      setExitCode(code)
      setState('ended')
    },
    [key, stop],
  )

  // A shell this tab remembered but the server no longer holds: back to the idle panel.
  const forget = useCallback(() => {
    stop()
    idRef.current = null
    resuming.current = false
    remember(key, null)
    setState('idle')
  }, [key, stop])

  // Reads the output until the shell ends. A dropped connection is retried a few times,
  // from the offset already shown; a terminal the server no longer holds ends it.
  const attach = useCallback(
    (id: string) => {
      stop()
      const ctl = new AbortController()
      controller.current = ctl
      idRef.current = id
      const run = async () => {
        let lostSince: number | null = null
        let wait = FIRST_RETRY_MS
        while (!ctl.signal.aborted) {
          try {
            await subscribeStream({
              path: `/v1/m/sessions/workspaces/${encodeURIComponent(workspaceRef)}/terminals/${encodeURIComponent(id)}/stream`,
              query: { from: String(offset.current) },
              token,
              tenant,
              signal: ctl.signal,
              onOpen: () => {
                lostSince = null
                wait = FIRST_RETRY_MS
                resuming.current = false
                setState('live')
              },
              onMessage: (msg) => {
                if (ctl.signal.aborted) return
                if (msg.event === 'exit') {
                  const body = JSON.parse(msg.data) as { code?: number }
                  end(body.code ?? null)
                  return
                }
                if (msg.event !== 'out') return
                const frame = JSON.parse(msg.data) as { o: number; d: string }
                const bytes = decode(frame.d)
                // Bytes at or before the offset were shown already.
                const skip = Math.max(0, offset.current - frame.o)
                if (skip >= bytes.length) return
                offset.current = frame.o + bytes.length
                output.current(skip ? bytes.subarray(skip) : bytes)
              },
            })
            if (idRef.current !== id) return // ended by an exit frame
          } catch (err) {
            if (ctl.signal.aborted) return
            const status = (err as { status?: number }).status
            if (status === 404 || status === 410) {
              if (resuming.current) forget()
              else end(null)
              return
            }
          }
          if (ctl.signal.aborted) return
          // The server keeps the shell for its window: keep asking for that long.
          lostSince ??= Date.now()
          if (Date.now() - lostSince > windowMs.current) {
            end(null)
            return
          }
          setState('reconnecting')
          await new Promise((r) => setTimeout(r, wait))
          wait = Math.min(wait * 2, MAX_RETRY_MS)
        }
      }
      void run()
    },
    [end, forget, stop, tenant, token, workspaceRef],
  )

  const open = useCallback(
    async (rows: number, cols: number) => {
      stop()
      setProblem(null)
      setExitCode(null)
      setState('starting')
      offset.current = 0
      try {
        const started = await agentOpsApi.startTerminal(workspaceRef, {
          rows,
          cols,
        })
        remember(key, started.terminal)
        windowMs.current = started.reconnect_seconds * 1000
        resuming.current = false
        attach(started.terminal)
      } catch (err) {
        setProblem(problemOf(err))
        setState('idle')
      }
    },
    [attach, key, stop, workspaceRef],
  )

  const dismiss = useCallback(() => setProblem(null), [])

  const resume = useCallback(() => {
    const id = remembered(key)
    if (!id) return false
    setProblem(null)
    setExitCode(null)
    setState('reconnecting')
    offset.current = 0
    resuming.current = true
    attach(id)
    return true
  }, [attach, key])

  const flush = useCallback(async () => {
    if (sending.current) return
    sending.current = true
    try {
      while (pending.current && idRef.current) {
        // UTF-8 needs at most three bytes per UTF-16 code unit. Keep each
        // request below the server's 64 KiB limit, including non-ASCII paste.
        let chunkEnd = Math.min(pending.current.length, Math.floor(65536 / 3))
        const last = pending.current.charCodeAt(chunkEnd - 1)
        if (
          chunkEnd < pending.current.length &&
          last >= 0xd800 &&
          last <= 0xdbff
        )
          chunkEnd--
        const data = pending.current.slice(0, chunkEnd)
        pending.current = pending.current.slice(chunkEnd)
        try {
          await agentOpsApi.terminalInput(workspaceRef, idRef.current, data)
        } catch (err) {
          if (
            err instanceof ApiError &&
            (err.status === 404 || err.status === 410)
          )
            end(null)
          else setProblem(problemOf(err))
          return
        }
      }
    } finally {
      sending.current = false
    }
  }, [end, workspaceRef])

  const send = useCallback(
    (data: string) => {
      if (!idRef.current) return
      pending.current += data
      void flush()
    },
    [flush],
  )

  const resize = useCallback(
    (rows: number, cols: number) => {
      clearTimeout(resizeTimer.current)
      resizeTimer.current = setTimeout(() => {
        const id = idRef.current
        if (id)
          void agentOpsApi
            .terminalSize(workspaceRef, id, { rows, cols })
            .catch(() => {})
      }, RESIZE_DEBOUNCE_MS)
    },
    [workspaceRef],
  )

  const close = useCallback(async () => {
    const id = idRef.current
    end(null)
    if (id) {
      try {
        await agentOpsApi.closeTerminal(workspaceRef, id)
      } catch {
        // The server ends an unattended shell on its own within the reconnect window.
      }
    }
  }, [end, workspaceRef])

  useEffect(
    () => () => {
      stop()
      clearTimeout(resizeTimer.current)
    },
    [stop],
  )

  return {
    state,
    problem,
    exitCode,
    canResume,
    dismiss,
    open,
    resume,
    send,
    resize,
    close,
  }
}
