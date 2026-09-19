// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the profile administration surface. What these cases pin is the CONTRACT with
// the engine, not the paint: which control issues which request with which body, that
// a role without the tier never issues the request at all (the control is not there),
// that the configuration read is on demand and admin-only, and that a tenant switch
// leaves nothing of the previous tenant on screen. The engine's own facts —
// local_environment, operable, state — are rendered as given, never re-derived.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  perms: new Set<string>(),
  isSuperadmin: false,
  principal: 'u1',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.activeTenant,
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: auth.isSuperadmin,
    principal: { user_id: auth.principal, aal: 1 },
  }),
}))
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  getProfile: vi.fn(),
  createProfile: vi.fn(),
  patchProfile: vi.fn(),
  retireProfile: vi.fn(),
  profileConfiguration: vi.fn(),
  profileLaunchReadiness: vi.fn(),
  listBindings: vi.fn(),
  createBinding: vi.fn(),
  revokeBinding: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})
const consoleApi = vi.hoisted(() => ({ listSources: vi.fn() }))
vi.mock('@/features/console/api', () => ({
  consoleApi,
  consoleKeys: { sources: () => ['console', 'sources'] },
}))
// Only the lazy ceremony PANEL is doubled (the pattern of data-table-step-up.test.tsx):
// the real `StepUpRequiredState` wrapper and the real `TableError` ordering stay under
// test, and the double is a marker, not a decision — which state the table paints for a
// `step_up_required` is decided by the product code, never by this file.
vi.mock('@/features/identity/assurance', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  StepUpPanel: ({ action }: { action: string }) => (
    <span>{`step-up ceremony:${action}`}</span>
  ),
}))

import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { fixtureReadiness } from './launch-readiness.fixture'
import { ProfilesPanel } from './profiles-panel'
import type { ProviderProfileDTO } from './types'
import { HIDDEN_ON_PHONE } from '@/lib/hooks/use-is-phone'

const READ = ['sessions:profile:read', 'sessions:profile-binding:read']
const WRITE = ['sessions:profile:write', 'sessions:profile-binding:write']
const ADMIN = ['sessions:profile:admin', 'sessions:profile-binding:admin']

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
  created_at: '2026-09-01T10:00:00Z',
}
const codexHome: ProviderProfileDTO = {
  profile_ref: 'ppf_codex',
  driver: 'codex',
  environment_ref: 'xenv_1',
  display_name: 'Codex home',
  state: 'active',
  local_environment: true,
  operable: false,
}
const foreign: ProviderProfileDTO = {
  profile_ref: 'ppf_far',
  driver: 'claude',
  environment_ref: 'xenv_other',
  display_name: 'Far away',
  state: 'active',
  local_environment: false,
  operable: false,
}
const disabled: ProviderProfileDTO = {
  ...homeA,
  profile_ref: 'ppf_off',
  display_name: 'Paused',
  state: 'disabled',
  operable: false,
}
const retired: ProviderProfileDTO = {
  ...homeA,
  profile_ref: 'ppf_gone',
  display_name: 'Old home',
  state: 'retired',
  operable: false,
  retired_at: '2026-09-02T10:00:00Z',
}

function grant(...sets: string[][]) {
  auth.perms = new Set(sets.flat())
}

function wrap(pageSurface = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = render(
    <QueryClientProvider client={qc}>
      <ProfilesPanel pageSurface={pageSurface} />
    </QueryClientProvider>,
  )
  return { ...ui, qc }
}

/** The table row that shows `text`, typed for `within()`. */
function rowOf(text: string): HTMLElement {
  const row = screen.getByText(text).closest('tr')
  expect(row).not.toBeNull()
  return row as HTMLElement
}

/** Renders the panel and opens the sheet of the row named `name`. */
async function openSheet(name: RegExp) {
  const user = userEvent.setup()
  const ui = wrap()
  const row = (await screen.findByText(name)).closest('tr')
  expect(row).not.toBeNull()
  await user.click(
    within(row as HTMLElement).getByRole('button', { name: /row actions/i }),
  )
  await user.click(await screen.findByRole('menuitem', { name: /^details$/i }))
  return { user, ...ui }
}

const page = (items: ProviderProfileDTO[]) => ({ items, has_more: false })

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 't1'
  auth.isSuperadmin = false
  auth.principal = 'u1'
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: 'sid-1',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  grant(READ, WRITE, ADMIN)
  api.listProfiles.mockResolvedValue(
    page([homeA, codexHome, foreign, disabled, retired]),
  )
  api.getProfile.mockImplementation(async (ref: string) => {
    const hit = [homeA, codexHome, foreign, disabled, retired].find(
      (p) => p.profile_ref === ref,
    )
    if (!hit) throw new Error('no such profile')
    return hit
  })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.profileConfiguration.mockResolvedValue({
    profile_ref: 'ppf_a',
    driver: 'claude',
    environment_ref: 'xenv_1',
    config_home: '/srv/homes/a/.claude',
    user_home: '/srv/homes/a',
    state: 'active',
  })
  api.createProfile.mockResolvedValue(homeA)
  api.patchProfile.mockResolvedValue(homeA)
  api.retireProfile.mockResolvedValue(retired)
  api.profileLaunchReadiness.mockImplementation(async (ref: string) =>
    fixtureReadiness({ profile_ref: ref }),
  )
})

