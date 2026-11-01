// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { currentControlFence } from '@/features/agentops/work-fence'
import { sessionTurnBody } from '@/features/agentops/session-turn'

/** Use only a fresh native command, never a project skill with the same name. */
export async function compactSession(
  run: RunDTO,
  signal: AbortSignal,
  failureMessage = 'Compaction was not confirmed. Your message was not sent.',
): Promise<void> {
  signal.throwIfAborted()
  const catalog = await agentOpsApi.commands(run.run_ref, { signal })
  if (
    run.provider_driver !== 'claude' ||
    catalog.tool !== 'claude' ||
    !catalog.reported ||
    catalog.stale ||
    !catalog.commands.some(
      (c) =>
        c.name === 'compact' && c.kind === 'command' && c.source === 'built-in',
    )
  )
    throw new Error(failureMessage)
  const before = await agentOpsApi.activity(run.run_ref, { signal })
  if (before.transcript !== 'read') throw new Error(failureMessage)
  const fence = await currentControlFence(run, { signal })
  signal.throwIfAborted()
  const body = sessionTurnBody(run, '/compact')
  if (!('line' in body)) throw new Error(failureMessage)
  await agentOpsApi.input(run.run_ref, body.line, fence)
  // Input acceptance is not completion. The native boundary is the same evidence
  // that resets the meter. Until it arrives, the draft stays out of the tool.
  for (let attempt = 0; attempt < 120; attempt++) {
    await new Promise<void>((resolve, reject) => {
      const abort = () => {
        clearTimeout(timer)
        reject(signal.reason)
      }
      const timer = setTimeout(() => {
        signal.removeEventListener('abort', abort)
        resolve()
      }, 1000)
      signal.addEventListener('abort', abort, { once: true })
      if (signal.aborted) {
        signal.removeEventListener('abort', abort)
        abort()
      }
    })
    const after = await agentOpsApi.activity(run.run_ref, { signal })
    if (after.transcript !== 'read') throw new Error(failureMessage)
    if (after.compactions.length > before.compactions.length) return
  }
  throw new Error(failureMessage)
}
