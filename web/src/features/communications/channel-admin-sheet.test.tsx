// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The administrative sheet reads through the GRANT HISTORY (never the read-tier
// channel read), sends a PATCH with only the changed fields under the ETag it read,
// treats a 409/412/428 as "re-read and confirm again" with nothing re-sent, revokes
// the EXACT generation shown and grants a successor as a SECOND act under the
// re-read's ETag, reports an expired active row as expired-not-revoked, restarts a
// listing whose channel moved between pages, and clears everything on a refusal.
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import type { ChannelAdminOutcome } from './api'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
import { toast } from '@/components/ui/toaster'
const api = vi.hoisted(() => ({
  listChannelGrants: vi.fn(),
  updateChannel: vi.fn(),
  grantChannel: vi.fn(),
  revokeChannelGrant: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
  getChannel: vi.fn(),
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

import { ChannelAdminContinuity } from './channel-admin-continuity'
import { ChannelAdminSheet } from './channel-admin-sheet'
import { diffChannel } from './channel-config-form'
import type { GrantIntent, RevokeIntent, UpdateChannelIntent } from './intent'
import {
  adminOutcomeOf,
  deferred,
  capabilityState,
  CHANNEL_ID,
  channelOf,
  GRANT_ID,
  grantItemOf,
  grantOf,
  grantsPageOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  USER_B,
  WS,
  WS2,
} from './test-harness'
import './i18n'

/** The Channel ETag the sheet header shows: the precondition of the read on screen. */
const etagOf = (sheet: HTMLElement) =>
  sheet.querySelector('[data-slot="admin-etag"]')?.textContent?.trim() ?? ''
async function readEtag(sheet: HTMLElement, etag: string) {
  await waitFor(() => expect(etagOf(sheet)).toBe(etag))
}

const gid = (n: number) =>
  `0192f2c0-9999-7000-8000-${String(n).padStart(12, '0')}`

function mount(
  over: { canAdmin?: boolean; me?: string | null; onMutated?: () => void } = {},
) {
  const onMutated = over.onMutated ?? vi.fn()
  const onOpenChange = vi.fn()
  // The sheet's authority is now three capability observations rather than one boolean
  // prop. `canAdmin: false` keeps meaning "this principal may not administer here", and
  // the case that used it (the read is refused) is an ESTABLISHED refusal of the exact
  // grant-sheet operation — which is what makes the existing Forbidden assertion right.
  if (over.canAdmin === false)
    (caps.state as ReturnType<typeof capabilityState>).access = 'denied'
  // The ROUTE's continuity boundary, in the position the gate mounts it: above the sheet
  // and outside everything the sheet's own answer tears down. These cases measure what the
  // SHEET does with it — publishing, taking and ending an opening — while
  // channel-admin-continuity.test.tsx measures the composition with the real gate.
  const r = renderWithQuery(() => (
    <ChannelAdminContinuity admitted access="allowed">
      <ChannelAdminSheet
        open
        onOpenChange={onOpenChange}
        channelId={CHANNEL_ID}
        scope={scopeOf()}
        canUserRead={false}
        canAgentRead={false}
        me={{ userId: over.me === undefined ? USER_A : over.me, label: 'A' }}
        onMutated={onMutated}
      />
    </ChannelAdminContinuity>
  ))
  return { ...r, onMutated, onOpenChange }
}

const conflict412 = () =>
  new ApiError(
    412,
    'version_mismatch',
    'stale',
    'req-12',
    {},
    { verdict: 'BROKEN', code: 'version_mismatch' },
  )

beforeEach(() => {
  // Every capability positive unless a case says otherwise: these tests are about what
  // the sheet DOES with an observation, not about how one is established.
  Object.assign(caps.state as object, capabilityState())
  for (const fn of Object.values(api)) fn.mockReset()
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
  // A TERMINAL default behind every `once`: a mock that runs dry resolves
  // `undefined`, and the real client never resolves undefined for a 200. Without
  // it an extra re-read fabricates a page shape the engine cannot produce.
  api.listChannelGrants.mockResolvedValue(grantsPageOf())
})

async function openGrantsTab(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('tab', { name: 'Grants' }))
}

describe('ChannelAdminSheet — configuration (PATCH under the read ETag)', () => {
  it('reads through the grant history only, seeds the form, and sends ONLY the changed fields with If-Match of that read; then re-reads', async () => {
    api.listChannelGrants.mockResolvedValueOnce(grantsPageOf())
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({ channel: { name: 'Ops renamed', version: 3 } }),
    )
    api.updateChannel.mockResolvedValue(
      adminOutcomeOf({ channel: { name: 'Ops renamed', version: 3 } }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    expect(api.getChannel).not.toHaveBeenCalled()
    const user = userEvent.setup()
    const name = within(sheet).getByLabelText(/^Name/)
    expect(name).toHaveValue('Ops')
    await user.clear(name)
    await user.type(name, 'Ops renamed')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    const confirm = within(sheet).getByRole('region', {
      name: 'Confirm the configuration change',
    })
    expect(within(confirm).getByText('Ops renamed')).toBeInTheDocument()
    expect(within(confirm).getAllByText('"v2"').length).toBeGreaterThan(0)
    expect(api.updateChannel).not.toHaveBeenCalled()
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.etag).toBe('"v2"')
    expect(intent.channelId).toBe(CHANNEL_ID)
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, name: 'Ops renamed' })
    expect(
      await within(sheet).findByText('Configuration applied'),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v3"')
  })

  it('a 412 is a CONFLICT: nothing re-sent, the intent dies, re-read yields the new ETag and a NEW confirmation carries it', async () => {
    api.listChannelGrants.mockResolvedValueOnce(grantsPageOf())
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({ channel: { version: 5 } }),
    )
    api.updateChannel.mockRejectedValueOnce(conflict412())
    api.updateChannel.mockResolvedValueOnce(
      adminOutcomeOf({ channel: { version: 6 } }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const name = within(sheet).getByLabelText(/^Name/)
    await user.clear(name)
    await user.type(name, 'First try')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    expect(
      await within(sheet).findByText('The channel changed since you read it'),
    ).toBeInTheDocument()
    expect(sheet.querySelector('[data-slot="config-conflict"]')).not.toBeNull()
    expect(api.updateChannel).toHaveBeenCalledTimes(1)
    await user.click(
      within(sheet).getByRole('button', { name: 'Re-read and edit again' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v5"')
    // Still one request: the console did not swap the ETag and re-send.
    expect(api.updateChannel).toHaveBeenCalledTimes(1)
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(2))
    const second = api.updateChannel.mock.calls[1][0] as UpdateChannelIntent
    expect(second.etag).toBe('"v5"')
    expect(second.body).toEqual({ channel_id: CHANNEL_ID, name: 'First try' })
  })

  it('an ARCHIVED channel: the state cannot change, but other configuration still can', async () => {
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ channel: { state: 'archived' } }),
    )
    api.updateChannel.mockResolvedValue(
      adminOutcomeOf({ channel: { state: 'archived', description: 'kept' } }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    expect(
      within(sheet).getByText(/This channel is archived/),
    ).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    expect(
      within(sheet).getByRole('combobox', { name: 'State' }),
    ).toBeDisabled()
    const description = within(sheet).getByLabelText('Description')
    await user.type(description, 'kept')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, description: 'kept' })
    expect(intent.body).not.toHaveProperty('state')
  })

  it('archiving an active channel is confirmed as a ONE-WAY transition, in its own words', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    api.updateChannel.mockResolvedValue(
      adminOutcomeOf({ channel: { state: 'archived' } }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    await user.click(within(sheet).getByRole('combobox', { name: 'State' }))
    await user.click(await screen.findByRole('option', { name: 'Archived' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    expect(
      within(sheet).getByText(
        /Archiving is one-way: it will not return to active/,
      ),
    ).toBeInTheDocument()
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm and archive' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, state: 'archived' })
  })

  it("the engine's rules are applied before a request leaves: restricted without application-sealed, a lowered protection, an empty name", async () => {
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({
        channel: { sensitivity: 'internal', content_protection: 'storage' },
      }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    await user.click(
      within(sheet).getByRole('combobox', { name: 'Sensitivity' }),
    )
    await user.click(await screen.findByRole('option', { name: 'Restricted' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    expect(
      await within(sheet).findByText(
        'A restricted channel must be application-sealed.',
      ),
    ).toBeInTheDocument()
    expect(api.updateChannel).not.toHaveBeenCalled()
    const name = within(sheet).getByLabelText(/^Name/)
    await user.clear(name)
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    expect(
      await within(sheet).findByText('The name must be 1 to 256 bytes.'),
    ).toBeInTheDocument()
    expect(api.updateChannel).not.toHaveBeenCalled()
    // The diff never carries an untouched field, and a lowered protection is refused
    // by the option itself (disabled) — measured on the pure function here.
    const sealed = channelOf({ content_protection: 'application_sealed' })
    expect(
      diffChannel(sealed, {
        name: sealed.name,
        description: '',
        state: 'active',
        sensitivity: 'internal',
        protection: 'application_sealed',
        ackPolicy: 'each_required',
        ackTimeout: '30000',
        wake: 'none',
        retention: '',
        maxFanout: '1',
        maxDepth: '0',
      }).changed,
    ).toEqual([])
  })

  // ─── IR-I2-2: a fresh base must never turn an untouched field into a rollback ───
  it('IR-I2-2 — name-only dirty + a remote max_fanout 5→9 and a new ETag: the confirmation sends ONLY name, never max_fanout 5', async () => {
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({ channel: { max_fanout: 5, version: 2 } }),
    )
    // The focus/explicit re-read: another operator moved a field this one never
    // touched, and the ETag advanced, which is what made the rollback CAS-valid.
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ channel: { max_fanout: 9, version: 3 } }),
    )
    api.updateChannel.mockResolvedValue(
      adminOutcomeOf({ channel: { max_fanout: 9, version: 4 } }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const name = within(sheet).getByLabelText(/^Name/)
    await user.clear(name)
    await user.type(name, 'Ops renamed')
    // The base moves underneath the edit.
    await user.click(within(sheet).getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v3"')
    // The operator's edit is preserved, the untouched field is REBASED to what the
    // engine now holds, and the form says the base moved.
    expect(within(sheet).getByLabelText(/^Name/)).toHaveValue('Ops renamed')
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    expect(within(sheet).getByLabelText('Max fanout')).toHaveValue('9')
    expect(sheet.querySelector('[data-slot="config-rebased"]')).not.toBeNull()
    // A NEW review, against the new base, sends the operator's field ONLY.
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.etag).toBe('"v3"')
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, name: 'Ops renamed' })
    expect(intent.body).not.toHaveProperty('max_fanout')
  })

  it('IR-I2-2 POSITIVE CONTROL — an operator who edits BOTH fields still sends both, under the current ETag', async () => {
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ channel: { max_fanout: 5, version: 2 } }),
    )
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const name = within(sheet).getByLabelText(/^Name/)
    await user.clear(name)
    await user.type(name, 'Ops renamed')
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    const fanout = within(sheet).getByLabelText('Max fanout')
    await user.clear(fanout)
    await user.type(fanout, '7')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.body).toEqual({
      channel_id: CHANNEL_ID,
      name: 'Ops renamed',
      max_fanout: 7,
    })
  })

  it('IR-I2-2 — a field the operator DID touch is kept across a re-read, and a remote change to it does not silently win', async () => {
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({ channel: { max_fanout: 5, version: 2 } }),
    )
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ channel: { max_fanout: 9, version: 3 } }),
    )
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    await user.click(
      within(sheet).getByRole('button', { name: 'Advanced options' }),
    )
    const fanout = within(sheet).getByLabelText('Max fanout')
    await user.clear(fanout)
    await user.type(fanout, '7')
    await user.click(within(sheet).getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v3"')
    // The operator's own value survives the rebase; it is their intention.
    expect(within(sheet).getByLabelText('Max fanout')).toHaveValue('7')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.etag).toBe('"v3"')
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, max_fanout: 7 })
  })

  it('a lost PATCH response is "result pending verification": the request is shown frozen, re-reading is offered, nothing is re-sent', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    api.updateChannel.mockRejectedValueOnce(new NetworkError('gone'))
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const name = within(sheet).getByLabelText(/^Name/)
    await user.clear(name)
    await user.type(name, 'Maybe')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    expect(
      await within(sheet).findByText('Result pending verification'),
    ).toBeInTheDocument()
    const unknown = sheet.querySelector('[data-slot="config-unknown"]')
    expect(unknown).not.toBeNull()
    expect(
      within(unknown as HTMLElement).getByText('Maybe'),
    ).toBeInTheDocument()
    expect(within(sheet).queryByRole('button', { name: /retry/i })).toBeNull()
    await user.click(
      within(sheet).getByRole('button', { name: 'Re-read to verify' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    expect(api.updateChannel).toHaveBeenCalledTimes(1)
  })
})

