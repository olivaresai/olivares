// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CAPABILITY OBSERVATION — what the ENGINE says this credential may do right now,
// for ONE registered operation or collection, with a finite window on the answer.
//
// This is not a second `can()`. `can()` (lib/auth/context.tsx) is the effective
// permission set whoami reflected: a tenant-wide membership fact that cannot express a
// workspace-scoped grant, a per-row local bit, or an authored policy that forbids what
// the role tier grants. This module asks the engine the exact question instead, and it
// never widens: an observation enables one decision and expires.
//
// FOUR PROPERTIES CARRY THE WHOLE THING, and each exists because its absence is a real
// defect and not a style preference:
//
//   1. A GENERATED TYPE IS NOT A RUNTIME CHECK. `authApi.capabilities` returns
//      `CapabilityResults` because the OpenAPI generator says the route does; that is an
//      assertion about already-parsed JSON, and it rejects nothing. `admitCapabilityResult`
//      below reads the ACTUAL value: envelope version, exactly one result, the requested
//      id and kind, the exact state/code pair, and a finite integer budget in (0, 30000].
//      Anything else yields no permit. A strict compile-time union does not make those
//      checks unnecessary — it is precisely what makes them look unnecessary.
//
//   2. THE CLOCK STARTS BEFORE THE TRANSPORT, NOT AT RECEIPT. `started` is taken
//      immediately before `authApi.capabilities` is invoked, so the shared client's
//      proactive credential refresh and its single 401 refresh/replay are INSIDE the
//      measured window. The deadline is `started + refresh_after_ms`, never
//      `receipt + budget` and never anything parsed from `observed_at`. A late answer
//      therefore cannot buy itself a fresh window by arriving late — which is exactly
//      what a retry that restarts the clock would do, and why `retry: false` is not a
//      performance choice here.
//
//   3. AN OBSERVATION IS BOUND TO A CONTEXT LIFETIME, NOT ONLY TO CONTEXT VALUES.
//      ⛔ THIS PARAGRAPH USED TO DESCRIBE A PROPERTY THE CODE DID NOT HAVE. It said the
//      partition was bound to a lifetime, and everything underneath compared VALUES —
//      principal kind and actor, tenant, credential generation and workspace, captured at
//      construction and compared with the live stores after every await. A comparison of
//      endpoints cannot see a round trip: workspace A → B → A leaves every value equal,
//      and when both writes land in one React batch there is no commit in between for the
//      removal effect or `gcTime: 0` to act on. Measured on the previous version: an
//      in-flight answer for the first A was admitted for the second, a saved permit went
//      false and then true again, and the composed dispatch guard let one PATCH through.
//
//      The lifetime is now an actual thing: a monotonic local counter, bumped from a
//      synchronous subscription to each of the four sources, carried in the context, and
//      therefore part of the query key, the cache partition and every permit. A round trip
//      advances it twice, so nothing captured before it can equal anything after it, and a
//      permit that has once been found outside its window or its context LATCHES dead —
//      asking again cannot revive it. The value comparison stays exactly as it was; it is
//      no longer the only thing standing between a spent permit and a mutation.
//
//   4. NOTHING HERE IS A CREDENTIAL. The cache key and the permit carry principal KIND
//      and ACTOR (the server's own non-secret, kind-qualified handle), tenant, generation,
//      workspace and the normalized question. No bearer token, no session id, no
//      `observed_at`, no display name, no role, no membership.
//
// The engine re-authorizes every real act. A permit is a short-lived client observation,
// so a revocation between this preflight and the server's own handling is still refused
// where it must be — at the handler.
import {
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import {
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useSyncExternalStore,
} from 'react'
import { authApi } from '@/lib/api/endpoints'
import type {
  CapabilityQuestion,
  CapabilityQuestionsRequest,
} from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'

/**
 * The maximum a single attempt may take, and the maximum positive budget the engine's
 * own contract allows (`refresh_after_ms` is capped at 30000). An answer that arrives
 * later than this cannot yield a usable positive even if it says `allowed`, because the
 * window it names would already be spent — so the attempt is abandoned at the bound
 * rather than waited out. Ratified for this slice.
 */
export const CAPABILITY_REQUEST_MAX_MS = 30_000

/**
 * The single visible retry cadence. EVERY completed non-positive — an established
 * denial, a concealed non-verdict, a target-free input rejection, a local stale and a
 * transport failure alike — refreshes on this one interval while the decision is on
 * screen. That uniformity IS the non-disclosure property at this layer: a cause-specific
 * backoff would let an observer read the concealed cause off the retry rhythm.
 */
export const CAPABILITY_RETRY_MS = 5_000

/** The one question id of this one-question-at-a-time slice. It is a fixed correlator,
 *  not a nonce: a random id would enter no cache key and would only make the response's
 *  id assertion untestable. The engine must echo it exactly. */
const QUESTION_ID = 'q'

const SCHEMA_VERSION = 2

/* ── the question, normalized ─────────────────────────────────────────────────── */

/** A selector pair. Sorted PAIRS, never a joined string: `a|b` and `a` + `|b` are
 *  different questions, and a delimiter inside an id must not be able to make them
 *  collide in a cache key. */
export type SelectorPair = readonly [name: string, value: string]

/**
 * One question in the exact shape this console asks it. `operation` is a MOUNTED route
 * pattern (`GET /v1/m/sessions/channels/{id}/grants`), never a resolved URL: the engine
 * matches its own route table, and substituting the id into the string would ask about a
 * route that does not exist.
 */
export interface NormalizedCapabilityQuestion {
  readonly kind: 'surface' | 'operation'
  readonly operation: string
  readonly workspaceId: string | null
  readonly path: readonly SelectorPair[]
  readonly body: readonly SelectorPair[]
}

/** ONE frozen pair. The pair itself and not only the list around it: a caller that
 *  holds `question.body[0]` can rewrite `[name, value]` in place, and the question a
 *  permit claims to answer would stop being the question that was asked — with the
 *  outer `Object.freeze` still reporting true. */
function pair(name: string, value: string): SelectorPair {
  return Object.freeze([name, value]) as SelectorPair
}

function sortedPairs(
  selectors: Readonly<Record<string, string>> | undefined,
): readonly SelectorPair[] {
  if (!selectors) return []
  return Object.entries(selectors)
    .map(([name, value]) => pair(name, value))
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
}

/** Deep-freeze a question that may not have come from `capabilityQuestion`. Cheap and
 *  idempotent, and it is what makes "the permit's question is immutable" TRUE at
 *  runtime rather than only for callers who type-check. */
function frozenQuestion(
  question: NormalizedCapabilityQuestion,
): NormalizedCapabilityQuestion {
  for (const p of question.path) Object.freeze(p)
  for (const p of question.body) Object.freeze(p)
  Object.freeze(question.path)
  Object.freeze(question.body)
  return Object.freeze(question)
}

/** Build a normalized question. Frozen: a question is compared, keyed and asserted
 *  against a response, and a caller that could mutate one after the fact could make a
 *  permit describe a question that was never asked. */
export function capabilityQuestion(input: {
  kind: 'surface' | 'operation'
  operation: string
  workspaceId?: string | null
  path?: Readonly<Record<string, string>>
  body?: Readonly<Record<string, string>>
}): NormalizedCapabilityQuestion {
  return Object.freeze({
    kind: input.kind,
    operation: input.operation,
    workspaceId: input.workspaceId ?? null,
    path: Object.freeze(sortedPairs(input.path)),
    body: Object.freeze(sortedPairs(input.body)),
  })
}

/** True when two normalized questions are the SAME question — used by a permit to refuse
 *  being spent on a sibling. Selector families are compared pair by pair. */
export function sameQuestion(
  a: NormalizedCapabilityQuestion,
  b: NormalizedCapabilityQuestion,
): boolean {
  const pairsEqual = (
    x: readonly SelectorPair[],
    y: readonly SelectorPair[],
  ): boolean =>
    x.length === y.length &&
    x.every((p, i) => p[0] === y[i][0] && p[1] === y[i][1])
  return (
    a.kind === b.kind &&
    a.operation === b.operation &&
    a.workspaceId === b.workspaceId &&
    pairsEqual(a.path, b.path) &&
    pairsEqual(a.body, b.body)
  )
}

/** The wire question, from the normalized one. Absent selector families are OMITTED
 *  rather than sent empty: a collection that receives an entity locator is answered
 *  `inputs_required`, and an empty object is still an object. */
export function wireQuestion(
  question: NormalizedCapabilityQuestion,
): CapabilityQuestion {
  const selectors: {
    path?: Record<string, string>
    body?: Record<string, string>
  } = {}
  if (question.path.length > 0)
    selectors.path = Object.fromEntries(question.path)
  if (question.body.length > 0)
    selectors.body = Object.fromEntries(question.body)
  const wire: CapabilityQuestion = {
    id: QUESTION_ID,
    kind: question.kind,
    operation: question.operation,
  }
  if (question.workspaceId !== null) wire.workspace_id = question.workspaceId
  if (selectors.path || selectors.body) wire.selectors = selectors
  return wire
}

/** The closed request body for exactly one question. */
export function capabilityRequestBody(
  question: NormalizedCapabilityQuestion,
): CapabilityQuestionsRequest {
  return { schema_version: SCHEMA_VERSION, questions: [wireQuestion(question)] }
}

/* ── the local context LIFETIME ───────────────────────────────────────────────── */

/**
 * ⛔ VALUES CANNOT SEE A ROUND TRIP, AND A ROUND TRIP IS THE WHOLE ATTACK.
 *
 * Every check in this module used to compare the CURRENT values of the four authority
 * sources against the ones captured at construction. That is blind to workspace
 * A → B → A, tenant X → Y → X and principal P → Q → P, because at both ends of the
 * movement the values are equal — and if the two writes land in one React batch, no
 * commit happens in between for anything to notice. Measured on this source: an
 * in-flight answer for the first A was admitted for the second A, a saved permit went
 * false and then true again, and the composed dispatch guard let ONE `PATCH` through.
 *
 * The fix is not a better comparison; it is observing the MOVEMENTS rather than
 * sampling the values. Synchronous subscriptions advance a monotonic lifetime for
 * each client, so a round trip advances it TWICE. Nothing captured before it can
 * have the same lifetime afterwards. The counter travels in the
 * context, so it binds the query key, the cache partition and every saved permit at
 * once. Store movements affect every client; principal movements affect only their
 * own client. Both counters only advance, and their sum cannot come back.
 *
 * WHAT IT IS NOT: it is not per-principal ownership and it holds no authority state —
 * it is a movement clock. It is not a credential: an integer counting local transitions
 * of this page load discloses nothing, which is why it may enter a query key. And
 * nothing here writes during render — only the store and cache subscriptions bump it.
 */
let contextLifetime = 0
const lifetimeListeners = new Set<() => void>()

function bumpLifetime(): void {
  contextLifetime += 1
  for (const listener of lifetimeListeners) listener()
}

function subscribeContextLifetime(listener: () => void): () => void {
  lifetimeListeners.add(listener)
  return () => {
    lifetimeListeners.delete(listener)
  }
}

/**
 * Watch ONE field of one module-singleton store. The comparison happens INSIDE the
 * notification, so every transition is seen individually: A → B → A is two movements
 * here and a no-op to any observer that only compares the endpoints. The subscription
 * is installed once, at module scope, and its lifetime is the store's own — there is no
 * component to unmount and therefore nothing to leak or to clean up.
 */
function watchContextField<S, V>(
  store: {
    getState: () => S
    subscribe: (listener: (state: S) => void) => () => void
  },
  read: (state: S) => V,
): void {
  let last = read(store.getState())
  store.subscribe((state) => {
    const now = read(state)
    if (now === last) return
    last = now
    bumpLifetime()
  })
}

watchContextField(useTenantStore, (s) => s.activeTenant)
watchContextField(useSessionStore, (s) => s.credentialGeneration)
watchContextField(useWorkspaceStore, (s) => s.activeWorkspace)

/**
 * The principal does not live in a store: it is the `whoami` entry of the query cache,
 * so it is watched per client. The observer is installed once per `QueryClient` and held
 * by that client's own cache, so it dies exactly when the client does — a `WeakMap` key,
 * not a registry anyone has to remember to clear.
 *
 * The compared value is the kind-qualified actor, which is what the context already
 * carries and is not a secret. Comparing INSIDE the event is again what matters: a
 * whoami refetch that returns the same principal is not a movement and must not
 * invalidate anything, while P → Q → P inside one batch is two.
 */
interface PrincipalWatch {
  lifetime: number
  readonly listeners: Set<() => void>
}

const principalWatched = new WeakMap<QueryClient, PrincipalWatch>()

function watchPrincipal(queryClient: QueryClient): PrincipalWatch {
  const existing = principalWatched.get(queryClient)
  if (existing) return existing
  const watch: PrincipalWatch = { lifetime: 0, listeners: new Set() }
  const read = (): string | null => {
    const principal = queryClient.getQueryData<Whoami>(queryKeys.whoami) ?? null
    return principal ? `${principal.kind}\u0000${principal.actor}` : null
  }
  let last = read()
  queryClient.getQueryCache().subscribe(() => {
    const now = read()
    if (now === last) return
    last = now
    watch.lifetime += 1
    for (const listener of watch.listeners) listener()
  })
  principalWatched.set(queryClient, watch)
  return watch
}

/** Pure snapshot: null means the client has never observed principal movements. */
function readCapabilityLifetime(queryClient: QueryClient): number | null {
  const watch = principalWatched.get(queryClient)
  return watch ? contextLifetime + watch.lifetime : null
}

/**
 * Install the client's observer during subscription, never during render. The first
 * render of a fresh client has no authority; useSyncExternalStore rechecks the snapshot
 * after subscribing and renders the first context only once the observer exists.
 * This covers the first suspended request as well as later movements, without asking
 * any consumer to prime the client. A discarded render installs nothing.
 *
 * React listeners are removed on unmount. The cache's own observer remains for the
 * client's lifetime: saved permits must also see movements between mounts. Neither
 * the WeakMap nor an unsubscribed React listener keeps that client alive.
 */
function useCapabilityLifetime(queryClient: QueryClient): number | null {
  const subscribe = useMemo(
    () => (listener: () => void) => {
      const watch = watchPrincipal(queryClient)
      watch.listeners.add(listener)
      const unsubscribe = subscribeContextLifetime(listener)
      return () => {
        watch.listeners.delete(listener)
        unsubscribe()
      }
    },
    [queryClient],
  )
  const snapshot = useMemo(
    () => () => readCapabilityLifetime(queryClient),
    [queryClient],
  )
  return useSyncExternalStore(subscribe, snapshot, () => null)
}

/* ── the context an observation belongs to ────────────────────────────────────── */

/**
 * The identity and lifetime an observation is bound to. `kind` AND `actor` both
 * participate: `user_id` alone does not distinguish every authenticated principal (an API
 * token principal carries the id of the user that minted it), and answering a token's
 * question out of a user's cache entry would be an authority confusion, not a cache miss.
 *
 * There is no token, no session id and nothing secret in this object by construction.
 */
export interface CapabilityContext {
  readonly principalKind: string
  readonly actor: string
  readonly tenant: string | null
  readonly credentialGeneration: number
  readonly workspace: string | null
  /**
   * The local movement counter at the moment this context was taken. Two contexts with
   * equal values but different lifetimes are DIFFERENT authorities: the second one is
   * what is live after a round trip the values cannot show. It is a local integer, never
   * a credential, and it is why an invalidation here cannot be undone.
   */
  readonly lifetime: number
}

/** Imperative context capture: install observation before taking authority. React
 * consumers use the pure reader below, after their commit-time subscription is ready. */
export function liveCapabilityContext(
  queryClient: QueryClient,
): CapabilityContext | null {
  watchPrincipal(queryClient)
  return readCapabilityContext(queryClient)
}

/** No observer or no principal means no authority. This reader has no side effects. */
function readCapabilityContext(
  queryClient: QueryClient,
): CapabilityContext | null {
  const lifetime = readCapabilityLifetime(queryClient)
  if (lifetime === null) return null
  const principal = queryClient.getQueryData<Whoami>(queryKeys.whoami) ?? null
  if (!principal) return null
  return {
    principalKind: principal.kind,
    actor: principal.actor,
    tenant: useTenantStore.getState().activeTenant,
    credentialGeneration: useSessionStore.getState().credentialGeneration,
    workspace: useWorkspaceStore.getState().activeWorkspace,
    lifetime,
  }
}

export function sameContext(
  a: CapabilityContext | null,
  b: CapabilityContext | null,
): boolean {
  if (!a || !b) return false
  return (
    a.principalKind === b.principalKind &&
    a.actor === b.actor &&
    a.tenant === b.tenant &&
    a.credentialGeneration === b.credentialGeneration &&
    a.workspace === b.workspace &&
    a.lifetime === b.lifetime
  )
}

/* ── the cache identity ───────────────────────────────────────────────────────── */

/** The prefix every observation of ONE context lives under: what a successful mutation
 *  invalidates, and what a context move cancels and removes. */
export function capabilityScopeKey(
  context: CapabilityContext,
): readonly unknown[] {
  return [
    'auth-capabilities',
    SCHEMA_VERSION,
    context.principalKind,
    context.actor,
    context.tenant,
    context.credentialGeneration,
    context.workspace ?? '-',
    // ⛔ THE PARTITION IS PER LIFETIME, not per value tuple. Without this, the second A
    //    of an A → B → A shares a cache entry with the first and inherits its answer.
    context.lifetime,
  ]
}

/**
 * The exact structural key of ONE observation. Ratified shape: context, then question
 * kind, operation and the two selector families as SORTED PAIRS. It carries no bearer
 * token, session id, `observed_at`, random question id, display name, membership or role.
 */
export function capabilityKey(
  context: CapabilityContext,
  question: NormalizedCapabilityQuestion,
): readonly unknown[] {
  return [
    ...capabilityScopeKey(context),
    question.kind,
    question.operation,
    ['path', ...question.path],
    ['body', ...question.body],
  ]
}

/* ── admitting an actual HTTP body ────────────────────────────────────────────── */

/**
 * What this console concluded. The server's vocabulary is preserved where it exists and
 * never collapsed: `undisclosed` is not `denied`, and neither is `unknown`.
 *
 *  · `checking`        — in flight, nothing observed yet.
 *  · `allowed`         — an operation positive with a live budget. THE ONLY permit.
 *  · `reachable`       — a surface positive with a live budget. Admission, not row authority.
 *  · `denied`          — an established, non-concealing operation denial.
 *  · `not_reachable`   — an established surface refusal.
 *  · `undisclosed`     — the registered concealment. Asserts NO denial, absence or outage.
 *  · `step_up_required`— a target-free assurance rejection, decided before any lookup.
 *  · `unknown`         — the CLIENT's own verdict: expired, moved, malformed, unreadable
 *                        or unreachable. Never converted to a refusal.
 */
export type CapabilityAccess =
  | 'checking'
  | 'allowed'
  | 'reachable'
  | 'denied'
  | 'not_reachable'
  | 'undisclosed'
  | 'step_up_required'
  | 'unknown'

/** True only for the two states that may enable anything. */
export function isPositive(access: CapabilityAccess): boolean {
  return access === 'allowed' || access === 'reachable'
}

/**
 * True only when the ENGINE established the refusal and said so. This is the one thing a
 * surface may state as a fact about the operator's authority.
 *
 * ⛔ EVERY OTHER NON-POSITIVE IS A DIFFERENT SENTENCE, and flattening them is how a
 *    console starts diagnosing causes it was never told. `undisclosed` asserts no denial
 *    at all; `unknown` is this client's own verdict on an expiry, a movement, a malformed
 *    body or a transport that did not answer; `step_up_required` is a gate decided before
 *    any lookup; `checking` has not asked yet. "The permission behind this was lost" is
 *    true for exactly one of them.
 */
export function isEstablishedRefusal(access: CapabilityAccess): boolean {
  return access === 'denied' || access === 'not_reachable'
}

/**
 * The EXACT positive pair per kind. Two vocabularies that share no value, so a surface
 * answer can never be presented as an operation permit or the reverse — the pair is
 * checked, not just the state.
 */
const POSITIVE_PAIR: Record<'surface' | 'operation', [string, string]> = {
  operation: ['allowed', 'authorized'],
  surface: ['reachable', 'admitted'],
}

/** The established refusal pair per kind. Anything else that is not positive and not a
 *  recognised concealment or gate falls to `unknown`, never to a refusal. */
const REFUSAL_PAIR: Record<'surface' | 'operation', [string, string]> = {
  operation: ['denied', 'not_permitted'],
  surface: ['not_reachable', 'not_permitted'],
}

function isFiniteInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value)
}

