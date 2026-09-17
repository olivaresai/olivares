// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E4a — the ⌘K palette must honour hideInNav: a hidden view is deep-link-only
// with a parameterized path (e.g. /session-viewer/$id), and offering it would
// navigate to the LITERAL "$id" segment (a 404). The anti-regression assertion is
// structural: no palette entry may target a path containing a "$" placeholder.
// Adds the federated search section (GET /v1/search) and per-item
// descriptions; both are covered below.
import { QueryClient } from '@tanstack/react-query'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderIntel } from '@/test/intel'

const navigateMock = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  useNavigate: () => navigateMock,
}))

// Superadmin-shaped: every permission granted, so ONLY hideInNav can filter. The
// tenant is mutable because the federated search is TENANT-SCOPED: with none
// selected the engine can only answer 400 "tenant required", so the palette must not
// ask — see the last test in this file.
const authState = vi.hoisted(() => ({
  activeTenant: 'tnt-1' as string | null,
  // Mutable, and an honest predicate rather than a constant: the palette-action cases
  // below hand it a principal who holds the three READ permissions and not the writes,
  // which is the exact principal the affordance defect was measured on.
  can: (_permission: string): boolean => true,
  logout: vi.fn(),
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const searchConsoleMock = vi.fn()
vi.mock('@/lib/api/search', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/search')>()
  return {
    ...actual,
    searchConsole: (q: string) => searchConsoleMock(q),
  }
})

import { CommandMenu } from './command-menu'
import { FEATURE_VIEWS } from '@/features/registry'
import { queryKeys } from '@/lib/api/query'
import { useCommandStore } from '@/stores/command'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'

/** The three page permissions, which is all a reader of these pages holds. */
const READS = [
  'eventing:subscription:read',
  'notify:route:read',
  'orchestration:graph:read',
]
/** The three permissions the ENGINE requires for the writes those verbs open. */
const WRITES = [
  'eventing:subscription:write',
  'notify:route:write',
  'orchestration:schedule:write',
]

/** A principal holding exactly `permissions`, and the navigation authority for the rest of
 *  the console (so these cases measure the ACTION filter, not an empty palette). */
function holding(permissions: string[]) {
  const held = new Set(permissions)
  authState.can = (permission: string) =>
    held.has(permission) || !isPaletteActionPermission(permission)
}
const isPaletteActionPermission = (permission: string) =>
  READS.includes(permission) || WRITES.includes(permission)

/**
 * A client that has observed a principal — i.e. an ESTABLISHED capability context.
 *
 * Built here rather than with `createTestQueryClient()` for one measured reason: that
 * helper sets `gcTime: 0`, and a `setQueryData` entry with no observer and no gc time is
 * collected on the next tick. The principal then vanishes between the render and the
 * click, and the case would pass for the wrong reason — as "no context", not as "no
 * permission".
 */
function establishedClient() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, {
    kind: 'user',
    user_id: 'u-1',
    actor: 'u-1',
    display_name: 'Ada',
    superadmin: false,
    grants: [],
  })
  return qc
}

/** Open the palette on a query client whose identity is (or is not) established. */
function openPalette(qc = establishedClient()) {
  useCommandStore.getState().setOpen(true)
  renderIntel(<CommandMenu />, { queryClient: qc })
  return qc
}

const optionValues = () =>
  screen.getAllByRole('option').map((o) => o.getAttribute('data-value') ?? '')

afterEach(() => {
  navigateMock.mockReset()
  searchConsoleMock.mockReset()
  authState.activeTenant = 'tnt-1'
  authState.can = () => true
  useTenantStore.setState({ activeTenant: 'tnt-1' })
  useSessionStore.setState({ credentialGeneration: 0 })
  useWorkspaceStore.setState({ activeWorkspace: null })
  useCommandStore.setState({ pendingAction: null, opener: null })
  useCommandStore.getState().setOpen(false)
})

