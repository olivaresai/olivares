// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FIVE REGISTERED QUESTIONS, AND THE ORDER OF AN ADMINISTRATIVE SEND.
//
// Three properties live here and nowhere else:
//
//   · the EXACT payload of each of the five questions — the mounted pattern, the workspace
//     and the declared selector family — because a question that names the wrong route or
//     the wrong locator is answered honestly about something nobody asked;
//   · each administrative act is gated by ITS OWN answer, so a sheet read cannot enable a
//     PATCH and one grant row's revoke cannot enable the next;
//   · the confirm → fresh preflight → composed guard → request ORDER, because a permit
//     obtained after the bytes left would be theatre.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  listChannelGrants: vi.fn(),
  updateChannel: vi.fn(),
  grantChannel: vi.fn(),
  revokeChannelGrant: vi.fn(),
  listMembers: vi.fn(),
  listAgents: vi.fn(),
  getChannel: vi.fn(),
  listAdministrableChannels: vi.fn(),
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

import { ChannelAdminSheet } from './channel-admin-sheet'
import { ChannelConfigForm } from './channel-config-form'
import {
  administrationSurfaceQuestion,
  adminChannelFromSearch,
  configurationQuestion,
  grantCreationQuestion,
  grantRevocationQuestion,
  grantSheetQuestion,
} from './capabilities'
import { permittedDispatchGuard, StaleIntentError } from './intent'
import {
  adminOutcomeOf,
  capabilityState,
  channelOf,
  CHANNEL_ID,
  GRANT_ID,
  grantItemOf,
  grantsPageOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  USER_B,
  WS,
  WS2,
} from './test-harness'
import './i18n'

const state = () => caps.state as ReturnType<typeof capabilityState>
const SHEET_OP = 'GET /v1/m/sessions/channels/{id}/grants'
const PATCH_OP = 'PATCH /v1/m/sessions/channels'
const GRANT_OP = 'POST /v1/m/sessions/channels/{id}/grants'
const REVOKE_OP = 'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke'

beforeEach(() => {
  Object.assign(caps.state as object, capabilityState())
  for (const fn of Object.values(api)) fn.mockReset()
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
  api.listChannelGrants.mockResolvedValue(grantsPageOf())
})

