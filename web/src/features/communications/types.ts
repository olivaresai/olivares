// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { components, paths } from '@/lib/api/openapi.gen'

/**
 * The K3 communication contract AS THE ENGINE SERVES IT. Every shape below is derived
 * from the generated `paths` of the embedded OpenAPI document — the module routes are
 * beta and their schemas are inline, so there is nothing under `components.schemas`
 * to name and a hand-written mirror would only be a second copy that drifts. If the
 * engine changes a field, `pnpm codegen` moves these types and the compiler names
 * every consumer.
 */
type JsonOf<R> = R extends { content: { 'application/json': infer B } }
  ? B
  : never
type BodyOf<Op> = Op extends {
  requestBody: { content: { 'application/json': infer B } }
}
  ? B
  : Op extends { requestBody?: { content: { 'application/json': infer B } } }
    ? B
    : never

type ChannelsPath = paths['/v1/m/sessions/channels']
type ChannelPath = paths['/v1/m/sessions/channels/{id}']
type SendOp = paths['/v1/m/sessions/messages/send']['post']
type DeliveryPath = paths['/v1/m/sessions/deliveries/{id}']
type AckOp = paths['/v1/m/sessions/deliveries/{id}/ack']['post']
type InboxPath = paths['/v1/m/sessions/inbox']
type AdministrationPath = paths['/v1/m/sessions/channels/administration']
type GrantsPath = paths['/v1/m/sessions/channels/{id}/grants']
type RevokeOp =
  paths['/v1/m/sessions/channels/{id}/grants/{grant_id}/revoke']['post']
type CursorPath = paths['/v1/m/sessions/inbox/cursors/personal/{recipient}']
type HandoffsPath = paths['/v1/m/sessions/handoffs']
type HandoffResponseOp = paths['/v1/m/sessions/handoffs/{id}/responses']['post']
type HandoffInboxPath = paths['/v1/m/sessions/inbox/handoffs']
type HandoffDetailPath = paths['/v1/m/sessions/deliveries/{id}/handoff']

/** `GET /channels` — the visible page with the caller's OWN current local bits. */
export type ChannelCatalogPage = JsonOf<ChannelsPath['get']['responses'][200]>
export type ChannelCatalogItem = ChannelCatalogPage['items'][number]
/** The three independent LOCAL grant bits of the caller on one visible Channel. They
 * say nothing about the core permission a later write or admin route also requires. */
export type ChannelAccess = ChannelCatalogItem['my_access']

/** `POST /channels` — explicit initial grants; nothing is implied by the engine. */
export type ChannelCreateInput = BodyOf<ChannelsPath['post']>
export type ChannelGrantInput = ChannelCreateInput['initial_grants'][number]
export type GrantSubject = ChannelGrantInput['subject']
export type GrantSubjectKind = GrantSubject['kind']
export type ChannelMutationResult = JsonOf<
  ChannelsPath['post']['responses'][201]
>
export type ChannelGrant = NonNullable<ChannelMutationResult['grants']>[number]

/** `GET /channels/{id}` — the Channel alone: NO grants and NO my_access. */
export type Channel = JsonOf<ChannelPath['get']['responses'][200]>

/** `POST /messages/send` — one direct notice to one user, agent or session. */
export type SendNoticeInput = BodyOf<SendOp>
export type Recipient = SendNoticeInput['recipient']
export type RecipientKind = Recipient['kind']
export type MessageContent = SendNoticeInput['content']
export type ContentBlock = MessageContent['blocks'][number]
export type ContentBlockType = ContentBlock['type']
export type ContentReference = NonNullable<ContentBlock['reference']>
export type TextFormat = NonNullable<ContentBlock['format']>
export type NoticeUrgency = NonNullable<SendNoticeInput['urgency']>
/** 201 applied and 200 replayed carry the same body; the status and `replayed`
 * distinguish them. */
export type PublishResult = JsonOf<SendOp['responses'][201]>

/** `GET /deliveries/{id}` and `GET /messages/{id}` share one read projection. */
export type ReadResult = JsonOf<DeliveryPath['get']['responses'][200]>
export type DeliveryView = ReadResult['delivery']
export type MessageView = ReadResult['message']
export type Fulfillment = ReadResult['fulfillment']
export type MessageSender = MessageView['sender']

