// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE PROJECTION, MEASURED WHERE THE FOUR SURFACES ACTUALLY SHARE IT.
//
// The sidebar, the ⌘K palette, the area directories and the `g`-shortcuts do not each
// implement visibility: they call `visibleAreas`, `authorizedSections`,
// `authorizedEntries` and `navigable` with the gate this module builds. So the property
// "the three agree" is a property of THAT gate, and it is measured here, once, on the real
// registry — rather than four times through four component harnesses, where a green result
// would mostly be evidence that four mocks were configured the same way.
//
// The route gate is measured on its own, because it answers something navigation cannot:
// pending, concealed, and an entity deep link that outranks a refused collection.
import { render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import type { CapabilityAccess } from '@/lib/auth/capabilities'
import { useWorkspaceStore } from '@/stores/workspace'

const WS = '0192f2c0-aaaa-7000-8000-000000000001'
const CH = '0192f2c0-bbbb-7000-8000-000000000001'
const ADMIN_VIEW = 'communicationsAdministration'
const ADMIN_PERMISSION = 'sessions:channel:admin'

const auth = vi.hoisted(() => ({ perms: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => auth.perms.has(p),
    principal: { kind: 'user', user_id: 'u-a', actor: 'user:u-a' },
    activeTenant: 't1',
  }),
}))

const caps = vi.hoisted(() => ({
  surface: 'checking' as CapabilityAccess,
  entity: 'checking' as CapabilityAccess,
  asked: [] as { kind: string; operation: string }[],
}))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    useCapability: (q: { kind: string; operation: string } | null) => {
      if (!q) return { access: 'unknown', permit: null, question: null }
      caps.asked.push({ kind: q.kind, operation: q.operation })
      const access = q.kind === 'surface' ? caps.surface : caps.entity
      return { access, permit: null, question: q }
    },
  }
})

const routerSearch = vi.hoisted(() => ({ value: '' }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { searchStr: routerSearch.value, pathname: '/x' } }),
}))

import {
  authorizedEntries,
  authorizedSections,
  buildNavSearchIndex,
  visibleAreas,
  type ViewGate,
} from './model'
import i18n from 'i18next'
import {
  useRouteAccess,
  useViewAccess,
  type RouteAccess,
} from './authorization'

const view = (id: string): FeatureView => {
  const found = FEATURE_VIEWS.find((v) => v.id === id)
  if (!found) throw new Error(`no such view: ${id}`)
  return found
}

/** The gate the four navigation surfaces receive, taken from a real render. */
function gate(): ViewGate {
  let captured: ViewGate | null = null
  function Probe() {
    captured = useViewAccess().navigable
    return null
  }
  render(<Probe />)
  if (!captured) throw new Error('the projection did not render')
  return captured
}

function routeAccess(v: FeatureView): RouteAccess {
  let captured: RouteAccess | null = null
  function Probe() {
    captured = useRouteAccess(v, routerSearch.value)
    return null
  }
  render(<Probe />)
  if (!captured) throw new Error('the route gate did not render')
  return captured
}

/** Whether the administration door is offered by each of the three projections that
 *  consume this gate — computed exactly as sidebar, palette and directory compute it. */
function offeredBy(g: ViewGate): {
  sidebar: boolean
  palette: boolean
  directory: boolean
} {
  // The same `t` the model's own suite uses: the index needs labels to rank, and this
  // battery is about membership, which does not depend on the language.
  const index = buildNavSearchIndex(i18n.getFixedT(null, 'nav') as never)
  return {
    sidebar: visibleAreas(g).some((a) => a.id === 'work-communications'),
    palette: authorizedEntries(index, g).some((e) => e.id === ADMIN_VIEW),
    directory: authorizedSections('work-communications', g)
      .flatMap((s) => s.views)
      .some((v) => v.id === ADMIN_VIEW),
  }
}

beforeEach(() => {
  auth.perms = new Set()
  caps.surface = 'checking'
  caps.entity = 'checking'
  caps.asked = []
  routerSearch.value = ''
  useWorkspaceStore.setState({
    activeWorkspace: WS,
    activeWorkspaceName: 'Billing',
  })
})

