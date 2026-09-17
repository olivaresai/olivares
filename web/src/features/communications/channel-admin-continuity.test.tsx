// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONTINUITY BOUNDARY, MEASURED THROUGH THE REAL GATE — because the property at stake
// is a property of the COMPOSITION and of nothing smaller.
//
// The defect these cases exist for was invisible to every green component test: the
// configuration form kept the operator's draft correctly, the sheet handed it back
// correctly, and in the browser the whole administration view was torn down and rebuilt
// every ~5 s by `RequirePermission`, taking the sheet, the form and the draft with it.
// Measured, three runs, 34–46 ms per teardown.
//
// So every case below renders the REAL `RequirePermission` with the REAL registry entry,
// and drives the REAL capability answer. Two consequences are deliberate:
//
//   · the first case is also the CONTROL against putting the hold back under the cut —
//     it survives a genuine unmount of the protected subtree, which nothing owned by that
//     subtree can do;
//   · the identity cases drive `CapabilityContext` itself, so an implementation that keyed
//     on the communications scope alone — no principal class, no actor, no credential
//     generation, no movement counter — fails them.
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useCallback, useEffect, useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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

const auth = vi.hoisted(() => ({
  perms: new Set<string>(['sessions:channel:admin']),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => auth.perms.has(p),
    principal: { kind: 'user', user_id: 'u-a', actor: 'user:u-a' },
    activeTenant: 't1',
  }),
}))

const routerSearch = vi.hoisted(() => ({ value: '' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { searchStr: routerSearch.value, pathname: '/x' } }),
  useRouter: () => ({ navigate: () => {}, subscribe: () => () => {} }),
  useNavigate: () => () => {},
}))

/**
 * The capability layer, steered rather than replaced: the real pure exports stay, and only
 * the two hooks answer from this table. `entity` is ONE value on purpose — in production
 * the route's deep-link question and the sheet's own read are the SAME registered
 * question, so they expire together, which is exactly the race being measured.
 */
const caps = vi.hoisted(() => ({
  entity: 'allowed' as string,
  other: 'allowed' as string,
  context: {
    principalKind: 'user',
    actor: 'user:u-a',
    tenant: 't1',
    credentialGeneration: 0,
    workspace: '0192f2c0-aaaa-7000-8000-000000000001',
    lifetime: 0,
  } as Record<string, unknown> | null,
  preflighted: [] as string[],
}))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  const GRANTS = 'GET /v1/m/sessions/channels/{id}/grants'
  const preflight = {
    // A getter: the identity of this object is stable across renders (so the boundary's
    // memo is not rebuilt for nothing) while the VALUE is whatever the case last set.
    get context() {
      return caps.context
    },
    live: () => caps.context,
    request: async (q: { operation: string }) => {
      caps.preflighted.push(q.operation)
      return {
        context: caps.context,
        question: q,
        deadline: Number.POSITIVE_INFINITY,
        isCurrent: () => true,
        assertCurrent: () => {},
      }
    },
  }
  return {
    ...real,
    useCapability: (q: { kind: string; operation: string } | null) => {
      if (!q) return { access: 'unknown', permit: null, question: null }
      const access = q.operation === GRANTS ? caps.entity : caps.other
      const positive = access === 'allowed' || access === 'reachable'
      return {
        access,
        permit: positive
          ? {
              context: caps.context,
              question: q,
              deadline: Number.POSITIVE_INFINITY,
              isCurrent: () => true,
              assertCurrent: () => {},
            }
          : null,
        question: q,
      }
    },
    useCapabilityPreflight: () => preflight,
  }
})

import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
import { useWorkspaceStore } from '@/stores/workspace'
import { useChannelDraftCapsule } from './channel-admin-continuity'
import { ChannelAdminSheet } from './channel-admin-sheet'
import type { UpdateChannelIntent } from './intent'
import {
  adminOutcomeOf,
  CHANNEL_ID,
  grantsPageOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  WS,
} from './test-harness'
import './i18n'

const ADMIN_VIEW = FEATURE_VIEWS.find(
  (v) => v.id === 'communicationsAdministration',
)!