describe('ProfilesPanel — the list', () => {
  it('lists ONE page with the ceiling and renders the server facts as given', async () => {
    wrap()
    expect(await screen.findByText('Home A')).toBeInTheDocument()
    expect(api.listProfiles.mock.calls[0][0]).toEqual({
      limit: 100,
      cursor: undefined,
    })
    // A Codex profile is local, active and honestly NOT enabled — the server said so.
    const codex = rowOf('Codex home')
    expect(
      within(codex).getByText('Not enabled in this environment'),
    ).toBeInTheDocument()
    expect(within(codex).getByText('this node')).toBeInTheDocument()
    // A foreign profile is shown as another environment, never as enabled here.
    const far = rowOf('Far away')
    expect(within(far).getByText('another environment')).toBeInTheDocument()
    expect(
      within(far).getByText('Not enabled in this environment'),
    ).toBeInTheDocument()
    // Disabled and retired read as what they are.
    expect(within(rowOf('Paused')).getByText('Disabled')).toBeInTheDocument()
    expect(within(rowOf('Old home')).getByText('Retired')).toBeInTheDocument()
    // The ordinary list never asked for a home or an inventory-wide readiness GET.
    expect(api.profileConfiguration).not.toHaveBeenCalled()
    expect(api.profileLaunchReadiness).not.toHaveBeenCalled()
  })

  it('narrows by state on the SERVER, not on the loaded page', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findByText('Home A')
    await user.click(screen.getByRole('combobox', { name: 'All states' }))
    await user.click(await screen.findByRole('option', { name: 'Retired' }))
    await waitFor(() =>
      expect(
        api.listProfiles.mock.calls.map((c) => c[0] as { state?: string }),
      ).toContainEqual({ state: 'retired', limit: 100, cursor: undefined }),
    )
  })

  it('says so, and asks nothing, without sessions:profile:read', async () => {
    grant(WRITE, ADMIN)
    wrap()
    expect(
      await screen.findByText(/does not include sessions:profile:read/),
    ).toBeInTheDocument()
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Register profile' }),
    ).not.toBeInTheDocument()
  })
})

/** The state facet's trigger — a Radix Select trigger is a `button role="combobox"`. */
const stateTrigger = () => screen.getByRole('combobox', { name: 'All states' })
/** The DataTable's own free-text search box (common:actions.search). */
const searchBox = () => screen.getByRole('textbox', { name: 'Search' })
/** The empty-state region that holds `text`, typed for `within()`. */
function emptyStateOf(text: string | RegExp): HTMLElement {
  const region = screen.getByText(text).closest('[data-slot="empty-state"]')
  expect(region).not.toBeNull()
  return region as HTMLElement
}
/** Narrows the list on the server to `label` through the existing state control. */
async function narrowTo(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
) {
  await user.click(stateTrigger())
  await user.click(await screen.findByRole('option', { name: label }))
}
/** A list that answers the SERVER-side state filter: nothing for a state, records for ALL. */
function serverAnswers(
  all: ProviderProfileDTO[],
  byState: Record<string, ProviderProfileDTO[]> = {},
) {
  api.listProfiles.mockImplementation(async (params: { state?: string }) =>
    page(params.state ? (byState[params.state] ?? []) : all),
  )
}

