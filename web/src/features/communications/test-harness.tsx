// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared fixtures of the communications tests. Not a test file itself (no `.test.`),
// so the console-route census and the i18n usage guard treat it as source — it calls
// no transport and renders no strings of its own.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderResult } from '@testing-library/react'
import { queryKeys } from '@/lib/api/query'
import type {
  CapabilityAccess,
  CapabilityContext,
  CapabilityPermit,
  NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import type { ReactNode } from 'react'
import type { ChannelAdminOutcome } from './api'
import type { CommunicationsScope } from './boundary'
import type {
  AdministrationItem,
  ChannelCatalogItem,
  Channel,
  ChannelGrant,
  ChannelMutationResult,
  GrantAdministrationItem,
  GrantAdministrationPage,
  HandoffDetail,
  HandoffInboxItem,
  HandoffOfferResult,
  HandoffResponseResult,
  InboxItem,
  ReadResult,
} from './types'

export const WS = '0192f2c0-aaaa-7000-8000-000000000001'
export const WS2 = '0192f2c0-aaaa-7000-8000-000000000002'
export const CHANNEL_ID = '0192f2c0-bbbb-7000-8000-000000000001'
export const DELIVERY_ID = '0192f2c0-cccc-7000-8000-000000000001'
export const MESSAGE_ID = '0192f2c0-dddd-7000-8000-000000000001'
export const USER_A = '0192f2c0-eeee-7000-8000-00000000000a'
export const USER_B = '0192f2c0-eeee-7000-8000-00000000000b'

export function scopeOf(
  over: Partial<CommunicationsScope> = {},
): CommunicationsScope {
  return {
    tenant: 't1',
    epoch: 1,
    principal: USER_A,
    workspace: WS,
    workspaceName: 'Billing',
    key: `${USER_A}|t1|c0|w:${WS}`,
    boundaryKey: `${USER_A}|t1|c0`,
    ...over,
  }
}

export function channelOf(over: Partial<Channel> = {}): Channel {
  return {
    id: CHANNEL_ID,
    tenant_id: 't1',
    workspace_id: WS,
    version: 2,
    slug: 'ops',
    name: 'Ops',
    kind: 'coordination',
    state: 'active',
    sensitivity: 'internal',
    content_protection: 'application_sealed',
    protection_generation: 1,
    default_ack_policy: 'each_required',
    // 30 s, not 0. The engine's own rule is an EQUIVALENCE — `ValidateChannel`
    // (modules/sessions/communication_state.go) refuses a Channel unless
    // `policy == none` ⇔ `timeout == 0` — so `each_required` with a zero timeout is
    // a Channel the engine would never store. I1 never validated the fixture, so the
    // impossible value was harmless there; the I2 configuration form mirrors that
    // rule before sending, and against the old fixture it correctly refused to review
    // ANY edit. The fixture is what was wrong, so the fixture is what changed.
    default_ack_timeout_ms: 30_000,
    default_wake: 'none',
    max_fanout: 1,
    max_automation_depth: 0,
    acl_revision: 1,
    route_revision: 1,
    subscription_revision: 1,
    created_at: '2026-09-06T10:00:00Z',
    updated_at: '2026-09-06T10:00:00Z',
    ...over,
  }
}

export function catalogItemOf(
  over: Partial<ChannelCatalogItem> = {},
): ChannelCatalogItem {
  return {
    ...channelOf(),
    my_access: { read: true, write: true, admin: false },
    ...over,
  }
}

export function readOf(
  over: {
    version?: number
    subject?: string
    text?: string
    blocks?: ReadResult['message']['content']['blocks']
    acknowledged_at?: string | null
    ack_due_at?: string | null
  } = {},
): ReadResult {
  return {
    message: {
      id: MESSAGE_ID,
      version: 1,
      channel_id: CHANNEL_ID,
      thread_id: '0192f2c0-ffff-7000-8000-000000000001',
      state: 'published',
      sender: { kind: 'user', ref: USER_A },
      content: {
        subject: over.subject ?? 'Deploy window',
        blocks: over.blocks ?? [
          {
            type: 'text',
            format: 'plain',
            text: over.text ?? 'Freeze at 18:00',
          },
        ],
      },
      urgency: 'normal',
      ack_policy: 'each_required',
      available_at: '2026-09-06T10:00:00Z',
      published_at: '2026-09-06T10:00:00Z',
    },
    delivery: {
      id: DELIVERY_ID,
      version: over.version ?? 1,
      message_id: MESSAGE_ID,
      recipient: { kind: 'user', ref: USER_B },
      delivery_seq: 1,
      required: true,
      state: over.acknowledged_at ? 'acknowledged' : 'delivered',
      available_at: '2026-09-06T10:00:00Z',
      ack_due_at: over.ack_due_at ?? null,
      acknowledged_at: over.acknowledged_at ?? null,
    },
    fulfillment: {
      state: over.acknowledged_at ? 'fulfilled' : 'pending',
      required: 1,
      acknowledged: over.acknowledged_at ? 1 : 0,
      viable: 1,
      unmet: over.acknowledged_at ? 0 : 1,
    },
  }
}

export function inboxItemOf(
  over: Partial<InboxItem['delivery']> = {},
): InboxItem {
  const r = readOf()
  return { ...r, delivery: { ...r.delivery, ...over } }
}

/* ── I2 fixtures: administrable catalog and grant history ─────────────────────── */

export const GRANT_ID = '0192f2c0-9999-7000-8000-000000000001'

export function grantOf(over: Partial<ChannelGrant> = {}): ChannelGrant {
  return {
    id: GRANT_ID,
    tenant_id: 't1',
    workspace_id: WS,
    version: 1,
    channel_id: CHANNEL_ID,
    subject: { kind: 'user', ref: USER_B },
    can_read: true,
    can_write: false,
    can_admin: false,
    state: 'active',
    generation: 1,
    granted_by: { kind: 'user', ref: USER_A },
    created_at: '2026-09-06T10:00:00Z',
    updated_at: '2026-09-06T10:00:00Z',
    ...over,
  }
}

export function grantItemOf(
  over: Partial<ChannelGrant> = {},
  temporal: GrantAdministrationItem['temporal_state'] = 'active',
): GrantAdministrationItem {
  return { grant: grantOf(over), temporal_state: temporal }
}

/** One page of `GET /channels/{id}/grants`: the channel, its precondition ETag, the
 * observation instant and the stored generations. */
export function grantsPageOf(
  over: {
    channel?: Partial<Channel>
    etag?: string
    items?: GrantAdministrationItem[]
    has_more?: boolean
    continuation?: string
    observed_at?: string
  } = {},
): GrantAdministrationPage {
  const channel = channelOf(over.channel)
  const page: GrantAdministrationPage = {
    channel,
    etag: over.etag ?? `"v${channel.version}"`,
    observed_at: over.observed_at ?? '2026-09-07T00:00:00Z',
    items: over.items ?? [grantItemOf()],
    has_more: over.has_more ?? false,
  }
  if (over.continuation) page.continuation = over.continuation
  return page
}

export function adminItemOf(over: Partial<Channel> = {}): AdministrationItem {
  const channel = channelOf(over)
  return { channel, etag: `"v${channel.version}"` }
}

export function adminOutcomeOf(
  over: {
    channel?: Partial<Channel>
    grant?: ChannelGrant | null
    audit_seq?: number
  } = {},
): ChannelAdminOutcome {
  const channel = channelOf({ version: 3, ...over.channel })
  const result: ChannelMutationResult = {
    channel,
    etag: `"v${channel.version}"`,
    audit_seq: over.audit_seq ?? 11,
  }
  if (over.grant !== undefined) result.grant = over.grant
  return { result, etag: `"v${channel.version}"` }
}

/** A promise the test releases when it decides. */
export function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/**
 * The principal the dispatch guard reads LIVE from the whoami query (the same entry
 * `useAuth` reads). The component tests mock `useAuth().can` to steer the permission
 * a surface OBSERVES; this seed keeps the guard's own RBAC re-check inert for them —
 * a superadmin passes every permission — so those tests keep measuring the observed
 * permission, and the guard's live re-check is measured on its own, with explicit
 * grants, in intent-guard.test.tsx. Component fixtures are not HTTP proof of RBAC.
 */
export function seedPrincipal(
  qc: QueryClient,
  over: { user_id?: string; superadmin?: boolean; grants?: unknown[] } = {},
): void {
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: over.user_id ?? USER_A,
    actor: `user:${over.user_id ?? USER_A}`,
    display_name: 'A',
    superadmin: over.superadmin ?? true,
    grants: over.grants ?? [],
  })
}

