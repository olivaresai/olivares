// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClientContext } from '@tanstack/react-query'
import { useCallback, useContext, useEffect, useMemo, useRef } from 'react'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import {
  CapabilityLostError,
  type CapabilityPermit,
} from '@/lib/auth/capabilities'
import { can as rbacCan } from '@/lib/auth/rbac'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
import type {
  ChannelCreateInput,
  ChannelUpdateInput,
  CursorAdvanceInput,
  GrantCreateInput,
  HandoffOfferInput,
  HandoffRecipient,
  HandoffResponseInput,
  SendNoticeInput,
} from './types'

/**
 * AN INTENTION IS IMMUTABLE, AND IT OWNS ITS KEY.
 *
 * The idempotency key is minted when the operator CONFIRMS what they are about to
 * send, together with the body, the target Channel and the authority boundary the
 * confirmation was made under. Nothing in this module — or anywhere in the feature —
 * changes a field of a built intent: a retry after an ambiguous transport result
 * re-sends the SAME object, so the engine's ledger can answer "already applied"
 * instead of applying twice; and editing is only possible after the previous
 * intention has been explicitly resolved or discarded, which makes "regenerate the
 * key on retry" unwritable rather than merely discouraged.
 */
export interface IntentScope {
  /** Tenant captured beside the key. Every retry stays in this request scope. */
  readonly tenant: string | null
  /** The explicit workspace the operator was acting in. */
  readonly workspace: string
  /** The authority boundary (principal | tenant | credential generation | workspace)
   * the intention was confirmed under. A moved boundary invalidates it. */
  readonly boundary: string
}

/**
 * THE AUTHORITY AN INTENTION WAS CONFIRMED UNDER, read from the LIVE stores at the
 * moment of confirmation and frozen into the intent — not from React props, refs or
 * effects, whose timing is the defect R2 closes. Three non-secret facts:
 *
 *   · the session store's credential generation (a renewal under the SAME session id
 *     advances it; the token itself is never read here);
 *   · the active tenant of the tenant store;
 *   · the active workspace of the workspace store.
 *
 * `assertAuthorityCurrent` compares this with the same stores again, synchronously,
 * immediately before every fetch the transport makes for the intent (see
 * `RequestOptions.dispatchGuard` in lib/api/client.ts). Nothing here depends on a
 * component having re-rendered or an effect having run.
 */
export interface AuthoritySnapshot {
  readonly credentialGeneration: number
  readonly tenant: string | null
  readonly workspace: string | null
}

export function snapshotAuthority(): AuthoritySnapshot {
  return Object.freeze({
    credentialGeneration: useSessionStore.getState().credentialGeneration,
    tenant: useTenantStore.getState().activeTenant,
    workspace: useWorkspaceStore.getState().activeWorkspace,
  })
}

/** Which fact of the authority is no longer the one the intention was confirmed
 * under. Never an id, never a token: only the NAME of the fact that moved. */
export type MovedFact =
  | 'credential'
  | 'tenant'
  | 'workspace'
  | 'principal'
  | 'permission'
  | 'surface'
  /** G1-B: the exact capability permit this act was preflighted under expired, or its
   *  context moved. A separate fact from `permission`, which is the whoami reflection:
   *  the two can disagree, and saying which one closed the act is the whole point. */
  | 'capability'

/** The first fact of `under` that the live stores no longer agree with, or null. */
export function movedSince(under: AuthoritySnapshot): MovedFact | null {
  const live = snapshotAuthority()
  if (live.credentialGeneration !== under.credentialGeneration)
    return 'credential'
  if (live.tenant !== under.tenant) return 'tenant'
  if (live.workspace !== under.workspace) return 'workspace'
  return null
}

/**
 * StaleIntentError — a LOCAL, TYPED cancellation: the intention was confirmed under
 * an authority that is no longer the live one, so it was NOT dispatched. Zero bytes
 * left; there is no request id, no status and no server message, because there was
 * no request. It is an `AuthorityLostError`, so every surface's `onError` already
 * treats it as the calm "nothing was sent" it is, never as a red failure and never
 * as a success.
 */