// ⛔ A server-filtered EMPTY page is not an empty ESTATE. `state` narrows the list in
// the request (`params.state`), so `data=[]` under a filter is what the engine said
// about THAT state and says nothing about the others — and DataTable's own filtered
// arm cannot see it, because nothing was filtered on the client. The caller answers,
// with copy that names the filter and one action that drops it: no total is invented,
// no existence is claimed, no read is bypassed, and nothing is written.
describe('ProfilesPanel — a filtered empty page is not an empty estate', () => {
  it('names the state filter, claims nothing about other states, and one action returns the ACTUAL query to ALL', async () => {
    const user = userEvent.setup()
    serverAnswers([homeA, codexHome, foreign, disabled, retired])
    wrap()
    await screen.findByText('Home A')
    await narrowTo(user, 'Retired')
    // The SERVER was asked for that state; the empty page is its answer, not a local cut.
    await waitFor(() =>
      expect(
        api.listProfiles.mock.calls.map((c) => c[0] as { state?: string }),
      ).toContainEqual({ state: 'retired', limit: 100, cursor: undefined }),
    )
    expect(
      await screen.findByText('No profiles match this state'),
    ).toBeInTheDocument()
    const empty = emptyStateOf('No profiles match this state')
    // The copy names the filter value the operator chose and offers exactly ONE thing.
    expect(
      within(empty).getByText(
        'Only profiles in the state “Retired” are listed. Show all states to remove this filter.',
      ),
    ).toBeInTheDocument()
    expect(within(empty).getAllByRole('button')).toHaveLength(1)
    // Not the estate copy, and no existence claim about what the filter hides.
    expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
    expect(empty.textContent).not.toMatch(/exist|other states|\d/)
    // The facet stays on screen, showing what hides the rows.
    expect(stateTrigger()).toHaveTextContent('Retired')

    await user.click(
      within(empty).getByRole('button', { name: 'Show all states' }),
    )
    // The recovery changed the ACTUAL query: the next read carries no state, and the
    // records that state hid are back. The empty state — and its button — are gone.
    await waitFor(() =>
      expect(
        api.listProfiles.mock.calls.filter(
          (c) => !('state' in (c[0] as object)),
        ).length,
      ).toBeGreaterThanOrEqual(2),
    )
    expect(await screen.findByText('Home A')).toBeInTheDocument()
    expect(screen.getByText('Old home')).toBeInTheDocument()
    expect(stateTrigger()).toHaveTextContent('All states')
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    // Clearing a facet is a filter change and NOTHING else.
    expect(api.createProfile).not.toHaveBeenCalled()
    expect(api.patchProfile).not.toHaveBeenCalled()
    expect(api.retireProfile).not.toHaveBeenCalled()
    expect(api.profileLaunchReadiness).not.toHaveBeenCalled()
    expect(api.profileConfiguration).not.toHaveBeenCalled()
  })

  it('clears ONLY the state: the free-text search survives, still filters, and stays recoverable through its own input; focus lands on the trigger that stayed', async () => {
    const user = userEvent.setup()
    serverAnswers([homeA, codexHome, foreign, disabled, retired])
    wrap()
    await screen.findByText('Home A')
    await user.type(searchBox(), 'Home A')
    expect(screen.queryByText('Codex home')).not.toBeInTheDocument()
    await narrowTo(user, 'Disabled')
    expect(
      await screen.findByText('No profiles match this state'),
    ).toBeInTheDocument()
    // Keyboard activation: the action is a real button, reached and pressed without a pointer.
    const clear = screen.getByRole('button', { name: 'Show all states' })
    clear.focus()
    expect(clear).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('Home A')).toBeInTheDocument()
    // The button left with the empty state it lived in; focus did not fall to the
    // body — it moved to the persistent facet trigger.
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    expect(stateTrigger()).toHaveFocus()
    expect(stateTrigger()).toHaveTextContent('All states')
    // The search was NOT cleared, and it still filters the returned page.
    expect(searchBox()).toHaveValue('Home A')
    expect(screen.queryByText('Codex home')).not.toBeInTheDocument()
    expect(screen.queryByText('Paused')).not.toBeInTheDocument()
    // A search that excludes every returned row is the GENERIC no-results — not an
    // empty estate, not a state filter, no state action — and its recovery is the
    // input itself.
    await user.clear(searchBox())
    await user.type(searchBox(), 'zzz-nothing')
    expect(await screen.findByText('No results')).toBeInTheDocument()
    expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    await user.clear(searchBox())
    expect(await screen.findByText('Codex home')).toBeInTheDocument()
    expect(api.createProfile).not.toHaveBeenCalled()
    expect(api.patchProfile).not.toHaveBeenCalled()
  })

  it('an empty ESTATE points a writer at the existing Register control by its own label, and tells a reader which permission is missing', async () => {
    api.listProfiles.mockResolvedValue(page([]))
    const writer = wrap()
    expect(await screen.findByText('No provider profiles')).toBeInTheDocument()
    let empty = emptyStateOf('No provider profiles')
    expect(
      within(empty).getByText(/Use “Register profile” above to register/),
    ).toBeInTheDocument()
    // The ONE control that registers is the header button, gated by the write tier;
    // the empty state holds no second create workflow and no clear action.
    expect(
      screen.getByRole('button', { name: 'Register profile' }),
    ).toBeInTheDocument()
    expect(within(empty).queryByRole('button')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    writer.unmount()

    grant(READ)
    wrap()
    expect(await screen.findByText('No provider profiles')).toBeInTheDocument()
    empty = emptyStateOf('No provider profiles')
    // Accurate for a reader: the specific missing write, not "you cannot use this".
    expect(
      within(empty).getByText(
        /needs sessions:profile:write, which your role does not include/,
      ),
    ).toBeInTheDocument()
    expect(
      within(empty).queryByText(/Use “Register profile” above/),
    ).not.toBeInTheDocument()
    expect(within(empty).queryByRole('button')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Register profile' }),
    ).not.toBeInTheDocument()
    expect(api.createProfile).not.toHaveBeenCalled()
  })

  it('a filtered page released by the OLD tenant after a switch paints nothing and offers no recovery under the new one', async () => {
    let release: (v: unknown) => void = () => {}
    api.listProfiles.mockImplementation(async (params: { state?: string }) => {
      if (params.state === 'retired')
        return new Promise((resolve) => {
          release = resolve
        })
      return page([homeA])
    })
    const user = userEvent.setup()
    const { qc, rerender } = wrap()
    await screen.findByText('Home A')
    await narrowTo(user, 'Retired')
    // The filtered read is in flight: LOADING, not empty — neither node, no action.
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    const signal = (
      api.listProfiles.mock.calls[1][1] as { signal?: AbortSignal }
    ).signal

    auth.activeTenant = 't2'
    api.listProfiles.mockImplementation(async () => page([]))
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    // t2 starts from ALL — the remount dropped t1's filter with everything else — and
    // its honest answer is the estate copy, read under t2's own key.
    expect(await screen.findByText('No provider profiles')).toBeInTheDocument()
    expect(stateTrigger()).toHaveTextContent('All states')
    expect(signal?.aborted).toBe(true)
    // t1's late page, released NOW with a record, paints nothing under t2 and mints no
    // recovery: a response from the old boundary has no authority in the new one.
    await act(async () => {
      release(page([retired]))
    })
    expect(screen.queryByText('Old home')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    expect(screen.getByText('No provider profiles')).toBeInTheDocument()
    // And it is not sitting under a t2 key waiting to be repainted.
    const underT2 = qc
      .getQueryCache()
      .findAll()
      .filter(
        (q) =>
          q.queryKey.includes('t2') &&
          JSON.stringify(q.state.data ?? null).includes('ppf_gone'),
      )
    expect(underT2).toEqual([])
  })

  it('a deferred first read is LOADING — neither empty node, no action — until it answers', async () => {
    let release: (v: unknown) => void = () => {}
    api.listProfiles.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = resolve
        }),
    )
    wrap()
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
    expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all states' }),
    ).not.toBeInTheDocument()
    await act(async () => {
      release(page([homeA]))
    })
    expect(await screen.findByText('Home A')).toBeInTheDocument()
  })

  it.each([
    [
      '403 of role',
      () => new ApiError(403, 'forbidden', 'your role cannot list profiles'),
      'Not authorized',
    ],
    [
      '403 of assurance',
      () => new ApiError(403, 'step_up_required', 'assurance level too low'),
      'step-up ceremony:generic',
    ],
    [
      'lookup failure',
      () => new ApiError(500, 'internal', 'boom', 'req-77'),
      'Something went wrong',
    ],
  ])(
    'a %s under a state filter is its own replacement state — never an empty node, and the clear action never renders inside it',
    async (_what, make, expected) => {
      const user = userEvent.setup()
      api.listProfiles.mockImplementation(
        async (params: { state?: string }) => {
          if (params.state) throw make()
          return page([homeA])
        },
      )
      wrap()
      await screen.findByText('Home A')
      await narrowTo(user, 'Retired')
      expect(await screen.findByText(expected)).toBeInTheDocument()
      expect(screen.queryByText('Home A')).not.toBeInTheDocument()
      expect(
        screen.queryByText('No profiles match this state'),
      ).not.toBeInTheDocument()
      expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: 'Show all states' }),
      ).not.toBeInTheDocument()
      // The facet is still on screen, still showing the value, still the way out.
      expect(stateTrigger()).toHaveTextContent('Retired')
    },
  )

  it('a refused REFETCH replaces the rows it had — retained data is not painted under a refusal, and no empty node stands in', async () => {
    api.listProfiles.mockResolvedValueOnce(page([homeA]))
    const { qc } = wrap()
    expect(await screen.findByText('Home A')).toBeInTheDocument()
    api.listProfiles.mockRejectedValue(
      new ApiError(403, 'forbidden', 'your role cannot list profiles'),
    )
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['agentops', 't1'] })
    })
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText('Home A')).not.toBeInTheDocument()
    expect(screen.queryByText('No provider profiles')).not.toBeInTheDocument()
    expect(
      screen.queryByText('No profiles match this state'),
    ).not.toBeInTheDocument()
  })
})

