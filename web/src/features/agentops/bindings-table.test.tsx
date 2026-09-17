// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// B1 — source→profile bindings. The identity a binding posts is the roster row's
// PERSISTENT id and the revision THIS node applied, both read from the roster and
// never from a name; a row this node has not applied cannot be chosen; and the
// three binding tiers act independently of the profile tiers.
//
// WHO MAY BIND IS THE ENGINE'S ANSWER, NOW: opening Bind with the current write tier
// issues the protected roster GET; a 403 offers nothing, an error offers nothing, a
// success offers rows only while it is current. Reopening reads again. A change of
// permission, tenant, principal or credential ends the cycle and the selection with
// it, and a revoke confirmation left open does not outlive the tier it needed.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
  tenant: 't1' as string | null,
  principal: 'u1',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
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

import { BindingsTable } from './bindings-table'
import type { ProviderBindingDTO, ProviderProfileDTO } from './types'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}
const foreign: ProviderProfileDTO = {
  ...homeA,
  profile_ref: 'ppf_far',
  display_name: 'Far away',
  environment_ref: 'xenv_other',
  local_environment: false,
  operable: false,
}
const paused: ProviderProfileDTO = {
  ...homeA,
  profile_ref: 'ppf_off',
  display_name: 'Paused',
  state: 'disabled',
  operable: false,
}
const active: ProviderBindingDTO = {
  binding_ref: 'psb_1',
  source_id: '0192f2c0-aaaa-7000-8000-000000000001',
  source_revision: 3,
  source_name: 'claude-home-a',
  environment_ref: 'xenv_1',
  selector_key: 'dedicated',
  profile_ref: 'ppf_a',
  driver: 'claude',
  state: 'active',
  bound_at: '2026-09-01T10:00:00Z',
}
const revoked: ProviderBindingDTO = {
  ...active,
  binding_ref: 'psb_0',
  source_revision: 2,
  state: 'revoked',
  revoked_at: '2026-09-01T11:00:00Z',
}
const ROSTER = {
  sources: [
    {
      name: 'claude-home-a',
      kind: 'claude',
      tenant: 't1',
      enabled: true,
      status: 'running',
      id: '0192f2c0-aaaa-7000-8000-000000000001',
      applied_revision: 3,
    },
    {
      name: 'not-yet-applied',
      kind: 'claude',
      tenant: 't1',
      enabled: true,
      status: 'not_wired',
      id: '0192f2c0-aaaa-7000-8000-000000000002',
    },
    {
      name: 'older-engine-row',
      kind: 'vault',
      tenant: 't1',
      enabled: true,
      status: 'running',
    },
  ],
}

function grant(...p: string[]) {
  auth.perms = new Set(p)
}
const BR = 'sessions:profile-binding:read'
const BW = 'sessions:profile-binding:write'
const BA = 'sessions:profile-binding:admin'
const PR = 'sessions:profile:read'

function wrap(profile?: ProviderProfileDTO) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ui = render(
    <QueryClientProvider client={qc}>
      <BindingsTable profile={profile} />
    </QueryClientProvider>,
  )
  const rerender = () =>
    ui.rerender(
      <QueryClientProvider client={qc}>
        <BindingsTable profile={profile} />
      </QueryClientProvider>,
    )
  return { ...ui, qc, rerender }
}

/** The AbortSignal the roster read was given, per call. */
const rosterSignals = () =>
  consoleApi.listSources.mock.calls.map(
    (c) => (c[0] as { signal?: AbortSignal } | undefined)?.signal,
  )

beforeEach(() => {
  vi.clearAllMocks()
  auth.tenant = 't1'
  auth.principal = 'u1'
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: 'sid-1',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  grant(BR, BW, BA, PR)
  api.listBindings.mockResolvedValue({
    items: [active, revoked],
    has_more: false,
  })
  api.listProfiles.mockResolvedValue({
    items: [homeA, foreign, paused],
    has_more: false,
  })
  consoleApi.listSources.mockResolvedValue(ROSTER)
  api.createBinding.mockResolvedValue(active)
  api.revokeBinding.mockResolvedValue(revoked)
})

