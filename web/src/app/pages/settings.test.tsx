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
import { renderIntel as render } from '@/test/intel'

// The module list has its own tests (features/settings/modules-settings.test.tsx); here it
// only has to be where a system administrator finds it.
vi.mock('@/features/settings/modules-settings', () => ({
  ModulesSettings: () => <section aria-label="Modules list" />,
}))

const auth = vi.hoisted(() => ({ denied: new Set<string>() }))
const location = vi.hoisted(() => ({ search: undefined as string | undefined }))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => location.search,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) => !auth.denied.has(permission),
    principal: {
      actor: 'user:01a10672-test',
      email: 'admin@example.com',
      display_name: 'Admin Operator',
    },
    isSuperadmin: true,
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

beforeEach(async () => {
  location.search = undefined
  server.license = { status: 'none', licensee: '' }
  auth.denied.clear()
  useModulesStore.setState({ off: new Set() })
  localStorage.clear()
  document.documentElement.classList.remove('dark')
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
    expect(screen.getByRole('link', { name: 'Setup' })).toHaveAttribute(
      'href',
      '/onboarding',
    )
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
    }
    await chooseTheme(
      'light',
      'Light',
      'settings theme/light Fired: setTheme did not receive light',
    )
    await chooseTheme('dark', 'Dark')
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

  it('treats System as the theme default: no marker, and reset returns to it (#478)', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    expect(screen.getByRole('radio', { name: 'System' })).toBeChecked()
    expect(
      screen.queryByRole('button', { name: /Changed · reset/ }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByText('Follows the system unless you choose light or dark.'),
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

  it('names the edition once, and on Community lists what a Business build adds', async () => {
    const user = userEvent.setup()
    server.edition = 'community'
    const { unmount } = render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'Edition & modules' }))
    expect(
      screen.getByRole('heading', { name: 'Edition & modules' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Community')).toBeInTheDocument()
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
    expect(screen.getByText('Business')).toBeInTheDocument()
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

  it('links only to pages this installation runs and this principal may open (#474)', () => {
    const href = (name: string) =>
      screen.queryByRole('link', { name })?.getAttribute('href')
    const { unmount } = render(<SettingsPage />)
    expect(href('Deploy and updates')).toBe('/deploy')
    expect(href('AI tools')).toBe('/providers')
    expect(href('Workspaces')).toBe('/workspace')
    expect(screen.getByText('Work')).toBeInTheDocument()
    unmount()

    // A fresh install leaves deploy off (modulespec standard=false).
    useModulesStore.getState().setOff(['deploy'])
    const off = render(<SettingsPage />)
    expect(href('Deploy and updates')).toBeUndefined()
    expect(href('AI tools')).toBe('/providers')
    off.unmount()

    // Module on, permission not held.
    useModulesStore.getState().setOff([])
    auth.denied.add('deploy:deployment:read')
    const denied = render(<SettingsPage />)
    expect(href('Deploy and updates')).toBeUndefined()
    denied.unmount()

    // One link left keeps its group; none left drops the heading too.
    auth.denied.add('tenant:read')
    const one = render(<SettingsPage />)
    expect(href('Workspaces')).toBeUndefined()
    expect(href('AI tools')).toBe('/providers')
    expect(screen.getByText('Work')).toBeInTheDocument()
    one.unmount()
    auth.denied.add('sessions:provider:read')
    render(<SettingsPage />)
    expect(href('AI tools')).toBeUndefined()
    expect(href('Workspaces')).toBeUndefined()
    expect(screen.queryByText('Work')).toBeNull()
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
      'API keys',
      'Deployment',
    ])
    all.unmount()

    useModulesStore.getState().setOff(['deploy'])
    render(<SettingsPage />)
    expect(await startPages()).not.toContain('Deployment')
  })
})