/** What the protected child did with the capsule on its last mount. */
const probe = vi.hoisted(() => ({
  mounts: 0,
  taken: null as { edits: Record<string, string>; focus: string | null } | null,
  generation: 0,
  publish: (() => {}) as (edits: Record<string, string>) => void,
  take: (() => null) as () => { edits: Record<string, string> } | null,
  takeWith: (() => null) as (
    gen: number,
  ) => { edits: Record<string, string> } | null,
  /** The closures of the FIRST opening, kept to fire late on purpose. */
  stale: null as ((edits: Record<string, string>) => void) | null,
  staleTake: null as (() => { edits: Record<string, string> } | null) | null,
  staleDiscard: null as (() => void) | null,
  discard: (() => {}) as () => void,
}))

function Protected({ channel = CHANNEL_ID }: { channel?: string }) {
  const capsule = useChannelDraftCapsule()
  const generation = capsule.generation
  // The delivery happens where the real form's does: once, in a mount initializer.
  const [taken] = useState(() => capsule.take(channel, generation))
  const publish = useCallback(
    (edits: Record<string, string>) =>
      capsule.publish(
        channel,
        { edits, confirming: false, submitting: false },
        generation,
        'name',
      ),
    [capsule, channel, generation],
  )
  // Reported from an effect, never written during a render: this probe stands in for a
  // real surface and may not be less disciplined than one.
  const takeNow = useCallback(() => {
    const got = capsule.take(channel, generation)
    return got ? { edits: { ...got.hold.edits } } : null
  }, [capsule, channel, generation])
  useEffect(() => {
    probe.taken = taken
      ? { edits: { ...taken.hold.edits }, focus: taken.focus }
      : null
    probe.generation = generation
    probe.publish = publish
    probe.take = takeNow
    probe.takeWith = (gen) => {
      const got = capsule.take(channel, gen)
      return got ? { edits: { ...got.hold.edits } } : null
    }
    if (!probe.stale) probe.stale = publish
    if (!probe.staleTake) probe.staleTake = takeNow
    if (!probe.staleDiscard)
      probe.staleDiscard = () => capsule.discard('closed', generation)
    probe.discard = () => capsule.discard('closed', generation)
  })
  useEffect(() => {
    probe.mounts += 1
  }, [])
  return <div data-slot="protected-child" />
}

function mountGate(child = <Protected />) {
  return render(
    <RequirePermission view={ADMIN_VIEW}>{child}</RequirePermission>,
  )
}

/** Re-render the gate so the double's new answer reaches it, as a real answer would. */
function answer(
  rerender: (ui: React.ReactElement) => void,
  access: string,
  child = <Protected />,
) {
  caps.entity = access
  act(() => {
    rerender(<RequirePermission view={ADMIN_VIEW}>{child}</RequirePermission>)
  })
}

beforeEach(() => {
  caps.entity = 'allowed'
  caps.other = 'allowed'
  caps.context = {
    principalKind: 'user',
    actor: 'user:u-a',
    tenant: 't1',
    credentialGeneration: 0,
    workspace: WS,
    lifetime: 0,
  }
  caps.preflighted = []
  // The question builders refuse to ask about "every workspace": without an explicit one
  // there is nothing to ask, the route falls back to the reflection, and this whole
  // battery would measure a permission string instead of a capability answer.
  useWorkspaceStore.setState({
    activeWorkspace: WS,
    activeWorkspaceName: 'Billing',
  })
  routerSearch.value = `?admin_channel=${CHANNEL_ID}`
  // ⛔ EVERY retained handle goes, not only `stale`. The probe keeps the FIRST closures of a
  //    case on purpose so they can fire late — and a handle the previous case left behind is
  //    a closure of an owner that has already unmounted. Measured by the independent review
  //    of 2026-09-07: with only `stale` reset, the stale-discard case called the previous
  //    case's discard, whose no-op proved nothing about THIS owner's generation guard.
  probe.mounts = 0
  probe.taken = null
  probe.generation = 0
  probe.publish = () => {}
  probe.take = () => null
  probe.takeWith = () => null
  probe.stale = null
  probe.staleTake = null
  probe.staleDiscard = null
  probe.discard = () => {}
  for (const fn of Object.values(api)) fn.mockReset()
  api.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listAgents.mockResolvedValue({ items: [], has_more: false })
  api.listChannelGrants.mockResolvedValue(grantsPageOf())
  ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()
})

