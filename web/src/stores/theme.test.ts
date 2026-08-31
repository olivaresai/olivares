// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it } from 'vitest'
import { resolveDark, useThemeStore } from './theme'

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
})
