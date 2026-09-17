// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import {
  CapabilityLostError,
  sameQuestion,
  type CapabilityPermit,
  type NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import {
  administrationSurfaceQuestion,
  grantSheetQuestion,
} from './capabilities'
import {
  dispatchGuardFor,
  type AckIntent,
  type CreateIntent,
  type CursorIntent,
  type GrantIntent,
  type HandoffOfferIntent,
  type HandoffResponseIntent,
  type RevokeIntent,
  type SendIntent,
  type UpdateChannelIntent,
} from './intent'
import type {
  AckResult,
  AdministrationPage,
  AdministrationState,
  Channel,
  ChannelCatalogPage,
  ChannelMutationResult,
  CursorAdvanceResult,
  CursorTokenResult,
  DirectoryAgent,
  GrantAdministrationPage,
  GrantStateFilter,
  GrantSubjectFilterKind,
  HandoffDetail,
  HandoffInboxPage,
  HandoffOfferResult,
  HandoffResponseResult,
  HandoffStateFilter,
  InboxPage,
  PublishResult,
  ReadResult,
  RevokeResult,
  RosterMember,
} from './types'

/**
 * The K3 communication client of the console: the EIGHT operations of the first
 * increment (catalog, create, channel read, send, inbox, delivery read, message
 * read, Ack), the SEVEN of the second (administrable catalog, grant history, PATCH,
 * grant, revoke, cursor token, cursor advance) plus the two directory reads the
 * pickers need. Every call goes through
 * the shared `http` seam — tenant header, bearer, 401 replay and the error envelope
 * are that module's business — and names its tenant explicitly through
 * `TenantRequestOptions`, captured when the operator acted, never read again later.
 *
 * Paths are direct string literals: the console-route census resolves literal
 * module paths and deliberately does not execute nested constant interpolation.
 */
const CHANNELS = '/v1/m/sessions/channels'
const MESSAGES = '/v1/m/sessions/messages'
const DELIVERIES = '/v1/m/sessions/deliveries'
const INBOX = '/v1/m/sessions/inbox'
const HANDOFFS = '/v1/m/sessions/handoffs'
const MEMBERS = '/v1/members'
const AGENTS = '/v1/agents'
const encodeId = (value: string) => encodeURIComponent(value)

/** One page of the two directory reads. A directory row is a CANDIDATE, not an
 * eligible recipient: the engine decides eligibility on the send. */
export const DIRECTORY_PAGE = 200

/**
 * Query keys, partitioned by the AUTHORITY BOUNDARY and then by the WORKSPACE:
 *   ['communications', tenant, 'b', epoch, workspace, resource, params]
 * The epoch is the opaque number `useAuthBoundary` mints per distinct
 * (principal, tenant, credential generation); the workspace is the explicit
 * selection every K3 read is made under. Nothing read under one partition can be
 * painted for another, and `useCommunicationsScope` cancels and removes the old
 * partition when either moves. No bearer, session id or continuation token is a
 * key: continuations are opaque page cursors that live in the query's own pages.
 */