describe('ProfilesPanel — no dead half, and no stretched row either', () => {
  // Three rows in a 1440×900 frame left 16 empty 200×200 cells (35% fill), so the table
  // was given the remaining height — and a table hands its height to its ROWS: a single
  // profile then measured 704 px against a 36 px budget. The REGION takes the height
  // now and the foot absorbs it. Zero rows keep the existing empty state.
  it('gives the region the height and leaves the table element without one', async () => {
    wrap(true)
    await screen.findByText('Home A')
    const table = screen.getByRole('table')
    expect(table.className).not.toMatch(/min-h-/)
    expect(table.closest('[data-slot="table-region"]')!.className).toMatch(
      /min-h-\[calc\(100svh-8rem\)\]/,
    )
    expect(table.querySelector('tfoot')!.className).toMatch(/\bh-full\b/)
    for (const row of table.querySelectorAll('tbody tr')) {
      expect(row.className).not.toMatch(/h-\[|min-h-/)
    }
    const next = screen.getByRole('button', { name: /register a profile/i })
    expect(next.closest('tfoot')).toBeTruthy()
  })

  it('offers the quiet line only on the page surface, not inside a tab', async () => {
    // Embedded, this panel's verb is already in the header through the action slot;
    // a second offer of it under the table is the screen having two firsts.
    wrap(false)
    await screen.findByText('Home A')
    expect(
      screen.queryByRole('button', { name: /register a profile/i }),
    ).toBeNull()
    expect(screen.getByRole('table').querySelector('tfoot')).toBeNull()
  })

  /**
   * THE QUIET LINE PERFORMS THE ACTION ITS WORDS NAME.
   *
   * ⛔ IT USED TO BE `<a href="#register-profile">` WITH `preventDefault()`. The click
   *    worked and the ADDRESS did not: no element in the document carries that id, so
   *    the status bar, "copy link", a middle-click and a new tab all offered a
   *    destination that resolves to nothing. A control that acts in place is a button —
   *    which also answers Space, and is announced as a command rather than as a link to
   *    somewhere.
   */
  it('opens the register flow from the keyboard, with no fragment that names nothing', async () => {
    const user = userEvent.setup()
    const { container } = wrap(true)
    await screen.findByText('Home A')

    // Every in-page link in this surface must name an element that exists. With the
    // fragment restored this census is what reddens.
    const dangling = [...container.querySelectorAll('a[href^="#"]')]
      .map((a) => a.getAttribute('href')!.slice(1))
      .filter((id) => id !== '' && !document.getElementById(id))
    expect(dangling).toEqual([])

    const next = screen.getByRole('button', { name: /register a profile/i })
    next.focus()
    expect(next).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('keeps the empty state and no next-action line when there are zero rows', async () => {
    api.listProfiles.mockResolvedValue(page([]))
    wrap(true)
    expect(await screen.findByText(/no provider profiles/i)).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /register a profile/i }),
    ).toBeNull()
  })

  /**
   * ⛔ ZERO ROWS IS THE ONE COUNT WHERE A DEAD HALF IS CERTAIN, and it was the one count
   *    the region never reached. One row, three, two hundred — all of them go through
   *    `FillingTable`, whose region takes the viewport and whose foot absorbs the
   *    surplus. An EMPTY page went round it: measured in Chromium at 1440×900, a centred
   *    panel **220 px** tall at the top of the frame with no region around it at all,
   *    against the **772 px** region the same screen gives its rows. The decision "no
   *    dead half" was measured on every count except the one that cannot avoid it.
   *
   *    The region is the SAME piece, not a copy: a second height written here would
   *    drift from `FillingTable`'s the first time one of them was corrected.
   */
  it('zero rows: the empty state fills the same region the table would have', async () => {
    api.listProfiles.mockResolvedValue(page([]))
    wrap(true)
    const empty = await screen.findByText(/no provider profiles/i)
    const region = empty.closest('[data-slot="table-region"]')
    expect(region, 'the empty page is inside the region piece').not.toBeNull()
    expect(region!.className).toMatch(/min-h-\[calc\(100svh-8rem\)\]/)
    expect(
      empty.closest('[data-slot="empty-state"]')!.className,
      'and it takes the height rather than sitting at the top of it',
    ).toMatch(/\bflex-1\b/)
  })

  it('embedded in a tab, zero rows does NOT take the viewport', async () => {
    // The CONTROL: `fill` is the page surface's, and a panel that is one tab of a
    // workspace divides a region it does not own — the same rule the table follows.
    api.listProfiles.mockResolvedValue(page([]))
    wrap(false)
    const empty = await screen.findByText(/no provider profiles/i)
    const region = empty.closest('[data-slot="table-region"]')
    expect(region!.className).not.toMatch(/min-h-\[calc\(100svh-8rem\)\]/)
  })
})

