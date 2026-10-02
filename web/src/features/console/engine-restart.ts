// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'

/** True when the engine answers GET /v1/server-info (no session), the console's own
 * readiness read (the engine's machine readiness route is not a console call). */
export async function engineReady(): Promise<boolean> {
  try {
    await http.get('/v1/server-info', { anonymous: true })
    return true
  } catch {
    return false
  }
}

/**
 * Wait for the engine to come back after an apply that answered `restarting: true`.
 *
 * The engine accepts the restart and answers BEFORE it goes down, so the first
 * ready probe can still reach the old process. The wait therefore has two legs:
 * see the engine go away (one failed probe), then see it answer again. If it is
 * never seen down within `downWindowMs` (a restart faster than one probe), a ready
 * answer after that window counts. Resolves false only when the whole wait times
 * out or is aborted, so the caller can still reload instead of waiting forever.
 */
export async function waitForEngineRestart({
  probe = engineReady,
  signal,
  intervalMs = 1500,
  downWindowMs = 8_000,
  timeoutMs = 180_000,
  now = () => Date.now(),
  sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms)),
}: {
  probe?: () => Promise<boolean>
  signal?: AbortSignal
  intervalMs?: number
  downWindowMs?: number
  timeoutMs?: number
  now?: () => number
  sleep?: (ms: number) => Promise<void>
} = {}): Promise<boolean> {
  const start = now()
  let seenDown = false
  while (!signal?.aborted && now() - start < timeoutMs) {
    const ready = await probe()
    if (signal?.aborted) return false
    if (!ready) seenDown = true
    else if (seenDown || now() - start >= downWindowMs) return true
    await sleep(intervalMs)
  }
  return false
}