describe('BindingsTable — listing and revoking', () => {
  it('lists one page with the ceiling and reads identity from id + revision', async () => {
    wrap()
    // Two rows name one source (an active binding and a revoked one of an older
    // revision): the name is a label; the rows are told apart by their reference.
    expect(await screen.findAllByText('claude-home-a')).toHaveLength(2)
    expect(api.listBindings.mock.calls[0][0]).toEqual({
      limit: 100,
      cursor: undefined,
    })
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText(active.source_id)).toBeInTheDocument()
    expect(within(rows[0]).getByText('3')).toBeInTheDocument()
    expect(
      within(rows[0]).getByRole('button', { name: 'Revoke' }),
    ).toBeInTheDocument()
    expect(
      within(rows[1]).queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
  })

  it('narrows to ONE profile when opened from its sheet', async () => {
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    expect(api.listBindings.mock.calls[0][0]).toEqual({
      profile_ref: 'ppf_a',
      limit: 100,
      cursor: undefined,
    })
  })

  it('revokes only after confirmation, by binding reference', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    expect(api.revokeBinding).not.toHaveBeenCalled()
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeBinding).toHaveBeenCalledWith('psb_1'))
  })

  it('a reader sees no bind and no revoke; without read it sees a notice and asks nothing', async () => {
    grant(BR, PR)
    const { unmount } = wrap()
    await screen.findAllByText(active.source_id)
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
    unmount()
    grant(BW, BA, PR)
    wrap()
    expect(
      await screen.findByText(/does not include sessions:profile-binding:read/),
    ).toBeInTheDocument()
    expect(api.listBindings).toHaveBeenCalledOnce()
  })

  it('an admin of profiles is not an admin of bindings', async () => {
    grant(BR, 'sessions:profile:admin', 'sessions:profile:write')
    wrap()
    await screen.findAllByText(active.source_id)
    expect(
      screen.queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
  })
})

describe('BindingsTable — a revoke confirmation does not outlive its tier', () => {
  it('closes when the admin tier is revoked, and nothing is sent', async () => {
    const user = userEvent.setup()
    const { rerender } = wrap()
    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    // The client learns the tier is gone (a whoami refresh) and re-renders.
    grant(BR, BW, PR)
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(api.revokeBinding).not.toHaveBeenCalled()
  })

  it('closes when the row it names is no longer an active binding', async () => {
    const user = userEvent.setup()
    const { qc } = wrap()
    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    // Somebody else revoked it: the next read no longer holds an active psb_1.
    api.listBindings.mockResolvedValue({
      items: [{ ...active, state: 'revoked' }, revoked],
      has_more: false,
    })
    // The list key is partitioned by the authority boundary (an opaque epoch), so the
    // test invalidates by the key the cache actually holds, up to its 'bindings' segment.
    const held = qc
      .getQueryCache()
      .findAll()
      .map((q) => q.queryKey)
      .find((k) => k.includes('bindings'))
    expect(held).toBeDefined()
    await act(async () => {
      await qc.invalidateQueries({
        queryKey: held!.slice(0, held!.indexOf('bindings') + 1),
      })
    })
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(api.revokeBinding).not.toHaveBeenCalled()
  })

  it('a confirm that still holds its tier still revokes (the positive direction)', async () => {
    const user = userEvent.setup()
    const { rerender } = wrap()
    await user.click(await screen.findByRole('button', { name: 'Revoke' }))
    const dialog = await screen.findByRole('dialog')
    rerender()
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeBinding).toHaveBeenCalledWith('psb_1'))
    expect(toast.warning).not.toHaveBeenCalled()
  })
})

