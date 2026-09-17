// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THREE DOORS, ONE ROOM — and the room is bound to its scope. Measured here: which
// request each door makes under EXACTLY its own permission (and which it never makes
// without the other tiers); that "all workspaces" makes no K3 request at all, with the
// positive control that a selected workspace does; and that a response landing after
// the principal, the tenant, the workspace or the credential moved is neither painted
// nor kept, while the previous partition is cancelled and removed.
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useSessionStore } from '@/stores/session'
import { useWorkspaceStore } from '@/stores/workspace'

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
  tenant: 't1' as string | null,
  principal: 'u-a',
  // The authoritative flag of the whoami payload, steered per case. The permission
  // set is steered separately on purpose: a real global superadmin passes every
  // `can()` (rbac.ts short-circuits on this same flag), and these cases must be able
  // to hold the permissions still while the ACCOUNT changes underneath them.
  superadmin: false,
  /** whoami has not resolved: there is no principal at all, only an absence. */
  unknownPrincipal: false,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: auth.superadmin,
    principal: auth.unknownPrincipal
      ? null
      : {
          kind: 'user',
          user_id: auth.principal,
          actor: `user:${auth.principal}`,
          display_name: 'Ada',
          superadmin: auth.superadmin,
          grants: [],
        },
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
vi.mock('@/lib/hooks/use-url-state', () => ({
  useUrlState: () => [urlState.value, vi.fn()],
}))
const api = vi.hoisted(() => ({
  listChannels: vi.fn(),
  listInbox: vi.fn(),
  getChannel: vi.fn(),
  getDelivery: vi.fn(),
  getMessage: vi.fn(),
  createChannel: vi.fn(),
  sendNotice: vi.fn(),
  ackDelivery: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
  listAdministrableChannels: vi.fn(),
  listChannelGrants: vi.fn(),
  updateChannel: vi.fn(),
  grantChannel: vi.fn(),
  revokeChannelGrant: vi.fn(),
  getCursorToken: vi.fn(),
  advanceCursor: vi.fn(),
  listHandoffInbox: vi.fn(),
  getHandoffDetail: vi.fn(),
  respondToHandoff: vi.fn(),
}))
const urlState = vi.hoisted(() => ({
  value: {} as Record<string, string | undefined>,
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, ...api }
})
const caps = vi.hoisted(() => ({ state: null as unknown }))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  const harness = await import('./test-harness')
  caps.state = harness.capabilityState()
  return {
    ...real,
    ...harness.capabilityDoubles(
      caps.state as ReturnType<typeof capabilityState>,
    ),
  }
})

import { communicationsKeys } from './api'
import { ApiError } from '@/lib/api/errors'
import { workKeys } from '@/features/work/api'
import { CommunicationsView } from './communications-view'
import {
  adminItemOf,
  capabilityState,
  CHANNEL_ID,
  deferred,
  grantsPageOf,
  handoffDetailOf,
  handoffResponseResultOf,
  handoffRowOf,
  HANDOFF_DELIVERY_ID,
  HANDOFF_ETAG,
  inboxItemOf,
  catalogItemOf,
  renderWithQuery,
  WORK_ITEM_ID,
  WS,
  WS2,
} from './test-harness'

const CR = 'sessions:channel:read'
const CW = 'sessions:channel:write'
const CA = 'sessions:channel:admin'
const DR = 'sessions:delivery:read'
const EXP = '2030-01-01T00:00:00Z'

beforeEach(() => {
  auth.perms = new Set()
  auth.tenant = 't1'
  auth.principal = 'u-a'
  auth.superadmin = false
  auth.unknownPrincipal = false
  // ⛔ ADMINISTRATION IS REFUSED BY DEFAULT, and NOT because the permission set is empty:
  //    the door no longer reads the reflection at all. The cases that open it publish a
  //    capability positive while leaving `auth.perms` empty — which is the point, and is
  //    the same thing the live browser journey proves against a real engine.
  Object.assign(caps.state as object, capabilityState(), { access: 'negative' })
  urlState.value = {}
  for (const fn of Object.values(api)) fn.mockReset()
  api.listChannels.mockResolvedValue({ items: [], has_more: false })
  api.listInbox.mockResolvedValue({ items: [], has_more: false })
  api.listHandoffInbox.mockResolvedValue({ items: [], has_more: false })
  api.listAdministrableChannels.mockResolvedValue({
    items: [],
    has_more: false,
  })
  api.listChannelGrants.mockResolvedValue(grantsPageOf())
  useWorkspaceStore.setState({
    activeWorkspace: WS,
    activeWorkspaceName: 'Billing',
  })
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: 'sid',
    expiresAt: EXP,
  })
})