describe('the continuity boundary, through the real route gate', () => {
  it('⛔ THE CONTROL: the protected subtree is really destroyed and rebuilt, and the operator’s typing survives that', () => {
    const { rerender } = mountGate()
    expect(
      document.querySelector('[data-slot="protected-child"]'),
    ).not.toBeNull()
    act(() => probe.publish({ name: 'typed before the edge' }))

    // The budget edge: the answer that authorized this room expires and the next one has
    // not landed. The gate does what it must — the subtree is GONE, not hidden.
    answer(rerender, 'unknown')
    expect(document.querySelector('[data-slot="protected-child"]')).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-unavailable"]'),
    ).not.toBeNull()
    const mountsAtGap = probe.mounts

    // The next answer lands: a NEW child instance, and the operator's own text with it.
    answer(rerender, 'allowed')
    expect(probe.mounts).toBe(mountsAtGap + 1)
    expect(probe.taken?.edits).toEqual({ name: 'typed before the edge' })
    expect(probe.taken?.focus).toBe('name')
  })

  it('holds nothing on a first entry that never had a positive: an empty owner grants nothing', () => {
    caps.entity = 'checking'
    mountGate()
    expect(document.querySelector('[data-slot="protected-child"]')).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-checking"]'),
    ).not.toBeNull()
    // Nothing was mounted, so nothing was published; the capsule has nothing to give.
    expect(probe.mounts).toBe(0)
  })

  it('a write in the same commit as the expiry is kept: the last keystroke is not the price of a refresh', () => {
    const { rerender } = mountGate()
    act(() => {
      probe.publish({ name: 'the last character' })
      caps.entity = 'unknown'
    })
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken?.edits).toEqual({ name: 'the last character' })
  })

  it.each([
    ['denied', 'an established refusal'],
    ['not_reachable', 'an established surface refusal'],
    ['undisclosed', 'a registered concealment'],
    ['step_up_required', 'a target-free assurance gate'],
  ])(
    '%s (%s) ends the opening, and a later positive does not bring it back',
    (access) => {
      const { rerender } = mountGate()
      act(() => probe.publish({ name: 'not mine to keep' }))
      answer(rerender, access)
      expect(document.querySelector('[data-slot="protected-child"]')).toBeNull()
      answer(rerender, 'allowed')
      expect(probe.taken).toBeNull()
    },
  )

  it('⛔ `step_up_required` is the case the route’s own kind cannot tell from an expiry', () => {
    // Both reach the gate as `unavailable`. One is an answer and one is the absence of
    // one, and the boundary must not treat them alike — which is the whole reason the
    // route hands down the exact observation beside its decision.
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'kept across an expiry' }))
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken?.edits).toEqual({ name: 'kept across an expiry' })

    act(() => probe.publish({ name: 'not kept across a step-up' }))
    answer(rerender, 'step_up_required')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('an explicit close ends it, and returning to the very same channel resurrects nothing', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'closed by the operator' }))
    act(() => probe.discard())
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('a STALE consumer cannot take the CURRENT opening’s draft', () => {
    // The other half of the same rule: a retained closure is refused on DELIVERY too, not
    // only on the way in. Nothing here is stale about the text — it is the caller that no
    // longer belongs to the opening it is asking about.
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'first opening' }))
    const staleTake = probe.staleTake!
    act(() => probe.discard())
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    act(() => probe.publish({ name: 'second opening' }))
    expect(probe.take()).toEqual({ edits: { name: 'second opening' } })
    expect(staleTake()).toBeNull()
  })

  it('a caller that presents a generation it does not belong to is refused, current capsule or not', () => {
    // The argument is the contract: "hand back the opening you belong to". A consumer that
    // kept the number from an earlier one — or invented a later one — is answered nothing,
    // and this is checked at the call, not by a cleanup that has not run yet.
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'the current opening' }))
    expect(probe.take()).toEqual({ edits: { name: 'the current opening' } })
    expect(probe.takeWith(probe.generation - 1)).toBeNull()
    expect(probe.takeWith(probe.generation + 1)).toBeNull()
    // And the refusal did not disturb what the rightful consumer holds.
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken?.edits).toEqual({ name: 'the current opening' })
  })

  it('a LATE callback from a closed opening cannot write into the next one', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'first opening' }))
    const stale = probe.stale!
    act(() => probe.discard())
    // The retained closure fires after the opening it belonged to ended.
    act(() => stale({ name: 'resurrected' }))
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('the route leaving takes everything with it: a new visit starts empty', () => {
    const first = mountGate()
    act(() => probe.publish({ name: 'left behind' }))
    first.unmount()
    mountGate()
    expect(probe.taken).toBeNull()
  })

  it('survives React’s development double mount and cleanup', () => {
    const { rerender } = render(
      <StrictMode>
        <RequirePermission view={ADMIN_VIEW}>
          <Protected />
        </RequirePermission>
      </StrictMode>,
    )
    act(() => probe.publish({ name: 'twice mounted' }))
    caps.entity = 'unknown'
    act(() => {
      rerender(
        <StrictMode>
          <RequirePermission view={ADMIN_VIEW}>
            <Protected />
          </RequirePermission>
        </StrictMode>,
      )
    })
    caps.entity = 'allowed'
    act(() => {
      rerender(
        <StrictMode>
          <RequirePermission view={ADMIN_VIEW}>
            <Protected />
          </RequirePermission>
        </StrictMode>,
      )
    })
    expect(probe.taken?.edits).toEqual({ name: 'twice mounted' })
  })
})

