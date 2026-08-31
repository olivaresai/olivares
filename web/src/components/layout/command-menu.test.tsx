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
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

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
  can: () => true,
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
import { useCommandStore } from '@/stores/command'

afterEach(() => {
  navigateMock.mockReset()
  searchConsoleMock.mockReset()
  authState.activeTenant = 'tnt-1'
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

    await user.type(screen.getByRole('combobox'), 'billing')
    await new Promise((r) => setTimeout(r, 400))
    expect(searchConsoleMock).not.toHaveBeenCalled()
    // The palette is still a palette: its navigation entries are all there.
    expect(screen.getAllByRole('option').length).toBeGreaterThan(0)
  })
})
