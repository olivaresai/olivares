// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Which word a run's state is said with: the rule, and the one read it may need.
import { useQuery } from '@tanstack/react-query'
import { useTenantStore } from '@/stores/tenant'
import { agentOpsApi, agentOpsKeys } from './api'
import type { RunDTO, RunEventDTO } from './types'

export type RunWordFacts = Pick<
  RunDTO,
  'run_ref' | 'state' | 'reason' | 'exit_code'
>

/** The page of a run's ledger the badge reads: the same read as the detail's Events tab. */
const LEDGER_PAGE = { limit: 200 }

/**
 * The word for a run's state (dev#3120: Recent work read every finished run as Stopped,
 * which reads as cancelled). Finished is Done; Stopped is a stop a person or a policy
 * asked for.
 *
 * ⛔ THE ROW ALONE CANNOT TELL THEM APART. When the tool ends with exit 0 and no stop was
 *    asked, the engine records the reason "exit 0" (modules/sessions/runtime_bridge.go);
 *    a person's Stop records no reason, so a tool that exits cleanly on it leaves the same
 *    row (r08b-rows.fixture.ts, `olivares session stop`). The run's own ledger can: an
 *    asked stop appends `stopping` after the run last started. Without the ledger such a
 *    run says Ended, which is true of both. Any stop that recorded a reason (the idle
 *    policy, a hand-off, an access change) or exit code is Stopped from the row.
 */
export function runStateWord(
  run: Omit<RunWordFacts, 'run_ref'>,
  ledger?: readonly RunEventDTO[],
): string {
  if (run.state !== 'stopped' || run.exit_code !== 0 || run.reason !== 'exit 0')
    return String(run.state)
  if (!ledger) return 'ended'
  const last = (match: (e: RunEventDTO) => boolean) =>
    ledger.reduce((seq, e) => (match(e) ? Math.max(seq, e.seq) : seq), -1)
  return last((e) => e.event === 'stopping') >
    last((e) => e.to_state === 'running')
    ? 'stopped'
    : 'done'
}

/**
 * The ledger of a run whose row cannot say whether it finished or was stopped, read only
 * for such a run. It is read again like any other read: a run that is resumed and ends
 * again has a newer ledger. Undefined while unread, refused or longer than one page.
 */
export function useRunLedger(
  run: RunWordFacts | undefined,
): readonly RunEventDTO[] | undefined {
  const tenant = useTenantStore((s) => s.activeTenant)
  const unsure = !!run && runStateWord(run) === 'ended'
  const read = useQuery({
    queryKey: agentOpsKeys.runEvents(tenant, run?.run_ref ?? '', LEDGER_PAGE),
    queryFn: () => agentOpsApi.runEvents(run!.run_ref, LEDGER_PAGE),
    enabled: unsure,
    retry: false,
  })
  return unsure && read.data && !read.data.has_more
    ? read.data.items
    : undefined
}
