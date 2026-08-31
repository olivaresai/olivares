// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Regression guard for the breadcrumb view-id resolver. The dynamic-route case
// (`/session-viewer/$id`) is the one the matchBase change fixed: before it,
// currentViewId returned null for a RESOLVED session-viewer path, so the topbar
// breadcrumb rendered a blank BreadcrumbPage — a visible bug AND an axe
// aria-command-name violation. These assertions pin the resolver's behaviour.
import { describe, expect, it } from 'vitest'
import { currentViewId } from './topbar'

describe('currentViewId', () => {
  it('resolves a dynamic route to its registry view id (the matchBase fix)', () => {
    expect(currentViewId('/session-viewer/sess-a11y')).toBe('session-viewer')
    expect(currentViewId('/session-viewer/anything-here')).toBe(
      'session-viewer',
    )
  })

  it('resolves static routes and their subpaths', () => {
    expect(currentViewId('/inventory')).toBe('inventory')
    expect(currentViewId('/model-operations')).toBe('modelOps')
    // A deeper subpath of a static view still resolves to that view.
    expect(currentViewId('/audit/some/detail')).toBe('audit')
  })

  it('handles the root, settings, and unknown paths', () => {
    expect(currentViewId('/')).toBe('home')
    expect(currentViewId('/settings')).toBe('settings')
    expect(currentViewId('/does-not-exist')).toBeNull()
  })

  it('prefers the longest matching prefix (no shorter-path shadowing)', () => {
    // `/model-operations` must not be shadowed by `/models`.
    expect(currentViewId('/model-operations')).toBe('modelOps')
    expect(currentViewId('/models')).toBe('models')
  })
})