// ⛔ THE OWNER'S LIFE, CHECKED AT EVERY CALL — root's return of 2026-09-07.
//
// The first three cases are root's own controls, reproduced verbatim in intent: they FAILED
// on the delivered source, on clean tracked files and without a mutation. The methods were
// built in a memo that CAPTURED `admitted`, `terminal` and `generation`, the live reader
// kept only the identity current, and an empty capsule made the eligibility check answer
// "yes" without accrediting the owner at all. So a retained `take` still delivered during
// the gap; a retained `publish` repopulated the payload after a close and BLOCKED the new
// opening from saving anything; and both went on working after the owner had unmounted.
//
// They are permanent cases now, with the two the return also asks for — the empty owner
// against a moved identity, and a stale discard against a newer opening — and the positive
// property beside them, because a fence that refuses everything would pass all five.
describe('the owner’s life is checked at every call', () => {
  it('ROOT 1 — a callback retained before `unknown` cannot deliver during the gap', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'operator edit' }))
    const readBeforeGap = probe.take
    answer(rerender, 'unknown')
    expect(readBeforeGap()).toBeNull()
  })

  it('POSITIVE CONTROL — the same-context gap still returns the text to the next admitted mount', () => {
    // The refusal above is about the CALLER, never about the text. This case asserts only
    // the property being protected, so it passes on the returned source as well as on this
    // one: a fence that refused everything would satisfy the three controls and destroy
    // the feature, and this is what says it did not.
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'operator edit' }))
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken?.edits).toEqual({ name: 'operator edit' })
    expect(probe.take()).toEqual({ edits: { name: 'operator edit' } })
  })

  it('ROOT 2 — a stale write after a close cannot stop the current opening from saving', () => {
    mountGate()
    act(() => probe.publish({ name: 'old edit' }))
    const oldWrite = probe.publish
    act(() => probe.discard())
    act(() => oldWrite({ name: 'late old edit' }))
    act(() => probe.publish({ name: 'current operator edit' }))
    expect(probe.take()).toEqual({ edits: { name: 'current operator edit' } })
  })

  it('ROOT 3 — callbacks retained across the owner’s unmount recreate and deliver nothing', () => {
    const mounted = mountGate()
    const oldWrite = probe.publish
    const oldRead = probe.take
    act(() => probe.publish({ name: 'old edit' }))
    mounted.unmount()
    act(() => oldWrite({ name: 'late edit after route leave' }))
    expect(oldRead()).toBeNull()
  })

  it('the EMPTY owner is not a free pass: a first publish after an unseen identity move is refused', () => {
    // Nothing is held yet, so no payload can vouch for anything. The owner's own view of
    // the identity is what the write is contrasted against — and it has not seen this move.
    const { rerender } = mountGate()
    act(() => {
      caps.context = {
        ...(caps.context as Record<string, unknown>),
        lifetime: 9,
      }
    })
    act(() =>
      probe.publish({
        name: 'written under an authority the owner has not seen',
      }),
    )
    expect(probe.take()).toBeNull()
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('a stale discard does not end the opening that replaced it', () => {
    mountGate()
    act(() => probe.publish({ name: 'first opening' }))
    const staleDiscard = probe.staleDiscard!
    act(() => probe.discard())
    act(() => probe.publish({ name: 'second opening' }))
    // The old closure fires its own close, late. It ended its own opening long ago.
    act(() => staleDiscard())
    expect(probe.take()).toEqual({ edits: { name: 'second opening' } })
  })
})