/** `GET /inbox` — the exact personal mailbox, page by opaque continuation. */
export type InboxPage = JsonOf<InboxPath['get']['responses'][200]>
export type InboxItem = InboxPage['items'][number]

/** `POST /deliveries/{id}/ack` — CAS on the Delivery version, idempotent receipt. */
export type AckResult = JsonOf<AckOp['responses'][200]>

/* ── I2: administration and the personal seen cursor ─────────────────────────── */

/** `GET /channels/administration` — the Channels this caller may ADMINISTER in one
 * workspace (core admin + local admin bit), each with the strong Channel ETag the
 * mutations take as their precondition. No `my_access`, no total, archived included
 * when asked. The page carries NO HTTP ETag: `etag` here is a Channel precondition. */
export type AdministrationPage = JsonOf<
  AdministrationPath['get']['responses'][200]
>
export type AdministrationItem = AdministrationPage['items'][number]
export type AdministrationState = NonNullable<
  AdministrationPath['get']['parameters']['query']['state']
>

/** `GET /channels/{id}/grants` — the administrable Channel, its precondition ETag,
 * the database instant the page was closed at, and one page of STORED grant
 * generations. `temporal_state` is derived at `observed_at` and never replaces the
 * stored `grant.state`. */
export type GrantAdministrationPage = JsonOf<
  GrantsPath['get']['responses'][200]
>
export type GrantAdministrationItem = GrantAdministrationPage['items'][number]
export type GrantTemporalState = GrantAdministrationItem['temporal_state']
export type GrantStateFilter = NonNullable<
  GrantsPath['get']['parameters']['query']['state']
>
export type GrantSubjectFilterKind = NonNullable<
  GrantsPath['get']['parameters']['query']['subject_kind']
>

/** `PATCH /channels` — `channel_id` plus ONLY the fields being changed. */
export type ChannelUpdateInput = BodyOf<ChannelsPath['patch']>
export type ChannelUpdateField = Exclude<keyof ChannelUpdateInput, 'channel_id'>
export type ChannelPatchState = NonNullable<ChannelUpdateInput['state']>

/** `POST /channels/{id}/grants` — one explicit, optionally expiring generation. */
export type GrantCreateInput = BodyOf<GrantsPath['post']>

/** `POST /channels/{id}/grants/{grant_id}/revoke` — bodyless; the result is the
 * same mutation receipt the other two administrative acts return. */
export type RevokeResult = JsonOf<RevokeOp['responses'][200]>

/** `GET /inbox/cursors/personal/{recipient}` — an opaque navigation token minted
 * for one inbox page's `cursor_target`, with the cursor's version and strong ETag.
 * Nothing is advanced; the token conveys no authority. */
export type CursorTokenResult = JsonOf<CursorPath['get']['responses'][200]>
/** `PUT /inbox/cursors/personal/{recipient}` — the body: the minted token and the
 * exact Delivery it names. */
export type CursorAdvanceInput = BodyOf<CursorPath['put']>
/** The committed (or replayed) cursor command: version, ETag, audit sequence and
 * the projection the engine chose to return (`last_seen_seq`, optional barrier). */
export type CursorAdvanceResult = JsonOf<CursorPath['put']['responses'][200]>
export type CursorProjection = CursorAdvanceResult['projection']

/* ── I3: the four handoff operations ─────────────────────────────────── */

/** `POST /handoffs` — the offer body. `expected_owner_epoch` and the WorkItem
 * `If-Match` come from ONE fresh WorkItem read; the deadline is an absolute instant.
 * Creating the offer transfers NOTHING: ownership moves only on an accepted response. */
export type HandoffOfferInput = BodyOf<HandoffsPath['post']>
export type HandoffContent = HandoffOfferInput['handoff']
export type HandoffArtifactRef = NonNullable<
  HandoffContent['artifact_refs']
>[number]
export type HandoffRecipient = HandoffOfferInput['recipient']
export type HandoffRecipientKind = HandoffRecipient['kind']
/** 201 created and 200 replayed carry the same body; `replayed` and the status
 * distinguish them, exactly as on `POST /messages/send`. */
export type HandoffOfferResult = JsonOf<HandoffsPath['post']['responses'][201]>