describe('ProfilesPanel — one-line rows', () => {
  // Name + ppf_… stacked in the cell is what measured 53 px (and 181 at 390).
  // The name is the cell; the id lives on title= and in the sheet. Secondary
  // columns hide at 390; Details sits in one overflow menu.
  it('paints the name on one line and keeps the id on title=', async () => {
    wrap()
    const name = await screen.findByText('Home A')
    const cell = name.closest('td')
    expect(cell).toHaveAttribute('title', homeA.profile_ref)
    expect(cell?.querySelector('.flex.flex-col')).toBeNull()
    expect(
      within(name.closest('tr')!).queryByText(homeA.profile_ref),
    ).toBeNull()
  })

  it('paints the environment as a name with the id on title=', async () => {
    wrap()
    const row = (await screen.findByText('Home A')).closest('tr')!
    expect(within(row).queryByText('xenv_1')).toBeNull()
    expect(within(row).getByText('this node')).toBeInTheDocument()
    expect(within(row).getByText('this node').closest('td')).toHaveAttribute(
      'title',
      'xenv_1',
    )
    const far = (await screen.findByText('Far away')).closest('tr')!
    expect(within(far).queryByText('xenv_other')).toBeNull()
    expect(within(far).getByText('another environment')).toBeInTheDocument()
    expect(
      within(far).getByText('another environment').closest('td'),
    ).toHaveAttribute('title', 'xenv_other')
  })

  it('hides its secondary columns at the SHELL\u2019s breakpoint, and keeps one overflow action', async () => {
    wrap()
    const row = (await screen.findByText('Home A')).closest('tr')!
    expect(
      within(row).getByRole('button', { name: /row actions/i }),
    ).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: /^details$/i })).toBeNull()
    // ⛔ THIS USED TO SPELL `max-[390px]:hidden` HERE TOO, which made the test a copy of
    //    the second breakpoint rather than a check on it: the table hid its columns at
    //    390 while the page header collapsed its verbs at 639, and at 500 px the two
    //    disagreed with every suite green. The class is now the shell's own, so a
    //    breakpoint that moves moves once.
    expect(
      screen.getByRole('columnheader', { name: /^driver$/i }).className,
    ).toContain(HIDDEN_ON_PHONE)
  })

  it('shows the id in the sheet, not under the name', async () => {
    await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    expect(
      within(sheet).getByText(homeA.profile_ref, { selector: 'p' }),
    ).toBeInTheDocument()
  })
})