export const communicationsKeys = {
  all: (tenant: string | null) => ['communications', tenant] as const,
  boundaryScope: (tenant: string | null, epoch: number) =>
    ['communications', tenant, 'b', epoch] as const,
  workspaceScope: (tenant: string | null, epoch: number, workspace: string) =>
    ['communications', tenant, 'b', epoch, workspace] as const,
  catalog: (
    tenant: string | null,
    epoch: number,
    workspace: string,
    params: { limit: number },
  ) =>
    [
      'communications',
      tenant,
      'b',
      epoch,
      workspace,
      'catalog',
      params,
    ] as const,
  inbox: (
    tenant: string | null,
    epoch: number,
    workspace: string,
    params: { limit: number },
  ) =>
    ['communications', tenant, 'b', epoch, workspace, 'inbox', params] as const,
  /** Tenant-wide directory: the members roster is not a workspace collection. */
  members: (tenant: string | null, epoch: number) =>
    ['communications', tenant, 'b', epoch, '-', 'members'] as const,
  agents: (tenant: string | null, epoch: number, workspace: string) =>
    ['communications', tenant, 'b', epoch, workspace, 'agents'] as const,
  /** I2 — the administrable catalog, by persisted state and page size. */
  administration: (
    tenant: string | null,
    epoch: number,
    workspace: string,
    params: { state: AdministrationState; limit: number },
  ) =>
    [
      'communications',
      tenant,
      'b',
      epoch,
      workspace,
      'administration',
      params,
    ] as const,
  /** I3 — the CONTENT-FREE personal handoff page, by explicit state filter and page
   * size. The continuation is NOT a key: it lives in the query's own pages, and it
   * resets with the filter because the engine mints a token per filter domain. */
  handoffs: (
    tenant: string | null,
    epoch: number,
    workspace: string,
    params: { state: HandoffStateFilter; limit: number },
  ) =>
    [
      'communications',
      tenant,
      'b',
      epoch,
      workspace,
      'handoffs',
      params,
    ] as const,
  /** I2 — one Channel's grant history, by channel and NORMALISED filters. The
   * continuation chain of each (channel, filters) is its own query. */
  grants: (
    tenant: string | null,
    epoch: number,
    workspace: string,
    channelId: string,
    params: GrantFilters,
  ) =>
    [
      'communications',
      tenant,
      'b',
      epoch,
      workspace,
      'grants',
      channelId,
      params,
    ] as const,
}

/** The grant history filters as the query normalises them: a subject is either
 * both of kind and ref or neither (the engine refuses one without the other). */
export interface GrantFilters {
  state: GrantStateFilter
  limit: number
  subject_kind?: GrantSubjectFilterKind
  subject_ref?: string
}

export interface CatalogParams {
  workspace_id: string
  /** Explicit visible page size, 1..200. Always sent: a page that names no ceiling
   * asks the engine for its default, and a screen that shows a page says so. */
  limit: number
  /** The opaque continuation the previous page returned. Never decoded here. */
  continuation?: string
}

export type InboxParams = CatalogParams

