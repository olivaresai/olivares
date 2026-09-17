// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Binding DETAILS — the current, authorized point read of one source→profile binding.
//
// Pressing Details on a row issues `GET /provider-source-bindings/{ref}` for THAT
// reference, now, under `sessions:profile-binding:read` alone (a tier independent of
// every profile tier). The dialog paints only what that answer says: while it is in
// flight there is nothing but the reference; a 403 is the engine's refusal; a 404 is
// the engine holding no such binding now; an error is an error. The list row the
// dialog opened from is never painted and never stands in for an answer the engine
// did not give. Refresh and Retry read again, explicitly, and drop what was shown
// until the new answer lands. Closing, losing the tier, or a move of the boundary
// (principal, tenant, credential) aborts the request and discards a late answer.
// Nothing here writes. Real QueryClient, mocked client, real zustand session store.
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
  getProfile: vi.fn(),
  listBindings: vi.fn(),
  getBinding: vi.fn(),
  createBinding: vi.fn(),
  revokeBinding: vi.fn(),
  profileConfiguration: vi.fn(),
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
/** The row as the LIST knows it. Its name and revision are deliberately different
 * from what the engine answers on the point read, so a test can tell which of the
 * two the dialog painted. */
const listed: ProviderBindingDTO = {
  binding_ref: 'psb_1',
  source_id: '0192f2c0-aaaa-7000-8000-000000000001',
  source_revision: 3,
  source_name: 'list-says-this-name',
  environment_ref: 'xenv_1',
  selector_key: 'dedicated',
  profile_ref: 'ppf_a',
  driver: 'claude',
  state: 'active',
  bound_at: '2026-09-01T10:00:00Z',
}
const listedRevoked: ProviderBindingDTO = {
  ...listed,
  binding_ref: 'psb_0',
  source_revision: 2,
  state: 'revoked',
  revoked_at: '2026-09-01T11:00:00Z',
}
/** The engine's CURRENT answer for psb_1: renamed source, a later revision. */
const engineAnswer: ProviderBindingDTO = {
  ...listed,
  source_name: 'engine-says-this-name',
  source_revision: 4,
}
/** The engine's answer after somebody revoked psb_1 elsewhere. */
const engineRevoked: ProviderBindingDTO = {
  ...engineAnswer,
  state: 'revoked',
  revoked_at: '2026-09-02T08:30:00Z',
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

/** A response the test releases when it decides. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
/** The AbortSignal each point read was given, per call. */
const readSignals = () =>
  api.getBinding.mock.calls.map(
    (c) => (c[1] as { signal?: AbortSignal } | undefined)?.signal,
  )
const detailsOf = (ref: string) =>
  screen.getByRole('button', { name: `Details of binding ${ref}` })
const dialog = () => screen.findByRole('dialog', { name: 'Binding details' })
/** The footer's Close (the primitive's top-right X is also named Close). */
const closeButton = (d: HTMLElement) => {
  const footer = within(d)
    .getAllByRole('button', { name: 'Close' })
    .find((b) => !b.querySelector('.sr-only'))
  expect(footer).toBeDefined()
  return footer!
}
/** The state badge of the answer (the row label "Revoked" is a <dt>). */
const badge = (d: HTMLElement, text: string) =>
  within(d).findByText(text, { selector: 'span' })
const noWrite = () => {
  expect(api.createBinding).not.toHaveBeenCalled()
  expect(api.revokeBinding).not.toHaveBeenCalled()
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.tenant = 't1'
  auth.principal = 'u1'
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: 'sid-1',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  grant(BR)
  api.listBindings.mockResolvedValue({
    items: [listed, listedRevoked],
    has_more: false,
  })
  api.getBinding.mockResolvedValue(engineAnswer)
  api.listProfiles.mockResolvedValue({ items: [homeA], has_more: false })
  consoleApi.listSources.mockResolvedValue({ sources: [] })
})

