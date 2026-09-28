// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { resolveDark, useThemeStore, type Theme } from '@/stores/theme'
import { usePreferencesStore } from '@/stores/preferences'
import { useClientSettings } from '@/features/settings/preferences'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: {
      actor: 'admin@example.com',
      display_name: 'Admin Operator',
    },
    isSuperadmin: true,
    activeRole: 'owner',
    grants: [{ tenant: 'tenant-a' }, { tenant: 'tenant-b' }],
  }),
}))

vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    data: {
      version: 'v-test-settings',
      engine: 'sqlite',
      setup_required: false,
      license: { status: 'community', licensee: 'Olivares Test' },
    },
  }),
}))

import { SettingsPage } from './settings'

beforeEach(async () => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
  useThemeStore.setState({ theme: 'dark', resolved: 'dark' })
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

    async function chooseTheme(code: Theme, label: string) {
      await user.click(screen.getByRole('radio', { name: label }))
      expect(useThemeStore.getState().theme).toBe(code)
      expect(localStorage.getItem('olivares.theme')).toBe(code)
      expect(document.documentElement.classList.contains('dark')).toBe(
        resolveDark(code),
      )
    }
    await chooseTheme('light', 'Light')
    await chooseTheme('dark', 'Dark')
    await chooseTheme('system', 'System')

    await user.click(screen.getByRole('radio', { name: 'Compact' }))
    expect(usePreferencesStore.getState().density).toBe('compact')
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
    await user.click(within(listbox).getByRole('option', { name: 'Deutsch' }))
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
    expect(useThemeStore.getState().theme).toBe('dark')
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

  it('shows this installation on About', async () => {
    const user = userEvent.setup()
    render(<SettingsPage />)
    await user.click(screen.getByRole('button', { name: 'About' }))
    expect(screen.getByText('v-test-settings')).toBeInTheDocument()
    expect(screen.getByText('Admin Operator')).toBeInTheDocument()
  })
})