describe('ChannelAdminSheet — grants (history, revoke → grant as two acts)', () => {
  it('shows stored state beside the temporal state: an active row past its expiry reads expired, is NOT called revoked, and still needs an explicit revocation', async () => {
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({
        items: [grantItemOf({ expires_at: '2026-09-06T23:00:00Z' }, 'expired')],
        observed_at: '2026-09-07T00:00:00Z',
      }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    const row = await within(sheet).findByRole('row', {
      name: new RegExp(USER_B),
    })
    expect(within(row).getByText('Active')).toBeInTheDocument()
    expect(within(row).getByText('Expired')).toBeInTheDocument()
    expect(within(row).queryByText('Revoked')).toBeNull()
    await user.click(row)
    const detail = within(sheet).getByRole('region', {
      name: 'Generation detail',
    })
    expect(within(detail).getByText(GRANT_ID)).toBeInTheDocument()
    expect(
      within(detail).getByText(/The engine did not revoke it/),
    ).toBeInTheDocument()
    expect(
      within(detail).getByRole('button', { name: 'Revoke this generation' }),
    ).toBeInTheDocument()
    expect(within(sheet).getByText(/Observed at/)).toBeInTheDocument()
  })

  it('revoke → grant is TWO acts: the exact generation id under the read ETag, then the successor under the RE-READ ETag after the result was read', async () => {
    api.listChannelGrants.mockResolvedValueOnce(grantsPageOf())
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({
        channel: { version: 3 },
        items: [grantItemOf({ state: 'revoked' }, 'revoked')],
      }),
    )
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({
        channel: { version: 4 },
        items: [
          grantItemOf({ state: 'revoked' }, 'revoked'),
          grantItemOf({ id: gid(2), generation: 2, supersedes_id: GRANT_ID }),
        ],
      }),
    )
    api.revokeChannelGrant.mockResolvedValue(
      adminOutcomeOf({
        channel: { version: 3 },
        grant: grantOf({ state: 'revoked' }),
      }),
    )
    api.grantChannel.mockResolvedValue(
      adminOutcomeOf({
        channel: { version: 4 },
        grant: grantOf({ id: gid(2), generation: 2, supersedes_id: GRANT_ID }),
      }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('row', { name: new RegExp(USER_B) }),
    )
    await user.click(
      within(sheet).getByRole('button', {
        name: 'Revoke, then grant a successor',
      }),
    )
    const revoke = within(sheet).getByRole('region', {
      name: 'Revoke this exact generation',
    })
    expect(within(revoke).getByText(GRANT_ID)).toBeInTheDocument()
    expect(within(revoke).getByText('"v2"')).toBeInTheDocument()
    expect(api.revokeChannelGrant).not.toHaveBeenCalled()
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm revocation' }),
    )
    await waitFor(() => expect(api.revokeChannelGrant).toHaveBeenCalledTimes(1))
    const r = api.revokeChannelGrant.mock.calls[0][0] as RevokeIntent
    expect(r.grantId).toBe(GRANT_ID)
    expect(r.etag).toBe('"v2"')
    expect(
      await within(sheet).findByText('Generation revoked'),
    ).toBeInTheDocument()
    // The history is re-read before the successor can be confirmed.
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v3"')
    expect(api.grantChannel).not.toHaveBeenCalled()
    await user.click(
      within(sheet).getByRole('button', {
        name: 'Grant a successor (separate act)',
      }),
    )
    const form = within(sheet).getByRole('form', {
      name: 'New grant generation',
    })
    expect(
      within(form).getByRole('textbox', { name: 'Reference (ID)' }),
    ).toHaveValue(USER_B)
    expect(within(form).getByRole('checkbox', { name: 'Read' })).toBeChecked()
    await user.click(within(form).getByRole('checkbox', { name: 'Write' }))
    await user.click(within(form).getByRole('button', { name: 'Review grant' }))
    const confirm = within(sheet).getByRole('region', {
      name: 'Confirm the grant',
    })
    expect(within(confirm).getByText('"v3"')).toBeInTheDocument()
    expect(
      within(confirm).getByText(
        new RegExp(`Successor of revoked generation ${GRANT_ID}`),
      ),
    ).toBeInTheDocument()
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm grant' }),
    )
    await waitFor(() => expect(api.grantChannel).toHaveBeenCalledTimes(1))
    const g = api.grantChannel.mock.calls[0][0] as GrantIntent
    expect(g.etag).toBe('"v3"')
    expect(g.body).toEqual({
      subject: { kind: 'user', ref: USER_B },
      can_read: true,
      can_write: true,
      can_admin: false,
    })
    expect(await within(sheet).findByText('Grant recorded')).toBeInTheDocument()
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(3))
    await readEtag(sheet, '"v4"')
  })

  it('a 409/412 on the revoke re-sends nothing: the intent dies and re-reading the history is the only way on', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    api.revokeChannelGrant.mockRejectedValueOnce(
      new ApiError(
        409,
        'internal',
        'conflict',
        'req-7',
        {},
        { error: { message: 'conflict' } },
      ),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('row', { name: new RegExp(USER_B) }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Revoke this generation' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm revocation' }),
    )
    expect(
      await within(sheet).findByText('The channel changed since you read it'),
    ).toBeInTheDocument()
    expect(sheet.querySelector('[data-slot="act-conflict"]')).not.toBeNull()
    expect(
      within(sheet).queryByRole('button', { name: 'Confirm revocation' }),
    ).toBeNull()
    await user.click(
      within(sheet).getByRole('button', { name: 'Re-read the history' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    expect(api.revokeChannelGrant).toHaveBeenCalledTimes(1)
  })

  it('a lost grant response creates NO second generation: pending verification, re-read, no retry button', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf({ items: [] }))
    api.grantChannel.mockRejectedValueOnce(new NetworkError('dropped'))
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('button', { name: 'Add grant' }),
    )
    const form = within(sheet).getByRole('form', {
      name: 'New grant generation',
    })
    await user.type(
      within(form).getByRole('textbox', { name: 'Reference (ID)' }),
      USER_B,
    )
    await user.click(within(form).getByRole('checkbox', { name: 'Admin' }))
    await user.click(within(form).getByRole('button', { name: 'Review grant' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm grant' }),
    )
    expect(
      await within(sheet).findByText('Result pending verification'),
    ).toBeInTheDocument()
    const unknown = sheet.querySelector(
      '[data-slot="act-unknown"]',
    ) as HTMLElement
    expect(within(unknown).getByText(`user:${USER_B}`)).toBeInTheDocument()
    expect(within(sheet).queryByRole('button', { name: /retry/i })).toBeNull()
    await user.click(
      within(sheet).getByRole('button', { name: 'Re-read to verify' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    expect(api.grantChannel).toHaveBeenCalledTimes(1)
  })

  // C32. The transport did NOT fail here: the engine answered, with 503 and the
  // code that says it issued COMMIT and never learned the result. That is the
  // same situation as a dropped response and it must reach the same screen — the
  // intent stays on the operator's screen and nothing is re-sent. Before the
  // console arm exists this 503 is classified `unavailable`, which renders the
  // refusal screen and CLEARS the intent, so this control is red by its
  // assertion on the retained subject rather than by a missing element.
  it('a 503 commit_outcome_unknown keeps the grant intent: the engine said it does not know, not that it refused', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf({ items: [] }))
    api.grantChannel.mockRejectedValueOnce(
      new ApiError(503, 'commit_outcome_unknown', 'commit_outcome_unknown', 'req-c32', {}, {
        code: 'commit_outcome_unknown',
        error: {
          code: 'commit_outcome_unknown',
          message: 'commit_outcome_unknown',
        },
        verdict: 'NO_HE_PODIDO_MIRAR',
      }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('button', { name: 'Add grant' }),
    )
    const form = within(sheet).getByRole('form', {
      name: 'New grant generation',
    })
    await user.type(
      within(form).getByRole('textbox', { name: 'Reference (ID)' }),
      USER_B,
    )
    await user.click(within(form).getByRole('checkbox', { name: 'Admin' }))
    await user.click(within(form).getByRole('button', { name: 'Review grant' }))
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm grant' }),
    )
    expect(
      await within(sheet).findByText('Result pending verification'),
    ).toBeInTheDocument()
    const unknown = sheet.querySelector(
      '[data-slot="act-unknown"]',
    ) as HTMLElement
    expect(unknown).not.toBeNull()
    // The intent is RETAINED: the subject the operator chose is still named, so
    // they can verify and decide rather than start again from memory.
    expect(within(unknown).getByText(`user:${USER_B}`)).toBeInTheDocument()
    // And the refusal screen is NOT the one shown: a refusal would mean the
    // engine decided against the act, which it did not.
    expect(sheet.querySelector('[data-slot="act-refused"]')).toBeNull()
    // No retry button: re-sending is the operator's decision, never the console's.
    expect(within(sheet).queryByRole('button', { name: /retry/i })).toBeNull()
    await user.click(
      within(sheet).getByRole('button', { name: 'Re-read to verify' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    expect(api.grantChannel).toHaveBeenCalledTimes(1)
  })

  it("revoking the operator's OWN admin generation warns that it may sustain their access, without computing their authority", async () => {
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({
        items: [
          grantItemOf({
            subject: { kind: 'user', ref: USER_A },
            can_admin: true,
            can_read: true,
          }),
        ],
      }),
    )
    mount({ me: USER_A })
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('row', { name: new RegExp(USER_A) }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Revoke this generation' }),
    )
    expect(sheet.querySelector('[data-slot="revoke-own-admin"]')).not.toBeNull()
    expect(
      within(sheet).getByText(
        /may be what sustains your administrative access/,
      ),
    ).toBeInTheDocument()
    expect(api.revokeChannelGrant).not.toHaveBeenCalled()
  })

  it('a 409 channel_snapshot_changed between two pages discards EVERY page and restarts with no continuation', async () => {
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({
        items: [grantItemOf({ id: gid(1), generation: 1 })],
        has_more: true,
        continuation: 'c3g1.page-two',
      }),
    )
    api.listChannelGrants.mockRejectedValueOnce(
      new ApiError(
        409,
        'channel_snapshot_changed',
        'moved',
        'req-9',
        {},
        { verdict: 'BROKEN', code: 'channel_snapshot_changed' },
      ),
    )
    api.listChannelGrants.mockResolvedValueOnce(
      grantsPageOf({
        channel: { version: 3 },
        items: [
          grantItemOf(
            { id: gid(1), generation: 1, state: 'revoked' },
            'revoked',
          ),
          grantItemOf({ id: gid(2), generation: 2 }),
        ],
        has_more: false,
      }),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await within(sheet).findByText('More generations exist beyond this page')
    await user.click(within(sheet).getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(3))
    expect(api.listChannelGrants.mock.calls[1][1].continuation).toBe(
      'c3g1.page-two',
    )
    // The restart: first page again, NO continuation, and the notice says so.
    expect(api.listChannelGrants.mock.calls[2][1].continuation).toBeUndefined()
    expect(
      await within(sheet).findByText(/restarted from the beginning/),
    ).toBeInTheDocument()
    await readEtag(sheet, '"v3"')
    const rows = within(sheet).getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(
      within(sheet).queryByText('More generations exist beyond this page'),
    ).toBeNull()
  })

  it('a 403 / 404 / 503 on re-read replaces EVERY row and the configuration with the refusal; nothing stale stays', async () => {
    api.listChannelGrants.mockResolvedValueOnce(grantsPageOf())
    api.listChannelGrants.mockRejectedValueOnce(
      new ApiError(
        404,
        'not_found',
        'hidden',
        'req-4',
        {},
        { code: 'not_found' },
      ),
    )
    mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    expect(within(sheet).getByLabelText(/^Name/)).toHaveValue('Ops')
    const user = userEvent.setup()
    await user.click(within(sheet).getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(etagOf(sheet)).toBe(''))
    expect(within(sheet).queryByLabelText(/^Name/)).toBeNull()
    expect(within(sheet).queryByRole('tab', { name: 'Grants' })).toBeNull()
    expect(
      sheet.querySelector('[data-slot="admin-read-failure"]'),
    ).not.toBeNull()
    expect(
      within(sheet).getByText(/Not found, or not visible/),
    ).toBeInTheDocument()
  })

  it('losing the revoke capability with a confirmation open closes it and dispatches nothing', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    const { rerender } = renderWithQuery(() => (
      <ChannelAdminContinuity admitted access="allowed">
        <ChannelAdminSheet
          open
          onOpenChange={() => {}}
          channelId={CHANNEL_ID}
          scope={scopeOf()}
          canUserRead={false}
          canAgentRead={false}
          me={{ userId: USER_A, label: 'A' }}
          onMutated={() => {}}
        />
      </ChannelAdminContinuity>
    ))
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('row', { name: new RegExp(USER_B) }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Revoke this generation' }),
    )
    expect(
      within(sheet).getByRole('button', { name: 'Confirm revocation' }),
    ).toBeInTheDocument()
    // The exact revoke operation stops being allowed — an expiry, a moved context or a
    // policy change all look like this to the surface — and the open confirmation closes.
    ;(caps.state as ReturnType<typeof capabilityState>).byOperation[
      'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke'
    ] = 'unknown'
    act(() => rerender())
    await waitFor(() =>
      expect(
        within(sheet).queryByRole('button', { name: 'Confirm revocation' }),
      ).toBeNull(),
    )
    expect(api.revokeChannelGrant).not.toHaveBeenCalled()
  })
})

