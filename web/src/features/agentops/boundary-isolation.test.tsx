// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C3 — a new authority boundary never paints the previous one's QueryCache. The profile
// plane's keys are partitioned by the boundary's opaque epoch (principal | tenant |
// credential), so with the SAME tenant and the SAME permission a change of principal, or
// of credential, starts from nothing: no row of the previous operator is shown while the
// new operator's answer is pending, a late answer of the previous operator lands nowhere,
// and the previous entries are cancelled and removed. Real QueryClient, mocked client.
//
// THREE moves, because the engine has three ways of changing who is calling: another
// principal; another session (login, a new session id); and a RENEWAL — `POST
// /v1/auth/refresh` rotates the bearer in place and answers with the SAME session id, so
// the third move keeps the session id byte for byte and changes only the credential.
// Every case also checks that no key under the plane carries the bearer or the session id.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  getProfile: vi.fn(),
  listBindings: vi.fn(),
  createBinding: vi.fn(),
  revokeBinding: vi.fn(),
  profileConfiguration: vi.fn(),
  listWorkspaces: vi.fn(),
  createRun: vi.fn(),
  profileLaunchReadiness: vi.fn(),
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
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))

import { BindingsTable } from './bindings-table'
import { fixtureReadiness } from './launch-readiness.fixture'
import { ProfilesPanel } from './profiles-panel'
import { RunCreateDialog } from './run-create-dialog'
import type { ProviderBindingDTO, ProviderProfileDTO } from './types'

const PR = 'sessions:profile:read'
const BR = 'sessions:profile-binding:read'
const BW = 'sessions:profile-binding:write'

const ownedBy = (who: string): ProviderProfileDTO => ({
  profile_ref: `ppf_${who}`,
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: `Profile owned by principal ${who}`,
  state: 'active',
  local_environment: true,
  operable: true,
})
const bindingOf = (who: string): ProviderBindingDTO => ({
  binding_ref: `psb_${who}`,
  source_id: `0192f2c0-aaaa-7000-8000-00000000000${who === 'A' ? 1 : 2}`,
  source_revision: 3,
  source_name: `source of ${who}`,
  environment_ref: 'xenv_1',
  selector_key: 'dedicated',
  profile_ref: `ppf_${who}`,
  driver: 'claude',
  state: 'active',
  bound_at: '2026-09-01T10:00:00Z',
})

/** A response the test releases when it decides, plus the signal the read was given. */
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}
const signalOf = (call: unknown[]) =>
  (call[1] as { signal?: AbortSignal } | undefined)?.signal

/** A FRESH element per render: React bails out of re-rendering an identical element
 * reference, which would hide a boundary change the real AuthContext always delivers. */
function wrap(ui: () => React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const r = render(
    <QueryClientProvider client={qc}>{ui()}</QueryClientProvider>,
  )
  return {
    qc,
    rerender: () =>
      r.rerender(<QueryClientProvider client={qc}>{ui()}</QueryClientProvider>),
  }
}

const EXP = '2030-01-01T00:00:00Z'
/** The session id of every case, and of the rotated credential: byte for byte. */
const SID = 'sid-fixed'
let rotations = 0
const moves: Array<[string, () => void]> = [
  ['principal', () => (auth.principal = 'u2')],
  [
    'new-session credential',
    () =>
      useSessionStore.getState().setSession({
        token: 'olvs_next',
        sessionId: `sid-${Date.now()}`,
        expiresAt: EXP,
      }),
  ],
  [
    'same-session credential rotation',
    () => {
      // Faithful to the engine's RefreshSession: a new bearer, the same session id.
      const before = useSessionStore.getState().sessionId
      useSessionStore.getState().setSession({
        token: `olvs_rotated_${++rotations}`,
        sessionId: SID,
        expiresAt: EXP,
      })
      expect(before).toBe(SID)
      expect(useSessionStore.getState().sessionId).toBe(before)
    },
  ],
]

/** Every key of the plane is partitioned by an OPAQUE number, and none of them — nor
 * any other key in the cache — carries the bearer or the session id. */