describe('CommunicationsView — three doors, each on its own permission', () => {
  it('the catalog door with ONLY channel:read reads the catalog and never the inbox', async () => {
    auth.perms = new Set([CR])
    renderWithQuery(() => <CommunicationsView entrance="catalog" />)
    expect(
      await screen.findByRole('heading', { name: 'Communications' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(1))
    expect(api.listChannels.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      limit: 50,
      continuation: undefined,
    })
    expect(api.listChannels.mock.calls[0][1]).toEqual({ tenant: 't1' })
    expect(screen.getByRole('tab', { name: 'Channels' })).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Inbox' })).toBeNull()
    expect(screen.queryByRole('tab', { name: 'New channel' })).toBeNull()
    expect(api.listInbox).not.toHaveBeenCalled()
  })

  it('the inbox door with ONLY delivery:read reads the inbox and never the catalog', async () => {
    auth.perms = new Set([DR])
    renderWithQuery(() => <CommunicationsView entrance="inbox" />)
    expect(
      await screen.findByRole('heading', { name: 'Communications inbox' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(1))
    expect(api.listInbox.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      limit: 50,
      continuation: undefined,
    })
    expect(screen.queryByRole('tab', { name: 'Channels' })).toBeNull()
    expect(api.listChannels).not.toHaveBeenCalled()
  })

  it('the create door with ONLY channel:write offers the form with explicit grants and reads nothing', async () => {
    auth.perms = new Set([CW])
    renderWithQuery(() => <CommunicationsView entrance="new" />)
    expect(
      await screen.findByRole('heading', { name: 'New channel' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Create channel' }),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/channel:write without channel:read/),
    ).toBeInTheDocument()
    expect(api.listChannels).not.toHaveBeenCalled()
    expect(api.listInbox).not.toHaveBeenCalled()
  })

  it("the ADMINISTRATION door opens on the ENGINE's admission with an EMPTY permission set, and reads the administrable catalog and never the read catalog, the inbox or a directory", async () => {
    // No `sessions:channel:admin` in the reflection, and the door opens: that is the
    // workspace-scoped authority whoami cannot express.
    expect(auth.perms.has(CA)).toBe(false)
    ;(caps.state as ReturnType<typeof capabilityState>).access = 'positive'
    api.listAdministrableChannels.mockResolvedValue({
      items: [adminItemOf({ name: 'Ops', slug: 'ops' })],
      has_more: false,
    })
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    expect(
      await screen.findByRole('heading', { name: 'Channel administration' }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(api.listAdministrableChannels).toHaveBeenCalledTimes(1),
    )
    expect(api.listAdministrableChannels.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      state: 'all',
      limit: 50,
      continuation: undefined,
    })
    // The tenant AND the admission the read travels under: the collection's own SURFACE
    // question for this exact workspace, never an entity permit of a row in it. The
    // options used to be `{ tenant }` alone — the omission the independent review of
    // c04cb75de1 measured on the wire — so this assertion is the payload table's half of
    // that correction; the dispatch itself is measured in admitted-read-dispatch.test.tsx.
    expect(api.listAdministrableChannels.mock.calls[0][1]).toMatchObject({
      tenant: 't1',
      admission: {
        question: {
          kind: 'surface',
          operation: 'GET /v1/m/sessions/channels/administration',
          workspaceId: WS,
        },
      },
    })
    expect(
      screen.getByRole('tab', { name: 'Administration', selected: true }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Channels' })).toBeNull()
    expect(screen.queryByRole('tab', { name: 'Inbox' })).toBeNull()
    expect(screen.queryByRole('tab', { name: 'New channel' })).toBeNull()
    expect(await screen.findByText('Ops')).toBeInTheDocument()
    expect(api.listChannels).not.toHaveBeenCalled()
    expect(api.listInbox).not.toHaveBeenCalled()
    expect(api.listMembers).not.toHaveBeenCalled()
    expect(api.listAgents).not.toHaveBeenCalled()
    expect(api.getChannel).not.toHaveBeenCalled()
  })

  it('an administrable row opens the sheet through the GRANT HISTORY read, never through the read-tier channel read', async () => {
    ;(caps.state as ReturnType<typeof capabilityState>).access = 'positive'
    api.listAdministrableChannels.mockResolvedValue({
      items: [adminItemOf({ name: 'Ops', slug: 'ops' })],
      has_more: false,
    })
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    const row = await screen.findByText('Ops')
    act(() => {
      row.click()
    })
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(1))
    expect(api.listChannelGrants.mock.calls[0][0]).toBe(CHANNEL_ID)
    expect(api.listChannelGrants.mock.calls[0][1]).toEqual({
      workspace_id: WS,
      state: 'active',
      limit: 50,
      continuation: undefined,
    })
    const sheet = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(
        sheet.querySelector('[data-slot="admin-etag"]')?.textContent?.trim(),
      ).toBe('"v2"'),
    )
    expect(api.getChannel).not.toHaveBeenCalled()
  })

  it('a deep link admin_channel=<id> opens the administrative sheet on mount on the EXACT grant-sheet operation, with the collection REFUSED and never fetched', async () => {
    // The entity permit and the collection admission are different decisions: this is a
    // principal admitted to one row and refused the list, which is the ordinary shape of
    // a scoped grant. The sheet opens; the administration collection is never asked for.
    ;(caps.state as ReturnType<typeof capabilityState>).byOperation[
      'GET /v1/m/sessions/channels/{id}/grants'
    ] = 'positive'
    urlState.value = { admin_channel: CHANNEL_ID }
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(1))
    expect(api.listChannelGrants.mock.calls[0][0]).toBe(CHANNEL_ID)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(api.getChannel).not.toHaveBeenCalled()
    // The surface stayed refused throughout, and nothing asked it for the list.
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
  })

  it('the deep link opens nothing without a positive for its EXACT operation: no grant read leaves and the sheet refuses', async () => {
    auth.perms = new Set([CR])
    urlState.value = { admin_channel: CHANNEL_ID }
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    const sheet = await screen.findByRole('dialog')
    // The sheet is on screen because the url named a channel; it is EMPTY because the
    // engine refused this exact operation, and the read never left.
    expect(
      within(sheet).getByText(
        'The engine refused this for the current principal.',
      ),
    ).toBeInTheDocument()
    expect(api.listChannelGrants).not.toHaveBeenCalled()
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
    // The rest of the room is unaffected: this principal holds channel:read, so the read
    // catalog opened exactly as it did behind the sheet. Refusing ONE administrative
    // operation is not a refusal of the feature. (The tab itself is not queried by role
    // here: the sheet is a MODAL, so Radix marks the content behind it aria-hidden, and
    // that is a fact about the dialog rather than about authority.)
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(1))
  })

  it('"all workspaces" makes NO K3 request and says why; a selected workspace does (positive control)', async () => {
    auth.perms = new Set([CR, DR, CW])
    useWorkspaceStore.setState({
      activeWorkspace: null,
      activeWorkspaceName: null,
    })
    const { rerender } = renderWithQuery(() => (
      <CommunicationsView entrance="catalog" />
    ))
    expect(await screen.findByText('Select a workspace')).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Channels' })).toBeNull()
    expect(api.listChannels).not.toHaveBeenCalled()
    expect(api.listInbox).not.toHaveBeenCalled()
    act(() => {
      useWorkspaceStore.setState({
        activeWorkspace: WS,
        activeWorkspaceName: 'Billing',
      })
    })
    rerender()
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(1))
    expect(api.listChannels.mock.calls[0][0].workspace_id).toBe(WS)
  })
})

describe('CAN3 — the administration question is declared for every account, submitted for one that can be answered', () => {
  // The engine does not scope a `superadmin === true` credential to a tenant, so this
  // tenant self-capability question is answered 503 for it forever. Navigation and the
  // route gate already leave it unsubmitted; this room was still asking. Measured here
  // through the ordinary providers: WHICH question the surface submits, and what that
  // costs the tab and the protected collection. The wire itself — zero requests across
  // the CAN2 twelve-second hold, against a member's unchanged cadence — is measured with
  // the real hook and the real transport in communications-view-global-account.causal.
  const state = () => caps.state as ReturnType<typeof capabilityState>
  const surfaceQuestions = () =>
    state().asked.filter(
      (q) => q.operation === 'GET /v1/m/sessions/channels/administration',
    )

  it('a GLOBAL SUPERADMIN asks it ZERO times, and a positive it never asked for admits nothing', async () => {
    // The double would answer `reachable` to this question. The point is that the room
    // does not put it, so there is no answer to read — suppressing a question grants
    // nothing, and it cannot be mistaken for the permission it never obtained.
    ;(caps.state as ReturnType<typeof capabilityState>).access = 'positive'
    auth.superadmin = true
    auth.perms = new Set([CR])
    renderWithQuery(() => <CommunicationsView entrance="catalog" />)
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(1))

    expect(surfaceQuestions()).toHaveLength(0)
    expect(screen.queryByRole('tab', { name: 'Administration' })).toBeNull()
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
    // The rest of the room is exactly as it was: this is one observation, not a mode.
    expect(screen.getByRole('tab', { name: 'Channels' })).toBeInTheDocument()
  })

  it('THE SAME ROOM, a member account: the EXACT workspace question is asked and its admission opens the door', async () => {
    ;(caps.state as ReturnType<typeof capabilityState>).access = 'positive'
    auth.superadmin = false
    auth.perms = new Set([CR])
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    expect(
      await screen.findByRole('tab', { name: 'Administration' }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(api.listAdministrableChannels).toHaveBeenCalledTimes(1),
    )
    expect(surfaceQuestions()[0]).toEqual({
      kind: 'surface',
      operation: 'GET /v1/m/sessions/channels/administration',
      workspaceId: WS,
      path: [],
      body: [],
    })
  })

  it('AN UNRESOLVED PRINCIPAL IS NOT THIS FAMILY: the question is asked exactly as before', async () => {
    // "I do not know yet" is not "you are a global administrator". Inferring the family
    // from an absent principal would suppress the question for every first render.
    ;(caps.state as ReturnType<typeof capabilityState>).access = 'positive'
    auth.unknownPrincipal = true
    auth.perms = new Set([CR])
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    expect(
      await screen.findByRole('tab', { name: 'Administration' }),
    ).toBeInTheDocument()
    expect(surfaceQuestions().length).toBeGreaterThanOrEqual(1)
  })

  it('EVERY OTHER QUESTION OF THE ROOM IS UNTOUCHED: the administration ENTITY deep link still asks for itself', async () => {
    // The suppressed one is the collection SURFACE observation of this caller. An entity
    // permit is a different decision on a different route, and a global account that
    // follows a valid deep link still asks about that row exactly as it always did.
    ;(caps.state as ReturnType<typeof capabilityState>).byOperation[
      'GET /v1/m/sessions/channels/{id}/grants'
    ] = 'positive'
    auth.superadmin = true
    urlState.value = { admin_channel: CHANNEL_ID }
    renderWithQuery(() => <CommunicationsView entrance="administration" />)
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(1))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(
      state().asked.filter(
        (q) => q.operation === 'GET /v1/m/sessions/channels/{id}/grants',
      ).length,
    ).toBeGreaterThanOrEqual(1)
    expect(surfaceQuestions()).toHaveLength(0)
    expect(api.listAdministrableChannels).not.toHaveBeenCalled()
  })
})