describe('the exact payload of the five registered questions', () => {
  it('is the ratified table, with the MOUNTED pattern and the declared locator family', () => {
    expect(administrationSurfaceQuestion(WS)).toEqual({
      kind: 'surface',
      operation: 'GET /v1/m/sessions/channels/administration',
      workspaceId: WS,
      path: [],
      body: [],
    })
    expect(grantSheetQuestion(WS, CHANNEL_ID)).toEqual({
      kind: 'operation',
      operation: SHEET_OP,
      workspaceId: WS,
      path: [['id', CHANNEL_ID]],
      body: [],
    })
    // ⛔ PATCH LOCATES ITS ROW IN THE BODY. Sending `path.id` for it would be a selector
    //    the route never declared, and the honest answer to that is a refusal — so the
    //    screen would go permanently unavailable rather than wrongly enabled. Either way
    //    the family is not interchangeable, and this is the case that pins it.
    expect(configurationQuestion(WS, CHANNEL_ID)).toEqual({
      kind: 'operation',
      operation: PATCH_OP,
      workspaceId: WS,
      path: [],
      body: [['channel_id', CHANNEL_ID]],
    })
    expect(grantCreationQuestion(WS, CHANNEL_ID)).toEqual({
      kind: 'operation',
      operation: GRANT_OP,
      workspaceId: WS,
      path: [['id', CHANNEL_ID]],
      body: [],
    })
    expect(grantRevocationQuestion(WS, CHANNEL_ID, GRANT_ID)).toEqual({
      kind: 'operation',
      operation: REVOKE_OP,
      workspaceId: WS,
      // Sorted PAIRS: `grant_id` before `id`, and the grant id is part of the question.
      path: [
        ['grant_id', GRANT_ID],
        ['id', CHANNEL_ID],
      ],
      body: [],
    })
  })

  it('never asks about anything without an explicit workspace and an explicit target', () => {
    for (const [what, q] of [
      ['no workspace (surface)', administrationSurfaceQuestion(null)],
      ['empty workspace (surface)', administrationSurfaceQuestion('')],
      ['no workspace (sheet)', grantSheetQuestion(null, CHANNEL_ID)],
      ['no channel (sheet)', grantSheetQuestion(WS, null)],
      ['no channel (patch)', configurationQuestion(WS, undefined)],
      ['no channel (grant)', grantCreationQuestion(WS, '')],
      ['no grant (revoke)', grantRevocationQuestion(WS, CHANNEL_ID, null)],
      ['no channel (revoke)', grantRevocationQuestion(WS, null, GRANT_ID)],
    ] as const)
      expect(q, what).toBeNull()
  })

  it('a sibling channel, a sibling grant and a sibling workspace are DIFFERENT questions', () => {
    const other = '0192f2c0-bbbb-7000-8000-0000000000ff'
    expect(grantSheetQuestion(WS, CHANNEL_ID)).not.toEqual(
      grantSheetQuestion(WS, other),
    )
    expect(grantSheetQuestion(WS, CHANNEL_ID)).not.toEqual(
      grantSheetQuestion(WS2, CHANNEL_ID),
    )
    expect(grantRevocationQuestion(WS, CHANNEL_ID, GRANT_ID)).not.toEqual(
      grantRevocationQuestion(WS, CHANNEL_ID, other),
    )
    // And the two families of the same channel are not each other.
    expect(grantSheetQuestion(WS, CHANNEL_ID)).not.toEqual(
      grantCreationQuestion(WS, CHANNEL_ID),
    )
  })

  it('reads the deep link as a canonical id or not at all', () => {
    expect(adminChannelFromSearch(`?admin_channel=${CHANNEL_ID}`)).toBe(
      CHANNEL_ID,
    )
    expect(adminChannelFromSearch(`?x=1&admin_channel=${CHANNEL_ID}&y=2`)).toBe(
      CHANNEL_ID,
    )
    for (const bad of [
      '',
      '?admin_channel=',
      '?admin_channel=nope',
      '?admin_channel=0192f2c0-bbbb-7000-8000',
      '?channel=' + CHANNEL_ID,
    ])
      expect(adminChannelFromSearch(bad), bad).toBeNull()
  })
})