/** `GET /inbox/handoffs` — the CONTENT-FREE personal page: carrier ids, handoff
 * administrative fields, the WorkItem reference and the observation instant. The
 * protected summary is NOT here, so no list row may pretend to show it. */
export type HandoffInboxPage = JsonOf<HandoffInboxPath['get']['responses'][200]>
export type HandoffInboxItem = HandoffInboxPage['items'][number]
export type HandoffRow = HandoffInboxItem['handoff']
export type HandoffState = HandoffRow['state']
export type HandoffStateFilter = NonNullable<
  HandoffInboxPath['get']['parameters']['query']['state']
>

/** `GET /deliveries/{id}/handoff` — the PROTECTED offer context of one carrier
 * Delivery, read fresh for its own recipient. `handoff.etag` is the precondition of
 * a response; the carrier's `delivery_version` is NOT, and is never turned into one. */
export type HandoffDetail = JsonOf<HandoffDetailPath['get']['responses'][200]>
export type HandoffCarrier = HandoffDetail['carrier']
/** The engine's own reading of whether this offer can still be answered. `stale` and
 * `terminal` are its words, not a client inference from a timestamp. */
export type HandoffOfferContext = HandoffDetail['offer_context']
export type HandoffTerminalReason = NonNullable<
  HandoffDetail['terminal_reason']
>

/** `POST /handoffs/{id}/responses` — accept without a reason, or reject with one. */
export type HandoffResponseInput = BodyOf<HandoffResponseOp>
export type HandoffTransition = HandoffResponseInput['transition']
export type HandoffReason = NonNullable<HandoffResponseInput['reason']>
export type HandoffReasonReference = NonNullable<
  HandoffReason['references']
>[number]
/** The response receipt: the command's own result. `ack_id` and
 * `resulting_lease_fence` are OPTIONAL and are shown only when actually present —
 * R45 transfers a vacant generation without any lease, and inventing a fence would
 * describe a running execution that does not exist. */
export type HandoffResponseResult = JsonOf<HandoffResponseOp['responses'][200]>

/** The five states the personal collection can be filtered by, pinned to the
 * engine's own query vocabulary. `offered` is both the engine default and the
 * console's initial filter; it is sent explicitly all the same. */
export const HANDOFF_STATE_FILTERS = [
  'offered',
  'accepted',
  'rejected',
  'withdrawn',
  'expired',
] as const satisfies readonly HandoffStateFilter[]
/** Recipient kinds the offer supports. A session reference is a real supported kind
 * and is labelled as what it is; no provider conversation id is fabricated for it. */
export const HANDOFF_RECIPIENT_KINDS = [
  'user',
  'agent',
  'session',
] as const satisfies readonly HandoffRecipientKind[]
/** CONSOLE presets for a rejection code, not a backend enum: the engine keeps an
 * open code vocabulary, so the form also admits a custom code. */
export const HANDOFF_REASON_PRESETS = [
  'not_available',
  'outside_scope',
  'needs_information',
  'other',
] as const
/* ── Engine bounds, READ FROM THE ENGINE and not from the form's convenience.
 *    `canonicalReasonContent` and the `PayloadSlotHandoff` arm of
 *    `CanonicalProtectedPayloadSlot` (modules/sessions/communication_state.go) are the
 *    source: a reason code is a bounded TOKEN of at most 128 bytes, optional text is
 *    1..32 KiB, and both the rejection's references and the offer's artifact
 *    references are at most 64 rows whose every field is an opaque ref of 1..512
 *    trimmed bytes with no NUL, CR or LF. The server remains the final validator;
 *    mirroring these here only stops a request that could not possibly succeed. */
export const HANDOFF_REASON_CODE_MAX_BYTES = 128
export const HANDOFF_REASON_TEXT_MAX_BYTES = 32 * 1024
export const HANDOFF_REASON_REFERENCES_MAX = 64
/** `boundedToken`'s own shape (`workTokenRE`): a lowercase token, never free text. */
export const HANDOFF_REASON_CODE_PATTERN = /^[a-z0-9][a-z0-9._-]*$/
/** `maxMessageTextBytes` — the ceiling of summary, next action and risk alike. */
export const HANDOFF_TEXT_MAX_BYTES = 32 * 1024
/** `maxMessageReferences` — the same 64 the rejection reason gets. */
export const HANDOFF_ARTIFACT_REFS_MAX = 64
/** `validateOpaqueRef`: 1..512 bytes, already trimmed, no control characters. */
export const HANDOFF_REFERENCE_FIELD_MAX_BYTES = 512
/** The recipient reference ceiling the document declares (`maxLength: 1024`). */
export const HANDOFF_RECIPIENT_REF_MAX_BYTES = 1024

