// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Density and reduced motion are two switches the design tokens honor: the stylesheet
// keys the spacing tokens on `html[data-density]` and zeroes the motion tokens under
// `html[data-motion="reduce"]` (and under the operating system's reduced-motion
// preference). These cases pin the other half: the stored preference reaches the root
// element on every change, so a switch the operator sets is one the tokens can see.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it } from 'vitest'
import { applyAppearance, usePreferencesStore } from './preferences'

const root = () => document.documentElement

afterEach(() => {
  usePreferencesStore.setState({ density: 'comfortable', reduceMotion: false })
})

describe('the appearance switches reach the root element', () => {
  it('writes the density the operator chose, comfortable by default', () => {
    usePreferencesStore.getState().setDensity('comfortable')
    expect(root().dataset.density).toBe('comfortable')
    usePreferencesStore.getState().setDensity('compact')
    expect(root().dataset.density).toBe('compact')
    usePreferencesStore.getState().setDensity('comfortable')
    expect(root().dataset.density).toBe('comfortable')
  })

  it('marks reduced motion only while the operator asks for it', () => {
    expect(usePreferencesStore.getState().reduceMotion).toBe(false)
    usePreferencesStore.getState().setReduceMotion(true)
    expect(root().dataset.motion).toBe('reduce')
    usePreferencesStore.getState().setReduceMotion(false)
    expect(root().hasAttribute('data-motion')).toBe(false)
  })

  it('applies a stored preference without waiting for a change', () => {
    root().removeAttribute('data-density')
    root().removeAttribute('data-motion')
    applyAppearance({ density: 'compact', reduceMotion: true })
    expect(root().dataset.density).toBe('compact')
    expect(root().dataset.motion).toBe('reduce')
  })

  it('keeps a stored blob without the new field at full motion', () => {
    // A preference saved before the switch existed has no `reduceMotion`: it rehydrates to
    // false, never to an undefined that a strict comparison would read as "unknown".
    const merge = usePreferencesStore.persist.getOptions().merge!
    const merged = merge(
      { density: 'compact' },
      usePreferencesStore.getState(),
    ) as ReturnType<typeof usePreferencesStore.getState>
    expect(merged.reduceMotion).toBe(false)
    expect(merged.density).toBe('compact')
  })

  it('stops animations under the setting exactly as under the system preference', () => {
    const css = readFileSync('src/index.css', 'utf8').replace(
      /\/\*[\s\S]*?\*\//g,
      '',
    )
    expect(css).toMatch(
      /\[data-motion='reduce'\] \*,\s*\[data-motion='reduce'\] ::before,\s*\[data-motion='reduce'\] ::after\s*\{\s*animation-duration: 0\.01ms !important;/,
    )
    expect(css).toContain('translateY(var(--motion-shift))')
  })
})
