// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { SETTINGS_SECTIONS } from '@/features/settings/sections'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import {
  readStoredThemeForTest,
  resolveDark,
  useThemeStore,
  type Theme,
} from '@/stores/theme'
import { usePreferencesStore } from '@/stores/preferences'
import { useClientSettings } from '@/features/settings/preferences'
import { useModulesStore } from '@/stores/modules'
import { FEATURE_VIEWS } from '@/features/registry'
import { renderIntel as render } from '@/test/intel'
import { consoleApi } from '@/features/console/api'
import { ApiError } from '@/lib/api/errors'
import '@/features/console/i18n'

// The module list has its own tests (features/settings/modules-settings.test.tsx); here it
// only has to be where a system administrator finds it.
vi.mock('@/features/settings/modules-settings', () => ({
  ModulesSettings: () => <section aria-label="Modules list" />,
}))

// The Session idle form has its own tests (features/settings/session-idle-settings.test.tsx);
// here it only has to be where a system administrator finds it.
vi.mock('@/features/settings/session-idle-settings', () => ({
  SessionIdleSettings: () => <section aria-label="Session idle panel" />,
}))

const auth = vi.hoisted(() => ({
  denied: new Set<string>(),
  isSuperadmin: true,
}))
const location = vi.hoisted(() => ({ search: undefined as string | undefined }))
const router = vi.hoisted(() => ({ navigate: vi.fn() }))
vi.mock('@tanstack/react-router', async () => ({
  useNavigate: () => router.navigate,
  useRouter: () => undefined,
  useRouterState: () => location.search,
  Link: (await import('@/test/fake-router')).fakeRouterModule().Link,
}))

// Setup is a room with its own tests; here it only has to open INSIDE Settings (#2905).
vi.mock('@/features/onboarding/index', () => ({
  OnboardingView: () => <section aria-label="Setup wizard" />,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) => !auth.denied.has(permission),
    principal: {
      actor: 'user:01a10672-test',
      email: 'admin@example.com',
      display_name: 'Admin Operator',
    },
    isSuperadmin: auth.isSuperadmin,
    activeRole: 'owner',
    grants: [{ tenant: 'tenant-a' }, { tenant: 'tenant-b' }],
  }),
}))

const server = vi.hoisted(() => ({
  edition: 'community' as string | undefined,
  license: { status: 'none', licensee: '' },
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    data: {
      version: 'v-test-settings',
      engine: 'sqlite',
      setup_required: false,
      edition: server.edition,
      license: server.license,
    },
  }),
}))

import { SettingsPage } from './settings'

// Deployment is a Business page; only a Business build registers it.
const businessDeploy = FEATURE_VIEWS.some((view) => view.id === 'deploy')

beforeEach(async () => {
  badgeFixture.edition = null
  vi.restoreAllMocks()
  router.navigate.mockClear()
  location.search = undefined
  server.license = { status: 'none', licensee: '' }
  auth.denied.clear()
  auth.isSuperadmin = true
  vi.spyOn(consoleApi, 'getLicense').mockImplementation(async () => ({
    edition: server.edition ?? 'community',
    hot_apply: true,
    status: 'valid',
    source: 'data-dir',
    managed_externally: false,
    max_users: 0,
    seat_limit: 0,
    seat_limited: false,
    active_users: 1,
    licensee: 'Settings License Owner',
  }))
  vi.spyOn(consoleApi, 'getActivation').mockRejectedValue(
    new ApiError(
      501,
      'activation_unavailable',
      'Activation is not wired on this deployment.',
    ),
  )
  useModulesStore.setState({ off: new Set() })
  localStorage.clear()
  document.documentElement.classList.remove('dark', 'black')
  // A fresh profile: whatever the store reads with nothing stored (#478).
  const fresh = readStoredThemeForTest()
  useThemeStore.setState({
    theme: fresh,
    resolved: resolveDark(fresh) ? 'dark' : 'light',
  })
  usePreferencesStore.setState({ density: 'comfortable', reduceMotion: false })
  useClientSettings.setState({
    clock: '24',
    startPage: 'sessions',
    confirmStop: true,
    editor: 'vscode',
    editorSsh: '',
  })
  await i18n.changeLanguage('en')
  localStorage.removeItem('olivares.lang')
})

