// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/lib/i18n'
import { useModulesStore } from '@/stores/modules'
import { usePaneWidthsStore } from '@/stores/pane-widths'
import { PaneWidthsSync } from './pane-widths-sync'

const fixture = vi.hoisted(() => ({
  live: true,
  generation: 1,
  key: 'user-a',
  tenant: 'tenant-a',
  save: vi.fn(),
  load: vi.fn(),
  toastError: vi.fn(),
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
vi.mock('@/features/saved-views/api', () => ({
  savedViewsApi: { uiState: fixture.load, saveUiState: fixture.save },
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { error: fixture.toastError },
}))

const widths = () => usePaneWidthsStore.getState().widths
const loaded = () => usePaneWidthsStore.getState().loaded
// The sync saves after a pause (400 ms); the tests wait for the real one.
const SAVED = { timeout: 3000 }

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  // A new element each time: React skips a render for the very same one.
  const tree = () => (
    <QueryClientProvider client={client}>
      <PaneWidthsSync />
    </QueryClientProvider>
  )
  const view = render(tree())
  return { ...view, client, again: () => view.rerender(tree()) }
}

beforeEach(() => {
  useModulesStore.setState({ off: new Set() })
  usePaneWidthsStore.setState({
    owner: null,
    loaded: false,
    widths: {},
    touched: new Set(),
    persist: null,
  })
  fixture.live = true
  fixture.generation = 1
  fixture.key = 'user-a'
  fixture.tenant = 'tenant-a'
  fixture.toastError.mockReset()
  fixture.load
    .mockReset()
    .mockResolvedValue({ stored: true, pane_widths: { 'sessions-list': 420 } })
  fixture.save
    .mockReset()
    .mockImplementation(async (body) => ({ stored: true, ...body }))
})

describe('PaneWidthsSync', () => {
  it('restores the widths the engine kept for this person on a fresh browser', async () => {
    setup()
    await waitFor(() => expect(widths()).toEqual({ 'sessions-list': 420 }))
    expect(fixture.load).toHaveBeenCalledTimes(1)
  })

  it('saves a changed width to the engine and names only that pane', async () => {
    setup()
    await waitFor(() => expect(loaded()).toBe(true))
    usePaneWidthsStore.getState().commit('sessions-context', 360)
    await waitFor(() => expect(fixture.save).toHaveBeenCalled(), SAVED)
    expect(fixture.save).toHaveBeenCalledWith(
      { pane_widths: { 'sessions-context': 360 } },
      expect.objectContaining({ tenant: 'tenant-a', sessionEffects: 'none' }),
    )
  })

  it('sends one save for a burst of key presses, with the last width of each pane', async () => {
    setup()
    await waitFor(() => expect(loaded()).toBe(true))
    const store = usePaneWidthsStore.getState()
    store.commit('sessions-list', 436)
    store.commit('sessions-list', 452)
    store.commit('sessions-context', 340)
    await waitFor(() => expect(fixture.save).toHaveBeenCalled(), SAVED)
    expect(fixture.save).toHaveBeenCalledTimes(1)
    expect(fixture.save.mock.calls[0][0]).toEqual({
      pane_widths: { 'sessions-list': 452, 'sessions-context': 340 },
    })
  })

  it('tells the engine a pane is back to its default with a width of 0', async () => {
    setup()
    await waitFor(() => expect(loaded()).toBe(true))
    usePaneWidthsStore.getState().reset('sessions-list')
    await waitFor(() => expect(fixture.save).toHaveBeenCalled(), SAVED)
    expect(fixture.save.mock.calls[0][0]).toEqual({
      pane_widths: { 'sessions-list': 0 },
    })
    expect(widths()).toEqual({})
  })

  it('sends a width that is still waiting when the shell goes away', async () => {
    const view = setup()
    await waitFor(() => expect(loaded()).toBe(true))
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    view.unmount()
    await waitFor(() => expect(fixture.save).toHaveBeenCalledTimes(1))
    expect(fixture.save.mock.calls[0][0]).toEqual({
      pane_widths: { 'sessions-list': 500 },
    })
  })

  it('says so when a width cannot be saved, and keeps it for this visit', async () => {
    fixture.save.mockRejectedValue(new Error('engine unreachable'))
    setup()
    await waitFor(() => expect(loaded()).toBe(true))
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    await waitFor(() => expect(fixture.toastError).toHaveBeenCalled(), SAVED)
    expect(fixture.toastError).toHaveBeenCalledWith(
      'Could not save the panel width. It applies for this visit only.',
    )
    expect(widths()['sessions-list']).toBe(500)
  })

  it('does not report a failure for a width whose sign-in has ended', async () => {
    fixture.save.mockRejectedValue(
      new Error('Interface setting lifetime ended'),
    )
    setup()
    await waitFor(() => expect(loaded()).toBe(true))
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    fixture.live = false
    // The save runs after its pause and the lifetime guard refuses it before any request.
    await new Promise((resolve) => setTimeout(resolve, 800))
    expect(fixture.save).not.toHaveBeenCalled()
    expect(fixture.toastError).not.toHaveBeenCalled()
  })

  it('applies a width for this visit without sending it while the engine keeps no interface state', async () => {
    useModulesStore.setState({ off: new Set(['consoleviews']) })
    setup()
    await waitFor(() =>
      expect(usePaneWidthsStore.getState().owner).toBe('tenant-a/user-a'),
    )
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    await new Promise((resolve) => setTimeout(resolve, 700))
    expect(widths()['sessions-list']).toBe(500)
    expect(fixture.load).not.toHaveBeenCalled()
    expect(fixture.save).not.toHaveBeenCalled()
    expect(fixture.toastError).not.toHaveBeenCalled()
  })

  it('keeps saving after a later refresh of the read fails, as the engine took the first read', async () => {
    const { client } = setup()
    await waitFor(() => expect(loaded()).toBe(true))
    fixture.load.mockRejectedValue(new Error('network'))
    await client.refetchQueries()
    expect(client.getQueryCache().getAll()[0].state.status).toBe('error')
    // The failed refresh settles after the request; a width set before it renders would be
    // sent by the effect's cleanup and hide the difference.
    await new Promise((resolve) => setTimeout(resolve, 100))
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    await waitFor(() => expect(fixture.save).toHaveBeenCalled(), SAVED)
    expect(fixture.save.mock.calls[0][0]).toEqual({
      pane_widths: { 'sessions-list': 500 },
    })
  })

  it('keeps a width for this visit without a save that will fail when the engine refuses the read', async () => {
    fixture.load.mockRejectedValue(new Error('forbidden'))
    setup()
    await waitFor(() => expect(fixture.load).toHaveBeenCalled())
    // The refused read settles after the first request; a save before it would not tell.
    await new Promise((resolve) => setTimeout(resolve, 100))
    usePaneWidthsStore.getState().commit('sessions-list', 500)
    await new Promise((resolve) => setTimeout(resolve, 700))
    expect(widths()['sessions-list']).toBe(500)
    expect(fixture.save).not.toHaveBeenCalled()
    expect(fixture.toastError).not.toHaveBeenCalled()
  })

  it("gives the next person on this browser their own widths, not the last one's", async () => {
    const view = setup()
    await waitFor(() => expect(widths()).toEqual({ 'sessions-list': 420 }))
    fixture.key = 'user-b'
    fixture.load.mockResolvedValue({
      stored: true,
      pane_widths: { 'sessions-context': 300 },
    })
    view.again()
    await waitFor(() => expect(widths()).toEqual({ 'sessions-context': 300 }))
  })

  it('keeps the widths on screen when only the credential rotates', async () => {
    const view = setup()
    await waitFor(() => expect(widths()).toEqual({ 'sessions-list': 420 }))
    usePaneWidthsStore.getState().commit('sessions-context', 300)
    fixture.generation = 2
    view.again()
    await waitFor(() => expect(fixture.load).toHaveBeenCalledTimes(2))
    expect(widths()).toEqual({ 'sessions-list': 420, 'sessions-context': 300 })
    // The width set just before the rotation still reaches the engine.
    await waitFor(() => expect(fixture.save).toHaveBeenCalledTimes(1), SAVED)
    expect(fixture.save.mock.calls[0][0]).toEqual({
      pane_widths: { 'sessions-context': 300 },
    })
  })
})