export function renderWithQuery(ui: () => ReactNode): {
  qc: QueryClient
  result: RenderResult
  rerender: () => void
} {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  seedPrincipal(qc)
  const result = render(
    <QueryClientProvider client={qc}>{ui()}</QueryClientProvider>,
  )
  return {
    qc,
    result,
    rerender: () =>
      result.rerender(
        <QueryClientProvider client={qc}>{ui()}</QueryClientProvider>,
      ),
  }
}

/* ── G1-B: steering the capability observations of a COMPONENT test ──────────── */

/**
 * The state a component test steers, and what the surface actually asked.
 *
 * ⛔ WHY A DOUBLE HERE AND NOT A REAL RESPONSE. These tests measure what a SURFACE does
 *    with an observation — which control is enabled, which query is allowed to run, which
 *    notice is rendered — exactly as they previously measured what it did with an observed
 *    permission. Whether an actual HTTP body may become that observation is a different
 *    property with its own causals, which run the real `admitCapabilityResult`,
 *    `observeCapability` and permit against real parsed JSON and a real monotonic clock
 *    (`capabilities.causal.test.ts`). Proving the admission rule here, through six
 *    components, would measure it once and assert it thirty times.
 *
 * `asked` and `preflighted` are recorded so a surface test can still prove it asked the
 * EXACT question — the payload table — without owning the transport.
 */
