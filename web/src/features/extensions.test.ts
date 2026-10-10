// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  ANONYMOUS_EXTENSION_ROUTES,
  ANONYMOUS_FEATURE_EXTENSIONS,
  EXTENSION_ROUTES,
  FEATURE_EXTENSIONS,
  PANEL_EXTENSIONS,
} from './extensions'

describe('default console extensions', () => {
  it('contains no extension view or route', () => {
    expect(FEATURE_EXTENSIONS).toEqual([])
    expect(EXTENSION_ROUTES).toEqual([])
    expect(Object.isFrozen(FEATURE_EXTENSIONS)).toBe(true)
    expect(Object.isFrozen(EXTENSION_ROUTES)).toBe(true)
  })

  it('contains no panel inside a public page', () => {
    expect(PANEL_EXTENSIONS).toEqual({
      identityLoginCards: [],
      identityStepUpMethods: [],
      sessionPanels: [],
      governanceTabs: [],
      capabilitiesTabs: [],
      complianceTabs: [],
      reportingCards: [],
      licenseCards: [],
      scopesCards: [],
    })
    expect(Object.isFrozen(PANEL_EXTENSIONS)).toBe(true)
    for (const panels of Object.values(PANEL_EXTENSIONS)) {
      expect(Object.isFrozen(panels)).toBe(true)
    }
  })

  it('contains no anonymous route or login choice', () => {
    expect(ANONYMOUS_FEATURE_EXTENSIONS).toEqual([])
    expect(ANONYMOUS_EXTENSION_ROUTES).toEqual([])
    expect(Object.isFrozen(ANONYMOUS_FEATURE_EXTENSIONS)).toBe(true)
    expect(Object.isFrozen(ANONYMOUS_EXTENSION_ROUTES)).toBe(true)
  })
})