describe('ProfilesPanel — registering', () => {
  it('posts exactly the driver, the two homes and the label — no environment, no credential', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findByText('Home A')
    await user.click(screen.getByRole('button', { name: 'Register profile' }))
    const dialog = await screen.findByRole('dialog')
    // The dialog names WHICH machine validates the paths: the environment a local
    // profile reported.
    expect(
      within(dialog).getByText(/xenv_1, which validates the paths/),
    ).toBeInTheDocument()
    const submit = within(dialog).getByRole('button', { name: 'Register' })
    expect(submit).toBeDisabled()
    await user.type(within(dialog).getByLabelText('Driver'), 'Claude')
    await user.type(
      within(dialog).getByLabelText('Configuration home'),
      '/srv/homes/b/.claude ',
    )
    await user.type(within(dialog).getByLabelText('User home'), '/srv/homes/b')
    await user.type(within(dialog).getByLabelText('Display name'), 'Home B')
    await user.click(submit)
    await waitFor(() => expect(api.createProfile).toHaveBeenCalledOnce())
    expect(api.createProfile).toHaveBeenCalledWith({
      driver: 'Claude',
      config_home: '/srv/homes/b/.claude',
      user_home: '/srv/homes/b',
      display_name: 'Home B',
    })
    const body = api.createProfile.mock.calls[0][0] as Record<string, unknown>
    expect(body).not.toHaveProperty('environment_ref')
    expect(body).not.toHaveProperty('state')
    expect(Object.keys(body).sort()).toEqual([
      'config_home',
      'display_name',
      'driver',
      'user_home',
    ])
  })

  it('offers no register control to a read-only role, so no POST can be issued', async () => {
    grant(READ)
    wrap()
    await screen.findByText('Home A')
    expect(
      screen.queryByRole('button', { name: 'Register profile' }),
    ).not.toBeInTheDocument()
    expect(api.createProfile).not.toHaveBeenCalled()
  })
})

