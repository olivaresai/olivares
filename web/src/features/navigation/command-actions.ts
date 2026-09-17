// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE ANSWER TO "MAY THIS PRINCIPAL RUN THIS PALETTE VERB, HERE, NOW" — asked twice.
//
// ⛔ THE DEFECT THIS CLOSES, and it is an affordance defect, not an engine bypass. The
//    palette built its action list out of the views it could NAVIGATE to, so the verb
//    "New alert route" was offered to anyone holding `notify:route:read`. The engine has
//    always refused the write; what the console did was offer it, carry the operator away
//    from their work, and — in alerting, whose create dialog was the one dialog not gated
//    by `canWrite` — hand them the very form the page's own button hides, take their
//    typing and lose it at submit. `specification04 §1`: "An action in the palette requires
//    its mutation permission, target, and current context; read permission for its page
//    does not authorize it."
//
// ⛔ TWO CHECKS, AND THE SECOND IS NOT CEREMONY. Offering is decided in the palette;
//    acting is decided in the feature root, on the authority that is live when the command
//    is observed. Between the two there is a selection, possibly a navigation, and whatever
//    time the operator spent — a revocation, a tenant switch or a credential refresh all
//    fit in that gap. OPENING A FORM IS NOT AUTHORIZATION TO SUBMIT either: every server
//    check, step-up and recording gate downstream is untouched by this file.
//
// ⛔ AND NOTHING HERE AUTHORIZES ANYTHING. `authorized()` is the existing whoami
//    reflection (`can`) plus the tenant the console is actually standing in; `capture()`
//    is the existing capability context. No permit is minted, stored or replayed.
import { useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { FEATURE_VIEWS, type CommandAction } from '@/features/registry'
import {
  liveCapabilityContext,
  type CapabilityContext,
} from '@/lib/auth/capabilities'
import { useAuth } from '@/lib/auth/context'
import { useCommandStore } from '@/stores/command'

/**
 * The declared verbs, indexed by view and id. Built once from the registry, which is the
 * navigation authority: a view file cannot invent a verb, and a verb cannot lose its
 * permission by being copied into a component.
 */
const DECLARED: ReadonlyMap<
  string,
  ReadonlyMap<string, CommandAction>
> = new Map(
  FEATURE_VIEWS.filter((v) => v.commandActions?.length).map((v) => [
    v.id,
    new Map((v.commandActions ?? []).map((a) => [a.id, a])),
  ]),
)

/**
 * The verb `featureId` declares under `actionId`, or null.
 *
 * Null is a REFUSAL, not an absence to be worked around: a view asking for a verb the
 * registry does not declare has no permission to check, and the honest answer to "may I
 * run it" is no.
 */
export function commandActionOf(
  featureId: string,
  actionId: string,
): CommandAction | null {
  return DECLARED.get(featureId)?.get(actionId) ?? null
}

export interface CommandActionAuthority {
  /**
   * Whether this verb may be OFFERED and RUN right now: its own mutation permission, and a
   * tenant to run it in.
   *
   * The tenant term is not decoration. Every one of these writes is a tenant-scoped route,
   * so with none selected the verb has no target — `spec04 §1` requires the target as much
   * as the permission — and a form opened against nothing can only fail at submit.
   */
  authorized: (action: CommandAction) => boolean
  /**
   * The live identity to bind a queued command to, or null when none is established.
   *
   * Imperative on purpose, and read at CALL time: the context of a render is already one
   * render old by the time a selection handler or a mount effect runs.
   */
  capture: () => CapabilityContext | null
}

/**
 * Whether a command may be DISPATCHED or matched in this identity: established, and with a
 * tenant to write in.
 *
 * ⛔ THIS IS THE HALF OF THE SELECTION RE-CHECK THAT IS GENUINELY CALL-TIME, and the
 *    distinction is worth stating because the other half is not. `can()` reaches a
 *    selection handler as the closure of the last committed render — a revocation that
 *    has not re-rendered yet is invisible to it, and re-deriving the permission rule
 *    imperatively to fix that would create the second implementation of RBAC this console
 *    deleted in. The tenant and the rest of the identity are read from their own
 *    stores and the query cache AT CALL TIME, so a movement between the render that listed
 *    the verb and the keystroke that selects it is seen here.
 *
 *    The check that closes the remaining window is the one in the feature root, which
 *    re-asks `can` when it observes the command.
 */
export function dispatchable(
  context: CapabilityContext | null,
): context is CapabilityContext {
  return !!context && context.tenant !== null
}

/** The shared projection for palette verbs. The palette offers with it; the view acts with it. */
export function useCommandActionAuthority(): CommandActionAuthority {
  const { can, activeTenant } = useAuth()
  const queryClient = useQueryClient()
  return useMemo<CommandActionAuthority>(
    () => ({
      authorized: (action) => !!activeTenant && can(action.permission),
      capture: () => liveCapabilityContext(queryClient),
    }),
    [can, activeTenant, queryClient],
  )
}

/**
 * Observe this feature's pending palette command and consume it once, accepting it only if
 * the principal may still run the verb in the identity that queued it.
 *
 * ⛔ CALL THIS FROM THE FEATURE ROOT, the component that is mounted for the whole visit.
 *    The hook watches the store, so a command queued while the feature is ALREADY on
 *    screen is consumed on that arrival — the case a mount-only consumer missed, because
 *    navigating to the path already shown remounts nothing. A consumer nested in a tab
 *    would still miss a command queued while another tab is showing.
 *
 * The conditions, in the order they are asked:
 *
 *  1. the store holds a command FOR THIS FEATURE — another feature's is left where it is;
 *  2. it was queued in the identity that is live now (`sameContext`, so a tenant,
 *     principal, credential or workspace movement retires it, A → B → A included);
 *  3. it is the verb this call site handles, and this principal still holds its permission.
 *
 * ⛔ THE COMMAND IS CLEARED WHENEVER 1 HOLDS, whatever 2 and 3 answer, and it is cleared
 *    WHEN IT IS OBSERVED rather than when this component unmounts. A verb refused because
 *    the grant went is retired there and then, so a grant that comes back later finds
 *    nothing to open.
 *
 * ⛔ ONE OBSERVER PER FEATURE. Consumption is feature-scoped, so a feature that grows a
 *    second verb needs the store's grain changed with it — not a second call to this hook.
 *
 * The authority inputs are latched in a layout effect, committed before any passive effect
 * of the same commit, so the consumption reads the current render's authority rather than
 * a closure from the first one.
 */
export function usePendingCommandAction(
  featureId: string,
  actionId: string,
  accept: () => void,
): void {
  const { authorized, capture } = useCommandActionAuthority()
  // The store subscription is what makes this an observation rather than a mount hook.
  // The selector answers the same object until the command is replaced or cleared, so it
  // schedules exactly one effect per queued command.
  const queued = useCommandStore((s) =>
    s.pendingAction?.featureId === featureId ? s.pendingAction : null,
  )
  const latest = useRef({ authorized, capture, accept })
  useLayoutEffect(() => {
    latest.current = { authorized, capture, accept }
  })
  useEffect(() => {
    if (!queued) return
    const current = latest.current
    const action = commandActionOf(featureId, actionId)
    const taken = useCommandStore
      .getState()
      .consumeAction(featureId, current.capture())
    if (!action || taken !== action.id) return
    if (!current.authorized(action)) return
    current.accept()
  }, [featureId, actionId, queued])
}