describe('Binding details — the engine’s answer, under the read tier alone', () => {
  it('every row offers Details; a reader with ONLY binding read opens it and sees what the engine answered, not the list row', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    // Active and revoked rows alike; no write control for a reader.
    expect(detailsOf('psb_1')).toBeInTheDocument()
    expect(detailsOf('psb_0')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()

    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    // The point read, for this reference, with a signal — and nothing else was
    // asked: no profile, no roster, no second list.
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledOnce())
    expect(api.getBinding.mock.calls[0][0]).toBe('psb_1')
    expect(readSignals()[0]).toBeInstanceOf(AbortSignal)
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(api.getProfile).not.toHaveBeenCalled()
    expect(consoleApi.listSources).not.toHaveBeenCalled()
    expect(api.profileConfiguration).not.toHaveBeenCalled()
    expect(api.listBindings).toHaveBeenCalledOnce()

    // What the ENGINE said: its name, its revision — the list's are absent.
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    expect(within(d).queryByText('list-says-this-name')).not.toBeInTheDocument()
    expect(within(d).getByText('4')).toBeInTheDocument()
    expect(within(d).queryByText('3')).not.toBeInTheDocument()
    expect(within(d).getAllByText('psb_1').length).toBeGreaterThan(0)
    expect(within(d).getByText(listed.source_id)).toBeInTheDocument()
    expect(within(d).getByText('ppf_a')).toBeInTheDocument()
    expect(within(d).getByText('claude')).toBeInTheDocument()
    expect(within(d).getByText('xenv_1')).toBeInTheDocument()
    expect(within(d).getByText('dedicated')).toBeInTheDocument()
    expect(within(d).getByText('Active')).toBeInTheDocument()
    expect(within(d).getByText(/Read from the engine at/)).toBeInTheDocument()
    // Read-only surface: no revoke, no bind, nothing sent.
    expect(
      within(d).queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
    noWrite()
  })

  it('shows the engine’s revocation (state and timestamp) even while the list still says active', async () => {
    api.getBinding.mockResolvedValue(engineRevoked)
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(await badge(d, 'Revoked')).toBeInTheDocument()
    expect(within(d).queryByText('Active')).not.toBeInTheDocument()
    // The revocation timestamp row exists only because the engine reported one.
    expect(
      within(d).getByText('Revoked', { selector: 'dt' }),
    ).toBeInTheDocument()
    // The narrowed listing asked for its profile, and the point read is the same call.
    expect(api.listBindings.mock.calls[0][0]).toMatchObject({
      profile_ref: 'ppf_a',
    })
    expect(api.getBinding).toHaveBeenCalledWith('psb_1', expect.anything())
    noWrite()
  })

  it('while the answer is pending nothing of the list row is painted; closing aborts; a late answer lands nowhere; reopening reads again', async () => {
    const first = deferred<ProviderBindingDTO>()
    api.getBinding.mockImplementationOnce(() => first.promise)
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(
      await within(d).findByText('Reading the binding from the engine…'),
    ).toBeInTheDocument()
    // The subject is named; no field of the list row stands in for the answer.
    expect(within(d).getAllByText('psb_1').length).toBeGreaterThan(0)
    expect(within(d).queryByText('list-says-this-name')).not.toBeInTheDocument()
    expect(within(d).queryByText(listed.source_id)).not.toBeInTheDocument()
    expect(within(d).queryByText('ppf_a')).not.toBeInTheDocument()
    expect(within(d).getByRole('button', { name: 'Refresh' })).toBeDisabled()

    await user.click(closeButton(d))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(readSignals()[0]?.aborted).toBe(true)
    // Released after the dialog closed: not an answer for anything.
    first.resolve(engineAnswer)
    await act(async () => {})
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByText('engine-says-this-name')).not.toBeInTheDocument()

    await user.click(detailsOf('psb_1'))
    const again = await dialog()
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    expect(readSignals()[1]?.aborted).toBe(false)
    expect(
      await within(again).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    noWrite()
  })

  it('a 403 is the engine’s refusal now: nothing painted, and Retry asks again', async () => {
    api.getBinding.mockRejectedValueOnce(
      new ApiError(403, 'forbidden', 'sessions:profile-binding:read required'),
    )
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(
      await within(d).findByText(/engine refused this binding to your account/),
    ).toBeInTheDocument()
    expect(within(d).queryByText('list-says-this-name')).not.toBeInTheDocument()
    expect(
      within(d).queryByText('engine-says-this-name'),
    ).not.toBeInTheDocument()
    expect(within(d).queryByText('ppf_a')).not.toBeInTheDocument()
    expect(within(d).queryByText(/could not be read/)).not.toBeInTheDocument()
    const retry = within(d).getByRole('button', { name: 'Retry' })
    expect(retry).toBeEnabled()
    await user.click(retry)
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    expect(
      within(d).queryByText(/engine refused this binding/),
    ).not.toBeInTheDocument()
    noWrite()
  })

  it('a 404 is the engine holding no such binding now: the list row does not stand in, and Retry asks again', async () => {
    api.getBinding.mockRejectedValueOnce(
      new ApiError(404, 'not_found', 'provider source binding not found'),
    )
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(
      await within(d).findByText(/holds no binding with this reference/),
    ).toBeInTheDocument()
    // Kept apart from an error and from a refusal, and painted with no fields.
    expect(within(d).queryByText(/could not be read/)).not.toBeInTheDocument()
    expect(within(d).queryByText(/engine refused/)).not.toBeInTheDocument()
    expect(within(d).queryByText('list-says-this-name')).not.toBeInTheDocument()
    expect(within(d).queryByText(listed.source_id)).not.toBeInTheDocument()
    expect(within(d).queryByText('Active')).not.toBeInTheDocument()
    // The list itself is untouched by the 404: no refetch was triggered.
    expect(api.listBindings).toHaveBeenCalledOnce()
    await user.click(within(d).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    expect(
      within(d).queryByText(/holds no binding with this reference/),
    ).not.toBeInTheDocument()
    noWrite()
  })

  it('an error is an error, with its message, and Retry asks again', async () => {
    api.getBinding.mockRejectedValueOnce(new Error('upstream down'))
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    const alert = await within(d).findByRole('alert')
    expect(alert).toHaveTextContent(
      'The binding could not be read: upstream down',
    )
    expect(within(d).queryByText('list-says-this-name')).not.toBeInTheDocument()
    await user.click(within(d).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    expect(within(d).queryByRole('alert')).not.toBeInTheDocument()
    noWrite()
  })

  it('Refresh reads again explicitly and never leaves the older answer on screen while the new one is pending', async () => {
    const second = deferred<ProviderBindingDTO>()
    api.getBinding
      .mockResolvedValueOnce(engineAnswer)
      .mockImplementationOnce(() => second.promise)
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    expect(within(d).getByText('Active')).toBeInTheDocument()

    await user.click(within(d).getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    // Pending again: the earlier answer is gone, not shown as if current.
    expect(
      await within(d).findByText('Reading the binding from the engine…'),
    ).toBeInTheDocument()
    expect(
      within(d).queryByText('engine-says-this-name'),
    ).not.toBeInTheDocument()
    expect(within(d).queryByText('Active')).not.toBeInTheDocument()
    expect(
      within(d).queryByText(/Read from the engine at/),
    ).not.toBeInTheDocument()
    expect(within(d).getByRole('button', { name: 'Refresh' })).toBeDisabled()

    second.resolve(engineRevoked)
    expect(await badge(d, 'Revoked')).toBeInTheDocument()
    expect(within(d).queryByText('Active')).not.toBeInTheDocument()
    expect(within(d).getByText(/Read from the engine at/)).toBeInTheDocument()
    expect(readSignals()[0]?.aborted).toBe(false)
    expect(readSignals()[1]?.aborted).toBe(false)
    noWrite()
  })
})

describe('Binding details — the cycle does not outlive its authority', () => {
  it('losing binding read closes the dialog, aborts the read, and a late answer paints nothing', async () => {
    const pending = deferred<ProviderBindingDTO>()
    api.getBinding.mockImplementationOnce(() => pending.promise)
    grant(BR, BW, BA, PR)
    const user = userEvent.setup()
    const { rerender } = wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    await dialog()
    // Only the binding READ tier leaves; every other tier stays.
    grant(BW, BA, PR)
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(readSignals()[0]?.aborted).toBe(true)
    expect(
      screen.getByText(/does not include sessions:profile-binding:read/),
    ).toBeInTheDocument()
    pending.resolve(engineAnswer)
    await act(async () => {})
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByText('engine-says-this-name')).not.toBeInTheDocument()
    expect(api.getBinding).toHaveBeenCalledOnce()
    // Getting the tier back is not a gesture: the table returns, the dialog does not.
    grant(BR, BW, BA, PR)
    rerender()
    expect(await screen.findAllByText(listed.source_id)).not.toHaveLength(0)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.getBinding).toHaveBeenCalledOnce()
    noWrite()
  })

  it('profile tiers are irrelevant to the read: losing profile read mid-dialog changes nothing', async () => {
    grant(BR, PR)
    const user = userEvent.setup()
    const { rerender } = wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
    grant(BR)
    rerender()
    expect(screen.getByRole('dialog')).toBe(d)
    expect(within(d).getByText('engine-says-this-name')).toBeInTheDocument()
    expect(within(d).getByRole('button', { name: 'Refresh' })).toBeEnabled()
    expect(api.getBinding).toHaveBeenCalledOnce()
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
    'a %s change closes the dialog, aborts the read, discards a late answer and reads nothing on its own',
    async (_what, move) => {
      const pending = deferred<ProviderBindingDTO>()
      api.getBinding.mockImplementationOnce(() => pending.promise)
      const user = userEvent.setup()
      const { rerender } = wrap(homeA)
      await screen.findAllByText(listed.source_id)
      await user.click(detailsOf('psb_1'))
      await dialog()
      act(() => {
        move()
      })
      rerender()
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
      expect(readSignals()[0]?.aborted).toBe(true)
      pending.resolve(engineAnswer)
      await act(async () => {})
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(
        screen.queryByText('engine-says-this-name'),
      ).not.toBeInTheDocument()
      expect(api.getBinding).toHaveBeenCalledOnce()
      noWrite()
    },
  )

  it('an answer that lands after the boundary moved is discarded even when it arrives before the close paints', async () => {
    const pending = deferred<ProviderBindingDTO>()
    api.getBinding.mockImplementationOnce(() => pending.promise)
    const user = userEvent.setup()
    const { rerender } = wrap()
    await screen.findAllByText(listed.source_id)
    await user.click(detailsOf('psb_1'))
    await dialog()
    // The move and the answer arrive in the same tick, before React re-renders.
    await act(async () => {
      auth.principal = 'u2'
      pending.resolve(engineAnswer)
    })
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    expect(screen.queryByText('engine-says-this-name')).not.toBeInTheDocument()
    // Reopening under the new boundary is a NEW read, from nothing.
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    expect(
      await within(d).findByText('engine-says-this-name'),
    ).toBeInTheDocument()
  })
})

describe('Binding details — keyboard, labelling and the rest of the table', () => {
  it('Enter on Details opens a labelled dialog with focus inside; Escape closes it and returns focus to that control', async () => {
    const user = userEvent.setup()
    wrap(homeA)
    await screen.findAllByText(listed.source_id)
    const opener = detailsOf('psb_0')
    opener.focus()
    expect(opener).toHaveFocus()
    await user.keyboard('{Enter}')
    const d = await dialog()
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledOnce())
    expect(api.getBinding.mock.calls[0][0]).toBe('psb_0')
    expect(d).toHaveAccessibleName('Binding details')
    expect(d).toHaveAccessibleDescription(
      /Current binding details for the selected tenant\. Refresh to check for changes\./,
    )
    expect(d.contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    await waitFor(() => expect(opener).toHaveFocus())
  })

  it('Space activates Details too, and the Close button returns focus to the opener', async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    const opener = detailsOf('psb_1')
    opener.focus()
    await user.keyboard(' ')
    const d = await dialog()
    await user.click(closeButton(d))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    await waitFor(() => expect(opener).toHaveFocus())
  })

  it('an admin keeps Revoke beside Details on the active row, and Details never revokes', async () => {
    grant(BR, BW, BA, PR)
    api.revokeBinding.mockResolvedValue(listedRevoked)
    const user = userEvent.setup()
    wrap()
    await screen.findAllByText(listed.source_id)
    const rows = screen.getAllByRole('row').slice(1)
    expect(
      within(rows[0]).getByRole('button', { name: 'Details of binding psb_1' }),
    ).toBeInTheDocument()
    expect(
      within(rows[0]).getByRole('button', { name: 'Revoke' }),
    ).toBeInTheDocument()
    expect(
      within(rows[1]).getByRole('button', { name: 'Details of binding psb_0' }),
    ).toBeInTheDocument()
    expect(
      within(rows[1]).queryByRole('button', { name: 'Revoke' }),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Bind source' })).toBeEnabled()
    await user.click(detailsOf('psb_1'))
    const d = await dialog()
    await within(d).findByText('engine-says-this-name')
    await user.click(within(d).getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(api.getBinding).toHaveBeenCalledTimes(2))
    await user.click(closeButton(d))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    noWrite()
    // The existing revoke path is untouched: confirmation first, then the POST.
    await user.click(screen.getByRole('button', { name: 'Revoke' }))
    const confirm = await screen.findByRole('dialog')
    await user.click(within(confirm).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(api.revokeBinding).toHaveBeenCalledWith('psb_1'))
  })

  it('a reader whose roles are profile-only sees no table and no Details, and asks nothing', async () => {
    grant(PR, 'sessions:profile:write', 'sessions:profile:admin')
    wrap()
    expect(
      await screen.findByText(/does not include sessions:profile-binding:read/),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Details of binding/ }),
    ).not.toBeInTheDocument()
    expect(api.listBindings).not.toHaveBeenCalled()
    expect(api.getBinding).not.toHaveBeenCalled()
  })
})