/**
 * Read the ACTUAL parsed JSON of one capability response and decide what may be believed.
 * Pure, so the whole admission rule is testable without a transport, a store or a clock.
 *
 * `started` and `now` are `performance.now()` readings on the SAME monotonic timeline.
 * A non-finite clock or a clock that ran backwards is a local `unknown`: a positive whose
 * freshness cannot be established is not a positive.
 */
export function admitCapabilityResult(
  body: unknown,
  question: NormalizedCapabilityQuestion,
  started: number,
  now: number,
): { access: CapabilityAccess; deadline: number | null } {
  const nothing = { access: 'unknown' as CapabilityAccess, deadline: null }
  if (typeof body !== 'object' || body === null) return nothing
  const envelope = body as { schema_version?: unknown; results?: unknown }
  if (envelope.schema_version !== SCHEMA_VERSION) return nothing
  if (!Array.isArray(envelope.results) || envelope.results.length !== 1)
    return nothing
  const result = envelope.results[0] as unknown
  if (typeof result !== 'object' || result === null) return nothing
  const answer = result as {
    id?: unknown
    kind?: unknown
    state?: unknown
    code?: unknown
    refresh_after_ms?: unknown
  }
  // The correlator and the kind are asserted, not assumed. A batch of one cannot be
  // mis-correlated by accident — but a response that answers a DIFFERENT kind than the
  // one asked would otherwise be read in the wrong vocabulary, and `reachable` would
  // enable an operation.
  if (answer.id !== QUESTION_ID || answer.kind !== question.kind) return nothing

  const [positiveState, positiveCode] = POSITIVE_PAIR[question.kind]
  if (answer.state === positiveState && answer.code === positiveCode) {
    // A positive with no budget is the contract's own instruction to disbelieve it, so
    // this is a rejection of the POSITIVE and not a tolerated omission.
    if (!isFiniteInteger(answer.refresh_after_ms)) return nothing
    const budget = answer.refresh_after_ms
    if (budget <= 0 || budget > CAPABILITY_REQUEST_MAX_MS) return nothing
    if (!Number.isFinite(started) || !Number.isFinite(now)) return nothing
    if (now < started) return nothing
    const deadline = started + budget
    if (now >= deadline) return nothing
    return {
      access: question.kind === 'operation' ? 'allowed' : 'reachable',
      deadline,
    }
  }
  // A positive STATE with the wrong code, or the right code under another state, is not a
  // positive. It is not a refusal either: a pair this console does not recognise is
  // exactly the case where guessing would be worst.
  if (answer.state === positiveState || answer.code === positiveCode)
    return nothing

  const [refusalState, refusalCode] = REFUSAL_PAIR[question.kind]
  if (answer.state === refusalState && answer.code === refusalCode)
    return {
      access: question.kind === 'operation' ? 'denied' : 'not_reachable',
      deadline: null,
    }
  if (answer.state === 'undisclosed' && answer.code === 'not_disclosed')
    return { access: 'undisclosed', deadline: null }
  if (answer.state === 'unknown' && answer.code === 'step_up_required')
    return { access: 'step_up_required', deadline: null }
  return nothing
}

