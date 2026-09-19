// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH CONTROL PLANE ONE RUN ANSWERS ON — read from the run, never assumed.
//
// A run that carries a K2 work stamp is controlled through its lease fence for the
// REST OF ITS LIFE, even after that lease generation ends: the stamp is immutable and
// permanently selects the fenced plane (`modules/sessions/runtime_work.go`,
// refuseLegacyControlUnderWork). The unfenced /input, /stop and /resume routes answer
// such a run with 409 "work-bound session requires fenced runtime control" before any
// child effect.
//
// ⛔ THE FENCE IS NOT A CONTROL'S PROPERTY, IT IS THE RUN'S. It was read here for
//    interrupt and nowhere else, so the console could attach to a work-bound session,
//    show its conversation and its composer, and have every turn refused — while
//    `agent session input --text 'x' --work-lease-fence N` succeeded on that same run.
//    One derivation, used by every control, is why that cannot come back a door at a
//    time.
import type { RunDTO } from './types'

/** The four K2 authority links. A legacy or non-work run omits all of them. */
export type WorkBinding = Pick<
  RunDTO,
  'work_item_id' | 'work_lease_fence' | 'work_dispatch_key' | 'work_owner_epoch'
>

/**
 * Whether this run is bound to a work item, by the same predicate the engine uses
 * (`runHasWorkBinding`): ANY of the four stamps present, not all four.
 */
export function isWorkBound(run: WorkBinding): boolean {
  return [
    run.work_item_id,
    run.work_lease_fence,
    run.work_dispatch_key,
    run.work_owner_epoch,
  ].some((value) => value !== undefined)
}

/**
 * The fence this run's controls must present, or `undefined` when there is none to
 * present and the unfenced plane is the correct one.
 *
 * ⛔ A FENCE IS PRESENTED ONLY WHEN IT IS ONE. The engine answers 400 to a
 *    non-positive `work_lease_fence` (`runtime_api.go` handleInput), so a work-bound
 *    run whose fence did not arrive — or arrived as something that is not a positive
 *    safe integer — is left to the 409 of the unfenced plane. That is the deny-closed
 *    direction: the operator is told the session cannot be controlled from here, which
 *    is true, instead of the request being rejected as malformed, which would blame
 *    the wrong thing. It is never a reason to invent a number.
 */
export function workLeaseFenceFor(run: WorkBinding): number | undefined {
  if (!isWorkBound(run)) return undefined
  const fence = run.work_lease_fence
  return fence !== undefined && Number.isSafeInteger(fence) && fence > 0
    ? fence
    : undefined
}