describe('ProfilesPanel — a registration draft does not outlive its tier', () => {
  it('losing write ends the draft; regaining it does not reopen the dialog until a new gesture', async () => {
    const user = userEvent.setup()
    const { qc, rerender } = wrap()
    await screen.findByText('Home A')
    await user.click(screen.getByRole('button', { name: 'Register profile' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Driver'), 'claude')
    grant(READ)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    // The tier comes back through a whoami refresh: that is not a gesture.
    grant(READ, WRITE)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('button', { name: 'Register profile' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.createProfile).not.toHaveBeenCalled()
    // A new gesture opens a FRESH dialog: the earlier draft is gone with its tier.
    await user.click(screen.getByRole('button', { name: 'Register profile' }))
    const fresh = await screen.findByRole('dialog')
    expect(within(fresh).getByLabelText('Driver')).toHaveValue('')
  })
})

describe('ProfilesPanel — the sheet', () => {
  it('reads launch requirements for the opened profile only, after the sheet opens', async () => {
    await openSheet(/^Home A$/)
    await waitFor(() =>
      expect(api.profileLaunchReadiness).toHaveBeenCalledWith(
        'ppf_a',
        { transport: 'stream-json', isolation: 'native' },
        expect.anything(),
      ),
    )
    expect(
      api.profileLaunchReadiness.mock.calls.every((c) => c[0] === 'ppf_a'),
    ).toBe(true)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Provider sign-in is not observed here/),
    ).toBeInTheDocument()
  })

  it('renames through PATCH with only display_name', async () => {
    const { user } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Rename' }))
    const input = await screen.findByLabelText('Display name')
    await user.clear(input)
    await user.type(input, 'Home A (ops)')
    await user.click(
      screen.getByRole('button', {
        name: 'Rename',
      }),
    )
    await waitFor(() =>
      expect(api.patchProfile).toHaveBeenCalledWith('ppf_a', {
        display_name: 'Home A (ops)',
      }),
    )
  })

  it('disables and enables through PATCH state only', async () => {
    const { user } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Disable' }))
    await waitFor(() =>
      expect(api.patchProfile).toHaveBeenCalledWith('ppf_a', {
        state: 'disabled',
      }),
    )
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    const row = rowOf('Paused')
    await user.click(within(row).getByRole('button', { name: /row actions/i }))
    await user.click(
      await screen.findByRole('menuitem', { name: /^details$/i }),
    )
    const sheet2 = await screen.findByRole('dialog')
    await user.click(within(sheet2).getByRole('button', { name: 'Enable' }))
    await waitFor(() =>
      expect(api.patchProfile).toHaveBeenCalledWith('ppf_off', {
        state: 'active',
      }),
    )
  })

  it('retires only after the operator types the profile back, then POSTs /retire', async () => {
    const { user } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Retire' }))
    const confirm = await screen.findByRole('button', {
      name: 'Retire',
    })
    // The typed phrase is the guard: nothing is sent before it matches.
    expect(confirm).toBeDisabled()
    await user.click(confirm)
    expect(api.retireProfile).not.toHaveBeenCalled()
    await user.type(screen.getByLabelText(/confirm/i), 'Home A')
    await user.click(screen.getByRole('button', { name: 'Retire' }))
    await waitFor(() => expect(api.retireProfile).toHaveBeenCalledWith('ppf_a'))
    expect(api.patchProfile).not.toHaveBeenCalled()
  })

  it('offers a retired profile nothing to do, and its retirement date', async () => {
    await openSheet(/^Old home$/)
    const sheet = await screen.findByRole('dialog')
    expect(
      within(sheet).queryByRole('button', { name: 'Rename' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Enable' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Retire' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).getByText('Retired', { selector: 'dt' }),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByRole('button', { name: 'Bind source' }),
    ).toBeDisabled()
  })

  it('reads the configuration ON DEMAND, for an admin, and drops it when hidden', async () => {
    const { user } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    // Opening the sheet asked for the profile, never for its homes.
    await waitFor(() =>
      expect(api.getProfile).toHaveBeenCalledWith('ppf_a', expect.anything()),
    )
    expect(api.profileConfiguration).not.toHaveBeenCalled()
    expect(screen.queryByText('/srv/homes/a/.claude')).not.toBeInTheDocument()
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    expect(screen.getByText('/srv/homes/a')).toBeInTheDocument()
    expect(
      screen.getByText(/stored by execution environment xenv_1/),
    ).toBeInTheDocument()
    expect(api.profileConfiguration.mock.calls[0][0]).toBe('ppf_a')
    await user.click(
      within(sheet).getByRole('button', { name: 'Hide configuration' }),
    )
    await waitFor(() =>
      expect(
        screen.queryByText('/srv/homes/a/.claude'),
      ).not.toBeInTheDocument(),
    )
  })

  it('gives a writer without admin no reveal and no retire; a reader no controls at all', async () => {
    grant(READ, WRITE)
    const first = await openSheet(/^Home A$/)
    let sheet = await screen.findByRole('dialog')
    expect(
      within(sheet).getByRole('button', { name: 'Rename' }),
    ).toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Retire' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Reveal configuration' }),
    ).not.toBeInTheDocument()
    first.unmount()
    grant(READ)
    await openSheet(/^Codex home$/)
    sheet = await screen.findByRole('dialog')
    expect(
      within(sheet).queryByRole('button', { name: 'Rename' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Disable' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Retire' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Reveal configuration' }),
    ).not.toBeInTheDocument()
    expect(
      within(sheet).queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
    expect(api.profileConfiguration).not.toHaveBeenCalled()
    expect(api.patchProfile).not.toHaveBeenCalled()
    expect(api.retireProfile).not.toHaveBeenCalled()
  })

  it('a tenant switch closes the sheet and clears a revealed configuration', async () => {
    const { user, qc, rerender } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    expect(api.listProfiles).toHaveBeenCalledOnce()
    auth.activeTenant = 't2'
    api.listProfiles.mockResolvedValue(page([]))
    // The panel remounts on the tenant key: nothing of t1 stays on screen, and the
    // list is asked again — for t2, under t2's own key.
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(
        screen.queryByText('/srv/homes/a/.claude'),
      ).not.toBeInTheDocument(),
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('No provider profiles')).toBeInTheDocument()
    // And the t1 configuration is not sitting in the cache to be repainted.
    expect(
      qc.getQueryData(['agentops', 't1', 'profile', 'ppf_a', 'configuration']),
    ).toBeUndefined()
  })
})

/** No entry of the QueryCache carries a stored home. The reveal is a cycle, not a query. */
function cachedHomes(qc: QueryClient): unknown[] {
  return qc
    .getQueryCache()
    .findAll()
    .map((q) => q.state.data)
    .filter(
      (d) =>
        !!d &&
        typeof d === 'object' &&
        ('config_home' in (d as object) || 'user_home' in (d as object)),
    )
}

const CONFIG_KEY = ['agentops', 't1', 'profile', 'ppf_a', 'configuration']

describe('ProfilesPanel — an explicit reveal ends when the operator, or their authority, says so', () => {
  it('Hide purges the homes from the screen AND from the cache; a new Reveal reads again', async () => {
    const { user, qc } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    expect(api.profileConfiguration).toHaveBeenCalledOnce()
    await user.click(
      within(sheet).getByRole('button', { name: 'Hide configuration' }),
    )
    await waitFor(() =>
      expect(
        screen.queryByText('/srv/homes/a/.claude'),
      ).not.toBeInTheDocument(),
    )
    expect(cachedHomes(qc)).toEqual([])
    expect(qc.getQueryData(CONFIG_KEY)).toBeUndefined()
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    expect(api.profileConfiguration).toHaveBeenCalledTimes(2)
  })

  it('a response released AFTER Hide is neither painted nor cached, and its request was aborted', async () => {
    let release: (v: unknown) => void = () => {}
    api.profileConfiguration.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = resolve
        }),
    )
    const { user, qc } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(
      await within(sheet).findByText('Reading configuration…'),
    ).toBeInTheDocument()
    await user.click(
      within(sheet).getByRole('button', { name: 'Hide configuration' }),
    )
    const signal = (
      api.profileConfiguration.mock.calls[0][1] as { signal?: AbortSignal }
    ).signal
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      release({
        profile_ref: 'ppf_a',
        driver: 'claude',
        environment_ref: 'xenv_1',
        config_home: '/srv/homes/a/.claude',
        user_home: '/srv/homes/a',
        state: 'active',
      })
    })
    expect(screen.queryByText('/srv/homes/a/.claude')).not.toBeInTheDocument()
    expect(cachedHomes(qc)).toEqual([])
    expect(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    ).toBeInTheDocument()
  })

  it('losing admin ends the reveal; regaining it does NOT read again until a new explicit Reveal', async () => {
    const { user, qc, rerender } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    grant(READ, WRITE)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(
        screen.queryByText('/srv/homes/a/.claude'),
      ).not.toBeInTheDocument(),
    )
    expect(cachedHomes(qc)).toEqual([])
    grant(READ, WRITE, ADMIN)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    // Restored authority is not a gesture: nothing is read, nothing is shown.
    expect(
      await within(sheet).findByRole('button', {
        name: 'Reveal configuration',
      }),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/homes/a/.claude')).not.toBeInTheDocument()
    expect(api.profileConfiguration).toHaveBeenCalledOnce()
    // The positive direction: an explicit Reveal under the restored tier reads.
    await user.click(
      within(sheet).getByRole('button', { name: 'Reveal configuration' }),
    )
    expect(await screen.findByText('/srv/homes/a/.claude')).toBeInTheDocument()
    expect(api.profileConfiguration).toHaveBeenCalledTimes(2)
  })

  it.each([
    ['principal', () => (auth.principal = 'u2')],
    [
      'new-session credential',
      () =>
        useSessionStore.getState().setSession({
          token: 'olvs_next',
          sessionId: `sid-${Date.now()}`,
          expiresAt: '2030-01-01T00:00:00Z',
        }),
    ],
    [
      'same-session credential rotation',
      () => {
        // What POST /v1/auth/refresh really does: a new bearer, the SAME session id.
        useSessionStore.getState().setSession({
          token: 'olvs_rotated',
          sessionId: 'sid-1',
          expiresAt: '2030-01-01T00:00:00Z',
        })
        expect(useSessionStore.getState().sessionId).toBe('sid-1')
      },
    ],
  ])(
    'a %s change closes the sheet and clears a revealed configuration',
    async (_what, move) => {
      const { user, qc, rerender } = await openSheet(/^Home A$/)
      const sheet = await screen.findByRole('dialog')
      await user.click(
        within(sheet).getByRole('button', { name: 'Reveal configuration' }),
      )
      expect(
        await screen.findByText('/srv/homes/a/.claude'),
      ).toBeInTheDocument()
      act(() => {
        move()
      })
      rerender(
        <QueryClientProvider client={qc}>
          <ProfilesPanel />
        </QueryClientProvider>,
      )
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
      expect(screen.queryByText('/srv/homes/a/.claude')).not.toBeInTheDocument()
      expect(cachedHomes(qc)).toEqual([])
      expect(api.profileConfiguration).toHaveBeenCalledOnce()
    },
  )
})

