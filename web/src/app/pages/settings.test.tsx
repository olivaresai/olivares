// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// `/settings` has eighteen operator controls: three tabs, three select triggers,
// and twelve options. This test accounts for every one as Rendered → Fired →
// Effect. The messages are deliberately specific: the mutation replay changes the
// corresponding value/handler and must name the exact control that stopped biting.
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { resolveDark, useThemeStore, type Theme } from '@/stores/theme'
import { usePreferencesStore, type Density } from '@/stores/preferences'

const auth = vi.hoisted(() => ({
  principal: {
    actor: 'admin@example.com',
    display_name: 'Admin Operator',
  },
  isSuperadmin: true,
  activeRole: 'owner',
  grants: [{ tenant: 'tenant-a' }, { tenant: 'tenant-b' }],
}))

const serverInfo = vi.hoisted(() => ({
  version: 'v-test-settings',
  engine: 'sqlite',
  setup_required: false,
  license: { status: 'community', licensee: 'Olivares Test' },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: serverInfo }),
}))

import { SettingsPage } from './settings'

const CONTROL_IDS = [
  'tab/profile',
  'tab/appearance',
  'tab/about',
  'trigger/theme',
  'trigger/density',
  'trigger/language',
  'theme/light',
  'theme/dark',
  'theme/system',
  'density/comfortable',
  'density/compact',
  'language/en',
  'language/es',
  'language/zh',
  'language/ja',
  'language/de',
  'language/ru',
  'language/fr',
] as const

beforeEach(async () => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
  document.documentElement.style.colorScheme = ''
  useThemeStore.setState({ theme: 'system', resolved: 'light' })
  usePreferencesStore.setState({ density: 'comfortable' })
  localStorage.removeItem('olivares.prefs')
  await i18n.changeLanguage('en')
  localStorage.removeItem('olivares.lang')
})