describe('CommunicationsView — the scope ends every read of the previous one', () => {
  const moves: Array<[string, () => void]> = [
    ['principal', () => (auth.principal = 'u-b')],
    ['tenant', () => (auth.tenant = 't2')],
    [
      'workspace',
      () =>
        useWorkspaceStore.setState({
          activeWorkspace: WS2,
          activeWorkspaceName: 'Other',
        }),
    ],
    [
      'same-session credential rotation',
      () =>
        useSessionStore.getState().setSession({
          token: 'olvs_rotated',
          sessionId: 'sid',
          expiresAt: EXP,
        }),
    ],
  ]
  it.each(moves)(
    'a late inbox page after a %s change is not painted, and the old partition is cancelled and removed',
    async (_what, move) => {
      auth.perms = new Set([DR])
      const first = deferred<{ items: unknown[]; has_more: boolean }>()
      let firstSignal: AbortSignal | undefined
      api.listInbox.mockImplementationOnce(
        (_p: unknown, _o: unknown, signal?: AbortSignal) => {
          firstSignal = signal
          return first.promise
        },
      )
      api.listInbox.mockResolvedValue({ items: [], has_more: false })
      const { qc, rerender } = renderWithQuery(() => (
        <CommunicationsView entrance="inbox" />
      ))
      await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(1))
      const oldKey = communicationsKeys.inbox('t1', 1, WS, { limit: 50 })
      const oldEntries = qc
        .getQueryCache()
        .findAll({ queryKey: ['communications', 't1'] })
      expect(oldEntries.length).toBeGreaterThan(0)
      act(() => {
        move()
      })
      rerender()
      // The previous partition is gone before the late answer arrives…
      await waitFor(() => expect(firstSignal?.aborted).toBe(true))
      act(() => {
        first.resolve({
          items: [inboxItemOf({ id: '0192f2c0-cccc-7000-8000-00000000009' })],
          has_more: false,
        })
      })
      await waitFor(() =>
        expect(api.listInbox.mock.calls.length).toBeGreaterThanOrEqual(2),
      )
      // …and it is never painted: no row of the previous scope, no cache entry for it.
      expect(screen.queryByText('Deploy window')).toBeNull()
      expect(qc.getQueryCache().find({ queryKey: oldKey })).toBeUndefined()
      const keys = JSON.stringify(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => q.queryKey),
      )
      expect(keys).not.toContain('olvs')
      expect(keys).not.toContain('sid')
    },
  )

  it('POSITIVE CONTROL: with nothing moving the page is painted', async () => {
    auth.perms = new Set([DR])
    api.listInbox.mockResolvedValue({ items: [inboxItemOf()], has_more: false })
    renderWithQuery(() => <CommunicationsView entrance="inbox" />)
    expect(await screen.findByText('Deploy window')).toBeInTheDocument()
  })

  it("a catalog row opens the channel with the page's own local bits, read fresh, and composing follows the send permission", async () => {
    auth.perms = new Set([CR, 'sessions:message-send:write'])
    api.listChannels.mockResolvedValue({
      items: [catalogItemOf()],
      has_more: false,
    })
    api.getChannel.mockResolvedValue({ channel: catalogItemOf(), etag: '"v2"' })
    const { result } = renderWithQuery(() => (
      <CommunicationsView entrance="catalog" />
    ))
    const row = await screen.findByText('Ops')
    act(() => {
      row.click()
    })
    await waitFor(() => expect(api.getChannel).toHaveBeenCalledTimes(1))
    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByText('"v2"')).toBeInTheDocument()
    expect(
      within(sheet).getByRole('button', { name: /Send notice/ }),
    ).toBeInTheDocument()
    expect(result.container).toBeTruthy()
  })
})