// ⛔ IDENTITY IS `CapabilityContext`, NOT A SCOPE STRING. Every case below moves ONE field
//    of the context the engine's own observations are bound to and requires the opening to
//    end. An implementation that keyed the capsule on the communications scope alone —
//    principal id, tenant, credential generation, workspace — passes four of these five
//    and fails the two that matter most: a principal CLASS change (an API token minted by
//    a user carries that user's id) and A→B→A, where every value returns equal and the
//    authority did not.
describe('the identity fence', () => {
  const move = (
    over: Partial<{
      principalKind: string
      actor: string
      tenant: string
      credentialGeneration: number
      workspace: string
      lifetime: number
    }>,
  ) => {
    caps.context = { ...(caps.context as Record<string, unknown>), ...over }
  }

  it.each([
    ['the principal class', { principalKind: 'api_token' }],
    ['the actor', { actor: 'user:u-b' }],
    ['the tenant', { tenant: 't2' }],
    ['the credential generation', { credentialGeneration: 1 }],
    ['the workspace', { workspace: '0192f2c0-aaaa-7000-8000-000000000002' }],
  ])('%s moving ends the opening', (_what, over) => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'before the move' }))
    act(() => move(over as never))
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('A→B→A: every value returns equal and the movement counter does not, so nothing returns', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'before the round trip' }))
    // Away and back: tenant t1 → t2 → t1, with the local movement counter advanced by the
    // two moves exactly as the capability layer advances it.
    act(() => move({ tenant: 't2', lifetime: 1 }))
    answer(rerender, 'unknown')
    act(() => move({ tenant: 't1', lifetime: 2 }))
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('⛔ a context that moves BETWEEN renders is caught at the write and at the delivery', () => {
    // The window this closes: the boundary's own watch runs when it renders, and a
    // retained callback fires whenever it likes. So both entry points re-read the LIVE
    // context and compare it with the one the capsule was written under — no render, and
    // no effect, stands between the move and the refusal.
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'before the move' }))
    const write = probe.publish
    const read = probe.take
    // The authority moves and NOTHING re-renders: no new answer, no new props.
    act(() => {
      move({ lifetime: 7 })
    })
    expect(read()).toBeNull()
    act(() => write({ name: 'written after the move' }))
    // …and the refusal did not quietly replace the capsule either.
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })

  it('CONTROL: the same round trip with nothing moving DOES return the draft', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'nothing moved' }))
    answer(rerender, 'unknown')
    answer(rerender, 'allowed')
    expect(probe.taken?.edits).toEqual({ name: 'nothing moved' })
  })

  it('the context disappearing (logged out) ends it', () => {
    const { rerender } = mountGate()
    act(() => probe.publish({ name: 'before the logout' }))
    act(() => {
      caps.context = null
    })
    answer(rerender, 'unknown')
    act(() => {
      caps.context = {
        principalKind: 'user',
        actor: 'user:u-a',
        tenant: 't1',
        credentialGeneration: 1,
        workspace: WS,
        lifetime: 3,
      }
    })
    answer(rerender, 'allowed')
    expect(probe.taken).toBeNull()
  })
})