describe('each act is gated by its OWN answer', () => {
  function mountSheet() {
    return renderWithQuery(() => (
      <ChannelAdminSheet
        open
        onOpenChange={() => {}}
        channelId={CHANNEL_ID}
        scope={scopeOf()}
        canUserRead={false}
        canAgentRead={false}
        me={{ userId: null, label: 'A' }}
        onMutated={() => {}}
      />
    ))
  }

  it('the sheet READ waits for the exact grant-sheet positive and for nothing else', async () => {
    state().access = 'negative'
    state().byOperation[GRANT_OP] = 'positive'
    state().byOperation[PATCH_OP] = 'positive'
    mountSheet()
    await screen.findByRole('dialog')
    // A grant permit and a PATCH permit do not open the read.
    await new Promise((r) => setTimeout(r, 20))
    expect(api.listChannelGrants).not.toHaveBeenCalled()
    // CONTROL: with its own positive, the same mount reads.
    state().byOperation[SHEET_OP] = 'positive'
    mountSheet()
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalled())
  })

  it('add-grant is enabled by the grant operation, not by the sheet read', async () => {
    state().access = 'negative'
    state().byOperation[SHEET_OP] = 'positive'
    mountSheet()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Grants' }))
    const add = await within(sheet).findByRole('button', { name: 'Add grant' })
    expect(add).toBeDisabled()
  })

  it('the revoke decision is fetched for the SELECTED generation, and one row never speaks for another', async () => {
    // Two generations of two DIFFERENT subjects, so a row can be named unambiguously.
    const rowA = grantItemOf({
      id: GRANT_ID,
      subject: { kind: 'user', ref: USER_A },
    })
    const other = '0192f2c0-9999-7000-8000-0000000000ff'
    const rowB = grantItemOf({
      id: other,
      subject: { kind: 'user', ref: USER_B },
    })
    api.listChannelGrants.mockResolvedValue(
      grantsPageOf({ items: [rowA, rowB] }),
    )
    state().access = 'negative'
    state().byOperation[SHEET_OP] = 'positive'
    // ONLY the first generation may be revoked.
    mountSheet()
    const sheet = await screen.findByRole('dialog')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Grants' }))
    await within(sheet).findByRole('row', { name: new RegExp(USER_B) })

    // The asked questions carry the selected grant id: selecting a row asks about THAT
    // row, and the answer for one is never reused for the other.
    const revokeQuestions = () =>
      state()
        .asked.filter((q) => q.operation === REVOKE_OP)
        .map((q) => Object.fromEntries(q.path))
    await user.click(
      within(sheet).getByRole('row', { name: new RegExp(USER_B) }),
    )
    await waitFor(() =>
      expect(revokeQuestions()).toContainEqual({
        id: CHANNEL_ID,
        grant_id: other,
      }),
    )
    // Nothing asked about the OTHER generation while it was not selected.
    expect(revokeQuestions()).not.toContainEqual({
      id: CHANNEL_ID,
      grant_id: GRANT_ID,
    })
  })

  it('the configuration form waits for the PATCH operation, and a sheet read does not stand in for it', async () => {
    state().access = 'negative'
    state().byOperation[SHEET_OP] = 'positive'
    renderWithQuery(() => (
      <ChannelConfigForm
        channel={channelOf()}
        etag={'"v2"'}
        scope={scopeOf()}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />
    ))
    expect(
      await screen.findByRole('button', { name: 'Review changes' }),
    ).toBeDisabled()
    // The question it waited for is the PATCH one, with the BODY locator.
    expect(state().asked).toContainEqual(configurationQuestion(WS, CHANNEL_ID))
  })
})