/* ── the permit ───────────────────────────────────────────────────────────────── */

/** A local, typed refusal: the permit an act was preflighted under is no longer current,
 *  so NOTHING was sent. There is no status, request id or server message because there
 *  was no request. */
export class CapabilityLostError extends Error {
  constructor(reason: 'expired' | 'context') {
    super(
      `capability permit is no longer current (${reason}); nothing was sent`,
    )
    this.name = 'CapabilityLostError'
  }
}

/**
 * An IMMUTABLE positive observation: the exact context it was taken in, the exact
 * question it answers, and the monotonic deadline it dies at. It grants nothing by
 * itself — it is the client's evidence that, at `deadline - budget`, the engine said yes.
 */
export interface CapabilityPermit {
  readonly context: CapabilityContext
  readonly question: NormalizedCapabilityQuestion
  /** `started + refresh_after_ms` on the `performance.now()` timeline. */
  readonly deadline: number
  /** Still inside its window AND still the same live context. */
  isCurrent(): boolean
  /** `isCurrent()` or throw `CapabilityLostError`. */
  assertCurrent(): void
}

export function createCapabilityPermit(
  context: CapabilityContext,
  question: NormalizedCapabilityQuestion,
  deadline: number,
  live: () => CapabilityContext | null,
): CapabilityPermit {
  // ⛔ THE PERMIT OWNS ITS OWN COPY. Freezing the permit froze the CONTAINER: the caller
  //    kept a live reference to the context object and to the question's selector pairs,
  //    and rewriting `permit.context.workspace` or `question.body[0][1]` changed what
  //    this permit claims to be about while `Object.isFrozen(permit)` still said true.
  //    A copy taken here is the only version that answers `isCurrent()`.
  const bound: CapabilityContext = Object.freeze({ ...context })
  const asked = frozenQuestion(question)

  const expired = (): boolean => {
    const now = performance.now()
    return !Number.isFinite(now) || now >= deadline
  }

  // ⛔ INVALIDATION IS A ONE-WAY DOOR. A permit that has once been observed outside its
  //    window or outside its context is dead for good, and asking again cannot revive
  //    it. Without this latch the answer is a fresh comparison every time, so an
  //    authority that leaves and comes back — A → B → A, a logout and login with the
  //    same values, a credential that rotates back — brings a spent permit back to life.
  //    The permit is the thing a mutation is dispatched under, so "came back" would mean
  //    bytes sent under an authority that had already been withdrawn.
  //
  //    The latched REASON is kept, not just the fact: a caller that learns "expired"
  //    where the context had moved would be told the wrong thing about its own act.
  let lost: 'expired' | 'context' | null = null
  const check = (): 'expired' | 'context' | null => {
    if (lost) return lost
    if (expired()) lost = 'expired'
    else if (!sameContext(live(), bound)) lost = 'context'
    return lost
  }

  const permit: CapabilityPermit = {
    context: bound,
    question: asked,
    deadline,
    isCurrent: () => check() === null,
    assertCurrent: () => {
      const reason = check()
      if (reason) throw new CapabilityLostError(reason)
    },
  }
  return Object.freeze(permit)
}