describe('Settings', () => {
  it('opens the shared license status and actions as Settings › Edition, with the Settings list beside it', async () => {
    location.search = '?section=edition'
    render(<SettingsPage />)
    expect(
      await screen.findByText('Settings License Owner'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Install license' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Remove license' }),
    ).toBeInTheDocument()
    // Edition is a section of Settings (0.7): its row is the current one.
    const list = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(
      within(list).getByRole('button', { name: 'Edition' }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it('shows the module switches in Settings › Modules and none in Settings › Edition', async () => {
    location.search = '?section=modules'
    const { unmount } = render(<SettingsPage />)
    expect(
      screen.getByRole('heading', { level: 1, name: 'Modules' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('region', { name: 'Modules list' }),
    ).toBeInTheDocument()
    const list = screen.getByRole('navigation', { name: 'Settings sections' })
    expect(
      within(list).getByRole('button', { name: 'Modules' }),
    ).toHaveAttribute('aria-current', 'page')
    unmount()

    location.search = '?section=edition'
    render(<SettingsPage />)
    expect(
      await screen.findByRole('heading', { level: 1, name: 'Edition' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Modules list' })).toBeNull()
  })

  it('names Edition and Modules with one noun each, and no empty license source (C21)', async () => {
    vi.spyOn(consoleApi, 'getLicense').mockResolvedValue({
      edition: 'community',
      hot_apply: true,
      status: 'none',
      source: 'none',
      managed_externally: false,
      max_users: 0,
      seat_limit: 0,
      seat_limited: false,
      active_users: 7,
    })
    location.search = '?section=edition'
    render(<SettingsPage />)
    expect(
      await screen.findByRole('heading', { level: 2, name: 'License' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Install license' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/ & |Install \/ update/)).toBeNull()
    expect(screen.queryByText('Source')).toBeNull()
    // A module is named as the page it serves: capabilities is Extensions.
    expect(i18n.t('modules.names.capabilities', { ns: 'settings' })).toBe(
      i18n.t('items.capabilities', { ns: 'nav' }),
    )
  })

  it('lists no room another place of the sidebar holds (0.5 fold)', () => {
    render(<SettingsPage />)
    const list = screen.getByRole('navigation', { name: 'Settings sections' })
    for (const name of [
      'AI tools',
      'Workspaces',
      'Deploy and updates',
      'Updates',
    ])
      expect(within(list).queryByRole('button', { name })).toBeNull()
    // No row of the list leaves /settings.
    expect(within(list).queryAllByRole('link')).toHaveLength(0)
  })

  it('keeps the license superadmin gate in Settings', async () => {
    auth.isSuperadmin = false
    location.search = '?section=edition'
    render(<SettingsPage />)
    expect(
      screen.getByText(/ask a system administrator to view or change it/i),
    ).toBeInTheDocument()
    expect(consoleApi.getLicense).not.toHaveBeenCalled()
    expect(screen.getByText('Community')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install license' })).toBeNull()
  })

  it.each(['License', 'Edition'])(
    'finds the license section in Settings search for %s',
    async (term) => {
      location.search = `?q=${term}`
      render(<SettingsPage />)
      expect(screen.getByRole('link', { name: 'Edition' })).toHaveAttribute(
        'href',
        '/settings?section=edition',
      )
    },
  )

  const spanishEditionLabel = 'Edición' // language-data: Spanish expected label
  it('finds the license section in Settings search without diacritics in Spanish', async () => {
    await i18n.changeLanguage('es')
    location.search = '?q=edicion'
    render(<SettingsPage />)
    expect(
      screen.getByRole('link', { name: spanishEditionLabel }),
    ).toHaveAttribute('href', '/settings?section=edition')
  })

  it('opens a setting selected by navigation while the settings page is already mounted', async () => {
    window.history.replaceState(null, '', '/settings')
    location.search = ''
    const view = render(<SettingsPage />)
    expect(
      screen.getByRole('radiogroup', { name: 'Theme' }),
    ).toBeInTheDocument()
    location.search = '?section=keyboard'
    view.rerender(<SettingsPage />)
    expect(screen.getByText('New session')).toBeInTheDocument()
    expect(
      screen.queryByRole('radiogroup', { name: 'Theme' }),
    ).not.toBeInTheDocument()
    location.search = ''
    view.rerender(<SettingsPage />)
    expect(
      screen.getByRole('radiogroup', { name: 'Theme' }),
    ).toBeInTheDocument()
  })

  it('opens the notification choices, Setup wizard and named keyboard shortcuts', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    expect(screen.getByRole('button', { name: 'Notifications' })).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Session defaults' }),
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New sessions' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Notifications' }))
    expect(screen.getByRole('heading', { name: 'Notifications' })).toBeVisible()
    for (const name of ['Needs you', 'Finished', 'Failed'])
      expect(screen.getByRole('switch', { name })).toBeVisible()
    // Setup is a section of Settings, never a link out of it (#2905).
    expect(screen.queryByRole('link', { name: 'Setup' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Setup' }))
    expect(
      await screen.findByRole('region', { name: 'Setup wizard' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Keyboard' }))
    expect(screen.getByText('New session')).toBeInTheDocument()
    expect(screen.queryByText(/keys\.command\./)).not.toBeInTheDocument()
  })

  it.each(LANGUAGE_CODES)(
    'names every shortcut in %s without fallback',
    (lng) => {
      for (const { command } of KEYBINDINGS) {
        const key = `keys.command.${command}`
        const label = i18n.t(key, { lng, ns: 'common', fallbackLng: false })
        expect(label, `${lng}: ${key}`).not.toBe(key)
        expect(label.trim(), `${lng}: ${key}`).not.toBe('')
      }
    },
  )

  it.each(LANGUAGE_CODES)(
    'renders Keyboard without raw keys in %s',
    async (lng) => {
      await i18n.changeLanguage(lng)
      const user = userEvent.setup()
      const { container } = render(<SettingsPage />)
      await user.click(
        screen.getByRole('button', {
          name: i18n.t('nav.keyboard', { ns: 'settings' }),
        }),
      )
      expectNoRawI18nKeys(container)
      const open = screen.getAllByText(i18n.t('keys.command.rail.open'))
      expect(open).toHaveLength(1)
      const row = open[0]!.closest('li')!
      expect(within(row).getByText('Enter')).toBeInTheDocument()
      expect(within(row).getByText('Space')).toBeInTheDocument()
    },
  )

  it('shows Changed · reset only on a value that differs, and reset restores it', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'Compact' }))
    const markers = screen.getAllByRole('button', { name: /Changed · reset/ })
    expect(markers).toHaveLength(1)
    await user.click(markers[0]!)
    expect(usePreferencesStore.getState().density).toBe('comfortable')
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
  })

  it('writes theme, density and language', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)

    async function chooseTheme(code: Theme, label: string, witness?: string) {
      await user.click(screen.getByRole('radio', { name: label }))
      expect(useThemeStore.getState().theme, witness).toBe(code)
      expect(localStorage.getItem('olivares.theme')).toBe(code)
      expect(document.documentElement.classList.contains('dark')).toBe(
        resolveDark(code),
      )
      expect(document.documentElement.classList.contains('black')).toBe(
        code === 'black',
      )
    }
    await chooseTheme(
      'light',
      'Light',
      'settings theme/light Fired: setTheme did not receive light',
    )
    await chooseTheme('dark', 'Dark')
    await chooseTheme('black', 'Black')
    await chooseTheme('system', 'System')

    await user.click(screen.getByRole('radio', { name: 'Compact' }))
    expect(
      usePreferencesStore.getState().density,
      'settings density/compact Fired: setDensity did not receive compact',
    ).toBe('compact')
    await user.click(screen.getByRole('radio', { name: 'Comfortable' }))
    expect(usePreferencesStore.getState().density).toBe('comfortable')

    const trigger = screen.getByRole('combobox', { name: 'Language' })
    await user.click(trigger)
    const listbox = await screen.findByRole('listbox')
    expect(
      within(listbox)
        .getAllByRole('option')
        .map((option) => option.textContent?.trim()),
    ).toEqual([
      'English',
      'Español',
      '中文',
      '日本語',
      'Deutsch',
      'Русский',
      'Français',
      'Italiano',
      '한국어',
      'Українська',
      'Português',
    ])
    // prettier-ignore
    const spanishName = 'Español' // language-data: native language name
    await user.click(within(listbox).getByRole('option', { name: spanishName }))
    await waitFor(() =>
      expect(
        i18n.resolvedLanguage,
        'settings language/es Fired: setLanguage did not resolve es',
      ).toBe('es'),
    )
    expect(document.documentElement.lang).toBe('es')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('es')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Deutsch',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('de'))
    expect(document.documentElement.lang).toBe('de')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('de')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Italiano',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('it'))
    expect(document.documentElement.lang).toBe('it')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('it')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: '한국어',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('ko'))
    expect(document.documentElement.lang).toBe('ko')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('ko')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Українська',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('uk'))
    expect(document.documentElement.lang).toBe('uk')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('uk')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Português',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('pt'))
    expect(document.documentElement.lang).toBe('pt')
    expect(preferenceStorage.getItem('olivares.lang')).toBe('pt')
  })

  it('focuses search on / and filters to Reduce motion', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.keyboard('/')
    const search = screen.getByRole('searchbox')
    expect(search).toHaveFocus()
    await user.type(search, 'motion')
    expect(screen.getByText('Reduce motion')).toBeInTheDocument()
    expect(screen.queryByText('Density')).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Records and exports stay in UTC/),
    ).not.toBeInTheDocument()
  })

  // the regression: the search input drops its own outline, so its field draws the 3 px focus ring.
  it('rings the search field in the focus color while the search has keyboard focus', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.keyboard('/')
    const field = screen.getByRole('searchbox').closest('label')
    expect(field).toHaveClass(
      'has-focus-visible:outline-3',
      'has-focus-visible:outline-offset-2',
      'has-focus-visible:outline-focus',
    )
  })

  it('moves over the four themes with the arrows, Home and End, focus following', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('radio', { name: 'Dark' }))
    await user.keyboard('{ArrowRight}')
    expect(useThemeStore.getState().theme).toBe('black')
    expect(screen.getByRole('radio', { name: 'Black' })).toHaveFocus()
    expect(screen.getByRole('radio', { name: 'Black' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    // Past the last it wraps to the first.
    await user.keyboard('{ArrowRight}')
    expect(useThemeStore.getState().theme).toBe('system')
    await user.keyboard('{End}')
    expect(useThemeStore.getState().theme).toBe('black')
    await user.keyboard('{ArrowLeft}')
    expect(useThemeStore.getState().theme).toBe('dark')
    await user.keyboard('{Home}')
    expect(useThemeStore.getState().theme).toBe('system')
    expect(screen.getByRole('radio', { name: 'System' })).toHaveFocus()
  })

  it('treats System as the theme default: no marker, and reset returns to it (#478)', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    expect(screen.getByRole('radio', { name: 'System' })).toBeChecked()
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByText(
        'Follows the system unless you choose light, dark or black.',
      ),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'Dark' }))
    await user.click(screen.getByRole('button', { name: /Changed · reset/ }))
    expect(useThemeStore.getState().theme).toBe('system')
    expect(localStorage.getItem('olivares.theme')).toBe('system')
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
  })

  it('restores every default from the top of the page', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('radio', { name: 'Light' }))
    await user.click(screen.getByRole('radio', { name: 'Compact' }))
    await user.click(screen.getByRole('radio', { name: '12-hour' }))
    await user.click(screen.getByRole('switch', { name: 'Reduce motion' }))
    useClientSettings.setState({ editor: 'zed', editorSsh: 'me@devbox' })
    await user.click(
      screen.getByRole('button', { name: 'Restore all defaults' }),
    )
    expect(useThemeStore.getState().theme).toBe('system')
    expect(usePreferencesStore.getState().density).toBe('comfortable')
    expect(usePreferencesStore.getState().reduceMotion).toBe(false)
    expect(useClientSettings.getState().clock).toBe('24')
    expect(useClientSettings.getState().startPage).toBe('sessions')
    expect(useClientSettings.getState().confirmStop).toBe(true)
    expect(useClientSettings.getState().editor).toBe('vscode')
    expect(useClientSettings.getState().editorSsh).toBe('')
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('en'))
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
  })

  it('chooses the editor that file links open, and resets to VS Code', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    const choice = screen.getByRole('combobox', {
      name: 'Editor for file links',
    })
    expect(choice).toHaveTextContent('VS Code')
    await user.click(choice)
    await user.click(await screen.findByRole('option', { name: 'GoLand' }))
    expect(useClientSettings.getState().editor).toBe('goland')
    await user.click(screen.getByRole('button', { name: /Changed · reset/ }))
    expect(useClientSettings.getState().editor).toBe('vscode')
  })

  it('keeps the SSH host the editor connects with, and resets it to empty', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.type(
      screen.getByRole('textbox', { name: 'SSH host for the editor' }),
      'me@devbox',
    )
    expect(useClientSettings.getState().editorSsh).toBe('me@devbox')
    await user.click(screen.getByRole('button', { name: /Changed · reset/ }))
    expect(useClientSettings.getState().editorSsh).toBe('')
  })

  it('keeps Restore all in the General header, never portaled into a top bar', () => {
    // The top bar has no page slot since #2544; a stray one must not pull the button out.
    const bar = document.createElement('header')
    bar.dataset.slot = 'topbar'
    bar.innerHTML = '<div data-slot="page-actions"></div>'
    document.body.append(bar)
    try {
      render(<SettingsPage />)
      const button = screen.getByRole('button', {
        name: 'Restore all defaults',
      })
      expect(bar.contains(button)).toBe(false)
    } finally {
      bar.remove()
    }
  })

  it('names the edition once, and on Community lists what a Business build adds', async () => {
    // Settings › Edition opens this address.
    location.search = '?section=edition'
    server.edition = 'community'
    const { unmount } = render(<SettingsPage />)
    expect(
      screen.getByRole('heading', { level: 1, name: 'Edition' }),
    ).toBeInTheDocument()
    expect(await screen.findByText('Community')).toBeInTheDocument()
    // The list quotes the summary in docs/editions.md, not a console catalogue.
    const adds = screen.getByRole('heading', { name: 'Business adds' })
    const items = adds.parentElement?.querySelectorAll('li') ?? []
    expect([...items].map((li) => li.textContent)).toEqual(
      __EDITION_SUMMARIES__.business,
    )
    expect(
      screen.getByText(__EDITION_SUMMARIES__.enterprise[0]),
    ).toBeInTheDocument()
    expect(screen.queryByText('Tool pins')).toBeNull()
    unmount()

    // A Business build (the engine's "enterprise" build string) is named Business and
    // lists nothing it lacks.
    server.edition = 'enterprise'
    render(<SettingsPage />)
    expect(await screen.findByText('Business')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Business adds' })).toBeNull()
    server.edition = 'community'
  })

  it('shows this installation on About', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'About' }))
    expect(
      screen.queryByText('v-test-settings'),
      'settings about Effect: owned panel was not mounted',
    ).not.toBeNull()
    expect(screen.getByText('Admin Operator')).toBeInTheDocument()
  })
  it('shows Community without license placeholders and identifies the signed-in person', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'About' }))
    expect(
      screen.getByText('Community edition — no license needed'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Licensed to')).not.toBeInTheDocument()
    expect(screen.queryByText('License status')).not.toBeInTheDocument()
    expect(screen.getByText('Admin Operator')).toBeInTheDocument()
    expect(screen.getByText('admin@example.com')).toBeInTheDocument()
    expect(screen.queryByText('user:01a10672-test')).not.toBeVisible()
    await user.click(screen.getByText('Principal ID'))
    expect(screen.getByText('user:01a10672-test')).toBeVisible()
  })

  it.each(['valid', 'expired'])(
    'preserves the %s license and its owner on About',
    async (status) => {
      server.license = { status, licensee: 'Example Organization' }
      const user = userEvent.setup()
      render(<SettingsPage />)
      await user.click(screen.getByRole('button', { name: 'About' }))
      expect(screen.getByText(status)).toBeInTheDocument()
      expect(screen.getByText('Example Organization')).toBeInTheDocument()
      expect(screen.queryByText(/no license needed/)).not.toBeInTheDocument()
    },
  )

  it('opens ?section=setup on load, with its row marked current (#2905)', async () => {
    location.search = '?section=setup'
    render(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'Setup wizard' }),
    ).toBeVisible()
    expect(screen.getByRole('button', { name: 'Setup' })).toHaveAttribute(
      'aria-current',
      'page',
    )
  })

  it('offers Setup only where its page opens, and an address to it otherwise says it is for a system administrator, not General (#474, #2905)', () => {
    auth.denied.add('system:admin')
    location.search = '?section=setup'
    render(<SettingsPage />)
    expect(screen.queryByRole('button', { name: 'Setup' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Setup wizard' })).toBeNull()
    expect(screen.getByRole('heading', { name: 'Setup' })).toBeInTheDocument()
    expect(
      screen.getByText(/ask a system administrator to view or change it/i),
    ).toBeInTheDocument()
    expect(screen.queryByRole('radiogroup', { name: 'Theme' })).toBeNull()
    // The list does not say General is the page while another page's notice shows.
    expect(screen.getByRole('button', { name: 'General' })).not.toHaveAttribute(
      'aria-current',
    )
    // the regression: Permissions is open to everyone, so the group keeps its heading for it.
    expect(
      screen.getByRole('button', { name: 'Permissions' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Work')).toBeInTheDocument()
  })

  it('shows search results, not the hosted section, while the search holds a query (#2905)', async () => {
    location.search = '?section=setup&q=theme'
    const view = render(<SettingsPage />)
    expect(
      await screen.findByRole('radiogroup', { name: 'Theme' }),
    ).toBeVisible()
    expect(screen.queryByRole('region', { name: 'Setup wizard' })).toBeNull()
    location.search = '?section=setup'
    view.rerender(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'Setup wizard' }),
    ).toBeVisible()
  })

  it('offers as a start page only pages this installation runs (#474)', async () => {
    const user = userEvent.setup()
    const startPages = async () => {
      await user.click(screen.getByRole('combobox', { name: 'Start page' }))
      const options = within(await screen.findByRole('listbox'))
        .getAllByRole('option')
        .map((option) => option.textContent?.trim())
      await user.keyboard('{Escape}')
      return options
    }
    const all = render(<SettingsPage />)
    expect(await startPages()).toEqual([
      'Overview',
      'Sessions',
      'Workspace',
      'Providers',
      ...(businessDeploy ? ['Deployment'] : []),
    ])
    all.unmount()

    useModulesStore.getState().setOff(['deploy'])
    render(<SettingsPage />)
    expect(await startPages()).not.toContain('Deployment')
  })
})

const badgeFixture = vi.hoisted(() => ({ edition: null as string | null }))
vi.mock('@/features/console/use-edition-badge', () => ({
  useEditionBadge: () => ({
    edition: badgeFixture.edition,
    shown: true,
    canSave: true,
    setShown: async () => {},
    saving: false,
    saveError: false,
    loadError: false,
  }),
}))

it('shows the active commercial badge and one visibility setting in Edition', async () => {
  badgeFixture.edition = 'enterprise'
  location.search = '?section=edition'
  render(<SettingsPage />)
  expect(screen.getByLabelText('Enterprise edition')).toBeInTheDocument()
  expect(
    screen.getByRole('switch', { name: 'Show edition badge' }),
  ).toBeChecked()
})

describe('Settings > section list', () => {
  const label = (id: string) => i18n.t(`nav.${id}`, { ns: 'settings' })
  // A hosted page is listed only where that page opens: those rows are tested above.
  const own = SETTINGS_SECTIONS.filter((s) => !('view' in s))

  it.each(own.map((s) => s.id))('lists %s for an administrator', (id) => {
    render(<SettingsPage />)
    expect(screen.getByRole('button', { name: label(id) })).toBeVisible()
  })

  it.each(own.map((s) => [s.id, s.adminOnly] as const))(
    'lists %s for anyone else only when it is not administrator-only',
    (id, adminOnly) => {
      auth.isSuperadmin = false
      render(<SettingsPage />)
      const row = screen.queryByRole('button', { name: label(id) })
      if (adminOnly) expect(row).toBeNull()
      else expect(row).toBeVisible()
    },
  )

  it('opens Session idle by its row, under the section title', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: label('sessionIdle') }))
    expect(
      screen.getByRole('region', { name: 'Session idle panel' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { level: 1, name: label('sessionIdle') }),
    ).toBeInTheDocument()
  })

  it('opens Session idle from its address for an administrator', () => {
    location.search = '?section=sessionIdle'
    render(<SettingsPage />)
    expect(
      screen.getByRole('region', { name: 'Session idle panel' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: label('sessionIdle') }),
    ).toHaveAttribute('aria-current', 'page')
  })

  it.each(SETTINGS_SECTIONS.filter((s) => s.adminOnly).map((s) => s.id))(
    'says the address of %s is for a system administrator, not General, when the person is not one',
    (id) => {
      auth.isSuperadmin = false
      location.search = `?section=${id}`
      render(<SettingsPage />)
      expect(
        screen.getByRole('heading', {
          name: i18n.t(`nav.${id}`, { ns: 'settings' }),
        }),
      ).toBeInTheDocument()
      expect(
        screen.getByText(/ask a system administrator to view or change it/i),
      ).toBeInTheDocument()
      expect(
        screen.queryByRole('heading', {
          name: i18n.t('general.title', { ns: 'settings' }),
        }),
      ).toBeNull()
    },
  )

  it.each(['sessionDefaults', 'nothing-here'])(
    'opens General for the unknown address ?section=%s',
    (id) => {
      location.search = `?section=${id}`
      render(<SettingsPage />)
      expect(
        screen.getByRole('heading', {
          name: i18n.t('general.title', { ns: 'settings' }),
        }),
      ).toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: label('general') }),
      ).toHaveAttribute('aria-current', 'page')
    },
  )
})