describe('BindingsTable — binding a source under the engine’s current authority', () => {
  it('opening Bind with the write tier reads the protected roster, and posts id + APPLIED revision', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(active.source_id)
    expect(consoleApi.listSources).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(consoleApi.listSources).toHaveBeenCalledOnce())
    const submit = within(dialog).getByRole('button', { name: 'Bind' })
    expect(submit).toBeDisabled()
    await user.click(within(dialog).getByLabelText('Profile'))
    expect(
      await screen.findByRole('option', { name: /Far away/ }),
    ).toHaveAttribute('data-disabled')
    expect(screen.getByRole('option', { name: /Paused/ })).toHaveAttribute(
      'data-disabled',
    )
    await user.click(screen.getByRole('option', { name: /Home A/ }))
    await user.click(await within(dialog).findByLabelText('Source'))
    expect(
      await screen.findByRole('option', { name: /not-yet-applied/ }),
    ).toHaveAttribute('data-disabled')
    expect(
      screen.getByRole('option', { name: /older-engine-row/ }),
    ).toHaveAttribute('data-disabled')
    await user.click(
      screen.getByRole('option', {
        name: /claude-home-a · claude · revision 3/,
      }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Bind' }))
    await waitFor(() => expect(api.createBinding).toHaveBeenCalledOnce())
    expect(api.createBinding).toHaveBeenCalledWith({
      source_id: '0192f2c0-aaaa-7000-8000-000000000001',
      source_revision: 3,
      profile_ref: 'ppf_a',
    })
    const body = api.createBinding.mock.calls[0][0] as Record<string, unknown>
    expect(Object.keys(body).sort()).toEqual([
      'profile_ref',
      'source_id',
      'source_revision',
    ])
  })

  it('a 403 on the roster is the engine’s answer: no rows, no selection, no submit', async () => {
    consoleApi.listSources.mockRejectedValue(
      new ApiError(403, 'forbidden', 'system:admin required'),
    )
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(/refused the source roster/),
    ).toBeInTheDocument()
    expect(within(dialog).queryByLabelText('Source')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Bind' })).toBeDisabled()
    expect(api.createBinding).not.toHaveBeenCalled()
  })

  it('an error on the roster is an error, not a stale success', async () => {
    consoleApi.listSources.mockRejectedValue(new Error('upstream down'))
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(/could not be read: upstream down/),
    ).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Bind' })).toBeDisabled()
  })

  it('reopening Bind reads the roster AGAIN; closing aborts the read in flight', async () => {
    let release: (v: typeof ROSTER) => void = () => {}
    consoleApi.listSources.mockImplementationOnce(
      () =>
        new Promise<typeof ROSTER>((resolve) => {
          release = resolve
        }),
    )
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText('Reading the source roster…'),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(rosterSignals()[0]?.aborted).toBe(true)
    // A response that lands after the dialog closed is not an answer.
    release(ROSTER)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    await waitFor(() => expect(consoleApi.listSources).toHaveBeenCalledTimes(2))
    expect(rosterSignals()[1]?.aborted).toBe(false)
    expect(await screen.findByLabelText('Source')).toBeInTheDocument()
  })

  it('losing the write tier mid-dialog aborts the read, closes the dialog and posts nothing', async () => {
    let release: (v: typeof ROSTER) => void = () => {}
    consoleApi.listSources.mockImplementationOnce(
      () =>
        new Promise<typeof ROSTER>((resolve) => {
          release = resolve
        }),
    )
    const user = userEvent.setup()
    const { rerender } = wrap(homeA)
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    await screen.findByRole('dialog')
    grant(BR, BA, PR)
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(rosterSignals()[0]?.aborted).toBe(true)
    release(ROSTER)
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
    expect(api.createBinding).not.toHaveBeenCalled()
  })

  it.each([
    ['tenant', () => (auth.tenant = 't2')],
    ['principal', () => (auth.principal = 'u2')],
    [
      'new-session credential',
      () =>
        useSessionStore.getState().setSession({
          token: 'olvs_next',
          sessionId: 'sid-2',
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
    'a %s change ends the roster cycle and the selection; reopening reads under the new authority',
    async (_what, move) => {
      const user = userEvent.setup()
      const { rerender } = wrap(homeA)
      await screen.findAllByText(active.source_id)
      await user.click(screen.getByRole('button', { name: 'Bind source' }))
      const dialog = await screen.findByRole('dialog')
      await user.click(await within(dialog).findByLabelText('Source'))
      await user.click(
        await screen.findByRole('option', { name: /claude-home-a · claude/ }),
      )
      expect(within(dialog).getByRole('button', { name: 'Bind' })).toBeEnabled()
      act(() => {
        move()
      })
      rerender()
      // The dialog is keyed by the boundary, so what is on screen now is a NEW
      // dialog: the answer read for the previous authority is gone with its
      // selection, and nothing was read again on its own.
      const after = await screen.findByRole('dialog')
      await waitFor(() =>
        expect(
          within(after).queryByLabelText('Source'),
        ).not.toBeInTheDocument(),
      )
      expect(within(after).getByRole('button', { name: 'Bind' })).toBeDisabled()
      expect(
        within(after).getByText(/roster read ended because your permission/),
      ).toBeInTheDocument()
      expect(consoleApi.listSources).toHaveBeenCalledOnce()
      expect(api.createBinding).not.toHaveBeenCalled()
    },
  )

  it('losing profile-read forgets the picked target, drops the options and refuses the POST', async () => {
    const user = userEvent.setup()
    const { rerender } = wrap()
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByLabelText('Profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    await user.click(await within(dialog).findByLabelText('Source'))
    await user.click(
      await screen.findByRole('option', { name: /claude-home-a · claude/ }),
    )
    const submit = within(dialog).getByRole('button', { name: 'Bind' })
    expect(submit).toBeEnabled()
    // Only the profile READ tier leaves; binding read/write stay. The target was
    // learned under a read that is gone: it is forgotten, the options with it, and
    // the roster answer alone cannot make the submit actionable.
    act(() => grant(BR, BW, BA))
    rerender()
    await waitFor(() =>
      expect(
        within(dialog).getByRole('button', { name: 'Bind' }),
      ).toBeDisabled(),
    )
    expect(
      within(dialog).getByText(/Profiles could not be read/),
    ).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Profile')).toHaveTextContent(
      'Select a profile',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Bind' }))
    expect(api.createBinding).not.toHaveBeenCalled()
    // The profile-scoped flow, mounted under a sheet that holds its own read, still
    // binds: the fixed target came from an authorized surface, not from this picker.
  })

  it('a profile fixed by an authorized sheet still binds when only the tenant-wide read is absent', async () => {
    grant(BR, BW, BA)
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(await within(dialog).findByLabelText('Source'))
    await user.click(
      await screen.findByRole('option', { name: /claude-home-a · claude/ }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Bind' }))
    await waitFor(() =>
      expect(api.createBinding).toHaveBeenCalledWith({
        source_id: '0192f2c0-aaaa-7000-8000-000000000001',
        source_revision: 3,
        profile_ref: 'ppf_a',
      }),
    )
  })

  it('walks the profile picker past its first page and binds a profile from page two', async () => {
    const pageOne = Array.from({ length: 200 }, (_, i) => ({
      ...homeA,
      profile_ref: `ppf_p1_${i}`,
      display_name: `Page one ${i}`,
    }))
    const pageTwo: ProviderProfileDTO = {
      ...homeA,
      profile_ref: 'ppf_page_two',
      display_name: 'Second page home',
    }
    api.listProfiles.mockImplementation(async (params: { cursor?: string }) =>
      params.cursor === 'c2'
        ? { items: [pageTwo], has_more: false }
        : { items: pageOne, has_more: true, cursor: 'c2' },
    )
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(active.source_id)
    await user.click(screen.getByRole('button', { name: 'Bind source' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(
        /Loaded 200 active profiles; there are more/,
      ),
    ).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: 'Load more profiles' }),
    )
    await waitFor(() =>
      expect(
        api.listProfiles.mock.calls.map((c) => c[0] as { cursor?: string }),
      ).toContainEqual({ state: 'active', limit: 200, cursor: 'c2' }),
    )
    await waitFor(() =>
      expect(
        within(dialog).queryByText(/there are more/),
      ).not.toBeInTheDocument(),
    )
    await user.click(within(dialog).getByLabelText('Profile'))
    await user.click(
      await screen.findByRole('option', { name: /Second page home/ }),
    )
    await user.click(await within(dialog).findByLabelText('Source'))
    await user.click(
      await screen.findByRole('option', { name: /claude-home-a · claude/ }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Bind' }))
    await waitFor(() =>
      expect(api.createBinding).toHaveBeenCalledWith({
        source_id: '0192f2c0-aaaa-7000-8000-000000000001',
        source_revision: 3,
        profile_ref: 'ppf_page_two',
      }),
    )
  })

  it('Escape closes the bind dialog and returns focus to the control that opened it', async () => {
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(active.source_id)
    const bind = screen.getByRole('button', { name: 'Bind source' })
    bind.focus()
    await user.keyboard('{Enter}')
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(consoleApi.listSources).toHaveBeenCalledOnce())
    expect(dialog.contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    await waitFor(() => expect(bind).toHaveFocus())
  })

  it('will not open the bind dialog for a profile that cannot take a binding', async () => {
    wrap(paused)
    await screen.findAllByText(active.source_id)
    expect(screen.getByRole('button', { name: 'Bind source' })).toBeDisabled()
  })
})
