// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import type { MutationOptions } from './api'
import { classifyFailure, type Failure } from './errors'
import {
  StaleIntentError,
  type AuthoritySnapshot,
  type IntentGuard,
  type MovedFact,
} from './intent'

/**
 * The controller of one handoff mutation. It lives in the context-scoped host, not
 * in a dialog, so hiding a panel cannot turn a transmitted request into a draft.
 *
 * Phases:
 *  - idle        nothing confirmed; the editor is free.
 *  - reviewing   key, body, target, precondition and authority frozen; nothing sent.
 *  - submitting  on the wire.
 *  - confirmed   the engine returned a receipt for this command.
 *  - refused     a definitive typed refusal of this request.
 *  - conflict    the precondition or command no longer applies; a new review is
 *                required and the validator is never replaced in place.
 *  - uncertain   the outcome is unknown; the intent survives for a same-key retry.
 *  - lost        local tracking ended because authority or admission moved.
 */
export type HandoffOperationState<I, O> =
  | { phase: 'idle' }
  | { phase: 'reviewing'; intent: I }
  | { phase: 'submitting'; intent: I }
  | { phase: 'confirmed'; intent: I; outcome: O }
  | { phase: 'refused'; failure: Failure }
  | { phase: 'conflict'; failure: Failure }
  | { phase: 'uncertain'; intent: I; failure: Failure }
  | { phase: 'lost'; moved: MovedFact; sent: boolean }

export type HandoffOperationPhase = HandoffOperationState<
  unknown,
  unknown
>['phase']

/** The minimum an intent must carry for the transport to judge it. */
export interface GuardedIntent {
  readonly authority: AuthoritySnapshot
  readonly scope: { readonly tenant: string | null }
}

export interface HandoffOperation<I extends GuardedIntent, O> {
  state: HandoffOperationState<I, O>
  /** A transmitted command with no resolved result. A new target must not replace it. */
  unresolved: boolean
  /**
   * An EARLIER attempt of this invocation ended without establishing its outcome —
   * an ambiguous transport failure, an abort, or a guard refusal that may have
   * followed a first leg. It is set only by such an attempt, never by an ordinary
   * dispatch, so a first definitive refusal cannot invent one. A later definitive
   * answer describes the request that returned it and does not clear this; only a
   * receipt for this same command does.
   */
  priorUnresolvedAttempt: boolean
  /**
   * Freeze the confirmation. Accepted ONLY from `idle`; from every other phase it
   * is a no-op that changes neither the state, the generation, the history flag
   * nor the protected intent. Use `reset` or `discard` to end an invocation
   * first — review never ends one implicitly.
   */
  review: (intent: I) => void
  submit: () => void
  /** Re-send the same object: same key, body, target, validator and authority. */
  retrySame: () => void
  /** End local tracking. It cannot cancel or roll back a transmitted command. */
  discard: () => void
  /** Return to idle so this surface can start a different operation. */
  reset: () => void
  guard: IntentGuard
}

/**
 * The permission literals are built by the caller and handed in as a guard.
 * `check-console-perms` resolves the literals that flow into `useIntentGuard`, and
 * one extra parameter hop makes them unreadable — a permission no screen asks for
 * is hidden for every role with no 403 to notice it by.
 */
