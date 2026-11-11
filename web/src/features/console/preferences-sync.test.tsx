// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useModulesStore } from '@/stores/modules'
import { useClientSettings } from '@/features/settings/preferences'
import { preferenceStorage } from '@/lib/preference-storage'
import { PreferencesSync } from './preferences-sync'
const fixture = vi.hoisted(() => ({
  user: 'alice',
  verified: true,
  pending: false,
  live: () => fixture.verified,
  load: vi.fn(),
  save: vi.fn(),
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => ({
    scope: {
      key:
        'olivares.navigation.favorites:' +
        JSON.stringify(['https://console', 'user', fixture.user, 'tenant']),
      tenant: 'tenant',
      generation: 1,
      pending: fixture.pending,
      live: fixture.live,
    },
  }),
}))
vi.mock('@/features/saved-views/api', () => ({
  savedViewsApi: { uiState: fixture.load, saveUiState: fixture.save },
}))
vi.mock('@/components/ui/toaster', () => ({ toast: { error: vi.fn() } }))
function Consumer() {
  const clock = useClientSettings((s) => s.clock)
  return <p>{clock}</p>
}
function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const tree = () => (
    <QueryClientProvider client={client}>
      <PreferencesSync>
        <Consumer />
      </PreferencesSync>
    </QueryClientProvider>
  )
  const view = render(tree())
  return { ...view, client, again: () => view.rerender(tree()) }
}
beforeEach(() => {
  localStorage.clear()
  preferenceStorage.attach(null, {})
  fixture.user = 'alice'
  fixture.verified = true
  fixture.pending = false
  useModulesStore.setState({ off: new Set() })
  fixture.load.mockReset().mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({
        state: { clock: '12' },
        version: 0,
      }),
    },
  })
  fixture.save
    .mockReset()
    .mockImplementation(async (state) => ({ stored: true, ...state }))
})
it('hydrates a fresh browser before showing settings and saves changes through the API', async () => {
  setup()
  await screen.findByText('12')
  act(() => useClientSettings.getState().setClock('24'))
  await waitFor(() => expect(fixture.save).toHaveBeenCalled())
  const preferences = fixture.save.mock.calls[0][0].preferences
  expect(JSON.parse(preferences['olivares.settings']).state.clock).toBe('24')
  expect(localStorage.getItem('olivares.settings')).toBeNull()
})
it('restores session defaults and event choices in a second browser from the server', async () => {
  const first = setup()
  await screen.findByText('12')
  act(() => {
    useClientSettings.getState().setSessionDefaults({
      tool: 'codex',
      account: 'account-one',
      model: 'model-one',
      folder: 'folder-one',
    })
    useClientSettings
      .getState()
      .setNotificationEvents({ needsYou: true, finished: false, failed: true })
  })
  await waitFor(() => expect(fixture.save).toHaveBeenCalledTimes(2))
  const stored = fixture.save.mock.calls.at(-1)![0]
  first.unmount()
  localStorage.clear()
  fixture.load.mockResolvedValue({ stored: true, ...stored })
  setup()
  await screen.findByText('12')
  expect(useClientSettings.getState().sessionDefaults).toEqual({
    tool: 'codex',
    account: 'account-one',
    model: 'model-one',
    effort: '',
    folder: 'folder-one',
  })
  expect(useClientSettings.getState().notificationEvents).toEqual({
    needsYou: true,
    finished: false,
    failed: true,
  })
  expect(localStorage.getItem('olivares.settings')).toBeNull()
})