describe('the shared projection', () => {
  it("offers the administration door on the ENGINE's admission while the reflection says no", () => {
    // ⛔ THE HALF WHOAMI CANNOT EXPRESS, from the console side. The permission set does NOT
    //    contain sessions:channel:admin — a workspace-scoped authored grant never appears
    //    in it — and the door is offered anyway, identically in all three projections.
    expect(auth.perms.has(ADMIN_PERMISSION)).toBe(false)
    caps.surface = 'reachable'
    expect(offeredBy(gate())).toEqual({
      sidebar: true,
      palette: true,
      directory: true,
    })
    // And the question actually asked was the administration SURFACE, not a permission.
    expect(caps.asked).toContainEqual({
      kind: 'surface',
      operation: 'GET /v1/m/sessions/channels/administration',
    })
  })

  it('removes it from all three on an ESTABLISHED not_reachable, whatever the reflection says', () => {
    // The reflection is granted here, which is the other half: a permission the engine
    // reflects while an authored policy forbids the operation. The door disappears.
    auth.perms = new Set([ADMIN_PERMISSION])
    caps.surface = 'not_reachable'
    const offered = offeredBy(gate())
    expect(offered.palette).toBe(false)
    expect(offered.directory).toBe(false)
    // The AREA survives only if another leaf does; with an empty set it does not.
    expect(offered.sidebar).toBe(false)
  })

  it('keeps the installed link during every UNKNOWN, in all three, and never converts one to a refusal', () => {
    for (const access of [
      'checking',
      'unknown',
      'undisclosed',
      'step_up_required',
    ] as CapabilityAccess[]) {
      caps.surface = access
      expect(offeredBy(gate()), access).toEqual({
        sidebar: true,
        palette: true,
        directory: true,
      })
    }
  })

  it('keeps the link when there is nothing to ask, and asks nothing', () => {
    // No workspace selected: the question is null. That is "I have not asked", which is an
    // unknown — not a refusal, and not an ask with a fabricated scope.
    useWorkspaceStore.setState({
      activeWorkspace: null,
      activeWorkspaceName: null,
    })
    expect(offeredBy(gate()).palette).toBe(true)
    expect(caps.asked).toHaveLength(0)
  })

  it('still answers every ORDINARY view from the reflection, unchanged', () => {
    auth.perms = new Set(['governance:routine:read'])
    const g = gate()
    expect(g(view('routinePolicies'))).toBe(true)
    expect(g(view('permissions'))).toBe(false)
    // A view with no permission at all stays open to every signed-in principal.
    expect(g(view('dashboards'))).toBe(true)
  })

  it('adds no administration shortcut: the `g` sequences are the ones that already existed', async () => {
    const { NAV_SHORTCUTS } = await import('@/components/layout/shortcuts')
    expect(NAV_SHORTCUTS.map((s) => s.featureId)).not.toContain(ADMIN_VIEW)
  })
})