/* ── I3: the FIFTH door of the same room ──────────────────────────────────────── */

describe('CommunicationsView — the handoffs door', () => {
  it('the handoffs door with ONLY delivery:read opens the Handoffs tab and reads the handoff page', async () => {
    auth.perms = new Set([DR])
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    expect(
      await screen.findByRole('heading', { name: 'Handoffs' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(api.listHandoffInbox.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      state: 'offered',
      limit: 50,
      continuation: undefined,
    })
    expect(api.listHandoffInbox.mock.calls[0][1]).toEqual({ tenant: 't1' })
    // The entrance decides the SELECTED tab, and the ordinary inbox stays its own
    // independent tab rather than being replaced by this one.
    const tab = screen.getByRole('tab', { name: 'Handoffs' })
    expect(tab).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tab', { name: 'Inbox' })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    expect(screen.queryByRole('tab', { name: 'Channels' })).toBeNull()
    expect(api.listChannels).not.toHaveBeenCalled()
  })

  // R57/HA4 — THE VIEW→INBOX BOUNDARY, which is where the account fact enters. The
  // authoritative principal says this session is a GLOBAL superadmin; HA3 measured
  // that the engine refuses to scope such a session to a tenant at all, so this
  // collection can only answer it 503. Nothing about authority changes here: the room
  // hands the inbox the fact it already holds, and the inbox stops asking.
  it('a GLOBAL SUPERADMIN keeps the handoffs door and is told which account holds handoffs, with ZERO list requests', async () => {
    auth.superadmin = true
    auth.perms = new Set([DR])
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    expect(
      await screen.findByRole('heading', { name: 'Handoffs' }),
    ).toBeInTheDocument()
    // The entry is NOT hidden and NOT removed: the tab exists and is the selected one.
    const tab = screen.getByRole('tab', { name: 'Handoffs' })
    expect(tab).toHaveAttribute('aria-selected', 'true')
    expect(
      await screen.findByText('Use a member account for personal handoffs'),
    ).toBeVisible()
    expect(
      screen.getByText(/Sign in with a member account for this organization/),
    ).toBeVisible()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
    // …and no other room read was invented to compensate for the one not made.
    expect(api.getHandoffDetail).not.toHaveBeenCalled()
  })

  it('THE SAME ROOM, a member account: the real scoped read is invoked and no guidance appears', async () => {
    auth.superadmin = false
    auth.perms = new Set([DR])
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf()],
      has_more: false,
    })
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(api.listHandoffInbox.mock.calls[0][0]).toEqual({
      workspace_id: WS,
      state: 'offered',
      limit: 50,
      continuation: undefined,
    })
    expect(api.listHandoffInbox.mock.calls[0][1]).toEqual({ tenant: 't1' })
    expect(await screen.findByText(WORK_ITEM_ID)).toBeVisible()
    expect(
      screen.queryByText('Use a member account for personal handoffs'),
    ).not.toBeInTheDocument()
  })

  it('a member account whose read FAILS 503 keeps the typed unknown — the guidance is about the ACCOUNT, not about an error', async () => {
    auth.superadmin = false
    auth.perms = new Set([DR])
    api.listHandoffInbox.mockRejectedValue(
      new ApiError(
        503,
        'evidence_unavailable',
        'evidence_unavailable',
        'req-ha4',
        {},
        { code: 'evidence_unavailable', verdict: 'NO_HE_PODIDO_MIRAR' },
      ),
    )
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/handoff information cannot currently/i)
    expect(
      screen.queryByText('Use a member account for personal handoffs'),
    ).not.toBeInTheDocument()
  })

  it('an UNRESOLVED principal invents no global status: no guidance, and the page behaves as it always has', async () => {
    // Unknown is not a fact about the account. The room may not turn "I do not know
    // yet" into "you are a global administrator" — nor into the opposite claim: it
    // simply is not this case, so nothing about this page changes.
    auth.unknownPrincipal = true
    auth.perms = new Set([DR])
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf()],
      has_more: false,
    })
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(
      screen.queryByText('Use a member account for personal handoffs'),
    ).not.toBeInTheDocument()
    expect(await screen.findByText(WORK_ITEM_ID)).toBeVisible()
  })

  it('the OTHER doors of the room are untouched by the account fact', async () => {
    auth.superadmin = true
    auth.perms = new Set([DR, CR])
    renderWithQuery(() => <CommunicationsView entrance="catalog" />)
    // The catalog door reads its own collection exactly as before, and the ordinary
    // inbox keeps its own tab: only the personal handoff page changed.
    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('tab', { name: 'Inbox' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Handoffs' })).toBeInTheDocument()
    expect(
      screen.queryByText('Use a member account for personal handoffs'),
    ).not.toBeInTheDocument()
  })

  it('EXISTING ENTRANCES KEEP THEIR CURRENT INITIAL TAB: the inbox door still opens the ordinary inbox', async () => {
    auth.perms = new Set([DR])
    renderWithQuery(() => <CommunicationsView entrance="inbox" />)
    expect(
      await screen.findByRole('heading', { name: 'Communications inbox' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Inbox' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('tab', { name: 'Handoffs' })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    // …and it is the ORDINARY inbox that was read, not the handoff page.
    await waitFor(() => expect(api.listInbox).toHaveBeenCalledTimes(1))
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
  })

  it('a deep link handoff=<carrier delivery> opens the protected detail on mount, through the Delivery-bound route', async () => {
    auth.perms = new Set([DR])
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await waitFor(() =>
      expect(api.getHandoffDetail).toHaveBeenCalledWith(
        HANDOFF_DELIVERY_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    expect(
      await screen.findByText('Deploy freeze needs an owner'),
    ).toBeVisible()
    // THE ORDINARY `delivery` KEY KEEPS ITS OWN MEANING: this link opened the
    //    handoff sheet and did NOT drag the plain delivery read along with it.
    expect(api.getDelivery).not.toHaveBeenCalled()
  })

  it('a NON-canonical handoff URL value opens nothing and reads nothing', async () => {
    auth.perms = new Set([DR])
    urlState.value = { handoff: 'not-a-uuid' }
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalled())
    expect(api.getHandoffDetail).not.toHaveBeenCalled()
  })

  it('without delivery:read the handoffs tab does not exist and its page is never fetched', async () => {
    auth.perms = new Set([CR])
    renderWithQuery(() => <CommunicationsView entrance="catalog" />)
    expect(
      await screen.findByRole('heading', { name: 'Communications' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Handoffs' })).toBeNull()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
  })

  it('with NO explicit workspace the handoffs door makes no K3 request at all', async () => {
    auth.perms = new Set([DR])
    useWorkspaceStore.setState({
      activeWorkspace: null,
      activeWorkspaceName: null,
    })
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    expect(await screen.findByText('Select a workspace')).toBeInTheDocument()
    expect(api.listHandoffInbox).not.toHaveBeenCalled()
    expect(api.getHandoffDetail).not.toHaveBeenCalled()
  })

  it('a row opens its Delivery-bound detail, and the response goes to the room-owned operation', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    api.listHandoffInbox.mockResolvedValue({
      items: [handoffRowOf()],
      has_more: false,
    })
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await user.click(await screen.findByText(WORK_ITEM_ID))
    await waitFor(() =>
      expect(api.getHandoffDetail).toHaveBeenCalledWith(
        HANDOFF_DELIVERY_ID,
        { tenant: 't1' },
        expect.any(AbortSignal),
      ),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Accept responsibility' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await waitFor(() => expect(api.respondToHandoff).toHaveBeenCalledTimes(1))
    // The precondition is the handoff's own ETag, taken from the protected read.
    expect(api.respondToHandoff.mock.calls[0][0].etag).toBe(HANDOFF_ETAG)
  })
})

/* ── I3 correction: refresh owners, URL navigation, scope and nested focus ───── */

describe('I3 correction — the mounted room', () => {
  it('a resolved response refreshes the uncached protected detail through its own read owner', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail
      .mockResolvedValueOnce(handoffDetailOf())
      .mockResolvedValueOnce(
        handoffDetailOf({ state: 'accepted', offerContext: 'terminal' }),
      )
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await user.click(
      await screen.findByRole('button', { name: 'Accept responsibility' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await screen.findByText('Responsibility accepted')

    // Invalidating a query key cannot reach a read that lives outside the cache,
    // so the detail's own controller has to be restarted.
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(2))
    await user.keyboard('{Escape}')
    // The refreshed observation is what the sheet now shows.
    expect(await screen.findByText('Already resolved')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Accept responsibility' }),
    ).toBeNull()
  })

  it('a conflicting response also refreshes the detail rather than leaving the old one current', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    api.respondToHandoff.mockRejectedValue(
      new ApiError(409, 'internal', 'conflict'),
    )
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await user.click(
      await screen.findByRole('button', { name: 'Accept responsibility' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await screen.findByText('The response did not apply')
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(2))
  })

  it('invalidates only the captured tenant WorkItem families, never a sibling family or another tenant', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    const { qc } = renderWithQuery(() => (
      <CommunicationsView entrance="handoffs" />
    ))
    const seeded: [readonly unknown[], string][] = [
      [workKeys.items('t1', { limit: 100 }), 'items t1'],
      [workKeys.item('t1', WORK_ITEM_ID), 'item t1 target'],
      [workKeys.item('t1', 'another-item'), 'item t1 other'],
      [workKeys.lease('t1', WORK_ITEM_ID), 'lease t1'],
      [workKeys.events('t1', WORK_ITEM_ID), 'events t1'],
      [workKeys.items('t2', { limit: 100 }), 'items t2'],
    ]
    for (const [key] of seeded) qc.setQueryData(key, 'seeded')

    await user.click(
      await screen.findByRole('button', { name: 'Accept responsibility' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    await screen.findByText('Responsibility accepted')

    const invalidated = (key: readonly unknown[]) =>
      qc.getQueryState(key)?.isInvalidated === true
    await waitFor(() =>
      expect(invalidated(workKeys.items('t1', { limit: 100 }))).toBe(true),
    )
    expect(invalidated(workKeys.item('t1', WORK_ITEM_ID))).toBe(true)
    expect(invalidated(workKeys.item('t1', 'another-item'))).toBe(false)
    expect(invalidated(workKeys.lease('t1', WORK_ITEM_ID))).toBe(false)
    expect(invalidated(workKeys.events('t1', WORK_ITEM_ID))).toBe(false)
    expect(invalidated(workKeys.items('t2', { limit: 100 }))).toBe(false)
  })

  it('never presents the receipt owner epoch as current ownership', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    api.respondToHandoff.mockResolvedValue({
      result: handoffResponseResultOf(),
      replayed: false,
      etag: '"h-v2"',
    })
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await user.click(
      await screen.findByRole('button', { name: 'Accept responsibility' }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'Review response' }),
    )
    await user.click(screen.getByRole('button', { name: 'Confirm acceptance' }))
    const receipt = await screen.findByLabelText('Receipt')
    expect(
      within(receipt).getByText(/Read the work item for current ownership/i),
    ).toBeVisible()
    // No lease is claimed on the R45 vacant-generation path.
    expect(within(receipt).queryByText('Resulting lease fence')).toBeNull()
  })

  // This suite replaces `useUrlState` with a mutable object, so it measures the
  // room's reaction to a CHANGED URL VALUE, not a router navigation. Real back and
  // forward through the router's own history are measured in
  // handoff-router-navigation.test.tsx, which replaces neither the hook nor the
  // router.
  it('re-selects and re-reads when the validated handoff URL VALUE changes while mounted', async () => {
    auth.perms = new Set([DR])
    const second = '0192f2c0-cccc-7000-8000-0000000000ee'
    api.getHandoffDetail
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'First delivery' }))
      .mockResolvedValueOnce(
        handoffDetailOf({ deliveryId: second, summary: 'Second delivery' }),
      )
    const { rerender } = renderWithQuery(() => (
      <CommunicationsView entrance="handoffs" />
    ))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalledTimes(1))
    expect(api.getHandoffDetail).not.toHaveBeenCalled()

    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    rerender()
    expect(await screen.findByText('First delivery')).toBeVisible()

    // A later value for the same key; the selection has to follow it.
    urlState.value = { handoff: second }
    rerender()
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(2))
    expect(api.getHandoffDetail.mock.calls[1][0]).toBe(second)
    expect(await screen.findByText('Second delivery')).toBeVisible()
    expect(document.body.textContent).not.toContain('First delivery')

    // Leaving the key selects nothing and clears the protected read.
    urlState.value = {}
    rerender()
    await waitFor(() =>
      expect(document.body.textContent).not.toContain('Second delivery'),
    )
  })

  it('a malformed handoff URL selects nothing and reads nothing', async () => {
    auth.perms = new Set([DR])
    const { rerender } = renderWithQuery(() => (
      <CommunicationsView entrance="handoffs" />
    ))
    await waitFor(() => expect(api.listHandoffInbox).toHaveBeenCalled())
    urlState.value = { handoff: 'not-a-uuid' }
    rerender()
    await Promise.resolve()
    expect(api.getHandoffDetail).not.toHaveBeenCalled()
  })

  it('a real workspace A→B→A rejects the late first read and keeps the current one', async () => {
    auth.perms = new Set([DR])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    const slowA = deferred<ReturnType<typeof handoffDetailOf>>()
    api.getHandoffDetail
      .mockReturnValueOnce(slowA.promise)
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'B current' }))
      .mockResolvedValueOnce(handoffDetailOf({ summary: 'New A current' }))
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    await waitFor(() => expect(api.getHandoffDetail).toHaveBeenCalledTimes(1))

    act(() => {
      useWorkspaceStore.setState({ activeWorkspace: WS2 })
    })
    expect(await screen.findByText('B current')).toBeVisible()
    act(() => {
      useWorkspaceStore.setState({ activeWorkspace: WS })
    })
    expect(await screen.findByText('New A current')).toBeVisible()

    await act(async () => {
      slowA.resolve(handoffDetailOf({ summary: 'Old A protected' }))
      await Promise.resolve()
    })
    expect(document.body.textContent).toContain('New A current')
    expect(document.body.textContent).not.toContain('Old A protected')
  })

  it('returns focus from the nested response dialog to the sheet control that opened it', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail.mockResolvedValue(handoffDetailOf())
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    const opener = await screen.findByRole('button', { name: 'Reject handoff' })
    await user.click(opener)
    await screen.findByRole('dialog', { name: 'Reject handoff' })
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Reject handoff' }),
      ).toBeNull(),
    )
    // Settled, not a synchronous pre-animation snapshot.
    await waitFor(() => expect(document.activeElement).toBe(opener), {
      timeout: 1000,
    })
  })

  it('falls back to a visible sheet control when the opener is gone on close', async () => {
    auth.perms = new Set([DR, 'sessions:handoff-response:write'])
    urlState.value = { handoff: HANDOFF_DELIVERY_ID }
    api.getHandoffDetail
      .mockResolvedValueOnce(handoffDetailOf())
      // The reread returns a terminal offer, so the opener disappears.
      .mockResolvedValue(
        handoffDetailOf({ state: 'withdrawn', offerContext: 'terminal' }),
      )
    const user = userEvent.setup()
    renderWithQuery(() => <CommunicationsView entrance="handoffs" />)
    const opener = await screen.findByRole('button', { name: 'Reject handoff' })
    await user.click(opener)
    await screen.findByRole('dialog', { name: 'Reject handoff' })
    // Remove the opener while the nested dialog is open. The sheet is behind an
    // aria-hidden barrier at this point, so its control is reached through the DOM
    // rather than by role — this is test setup, not the behavior under test.
    const reread = [...document.querySelectorAll('button')].find((b) =>
      b.textContent?.includes('Re-read'),
    ) as HTMLButtonElement
    fireEvent.click(reread)
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Reject handoff' }),
      ).toBeNull(),
    )
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Reject handoff' }),
      ).toBeNull(),
    )
    await waitFor(
      () => {
        const active = document.activeElement as HTMLElement | null
        expect(active).not.toBe(document.body)
        expect(active?.textContent).toMatch(/Re-read/)
      },
      { timeout: 1000 },
    )
  })
})