it('uses defaults for malformed nested settings received from the server', async () => {
  fixture.load.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({
        state: {
          clock: '12',
          sessionDefaults: null,
          notificationEvents: { finished: 'yes' },
          inAppEvents: { needsYou: 'no', failed: false },
        },
        version: 0,
      }),
    },
  })
  setup()
  await screen.findByText('12')
  expect(useClientSettings.getState().sessionDefaults).toEqual({
    tool: '',
    account: '',
    model: '',
    effort: '',
    folder: '',
  })
  expect(useClientSettings.getState().notificationEvents).toEqual({
    needsYou: true,
    finished: true,
    failed: true,
  })
  // The console's own choice: a field that is not a boolean is the default, one that is stays.
  expect(useClientSettings.getState().inAppEvents).toEqual({
    needsYou: true,
    finished: true,
    failed: false,
  })
})
// The key edits of Settings › Keyboard come back from the server as stored text; a
// value that is not text is not an edit.
it("keeps a person's key edits from the server, and only those stored as text", async () => {
  fixture.load.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({
        state: {
          clock: '12',
          keyOverrides: { 'palette.open|': 'Mod+p', 'help.toggle|': 7 },
        },
        version: 0,
      }),
    },
  })
  setup()
  await screen.findByText('12')
  expect(useClientSettings.getState().keyOverrides).toEqual({
    'palette.open|': 'Mod+p',
  })
  useClientSettings.setState({ keyOverrides: {} })
})
// The settings keep the key edits as stored text: the keybindings read each against the
// table, as when it was made (lib/keybindings/model.test.ts), and the store, which the
// registry's views read, knows nothing of the table.
it('keeps the stored key edits as text, for the keybindings to read against the table', async () => {
  fixture.load.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({
        state: {
          clock: '12',
          keyOverrides: {
            'palette.open|': 'Mod+p',
            'palette.close|': 'Mod+n',
            'approvals.deny|approvalQueue': 'x',
            'help.toggle|': 'Mod+p',
          },
        },
        version: 0,
      }),
    },
  })
  setup()
  await screen.findByText('12')
  expect(useClientSettings.getState().keyOverrides).toEqual({
    'palette.open|': 'Mod+p',
    'palette.close|': 'Mod+n',
    'approvals.deny|approvalQueue': 'x',
    'help.toggle|': 'Mod+p',
  })
  // The keybindings read them against the table when they first load (here, after the
  // blob: nothing in this file loaded them before) and on every change of the store: a
  // stored edit that cannot work never reaches the resolver.
  const { resolveBinding } = await import('@/lib/keybindings/model')
  const { KEYBINDINGS } = await import('@/lib/keybindings/table')
  await import('@/lib/keybindings/effective')
  const resolved = () => [
    resolveBinding(KEYBINDINGS, { key: 'x' }, { approvalQueue: true }),
    resolveBinding(KEYBINDINGS, { key: 'p', ctrlKey: true }),
  ]
  expect(resolved()).toEqual([null, 'palette.open'])
  const stored = useClientSettings.getState().keyOverrides
  act(() => useClientSettings.setState({ keyOverrides: { ...stored } }))
  expect(resolved()).toEqual([null, 'palette.open'])
  useClientSettings.setState({ keyOverrides: {} })
})
// Two rows whose keys a person swapped through a third key are saved in the order the
// edits were made: the re-edited row last. Both come back (and both work: model.test.ts).
it('reads back two rows whose keys a person swapped, in the order they were saved', async () => {
  fixture.load.mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({
        state: {
          clock: '12',
          keyOverrides: {
            'shell.sidebar|': 'Mod+k',
            'palette.open|': 'Mod+b',
          },
        },
        version: 0,
      }),
    },
  })
  setup()
  await screen.findByText('12')
  expect(useClientSettings.getState().keyOverrides).toEqual({
    'palette.open|': 'Mod+b',
    'shell.sidebar|': 'Mod+k',
  })
  useClientSettings.setState({ keyOverrides: {} })
})
it('resets a different user to defaults without leaking the first user settings', async () => {
  const view = setup()
  await screen.findByText('12')
  fixture.user = 'bob'
  fixture.load.mockResolvedValue({ stored: false })
  view.again()
  await screen.findByText('24')
  expect(fixture.save).not.toHaveBeenCalled()
})
it('moves legacy settings once and only removes them after the server accepts them', async () => {
  fixture.load.mockResolvedValue({ stored: false })
  const legacy = JSON.stringify({ state: { clock: '12' }, version: 0 })
  localStorage.setItem('olivares.settings', legacy)
  setup()
  await screen.findByText('12')
  await waitFor(() =>
    expect(localStorage.getItem('olivares.settings')).toBeNull(),
  )
  expect(fixture.save.mock.calls[0][0].preferences['olivares.settings']).toBe(
    legacy,
  )
})

it('keeps loaded preferences when a background server refresh fails', async () => {
  const view = setup()
  await screen.findByText('12')
  fixture.load.mockRejectedValue(new Error('offline'))
  await act(async () => {
    await view.client.invalidateQueries({ queryKey: ['consoleviews'] })
  })
  expect(screen.getByText('12')).toBeVisible()
  act(() => useClientSettings.getState().setClock('24'))
  await waitFor(() => expect(fixture.save).toHaveBeenCalled())
})

it('retains legacy data on a failed migration and retries it with the next change', async () => {
  fixture.load.mockResolvedValue({ stored: false })
  fixture.save.mockRejectedValueOnce(new Error('offline'))
  const legacy = JSON.stringify({ state: { density: 'compact' }, version: 0 })
  localStorage.setItem('olivares.prefs', legacy)
  setup()
  await screen.findByText('24')
  await waitFor(() => expect(fixture.save).toHaveBeenCalledTimes(1))
  expect(localStorage.getItem('olivares.prefs')).toBe(legacy)
  act(() => useClientSettings.getState().setClock('12'))
  await waitFor(() => expect(fixture.save).toHaveBeenCalledTimes(2))
  expect(fixture.save.mock.calls[1][0].preferences['olivares.prefs']).toBe(
    legacy,
  )
  await waitFor(() => expect(localStorage.getItem('olivares.prefs')).toBeNull())
})

it('does not expose preference controls before identity verification settles', async () => {
  fixture.verified = false
  fixture.pending = true
  const view = setup()
  expect(screen.queryByText('24')).not.toBeInTheDocument()
  fixture.verified = true
  fixture.pending = false
  view.again()
  await screen.findByText('12')
})

it('keeps the console usable after verification fails, without claiming persistence', () => {
  fixture.verified = false
  fixture.pending = false
  setup()
  expect(screen.getByText('24')).toBeVisible()
  expect(fixture.load).not.toHaveBeenCalled()
  expect(fixture.save).not.toHaveBeenCalled()
})

it('does not block the console while the preference module is off', () => {
  fixture.verified = false
  fixture.pending = true
  useModulesStore.setState({ off: new Set(['consoleviews']) })
  setup()
  expect(screen.getByText('24')).toBeVisible()
  expect(fixture.load).not.toHaveBeenCalled()
})