describe('confirm → fresh preflight → composed guard → request', () => {
  it('asks the EXACT operation again at confirm, before the mutation leaves', async () => {
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    renderWithQuery(() => (
      <ChannelConfigForm
        channel={channelOf()}
        etag={'"v2"'}
        scope={scopeOf()}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />
    ))
    const user = userEvent.setup()
    const name = await screen.findByLabelText(/^name/i)
    await user.clear(name)
    await user.type(name, 'Renamed')
    await user.click(screen.getByRole('button', { name: 'Review changes' }))
    expect(state().preflighted).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'Confirm changes' }))
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    // ⛔ ONE preflight, of THIS operation, and the render observation did not stand in for
    //    it: the confirmation may have been open for minutes.
    expect(state().preflighted).toEqual([configurationQuestion(WS, CHANNEL_ID)])
  })

  it('sends NOTHING when the preflight does not return a current positive', async () => {
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    state().permitted = false
    renderWithQuery(() => (
      <ChannelConfigForm
        channel={channelOf()}
        etag={'"v2"'}
        scope={scopeOf()}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />
    ))
    const user = userEvent.setup()
    const name = await screen.findByLabelText(/^name/i)
    await user.clear(name)
    await user.type(name, 'Renamed')
    await user.click(screen.getByRole('button', { name: 'Review changes' }))
    await user.click(screen.getByRole('button', { name: 'Confirm changes' }))
    await waitFor(() => expect(state().preflighted).toHaveLength(1))
    expect(api.updateChannel).not.toHaveBeenCalled()
    // And the confirmation closed rather than sitting there looking sendable.
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Confirm changes' }),
      ).toBeNull(),
    )
  })

  it('hands the TRANSPORT a guard that composes the intent check with the permit', async () => {
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    renderWithQuery(() => (
      <ChannelConfigForm
        channel={channelOf()}
        etag={'"v2"'}
        scope={scopeOf()}
        reading={false}
        onReread={() => {}}
        onApplied={() => {}}
      />
    ))
    const user = userEvent.setup()
    const name = await screen.findByLabelText(/^name/i)
    await user.clear(name)
    await user.type(name, 'Renamed')
    await user.click(screen.getByRole('button', { name: 'Review changes' }))
    await user.click(screen.getByRole('button', { name: 'Confirm changes' }))
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    // The shared client runs THIS function immediately before the first fetch and again
    // before the single 401 replay (proved on the real client in
    // lib/auth/capabilities.causal.test.ts). What is proved here is that the function the
    // transport was handed is the composed one, and that it is callable and current.
    const options = api.updateChannel.mock.calls[0][1] as {
      tenant: string | null
      guard: () => void
    }
    expect(options.tenant).toBe('t1')
    expect(typeof options.guard).toBe('function')
    expect(() => options.guard()).not.toThrow()
  })

  it("the composed guard refuses on an EXPIRED permit and on a MOVED context, as this feature's own typed refusal", () => {
    const guard = {
      begin: () => null,
      alive: () => true,
      end: () => {},
      check: () => {},
    }
    const question = configurationQuestion(WS, CHANNEL_ID)
    if (!question) throw new Error('unreachable')
    const context = {
      principalKind: 'user',
      actor: 'user:a',
      tenant: 't1',
      credentialGeneration: 1,
      workspace: WS,
      lifetime: 0,
    }
    const permit = (current: boolean) =>
      Object.freeze({
        context,
        question,
        deadline: 0,
        isCurrent: () => current,
        assertCurrent: () => {
          if (!current)
            throw new Error('capability permit is no longer current')
        },
      })
    expect(() => permittedDispatchGuard(guard, permit(true))()).not.toThrow()
    // A non-CapabilityLostError from the permit is NOT relabelled: only the known refusal
    // is translated, so a genuine bug cannot hide behind the calm path.
    expect(() => permittedDispatchGuard(guard, permit(false))()).toThrow(
      /no longer current/,
    )
    // The surface's own check runs FIRST and its refusal reaches the caller unchanged.
    const moved = {
      ...guard,
      check: () => {
        throw new StaleIntentError('workspace')
      },
    }
    expect(() => permittedDispatchGuard(moved, permit(true))()).toThrow(
      StaleIntentError,
    )
  })
})

describe('what the migration never does', () => {
  it('asks for no read tier, consults no my_access, and reads no permission string', () => {
    const questions = [
      administrationSurfaceQuestion(WS),
      grantSheetQuestion(WS, CHANNEL_ID),
      configurationQuestion(WS, CHANNEL_ID),
      grantCreationQuestion(WS, CHANNEL_ID),
      grantRevocationQuestion(WS, CHANNEL_ID, GRANT_ID),
    ]
    const serialized = JSON.stringify(questions)
    for (const forbidden of [
      'sessions:channel:read',
      'sessions:channel:admin',
      'my_access',
      'user:read',
      'agent:read',
    ])
      expect(serialized, forbidden).not.toContain(forbidden)
    // Positive control: the five are actually there, so "contains nothing" is not passing
    // because the list is empty.
    expect(questions.filter(Boolean)).toHaveLength(5)
    expect(serialized).toContain('/v1/m/sessions/channels/administration')
  })

  it('the administrative reads fire on the capability alone, with an EMPTY permission set', async () => {
    // The component tests mock `useAuth().can` to false everywhere by default in this
    // file: nothing below hands the sheet a permission, and it reads anyway.
    state().access = 'positive'
    renderWithQuery(() => (
      <ChannelAdminSheet
        open
        onOpenChange={() => {}}
        channelId={CHANNEL_ID}
        scope={scopeOf()}
        canUserRead={false}
        canAgentRead={false}
        me={{ userId: null, label: 'A' }}
        onMutated={() => {}}
      />
    ))
    await waitFor(() => expect(api.listChannelGrants).toHaveBeenCalledTimes(1))
    // And the read-tier channel GET never ran: administration is not a read.
    expect(api.getChannel).not.toHaveBeenCalled()
  })
})
