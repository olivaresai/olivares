// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, it, expect, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useEditionBadge } from './use-edition-badge'
import { useModulesStore } from '@/stores/modules'
const fixture = vi.hoisted(() => ({
  live: true,
  generation: 1,
  key: 'user-a',
  tenant: 'tenant-a',
  save: vi.fn(),
  load: vi.fn(),
  server: vi.fn(),
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => ({
    scope: {
      key: fixture.key,
      tenant: fixture.tenant,
      generation: fixture.generation,
      live: () => fixture.live,
    },
  }),
}))
vi.mock('@/lib/api/endpoints', () => ({
  authApi: { serverInfo: fixture.server },
}))
vi.mock('@/features/saved-views/api', () => ({
  savedViewsApi: { uiState: fixture.load, saveUiState: fixture.save },
}))
function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { client, ...renderHook(() => useEditionBadge(), { wrapper }) }
}
beforeEach(() => {
  useModulesStore.setState({ off: new Set() })
  fixture.live = true
  fixture.generation = 1
  fixture.key = 'user-a'
  fixture.tenant = 'tenant-a'
  fixture.load
    .mockReset()
    .mockResolvedValue({ stored: false, show_edition_badge: true })
  fixture.save
    .mockReset()
    .mockImplementation(async (body) => ({ stored: true, ...body }))
  fixture.server.mockReset().mockResolvedValue({
    edition: 'enterprise',
    license: { status: 'valid', support_profile: 'enterprise' },
  })
})
it('defaults on, saves one preference on the server and restores it on a fresh client', async () => {
  const a = setup()
  await waitFor(() => expect(a.result.current.canSave).toBe(true))
  expect(a.result.current.edition).toBe('enterprise')
  expect(a.result.current.shown).toBe(true)
  await act(async () => {
    await a.result.current.setShown(false)
  })
  expect(fixture.save).toHaveBeenCalledWith(
    { show_edition_badge: false },
    expect.objectContaining({ tenant: 'tenant-a', sessionEffects: 'none' }),
  )
  await waitFor(() => expect(a.result.current.shown).toBe(false))
  a.unmount()
  a.client.clear()
  fixture.load.mockResolvedValue({
    stored: true,
    sidebar: 'rail',
    show_edition_badge: false,
  })
  const b = setup()
  await waitFor(() => expect(b.result.current.canSave).toBe(true))
  expect(b.result.current.shown).toBe(false)
  b.unmount()
  b.client.clear()
})
it('hides expired/removed licences and failed refreshes instead of stale facts', async () => {
  const a = setup()
  await waitFor(() => expect(a.result.current.edition).toBe('enterprise'))
  for (const status of ['expired', 'none']) {
    fixture.server.mockResolvedValue({
      edition: 'enterprise',
      license: { status, support_profile: 'enterprise' },
    })
    await act(async () => {
      await a.client.invalidateQueries({ queryKey: ['server-info'] })
    })
    await waitFor(() => expect(a.result.current.edition).toBeNull())
  }
  fixture.server.mockRejectedValue(new Error('fixture unavailable'))
  await act(async () => {
    await a.client.invalidateQueries({ queryKey: ['server-info'] })
  })
  await waitFor(() => expect(a.result.current.edition).toBeNull())
  a.unmount()
  a.client.clear()
})
it('does not send an old preference through a retired identity and reports save failures', async () => {
  const a = setup()
  await waitFor(() => expect(a.result.current.canSave).toBe(true))
  fixture.save.mockRejectedValue(new Error('fixture write failure'))
  await act(async () => {
    await a.result.current.setShown(false).catch(() => {})
  })
  expect(a.result.current.shown).toBe(true)
  await waitFor(() => expect(a.result.current.saveError).toBe(true))
  fixture.live = false
  await act(async () => {
    await a.result.current.setShown(false).catch(() => {})
  })
  expect(fixture.save).toHaveBeenCalledTimes(1)
  a.unmount()
  a.client.clear()
})

it('does not revive a hidden badge while identity or its preference store is unavailable', async () => {
  fixture.load.mockResolvedValue({ stored: true, show_edition_badge: false })
  const a = setup()
  await waitFor(() => expect(a.result.current.canSave).toBe(true))
  expect(a.result.current.shown).toBe(false)
  fixture.live = false
  a.rerender()
  expect(a.result.current.shown).toBe(false)
  a.unmount()
  a.client.clear()
})

it('keeps a hidden badge hidden when consoleviews is disabled', async () => {
  fixture.load.mockResolvedValue({ stored: true, show_edition_badge: false })
  const a = setup()
  await waitFor(() => expect(a.result.current.canSave).toBe(true))
  await act(async () => {
    useModulesStore.setState({ off: new Set(['consoleviews']) })
  })
  expect(a.result.current.shown).toBe(false)
  expect(a.result.current.canSave).toBe(false)
  a.unmount()
  a.client.clear()
})
async function serverReads(edition: string, status: string) {
  fixture.server.mockClear()
  fixture.server.mockResolvedValue({ edition, license: { status } })
  vi.useFakeTimers()
  try {
    const a = setup()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(90_000)
    })
    a.unmount()
    a.client.clear()
    return fixture.server.mock.calls.length
  } finally {
    vi.useRealTimers()
  }
}
it('polls server-info only on a commercial build, never on Community', async () => {
  expect(await serverReads('community', 'none')).toBe(1)
  // An expired licence keeps polling: a renewed one must reappear without a reload.
  expect(await serverReads('business', 'expired')).toBeGreaterThanOrEqual(3)
  expect(await serverReads('enterprise', 'valid')).toBeGreaterThanOrEqual(3)
})
it('keeps retrying a failed first read, so the badge appears once it succeeds', async () => {
  const down = new Error('fixture unavailable')
  fixture.server
    .mockReset()
    .mockRejectedValueOnce(down)
    .mockRejectedValueOnce(down)
    .mockResolvedValue({
      edition: 'enterprise',
      license: { status: 'valid', support_profile: 'enterprise' },
    })
  vi.useFakeTimers()
  try {
    const a = setup()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(a.result.current.edition).toBeNull()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000)
    })
    expect(a.result.current.edition).toBe('enterprise')
    a.unmount()
    a.client.clear()
  } finally {
    vi.useRealTimers()
  }
})