function expectOpaqueKeys(qc: QueryClient) {
  const { token, sessionId } = useSessionStore.getState()
  const keys = qc
    .getQueryCache()
    .findAll()
    .map((q) => q.queryKey as readonly unknown[])
  const plane = keys.filter((k) => k[0] === 'agentops' && k[2] === 'b')
  expect(plane.length).toBeGreaterThan(0)
  for (const k of plane) expect(typeof k[3]).toBe('number')
  const flat = JSON.stringify(keys)
  expect(flat).not.toContain('olvs_')
  if (token) expect(flat).not.toContain(token)
  if (sessionId) expect(flat).not.toContain(sessionId)
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.tenant = 't1'
  auth.principal = 'u1'
  auth.perms = new Set([PR, BR, BW])
  // The credential every case starts under. A direct write, as rehydration is: not a
  // transition, so the generation is whatever the previous case left (monotonic).
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: SID,
    expiresAt: EXP,
  })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  consoleApi.listSources.mockResolvedValue({ sources: [] })
  api.getProfile.mockImplementation(async (ref: string) =>
    ownedBy(ref.replace('ppf_', '')),
  )
  api.profileLaunchReadiness.mockImplementation(async (ref: string) =>
    fixtureReadiness({ profile_ref: ref }),
  )
})

