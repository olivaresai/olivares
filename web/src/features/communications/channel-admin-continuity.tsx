// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OPERATOR'S OWN UNSENT WORK, HELD WHERE THE PERMISSION CUT CANNOT REACH IT — and
// nothing else held anywhere.
//
// ⛔ THE MEASUREMENT THIS EXISTS FOR. `RequirePermission` mounts a protected view only for
//    `permitted`, and a capability observation is deliberately withdrawn at its deadline
//    (`capabilities.ts`: an expired positive is never replayed). Between that deadline and
//    the next answer the route's answer is a local `unknown`, so the WHOLE view is
//    unmounted and rebuilt. Measured on a real estate, three runs: 34–46 ms of teardown
//    every ~5 s, with the collection, the sheet, the configuration form, the operator's
//    typed name and their prepared confirmation destroyed each time — ETag unchanged at
//    `"v522"`, zero requests sent, nobody refused.
//
// ⛔ AND THE FIX IS NOT TO KEEP THE PROTECTED TREE ALIVE. Hiding it, disabling its
//    controls or holding the last positive would each require proving that every query,
//    effect, portal and callback under the cut is suspended — a property nobody has
//    demonstrated, and one that a single missed subscription turns into a read performed
//    without a current admission. The cut stays exactly where it is.
//
//    What is lifted above the cut is ONLY what the operator typed and could not send: the
//    touched fields, in the form's own draft terms, plus the neutral marks saying a
//    confirmation was interrupted and which control the caret was in. Not the Channel, not
//    its ETag, not a grant row, not a response, not a permit, not a credential, not a
//    prepared request. Nothing here can authorize, replay or reveal anything: it is the
//    operator's own text, which no permission answer produced and none invalidates.
//
// ⛔ AND IT IS NOT A CACHE. One channel, one context, one opening. Any change of identity
//    — principal class, actor, tenant, credential generation, workspace, or the local
//    movement counter that separates A→B→A from A — ends it, and so does every established
//    refusal, concealment, step-up, failed read and explicit close. It never outlives its
//    own route: no storage, no url, no module singleton, no telemetry.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import {
  isPositive,
  sameContext,
  useCapabilityPreflight,
  type CapabilityAccess,
  type CapabilityContext,
} from '@/lib/auth/capabilities'
import type { ChannelDraftHold } from './channel-config-form'

/**
 * Why an opening ended. It is an argument, not a record: a terminated opening keeps
 * nothing, not even the reason. Callers name their own cause in their own logs.
 */
export type CapsuleEnd =
  'closed' | 'channel' | 'context' | 'refused' | 'read-failed' | 'applied'

/**
 * What ONE opening holds. `hold` is the configuration form's existing contract, unchanged.
 *
 * ⛔ `generation` IS PART OF THE PAYLOAD, not only of the bookkeeping. A capsule tagged
 *    with a generation that is no longer current is unreachable by construction — before
 *    any cleanup effect has run, and whatever a late caller believes.
 */
interface Capsule {
  readonly generation: number
  readonly channelId: string
  readonly context: CapabilityContext
  readonly hold: ChannelDraftHold
  /** The logical control the caret was in, as the form's own field key. */
  readonly focus: string | null
}

export interface ChannelDraftCapsule {
  /**
   * The opening a consumer belongs to. Read at RENDER and handed back on every call, so a
   * callback retained from a previous opening carries the previous number and is refused —
   * even when the operator returns to the very same channel.
   */
  readonly generation: number
  /** Whether the route is admitted right now. Nothing is written or delivered otherwise. */
  readonly admitted: boolean
  /** Publish the operator's own edits, as they happen. */
  publish: (
    channelId: string,
    hold: ChannelDraftHold,
    generation: number,
    focus?: string | null,
  ) => void
  /** Take them back, for a mount of the same channel in the same context. */
  take: (
    channelId: string,
    generation: number,
  ) => { hold: ChannelDraftHold; focus: string | null } | null
  /**
   * End this opening: an explicit close, a refusal, a failed read, another channel.
   *
   * ⛔ IT CARRIES THE CALLER'S GENERATION TOO. A discard is as retainable as any other
   *    callback, and one fired from an opening that is already over must not end the
   *    opening that replaced it.
   */
  discard: (reason: CapsuleEnd, generation: number) => void
}