export class StaleIntentError extends AuthorityLostError {
  readonly moved: MovedFact
  constructor(moved: MovedFact) {
    super()
    this.name = 'StaleIntentError'
    this.message = `stale intent: the ${moved} moved before dispatch; nothing was sent`
    this.moved = moved
  }
}

export function assertAuthorityCurrent(under: AuthoritySnapshot): void {
  const moved = movedSince(under)
  if (moved) throw new StaleIntentError(moved)
}

export interface SendIntent {
  readonly key: string
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly channelId: string
  readonly body: SendNoticeInput
}

export interface AckIntent {
  readonly key: string
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly deliveryId: string
  /** The Delivery version the operator READ; the ETag is derived from it and never
   * from a guess or a previous response. */
  readonly version: number
  readonly etag: string
}

/** `POST /channels` has no idempotency key, so a create intention owns no key: it
 * still owns the body and the authority it was submitted under. */
export interface CreateIntent {
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly body: ChannelCreateInput
}

/**
 * A CANONICAL communication id: UUID version 7, lowercase. The engine accepts any
 * UUID on `POST /messages/send` but the Ack normaliser requires the canonical form
 * (`validCanonicalCommunicationID`: version 7, RFC 4122 variant), and answers 400
 * "idempotency key is invalid" to a v4 — measured on the live engine. One generator
 * serves both routes so a key is canonical wherever it travels: 48 bits of
 * millisecond time, the version nibble, 74 random bits from the platform CSPRNG.
 */