describe('C3 — profiles list under a moved boundary (same tenant, same permission)', () => {
  it.each(moves)(
    'a %s change shows nothing of A while B is pending, and paints B when B answers',
    async (_what, move) => {
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles
        .mockResolvedValueOnce({ items: [ownedBy('A')], has_more: false })
        .mockReturnValueOnce(b.promise)
      const { qc, rerender } = wrap(() => <ProfilesPanel />)
      expect(
        await screen.findByText('Profile owned by principal A'),
      ).toBeInTheDocument()
      act(() => {
        move()
      })
      rerender()
      // The new boundary has its own key: nothing of A is painted, and B's read is
      // in flight under the current authority.
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      expect(
        screen.queryByText('Profile owned by principal A'),
      ).not.toBeInTheDocument()
      // A's cached entry left with A's boundary.
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      b.resolve({ items: [ownedBy('B')], has_more: false })
      expect(
        await screen.findByText('Profile owned by principal B'),
      ).toBeInTheDocument()
      expect(
        screen.queryByText('Profile owned by principal A'),
      ).not.toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a LATE answer of A released after a %s change is cancelled and never painted',
    async (_what, move) => {
      const a = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles
        .mockReturnValueOnce(a.promise)
        .mockReturnValueOnce(b.promise)
      const { qc, rerender } = wrap(() => <ProfilesPanel />)
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      // The previous boundary's request was aborted…
      expect(signalOf(api.listProfiles.mock.calls[0])?.aborted).toBe(true)
      // …and its answer, arriving anyway, occupies nothing of the current boundary.
      await act(async () => {
        a.resolve({ items: [ownedBy('A')], has_more: false })
      })
      expect(
        screen.queryByText('Profile owned by principal A'),
      ).not.toBeInTheDocument()
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      b.resolve({ items: [ownedBy('B')], has_more: false })
      expect(
        await screen.findByText('Profile owned by principal B'),
      ).toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a profile DETAIL read under A is not shown after a %s change, and prefix invalidation reaches the current keys',
    async (_what, move) => {
      api.listProfiles.mockResolvedValue({
        items: [ownedBy('A')],
        has_more: false,
      })
      const user = userEvent.setup()
      const { qc, rerender } = wrap(() => <ProfilesPanel />)
      const row = (
        await screen.findByText('Profile owned by principal A')
      ).closest('tr') as HTMLElement
      await user.click(
        within(row).getByRole('button', { name: /row actions/i }),
      )
      await user.click(
        await screen.findByRole('menuitem', { name: /^details$/i }),
      )
      await screen.findByRole('dialog')
      await waitFor(() =>
        expect(api.getProfile).toHaveBeenCalledWith('ppf_A', expect.anything()),
      )
      // B's list is what the moved boundary reads. A credential move re-renders
      // through the store synchronously, so the answer is in place before the move.
      api.listProfiles.mockResolvedValue({
        items: [ownedBy('B')],
        has_more: false,
      })
      act(() => {
        move()
      })
      rerender()
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
      expect(
        await screen.findByText('Profile owned by principal B'),
      ).toBeInTheDocument()
      // The detail read under A went with A's boundary: nothing of it is cached.
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      expect(
        qc
          .getQueryCache()
          .findAll()
          .some((q) => q.queryKey.includes('profile')),
      ).toBe(false)
      expectOpaqueKeys(qc)
      // Invalidating the current boundary's list prefix reaches its query.
      const before = api.listProfiles.mock.calls.length
      const keys = qc
        .getQueryCache()
        .findAll()
        .map((q) => q.queryKey)
        .filter((k) => k.includes('profiles'))
      expect(keys).toHaveLength(1)
      await act(async () => {
        await qc.invalidateQueries({ queryKey: keys[0].slice(0, 5) })
      })
      await waitFor(() =>
        expect(api.listProfiles.mock.calls.length).toBeGreaterThan(before),
      )
    },
  )

  it.each(moves)(
    'a LATE detail answer of A released after a %s change is cancelled and never cached',
    async (_what, move) => {
      api.listProfiles.mockResolvedValue({
        items: [ownedBy('A')],
        has_more: false,
      })
      const a = deferred<ProviderProfileDTO>()
      api.getProfile.mockReturnValueOnce(a.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap(() => <ProfilesPanel />)
      const row = (
        await screen.findByText('Profile owned by principal A')
      ).closest('tr') as HTMLElement
      await user.click(
        within(row).getByRole('button', { name: /row actions/i }),
      )
      await user.click(
        await screen.findByRole('menuitem', { name: /^details$/i }),
      )
      await waitFor(() => expect(api.getProfile).toHaveBeenCalledOnce())
      api.listProfiles.mockResolvedValue({
        items: [ownedBy('B')],
        has_more: false,
      })
      act(() => {
        move()
      })
      rerender()
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
      expect(signalOf(api.getProfile.mock.calls[0])?.aborted).toBe(true)
      await act(async () => {
        a.resolve({ ...ownedBy('A'), display_name: 'Late detail of A' })
      })
      expect(screen.queryByText('Late detail of A')).not.toBeInTheDocument()
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('Late detail of A')),
      ).toBe(false)
      expect(
        await screen.findByText('Profile owned by principal B'),
      ).toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )
})

describe('C3 — bindings and the picker under a moved boundary', () => {
  it.each(moves)(
    'the tenant-wide bindings list shows nothing of A after a %s change until B answers',
    async (_what, move) => {
      const b = deferred<{ items: ProviderBindingDTO[]; has_more: boolean }>()
      api.listBindings
        .mockResolvedValueOnce({ items: [bindingOf('A')], has_more: false })
        .mockReturnValueOnce(b.promise)
      const { qc, rerender } = wrap(() => <BindingsTable />)
      expect(await screen.findByText('source of A')).toBeInTheDocument()
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listBindings).toHaveBeenCalledTimes(2))
      expect(screen.queryByText('source of A')).not.toBeInTheDocument()
      b.resolve({ items: [bindingOf('B')], has_more: false })
      expect(await screen.findByText('source of B')).toBeInTheDocument()
      expect(screen.queryByText('source of A')).not.toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a LATE bindings answer of A released after a %s change is cancelled and never painted',
    async (_what, move) => {
      const a = deferred<{ items: ProviderBindingDTO[]; has_more: boolean }>()
      const b = deferred<{ items: ProviderBindingDTO[]; has_more: boolean }>()
      api.listBindings
        .mockReturnValueOnce(a.promise)
        .mockReturnValueOnce(b.promise)
      const { qc, rerender } = wrap(() => <BindingsTable />)
      await waitFor(() => expect(api.listBindings).toHaveBeenCalledOnce())
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listBindings).toHaveBeenCalledTimes(2))
      expect(signalOf(api.listBindings.mock.calls[0])?.aborted).toBe(true)
      await act(async () => {
        a.resolve({ items: [bindingOf('A')], has_more: false })
      })
      expect(screen.queryByText('source of A')).not.toBeInTheDocument()
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('source of A')),
      ).toBe(false)
      b.resolve({ items: [bindingOf('B')], has_more: false })
      expect(await screen.findByText('source of B')).toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'the bind picker read under A is not offered after a %s change',
    async (_what, move) => {
      api.listProfiles.mockResolvedValueOnce({
        items: [ownedBy('A')],
        has_more: false,
      })
      const user = userEvent.setup()
      const { qc, rerender } = wrap(() => <BindingsTable />)
      await waitFor(() => expect(api.listBindings).toHaveBeenCalled())
      await user.click(
        await screen.findByRole('button', { name: 'Bind source' }),
      )
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByLabelText('Profile'))
      await user.click(
        await screen.findByRole('option', { name: /principal A/ }),
      )
      expect(within(dialog).getByLabelText('Profile')).toHaveTextContent(
        'principal A',
      )
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles.mockReturnValueOnce(b.promise)
      act(() => {
        move()
      })
      rerender()
      // The dialog is keyed by the boundary: it remounts, and the picker under the
      // new boundary has nothing of A to offer — nor A's selection — while its own
      // read is pending.
      const after = await screen.findByRole('dialog')
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      expect(within(after).getByLabelText('Profile')).toHaveTextContent(
        'Select a profile',
      )
      await user.click(within(after).getByLabelText('Profile'))
      expect(
        screen.queryByRole('option', { name: /principal A/ }),
      ).not.toBeInTheDocument()
      await user.keyboard('{Escape}')
      b.resolve({ items: [ownedBy('B')], has_more: false })
      await user.click(within(after).getByLabelText('Profile'))
      expect(
        await screen.findByRole('option', { name: /principal B/ }),
      ).toBeInTheDocument()
      await user.keyboard('{Escape}')
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a LATE bind-picker answer of A released after a %s change is cancelled and never offered',
    async (_what, move) => {
      const a = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles
        .mockReturnValueOnce(a.promise)
        .mockReturnValueOnce(b.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap(() => <BindingsTable />)
      await waitFor(() => expect(api.listBindings).toHaveBeenCalled())
      await user.click(
        await screen.findByRole('button', { name: 'Bind source' }),
      )
      await screen.findByRole('dialog')
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
      act(() => {
        move()
      })
      rerender()
      const after = await screen.findByRole('dialog')
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      expect(signalOf(api.listProfiles.mock.calls[0])?.aborted).toBe(true)
      await act(async () => {
        a.resolve({ items: [ownedBy('A')], has_more: false })
      })
      await user.click(within(after).getByLabelText('Profile'))
      expect(
        screen.queryByRole('option', { name: /principal A/ }),
      ).not.toBeInTheDocument()
      await user.keyboard('{Escape}')
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      b.resolve({ items: [ownedBy('B')], has_more: false })
      await user.click(within(after).getByLabelText('Profile'))
      expect(
        await screen.findByRole('option', { name: /principal B/ }),
      ).toBeInTheDocument()
      await user.keyboard('{Escape}')
      expectOpaqueKeys(qc)
    },
  )
})

