// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { LANGUAGE_CODES } from '@/lib/i18n'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { RAIL_KEYS } from '@/features/navigation/rail-keys'
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

// The Updates panel has its own tests (features/settings/updates-settings.test.tsx); here it
// only has to be where a system administrator finds it.
vi.mock('@/features/settings/updates-settings', () => ({
  UpdatesSettings: () => <section aria-label="Updates panel" />,
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
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => router.navigate,
  useRouterState: () => location.search,
}))

// AI tools, Workspaces and Setup are rooms with their own tests; here they only have to
// open INSIDE Settings (#2905).
vi.mock('@/features/agent-tools/agent-tools-view', () => ({
  AgentToolsView: () => <section aria-label="AI tools room" />,
}))
vi.mock('@/features/workspace-dashboard/workspace-dashboard-view', () => ({
  WorkspaceDashboardView: () => <section aria-label="Workspaces room" />,
}))
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
    startPage: 'home',
    confirmStop: true,
  })
  await i18n.changeLanguage('en')
  localStorage.removeItem('olivares.lang')
})

describe('Settings', () => {
  it.each(['deep link', 'navigation'])(
    'opens the shared license status and actions through %s',
    async (entry) => {
      const user = userEvent.setup()
      location.search = entry === 'deep link' ? '?section=edition' : ''
      render(<SettingsPage />)
      if (entry === 'navigation') {
        await user.click(
          screen.getByRole('button', { name: 'Edition & modules' }),
        )
      }
      expect(
        await screen.findByText('Settings License Owner'),
      ).toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: 'Install / update' }),
      ).toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: 'Remove license' }),
      ).toBeInTheDocument()
      expect(
        screen.getByRole('region', { name: 'Modules list' }),
      ).toBeInTheDocument()
    },
  )

  it('keeps the license superadmin gate in Settings', async () => {
    auth.isSuperadmin = false
    location.search = '?section=edition'
    render(<SettingsPage />)
    expect(
      screen.getByText('Only a superadmin can view or change the license.'),
    ).toBeInTheDocument()
    expect(consoleApi.getLicense).not.toHaveBeenCalled()
    expect(screen.getByText('Community')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Install / update' }),
    ).toBeNull()
  })

  it.each(['License', 'Edition'])(
    'finds the license section in Settings search for %s',
    async (term) => {
      location.search = `?q=${term}`
      render(<SettingsPage />)
      expect(
        screen.getByRole('link', { name: 'Edition & license' }),
      ).toHaveAttribute('href', '/settings?section=edition')
    },
  )

  const spanishEditionLabel = 'Edición y licencia' // language-data: Spanish expected label
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

  it('offers no section that does nothing, opens the wizard from Setup, and names every key (HU-14)', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    expect(
      screen.queryByRole('button', { name: 'Notifications' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Session defaults' }),
    ).not.toBeInTheDocument()
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
      for (const { command } of [...KEYBINDINGS, ...RAIL_KEYS]) {
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
    expect(localStorage.getItem('olivares.lang')).toBe('es')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Deutsch',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('de'))
    expect(document.documentElement.lang).toBe('de')
    expect(localStorage.getItem('olivares.lang')).toBe('de')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: 'Italiano',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('it'))
    expect(document.documentElement.lang).toBe('it')
    expect(localStorage.getItem('olivares.lang')).toBe('it')

    await user.click(trigger)
    await user.click(
      within(await screen.findByRole('listbox')).getByRole('option', {
        name: '한국어',
      }),
    )
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('ko'))
    expect(document.documentElement.lang).toBe('ko')
    expect(localStorage.getItem('olivares.lang')).toBe('ko')
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

  // dev#2560: the search input drops its own outline, so its field draws the 3 px focus ring.
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
    await user.click(
      screen.getByRole('button', { name: 'Restore all defaults' }),
    )
    expect(useThemeStore.getState().theme).toBe('system')
    expect(usePreferencesStore.getState().density).toBe('comfortable')
    expect(usePreferencesStore.getState().reduceMotion).toBe(false)
    expect(useClientSettings.getState().clock).toBe('24')
    expect(useClientSettings.getState().startPage).toBe('home')
    expect(useClientSettings.getState().confirmStop).toBe(true)
    await waitFor(() => expect(i18n.resolvedLanguage).toBe('en'))
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
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
    const user = userEvent.setup()
    server.edition = 'community'
    const { unmount } = render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'Edition & modules' }))
    expect(
      screen.getByRole('heading', { name: 'Edition & modules' }),
    ).toBeInTheDocument()
    expect(await screen.findByText('Community')).toBeInTheDocument()
    const adds = screen.getByRole('heading', { name: 'Business adds' })
    expect(adds.parentElement?.querySelectorAll('li')).toHaveLength(6)
    expect(screen.getByText('Tool pins')).toBeInTheDocument()
    // Every optional module is turned on and off here, by a system administrator.
    expect(
      screen.getByRole('region', { name: 'Modules list' }),
    ).toBeInTheDocument()
    unmount()

    // A Business build (the engine's "enterprise" build string) is named Business and
    // lists nothing it lacks.
    server.edition = 'enterprise'
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'Edition & modules' }))
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

  it('opens AI tools and Workspaces as sections of Settings, never as a link out of it (#2905)', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    const list = screen.getByRole('navigation', { name: 'Settings sections' })
    // No row of the list leaves /settings.
    expect(within(list).queryAllByRole('link')).toHaveLength(0)

    await user.click(within(list).getByRole('button', { name: 'AI tools' }))
    expect(
      await screen.findByRole('region', { name: 'AI tools room' }),
    ).toBeInTheDocument()
    expect(
      within(list).getByRole('button', { name: 'AI tools' }),
    ).toHaveAttribute('aria-current', 'page')
    // The section is a place: Back returns to the one before it, inside Settings.
    const toAiTools = router.navigate.mock.calls.at(-1)![0]
    expect(toAiTools.replace).toBe(false)
    expect(toAiTools.search({})).toEqual({ section: 'aiTools' })

    await user.click(within(list).getByRole('button', { name: 'Workspaces' }))
    expect(
      await screen.findByRole('region', { name: 'Workspaces room' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'AI tools room' })).toBeNull()
    expect(
      within(list).getByRole('button', { name: 'Workspaces' }),
    ).toHaveAttribute('aria-current', 'page')
    expect(
      within(list).getByRole('button', { name: 'General' }),
    ).not.toHaveAttribute('aria-current')
    expect(router.navigate.mock.calls.at(-1)![0].search({})).toEqual({
      section: 'workspaces',
    })
  })

  it('leaves the AI tools wizard address behind when the section changes (#2905)', async () => {
    // `?add=<kind>&returnTo=` opens the add-a-key form each time the AI tools room mounts:
    // kept in the address across a section change, it reopens on every return to AI tools.
    location.search = '?section=aiTools&add=anthropic&returnTo=%2Fonboarding'
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'Workspaces' }))
    expect(
      router.navigate.mock.calls.at(-1)![0].search({
        section: 'aiTools',
        add: 'anthropic',
        returnTo: '/onboarding',
      }),
    ).toEqual({ section: 'workspaces' })
  })

  it('leaves the AI tools wizard address behind when a search starts, which unmounts the room (#2905)', async () => {
    location.search = '?section=aiTools&add=anthropic&returnTo=%2Fonboarding'
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.type(screen.getByRole('searchbox'), 't')
    expect(
      router.navigate.mock.calls.at(-1)![0].search({
        section: 'aiTools',
        add: 'anthropic',
        returnTo: '/onboarding',
      }),
    ).toEqual({ section: 'aiTools', q: 't' })
  })

  it.each([
    ['aiTools', 'AI tools room', 'AI tools'],
    ['workspaces', 'Workspaces room', 'Workspaces'],
    ['setup', 'Setup wizard', 'Setup'],
  ])(
    'opens ?section=%s on load, with its row marked current (#2905)',
    async (section, room, row) => {
      location.search = `?section=${section}`
      render(<SettingsPage />)
      expect(await screen.findByRole('region', { name: room })).toBeVisible()
      expect(screen.getByRole('button', { name: row })).toHaveAttribute(
        'aria-current',
        'page',
      )
    },
  )

  it('offers a section only where its page opens, and an address to one that is not falls back to General (#474, #2905)', async () => {
    const row = (name: string) => screen.queryByRole('button', { name })
    const { unmount } = render(<SettingsPage />)
    // Deployment is a Business page: Community has no page to host.
    expect(row('Deploy and updates') !== null).toBe(businessDeploy)
    expect(row('AI tools')).not.toBeNull()
    expect(row('Workspaces')).not.toBeNull()
    expect(row('Setup')).not.toBeNull()
    expect(screen.getByText('Work')).toBeInTheDocument()
    unmount()

    if (businessDeploy) {
      // A fresh install leaves deploy off (modulespec standard=false).
      useModulesStore.getState().setOff(['deploy'])
      const off = render(<SettingsPage />)
      expect(row('Deploy and updates')).toBeNull()
      expect(row('AI tools')).not.toBeNull()
      off.unmount()

      // Module on, permission not held.
      useModulesStore.getState().setOff([])
      auth.denied.add('deploy:deployment:read')
      const denied = render(<SettingsPage />)
      expect(row('Deploy and updates')).toBeNull()
      denied.unmount()
    }

    // One section left keeps its group; none left drops the heading too.
    auth.denied.add('tenant:read')
    const one = render(<SettingsPage />)
    expect(row('Workspaces')).toBeNull()
    expect(row('AI tools')).not.toBeNull()
    expect(screen.getByText('Work')).toBeInTheDocument()
    one.unmount()

    // An address to a section the person may not open shows General, not an empty pane.
    location.search = '?section=workspaces'
    const address = render(<SettingsPage />)
    expect(screen.queryByRole('region', { name: 'Workspaces room' })).toBeNull()
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeVisible()
    address.unmount()

    location.search = undefined
    auth.denied.add('sessions:provider:read')
    auth.denied.add('system:admin')
    render(<SettingsPage />)
    expect(row('AI tools')).toBeNull()
    expect(row('Workspaces')).toBeNull()
    expect(row('Setup')).toBeNull()
    expect(screen.queryByText('Work')).toBeNull()
  })

  it('follows the address between hosted sections, as Back and Forward move it (#2905)', async () => {
    location.search = '?section=aiTools'
    const view = render(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'AI tools room' }),
    ).toBeVisible()
    location.search = '?section=workspaces'
    view.rerender(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'Workspaces room' }),
    ).toBeVisible()
    expect(screen.queryByRole('region', { name: 'AI tools room' })).toBeNull()
    location.search = '?section=aiTools'
    view.rerender(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'AI tools room' }),
    ).toBeVisible()
    // The address before any section is General.
    location.search = ''
    view.rerender(<SettingsPage />)
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeVisible()
    expect(screen.queryByRole('region', { name: 'AI tools room' })).toBeNull()
  })

  it('shows search results, not the hosted section, while the search holds a query (#2905)', async () => {
    location.search = '?section=aiTools&q=theme'
    const view = render(<SettingsPage />)
    expect(
      await screen.findByRole('radiogroup', { name: 'Theme' }),
    ).toBeVisible()
    expect(screen.queryByRole('region', { name: 'AI tools room' })).toBeNull()
    // The room is there once the query is cleared: the absence above is not a missing section.
    location.search = '?section=aiTools'
    view.rerender(<SettingsPage />)
    expect(
      await screen.findByRole('region', { name: 'AI tools room' }),
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
      'Now',
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

describe('Settings > Updates', () => {
  it('offers the Updates section to a system administrator, by menu and by link', async () => {
    const user = userEvent.setup()
    location.search = ''
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'Updates' }))
    expect(
      await screen.findByRole('region', { name: 'Updates panel' }),
    ).toBeInTheDocument()
  })

  it('opens the Updates section from its address', () => {
    location.search = '?section=updates'
    render(<SettingsPage />)
    expect(
      screen.getByRole('region', { name: 'Updates panel' }),
    ).toBeInTheDocument()
  })

  it('does not offer Updates to anyone else', () => {
    auth.isSuperadmin = false
    location.search = '?section=updates'
    render(<SettingsPage />)
    expect(screen.queryByRole('button', { name: 'Updates' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Updates panel' })).toBeNull()
    // An address to a section the person is not offered shows General, not an empty pane.
    expect(screen.getByRole('radiogroup', { name: 'Theme' })).toBeVisible()
  })
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
    'opens General, not an empty page, for the address of %s when the person is not an administrator',
    (id) => {
      auth.isSuperadmin = false
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
      expect(
        screen.queryByRole('region', { name: 'Session idle panel' }),
      ).toBeNull()
      expect(screen.queryByRole('region', { name: 'Updates panel' })).toBeNull()
    },
  )

  it.each(['notifications', 'sessionDefaults', 'nothing-here'])(
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