/**
 * THE OWNER'S OWN LIFE, as of the last committed render — the thing every retained method
 * is contrasted against, because a method cannot be trusted about the world it was born in.
 *
 * ⛔ THIS IS THE CORRECTION OF 2026-09-07, AND THE DEFECT IT CLOSES WAS REPRODUCED THREE
 *    TIMES ON THE DELIVERED SOURCE. The methods used to be built in a `useMemo` that
 *    CAPTURED `admitted`, `terminal` and `generation`; the live context reader kept the
 *    identity current but nothing kept those three current, and an empty capsule made the
 *    eligibility check answer "yes" without accrediting the owner at all. So a `take` kept
 *    from an admitted render still delivered the operator's text during the gap; a
 *    `publish` kept from a closed opening repopulated the payload and BLOCKED the new
 *    opening from saving anything; and both went on working after the owner had unmounted.
 *
 * `epoch` and `generation` are monotone, and the comparison against them is `>=` rather
 * than `===` on purpose: the last COMMITTED value is one behind the render in progress, so
 * a consumer mounting in the very commit that re-admitted the room carries a number one
 * AHEAD, while a retained closure carries one BEHIND. That separates "born now" from "born
 * earlier" without reading a ref during a render.
 */
interface OwnerState {
  /** Bumps whenever the admission picture changes — admitted, or ended by an answer. */
  readonly epoch: number
  /** The opening that is current for this owner. */
  readonly generation: number
  /** The context this owner last rendered with. */
  readonly context: CapabilityContext | null
  /** False the moment this owner unmounts, and never true again for it. */
  readonly alive: boolean
}

/** No boundary above: a capsule that holds nothing and returns nothing. A surface
 *  rendered outside one therefore keeps exactly the behaviour it had before this
 *  existed — continuity is a property of the ROUTE, never a default of the component. */
const NOTHING: ChannelDraftCapsule = {
  generation: 0,
  admitted: false,
  publish: () => {},
  take: () => null,
  discard: () => {},
}

const CapsuleContext = createContext<ChannelDraftCapsule>(NOTHING)

/** The capsule of the surrounding route, or the refusing one when there is no boundary. */
export function useChannelDraftCapsule(): ChannelDraftCapsule {
  return useContext(CapsuleContext)
}

/**
 * True for the answers that END an opening, as opposed to the two that only interrupt it.
 *
 * `checking` has not asked yet and `unknown` is this CLIENT's verdict on an expiry, a
 * movement, a malformed body or a transport that did not answer: the engine said nothing
 * about this operator in either, so their unsent text is not forfeit. A positive is not an
 * ending either, obviously: it is the state this exists to return to. Every REMAINING
 * answer ends the opening — an established refusal, a registered concealment, a
 * target-free assurance gate — and the route's own `kind` cannot make that distinction:
 * `unknown` and `step_up_required` both reach it as `unavailable`.
 */
function endsOpening(access: CapabilityAccess | null): boolean {
  if (access === null || isPositive(access)) return false
  return access !== 'checking' && access !== 'unknown'
}

/** `sameContext` answers false when either side is null; two absences are not a move. */
function sameOrBothAbsent(
  a: CapabilityContext | null,
  b: CapabilityContext | null,
): boolean {
  if (a === null && b === null) return true
  return sameContext(a, b)
}

/**
 * ChannelAdminContinuity — the continuity boundary of the channel-administration route.
 *
 * `RequirePermission` mounts it in ONE stable position around the answer it has ALREADY
 * decided: the protected children when they are permitted, its own notice otherwise. So
 * this component never receives the protected tree without an admission, cannot mount it,
 * and cannot be handed a fabricated `permitted` — it computes no admission and gates
 * nothing. Its whole job is to outlive that subtree by one refresh.
 */