describe('CommandMenu', () => {
  it('lists no hideInNav view and therefore no parameterized route', () => {
    // The registry MUST contain at least one hidden, parameterized view
    // (session-viewer) or this test would pass vacuously.
    const hidden = FEATURE_VIEWS.filter((v) => v.hideInNav)
    expect(hidden.length).toBeGreaterThan(0)
    expect(hidden.some((v) => v.path.includes('$'))).toBe(true)

    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    const options = screen.getAllByRole('option')
    const values = options.map((o) => o.getAttribute('data-value') ?? '')

    for (const v of hidden) {
      // The palette keys items by `<label> <id>` — a hidden id must not appear.
      expect(values.some((val) => val.includes(v.id))).toBe(false)
    }
    // Every visible view IS offered (the filter only removes hidden ones).
    for (const v of FEATURE_VIEWS.filter((x) => !x.hideInNav)) {
      expect(values.some((val) => val.includes(v.id))).toBe(true)
    }
  })

  it('searches the federated endpoint after a pause and navigates to the hit', async () => {
    const user = userEvent.setup()
    searchConsoleMock.mockResolvedValue({
      results: [
        {
          kind: 'eventing.subscription',
          id: 's1',
          name: 'Billing-Alerts',
          detail: 'enabled',
        },
      ],
      truncated: false,
    })

    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    await user.type(screen.getByRole('combobox'), 'billing')
    await waitFor(() =>
      expect(searchConsoleMock).toHaveBeenCalledWith('billing'),
    )

    const hit = await screen.findByText('Billing-Alerts')
    expect(hit).toBeInTheDocument()
    await user.click(hit)
    expect(navigateMock).toHaveBeenCalledWith({ to: '/eventing' })
    // Selecting a result closes the palette.
    expect(useCommandStore.getState().open).toBe(false)
  })

  it('does not hit the endpoint below the minimum query length', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    await user.type(screen.getByRole('combobox'), 'b')
    // Give the debounce time to elapse; a request now would be a regression.
    await new Promise((r) => setTimeout(r, 400))
    expect(searchConsoleMock).not.toHaveBeenCalled()
  })

  // A DEGRADED SEARCH WITH NO HITS MUST STILL SAY SO (2026-08-06). The warning was first
  // rendered INSIDE the `searchHits.length > 0` block, so it appeared only when something
  // else had matched — and stayed silent in the one case that matters most: the failed
  // provider was the only one that would have matched, so `{results: [], degraded: true}`
  // drew the ordinary "no results" screen. An incomplete list shown as an empty one is
  // exactly the defect the flag exists to remove, surviving in the UI after the API was
  // fixed. Found by the adversarial contrast, not by this suite.
  it('warns that the search is incomplete even when it returned no hits', async () => {
    const user = userEvent.setup()
    searchConsoleMock.mockResolvedValue({
      results: [],
      truncated: false,
      degraded: true,
      degraded_kinds: ['audit'],
    })

    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    await user.type(screen.getByRole('combobox'), 'billing')
    await waitFor(() =>
      expect(searchConsoleMock).toHaveBeenCalledWith('billing'),
    )

    // The i18n key resolves to prose about the list being incomplete; assert on the key's
    // rendered text rather than the key, because a missing translation would otherwise pass.
    expect(await screen.findByText(/incomplet/i)).toBeInTheDocument()
  })

  // ...and the other direction, so the warning cannot become permanent furniture.
  it('does not warn when every source answered', async () => {
    const user = userEvent.setup()
    searchConsoleMock.mockResolvedValue({
      results: [],
      truncated: false,
      degraded: false,
      degraded_kinds: [],
    })

    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    await user.type(screen.getByRole('combobox'), 'billing')
    await waitFor(() =>
      expect(searchConsoleMock).toHaveBeenCalledWith('billing'),
    )

    expect(screen.queryByText(/incomplet/i)).not.toBeInTheDocument()
  })

  it('does not search at all while no organization is selected', async () => {
    // GET /v1/search resolves a tenant like every other scoped route
    // (core/api/search.go handleSearch): with none selected it can only answer
    // 400 "tenant required", so the palette must hold the request back. The
    // navigation half of the palette keeps working — only the data search waits.
    authState.activeTenant = null
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)

    // A term that names a module: the navigation half must keep answering while the
    // data search is held back (N1 ranks the list itself, so an unmatched term shows
    // the empty state — which is a different fact from "the palette stopped working").
    await user.type(screen.getByRole('combobox'), 'audit')
    await new Promise((r) => setTimeout(r, 400))
    expect(searchConsoleMock).not.toHaveBeenCalled()
    // The palette is still a palette: its navigation entries are all there.
    expect(screen.getAllByRole('option').length).toBeGreaterThan(0)
  })

  // N1 — the palette shares the sidebar's index and ranking.
  it('ranks the exact label first and shows Area › Section beside each module', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), 'models')
    const options = screen.getAllByRole('option')
    const values = options.map((o) => o.getAttribute('data-value') ?? '')
    expect(values[0]).toMatch(/^view:models /)
    expect(
      values.indexOf(values.find((v) => v.startsWith('view:modelOps'))!),
    ).toBeLessThan(
      values.indexOf(values.find((v) => v.startsWith('view:finops'))!),
    )
    expect(options[0]).toHaveTextContent('AI › Models')
  })

  it('offers the areas as destinations, only those with an authorized leaf', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), 'security')
    const options = screen.getAllByRole('option')
    const values = options.map((o) => o.getAttribute('data-value') ?? '')
    // The exact-label module comes first, the area right behind it, tagged as an area.
    expect(values[0]).toMatch(/^view:security /)
    expect(values[1]).toMatch(/^area:security-identity /)
    expect(options[1]).toHaveTextContent('Area')
    await user.click(options[1])
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/areas/security-identity',
    })
  })

  it('finds the Claude Code doors by their former name and the administration console by its old one', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), 'claude code')
    let values = screen
      .getAllByRole('option')
      .map((o) => o.getAttribute('data-value') ?? '')
    expect(values.some((v) => v.startsWith('view:agentops '))).toBe(true)
    expect(values.some((v) => v.startsWith('view:claudePolicy '))).toBe(true)
    await user.clear(screen.getByRole('combobox'))
    await user.type(screen.getByRole('combobox'), 'control console')
    values = screen
      .getAllByRole('option')
      .map((o) => o.getAttribute('data-value') ?? '')
    expect(values[0]).toMatch(/^view:console /)
  })

  it('keeps the three command actions for a principal who holds their writes', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    expect(optionValues()).toEqual(
      expect.arrayContaining([
        expect.stringMatching(/^action:eventing:createSubscription /),
        expect.stringMatching(/^action:alerting:createRoute /),
        expect.stringMatching(/^action:orchestration:createSchedule /),
      ]),
    )
    await user.click(screen.getByRole('option', { name: /New alert route/ }))
    // The command now carries the identity it was chosen in — and nothing else: no
    // token, no payload, no permit. `CapabilityContext` has no field that could hold one.
    expect(useCommandStore.getState().pendingAction).toEqual({
      featureId: 'alerting',
      action: 'createRoute',
      context: expect.objectContaining({
        principalKind: 'user',
        actor: 'u-1',
        tenant: 'tnt-1',
        credentialGeneration: 0,
        workspace: null,
        lifetime: expect.any(Number),
      }),
    })
    expect(navigateMock).toHaveBeenCalledWith({ to: '/alerting' })
    qc.clear()
  })

  // ⛔ THE DEFECT ITSELF (spec04 §1): "An action in the palette requires its mutation
  //    permission, target, and current context; read permission for its page does not
  //    authorize it." A reader of these three pages used to be offered all three verbs.
  it('offers NO creation verb to a principal holding only the page reads', async () => {
    const user = userEvent.setup()
    holding(READS)
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    const values = optionValues()
    for (const verb of [
      'action:eventing:createSubscription',
      'action:alerting:createRoute',
      'action:orchestration:createSchedule',
    ])
      expect(values.some((v) => v.startsWith(verb))).toBe(false)
    // AND THE READ-ONLY PALETTE IS INTACT: the three pages are still reachable and the two
    // non-mutating commands are still offered, because removing a verb must not remove the
    // navigation it happened to sit next to.
    await user.clear(screen.getByRole('combobox'))
    const whole = optionValues()
    for (const entry of [
      'view:alerting ',
      'view:eventing ',
      'view:orchestration ',
      'theme:light ',
      'theme:dark ',
      'signout ',
    ])
      expect(whole.some((v) => v.startsWith(entry))).toBe(true)
    expect(whole.some((v) => v.startsWith('action:'))).toBe(false)
    qc.clear()
  })

  it.each([
    ['notify:route:write', 'action:alerting:createRoute'],
    ['eventing:subscription:write', 'action:eventing:createSubscription'],
    ['orchestration:schedule:write', 'action:orchestration:createSchedule'],
  ])('offers exactly the verb %s buys', async (permission, expected) => {
    const user = userEvent.setup()
    holding([...READS, permission])
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    const verbs = optionValues().filter((v) => v.startsWith('action:'))
    // FIRES IF: one verb's permission is wired to another's — each write buys ITS OWN
    // verb and no other. orchestration is the case that matters most: its page reads
    // `orchestration:graph:read`, a different RESOURCE from the schedule it creates.
    expect(verbs).toHaveLength(1)
    expect(verbs[0]).toMatch(new RegExp(`^${expected} `))
    qc.clear()
  })

  it('offers no creation verb with no tenant selected, and still navigates', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    authState.activeTenant = null
    useTenantStore.setState({ activeTenant: null })
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    expect(optionValues().some((v) => v.startsWith('action:'))).toBe(false)
    await user.clear(screen.getByRole('combobox'))
    await user.type(screen.getByRole('combobox'), 'alert')
    // Navigation, theme and sign-out are not mutations and do not need a tenant.
    await user.click(screen.getByRole('option', { name: /Alerting/i }))
    expect(navigateMock).toHaveBeenCalledWith({ to: '/alerting' })
    qc.clear()
  })

  it('withdraws the offered verb as soon as the grant is revoked', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    expect(
      optionValues().some((v) => v.startsWith('action:alerting:createRoute')),
    ).toBe(true)
    // The revocation lands, and the next render of the list is the console's answer to it.
    holding(READS)
    await user.type(screen.getByRole('combobox'), ' ')
    await user.clear(screen.getByRole('combobox'))
    await user.type(screen.getByRole('combobox'), 'new')
    expect(
      optionValues().some((v) => v.startsWith('action:alerting:createRoute')),
    ).toBe(false)
    expect(
      screen.queryByRole('option', { name: /New alert route/ }),
    ).not.toBeInTheDocument()
    qc.clear()
  })

  // ⛔ WHAT THE SELECTION RE-CHECK CAN AND CANNOT SEE, measured rather than asserted in
  //    prose. The identity is read from its own stores at CALL time, so a tenant that goes
  //    away between the render that listed the verb and the keystroke that selects it is
  //    seen right here, with no re-render in between. `can()` is not: it arrives as the
  //    closure of the last committed render, and re-deriving the permission rule
  //    imperatively would be the second RBAC implementation this console deleted in.
  //    The revocation case above and the target view's own check cover that half.
  it('dispatches nothing when the tenant goes away between listing and selection', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'new')
    const item = screen.getByRole('option', { name: /New alert route/ })
    useTenantStore.setState({ activeTenant: null })
    await user.click(item)
    expect(useCommandStore.getState().pendingAction).toBeNull()
    expect(navigateMock).not.toHaveBeenCalled()
    // AND THE PALETTE IS STILL OPEN: a refused selection spends nothing, so it must not
    // spend the operator's ⌘K either. The list corrects itself on the next render.
    expect(useCommandStore.getState().open).toBe(true)
    qc.clear()
  })

  // ⛔ THE KEYBOARD PATH IS THE ONE THE PALETTE EXISTS FOR, and it is a different code
  //    path from a click: cmdk highlights an item and Enter runs `onSelect`. The verb
  //    filter and the re-check are in that handler, so both paths are pinned.
  it('selects a verb by keyboard, then Escape closes and hands focus back', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const qc = openPalette()
    await user.type(screen.getByRole('combobox'), 'New alert route')
    const option = screen.getByRole('option', { name: /New alert route/ })
    expect(option).toHaveAttribute('data-selected', 'true')
    await user.keyboard('{Enter}')
    expect(useCommandStore.getState().pendingAction).toEqual(
      expect.objectContaining({ featureId: 'alerting', action: 'createRoute' }),
    )
    expect(navigateMock).toHaveBeenCalledWith({ to: '/alerting' })

    // Escape on a REOPENED palette: the dialog closes and the control that opened it gets
    // focus back — the behaviour measured in Chromium on 2026-09-06 and kept here as the
    // seam it runs through (the store's opener, consumed once).
    opener.focus()
    act(() => useCommandStore.getState().setOpen(true))
    expect(useCommandStore.getState().opener).toBe(opener)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(useCommandStore.getState().open).toBe(false))
    await waitFor(() => expect(document.activeElement).toBe(opener))
    opener.remove()
    qc.clear()
  })

  it('dispatches nothing while the identity is still unestablished', async () => {
    const user = userEvent.setup()
    holding([...READS, ...WRITES])
    // A client that has observed no principal: the context cannot be established, so
    // there is nothing to bind a command to. Queuing anyway would leave it for whoever
    // arrives next.
    const qc = openPalette(createTestQueryClient())
    await user.type(screen.getByRole('combobox'), 'new')
    await user.click(screen.getByRole('option', { name: /New alert route/ }))
    expect(useCommandStore.getState().pendingAction).toBeNull()
    expect(navigateMock).not.toHaveBeenCalled()
    expect(useCommandStore.getState().open).toBe(true)
    qc.clear()
  })
})