export interface CapabilityDoubleState {
  /** `positive` becomes `reachable` for a surface question and `allowed` for an operation;
   *  `negative` becomes the matching ESTABLISHED refusal (`not_reachable` / `denied`). Any
   *  other value is used verbatim, so a case can ask for `undisclosed`, `checking` or the
   *  local `unknown` by name. */
  access: 'positive' | 'negative' | CapabilityAccess
  /** Per-operation overrides, keyed by the mounted route pattern. */
  byOperation: Record<string, 'positive' | 'negative' | CapabilityAccess>
  /** Whether the preflight returns a permit. */
  permitted: boolean
  /** Every question observed, in call order (re-renders repeat entries). */
  asked: NormalizedCapabilityQuestion[]
  /** Every question preflighted at confirm, in call order. */
  preflighted: NormalizedCapabilityQuestion[]
}

export function capabilityState(): CapabilityDoubleState {
  return {
    access: 'positive',
    byOperation: {},
    permitted: true,
    asked: [],
    preflighted: [],
  }
}

const DOUBLE_CONTEXT: CapabilityContext = {
  principalKind: 'user',
  actor: `user:${USER_A}`,
  tenant: 't1',
  credentialGeneration: 0,
  workspace: WS,
  // The double never moves, so its movement counter never advances either.
  lifetime: 0,
}

/** The module replacement a test file installs with `vi.mock('@/lib/auth/capabilities')`.
 *  The pure exports of the real module are kept by the caller's spread; only the two hooks
 *  are replaced. */
export function capabilityDoubles(state: CapabilityDoubleState) {
  const resolve = (q: NormalizedCapabilityQuestion): CapabilityAccess => {
    const chosen = state.byOperation[q.operation] ?? state.access
    if (chosen === 'positive')
      return q.kind === 'surface' ? 'reachable' : 'allowed'
    if (chosen === 'negative')
      return q.kind === 'surface' ? 'not_reachable' : 'denied'
    return chosen
  }
  const permitFor = (q: NormalizedCapabilityQuestion): CapabilityPermit =>
    Object.freeze({
      context: DOUBLE_CONTEXT,
      question: q,
      deadline: Number.POSITIVE_INFINITY,
      isCurrent: () => true,
      assertCurrent: () => {},
    })
  return {
    useCapability: (q: NormalizedCapabilityQuestion | null) => {
      if (!q)
        return {
          access: 'unknown' as CapabilityAccess,
          permit: null,
          question: null,
        }
      state.asked.push(q)
      const access = resolve(q)
      const positive = access === 'allowed' || access === 'reachable'
      return { access, permit: positive ? permitFor(q) : null, question: q }
    },
    useCapabilityPreflight: () => ({
      context: DOUBLE_CONTEXT,
      live: () => DOUBLE_CONTEXT,
      request: async (q: NormalizedCapabilityQuestion) => {
        state.preflighted.push(q)
        return state.permitted ? permitFor(q) : null
      },
    }),
  }
}

/* ── I3 fixtures: the offer, the content-free page and the protected detail ───── */

export const WORK_ITEM_ID = '0192f2c0-7777-7000-8000-000000000001'
export const HANDOFF_ID = '0192f2c0-8888-7000-8000-000000000001'
export const HANDOFF_DELIVERY_ID = '0192f2c0-cccc-7000-8000-0000000000ff'
export const HANDOFF_ETAG = '"h-v1"'

/** One CONTENT-FREE row of `GET /inbox/handoffs`. It deliberately carries no
 *  `content` field: the fixture cannot let a test assert a summary the real route
 *  never sends. */