/** `GET /channels` — the caller's visible page of the workspace's active Channels. */
export function listChannels(
  params: CatalogParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ChannelCatalogPage> {
  return http.get<ChannelCatalogPage>(CHANNELS, {
    ...options,
    query: {
      workspace_id: params.workspace_id,
      limit: params.limit,
      continuation: params.continuation,
    },
    signal,
  })
}

export interface ChannelCreateOutcome {
  result: ChannelMutationResult
  /** The Channel ETag from the response header (the body carries it too). */
  etag: string | null
}

/**
 * Options of the three I1 mutations: the explicit tenant of every request through
 * this seam, plus the SURFACE's half of the dispatch guard (`useIntentGuard().check`).
 * The intent's own half — its frozen authority against the live stores — is always
 * attached here, whether or not a surface guard is given, so the transport never
 * sends an intention confirmed under a credential, tenant or workspace that moved.
 */
export type MutationOptions = TenantRequestOptions & {
  guard?: () => void
}

/**
 * `POST /channels`. There is NO idempotency key on this route, so the client never
 * retries it on its own: a transport failure is an UNKNOWN outcome the operator
 * reconciles by looking at the catalog for the slug before creating again.
 */
export async function createChannel(
  intent: CreateIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<ChannelCreateOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.postWithMeta<ChannelMutationResult>(
    CHANNELS,
    intent.body,
    { ...rest, signal, dispatchGuard: dispatchGuardFor(intent, guard) },
  )
  return { result: data, etag: headers.get('ETag') }
}

/** `GET /channels/{id}` — the Channel with its ETag, and nothing about grants. */
export async function getChannel(
  channelId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<{ channel: Channel; etag: string | null }> {
  const { data, headers } = await http.getWithMeta<Channel>(
    `${CHANNELS}/${encodeId(channelId)}`,
    { ...options, signal },
  )
  return { channel: data, etag: headers.get('ETag') }
}

export interface SendOutcome {
  result: PublishResult
  /** The engine answered from the idempotency ledger: this exact intention was
   * ALREADY applied. 200 + `replayed`, against 201 for a fresh application. */
  replayed: boolean
  status: number
}

/**
 * `POST /messages/send` with the intent's key — never a fresh one. There is no
 * preview endpoint and no plan hash to bind to, so nothing is invented here: the
 * body, the target and the key are the intent's, exactly as the operator confirmed.
 */
export async function sendNotice(
  intent: SendIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<SendOutcome> {
  const { guard, ...rest } = options
  const { data, status } = await http.postWithMeta<PublishResult>(
    `${MESSAGES}/send`,
    intent.body,
    {
      ...rest,
      headers: { 'Idempotency-Key': intent.key },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return {
    result: data,
    replayed: data.replayed === true || status === 200,
    status,
  }
}

/** `GET /inbox` — the exact personal mailbox of the caller in one workspace. */
export function listInbox(
  params: InboxParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<InboxPage> {
  return http.get<InboxPage>(INBOX, {
    ...options,
    query: {
      workspace_id: params.workspace_id,
      limit: params.limit,
      continuation: params.continuation,
    },
    signal,
  })
}

/** `GET /deliveries/{id}` — one Delivery for its exact recipient, read fresh. */
export function getDelivery(
  deliveryId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ReadResult> {
  return http.get<ReadResult>(`${DELIVERIES}/${encodeId(deliveryId)}`, {
    ...options,
    signal,
  })
}

/** `GET /messages/{id}` — one Message for its exact user recipient, read fresh. */
export function getMessage(
  messageId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ReadResult> {
  return http.get<ReadResult>(`${MESSAGES}/${encodeId(messageId)}`, {
    ...options,
    signal,
  })
}

export interface AckOutcome {
  result: AckResult
  etag: string | null
}

/**
 * `POST /deliveries/{id}/ack` — bodyless, with `If-Match` built from the Delivery
 * version the operator READ and the intent's own key. A 412 is never answered by
 * re-reading and re-sending here: the caller shows the conflict and the operator
 * re-reads and reconfirms.
 */
export async function ackDelivery(
  intent: AckIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<AckOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.postWithMeta<AckResult>(
    `${DELIVERIES}/${encodeId(intent.deliveryId)}/ack`,
    undefined,
    {
      ...rest,
      headers: { 'If-Match': intent.etag, 'Idempotency-Key': intent.key },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return { result: data, etag: headers.get('ETag') }
}

/** `GET /members` (user:read) — the tenant roster the user picker offers. */
export function listMembers(
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ListResponse<RosterMember>> {
  return http.get<ListResponse<RosterMember>>(MEMBERS, {
    ...options,
    query: { limit: DIRECTORY_PAGE },
    signal,
  })
}

/** `GET /agents?workspace_id=…` (agent:read) — the agent picker's candidates; an
 * agent is addressable by its `identity_id`, never by its row id. */
export function listAgents(
  workspaceId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ListResponse<DirectoryAgent>> {
  return http.get<ListResponse<DirectoryAgent>>(AGENTS, {
    ...options,
    query: { workspace_id: workspaceId, limit: DIRECTORY_PAGE },
    signal,
  })
}

/* ── I2: the seven callers of channel administration and the personal cursor ──── */

/* ── the two administrative READS travel under their own admission ────────────── */

/**
 * A LOCAL refusal of an administrative read: the admission the request would have
 * travelled under is not a CURRENT and EXACT answer to the question this very request
 * asks, so NOTHING was sent. There is no status, request id or server sentence because
 * there was no request, and this is the whole reason it is a class of its own:
 *
 *   · it is NOT a server response — the engine decided nothing, so a surface must not
 *     paint it through `classifyFailure` as if it had. The engine still authorizes
 *     every request it does receive; this refusal happens strictly before that.
 *   · it is NOT on the retry cadence — re-asking cannot make a spent permit current
 *     again, and a permit is a one-way door (`createCapabilityPermit`). Only a NEW
 *     exact admission resumes the read, and that arrives as a re-render, not a retry.
 *
 * `reason` names WHICH of the two preconditions failed. `cause` keeps the permit's own
 * `CapabilityLostError`, whose message distinguishes an expiry from a context move, so
 * naming the seam's reason does not throw the permit's away.
 */
export class UnadmittedReadError extends Error {
  /** `permit`: there is no admission, or the one held is no longer current (expired, or
   *  its owner/context moved). `question`: the admission held answers ANOTHER question
   *  than the one this request asks — another surface, channel, workspace or kind. */
  readonly reason: 'permit' | 'question'
  constructor(reason: 'permit' | 'question', cause?: unknown) {
    super(
      `the administrative read is not admitted (${reason}); nothing was sent`,
      cause === undefined ? undefined : { cause },
    )
    this.name = 'UnadmittedReadError'
    this.reason = reason
  }
}

/**
 * Options of the TWO ADMINISTRATIVE READS: the explicit tenant of every request through
 * this seam, plus the ADMISSION the opening was loaded under.
 *
 * ⛔ `admission` IS REQUIRED, AND THAT IS THE POINT. The independent review of
 *    c04cb75de1 measured both query callbacks handing the client only `{ tenant }`:
 *    a rendered `enabled` had decided the read, and nothing checked the permit again
 *    between that render and the bytes. Rendered `enabled` is not a final authority
 *    check for a query already installed, explicitly refetched, retried or scheduled
 *    while admission changes. A REQUIRED field turns that omission into a compile
 *    error instead of a defect a wire capture has to find.
 *
 *    `null` is admitted as a VALUE — a surface with no current positive still has to
 *    be able to call — and it refuses with zero bytes, so the deny-closed decision
 *    lives here, once, rather than in each caller.
 */
export type AdmittedReadOptions = TenantRequestOptions & {
  readonly admission: CapabilityPermit | null
}

/**
 * The dispatch guard of ONE administrative read: the synchronous check the shared client
 * runs immediately before EVERY fetch it makes for this request — the first send, after
 * the proactive credential refresh has been awaited, and the single 401 replay, after
 * its own refresh (`RequestOptions.dispatchGuard`, lib/api/client.ts). Between the query
 * callback and those fetches the client awaits, so a check at the callback alone would
 * leave exactly the interval where an admission expires or its owner moves.
 *
 * `asked` is DERIVED FROM THE REQUEST ITSELF, never from what the caller says it is
 * asking: the workspace, channel and kind compared here are the ones travelling in the
 * URL a few lines below. That is what makes "a permit naming one resource is not a
 * licence to enumerate its neighbours" true at the seam and not only in the comment.
 */
function admittedRead(
  permit: CapabilityPermit | null,
  asked: NormalizedCapabilityQuestion | null,
): () => void {
  return () => {
    if (!permit) throw new UnadmittedReadError('permit')
    if (!asked || !sameQuestion(permit.question, asked))
      throw new UnadmittedReadError('question')
    try {
      permit.assertCurrent()
    } catch (cause) {
      if (cause instanceof CapabilityLostError)
        throw new UnadmittedReadError('permit', cause)
      throw cause
    }
  }
}

export interface AdministrationParams {
  workspace_id: string
  /** Persisted Channel state selection; `all` is the engine's default and is sent
   * explicitly so the screen and the request say the same thing. */
  state: AdministrationState
  limit: number
  continuation?: string
}

/**
 * `GET /channels/administration` (sessions:channel:admin) — the Channels this caller
 * may administer in the workspace, with each Channel's precondition ETag. The
 * response is `Cache-Control: no-store` and carries no HTTP ETag: nothing here sends
 * `If-None-Match`, expects a 304 or rebuilds a validator from a version.
 *
 * The SURFACE question of the workspace being listed is what admits it: the collection's
 * own admission, never an entity permit of a row in it and never a mutation's.
 */
export function listAdministrableChannels(
  params: AdministrationParams,
  options: AdmittedReadOptions,
  signal?: AbortSignal,
): Promise<AdministrationPage> {
  const { admission, ...rest } = options
  return http.get<AdministrationPage>(`${CHANNELS}/administration`, {
    ...rest,
    query: {
      workspace_id: params.workspace_id,
      state: params.state,
      limit: params.limit,
      continuation: params.continuation,
    },
    signal,
    dispatchGuard: admittedRead(
      admission,
      administrationSurfaceQuestion(params.workspace_id),
    ),
  })
}

export interface GrantListParams extends GrantFilters {
  workspace_id: string
  continuation?: string
}

/**
 * `GET /channels/{id}/grants` (sessions:channel:admin) — the administrable Channel,
 * its precondition ETag, `observed_at`, and one page of STORED grant generations.
 * `state` selects the persisted state (an active row past its TTL is still
 * `active` here, reported `temporal_state: expired`); `subject_kind` and
 * `subject_ref` travel together or not at all. A 409 `channel_snapshot_changed`
 * between two pages is the caller's signal to discard EVERY page and restart with
 * no continuation.
 */
export function listChannelGrants(
  channelId: string,
  params: GrantListParams,
  options: AdmittedReadOptions,
  signal?: AbortSignal,
): Promise<GrantAdministrationPage> {
  const { admission, ...rest } = options
  const subject =
    params.subject_kind !== undefined && params.subject_ref !== undefined
      ? { subject_kind: params.subject_kind, subject_ref: params.subject_ref }
      : {}
  return http.get<GrantAdministrationPage>(
    `${CHANNELS}/${encodeId(channelId)}/grants`,
    {
      ...rest,
      query: {
        workspace_id: params.workspace_id,
        state: params.state,
        ...subject,
        limit: params.limit,
        continuation: params.continuation,
      },
      signal,
      // The ENTITY question of THIS channel in THIS workspace — the operation permit the
      // sheet and a deep link are admitted by. A surface collection permit, a POST-grant
      // permit or a revoke permit answers another question and is refused here.
      dispatchGuard: admittedRead(
        admission,
        grantSheetQuestion(params.workspace_id, channelId),
      ),
    },
  )
}

export interface ChannelAdminOutcome {
  result: ChannelMutationResult
  /** The new Channel ETag from the response header (the body carries it too). */
  etag: string | null
}

/**
 * `PATCH /channels` (sessions:channel:admin) — `If-Match` is the CHANNEL ETag the
 * operator read; the body is `channel_id` plus only the changed fields. There is no
 * idempotency key on this route, so a 409/412/428 is shown and the operator
 * re-reads and reconfirms; a lost response is an unknown outcome, never a re-send.
 */
export async function updateChannel(
  intent: UpdateChannelIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<ChannelAdminOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.patchWithMeta<ChannelMutationResult>(
    CHANNELS,
    intent.body,
    {
      ...rest,
      headers: { 'If-Match': intent.etag },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return { result: data, etag: headers.get('ETag') }
}

/** `POST /channels/{id}/grants` (sessions:channel:admin) — one explicit generation
 * for one subject, under the Channel ETag the operator read. No idempotency key. */
export async function grantChannel(
  intent: GrantIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<ChannelAdminOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.postWithMeta<ChannelMutationResult>(
    `${CHANNELS}/${encodeId(intent.channelId)}/grants`,
    intent.body,
    {
      ...rest,
      headers: { 'If-Match': intent.etag },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return { result: data, etag: headers.get('ETag') }
}

/** `POST /channels/{id}/grants/{grant_id}/revoke` (sessions:channel:admin) —
 * bodyless; the generation is the one the operator saw and confirmed. */
export async function revokeChannelGrant(
  intent: RevokeIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<ChannelAdminOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.postWithMeta<RevokeResult>(
    `${CHANNELS}/${encodeId(intent.channelId)}/grants/${encodeId(intent.grantId)}/revoke`,
    undefined,
    {
      ...rest,
      headers: { 'If-Match': intent.etag },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return { result: data, etag: headers.get('ETag') }
}

export interface CursorTokenOutcome {
  result: CursorTokenResult
  /** The cursor's strong ETag from the header; the body carries it too. */
  etag: string
}

/**
 * `GET /inbox/cursors/personal/{recipient}` (sessions:delivery:read) — mints the
 * navigation token for ONE inbox page's `cursor_target`. It advances nothing and
 * returns no position: only the token, the cursor's version and its ETag. The
 * recipient is the authenticated mailbox (`principal.user_id`, no prefix).
 */
/**
 * Options of the cursor PREPARATION. The guard is REQUIRED, and that is the whole
 * difference from `MutationOptions`: the three administrative acts and the cursor
 * advance carry an intent, so `dispatchGuardFor` always checks that intent's frozen
 * authority even when a surface passes no guard of its own. The preparation has no
 * intent yet — it is the call that mints the token an intent will carry — so an
 * absent guard would leave NOTHING between a moved authority and the wire. Making
 * it required means the shape the review found cannot be written again.
 */
export type PreparationOptions = TenantRequestOptions & {
  guard: () => void
}

export async function getCursorToken(
  recipient: string,
  params: { workspace_id: string; target: string },
  options: PreparationOptions,
  signal?: AbortSignal,
): Promise<CursorTokenOutcome> {
  // ⛔ THIS READ CARRIES THE TRANSPORT GUARD, and it is not defensive decoration:
  // it is the correction of IR-I2-1. The shared client may await a preventive
  // credential refresh between the caller's decision and `fetch`, so a rotation in
  // that window sent this GET with the NEXT credential — measured by the reviewer's
  // witness, which recorded exactly one request bearing the rotated bearer. The
  // abort effect cannot close that gap, because the bytes have already left. The
  // caller composes the guard from the authority frozen at the operator's act plus
  // its own live re-check, and the client runs it immediately before every fetch,
  // including the 401 replay.
  const { guard, ...rest } = options
  const { data, headers } = await http.getWithMeta<CursorTokenResult>(
    `${INBOX}/cursors/personal/${encodeId(recipient)}`,
    {
      ...rest,
      query: { workspace_id: params.workspace_id, target: params.target },
      signal,
      dispatchGuard: guard,
    },
  )
  return { result: data, etag: headers.get('ETag') ?? data.etag }
}

export interface CursorAdvanceOutcome {
  result: CursorAdvanceResult
  etag: string | null
}

/**
 * `PUT /inbox/cursors/personal/{recipient}` (sessions:delivery:write) — NO query;
 * body `{cursor, delivery_id}` exactly as frozen at confirmation; `If-Match` is the
 * CURSOR's ETag from the GET; `Idempotency-Key` is the intent's own canonical key.
 * A retry after an ambiguous result re-sends this identical object; a 409/412/428
 * kills the intent and the operator re-reads the inbox before preparing again.
 */
export async function advanceCursor(
  intent: CursorIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<CursorAdvanceOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.putWithMeta<CursorAdvanceResult>(
    `${INBOX}/cursors/personal/${encodeId(intent.recipient)}`,
    intent.body,
    {
      ...rest,
      headers: { 'If-Match': intent.etag, 'Idempotency-Key': intent.key },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return { result: data, etag: headers.get('ETag') }
}

/* ── I3: the four callers of the work handoff ─────────────────────────────────── */

export interface HandoffOfferOutcome {
  result: HandoffOfferResult
  /** The engine answered from the idempotency ledger: this exact offer was ALREADY
   * created. 200 + `replayed`, against 201 for a fresh application. */
  replayed: boolean
  status: number
  /** The handoff ETag from the response header; the body carries it too. */
  etag: string | null
}

/**
 * `POST /handoffs` — the atomic offer. `If-Match` is the WorkItem's strong ETag from
 * the fresh read the confirmation was built on, and the body's
 * `expected_owner_epoch` comes from that same read: two coordinates of one
 * observation, kept as distinct typed inputs because they travel separately.
 *
 * A 201 is not a transfer. The engine creates the carrier and the offer and leaves
 * ownership where it was; only an accepted response moves it.
 */
export async function offerHandoff(
  intent: HandoffOfferIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<HandoffOfferOutcome> {
  const { guard, ...rest } = options
  const { data, status, headers } = await http.postWithMeta<HandoffOfferResult>(
    HANDOFFS,
    intent.body,
    {
      ...rest,
      headers: { 'If-Match': intent.etag, 'Idempotency-Key': intent.key },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return {
    result: data,
    replayed: data.replayed === true || status === 200,
    status,
    etag: headers.get('ETag') ?? data.etag,
  }
}

export interface HandoffInboxParams {
  workspace_id: string
  /** EXACTLY ONE state. The engine refuses a repeated, unknown or comma-joined
   * selector, and mints its continuations per filter domain, so a token of one
   * filter is not offered to another. */
  state: HandoffStateFilter
  /** Explicit visible page size, 1..200. Always sent, like every other K3 page. */
  limit: number
  /** The opaque `h3n1` continuation the previous page returned. Never decoded here. */
  continuation?: string
}

/**
 * `GET /inbox/handoffs` (sessions:delivery:read) — the offers addressed to THIS
 * recipient, WITHOUT their content. The page carries no HTTP ETag and no total:
 * `has_more` is the engine's own authorized limit+1 fact, not a count of hidden rows.
 */
export function listHandoffInbox(
  params: HandoffInboxParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<HandoffInboxPage> {
  return http.get<HandoffInboxPage>(`${INBOX}/handoffs`, {
    ...options,
    query: {
      workspace_id: params.workspace_id,
      state: params.state,
      limit: params.limit,
      continuation: params.continuation,
    },
    signal,
  })
}

/**
 * `GET /deliveries/{id}/handoff` (sessions:delivery:read) — the PROTECTED offer
 * context of one carrier Delivery, read fresh for its exact recipient. The id is the
 * carrier the personal page admitted, never a handoff id: there is no
 * `GET /handoffs/{id}` on this engine and this caller does not pretend there is.
 *
 * The read carries no HTTP ETag either. The precondition a response takes is
 * `handoff.etag` INSIDE the body, and nothing here rebuilds one from a version.
 */
export function getHandoffDetail(
  deliveryId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<HandoffDetail> {
  return http.get<HandoffDetail>(
    `${DELIVERIES}/${encodeId(deliveryId)}/handoff`,
    { ...options, signal },
  )
}

export interface HandoffResponseOutcome {
  result: HandoffResponseResult
  /** The ledger answered: this exact response was ALREADY applied. This route
   * answers 200 for both, so `replayed` in the body is the ONLY signal. */
  replayed: boolean
  etag: string | null
}

/**
 * `POST /handoffs/{id}/responses` (sessions:handoff-response:write) — accept without
 * a reason, or reject with the required `{code, text?, references?}`. `If-Match` is
 * the HANDOFF ETag the protected detail reported, and the key is the intent's own.
 *
 * A 409 here is code-less at R45 and covers a stale precondition, a stale offer, an
 * ended generation, an owner-epoch mismatch and a rebound key alike. The caller shows
 * ONE conflict class and re-reads; it never decides which of them happened.
 */
export async function respondToHandoff(
  intent: HandoffResponseIntent,
  options: MutationOptions,
  signal?: AbortSignal,
): Promise<HandoffResponseOutcome> {
  const { guard, ...rest } = options
  const { data, headers } = await http.postWithMeta<HandoffResponseResult>(
    `${HANDOFFS}/${encodeId(intent.handoffId)}/responses`,
    intent.body,
    {
      ...rest,
      headers: { 'If-Match': intent.etag, 'Idempotency-Key': intent.key },
      signal,
      dispatchGuard: dispatchGuardFor(intent, guard),
    },
  )
  return {
    result: data,
    replayed: data.replayed === true,
    etag: headers.get('ETag') ?? data.etag,
  }
}