/** Directory rows the pickers offer. A row in the directory is NOT an eligible
 * recipient of a given Channel: the engine decides that on the send. */
export type RosterMember = components['schemas']['RosterMember']
export type DirectoryAgent = components['schemas']['Agent']

/* ── Closed vocabularies, written once so a form cannot offer a value the contract
 *    does not admit. `satisfies` pins each list to the generated enum: a value the
 *    engine drops or adds breaks compilation here, not on the wire. */
export const CHANNEL_KINDS = [
  'coordination',
  'work',
  'incident',
  'announcement',
  'private',
] as const satisfies readonly NonNullable<ChannelCreateInput['kind']>[]
export const CHANNEL_SENSITIVITIES = [
  'internal',
  'restricted',
] as const satisfies readonly NonNullable<ChannelCreateInput['sensitivity']>[]
export const CONTENT_PROTECTIONS = [
  'storage',
  'application_sealed',
] as const satisfies readonly NonNullable<
  ChannelCreateInput['content_protection']
>[]
export const ACK_POLICIES = [
  'none',
  'each_required',
  'quorum',
] as const satisfies readonly NonNullable<
  ChannelCreateInput['default_ack_policy']
>[]
export const WAKE_POLICIES = [
  'none',
  'primary',
  'all',
  'inherit',
] as const satisfies readonly NonNullable<ChannelCreateInput['default_wake']>[]
export const GRANT_SUBJECT_KINDS = [
  'user',
  'user_group',
  'agent',
  'agent_group',
  'session',
] as const satisfies readonly GrantSubjectKind[]
export const RECIPIENT_KINDS = [
  'user',
  'agent',
  'session',
] as const satisfies readonly RecipientKind[]
export const NOTICE_URGENCIES = [
  'normal',
  'high',
  'critical',
] as const satisfies readonly NoticeUrgency[]
export const CONTENT_BLOCK_TYPES = [
  'text',
  'reference',
  'status',
  'action_ref',
] as const satisfies readonly ContentBlockType[]
export const TEXT_FORMATS = [
  'plain',
  'markdown',
] as const satisfies readonly TextFormat[]

/** Persisted Channel states the administrative catalog can select. */
export const ADMINISTRATION_STATES = [
  'all',
  'active',
  'archived',
] as const satisfies readonly AdministrationState[]
/** PERSISTED grant states the sheet can select — not a temporal predicate. */
export const GRANT_STATE_FILTERS = [
  'active',
  'revoked',
  'expired',
  'all',
] as const satisfies readonly GrantStateFilter[]
/** The Channel states a PATCH may name. `archived` is one-way: the engine refuses
 * any change to an archived Channel. */
export const CHANNEL_PATCH_STATES = [
  'active',
  'archived',
] as const satisfies readonly ChannelPatchState[]
/** The wake policies a CHANNEL may hold: `inherit` is a message-level value the
 * engine refuses on a Channel (`ValidateChannel`: `DefaultWake == WakeInherit`). */
export const CHANNEL_WAKE_POLICIES = [
  'none',
  'primary',
  'all',
] as const satisfies readonly NonNullable<ChannelUpdateInput['default_wake']>[]
/** Engine bounds of the PATCH fields (`ValidateChannel`, communication_state.go). */
export const CHANNEL_NAME_MAX_BYTES = 256
export const CHANNEL_DESCRIPTION_MAX_BYTES = 4096

/** The engine's page ceiling for the catalog and the inbox (`limit` 1..200). */
export const PAGE_LIMIT_MAX = 200
export const PAGE_LIMIT_DEFAULT = 50
export const PAGE_LIMITS = [25, 50, 100, 200] as const
/** Content blocks per notice (`blocks` 1..64). */
export const BLOCKS_MAX = 64
/** Initial grants per Channel (`initial_grants` 1..64). */
export const GRANTS_MAX = 64