export function handoffRowOf(
  over: {
    deliveryId?: string
    handoffId?: string
    workItemId?: string
    state?: HandoffInboxItem['handoff']['state']
    deadline?: string
    deadlineElapsed?: boolean
    observedAt?: string
    to?: HandoffInboxItem['handoff']['to']
    from?: HandoffInboxItem['handoff']['from']
  } = {},
): HandoffInboxItem {
  return {
    carrier: {
      channel_id: CHANNEL_ID,
      delivery_id: over.deliveryId ?? HANDOFF_DELIVERY_ID,
      delivery_version: 1,
      message_id: MESSAGE_ID,
    },
    deadline_elapsed: over.deadlineElapsed ?? false,
    handoff: {
      ack_deadline: over.deadline ?? '2026-09-30T12:00:00Z',
      created_at: '2026-09-10T08:00:00Z',
      etag: HANDOFF_ETAG,
      from: over.from ?? { kind: 'user', ref: USER_A },
      id: over.handoffId ?? HANDOFF_ID,
      state: over.state ?? 'offered',
      to: over.to ?? { kind: 'user', ref: USER_B },
      version: 1,
    },
    observed_at: over.observedAt ?? '2026-09-10T09:00:00Z',
    work_item: {
      id: over.workItemId ?? WORK_ITEM_ID,
      presentation: 'handoff_context',
    },
  }
}

/** The PROTECTED detail of one offer. `offer_context` is the engine's own reading
 *  and is what decides whether a response control exists at all. */
export function handoffDetailOf(
  over: {
    summary?: string
    nextAction?: string
    risk?: string
    artifacts?: NonNullable<HandoffDetail['content']['artifact_refs']>
    etag?: string
    state?: HandoffDetail['handoff']['state']
    offerContext?: HandoffDetail['offer_context']
    deliveryId?: string
    deadlineElapsed?: boolean
    terminalReason?: HandoffDetail['terminal_reason']
    observedAt?: string
  } = {},
): HandoffDetail {
  const row = handoffRowOf({
    deliveryId: over.deliveryId,
    state: over.state,
    deadlineElapsed: over.deadlineElapsed,
    observedAt: over.observedAt,
  })
  const detail: HandoffDetail = {
    carrier: row.carrier,
    content: {
      summary: over.summary ?? 'Deploy freeze needs an owner',
      next_action: over.nextAction ?? 'Confirm the freeze window with platform',
    },
    deadline_elapsed: over.deadlineElapsed ?? false,
    handoff: { ...row.handoff, etag: over.etag ?? HANDOFF_ETAG },
    observed_at: row.observed_at,
    offer_context: over.offerContext ?? 'current',
    work_item: row.work_item,
  }
  if (over.risk !== undefined) detail.content.risk = over.risk
  if (over.artifacts !== undefined)
    detail.content.artifact_refs = over.artifacts
  if (over.terminalReason !== undefined)
    detail.terminal_reason = over.terminalReason
  return detail
}

export function handoffOfferResultOf(
  over: Partial<HandoffOfferResult> = {},
): HandoffOfferResult {
  return {
    audit_seq: 41,
    command_id: '0192f2c0-1111-7000-8000-000000000001',
    delivery_id: HANDOFF_DELIVERY_ID,
    etag: HANDOFF_ETAG,
    event_id: '0192f2c0-2222-7000-8000-000000000001',
    handoff_id: HANDOFF_ID,
    message_id: MESSAGE_ID,
    replayed: false,
    state: 'offered',
    version: 1,
    work_item_id: WORK_ITEM_ID,
    ...over,
  }
}

/**
 * The response receipt of the REAL R45 path: a user-to-user transfer of a
 * never-held vacant generation. The owner epoch advances by one, an Ack exists,
 * and `resulting_lease_fence` is ABSENT — the fixture omits it on purpose, because
 * a "0" here would let a test pass while the screen invented an execution lease.
 */
export function handoffResponseResultOf(
  over: Partial<HandoffResponseResult> = {},
): HandoffResponseResult {
  return {
    ack_id: '0192f2c0-3333-7000-8000-000000000001',
    audit_seq: 42,
    command_id: '0192f2c0-1111-7000-8000-000000000002',
    delivery_id: HANDOFF_DELIVERY_ID,
    etag: '"h-v2"',
    event_id: '0192f2c0-2222-7000-8000-000000000002',
    handoff_id: HANDOFF_ID,
    message_id: MESSAGE_ID,
    owner_epoch: 2,
    replayed: false,
    state: 'accepted',
    version: 2,
    work_item_id: WORK_ITEM_ID,
    ...over,
  }
}

/** The narrow work-item projection the offer host receives from the work adapter. */
export function workItemViewOf(
  over: {
    id?: string
    workspace_id?: string
    title?: string
    status?: string
    owner_kind?: string
    owner_ref?: string
    owner_epoch?: number
    etag?: string | null
  } = {},
) {
  return {
    item: {
      id: over.id ?? WORK_ITEM_ID,
      workspace_id: over.workspace_id ?? WS,
      title: over.title ?? 'Freeze the deploy window',
      status: over.status ?? 'active',
      owner_kind: over.owner_kind ?? 'user',
      owner_ref: over.owner_ref ?? USER_A,
      owner_epoch: over.owner_epoch ?? 1,
    },
    etag: over.etag === undefined ? '"w-v3"' : over.etag,
  }
}