export function ChannelAdminContinuity({
  admitted,
  access,
  children,
}: {
  /** The gate's own decision. Informational here; nothing is unlocked by it. */
  admitted: boolean
  /** The exact answer that decision came from, for the interrupt/end distinction. */
  access: CapabilityAccess | null
  children: ReactNode
}) {
  const preflight = useCapabilityPreflight()
  // The imperative reader, used at CALL time. A context read during a render is already
  // one render old when a late callback fires, and that window is exactly where an
  // invalidated capsule must not be written through.
  const live = preflight.live
  const current = preflight.context
  const held = useRef<Capsule | null>(null)
  // The owner's life, written only from effects and callbacks and read only at call time.
  // Its initial value is this first render's, so a consumer that mounts with the owner has
  // something true to be contrasted against before any effect has run.
  const owner = useRef<OwnerState>({
    epoch: 1,
    generation: 1,
    context: current,
    alive: true,
  })

  // ── what ends an opening, and what merely changes the admission ───────────────────
  //
  // Adjust-during-render rather than an effect: the numbers a consumer captures in THIS
  // render have to be this render's, and the payload's own tag has to become unreachable
  // at the same instant rather than one commit later.
  const terminal = endsOpening(access)
  const [generation, setGeneration] = useState(1)
  const [epoch, setEpoch] = useState(1)
  const [seen, setSeen] = useState({ admitted, terminal })
  if (seen.admitted !== admitted || seen.terminal !== terminal) {
    setSeen({ admitted, terminal })
    // EVERY CHANGE OF THE ADMISSION PICTURE OBSOLETES EVERY METHOD BORN UNDER THE OLD ONE.
    // It does not end the opening: a transient `unknown` is exactly the case whose whole
    // point is that the operator's text survives it.
    setEpoch((e) => e + 1)
    // AN ANSWER, THOUGH, ENDS IT. An established refusal, a concealment and a step-up all
    // arrive here, and none may leave the text where a later positive could pick it up.
    if (terminal) setGeneration((g) => g + 1)
  }
  // IDENTITY, WATCHED WHERE IT MOVES. `CapabilityContext` separates what
  // `CommunicationsScope.key` cannot: the principal's CLASS and actor (a token minted by
  // a user carries that user's id), and the local movement counter that makes A→B→A a
  // different authority from A even when every value matches again.
  const [seenContext, setSeenContext] = useState<CapabilityContext | null>(
    current,
  )
  if (!sameOrBothAbsent(seenContext, current)) {
    setSeenContext(current)
    setGeneration((g) => g + 1)
  }

  // ⛔ A LAYOUT EFFECT, AND THE CHOICE IS THE WHOLE ORDERING ARGUMENT. The layout effects
  //    of a commit run child-first and then parent, and every PASSIVE effect of that commit
  //    runs after all of them. So this line is the last committed truth before any consumer
  //    callback can fire, and a consumer that reads the capsule in its own mount — during
  //    the render, before this runs — is admitted by the `>=` comparison instead.
  useLayoutEffect(() => {
    owner.current = { epoch, generation, context: current, alive: true }
    // Housekeeping: a payload of a closed opening is already unreachable by the checks
    // below; this stops it occupying memory as well.
    if (held.current && held.current.generation !== generation)
      held.current = null
    return () => {
      owner.current = { ...owner.current, alive: false }
    }
  })
  // The owner is going away — route left, logged out, torn down. Nothing reappears on the
  // way back in, because nothing is left behind and nothing may write here again.
  useEffect(
    () => () => {
      held.current = null
    },
    [],
  )

  const discard = useCallback((_reason: CapsuleEnd, gen: number) => {
    const state = owner.current
    // A stale discard ends nothing: the opening it belonged to is already over, and the
    // one that replaced it is not the caller's to close.
    if (!state.alive || gen < state.generation) return
    const next = state.generation + 1
    // Written HERE and not only through state: the next callback may fire before React has
    // re-rendered, and it must already be looking at the new opening.
    owner.current = { ...state, generation: next }
    held.current = null
    setGeneration(next)
  }, [])

  const value = useMemo<ChannelDraftCapsule>(() => {
    /**
     * Every gate a write or a delivery passes: the CALLER's ticket, checked against the
     * OWNER's life as it stands at this instant — never against the world the caller was
     * born in, and never dependent on whether anything happens to be stored.
     */
    const eligible = (
      gen: number,
      now: CapabilityContext | null,
    ): now is CapabilityContext => {
      const state = owner.current
      // The owner is gone: nothing it once vouched for is vouched for now.
      if (!state.alive) return false
      // This method was not born under an admission…
      if (!admitted || terminal) return false
      // …or it was, and that admission has since been replaced.
      if (epoch < state.epoch) return false
      // The caller hands back the opening it belongs to, and that opening is not older
      // than the owner's.
      if (gen !== generation || generation < state.generation) return false
      // The identity the owner last rendered with is still the live one.
      if (!now || !sameContext(state.context, now)) return false
      return true
    }
    return {
      generation,
      admitted,
      publish: (channelId, hold, gen, focus = null) => {
        const now = live()
        if (!eligible(gen, now)) return
        // An overwrite, unconditionally: a payload left by another opening is not a reason
        // to refuse this one its own draft.
        held.current = { generation, channelId, context: now, hold, focus }
      },
      take: (channelId, gen) => {
        const now = live()
        if (!eligible(gen, now)) return null
        const capsule = held.current
        if (!capsule) return null
        // Not this caller's opening, not this caller's channel, not this identity: three
        // reasons to answer nothing, and none of them a reason to keep it either.
        if (capsule.generation !== generation) return null
        if (capsule.channelId !== channelId) return null
        if (!sameContext(capsule.context, now)) return null
        return { hold: capsule.hold, focus: capsule.focus }
      },
      discard,
    }
  }, [admitted, terminal, generation, epoch, live, discard])

  return (
    <CapsuleContext.Provider value={value}>{children}</CapsuleContext.Provider>
  )
}
