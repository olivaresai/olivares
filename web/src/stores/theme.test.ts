// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { beforeEach, describe, expect, it } from 'vitest'
import { readStoredThemeForTest, resolveDark, useThemeStore } from './theme'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.classList.remove('dark')
})

describe('theme store', () => {
  it('applies the dark class and persists the raw key on setTheme', () => {
    useThemeStore.getState().setTheme('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(localStorage.getItem('olivares.theme')).toBe('dark')
    expect(useThemeStore.getState().resolved).toBe('dark')

    useThemeStore.getState().setTheme('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(localStorage.getItem('olivares.theme')).toBe('light')
    expect(useThemeStore.getState().resolved).toBe('light')
  })

  it('persists the raw string under the same key the no-FOUC bootstrap reads', () => {
    useThemeStore.getState().setTheme('system')
    // NOT a JSON-wrapped value — index.html reads it verbatim before any JS bundle.
    expect(localStorage.getItem('olivares.theme')).toBe('system')
  })

  it('resolveDark mirrors the bootstrap logic', () => {
    expect(resolveDark('dark')).toBe(true)
    expect(resolveDark('light')).toBe(false)
    // 'system' depends on matchMedia, stubbed to no-match in tests → light.
    expect(resolveDark('system')).toBe(false)
  })

  it('follows the system when nothing is stored (HU-27)', () => {
    localStorage.clear()
    expect(readStoredThemeForTest()).toBe('system')
    // `matchMedia` is stubbed to no-match here: an OS asking for light gets light.
    expect(resolveDark(readStoredThemeForTest())).toBe(false)
  })

  it('the index.html bootstrap paints what the store resolves, for every stored value and OS', () => {
    const html = readFileSync(join(__dirname, '..', '..', 'index.html'), 'utf8')
    const script = /<script nonce="__CSP_NONCE__">([\s\S]*?)<\/script>/.exec(
      html,
    )?.[1]
    expect(script).toBeTruthy()
    const original = window.matchMedia
    try {
      for (const osDark of [false, true]) {
        window.matchMedia = ((query: string) => ({
          matches: osDark && query.includes('dark'),
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
        })) as unknown as typeof window.matchMedia
        for (const stored of [null, 'system', 'light', 'dark', 'garbage']) {
          localStorage.clear()
          if (stored !== null) localStorage.setItem('olivares.theme', stored)
          document.documentElement.classList.remove('dark')
          new Function(script as string)()
          const painted = document.documentElement.classList.contains('dark')
          expect({ stored, osDark, painted }).toEqual({
            stored,
            osDark,
            painted: resolveDark(readStoredThemeForTest()),
          })
        }
      }
    } finally {
      window.matchMedia = original
    }
  })

  it('an explicit light choice still wins, so parity is not a courtesy', () => {
    localStorage.setItem('olivares.theme', 'light')
    expect(readStoredThemeForTest()).toBe('light')
    expect(resolveDark('light')).toBe(false)
  })
})