export function useHandoffOperation<I extends GuardedIntent, O>({
  send,
  allowed,
  guard,
}: {
  send: (intent: I, options: MutationOptions, signal: AbortSignal) => Promise<O>
  /** The admission this surface currently observes for the act. */
  allowed: boolean
  guard: IntentGuard
}): HandoffOperation<I, O> {
  const { t } = useTranslation('communications')
  const [state, setState] = useState<HandoffOperationState<I, O>>({
    phase: 'idle',
  })
  const [priorUnresolvedAttempt, setPriorUnresolvedAttempt] = useState(false)
  const [lostCount, setLostCount] = useState(0)

  /**
   * The generation of the dispatch a callback belongs to. Retiring it before any
   * state is cleared or any observation aborted is what stops a late resolution
   * from repopulating a lifetime that has ended. A mounted flag cannot do this: the
   * component stays mounted across admission loss and explicit discard.
   */
  const generation = useRef(0)
  const live = useRef(true)
  const allowedRef = useRef(allowed)

  // Layout effects, not passive ones: admission has to be current before any
  // promise callback can run, or a late resolution would write into a lifetime
  // this render has already ended.
  useLayoutEffect(() => {
    allowedRef.current = allowed
  })
  useLayoutEffect(() => {
    live.current = true
    return () => {
      live.current = false
      generation.current += 1
    }
  }, [])

  /** A callback may write only if it is the current dispatch and still admitted. */
  const owns = (gen: number) =>
    live.current && gen === generation.current && allowedRef.current

  const retire = useCallback(() => {
    generation.current += 1
  }, [])

  /**
   * Admission loss ends the lifetime of every local artefact of this operation:
   * the reviewed intent, the receipt and the typed failure all describe protected
   * work. Only the neutral notice survives, and the sticky uncertainty of an
   * already transmitted command survives with it.
   */
  const admitted = allowed
  const [seenAdmitted, setSeenAdmitted] = useState(admitted)
  if (seenAdmitted !== admitted) {
    setSeenAdmitted(admitted)
    if (!admitted) {
      if (state.phase !== 'idle') {
        // The uncertainty carries over into `lost.sent`, which is the neutral
        // diagnostic; the flag is cleared so one fact has one representation and
        // a later invocation cannot inherit it.
        setState({
          phase: 'lost',
          moved: 'permission',
          sent:
            state.phase === 'submitting' ||
            state.phase === 'uncertain' ||
            priorUnresolvedAttempt,
        })
        setLostCount((n) => n + 1)
      }
      setPriorUnresolvedAttempt(false)
    }
  }
  // The generation is retired in a layout effect of the same commit, so the state
  // adjustment above and the revocation land together.
  useLayoutEffect(() => {
    if (!admitted) {
      generation.current += 1
      guard.end()
    }
  }, [admitted, guard])
  useEffect(() => {
    if (lostCount > 0) toast.warning(t('authority.confirmationClosed'))
  }, [lostCount, t])

  const dispatch = useCallback(
    (intent: I) => {
      const signal = guard.begin()
      if (!signal) {
        // Refused before the first fetch: no bytes left this console.
        setState({ phase: 'lost', moved: 'surface', sent: false })
        return
      }
      const gen = ++generation.current
      // Pending dispatch is the CURRENT attempt's state, not history: the phase
      // already says the request is on the wire, and marking history here would
      // make a first definitive refusal claim an earlier unconfirmed transmission.
      setState({ phase: 'submitting', intent })
      send(intent, { tenant: intent.scope.tenant, guard: guard.check }, signal)
        .then((outcome) => {
          if (!owns(gen)) return
          // A receipt for THIS command resolves it, including any earlier unknown
          // attempt of the same command that this retry has now answered.
          setPriorUnresolvedAttempt(false)
          setState({ phase: 'confirmed', intent, outcome })
        })
        .catch((err: unknown) => {
          if (!owns(gen)) return
          if (err instanceof StaleIntentError) {
            // The guard refused before the first fetch or before the single 401
            // replay, and those are not distinguishable here: the outcome is
            // unknown rather than unsent, so it becomes history.
            setPriorUnresolvedAttempt(true)
            setState({ phase: 'lost', moved: err.moved, sent: true })
            return
          }
          if (err instanceof AuthorityLostError) {
            setPriorUnresolvedAttempt(true)
            setState({ phase: 'lost', moved: 'surface', sent: true })
            return
          }
          const failure = classifyFailure(err)
          if (
            failure.kind === 'aborted' ||
            failure.kind === 'ambiguous' ||
            failure.kind === 'unavailable'
          ) {
            // The transport could not establish whether bytes were sent, so this
            // attempt joins the history a later observation must not overwrite.
            // An abort in particular cannot restore an editable review.
            setPriorUnresolvedAttempt(true)
            setState({ phase: 'uncertain', intent, failure })
            return
          }
          if (
            failure.kind === 'conflict' ||
            failure.kind === 'version_mismatch' ||
            failure.kind === 'plan_changed' ||
            failure.kind === 'version_required' ||
            failure.kind === 'terminal'
          ) {
            // A definitive answer about the request that just returned. It ends
            // this intent, and it neither creates nor clears history about any
            // earlier attempt.
            setState({ phase: 'conflict', failure })
            return
          }
          setState({ phase: 'refused', failure })
        })
    },
    [guard, send],
  )

  const unresolved = state.phase === 'submitting' || state.phase === 'uncertain'

  /**
   * Only `idle` accepts a review. Every other phase — reviewing, submitting,
   * uncertain, confirmed, refused, conflict, lost — leaves the state, the
   * generation, the history flag and the protected intent exactly as they were.
   *
   * The restriction is what stops a new command from inheriting an earlier one's
   * uncertainty: without it, a review taken straight from `conflict` carried k1's
   * history into k2's lifetime, and k2's receipt then cleared it — a receipt for
   * a command that never answered k1. Ending an invocation is the explicit
   * `reset` or `discard`, which is where the caller states the limit.
   */
  const review = useCallback((intent: I) => {
    setState((s) => (s.phase === 'idle' ? { phase: 'reviewing', intent } : s))
  }, [])
  const submit = useCallback(() => {
    if (state.phase !== 'reviewing') return
    dispatch(state.intent)
  }, [state, dispatch])
  const retrySame = useCallback(() => {
    if (state.phase !== 'uncertain') return
    dispatch(state.intent)
  }, [state, dispatch])
  /**
   * End local tracking of this invocation. Retire before aborting, so a response
   * already on its way cannot write into the state this clears. The history flag
   * goes with it: the invocation is over, and the caller states the truthful
   * no-cancellation limit instead of carrying uncertainty into the next one.
   */
  const discard = useCallback(() => {
    retire()
    guard.end()
    setPriorUnresolvedAttempt(false)
    setState({ phase: 'idle' })
  }, [guard, retire])
  /**
   * Return to idle after a resolved result. It REFUSES an unresolved operation
   * before retiring anything: retiring first would invalidate the callback that
   * could still resolve the state left on screen. Ending an unresolved operation
   * is `discard`, which is explicit.
   */
  const reset = useCallback(() => {
    if (state.phase === 'submitting' || state.phase === 'uncertain') return
    retire()
    setPriorUnresolvedAttempt(false)
    setState({ phase: 'idle' })
  }, [retire, state])

  return {
    state,
    unresolved,
    priorUnresolvedAttempt,
    review,
    submit,
    retrySame,
    discard,
    reset,
    guard,
  }
}