export function newCanonicalId(): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  const ms = BigInt(Date.now())
  for (let i = 0; i < 6; i++) {
    bytes[i] = Number((ms >> BigInt(8 * (5 - i))) & 0xffn)
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function newIdempotencyKey(): string {
  return newCanonicalId()
}

/** The strong ETag a Delivery answers with: `"v<version>"`, verbatim. */
export function deliveryEtag(version: number): string {
  return `"v${version}"`
}

export function buildSendIntent(
  scope: IntentScope,
  channelId: string,
  body: SendNoticeInput,
): SendIntent {
  return Object.freeze({
    key: newIdempotencyKey(),
    scope,
    authority: snapshotAuthority(),
    channelId,
    body: structuredClone(body),
  })
}

export function buildAckIntent(
  scope: IntentScope,
  deliveryId: string,
  version: number,
): AckIntent {
  return Object.freeze({
    key: newIdempotencyKey(),
    scope,
    authority: snapshotAuthority(),
    deliveryId,
    version,
    etag: deliveryEtag(version),
  })
}

export function buildCreateIntent(
  scope: IntentScope,
  body: ChannelCreateInput,
): CreateIntent {
  return Object.freeze({
    scope,
    authority: snapshotAuthority(),
    body: structuredClone(body),
  })
}

/* ── I2: the three administrative acts and the personal cursor ─────────────────── */

/**
 * The three administrative acts have NO idempotency key: the engine binds each of
 * them to the CHANNEL's strong ETag the operator read (`If-Match`), so a lost
 * response is an UNKNOWN outcome the operator resolves by re-reading, never by
 * re-sending. Each intent owns the ETag it was confirmed against, the frozen
 * authority, and — for PATCH — exactly the fields the operator changed.
 */
export interface UpdateChannelIntent {
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly channelId: string
  /** The Channel ETag of the read the form was built from; sent as If-Match. */
  readonly etag: string
  /** `channel_id` plus ONLY the changed fields. */
  readonly body: ChannelUpdateInput
}

export interface GrantIntent {
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly channelId: string
  readonly etag: string
  readonly body: GrantCreateInput
}

export interface RevokeIntent {
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly channelId: string
  /** The exact generation the operator SAW and confirmed, never re-derived. */
  readonly grantId: string
  readonly etag: string
}

/**
 * The personal cursor advance: the token the engine minted for ONE inbox page's
 * `cursor_target`, the exact last Delivery of that page, the CURSOR's ETag (not
 * the Channel's, not the Delivery's), the recipient the path names, and a key of
 * its own. Frozen together at confirmation; a retry after an ambiguous result
 * re-sends exactly this object.
 */
export interface CursorIntent {
  readonly key: string
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  /** `principal.user_id` without a `user:` prefix — the authenticated mailbox. */
  readonly recipient: string
  readonly deliveryId: string
  /** The cursor version the GET reported, and the ETag derived from the GET. */
  readonly version: number
  readonly etag: string
  readonly body: CursorAdvanceInput
}

export function buildUpdateChannelIntent(
  scope: IntentScope,
  channelId: string,
  etag: string,
  body: ChannelUpdateInput,
): UpdateChannelIntent {
  return Object.freeze({
    scope,
    authority: snapshotAuthority(),
    channelId,
    etag,
    body: structuredClone(body),
  })
}

export function buildGrantIntent(
  scope: IntentScope,
  channelId: string,
  etag: string,
  body: GrantCreateInput,
): GrantIntent {
  return Object.freeze({
    scope,
    authority: snapshotAuthority(),
    channelId,
    etag,
    body: structuredClone(body),
  })
}

export function buildRevokeIntent(
  scope: IntentScope,
  channelId: string,
  grantId: string,
  etag: string,
): RevokeIntent {
  return Object.freeze({
    scope,
    authority: snapshotAuthority(),
    channelId,
    grantId,
    etag,
  })
}

export function buildCursorIntent(
  scope: IntentScope,
  recipient: string,
  minted: { cursor: string; version: number; etag: string },
  deliveryId: string,
): CursorIntent {
  return Object.freeze({
    key: newIdempotencyKey(),
    scope,
    authority: snapshotAuthority(),
    recipient,
    deliveryId,
    version: minted.version,
    etag: minted.etag,
    body: Object.freeze({ cursor: minted.cursor, delivery_id: deliveryId }),
  })
}

/* ── I3: the offer and the response ───────────────────────────────────────────── */

/**
 * DEEP freeze, and it is the difference between "the draft was copied" and "the
 * retry cannot change". `structuredClone` already decouples the intent from the
 * object the form held, so an ordinary React edit — which replaces state rather than
 * mutating it — cannot reach the copy. What it does NOT stop is a mutation of a
 * NESTED node of the copy itself, and both I3 bodies are nested: the offer carries
 * `handoff.artifact_refs[]` and `recipient`, the rejection carries
 * `reason.references[]`. `Object.freeze` on the intent is shallow, so those arrays
 * would stay writable and an uncertain operation's SAME-KEY retry could re-send
 * different bytes under the key the engine already ledgered — the one thing an
 * idempotency key exists to make impossible.
 */
function deepFreeze<T>(value: T): T {
  if (value === null || typeof value !== 'object') return value
  for (const nested of Object.values(value as Record<string, unknown>)) {
    deepFreeze(nested)
  }
  return Object.freeze(value)
}

/**
 * The offer: the canonical key, the exact body, and the WorkItem precondition of the
 * FRESH read the confirmation was built on. `expected_owner_epoch` inside the body
 * comes from that SAME read — the pair is the whole point, because an ETag that
 * matches while the owner epoch does not is a different item state than the operator
 * reviewed. The engine answers a code-less 409 to either; the console never guesses
 * which, and never replaces one and re-sends.
 */
export interface HandoffOfferIntent {
  readonly key: string
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly workItemId: string
  /** The strong WorkItem ETag of the fresh read, verbatim from its header. */
  readonly etag: string
  readonly body: HandoffOfferInput
}

/**
 * The response: the HANDOFF's own ETag, from the protected detail. NOT the carrier
 * Delivery's version and NOT an ETag rebuilt from an integer — `deliveryEtag` exists
 * for the Ack route and has no business here.
 *
 * The three identifiers beside the body are what the CONFIRMATION and the retained
 * operation are allowed to keep once the protected detail is closed: the WorkItem
 * reference, the recipient and the carrier Delivery. None of them is protected
 * content; the summary, next action and risk are not copied here and die with the
 * detail read.
 */
export interface HandoffResponseIntent {
  readonly key: string
  readonly scope: IntentScope
  readonly authority: AuthoritySnapshot
  readonly handoffId: string
  readonly etag: string
  readonly workItemId: string
  readonly recipient: HandoffRecipient
  readonly deliveryId: string
  readonly body: HandoffResponseInput
}

export function buildHandoffOfferIntent(
  scope: IntentScope,
  workItemId: string,
  etag: string,
  body: HandoffOfferInput,
): HandoffOfferIntent {
  return Object.freeze({
    key: newIdempotencyKey(),
    scope,
    authority: snapshotAuthority(),
    workItemId,
    etag,
    body: deepFreeze(structuredClone(body)),
  })
}

export function buildHandoffResponseIntent(
  scope: IntentScope,
  target: {
    handoffId: string
    etag: string
    workItemId: string
    recipient: HandoffRecipient
    deliveryId: string
  },
  body: HandoffResponseInput,
): HandoffResponseIntent {
  return Object.freeze({
    key: newIdempotencyKey(),
    scope,
    authority: snapshotAuthority(),
    handoffId: target.handoffId,
    etag: target.etag,
    workItemId: target.workItemId,
    recipient: deepFreeze(structuredClone(target.recipient)),
    deliveryId: target.deliveryId,
    body: deepFreeze(structuredClone(body)),
  })
}

/**
 * The transport's dispatch guard for ONE intent: the synchronous check the shared
 * client runs immediately before EVERY fetch it makes for the request — the first
 * send, after any preventive refresh, and the single 401 replay, after its refresh.
 * It compares the intent's frozen authority with the live stores, then runs the
 * surface's own check (permission, principal, surface still mounted) when given.
 * It throws `StaleIntentError`; the client rethrows it unchanged and sends nothing.
 */
export function dispatchGuardFor(
  intent: { readonly authority: AuthoritySnapshot },
  surface?: () => void,
): () => void {
  return () => {
    assertAuthorityCurrent(intent.authority)
    surface?.()
  }
}

/**
 * G1-B — THE COMPOSED GUARD OF A MIGRATED ADMINISTRATIVE ACT.
 *
 * The surface's own check, then the immutable permit the act was preflighted under. The
 * shared client runs this immediately before the first fetch (after any proactive
 * credential refresh) and again before the single 401 replay, so an expiry or a context
 * move in EITHER gap refuses with zero bytes on the wire.
 *
 * The permit's own refusal is re-thrown as this feature's typed local refusal: every
 * surface's `onError` already treats a `StaleIntentError` as the calm "nothing was sent"
 * it is, and a second error class would have to be taught to each of them. The condition
 * is not softened — only named in this feature's vocabulary.
 */
export function permittedDispatchGuard(
  guard: IntentGuard,
  permit: CapabilityPermit,
): () => void {
  return () => {
    guard.check()
    try {
      permit.assertCurrent()
    } catch (cause) {
      if (cause instanceof CapabilityLostError)
        throw new StaleIntentError('capability')
      throw cause
    }
  }
}

/**
 * useIntentGuard — what a SURFACE knows about whether a dispatch may still go, and
 * the cancellation that reaches requests already in flight.
 *
 * R2 (independent review F1 of d8c5ad3e9e): the first version kept the permission
 * and the boundary in refs that `useEffect` updated, and aborted in effects too. A
 * mutation queued by React Query and resumed after the session store had already
 * rotated — same session id, new credential — found refs that still said the old
 * boundary, was handed a fresh controller, and the client sent it with the NEW
 * credential before React's cleanup ran. One POST left where none should have.
 *
 * Now nothing here depends on an effect having run:
 *
 *   · the authority the surface is mounted under is captured DURING RENDER from the
 *     live stores (`snapshotAuthority`), keyed by the boundary, and `moved()`
 *     compares it with those stores again at call time;
 *   · the principal is read from the whoami query the auth context also reads, and
 *     the named core permission is re-evaluated against that principal and the live
 *     tenant with the same RBAC rule `useAuth().can` uses — deny-closed when the
 *     principal cannot be read;
 *   · `begin()` refuses (null, no controller) when anything moved, when the
 *     permission observed by the surface is gone, or after the surface unmounted —
 *     a controller is never resurrected for a retained callback;
 *   · `check()` is the surface's half of the transport's dispatch guard: it throws
 *     `StaleIntentError` synchronously, immediately before a fetch, under the same
 *     conditions, plus when the dispatch's own signal was already aborted.
 *
 * The effects remain for what only they can do — abort a request that is ALREADY
 * on the wire when the permission or the boundary changes, and on unmount — but
 * they are no longer what decides whether a request may leave. No token is read or
 * copied anywhere in this hook.
 */
export interface IntentGuard {
  /** A fresh signal for one dispatch, or null when the dispatch must not begin. */
  begin: () => AbortSignal | null
  /** Whether a dispatch begun under this guard may still proceed. */
  alive: () => boolean
  /** Cancel whatever this guard has in flight (an explicit discard). */
  end: () => void
  /** The synchronous dispatch guard the transport runs before each fetch. */
  check: () => void
}

export function useIntentGuard({
  allowed,
  boundary,
  permission,
  alsoRequires,
}: {
  allowed: boolean
  boundary: string
  /** The core permission this surface acts under, re-evaluated live at dispatch. */
  permission?: string
  /** A SECOND core permission the offered act depends on; both must hold. */
  alsoRequires?: string
}): IntentGuard {
  const queryClient = useContext(QueryClientContext)
  const livePrincipal = useCallback(
    (): Whoami | null =>
      queryClient?.getQueryData<Whoami>(queryKeys.whoami) ?? null,
    [queryClient],
  )
  // Captured DURING RENDER, for this boundary — not in an effect, not in a ref: the
  // checks below are closures over this render's capture, so a retained closure of
  // an earlier render compares ITS capture with the live stores and refuses.
  const under = useMemo(
    () => ({
      authority: snapshotAuthority(),
      principal: livePrincipal()?.user_id ?? null,
    }),
    // The boundary is the key: a new boundary is a new capture.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [boundary, livePrincipal],
  )
  const controller = useRef<AbortController | null>(null)
  const destroyed = useRef(false)

  const moved = useCallback((): MovedFact | null => {
    if (destroyed.current) return 'surface'
    if (!allowed) return 'permission'
    const fact = movedSince(under.authority)
    if (fact) return fact
    const principal = livePrincipal()
    if ((principal?.user_id ?? null) !== under.principal) return 'principal'
    const tenant = useTenantStore.getState().activeTenant
    if (permission !== undefined && !rbacCan(permission, { principal, tenant }))
      return 'permission'
    // A SECOND permission the offered act also depends on. The seen cursor is the
    // case that needs it: its preparation is a `delivery:read` GET and the act it
    // offers is a `delivery:write` PUT, and IR-I2-1 asks for BOTH to be re-evaluated
    // at the dispatch boundary rather than trusted from a rendered boolean. Kept as
    // a second SCALAR rather than a list on purpose: `check-console-perms` resolves
    // the literals that flow into this call, and it cannot read an array literal.
    if (
      alsoRequires !== undefined &&
      !rbacCan(alsoRequires, { principal, tenant })
    )
      return 'permission'
    return null
  }, [allowed, under, permission, alsoRequires, livePrincipal])

  // Permission lost: abort what is on the wire right after the commit that lost it.
  useEffect(() => {
    if (!allowed) {
      controller.current?.abort()
      controller.current = null
    }
  }, [allowed])
  // Boundary moved: same, and on mount there is nothing to cancel yet.
  useEffect(() => {
    controller.current?.abort()
    controller.current = null
  }, [boundary])
  // Unmounted: the surface is over; nothing begun later may borrow it.
  useEffect(() => {
    destroyed.current = false
    return () => {
      destroyed.current = true
      controller.current?.abort()
      controller.current = null
    }
  }, [])

  const end = useCallback(() => {
    controller.current?.abort()
    controller.current = null
  }, [])
  const begin = useCallback((): AbortSignal | null => {
    controller.current?.abort()
    controller.current = null
    if (moved() !== null) return null
    const ac = new AbortController()
    controller.current = ac
    return ac.signal
  }, [moved])
  const check = useCallback((): void => {
    const fact = moved()
    if (fact) {
      controller.current?.abort()
      controller.current = null
      throw new StaleIntentError(fact)
    }
    if (controller.current?.signal.aborted)
      throw new StaleIntentError('surface')
  }, [moved])
  const alive = useCallback(
    () =>
      moved() === null &&
      controller.current !== null &&
      !controller.current.signal.aborted,
    [moved],
  )
  return { begin, alive, end, check }
}