// The whole room, through the gate, with the real sheet and the real configuration form.
describe('the real administration sheet across a budget edge', () => {
  const sheet = () => (
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
  )
  /** The room, plus one control OUTSIDE the gate — where a sidebar or a workspace picker
   *  lives, and where the operator can still go while the room is suspended. */
  const room = () => (
    <>
      <button type="button" data-slot="outside-the-gate">
        outside
      </button>
      <RequirePermission view={ADMIN_VIEW}>{sheet()}</RequirePermission>
    </>
  )
  const nameOf = (dialog: HTMLElement) =>
    within(dialog).getByRole('textbox', { name: 'Name' })
  async function typedAndSuspended(
    rerender: () => void,
    typed = 'Typed under the gate',
  ) {
    const dialog = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(
        dialog.querySelector('[data-slot="admin-etag"]')?.textContent,
      ).toBe('"v2"'),
    )
    const user = userEvent.setup()
    const name = nameOf(dialog)
    await user.clear(name)
    await user.type(name, typed)
    expect(document.activeElement).toBe(name)
    caps.entity = 'unknown'
    act(() => rerender())
    expect(screen.queryByRole('dialog')).toBeNull()
    return { typed, user }
  }

  it('keeps the typed name and the touched intention across a real teardown, says the confirmation was interrupted ONCE, and sends nothing', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    api.updateChannel.mockResolvedValue(adminOutcomeOf())
    const { result, rerender } = renderWithQuery(() => (
      <RequirePermission view={ADMIN_VIEW}>{sheet()}</RequirePermission>
    ))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(
        dialog.querySelector('[data-slot="admin-etag"]')?.textContent,
      ).toBe('"v2"'),
    )
    const user = userEvent.setup()
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'Renamed under the gate')
    await user.click(
      within(dialog).getByRole('button', { name: 'Review changes' }),
    )
    expect(dialog.querySelector('[data-slot="config-confirm"]')).not.toBeNull()
    ;(toast.warning as ReturnType<typeof vi.fn>).mockClear()

    // The budget edge. The gate removes the room; the sheet does not get to say anything,
    // because the sheet is gone too.
    caps.entity = 'unknown'
    act(() => rerender())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-unavailable"]'),
    ).not.toBeNull()
    expect(toast.warning).not.toHaveBeenCalled()

    // The next answer: the room is rebuilt, the operator's own text is in the field, the
    // confirmation is NOT, and the interruption is explained exactly once.
    caps.entity = 'allowed'
    act(() => rerender())
    const back = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(within(back).getByRole('textbox', { name: 'Name' })).toHaveValue(
        'Renamed under the gate',
      ),
    )
    expect(back.querySelector('[data-slot="config-confirm"]')).toBeNull()
    const said = (toast.warning as ReturnType<typeof vi.fn>).mock.calls.map(
      (c) => String(c[0]),
    )
    expect(said).toHaveLength(1)
    expect(said[0]).toMatch(/edits are preserved and nothing was sent/)
    expect(said[0]).not.toMatch(/permission behind it was lost/)
    expect(api.updateChannel).not.toHaveBeenCalled()

    // And the restored edit is an INTENTION: a new review sends that field and no other,
    // under the ETag of the read on screen, after a fresh exact preflight.
    await user.click(
      within(back).getByRole('button', { name: 'Review changes' }),
    )
    await user.click(
      within(back).getByRole('button', { name: 'Confirm changes' }),
    )
    await waitFor(() => expect(api.updateChannel).toHaveBeenCalledTimes(1))
    const intent = api.updateChannel.mock.calls[0][0] as UpdateChannelIntent
    expect(intent.etag).toBe('"v2"')
    expect(intent.body).toEqual({
      channel_id: CHANNEL_ID,
      name: 'Renamed under the gate',
    })
    expect(caps.preflighted).toContain('PATCH /v1/m/sessions/channels')
    result.unmount()
  })

  it('the caret returns to the control it was in, with the text intact', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    const { rerender } = renderWithQuery(room)
    const { typed } = await typedAndSuspended(rerender)
    // Nothing of the room is focused any more; the operator did not move the caret.
    expect(document.activeElement).toBe(document.body)

    caps.entity = 'allowed'
    act(() => rerender())
    const back = await screen.findByRole('dialog')
    await waitFor(() => expect(nameOf(back)).toHaveValue(typed))
    await waitFor(() => expect(document.activeElement).toBe(nameOf(back)))
    // And it is a caret, not a selection: the operator continues typing where they were.
    const field = nameOf(back) as HTMLInputElement
    expect(field.selectionStart).toBe(typed.length)
  })

  it('CONTROL: it does NOT take the caret back when the operator moved it', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    const { rerender } = renderWithQuery(room)
    const { typed } = await typedAndSuspended(rerender)
    // While the room is suspended the operator goes somewhere else — a control that stays
    // mounted precisely because it is outside the cut.
    const outside = document.querySelector(
      '[data-slot="outside-the-gate"]',
    ) as HTMLButtonElement
    act(() => outside.focus())

    caps.entity = 'allowed'
    act(() => rerender())
    const back = await screen.findByRole('dialog')
    await waitFor(() => expect(nameOf(back)).toHaveValue(typed))
    // Long enough for the deferred restore to have run if it were going to.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20))
    })
    expect(document.activeElement).toBe(outside)
  })

  it('an established refusal during the edit takes the draft with it, and a later positive starts clean', async () => {
    api.listChannelGrants.mockResolvedValue(grantsPageOf())
    const { rerender } = renderWithQuery(() => (
      <RequirePermission view={ADMIN_VIEW}>{sheet()}</RequirePermission>
    ))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(
        dialog.querySelector('[data-slot="admin-etag"]')?.textContent,
      ).toBe('"v2"'),
    )
    const user = userEvent.setup()
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'Refused mid-edit')

    caps.entity = 'denied'
    act(() => rerender())
    expect(screen.queryByRole('dialog')).toBeNull()

    caps.entity = 'allowed'
    act(() => rerender())
    const back = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(within(back).getByRole('textbox', { name: 'Name' })).toHaveValue(
        'Ops',
      ),
    )
  })
})