describe('ChannelAdminSheet — filters', () => {
  it('the subject filter travels as kind AND ref together, and the persisted-state filter restarts the chain', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf({ items: [] }))
    mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await within(sheet).findByText('No generations')
    await user.click(
      within(sheet).getByRole('combobox', { name: 'Subject kind' }),
    )
    await user.click(await screen.findByRole('option', { name: 'User' }))
    await user.type(within(sheet).getByLabelText('Subject reference'), USER_B)
    await user.click(
      within(sheet).getByRole('button', { name: 'Apply filter' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    expect(api.listChannelGrants.mock.calls[1][1]).toEqual({
      workspace_id: WS,
      state: 'active',
      limit: 50,
      subject_kind: 'user',
      subject_ref: USER_B,
      continuation: undefined,
    })
    await user.click(
      within(sheet).getByRole('combobox', { name: 'Persisted state' }),
    )
    await user.click(
      await screen.findByRole('option', { name: 'All (stored)' }),
    )
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(3))
    expect(api.listChannelGrants.mock.calls[2][1]).toMatchObject({
      state: 'all',
      subject_kind: 'user',
      subject_ref: USER_B,
      continuation: undefined,
    })
  })
})

describe('ChannelAdminSheet — the sentence that closes an act', () => {
  const REVOKE_OP = 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke'
  const SHEET_OP = 'GET /v1/m/sessions/channels/{id}/grants'
  const DIAGNOSIS = /permission behind it was lost/
  const warnings = () =>
    (toast.warning as ReturnType<typeof vi.fn>).mock.calls
      .map((c) => String(c[0]))
      .join(' ')

  /** Open a real revoke confirmation on a real grant row. */
  async function confirmingRevoke() {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    const { rerender } = mount()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await openGrantsTab(user)
    await user.click(
      await within(sheet).findByRole('row', { name: new RegExp(USER_B) }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Revoke this generation' }),
    )
    expect(
      within(sheet).getByRole('button', { name: 'Confirm revocation' }),
    ).toBeInTheDocument()
    ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()
    return { sheet, rerender }
  }

  async function closed(
    sheet: HTMLElement,
    rerender: () => void,
    answer: string,
    operation: string = REVOKE_OP,
  ) {
    ;(caps.state as ReturnType<typeof capabilityState>).byOperation[operation] =
      answer as never
    act(() => rerender())
    await waitFor(() =>
      expect(
        within(sheet).queryByRole('button', { name: 'Confirm revocation' }),
      ).toBeNull(),
    )
    expect(api.revokeChannelGrant).not.toHaveBeenCalled()
    return warnings()
  }

  // ⛔ FOUR ANSWERS REACHED ONE BRANCH AND IT NAMED A CAUSE FOR ALL OF THEM. Under the
  //    capability migration this closure now receives a concealed non-verdict, a local
  //    expiry, a movement and a transport that did not answer, and only the first of the
  //    five below is something the engine actually said.
  it.each([
    ['undisclosed', 'a concealed non-verdict'],
    ['unknown', 'a local expiry, movement or transport loss'],
    ['step_up_required', 'a target-free assurance gate'],
    ['checking', 'an observation that has not landed'],
  ])('%s (%s) closes it without naming a cause', async (answer) => {
    const { sheet, rerender } = await confirmingRevoke()
    const said = await closed(sheet, rerender, answer)
    expect(said).not.toMatch(DIAGNOSIS)
    expect(said).toMatch(/no current answer/i)
  })

  it('CONTROL: an ESTABLISHED denial of the act is the one case that may be named', async () => {
    const { sheet, rerender } = await confirmingRevoke()
    expect(await closed(sheet, rerender, 'denied')).toMatch(DIAGNOSIS)
  })

  it('losing the SHEET rather than the act is judged on the sheet’s own answer', async () => {
    const { sheet, rerender } = await confirmingRevoke()
    // The sheet read stops being positive while the revoke confirmation is open: the
    // deciding answer is the sheet's, and a concealment there is not a permission loss.
    expect(await closed(sheet, rerender, 'undisclosed', SHEET_OP)).not.toMatch(
      DIAGNOSIS,
    )
  })

  it('CONTROL: an established refusal of the SHEET may be named', async () => {
    const { sheet, rerender } = await confirmingRevoke()
    expect(await closed(sheet, rerender, 'denied', SHEET_OP)).toMatch(DIAGNOSIS)
  })
})

// ⛔ THE DRAFT IS THE OPERATOR'S, AND A REFRESH IS NOT A REFUSAL.
//
// Measured on G1-B with the real console and the real engine: a confirmation prepared over
// a name the operator had typed vanished 4,3 s after it was prepared, with the engine's own
// name back in the field, the ETag unchanged at `"v524"`, no sentence on screen and zero
// PATCH requests sent. Nothing had refused anything — the sheet's positive simply reached
// its budget, the next observation had not landed yet, and the console spent the operator's
// work on a five-second refresh.
//
// The chain, and every link of it is deliberate except the last: a local `unknown` makes the
// read not enabled, the effect that cancels a read of a scope nobody is in any more REMOVES
// its page, `channel && etag` gate the tabs, and a form's state dies with its instance.
//
// These cases hold the whole chain fixed and measure the ONE property that was wrong: what
// the operator typed survives, nothing the engine answered does, and a confirmation that
// closes says so.
describe('ChannelAdminSheet — a transient gap in the observation keeps the draft', () => {
  const SHEET_OP = 'GET /v1/m/sessions/channels/{id}/grants'
  const PATCH_OP = 'PATCH /v1/m/sessions/channels'
  const DIAGNOSIS = /permission behind it was lost/
  const state = () => caps.state as ReturnType<typeof capabilityState>
  const warnings = () =>
    (toast.warning as ReturnType<typeof vi.fn>).mock.calls
      .map((c) => String(c[0]))
      .join(' ')
  // `name` is a FULL-STRING match here, which is the same subject the browser case now
  // addresses by accessible name: the visual required marker is `aria-hidden`, so the
  // control is named `Name` even though its label reads `Name*`.
  const nameField = (sheet: HTMLElement) =>
    within(sheet).getByRole('textbox', { name: 'Name' })

  /** A prepared configuration confirmation over a name the operator typed. */
  async function confirmingRename(over: { name?: string } = {}): Promise<{
    sheet: HTMLElement
    rerender: () => void
    user: ReturnType<typeof userEvent.setup>
    typed: string
  }> {
    const typed = over.name ?? 'Ops renamed by the operator'
    const { rerender } = mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const field = nameField(sheet)
    await user.clear(field)
    await user.type(field, typed)
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    expect(sheet.querySelector('[data-slot="config-confirm"]')).not.toBeNull()
    ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()
    return { sheet, rerender, user, typed }
  }

  /** The sheet's own observation stops being a positive, without any engine verdict. */
  async function gap(sheet: HTMLElement, rerender: () => void, answer: string) {
    const readsBefore = api.listChannelGrants.mock.calls.length
    state().byOperation[SHEET_OP] = answer as never
    act(() => rerender())
    await waitFor(() =>
      expect(within(sheet).queryByRole('textbox', { name: 'Name' })).toBeNull(),
    )
    return readsBefore
  }

  async function readmit(sheet: HTMLElement, rerender: () => void) {
    delete state().byOperation[SHEET_OP]
    act(() => rerender())
    await waitFor(() =>
      expect(
        within(sheet).queryByRole('textbox', { name: 'Name' }),
      ).not.toBeNull(),
    )
  }

  it.each([['unknown'], ['checking']])(
    'D1 — a local `%s` on the sheet read closes the confirmation without naming a cause, asks nothing, sends nothing, and hands the operator their OWN draft back on the next authorized read',
    async (answer) => {
      const { sheet, rerender, typed } = await confirmingRename()
      const readsBefore = await gap(sheet, rerender, answer)

      // NOTHING IS ASKED WITHOUT A CURRENT POSITIVE. The read is not enabled while the
      // answer is missing, so the gap costs zero requests and zero bytes.
      expect(api.listChannelGrants.mock.calls.length).toBe(readsBefore)
      expect(api.updateChannel).not.toHaveBeenCalled()
      // The confirmation closed, and the console said so in the sentence that names no
      // cause: nothing was established about this operator.
      const said = warnings()
      expect(said).toMatch(/no current answer/i)
      expect(said).not.toMatch(DIAGNOSIS)

      await readmit(sheet, rerender)
      // What the operator typed is back, as their own edit: the field carries it, the
      // form is dirty, and a review is offered again.
      expect(nameField(sheet)).toHaveValue(typed)
      expect(
        within(sheet).getByRole('button', { name: 'Review changes' }),
      ).toBeEnabled()
      expect(
        within(sheet).getByRole('button', { name: 'Revert edits' }),
      ).toBeInTheDocument()

      // And it is an INTENTION, not a repaint: reviewing again sends exactly that field,
      // under the ETag of the read on screen, after a NEW exact preflight.
      api.updateChannel.mockResolvedValue(adminOutcomeOf())
      const user = userEvent.setup()
      await user.click(
        within(sheet).getByRole('button', { name: 'Review changes' }),
      )
      await user.click(
        within(sheet).getByRole('button', { name: 'Confirm changes' }),
      )
      await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
      const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
      expect(intent.etag).toBe('"v2"')
      expect(intent.body).toEqual({ channel_id: CHANNEL_ID, name: typed })
      expect(
        state().preflighted.filter((q) => q.operation === PATCH_OP).length,
      ).toBeGreaterThan(0)
    },
  )

  it('D1 CONCEALMENT — nothing the ENGINE answered survives the gap: no ETag, no channel fields, no grant history, and the notice states that the access could not be established', async () => {
    const { sheet, rerender } = await confirmingRename()
    await gap(sheet, rerender, 'unknown')

    expect(sheet.querySelector('[data-slot="admin-etag"]')).toBeNull()
    expect(sheet.querySelector('[data-slot="config-etag"]')).toBeNull()
    expect(sheet.querySelector('[data-slot="config-confirm"]')).toBeNull()
    expect(within(sheet).queryByRole('tab', { name: 'Grants' })).toBeNull()
    expect(within(sheet).queryByText('Ops')).toBeNull()
    // The authority state on screen is the one that is true: no answer, still asking.
    expect(
      sheet.querySelector('[data-slot="capability-unavailable"]'),
    ).not.toBeNull()
    expect(sheet.querySelector('[data-slot="capability-retry"]')).not.toBeNull()
  })

  it('D2 — a re-read that returns the SAME ETag leaves the confirmation standing and the draft untouched', async () => {
    const { sheet, user, typed } = await confirmingRename()
    await user.click(within(sheet).getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v2"')
    expect(sheet.querySelector('[data-slot="config-confirm"]')).not.toBeNull()
    expect(nameField(sheet)).toHaveValue(typed)
    expect(warnings()).toBe('')
    expect(api.updateChannel).not.toHaveBeenCalled()
  })

  it('D3 — a REAL ETag move under a prepared confirmation closes it in its own words, keeps the touched edit, and a NEW review carries the NEW validator', async () => {
    api.listChannelGrants.mockResolvedValueOnce(grantsPageOf())
    // The same fields, a moved version: the CAS validator changed and nothing else did,
    // which is exactly the case where the rebase banner has nothing to report.
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ channel: { version: 3 } }),
    )
    const { rerender } = mount()
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const field = nameField(sheet)
    await user.clear(field)
    await user.type(field, 'Ops renamed')
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    expect(sheet.querySelector('[data-slot="config-confirm"]')).not.toBeNull()
    ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()
    void rerender

    await user.click(within(sheet).getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v3"')

    // Closed, and said as the movement it is — not as a lost permission, not as a missing
    // answer, and not as the rebase of fields nobody touched.
    expect(sheet.querySelector('[data-slot="config-confirm"]')).toBeNull()
    const said = warnings()
    expect(said).toMatch(/The channel has changed/)
    expect(said).toMatch(/edits are preserved/)
    expect(said).not.toMatch(DIAGNOSIS)
    expect(said).not.toMatch(/no current answer/i)
    expect(sheet.querySelector('[data-slot="config-rebased"]')).toBeNull()
    // The edit stands and nothing was sent under either validator.
    expect(nameField(sheet)).toHaveValue('Ops renamed')
    expect(api.updateChannel).not.toHaveBeenCalled()

    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.etag).toBe('"v3"')
    expect(intent.body).toEqual({ channel_id: CHANNEL_ID, name: 'Ops renamed' })
  })

  it('D4 — losing the PATCH authority alone closes the confirmation neutrally, keeps the form mounted with the operator’s edit, and sends nothing', async () => {
    const { sheet, rerender, typed } = await confirmingRename()
    state().byOperation[PATCH_OP] = 'unknown'
    act(() => rerender())
    await waitFor(() =>
      expect(sheet.querySelector('[data-slot="config-confirm"]')).toBeNull(),
    )
    // The sheet itself never lost its read: the form is still on screen, with the edit.
    expect(sheet.querySelector('[data-slot="admin-etag"]')).not.toBeNull()
    expect(nameField(sheet)).toHaveValue(typed)
    const said = warnings()
    expect(said).toMatch(/no current answer/i)
    expect(said).not.toMatch(DIAGNOSIS)
    expect(api.updateChannel).not.toHaveBeenCalled()

    // Re-admitted, the operator confirms again — and the act still pays for a NEW exact
    // observation of its own operation before any byte leaves.
    delete state().byOperation[PATCH_OP]
    act(() => rerender())
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    const user = userEvent.setup()
    await user.click(
      within(sheet).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    expect(
      state().preflighted.filter((q) => q.operation === PATCH_OP).length,
    ).toBe(1)
  })

  it('an act ALREADY BEING SENT is never reported as a zero-send: the gap interrupts it and says exactly that', async () => {
    const pending = deferred<ChannelAdminOutcome>()
    api.updateChannel.mockReturnValue(pending.promise)
    const { sheet, rerender, user } = await confirmingRename()
    await user.click(
      within(sheet).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()

    await gap(sheet, rerender, 'unknown')
    // The bytes were already on their way when the answer went missing. The console
    // does not know whether they arrived, so it does not say that nothing was sent.
    const said = warnings()
    expect(said).toMatch(
      /whether the act reached the engine is not established/,
    )
    expect(said).not.toMatch(/nothing was sent/i)
    expect(said).not.toMatch(DIAGNOSIS)
    expect(api.updateChannel).toHaveBeenCalledTimes(1)
  })

  it('an ESTABLISHED refusal of the sheet is not a gap: the draft goes with everything else and does not come back', async () => {
    const { sheet, rerender } = await confirmingRename()
    await gap(sheet, rerender, 'denied')
    expect(sheet.querySelector('[data-slot="admin-etag"]')).toBeNull()
    // The one case that may be shown as a refusal, and the one that ends the draft.
    expect(
      within(sheet).getByText(/refused this for the current principal/),
    ).toBeInTheDocument()
    await readmit(sheet, rerender)
    expect(nameField(sheet)).toHaveValue('Ops')
    expect(api.updateChannel).not.toHaveBeenCalled()
  })

  it('an `undisclosed` non-verdict is not a gap either: the concealment keeps its existing teardown', async () => {
    const { sheet, rerender } = await confirmingRename()
    await gap(sheet, rerender, 'undisclosed')
    expect(
      sheet.querySelector('[data-slot="capability-undisclosed"]'),
    ).not.toBeNull()
    await readmit(sheet, rerender)
    expect(nameField(sheet)).toHaveValue('Ops')
  })

  it('CLOSING the sheet ends the draft: reopening the same channel starts from the engine, not from what was typed before', async () => {
    const props = { open: true }
    const { rerender } = renderWithQuery(() => (
      <ChannelAdminContinuity admitted access="allowed">
        <ChannelAdminSheet
          open={props.open}
          onOpenChange={() => {}}
          channelId={CHANNEL_ID}
          scope={scopeOf()}
          canUserRead={false}
          canAgentRead={false}
          me={{ userId: USER_A, label: 'A' }}
          onMutated={() => {}}
        />
      </ChannelAdminContinuity>
    ))
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const field = nameField(sheet)
    await user.clear(field)
    await user.type(field, 'Typed before closing')
    props.open = false
    act(() => rerender())
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    props.open = true
    act(() => rerender())
    const reopened = await screen.findByRole('dialog')
    await readEtag(reopened, '"v2"')
    expect(nameField(reopened)).toHaveValue('Ops')
  })

  it('another SCOPE is another context: a draft never crosses a boundary or workspace move', async () => {
    const props = { scope: scopeOf() }
    const { rerender } = renderWithQuery(() => (
      <ChannelAdminContinuity admitted access="allowed">
        <ChannelAdminSheet
          open
          onOpenChange={() => {}}
          channelId={CHANNEL_ID}
          scope={props.scope}
          canUserRead={false}
          canAgentRead={false}
          me={{ userId: USER_A, label: 'A' }}
          onMutated={() => {}}
        />
      </ChannelAdminContinuity>
    ))
    const sheet = await screen.findByRole('dialog')
    await readEtag(sheet, '"v2"')
    const user = userEvent.setup()
    const field = nameField(sheet)
    await user.clear(field)
    await user.type(field, 'Typed in Billing')
    props.scope = scopeOf({
      workspace: WS2,
      key: `${USER_A}|t1|c0|w:${WS2}`,
    })
    act(() => rerender())
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(2))
    await readEtag(sheet, '"v2"')
    expect(nameField(sheet)).toHaveValue('Ops')
  })
})