describe('SettingsPage — 18 controls, Rendered → Fired → Effect', () => {
  it('exercises every tab, trigger, and option with a discriminating effect', async () => {
    const user = userEvent.setup()
    const exercised = new Set<string>()
    render(<SettingsPage />)

    async function activateTab(
      name: string,
      id: string,
      assertEffect: (panel: HTMLElement) => void,
    ) {
      const tab = screen.getByRole('tab', { name })
      expect(tab, `settings ${id} Rendered: tab is missing`).toBeEnabled()
      expect(
        tab,
        `settings ${id} precondition: tab was already active, so the click would prove nothing`,
      ).toHaveAttribute('aria-selected', 'false')
      await user.click(tab)
      expect(
        tab,
        `settings ${id} Fired: click did not select the tab`,
      ).toHaveAttribute('aria-selected', 'true')
      const panelId = tab.getAttribute('aria-controls')
      expect(
        panelId,
        `settings ${id} Effect: trigger owns no panel`,
      ).toBeTruthy()
      const panel = document.getElementById(panelId as string)
      expect(
        panel,
        `settings ${id} Effect: owned panel was not mounted`,
      ).not.toBeNull()
      if (!panel)
        throw new Error(`settings ${id} Effect: owned panel was not mounted`)
      expect(panel).toBeVisible()
      assertEffect(panel)
      exercised.add(id)
    }

    // Appearance is inactive initially. Activating it first makes the subsequent
    // Profile click causal instead of merely clicking the default active tab.
    await activateTab('Appearance', 'tab/appearance', (panel) => {
      expect(
        within(panel).getAllByRole('combobox'),
        'settings tab/appearance Effect: expected its three preference controls',
      ).toHaveLength(3)
    })
    await activateTab('Profile', 'tab/profile', (panel) => {
      expect(
        within(panel).getByText('admin@example.com'),
        'settings tab/profile Effect: engine principal was not painted',
      ).toBeVisible()
    })
    await activateTab('About', 'tab/about', (panel) => {
      expect(
        within(panel).getByText('v-test-settings'),
        'settings tab/about Effect: server-info version was not painted',
      ).toBeVisible()
      expect(within(panel).getByText('sqlite')).toBeVisible()
      expect(within(panel).getByText('community')).toBeVisible()
    })

    // Return to Appearance; its control has already been accounted for above.
    await user.click(screen.getByRole('tab', { name: 'Appearance' }))

    async function exerciseTrigger(
      name: string,
      id: string,
      optionNames: readonly string[],
    ) {
      const trigger = screen.getByRole('combobox', { name })
      expect(
        trigger,
        `settings ${id} Rendered: trigger is missing`,
      ).toBeEnabled()
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
      await user.click(trigger)
      expect(
        trigger,
        `settings ${id} Fired: click did not open the listbox`,
      ).toHaveAttribute('aria-expanded', 'true')
      const listbox = await screen.findByRole('listbox')
      const rendered = within(listbox)
        .getAllByRole('option')
        .map((option) => option.textContent?.trim())
      expect(
        rendered,
        `settings ${id} Effect: listbox does not expose its exact option set`,
      ).toEqual(optionNames)
      await user.keyboard('{Escape}')
      exercised.add(id)
    }

    await exerciseTrigger('Theme', 'trigger/theme', ['Light', 'Dark', 'System'])
    await exerciseTrigger('Table density', 'trigger/density', [
      'Comfortable',
      'Compact',
    ])
    await exerciseTrigger('Language', 'trigger/language', [
      'English',
      'Español',
      '中文',
      '日本語',
      'Deutsch',
      'Русский',
      'Français',
    ])

    async function chooseTheme(code: Theme, label: string) {
      const id = `theme/${code}`
      const trigger = screen.getByRole('combobox', { name: 'Theme' })
      await user.click(trigger)
      const option = await screen.findByRole('option', { name: label })
      expect(option, `settings ${id} Rendered: option is missing`).toBeEnabled()
      await user.click(option)
      expect(
        useThemeStore.getState().theme,
        `settings ${id} Fired: setTheme did not receive ${code}`,
      ).toBe(code)
      expect(
        localStorage.getItem('olivares.theme'),
        `settings ${id} Effect: logical theme was not persisted`,
      ).toBe(code)
      expect(
        document.documentElement.classList.contains('dark'),
        `settings ${id} Effect: document theme does not match resolution`,
      ).toBe(resolveDark(code))
      exercised.add(id)
    }

    await chooseTheme('light', 'Light')
    await chooseTheme('dark', 'Dark')
    await chooseTheme('system', 'System')

    async function chooseDensity(code: Density, label: string) {
      const id = `density/${code}`
      const trigger = screen.getByRole('combobox', { name: 'Table density' })
      await user.click(trigger)
      const option = await screen.findByRole('option', { name: label })
      expect(option, `settings ${id} Rendered: option is missing`).toBeEnabled()
      await user.click(option)
      expect(
        usePreferencesStore.getState().density,
        `settings ${id} Fired: setDensity did not receive ${code}`,
      ).toBe(code)
      const persisted = JSON.parse(
        localStorage.getItem('olivares.prefs') ?? '{}',
      ) as { state?: { density?: string } }
      expect(
        persisted.state?.density,
        `settings ${id} Effect: density was not persisted`,
      ).toBe(code)
      exercised.add(id)
    }

    // Comfortable is the default; visit Compact first so returning to it fires.
    await chooseDensity('compact', 'Compact')
    await chooseDensity('comfortable', 'Comfortable')

    const languages = [
      ['es', 'Español'],
      ['zh', '中文'],
      ['ja', '日本語'],
      ['de', 'Deutsch'],
      ['ru', 'Русский'],
      ['fr', 'Français'],
      ['en', 'English'],
    ] as const

    for (const [code, label] of languages) {
      const id = `language/${code}`
      const trigger = document.getElementById('language')
      expect(
        trigger,
        `settings ${id} Rendered: language trigger is missing`,
      ).toBeEnabled()
      await user.click(trigger as HTMLElement)
      const option = await screen.findByRole('option', { name: label })
      expect(option, `settings ${id} Rendered: option is missing`).toBeEnabled()
      await user.click(option)
      await waitFor(() =>
        expect(
          i18n.resolvedLanguage,
          `settings ${id} Fired: setLanguage did not resolve ${code}`,
        ).toBe(code),
      )
      expect(
        localStorage.getItem('olivares.lang'),
        `settings ${id} Effect: language was not persisted`,
      ).toBe(code)
      expect(
        document.documentElement.lang,
        `settings ${id} Effect: document language was not synchronized`,
      ).toBe(code)
      exercised.add(id)
    }

    expect(
      [...exercised].sort(),
      'settings control ledger: Rendered/Fired/Effect did not account for exactly 18 controls',
    ).toEqual([...CONTROL_IDS].sort())
  })
})