describe('the route gate', () => {
  it('renders each answer as what it is, and never an unknown as Forbidden', () => {
    const cases: [CapabilityAccess, RouteAccess['kind']][] = [
      ['reachable', 'permitted'],
      ['not_reachable', 'forbidden'],
      ['undisclosed', 'undisclosed'],
      ['checking', 'checking'],
      ['unknown', 'unavailable'],
      ['step_up_required', 'unavailable'],
    ]
    for (const [access, expected] of cases) {
      caps.surface = access
      expect(routeAccess(view(ADMIN_VIEW)).kind, access).toBe(expected)
    }
  })

  it('an entity deep link asks its OWN operation and outranks a REFUSED collection', () => {
    // The scoped-grant shape: admitted to one row, refused the list. The route renders the
    // sheet, and it asks the exact GET-grants operation — never the collection.
    caps.surface = 'not_reachable'
    caps.entity = 'allowed'
    routerSearch.value = `?admin_channel=${CH}`
    expect(routeAccess(view(ADMIN_VIEW)).kind).toBe('permitted')
    expect(caps.asked).toEqual([
      {
        kind: 'operation',
        operation: 'GET /v1/m/sessions/channels/{id}/grants',
      },
    ])
    // ⛔ THE CONTROL THAT MAKES THAT MEAN SOMETHING: the surface question was NOT asked.
    expect(caps.asked.some((q) => q.kind === 'surface')).toBe(false)
  })

  // ⛔ THE DECISION IS COARSER THAN THE ANSWER, AND ONE CONSUMER NEEDS THE DIFFERENCE.
  //    `unknown` and `step_up_required` are the same `kind` and are not the same fact: the
  //    first is this client's own verdict on an expiry — the engine said nothing — and the
  //    second is a gate the engine decided before any lookup. The route hands both down
  //    beside its decision so the continuity boundary can keep an operator's unsent text
  //    across the first and end it on the second. Nothing here changes what is rendered.
  it('carries the exact answer and its question beside the decision, without changing it', () => {
    const cases: [CapabilityAccess, RouteAccess['kind']][] = [
      ['reachable', 'permitted'],
      ['not_reachable', 'forbidden'],
      ['undisclosed', 'undisclosed'],
      ['checking', 'checking'],
      ['unknown', 'unavailable'],
      ['step_up_required', 'unavailable'],
    ]
    for (const [access, kind] of cases) {
      caps.surface = access
      const answer = routeAccess(view(ADMIN_VIEW))
      expect(answer.kind, access).toBe(kind)
      expect(answer.observed, access).toBe(access)
      expect(answer.question?.operation).toBe(
        'GET /v1/m/sessions/channels/administration',
      )
    }
    // The two that share a `kind` do NOT share an answer, which is the whole point.
    caps.surface = 'unknown'
    const expired = routeAccess(view(ADMIN_VIEW))
    caps.surface = 'step_up_required'
    const gated = routeAccess(view(ADMIN_VIEW))
    expect(expired.kind).toBe(gated.kind)
    expect(expired.observed).not.toBe(gated.observed)
  })

  it('a deep link answer is carried with ITS own question, not the collection’s', () => {
    caps.surface = 'not_reachable'
    caps.entity = 'unknown'
    routerSearch.value = `?admin_channel=${CH}`
    const answer = routeAccess(view(ADMIN_VIEW))
    expect(answer.kind).toBe('unavailable')
    expect(answer.observed).toBe('unknown')
    expect(answer.question?.operation).toBe(
      'GET /v1/m/sessions/channels/{id}/grants',
    )
  })

  it('a route decided by the REFLECTION carries no answer and no question', () => {
    auth.perms = new Set(['governance:identity:read'])
    const ordinary = FEATURE_VIEWS.find((v) => v.id === 'permissions')!
    const answer = routeAccess(ordinary)
    expect(answer.kind).toBe('permitted')
    expect(answer.observed).toBeNull()
    expect(answer.question).toBeNull()
  })

  it('an INVALID deep link is not a deep link: the collection question decides', () => {
    caps.surface = 'not_reachable'
    caps.entity = 'allowed'
    for (const bad of ['?admin_channel=not-a-uuid', '?admin_channel=', '']) {
      caps.asked = []
      routerSearch.value = bad
      expect(routeAccess(view(ADMIN_VIEW)).kind, bad).toBe('forbidden')
      expect(
        caps.asked.every((q) => q.kind === 'surface'),
        bad,
      ).toBe(true)
    }
  })

  it("a refused entity deep link is refused on its own answer, not on the collection's", () => {
    caps.surface = 'reachable'
    caps.entity = 'undisclosed'
    routerSearch.value = `?admin_channel=${CH}`
    // The collection is reachable and the row is concealed: the route says the neutral
    // thing, not "welcome in" and not "forbidden".
    expect(routeAccess(view(ADMIN_VIEW)).kind).toBe('undisclosed')
  })

  it('renders the room when there is NOTHING to ask, and asks nothing', () => {
    // No workspace: the surface question does not exist, so there is no protected child to
    // gate — the collection cannot be requested without the scope the question carries, and
    // the room renders its own "select a workspace" state. Refusing here would be a false
    // statement about authority and would hide the only control that fixes it.
    useWorkspaceStore.setState({
      activeWorkspace: null,
      activeWorkspaceName: null,
    })
    caps.surface = 'not_reachable'
    expect(routeAccess(view(ADMIN_VIEW)).kind).toBe('permitted')
    expect(caps.asked).toHaveLength(0)
  })

  it('leaves every ordinary route on the reflection, permitting and forbidding as before', () => {
    auth.perms = new Set(['governance:routine:read'])
    expect(routeAccess(view('routinePolicies')).kind).toBe('permitted')
    expect(routeAccess(view('permissions')).kind).toBe('forbidden')
    expect(routeAccess(view('dashboards')).kind).toBe('permitted')
  })
})