describe('ProfilesPanel — a confirmation does not outlive its tier', () => {
  it('an open retirement confirmation closes when admin is revoked, and nothing is sent', async () => {
    const { user, qc, rerender } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Retire' }))
    await user.type(screen.getByLabelText(/confirm/i), 'Home A')
    const confirm = screen.getByRole('button', { name: 'Retire' })
    expect(confirm).toBeEnabled()
    grant(READ, WRITE)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(screen.queryByLabelText(/confirm/i)).not.toBeInTheDocument(),
    )
    expect(toast.warning).toHaveBeenCalled()
    // A click queued on the button that was there produces no request.
    await user.click(confirm).catch(() => {})
    expect(api.retireProfile).not.toHaveBeenCalled()
    expect(
      within(sheet).queryByRole('button', { name: 'Retire' }),
    ).not.toBeInTheDocument()
  })

  it('a rename draft closes when the write tier is revoked, and nothing is sent', async () => {
    const { user, qc, rerender } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Rename' }))
    const input = await screen.findByLabelText('Display name')
    await user.type(input, ' edited')
    grant(READ)
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(screen.queryByLabelText('Display name')).not.toBeInTheDocument(),
    )
    expect(api.patchProfile).not.toHaveBeenCalled()
  })

  it('a confirmation that still holds its tier still retires (the positive direction)', async () => {
    const { user, qc, rerender } = await openSheet(/^Home A$/)
    const sheet = await screen.findByRole('dialog')
    await user.click(within(sheet).getByRole('button', { name: 'Retire' }))
    await user.type(screen.getByLabelText(/confirm/i), 'Home A')
    rerender(
      <QueryClientProvider client={qc}>
        <ProfilesPanel />
      </QueryClientProvider>,
    )
    await user.click(screen.getByRole('button', { name: 'Retire' }))
    await waitFor(() => expect(api.retireProfile).toHaveBeenCalledWith('ppf_a'))
    expect(toast.warning).not.toHaveBeenCalled()
  })
})