/* ── one actual observation ───────────────────────────────────────────────────── */

export interface CapabilityObservationData {
  readonly access: CapabilityAccess
  readonly deadline: number | null
  readonly context: CapabilityContext
  readonly question: NormalizedCapabilityQuestion
}

/** What an aborted caller must be told. `signal.reason` is what the DOM puts there and
 *  what a caller's own `abort(reason)` chose; the fallback covers an implementation
 *  without it. It is a THROW and not an observation: a cancellation is not an answer,
 *  and turning it into `unknown` would put it on the retry cadence. */
function cancellation(signal: AbortSignal | undefined): unknown {
  const reason: unknown = signal?.reason
  if (reason !== undefined) return reason
  return new DOMException(
    'the capability observation was cancelled by its caller',
    'AbortError',
  )
}

/**
 * Ask ONE question over the real transport and decide what may be believed.
 *
 * `caller` is the signal of whoever wants the answer (a query's cancellation, or a
 * confirmation's own controller). It is composed with this module's attempt bound into a
 * single controller so the bound applies to the WHOLE attempt — including the shared
 * client's proactive refresh and its 401 replay, which is the point of taking `started`
 * before the call rather than around the fetch.
 */
export async function observeCapability(options: {
  question: NormalizedCapabilityQuestion
  context: CapabilityContext
  live: () => CapabilityContext | null
  caller?: AbortSignal
  dispatchGuard?: () => void
}): Promise<CapabilityObservationData> {
  const { question, context, live, caller, dispatchGuard } = options
  // ⛔ AN ALREADY-CANCELLED CALLER SENDS NOTHING AND LEARNS NOTHING. The abort listener
  //    below only fires on a FUTURE abort, so a signal that was already aborted used to
  //    produce a composed controller that was never aborted: the request went out under
  //    a cancelled intention and came back with a positive the caller could publish.
  //    Checked first, before any controller, listener, timer or byte.
  if (caller?.aborted) throw cancellation(caller)

  const attempt = new AbortController()
  // ⛔ A GUARD'S REFUSAL IS NOT A TRANSPORT FAILURE, AND MUST NOT BECOME AN OBSERVATION.
  //    Everything below turns a throw into a local `unknown` on the visible cadence, which
  //    is right for a network that is down and WRONG for the caller's own dispatch guard:
  //    that throw is a typed local refusal the surface must see, and swallowing it would
  //    turn "the surface moved, nothing was sent" into "we could not establish an answer",
  //    which then simply retries. The refused value is remembered by IDENTITY rather than
  //    recognised by class, so this cannot be fooled by a transport error that happens to
  //    look like a refusal, and it does not need to know the caller's error types.
  let refused: unknown
  let refusedAt = false
  const guarded = dispatchGuard
    ? () => {
        try {
          dispatchGuard()
        } catch (cause) {
          refused = cause
          refusedAt = true
          throw cause
        }
      }
    : undefined
  const unknown = (): CapabilityObservationData => ({
    access: 'unknown',
    deadline: null,
    context,
    question,
  })

  // ⛔ ABORTING A SIGNAL IS NOT ENDING AN ATTEMPT, and that gap was the whole defect.
  //    `attempt.abort()` reaches the fetch and NOTHING ELSE: the shared client awaits a
  //    proactive credential refresh BEFORE it builds a request, and that await does not
  //    observe any signal. With a refresh that never answers — a stalled renewal, a
  //    hung network — the promise below simply never settled, so the bound expired, the
  //    controller aborted, and the caller waited anyway. Measured: still pending at
  //    30.001 ms with zero capability fetches made.
  //
  //    So the attempt ends on a promise of its OWN, raced against the transport. The
  //    abort still fires — it is what stops the bytes — but it is no longer what the
  //    settlement depends on. The shared refresh is left alone: it belongs to every
  //    other consumer of the client, and cancelling it here would break requests this
  //    module never made.
  type Ending = { kind: 'bound' } | { kind: 'cancelled' }
  let end!: (ending: Ending) => void
  const ended = new Promise<Ending>((resolve) => {
    end = resolve
  })
  const bind = () => {
    attempt.abort()
    end({ kind: 'bound' })
  }
  const cancel = () => {
    attempt.abort()
    end({ kind: 'cancelled' })
  }

  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    // ⛔ HERE, AND NOWHERE LATER. Everything the shared client may do before bytes leave
    //    — reading the token, awaiting a proactive credential refresh, composing headers,
    //    running the dispatch guard — and everything it may do after a 401 — refreshing
    //    again and replaying — is inside this measurement.
    const started = performance.now()
    timer = setTimeout(bind, CAPABILITY_REQUEST_MAX_MS)
    caller?.addEventListener('abort', cancel)
    // ⛔ SETTLED, NOT REJECTED. Turning the transport into a promise that always
    //    FULFILLS is what makes a late answer harmless: whichever way it ends after the
    //    bound has passed, its handlers are already attached, so a late rejection is
    //    handled instead of becoming an unhandled rejection, and a late fulfillment is a
    //    value nobody reads. Neither can publish authority — the race is already over.
    const transport = authApi
      .capabilities(capabilityRequestBody(question), {
        tenant: context.tenant,
        signal: attempt.signal,
        dispatchGuard: guarded,
      })
      .then(
        (value: unknown) => ({ kind: 'answered' as const, value }),
        (cause: unknown) => ({ kind: 'failed' as const, cause }),
      )

    const outcome = await Promise.race([transport, ended])

    // The bound: a local unknown on the ordinary visible cadence, exactly like a
    // transport failure. It is never a denial and never diagnosed to the operator.
    if (outcome.kind === 'bound') return unknown()
    // The CALLER cancelled and the transport never came back: still a cancellation, and
    // the query layer must see one rather than a manufactured "unknown" it would retry.
    if (outcome.kind === 'cancelled') throw cancellation(caller)
    if (outcome.kind === 'failed') {
      const cause = outcome.cause
      // The caller's own dispatch guard refused: propagate it unchanged.
      if (refusedAt && cause === refused) throw cause
      // The CALLER cancelled: that is not an observation.
      if (caller?.aborted) throw cause
      // Anything else — transport failure, refusal, our own attempt bound — is a local
      // unknown on the ordinary visible cadence.
      return unknown()
    }
    // Cancellation is retained THROUGH the answer: bytes that were already on the way
    // back do not become authority for an intention that has been withdrawn.
    if (caller?.aborted) throw cancellation(caller)
    // The context moved while this was in flight: the answer is about an authority that
    // is no longer the live one, so it is discarded rather than cached.
    if (!sameContext(live(), context)) return unknown()
    const { access, deadline } = admitCapabilityResult(
      outcome.value,
      question,
      started,
      performance.now(),
    )
    return { access, deadline, context, question }
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    caller?.removeEventListener('abort', cancel)
  }
}