describe('C3 — the launch profile picker under a moved boundary', () => {
  const launch = () => <RunCreateDialog open onOpenChange={vi.fn()} />

  it.each(moves)(
    'the launch picker forgets the selection and offers nothing of A after a %s change until B answers',
    async (_what, move) => {
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles
        .mockResolvedValueOnce({ items: [ownedBy('A')], has_more: false })
        .mockReturnValueOnce(b.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap(launch)
      await user.click(await screen.findByLabelText('Provider profile'))
      await user.click(
        await screen.findByRole('option', { name: /principal A/ }),
      )
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        'principal A',
      )
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      // The choice made under the previous boundary is gone with its list…
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        'Select a profile',
      )
      expect(screen.queryByText(/ppf_A/)).not.toBeInTheDocument()
      await user.click(screen.getByLabelText('Provider profile'))
      expect(
        screen.queryByRole('option', { name: /principal A/ }),
      ).not.toBeInTheDocument()
      await user.keyboard('{Escape}')
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      // …and the current boundary's own answer is what gets offered.
      b.resolve({ items: [ownedBy('B')], has_more: false })
      await user.click(screen.getByLabelText('Provider profile'))
      expect(
        await screen.findByRole('option', { name: /principal B/ }),
      ).toBeInTheDocument()
      await user.keyboard('{Escape}')
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a LATE launch-picker answer of A released after a %s change is cancelled and never offered',
    async (_what, move) => {
      const a = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      const b = deferred<{ items: ProviderProfileDTO[]; has_more: boolean }>()
      api.listProfiles
        .mockReturnValueOnce(a.promise)
        .mockReturnValueOnce(b.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap(launch)
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listProfiles).toHaveBeenCalledTimes(2))
      expect(signalOf(api.listProfiles.mock.calls[0])?.aborted).toBe(true)
      await act(async () => {
        a.resolve({ items: [ownedBy('A')], has_more: false })
      })
      await user.click(screen.getByLabelText('Provider profile'))
      expect(
        screen.queryByRole('option', { name: /principal A/ }),
      ).not.toBeInTheDocument()
      await user.keyboard('{Escape}')
      expect(
        qc
          .getQueryCache()
          .findAll()
          .map((q) => JSON.stringify(q.state.data ?? null))
          .some((d) => d.includes('principal A')),
      ).toBe(false)
      b.resolve({ items: [ownedBy('B')], has_more: false })
      await user.click(screen.getByLabelText('Provider profile'))
      expect(
        await screen.findByRole('option', { name: /principal B/ }),
      ).toBeInTheDocument()
      await user.keyboard('{Escape}')
      expectOpaqueKeys(qc)
    },
  )
})