/* ── the hook ─────────────────────────────────────────────────────────────────── */

export interface CapabilityObservation {
  /** What may be believed RIGHT NOW — expiry is applied here, not at refetch time. */
  readonly access: CapabilityAccess
  /** The permit, only while `access` is a live positive. */
  readonly permit: CapabilityPermit | null
  readonly question: NormalizedCapabilityQuestion | null
}

const UNASKED: CapabilityObservation = {
  access: 'unknown',
  permit: null,
  question: null,
}

/**
 * Observe one capability question for the current principal, tenant, credential
 * generation and workspace.
 *
 * `question === null` means "there is nothing to ask yet" (no workspace selected, no
 * channel chosen). It is NOT a permission: the result is `unknown`, which enables nothing.
 */
export function useCapability(
  question: NormalizedCapabilityQuestion | null,
): CapabilityObservation {
  const queryClient = useQueryClient()
  // Subscribe to the auth provider and stores for rendered values. The live check
  // reads their QueryClient/store sources again before admitting or using a positive.
  const { principal } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  // ⛔ AND THE MOVEMENT COUNTER IS SUBSCRIBED, NOT SAMPLED, or the correction would be a
  //    deadlock instead of a fix. After a batched A → B → A the four values above are
  //    unchanged, so nothing re-renders this consumer: it would keep a context whose
  //    lifetime can never equal the live one again, hold a query key nobody refreshes,
  //    and answer `unknown` for the rest of the page's life. Subscribing makes the round
  //    trip a state change like any other — the old partition is dropped and a NEW
  //    observation starts, which is what the operator gets after any real movement.
  const lifetime = useCapabilityLifetime(queryClient)

  // ⛔ MEMOISED ON PRIMITIVES, NEVER ON THE PRINCIPAL OBJECT. `principal` is an object,
  //    and an auth context that rebuilds it on every render — which a mocked one does, and
  //    which a future refactor could reintroduce in production — would make this identity
  //    change every render. Everything downstream keys off it, so that is not a wasted
  //    memo: it is a render loop, and it was one. The five fields below are the whole
  //    context, and comparing them by VALUE is what the contract asks for anyway.
  const principalKind = principal?.kind ?? null
  const actor = principal?.actor ?? null
  const context = useMemo<CapabilityContext | null>(
    () =>
      lifetime !== null && principalKind !== null && actor !== null
        ? {
            principalKind,
            actor,
            tenant,
            credentialGeneration,
            workspace,
            lifetime,
          }
        : null,
    [principalKind, actor, tenant, credentialGeneration, workspace, lifetime],
  )
  const live = useMemo(
    () => () => readCapabilityContext(queryClient),
    [queryClient],
  )

  const enabled = context !== null && question !== null
  const key = useMemo(
    () =>
      context && question
        ? capabilityKey(context, question)
        : ['auth-capabilities', SCHEMA_VERSION, 'unasked'],
    [context, question],
  )

  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      observeCapability({
        question: question as NormalizedCapabilityQuestion,
        context: context as CapabilityContext,
        live,
        caller: signal,
      }),
    enabled,
    // ⛔ NO TANSTACK RETRY. A retry here would call the query function again with a NEW
    //    `started`, and the budget of the second attempt would be measured from a clock
    //    the first attempt's latency never touched. The bound and the visible cadence
    //    below are this module's own, precisely so the clock is never restarted.
    retry: false,
    // An authorization observation is never served from memory, and nothing of a context
    // survives leaving it: A → B → A finds no entry of A.
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
    refetchInterval: (q) => {
      const data = q.state.data as CapabilityObservationData | undefined
      if (!data) return CAPABILITY_RETRY_MS
      if (isPositive(data.access) && data.deadline !== null) {
        const remaining = data.deadline - performance.now()
        // ⛔ NEVER ZERO. TanStack reads `0` as "no interval" and CLEARS the timer, and
        //    the interval is recomputed on every observer update — including the render
        //    the deadline itself triggers. So a positive whose deadline had already
        //    passed when the next render happened switched the screen to `unknown` and
        //    turned OFF its own recheck in the same breath: measured, one request and
        //    then nothing for fifteen seconds, while the text promised a periodic
        //    recheck. An overdue positive is exactly a non-positive, so it takes the one
        //    visible cadence every other non-positive takes.
        return remaining > 0 ? remaining : CAPABILITY_RETRY_MS
      }
      // ONE cadence for every completed non-positive: an established refusal, a concealed
      // non-verdict, a step-up gate, a local unknown. No cause-specific backoff.
      return CAPABILITY_RETRY_MS
    },
  })

  const data = query.data
  // The DEADLINE is local and must bite between refetches, so the render re-evaluates it
  // at the deadline rather than trusting the refetch to have landed by then. Until the
  // replacement observation arrives the access is `unknown` — never the old positive.
  //
  // ⛔ THE COUNTER, NOT THE DISPATCHER, IS WHAT THE DERIVATION BELOW DEPENDS ON. A
  //    `useReducer` dispatcher has a stable identity: listing it as a dependency compiles,
  //    re-renders, and recomputes NOTHING — the old positive would keep being returned
  //    past its deadline until an unrelated render happened to invalidate the memo.
  const [expiryTick, bumpExpiry] = useReducer((n: number) => n + 1, 0)
  useEffect(() => {
    if (!data || !isPositive(data.access) || data.deadline === null) return
    const remaining = data.deadline - performance.now()
    if (remaining <= 0) return
    const t = setTimeout(bumpExpiry, remaining)
    return () => clearTimeout(t)
  }, [data])

  // ⛔ A CONTEXT MOVE cancels and REMOVES the previous partition — on the MOVE, not on
  //    unmount. Removing it on unmount would be wrong twice: several consumers share one
  //    observation, so the first to unmount would drop the answer the others are still
  //    rendering; and the removal would refetch, re-render and remove again. `gcTime: 0`
  //    already collects what nobody observes.
  const previous = useRef<CapabilityContext | null>(null)
  useEffect(() => {
    const prior = previous.current
    if (prior && !sameContext(prior, context)) {
      const stale = capabilityScopeKey(prior)
      void queryClient.cancelQueries({ queryKey: stale })
      queryClient.removeQueries({ queryKey: stale })
    }
    previous.current = context
  }, [context, queryClient])

  return useMemo<CapabilityObservation>(() => {
    if (!enabled || !context || !question) return UNASKED
    if (!data) return { access: 'checking', permit: null, question }
    if (
      !sameContext(data.context, context) ||
      !sameQuestion(data.question, question)
    )
      return { access: 'checking', permit: null, question }
    if (!isPositive(data.access) || data.deadline === null)
      return { access: data.access, permit: null, question }
    const permit = createCapabilityPermit(
      context,
      question,
      data.deadline,
      live,
    )
    if (!permit.isCurrent())
      return { access: 'unknown', permit: null, question }
    return { access: data.access, permit, question }
    // `expiryTick` is not read: its only job is to re-run this derivation at the deadline,
    // which is when `permit.isCurrent()` starts answering false.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, context, question, data, live, expiryTick])
}

/**
 * THE PREFLIGHT. A rendered positive is not enough to send a mutation: it may be seconds
 * old, and the operator confirmed under an authority that could have moved since. This
 * forces a NEW exact observation over the real transport, under the confirmation's own
 * signal and dispatch guard, and returns a permit only for a current positive.
 *
 * It deliberately does not consult the cache: the cache is what the screen was painted
 * from, and re-reading it would make this a formality.
 */
export async function requestCapabilityPermit(options: {
  question: NormalizedCapabilityQuestion
  context: CapabilityContext
  live: () => CapabilityContext | null
  signal?: AbortSignal
  dispatchGuard?: () => void
}): Promise<CapabilityPermit | null> {
  const observation = await observeCapability({
    question: options.question,
    context: options.context,
    live: options.live,
    caller: options.signal,
    dispatchGuard: options.dispatchGuard,
  })
  if (!isPositive(observation.access) || observation.deadline === null)
    return null
  const permit = createCapabilityPermit(
    options.context,
    options.question,
    observation.deadline,
    options.live,
  )
  return permit.isCurrent() ? permit : null
}

/**
 * The preflight, as a surface uses it: the live context reader plus a request bound to it.
 *
 * `context` is null when no principal can be read — deny-closed, so a surface cannot
 * preflight under an authority it could not establish.
 */
export interface CapabilityPreflight {
  readonly context: CapabilityContext | null
  readonly live: () => CapabilityContext | null
  /** A NEW exact observation over the real transport. Null unless it is a current positive. */
  request: (
    question: NormalizedCapabilityQuestion,
    options?: { signal?: AbortSignal; dispatchGuard?: () => void },
  ) => Promise<CapabilityPermit | null>
}

export function useCapabilityPreflight(): CapabilityPreflight {
  const queryClient = useQueryClient()
  // Subscriptions, so a surface re-renders when its authority moves; the VALUE is read
  // from the same place the live check reads. The movement counter is one of them: a
  // round trip that leaves every value equal still has to reach the surface holding an
  // open confirmation, because that confirmation's permit is already dead.
  const { principal } = useAuth()
  const tenant = useTenantStore((s) => s.activeTenant)
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const workspace = useWorkspaceStore((s) => s.activeWorkspace)
  const lifetime = useCapabilityLifetime(queryClient)
  const live = useMemo(
    () => () => readCapabilityContext(queryClient),
    [queryClient],
  )
  return useMemo<CapabilityPreflight>(() => {
    const context = lifetime === null ? null : live()
    return {
      context,
      live,
      request: async (question, options) => {
        // Re-read at CALL time, not at render time: a confirmation may sit open for
        // minutes, and the request must be about the authority that is live NOW.
        const now = live()
        if (!now) return null
        return requestCapabilityPermit({
          question,
          context: now,
          live,
          signal: options?.signal,
          dispatchGuard: options?.dispatchGuard,
        })
      },
    }
    // The subscriptions are the trigger; `live` is the source.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, principal, tenant, credentialGeneration, workspace, lifetime])
}

/**
 * Invalidate every observation of ONE context. Called after a successful administrative
 * mutation: the act may have changed what this principal may do next, and the honest
 * response is to ask again rather than to synthesize a permit out of a 200.
 */
export function invalidateCapabilities(
  queryClient: QueryClient,
  context: CapabilityContext | null,
): void {
  if (!context) return
  void queryClient.invalidateQueries({ queryKey: capabilityScopeKey(context) })
}
